package skillsync

import (
	"errors"
	"fmt"
	"sync"

	"github.com/tjbdwanghaibo/cube-core/syncstream"
	"github.com/tjbdwanghaibo/cube-skill/skillv2"
)

var (
	ErrRuntimeRequired    = errors.New("skillsync: runtime is required")
	ErrHistoryRequired    = errors.New("skillsync: history is required")
	ErrPublisherRequired  = errors.New("skillsync: publisher is required")
	ErrVisibilityRequired = errors.New("skillsync: visibility policy is required")
	ErrManifestMissing    = errors.New("skillsync: presentation manifest is missing")
	ErrTopicUnsupported   = errors.New("skillsync: topic is unsupported")
)

type PacketPublisher interface {
	Publish(syncstream.Packet) error
}

// VisibilityPolicy makes per-observer authorization an explicit server-side
// dependency. Implementations must return detached snapshots if they mutate
// slices while filtering.
type VisibilityPolicy interface {
	FilterStateSnapshot(syncstream.Observer, skillv2.RuntimeStateSnapshot) skillv2.RuntimeStateSnapshot
	AllowStateEvent(syncstream.Observer, skillv2.StateEvent) bool
	AllowPresentation(syncstream.Observer, skillv2.PresentationEvent) bool
}

// AllowAllVisibility is intentionally explicit; passing nil is rejected.
type AllowAllVisibility struct{}

func (AllowAllVisibility) FilterStateSnapshot(_ syncstream.Observer, snapshot skillv2.RuntimeStateSnapshot) skillv2.RuntimeStateSnapshot {
	return snapshot
}
func (AllowAllVisibility) AllowStateEvent(syncstream.Observer, skillv2.StateEvent) bool { return true }
func (AllowAllVisibility) AllowPresentation(syncstream.Observer, skillv2.PresentationEvent) bool {
	return true
}

type CoordinatorOptions struct {
	Runtime            *skillv2.Runtime
	History            *syncstream.History
	Publisher          PacketPublisher
	Projector          Projector
	Visibility         VisibilityPolicy
	MaxPacketsPerFlush int
}

type observerKey struct {
	observer syncstream.Observer
	key      int64
}

type sourceCursor struct {
	state        uint64
	presentation uint64
}

type CoordinatorMetrics struct {
	Published          uint64
	PublishFailures    uint64
	Filtered           uint64
	SnapshotRecoveries uint64
}

// Coordinator owns source cursors and converts retained Runtime events into
// recoverable syncstream packets. A publish failure never loses a source event:
// once appended, it remains recoverable from History.
type Coordinator struct {
	mutex      sync.Mutex
	runtime    *skillv2.Runtime
	history    *syncstream.History
	publisher  PacketPublisher
	projector  Projector
	visibility VisibilityPolicy
	maxPackets int
	cursors    map[observerKey]sourceCursor
	plans      map[int64]skillv2.PresentationPlan
	metrics    CoordinatorMetrics
}

func NewCoordinator(options CoordinatorOptions) (*Coordinator, error) {
	if options.Runtime == nil {
		return nil, ErrRuntimeRequired
	}
	if options.History == nil {
		return nil, ErrHistoryRequired
	}
	if options.Publisher == nil {
		return nil, ErrPublisherRequired
	}
	if options.Projector.SchemaVersion == 0 {
		return nil, ErrSchemaVersionRequired
	}
	if options.Visibility == nil {
		return nil, ErrVisibilityRequired
	}
	if options.MaxPacketsPerFlush <= 0 {
		options.MaxPacketsPerFlush = 256
	}
	return &Coordinator{
		runtime: options.Runtime, history: options.History, publisher: options.Publisher,
		projector: options.Projector, visibility: options.Visibility, maxPackets: options.MaxPacketsPerFlush,
		cursors: make(map[observerKey]sourceCursor), plans: make(map[int64]skillv2.PresentationPlan),
	}, nil
}

func (coordinator *Coordinator) RegisterProgram(key int64, program *skillv2.Program) {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	coordinator.plans[key] = skillv2.InspectPresentationPlan(program)
}

func (coordinator *Coordinator) PublishManifest(observer syncstream.Observer, key int64) error {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	plan, ok := coordinator.plans[key]
	if !ok {
		return ErrManifestMissing
	}
	packet, err := coordinator.projector.ManifestPacket(observer, key, plan)
	if err != nil {
		return err
	}
	return coordinator.appendAndPublish(packet)
}

func (coordinator *Coordinator) PublishSnapshot(observer syncstream.Observer, key int64) error {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	snapshot := coordinator.visibility.FilterStateSnapshot(observer, coordinator.runtime.StateSnapshot())
	packet, err := coordinator.projector.StateSnapshotPacket(observer, key, snapshot)
	if err != nil {
		return err
	}
	packet, err = coordinator.history.Append(packet)
	if err != nil {
		return err
	}
	cursor := coordinator.cursors[observerKey{observer: observer, key: key}]
	cursor.state = snapshot.LatestStateEventSequence
	coordinator.cursors[observerKey{observer: observer, key: key}] = cursor
	return coordinator.publish(packet)
}

