package skillsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/tjbdwanghaibo/cube-core/syncstream"
)

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
	store.mutex.Lock()
	defer store.mutex.Unlock()
	entries, err := os.ReadDir(store.directory)
	if err != nil {
		return nil, err
	}
	packets := make([]syncstream.Packet, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".packet" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(store.directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		var packet syncstream.Packet
		if err := json.Unmarshal(data, &packet); err != nil {
			return nil, err
		}
		packets = append(packets, packet)
	}
	sort.Slice(packets, func(i, j int) bool {
		if packets[i].Stream.Topic != packets[j].Stream.Topic {
			return packets[i].Stream.Topic < packets[j].Stream.Topic
		}
		if packets[i].Stream.Key != packets[j].Stream.Key {
			return packets[i].Stream.Key < packets[j].Stream.Key
		}
		return packets[i].Sequence < packets[j].Sequence
	})
	return packets, nil
}

func (store *FileOutboxStore) Put(packet syncstream.Packet) error {
	data, err := json.Marshal(packet)
	if err != nil {
		return err
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
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
