package skillsync

import (
	"errors"

	"github.com/tjbdwanghaibo/cube-core/syncstream"
)

var (
	ErrSchemaNegotiationFailed = errors.New("skillsync: no compatible schema version")
	ErrSchemaMigratorRequired  = errors.New("skillsync: schema migration is required")
)

type SchemaRange struct {
	Min uint32
	Max uint32
}

func (value SchemaRange) Contains(version uint32) bool {
	return value.Min != 0 && version >= value.Min && version <= value.Max
}

func NegotiateSchema(server, client SchemaRange) (uint32, error) {
	minimum := server.Min
	if client.Min > minimum {
		minimum = client.Min
	}
	maximum := server.Max
	if client.Max < maximum {
		maximum = client.Max
	}
	if minimum == 0 || minimum > maximum {
		return 0, ErrSchemaNegotiationFailed
	}
	return maximum, nil
}

type SchemaMigrator interface {
	Migrate(packet syncstream.Packet, targetVersion uint32) ([]byte, error)
}