// Flush publishes up to MaxPacketsPerFlush across state and presentation while
// retaining independent source cursors for each observer.
func (coordinator *Coordinator) Flush(observer syncstream.Observer, key int64) error {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	cursorKey := observerKey{observer: observer, key: key}
	cursor := coordinator.cursors[cursorKey]
	remaining := coordinator.maxPackets

	state := coordinator.runtime.StateEvents(cursor.state, remaining)
	if state.CursorExpired {
		snapshot := coordinator.visibility.FilterStateSnapshot(observer, coordinator.runtime.StateSnapshot())
		packet, err := coordinator.projector.StateSnapshotPacket(observer, key, snapshot)
		if err != nil {
			return err
		}
		packet, err = coordinator.history.Append(packet)
		if err != nil {
			return err
		}
		cursor.state = snapshot.LatestStateEventSequence
		coordinator.cursors[cursorKey] = cursor
		coordinator.metrics.SnapshotRecoveries++
		if err := coordinator.publish(packet); err != nil {
			return err
		}
		remaining--
		if remaining == 0 {
			return nil
		}
	} else {
		for _, event := range state.Events {
			if !coordinator.visibility.AllowStateEvent(observer, event) {
				cursor.state = event.Sequence
				coordinator.cursors[cursorKey] = cursor
				coordinator.metrics.Filtered++
				continue
			}
			packet, err := coordinator.projector.StateDeltaPacket(observer, key, event)
			if err != nil {
				return err
			}
			packet, err = coordinator.history.Append(packet)
			if err != nil {
				return err
			}
			cursor.state = event.Sequence
			coordinator.cursors[cursorKey] = cursor
			remaining--
			if err := coordinator.publish(packet); err != nil {
				return err
			}
			if remaining == 0 {
				return nil
			}
		}
	}

	presentation := coordinator.runtime.PollPresentation(cursor.presentation, remaining)
	if presentation.CursorExpired {
		snapshot := coordinator.runtime.StateSnapshot()
		packet, err := coordinator.projector.PresentationResetPacket(observer, key, snapshot)
		if err != nil {
			return err
		}
		packet, err = coordinator.history.Append(packet)
		if err != nil {
			return err
		}
		cursor.presentation = snapshot.LatestPresentationSequence
		coordinator.cursors[cursorKey] = cursor
		coordinator.metrics.SnapshotRecoveries++
		return coordinator.publish(packet)
	}
	for _, event := range presentation.Events {
		if !coordinator.visibility.AllowPresentation(observer, event) {
			cursor.presentation = event.Sequence
			coordinator.cursors[cursorKey] = cursor
			coordinator.metrics.Filtered++
			continue
		}
		packet, err := coordinator.projector.PresentationPacket(observer, key, event)
		if err != nil {
			return err
		}
		packet, err = coordinator.history.Append(packet)
		if err != nil {
			return err
		}
		cursor.presentation = event.Sequence
		coordinator.cursors[cursorKey] = cursor
		if err := coordinator.publish(packet); err != nil {
			return err
		}
	}
	return nil
}

// Acknowledge records client progress for diagnostics and recovery decisions.
func (coordinator *Coordinator) Acknowledge(observer syncstream.Observer, stream syncstream.Stream, sequence uint64) error {
	return coordinator.history.Acknowledge(observer, stream, sequence)
}

type snapshotProviderFunc func(syncstream.ResyncRequest) (syncstream.Packet, error)

func (provider snapshotProviderFunc) Snapshot(request syncstream.ResyncRequest) (syncstream.Packet, error) {
	return provider(request)
}

// Recover replays retained packets or automatically produces a topic-specific
// full snapshot, then publishes the result in order.
func (coordinator *Coordinator) Recover(request syncstream.ResyncRequest) (syncstream.ResyncResult, error) {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	result, err := coordinator.history.Recover(request, snapshotProviderFunc(coordinator.snapshotPacket))
	if err != nil {
		return result, err
	}
	if result.Reason != syncstream.ResyncNone && len(result.Packets) == 1 && result.Packets[0].Full {
		coordinator.metrics.SnapshotRecoveries++
	}
	for _, packet := range result.Packets {
		if err := coordinator.publish(packet); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (coordinator *Coordinator) snapshotPacket(request syncstream.ResyncRequest) (syncstream.Packet, error) {
	switch request.Stream.Topic {
	case TopicManifest:
		plan, ok := coordinator.plans[request.Stream.Key]
		if !ok {
			return syncstream.Packet{}, ErrManifestMissing
		}
		return coordinator.projector.ManifestPacket(request.Observer, request.Stream.Key, plan)
	case TopicState:
		snapshot := coordinator.visibility.FilterStateSnapshot(request.Observer, coordinator.runtime.StateSnapshot())
		return coordinator.projector.StateSnapshotPacket(request.Observer, request.Stream.Key, snapshot)
	case TopicPresentation:
		return coordinator.projector.PresentationResetPacket(request.Observer, request.Stream.Key, coordinator.runtime.StateSnapshot())
	default:
		return syncstream.Packet{}, fmt.Errorf("%w: %s", ErrTopicUnsupported, request.Stream.Topic)
	}
}

func (coordinator *Coordinator) appendAndPublish(packet syncstream.Packet) error {
	packet, err := coordinator.history.Append(packet)
	if err != nil {
		return err
	}
	return coordinator.publish(packet)
}

func (coordinator *Coordinator) publish(packet syncstream.Packet) error {
	if err := coordinator.publisher.Publish(packet.Clone()); err != nil {
		coordinator.metrics.PublishFailures++
		return err
	}
	coordinator.metrics.Published++
	return nil
}

func (coordinator *Coordinator) Metrics() CoordinatorMetrics {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	return coordinator.metrics
}
