package skillsync

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/tjbdwanghaibo/cube-core/syncstream"
)

var (
	ErrOutboxStoreRequired    = errors.New("skillsync: durable outbox store is required")
	ErrOutboxCapacityExceeded = errors.New("skillsync: outbox capacity exceeded")
	ErrOutboxPendingTooOld    = errors.New("skillsync: oldest outbox packet exceeds maximum age")
)

type OutboxStore interface {
	Load() ([]syncstream.Packet, error)
	Put(syncstream.Packet) error
	Delete(syncstream.Observer, syncstream.Stream, uint64, uint64) error
}

type OutboxDelete struct {
	Observer syncstream.Observer
	Stream   syncstream.Stream
	Epoch    uint64
	Sequence uint64
}

// BatchOutboxStore can make range ACK deletion atomic. Implementations must
// either delete every key or none of them.
type BatchOutboxStore interface {
	DeleteBatch([]OutboxDelete) error
}

type OutboxRecord struct {
	Packet      syncstream.Packet `json:"packet"`
	CreatedAt   time.Time         `json:"created_at"`
	Attempts    uint32            `json:"attempts,omitempty"`
	NextAttempt time.Time         `json:"next_attempt,omitempty"`
}

// RecordOutboxStore persists retry metadata and packet age across restarts.
type RecordOutboxStore interface {
	LoadRecords() ([]OutboxRecord, error)
	PutRecord(OutboxRecord) error
}

// BatchRecordOutboxStore persists a group atomically. Stores backed by a
// transactional database should implement it; other stores remain crash-safe
// because individual records are immutable and idempotent.
type BatchRecordOutboxStore interface {
	PutRecords([]OutboxRecord) error
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
	createdAt   time.Time
	bytes       int64
}

type OutboxOptions struct {
	Store               OutboxStore
	RequireDurable      bool
	AckRetryInterval    time.Duration
	FailureRetryDelay   time.Duration
	MaxRetryDelay       time.Duration
	MaxPendingPackets   int
	MaxPendingBytes     int64
	MaxPendingPerStream int
	MaxPendingAge       time.Duration
}

type OutboxMetrics struct {
	Pending          int
	PendingBytes     int64
	OldestPendingAge time.Duration
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
	maxPackets   int
	maxBytes     int64
	maxPerStream int
	maxAge       time.Duration
	pendingBytes int64
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
	if options.MaxPendingPackets <= 0 {
		options.MaxPendingPackets = 100000
	}
	if options.MaxPendingBytes <= 0 {
		options.MaxPendingBytes = 256 << 20
	}
	if options.MaxPendingPerStream <= 0 {
		options.MaxPendingPerStream = 4096
	}
	if options.MaxPendingAge <= 0 {
		options.MaxPendingAge = 24 * time.Hour
	}
	box := &Outbox{store: options.Store, ackRetry: options.AckRetryInterval, failureRetry: options.FailureRetryDelay, maxRetry: options.MaxRetryDelay, maxPackets: options.MaxPendingPackets, maxBytes: options.MaxPendingBytes, maxPerStream: options.MaxPendingPerStream, maxAge: options.MaxPendingAge, pending: make(map[pendingID]*pendingPacket)}
	if options.Store != nil {
		records, err := loadOutboxRecords(options.Store)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			packet := record.Packet
			if packet.Stream.Topic == "" || packet.Epoch == 0 || packet.Sequence == 0 {
				return nil, ErrRecordInvalid
			}
			size, err := outboxPacketBytes(packet)
			if err != nil {
				return nil, err
			}
			if record.CreatedAt.IsZero() {
				record.CreatedAt = time.Now()
			}
			id := packetID(packet)
			if _, duplicate := box.pending[id]; duplicate {
				return nil, ErrRecordInvalid
			}
			box.pending[id] = &pendingPacket{packet: packet.Clone(), attempts: record.Attempts, nextAttempt: record.NextAttempt, createdAt: record.CreatedAt, bytes: size}
			box.pendingBytes += size
		}
		if err := box.capacityError(time.Now(), nil, 0); err != nil {
			return nil, err
		}
	}
	return box, nil
}

