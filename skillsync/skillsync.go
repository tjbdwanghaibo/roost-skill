// Package skillsync projects skill runtime data onto cube-core's generic
// syncstream envelopes. It does not publish packets or own transport policy.
package skillsync

import (
	"encoding/json"
	"errors"

	"github.com/tjbdwanghaibo/cube-core/syncstream"
	"github.com/tjbdwanghaibo/cube-skill/skillv2"
)

const (
	TopicManifest     = "cube.skill.manifest"
	TopicState        = "cube.skill.state"
	TopicPresentation = "cube.skill.presentation"
)

var ErrSchemaVersionRequired = errors.New("skillsync: schema version is required")

type RecordKind string

const (
	RecordManifest     RecordKind = "manifest"
	RecordStateFull    RecordKind = "state_full"
	RecordStateDelta   RecordKind = "state_delta"
	RecordPresentation RecordKind = "presentation"
)

type Header struct {
	SchemaVersion      uint32                `json:"schema_version"`
	Kind               RecordKind            `json:"kind"`
	Tick               skillv2.Tick          `json:"tick,omitempty"`
	WorldRevision      skillv2.WorldRevision `json:"world_revision,omitempty"`
	GameplayDigest     string                `json:"gameplay_digest,omitempty"`
	PresentationDigest string                `json:"presentation_digest,omitempty"`
}

type ManifestRecord struct {
	Header
	Plan skillv2.PresentationPlan `json:"plan"`
}

// StateSnapshot is the authoritative skill-owned state visible to one
// observer. Applications may add world/entity replication on a separate topic.
type StateSnapshot struct {
	Tick          skillv2.Tick           `json:"tick"`
	WorldRevision skillv2.WorldRevision  `json:"world_revision"`
	Casts         []skillv2.CastSnapshot `json:"casts,omitempty"`
}

type StateRecord struct {
	Header
	Snapshot *StateSnapshot   `json:"snapshot,omitempty"`
	Entity   skillv2.EntityID `json:"entity,omitempty"`
	Change   string           `json:"change,omitempty"`
	Data     json.RawMessage  `json:"data,omitempty"`
}

type PresentationRecord struct {
	Header
	Event skillv2.PresentationEvent `json:"event"`
}

type Projector struct {
	SchemaVersion uint32
}

func NewProjector(schemaVersion uint32) (Projector, error) {
	if schemaVersion == 0 {
		return Projector{}, ErrSchemaVersionRequired
	}
	return Projector{SchemaVersion: schemaVersion}, nil
}

func (projector Projector) ManifestPacket(observer syncstream.Observer, key int64, plan skillv2.PresentationPlan) (syncstream.Packet, error) {
	record := ManifestRecord{Header: Header{
		SchemaVersion: projector.SchemaVersion, Kind: RecordManifest,
		GameplayDigest: plan.Identity.GameplayDigest, PresentationDigest: plan.Identity.PresentationDigest,
	}, Plan: plan}
	return projector.packet(observer, key, TopicManifest, true, true, record)
}

func (projector Projector) StateSnapshotPacket(observer syncstream.Observer, key int64, snapshot StateSnapshot) (syncstream.Packet, error) {
	copySnapshot := snapshot
	copySnapshot.Casts = append([]skillv2.CastSnapshot(nil), snapshot.Casts...)
	record := StateRecord{Header: Header{
		SchemaVersion: projector.SchemaVersion, Kind: RecordStateFull,
		Tick: snapshot.Tick, WorldRevision: snapshot.WorldRevision,
	}, Snapshot: &copySnapshot}
	return projector.packet(observer, key, TopicState, true, true, record)
}

func (projector Projector) StateDeltaPacket(observer syncstream.Observer, key int64, tick skillv2.Tick, revision skillv2.WorldRevision, entity skillv2.EntityID, change string, data json.RawMessage) (syncstream.Packet, error) {
	record := StateRecord{Header: Header{
		SchemaVersion: projector.SchemaVersion, Kind: RecordStateDelta, Tick: tick, WorldRevision: revision,
	}, Entity: entity, Change: change, Data: append(json.RawMessage(nil), data...)}
	return projector.packet(observer, key, TopicState, false, true, record)
}

func (projector Projector) PresentationPacket(observer syncstream.Observer, key int64, event skillv2.PresentationEvent) (syncstream.Packet, error) {
	record := PresentationRecord{Header: Header{
		SchemaVersion: projector.SchemaVersion, Kind: RecordPresentation,
		Tick: event.Tick, WorldRevision: event.WorldRevision,
		GameplayDigest: event.GameplayDigest, PresentationDigest: event.PresentationDigest,
	}, Event: event}
	return projector.packet(observer, key, TopicPresentation, false, false, record)
}

func (projector Projector) packet(observer syncstream.Observer, key int64, topic string, full, critical bool, record any) (syncstream.Packet, error) {
	if projector.SchemaVersion == 0 {
		return syncstream.Packet{}, ErrSchemaVersionRequired
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return syncstream.Packet{}, err
	}
	return syncstream.Packet{
		Observer: observer, Stream: syncstream.Stream{Topic: topic, Key: key},
		SchemaVersion: projector.SchemaVersion, Full: full, Critical: critical, Payload: payload,
	}, nil
}
