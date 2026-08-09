package skillsync

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/tjbdwanghaibo/cube-core/syncstream"
)

var ErrOutboxStoreRequired = errors.New("skillsync: durable outbox store is required")

type OutboxStore interface {
	Load() ([]syncstream.Packet, error)
	Put(syncstream.Packet) error
	Delete(syncstream.Observer, syncstream.Stream, uint64, uint64) error
}

type pendingID struct {
	observer syncstream.Observer
	stream   syncstream.Stream
	epoch    uint64
	sequence uint64
}

type pendingPacket struct {
	packet      syncstream.Packet
	attempts    uint32
	nextAttempt time.Time
}

type OutboxOptions struct {
	Store             OutboxStore
	RequireDurable    bool
	AckRetryInterval  time.Duration
	FailureRetryDelay time.Duration
	MaxRetryDelay     time.Duration
}

type OutboxMetrics struct {
	Pending          int
	PublishAttempts  uint64
	PublishSuccesses uint64
	PublishFailures  uint64
	Acknowledged     uint64
}

// Outbox retains every appended packet until client ACK, not merely until the
// transport accepts it. This closes the undetectable "last publish failed"
// window.
type Outbox struct {
	mutex        sync.Mutex
	store        OutboxStore
	ackRetry     time.Duration
	failureRetry time.Duration
	maxRetry     time.Duration
	pending      map[pendingID]*pendingPacket
	metrics      OutboxMetrics
}

func NewOutbox(options OutboxOptions) (*Outbox, error) {
	if options.RequireDurable && options.Store == nil {
		return nil, ErrOutboxStoreRequired
	}
	if options.AckRetryInterval <= 0 {
		options.AckRetryInterval = 5 * time.Second
	}
	if options.FailureRetryDelay <= 0 {
		options.FailureRetryDelay = 100 * time.Millisecond
	}
	if options.MaxRetryDelay <= 0 {
		options.MaxRetryDelay = 30 * time.Second
	}
	box := &Outbox{store: options.Store, ackRetry: options.AckRetryInterval, failureRetry: options.FailureRetryDelay, maxRetry: options.MaxRetryDelay, pending: make(map[pendingID]*pendingPacket)}
	if options.Store != nil {
		packets, err := options.Store.Load()
		if err != nil {
			return nil, err
		}
		for _, packet := range packets {
			if packet.Stream.Topic == "" || packet.Epoch == 0 || packet.Sequence == 0 {
				return nil, ErrRecordInvalid
			}
			box.pending[packetID(packet)] = &pendingPacket{packet: packet.Clone()}
		}
	}
	return box, nil
}

func packetID(packet syncstream.Packet) pendingID {
	return pendingID{observer: packet.Observer, stream: packet.Stream, epoch: packet.Epoch, sequence: packet.Sequence}
}

func (box *Outbox) Put(packet syncstream.Packet) error {
	if box == nil || packet.Stream.Topic == "" || packet.Epoch == 0 || packet.Sequence == 0 {
		return ErrRecordInvalid
	}
	box.mutex.Lock()
	defer box.mutex.Unlock()
	id := packetID(packet)
	if _, exists := box.pending[id]; exists {
		return nil
	}
	if box.store != nil {
		if err := box.store.Put(packet.Clone()); err != nil {
			return err
		}
	}
	box.pending[id] = &pendingPacket{packet: packet.Clone()}
	return nil
}