func packetID(packet syncstream.Packet) pendingID {
	return pendingID{observer: packet.Observer, stream: packet.Stream, epoch: packet.Epoch, sequence: packet.Sequence}
}

func loadOutboxRecords(store OutboxStore) ([]OutboxRecord, error) {
	if records, ok := store.(RecordOutboxStore); ok {
		return records.LoadRecords()
	}
	packets, err := store.Load()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	result := make([]OutboxRecord, len(packets))
	for index := range packets {
		result[index] = OutboxRecord{Packet: packets[index], CreatedAt: now}
	}
	return result, nil
}

func outboxPacketBytes(packet syncstream.Packet) (int64, error) {
	data, err := json.Marshal(packet)
	return int64(len(data)), err
}

func samePendingStream(id pendingID, packet syncstream.Packet) bool {
	return id.observer == packet.Observer && id.stream == packet.Stream && id.epoch == packet.Epoch
}

func (box *Outbox) capacityError(now time.Time, packet *syncstream.Packet, additionalBytes int64) error {
	additionalPackets := 0
	if packet != nil {
		additionalPackets = 1
	}
	if len(box.pending)+additionalPackets > box.maxPackets || box.pendingBytes+additionalBytes > box.maxBytes {
		return ErrOutboxCapacityExceeded
	}
	if packet != nil {
		perStream := 0
		for id := range box.pending {
			if samePendingStream(id, *packet) {
				perStream++
			}
		}
		if perStream+1 > box.maxPerStream {
			return ErrOutboxCapacityExceeded
		}
	} else {
		type streamLimitKey struct {
			observer syncstream.Observer
			stream   syncstream.Stream
			epoch    uint64
		}
		counts := make(map[streamLimitKey]int)
		for id := range box.pending {
			key := streamLimitKey{id.observer, id.stream, id.epoch}
			counts[key]++
			if counts[key] > box.maxPerStream {
				return ErrOutboxCapacityExceeded
			}
		}
	}
	if box.maxAge > 0 {
		for _, entry := range box.pending {
			if !entry.createdAt.IsZero() && now.Sub(entry.createdAt) > box.maxAge {
				return ErrOutboxPendingTooOld
			}
		}
	}
	return nil
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
	size, err := outboxPacketBytes(packet)
	if err != nil {
		return err
	}
	now := time.Now()
	if err := box.capacityError(now, &packet, size); err != nil {
		return err
	}
	if box.store != nil {
		var err error
		if records, ok := box.store.(RecordOutboxStore); ok {
			err = records.PutRecord(OutboxRecord{Packet: packet.Clone(), CreatedAt: now})
		} else {
			err = box.store.Put(packet.Clone())
		}
		if err != nil {
			return err
		}
	}
	box.pending[id] = &pendingPacket{packet: packet.Clone(), createdAt: now, bytes: size}
	box.pendingBytes += size
	return nil
}

