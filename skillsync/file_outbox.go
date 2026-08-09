package skillsync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/tjbdwanghaibo/cube-core/syncstream"
)

const fileOutboxVersion uint32 = 1

type fileOutboxEnvelope struct {
	Version  uint32       `json:"version"`
	Record   OutboxRecord `json:"record"`
	Checksum string       `json:"checksum"`
}

// FileOutboxStore persists one immutable file per network packet. Atomic rename
// makes Put crash-safe and idempotent without a database dependency.
type FileOutboxStore struct {
	mutex     sync.Mutex
	directory string
}

func NewFileOutboxStore(directory string) (*FileOutboxStore, error) {
	if directory == "" {
		return nil, ErrOutboxStoreRequired
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, err
	}
	return &FileOutboxStore{directory: absolute}, nil
}

func (store *FileOutboxStore) Load() ([]syncstream.Packet, error) {
	records, err := store.LoadRecords()
	if err != nil {
		return nil, err
	}
	packets := make([]syncstream.Packet, len(records))
	for index := range records {
		packets[index] = records[index].Packet.Clone()
	}
	return packets, nil
}

func (store *FileOutboxStore) LoadRecords() ([]OutboxRecord, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	entries, err := os.ReadDir(store.directory)
	if err != nil {
		return nil, err
	}
	records := make([]OutboxRecord, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".packet" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(store.directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		record, err := decodeFileOutboxRecord(data)
		if err != nil {
			return nil, err
		}
		if outboxFilename(record.Packet.Observer, record.Packet.Stream, record.Packet.Epoch, record.Packet.Sequence) != entry.Name() {
			return nil, ErrRecordInvalid
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Packet.Stream.Topic != records[j].Packet.Stream.Topic {
			return records[i].Packet.Stream.Topic < records[j].Packet.Stream.Topic
		}
		if records[i].Packet.Stream.Key != records[j].Packet.Stream.Key {
			return records[i].Packet.Stream.Key < records[j].Packet.Stream.Key
		}
		return records[i].Packet.Sequence < records[j].Packet.Sequence
	})
	return records, nil
}

func (store *FileOutboxStore) Put(packet syncstream.Packet) error {
	return store.PutRecord(OutboxRecord{Packet: packet.Clone(), CreatedAt: time.Now()})
}

func (store *FileOutboxStore) PutRecord(record OutboxRecord) error {
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now()
	}
	recordData, err := json.Marshal(record)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(recordData)
	data, err := json.Marshal(fileOutboxEnvelope{Version: fileOutboxVersion, Record: record, Checksum: hex.EncodeToString(digest[:])})
	if err != nil {
		return err
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	packet := record.Packet
	target := filepath.Join(store.directory, outboxFilename(packet.Observer, packet.Stream, packet.Epoch, packet.Sequence))
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(store.directory, "outbox-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(name)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, target); err != nil {
		return err
	}
	remove = false
	return nil
}

func decodeFileOutboxRecord(data []byte) (OutboxRecord, error) {
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(data, &shape); err != nil {
		return OutboxRecord{}, err
	}
	if _, ok := shape["version"]; ok {
		var envelope fileOutboxEnvelope
		if err := decodeStrict(data, &envelope); err != nil || envelope.Version != fileOutboxVersion {
			return OutboxRecord{}, ErrRecordInvalid
		}
		recordData, err := json.Marshal(envelope.Record)
		if err != nil {
			return OutboxRecord{}, err
		}
		digest := sha256.Sum256(recordData)
		if !bytes.Equal([]byte(envelope.Checksum), []byte(hex.EncodeToString(digest[:]))) {
			return OutboxRecord{}, ErrRecordInvalid
		}
		return envelope.Record, nil
	}
	if _, ok := shape["packet"]; ok { // pre-envelope record format
		var record OutboxRecord
		if err := decodeStrict(data, &record); err != nil {
			return OutboxRecord{}, err
		}
		return record, nil
	}
	var packet syncstream.Packet // original packet-only format
	if err := decodeStrict(data, &packet); err != nil {
		return OutboxRecord{}, err
	}
	return OutboxRecord{Packet: packet, CreatedAt: time.Now()}, nil
}

func (store *FileOutboxStore) Delete(observer syncstream.Observer, stream syncstream.Stream, epoch, sequence uint64) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	err := os.Remove(filepath.Join(store.directory, outboxFilename(observer, stream, epoch, sequence)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func outboxFilename(observer syncstream.Observer, stream syncstream.Stream, epoch, sequence uint64) string {
	data, _ := json.Marshal(struct {
		Observer syncstream.Observer
		Stream   syncstream.Stream
		Epoch    uint64
		Sequence uint64
	}{observer, stream, epoch, sequence})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) + ".packet"
}