// Reconcile repairs the crash window between a committed History append and
// outbox persistence. Every retained packet newer than ACK becomes pending.
func (box *Outbox) Reconcile(snapshot syncstream.HistorySnapshot) error {
	for _, stream := range snapshot.Streams {
		for _, packet := range stream.Packets {
			if packet.Sequence > stream.Acked {
				if err := box.Put(packet); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (box *Outbox) Acknowledge(observer syncstream.Observer, stream syncstream.Stream, epoch, sequence uint64) error {
	if box == nil {
		return nil
	}
	box.mutex.Lock()
	defer box.mutex.Unlock()
	ids := make([]pendingID, 0)
	for id := range box.pending {
		if id.observer == observer && id.stream == stream && id.epoch == epoch && id.sequence <= sequence {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].sequence < ids[j].sequence })
	for _, id := range ids {
		if box.store != nil {
			if err := box.store.Delete(id.observer, id.stream, id.epoch, id.sequence); err != nil {
				return err
			}
		}
		delete(box.pending, id)
		box.metrics.Acknowledged++
	}
	return nil
}

func (box *Outbox) DiscardObserver(observer syncstream.Observer) error {
	if box == nil {
		return nil
	}
	box.mutex.Lock()
	defer box.mutex.Unlock()
	ids := make([]pendingID, 0)
	for id := range box.pending {
		if id.observer == observer {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].stream.Topic != ids[j].stream.Topic {
			return ids[i].stream.Topic < ids[j].stream.Topic
		}
		if ids[i].stream.Key != ids[j].stream.Key {
			return ids[i].stream.Key < ids[j].stream.Key
		}
		return ids[i].sequence < ids[j].sequence
	})
	for _, id := range ids {
		if box.store != nil {
			if err := box.store.Delete(id.observer, id.stream, id.epoch, id.sequence); err != nil {
				return err
			}
		}
		delete(box.pending, id)
	}
	return nil
}

func (box *Outbox) due(now time.Time, observer *syncstream.Observer, stream *syncstream.Stream) []syncstream.Packet {
	box.mutex.Lock()
	defer box.mutex.Unlock()
	packets := make([]syncstream.Packet, 0)
	for _, entry := range box.pending {
		if observer != nil && entry.packet.Observer != *observer || stream != nil && entry.packet.Stream != *stream {
			continue
		}
		if entry.nextAttempt.IsZero() || !entry.nextAttempt.After(now) {
			packets = append(packets, entry.packet.Clone())
		}
	}
	sort.Slice(packets, func(i, j int) bool {
		if packets[i].Observer != packets[j].Observer {
			return observerLess(packets[i].Observer, packets[j].Observer)
		}
		if packets[i].Stream.Topic != packets[j].Stream.Topic {
			left, right := topicPriority(packets[i].Stream.Topic), topicPriority(packets[j].Stream.Topic)
			if left != right {
				return left < right
			}
			return packets[i].Stream.Topic < packets[j].Stream.Topic
		}
		if packets[i].Stream.Key != packets[j].Stream.Key {
			return packets[i].Stream.Key < packets[j].Stream.Key
		}
		return packets[i].Sequence < packets[j].Sequence
	})
	return packets
}

func topicPriority(topic string) int {
	switch topic {
	case TopicManifest:
		return 0
	case TopicState:
		return 1
	case TopicPresentation:
		return 2
	default:
		return 3
	}
}

func observerLess(left, right syncstream.Observer) bool {
	if left.Scope != right.Scope {
		return left.Scope < right.Scope
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	if left.ID != right.ID {
		return left.ID < right.ID
	}
	return left.Session < right.Session
}

// PublishDue invokes publisher without holding the outbox lock. Successful
// packets remain pending until ACK and are periodically retried to repair a
// lost final delivery.
func (box *Outbox) PublishDue(publisher PacketPublisher, now time.Time, observer *syncstream.Observer, stream *syncstream.Stream) error {
	if publisher == nil {
		return ErrPublisherRequired
	}
	packets := box.due(now, observer, stream)
	var firstError error
	for _, packet := range packets {
		err := publisher.Publish(packet.Clone())
		box.mutex.Lock()
		entry := box.pending[packetID(packet)]
		if entry != nil {
			entry.attempts++
			box.metrics.PublishAttempts++
			if err == nil {
				box.metrics.PublishSuccesses++
				entry.nextAttempt = now.Add(box.ackRetry)
			} else {
				box.metrics.PublishFailures++
				delay := box.failureRetry
				for attempt := uint32(1); attempt < entry.attempts && delay < box.maxRetry; attempt++ {
					delay *= 2
				}
				if delay > box.maxRetry {
					delay = box.maxRetry
				}
				entry.nextAttempt = now.Add(delay)
			}
		}
		box.mutex.Unlock()
		if err != nil && firstError == nil {
			firstError = err
		}
	}
	return firstError
}

func (box *Outbox) PublishNow(publisher PacketPublisher, now time.Time, observer *syncstream.Observer, stream *syncstream.Stream) error {
	if box == nil {
		return nil
	}
	box.mutex.Lock()
	for _, entry := range box.pending {
		if observer != nil && entry.packet.Observer != *observer || stream != nil && entry.packet.Stream != *stream {
			continue
		}
		entry.nextAttempt = time.Time{}
	}
	box.mutex.Unlock()
	return box.PublishDue(publisher, now, observer, stream)
}

func (box *Outbox) Metrics() OutboxMetrics {
	if box == nil {
		return OutboxMetrics{}
	}
	box.mutex.Lock()
	defer box.mutex.Unlock()
	metrics := box.metrics
	metrics.Pending = len(box.pending)
	return metrics
}