func (box *Outbox) PutBatch(packets []syncstream.Packet) error {
	if box == nil {
		return ErrRecordInvalid
	}
	box.mutex.Lock()
	defer box.mutex.Unlock()
	now := time.Now()
	if err := box.capacityError(now, nil, 0); err != nil {
		return err
	}
	records := make([]OutboxRecord, 0, len(packets))
	entries := make([]struct {
		id    pendingID
		entry *pendingPacket
	}, 0, len(packets))
	seen := make(map[pendingID]struct{}, len(packets))
	additionalBytes := int64(0)
	additionalPerStream := make(map[struct {
		observer syncstream.Observer
		stream   syncstream.Stream
		epoch    uint64
	}]int)
	for _, packet := range packets {
		if packet.Stream.Topic == "" || packet.Epoch == 0 || packet.Sequence == 0 {
			return ErrRecordInvalid
		}
		id := packetID(packet)
		if _, exists := box.pending[id]; exists {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		size, err := outboxPacketBytes(packet)
		if err != nil {
			return err
		}
		additionalBytes += size
		key := struct {
			observer syncstream.Observer
			stream   syncstream.Stream
			epoch    uint64
		}{packet.Observer, packet.Stream, packet.Epoch}
		additionalPerStream[key]++
		record := OutboxRecord{Packet: packet.Clone(), CreatedAt: now}
		records = append(records, record)
		entries = append(entries, struct {
			id    pendingID
			entry *pendingPacket
		}{id: id, entry: &pendingPacket{packet: packet.Clone(), createdAt: now, bytes: size}})
	}
	if len(box.pending)+len(entries) > box.maxPackets || box.pendingBytes+additionalBytes > box.maxBytes {
		return ErrOutboxCapacityExceeded
	}
	for key, additional := range additionalPerStream {
		count := 0
		for id := range box.pending {
			if id.observer == key.observer && id.stream == key.stream && id.epoch == key.epoch {
				count++
			}
		}
		if count+additional > box.maxPerStream {
			return ErrOutboxCapacityExceeded
		}
	}
	if batch, ok := box.store.(BatchRecordOutboxStore); ok {
		if err := batch.PutRecords(records); err != nil {
			return err
		}
	} else if box.store != nil {
		for _, record := range records {
			var err error
			if store, ok := box.store.(RecordOutboxStore); ok {
				err = store.PutRecord(record)
			} else {
				err = box.store.Put(record.Packet)
			}
			if err != nil {
				return err
			}
		}
	}
	for _, value := range entries {
		box.pending[value.id] = value.entry
		box.pendingBytes += value.entry.bytes
	}
	return nil
}

// Reconcile repairs the crash window between a committed History append and
// outbox persistence. Every retained packet newer than ACK becomes pending.
func (box *Outbox) Reconcile(snapshot syncstream.HistorySnapshot) error {
	packets := make([]syncstream.Packet, 0)
	for _, stream := range snapshot.Streams {
		for _, packet := range stream.Packets {
			if packet.Sequence > stream.Acked {
				packets = append(packets, packet)
			}
		}
	}
	return box.PutBatch(packets)
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
	return box.deletePendingLocked(ids, true)
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
	return box.deletePendingLocked(ids, false)
}

func (box *Outbox) deletePendingLocked(ids []pendingID, acknowledged bool) error {
	if len(ids) == 0 {
		return nil
	}
	if batch, ok := box.store.(BatchOutboxStore); ok {
		deletes := make([]OutboxDelete, len(ids))
		for index, id := range ids {
			deletes[index] = OutboxDelete{Observer: id.observer, Stream: id.stream, Epoch: id.epoch, Sequence: id.sequence}
		}
		if err := batch.DeleteBatch(deletes); err != nil {
			return err
		}
		for _, id := range ids {
			box.removePendingLocked(id, acknowledged)
		}
		return nil
	}
	for _, id := range ids {
		if box.store != nil {
			if err := box.store.Delete(id.observer, id.stream, id.epoch, id.sequence); err != nil {
				return err
			}
		}
		box.removePendingLocked(id, acknowledged)
	}
	return nil
}

func (box *Outbox) removePendingLocked(id pendingID, acknowledged bool) {
	entry := box.pending[id]
	if entry == nil {
		return
	}
	delete(box.pending, id)
	box.pendingBytes -= entry.bytes
	if box.pendingBytes < 0 {
		box.pendingBytes = 0
	}
	if acknowledged {
		box.metrics.Acknowledged++
	}
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
	box.mutex.Lock()
	capacityErr := box.capacityError(now, nil, 0)
	box.mutex.Unlock()
	if capacityErr != nil {
		return capacityErr
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
	metrics.PendingBytes = box.pendingBytes
	now := time.Now()
	for _, entry := range box.pending {
		age := now.Sub(entry.createdAt)
		if age > metrics.OldestPendingAge {
			metrics.OldestPendingAge = age
		}
	}
	return metrics
}
