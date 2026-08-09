package skillsync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/tjbdwanghaibo/cube-core/syncstream"
	"github.com/tjbdwanghaibo/cube-skill/skillv2"
)

var (
	ErrObserverMismatch = errors.New("skillsync: packet observer mismatch")
	ErrSchemaMismatch   = errors.New("skillsync: packet schema mismatch")
	ErrSequenceGap      = errors.New("skillsync: packet sequence gap")
	ErrPacketShape      = errors.New("skillsync: packet shape does not match record kind")
)

type ManifestConsumer interface {
	ApplyManifest(int64, skillv2.PresentationPlan) error
}

type StateConsumer interface {
	ApplyStateSnapshot(int64, skillv2.RuntimeStateSnapshot) error
	ApplyStateDelta(int64, skillv2.StateEvent) error
}

type PresentationConsumer interface {
	ResetPresentation(int64, PresentationReset) error
	ApplyPresentation(int64, skillv2.PresentationEvent) error
}

type ApplierOptions struct {
	Observer      syncstream.Observer
	SchemaVersion uint32
	Manifest      ManifestConsumer
	State         StateConsumer
	Presentation  PresentationConsumer
}

type ApplyResult struct {
	Applied   bool
	Duplicate bool
	Sequence  uint64
}

// Applier validates observer isolation, schema and stream chains before any
// consumer callback. Full packets reset a chain; stale packets are idempotent.
type Applier struct {
	mutex                sync.Mutex
	observer             syncstream.Observer
	schemaVersion        uint32
	manifestConsumer     ManifestConsumer
	stateConsumer        StateConsumer
	presentationConsumer PresentationConsumer
	sequences            map[syncstream.Stream]uint64
	stateEvents          map[int64]uint64
	presentationEvents   map[int64]uint64
	manifests            map[string]skillv2.PresentationPlan
}

func NewApplier(options ApplierOptions) (*Applier, error) {
	if options.SchemaVersion == 0 {
		return nil, ErrSchemaVersionRequired
	}
	return &Applier{
		observer: options.Observer, schemaVersion: options.SchemaVersion,
		manifestConsumer: options.Manifest, stateConsumer: options.State, presentationConsumer: options.Presentation,
		sequences: make(map[syncstream.Stream]uint64), stateEvents: make(map[int64]uint64),
		presentationEvents: make(map[int64]uint64), manifests: make(map[string]skillv2.PresentationPlan),
	}, nil
}

func (applier *Applier) Apply(packet syncstream.Packet) (ApplyResult, error) {
	applier.mutex.Lock()
	defer applier.mutex.Unlock()
	if packet.Observer != applier.observer {
		return ApplyResult{}, ErrObserverMismatch
	}
	if packet.SchemaVersion != applier.schemaVersion {
		return ApplyResult{}, ErrSchemaMismatch
	}
	if packet.Stream.Topic == "" || packet.Sequence == 0 {
		return ApplyResult{}, ErrPacketShape
	}
	current := applier.sequences[packet.Stream]
	if packet.Sequence <= current {
		return ApplyResult{Duplicate: true, Sequence: current}, nil
	}
	if packet.Full {
		if packet.BaseSequence != 0 {
			return ApplyResult{}, ErrPacketShape
		}
	} else if packet.BaseSequence != current || packet.Sequence != current+1 {
		return ApplyResult{}, fmt.Errorf("%w: current=%d base=%d sequence=%d", ErrSequenceGap, current, packet.BaseSequence, packet.Sequence)
	}
	var header Header
	if err := json.Unmarshal(packet.Payload, &header); err != nil {
		return ApplyResult{}, fmt.Errorf("%w: %v", ErrRecordInvalid, err)
	}
	if header.SchemaVersion != packet.SchemaVersion {
		return ApplyResult{}, ErrSchemaMismatch
	}
	if err := applier.applyRecord(packet, header); err != nil {
		return ApplyResult{}, err
	}
	applier.sequences[packet.Stream] = packet.Sequence
	return ApplyResult{Applied: true, Sequence: packet.Sequence}, nil
}

