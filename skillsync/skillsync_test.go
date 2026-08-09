package skillsync

import (
	"encoding/json"
	"testing"

	"github.com/tjbdwanghaibo/cube-core/syncstream"
	"github.com/tjbdwanghaibo/cube-skill/skillv2"
)

func TestProjectorBuildsManifestStateAndPresentationPackets(t *testing.T) {
	projector, err := NewProjector(2)
	if err != nil {
		t.Fatal(err)
	}
	observer := syncstream.Observer{Kind: 1, ID: 9, Scope: "match-7"}
	plan := skillv2.PresentationPlan{Identity: skillv2.ProgramIdentityView{GameplayDigest: "game", PresentationDigest: "view"}}
	manifest, err := projector.ManifestPacket(observer, 7, plan)
	if err != nil || !manifest.Full || !manifest.Critical || manifest.Stream.Topic != TopicManifest {
		t.Fatalf("manifest packet = %#v err=%v", manifest, err)
	}
	state, err := projector.StateSnapshotPacket(observer, 7, StateSnapshot{Tick: 3, WorldRevision: 4, Casts: []skillv2.CastSnapshot{{ID: 1}}})
	if err != nil || !state.Full || state.Stream.Topic != TopicState {
		t.Fatalf("state packet = %#v err=%v", state, err)
	}
	presentation, err := projector.PresentationPacket(observer, 7, skillv2.PresentationEvent{Sequence: 10, Tick: 3, WorldRevision: 4, GameplayDigest: "game", PresentationDigest: "view"})
	if err != nil || presentation.Full || presentation.Critical || presentation.Stream.Topic != TopicPresentation {
		t.Fatalf("presentation packet = %#v err=%v", presentation, err)
	}
	var record PresentationRecord
	if err := json.Unmarshal(presentation.Payload, &record); err != nil || record.Kind != RecordPresentation || record.Event.Sequence != 10 {
		t.Fatalf("presentation record = %#v err=%v", record, err)
	}
}

func TestProjectedPacketsUseCoreHistoryForReplayAndFullFallback(t *testing.T) {
	projector, _ := NewProjector(1)
	observer := syncstream.Observer{ID: 3}
	history := syncstream.NewHistory(syncstream.HistoryOptions{MaxPacketsPerStream: 1, SchemaVersion: 1})
	for sequence := uint64(1); sequence <= 2; sequence++ {
		packet, err := projector.PresentationPacket(observer, 11, skillv2.PresentationEvent{Sequence: sequence})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := history.Append(packet); err != nil {
			t.Fatal(err)
		}
	}
	result := history.Resync(syncstream.ResyncRequest{Observer: observer, Stream: syncstream.Stream{Topic: TopicPresentation, Key: 11}, AfterSequence: 0, SchemaVersion: 1})
	if !result.FullRequired || result.Reason != syncstream.ResyncHistoryGap {
		t.Fatalf("resync result = %#v", result)
	}
}

func TestProjectorRejectsZeroSchema(t *testing.T) {
	if _, err := NewProjector(0); err != ErrSchemaVersionRequired {
		t.Fatalf("NewProjector error = %v", err)
	}
}