func (applier *Applier) applyRecord(packet syncstream.Packet, header Header) error {
	switch packet.Stream.Topic {
	case TopicManifest:
		if !packet.Full || header.Kind != RecordManifest {
			return ErrPacketShape
		}
		var record ManifestRecord
		if err := decodeStrict(packet.Payload, &record); err != nil {
			return err
		}
		digest := record.Plan.Identity.PresentationDigest
		if digest == "" || digest != record.PresentationDigest {
			return fmt.Errorf("%w: manifest presentation digest", ErrRecordInvalid)
		}
		if applier.manifestConsumer != nil {
			if err := applier.manifestConsumer.ApplyManifest(packet.Stream.Key, record.Plan); err != nil {
				return err
			}
		}
		applier.manifests[digest] = record.Plan
		return nil
	case TopicState:
		var record StateRecord
		if err := decodeStrict(packet.Payload, &record); err != nil {
			return err
		}
		switch record.Kind {
		case RecordStateFull:
			if !packet.Full || record.Snapshot == nil || record.Delta != nil {
				return ErrPacketShape
			}
			if applier.stateConsumer != nil {
				if err := applier.stateConsumer.ApplyStateSnapshot(packet.Stream.Key, *record.Snapshot); err != nil {
					return err
				}
			}
			applier.stateEvents[packet.Stream.Key] = record.Snapshot.LatestStateEventSequence
		case RecordStateDelta:
			if packet.Full || record.Delta == nil || record.Snapshot != nil || record.Delta.Sequence == 0 || record.Delta.Event.Kind == "" {
				return ErrPacketShape
			}
			if record.Delta.Sequence <= applier.stateEvents[packet.Stream.Key] {
				return nil
			}
			if applier.stateConsumer != nil {
				if err := applier.stateConsumer.ApplyStateDelta(packet.Stream.Key, *record.Delta); err != nil {
					return err
				}
			}
			applier.stateEvents[packet.Stream.Key] = record.Delta.Sequence
		default:
			return ErrPacketShape
		}
		return nil
	case TopicPresentation:
		var record PresentationRecord
		if err := decodeStrict(packet.Payload, &record); err != nil {
			return err
		}
		switch record.Kind {
		case RecordPresentationReset:
			if !packet.Full || record.Reset == nil || record.Event != nil {
				return ErrPacketShape
			}
			if applier.presentationConsumer != nil {
				if err := applier.presentationConsumer.ResetPresentation(packet.Stream.Key, *record.Reset); err != nil {
					return err
				}
			}
			applier.presentationEvents[packet.Stream.Key] = record.Reset.LatestPresentationSequence
		case RecordPresentation:
			if packet.Full || record.Event == nil || record.Reset != nil || record.Event.Sequence == 0 || record.Event.Kind == "" {
				return ErrPacketShape
			}
			if record.Event.PresentationDigest != "" {
				if _, ok := applier.manifests[record.Event.PresentationDigest]; !ok {
					return ErrManifestMissing
				}
			}
			if record.Event.Sequence <= applier.presentationEvents[packet.Stream.Key] {
				return nil
			}
			if applier.presentationConsumer != nil {
				if err := applier.presentationConsumer.ApplyPresentation(packet.Stream.Key, *record.Event); err != nil {
					return err
				}
			}
			applier.presentationEvents[packet.Stream.Key] = record.Event.Sequence
		default:
			return ErrPacketShape
		}
		return nil
	default:
		return ErrTopicUnsupported
	}
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", ErrRecordInvalid, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing JSON content", ErrRecordInvalid)
	}
	return nil
}

func (applier *Applier) Sequence(stream syncstream.Stream) uint64 {
	applier.mutex.Lock()
	defer applier.mutex.Unlock()
	return applier.sequences[stream]
}
