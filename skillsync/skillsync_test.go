package skillsync

import (
	"errors"
	"testing"

	"github.com/tjbdwanghaibo/cube-core/syncstream"
	"github.com/tjbdwanghaibo/cube-skill/skillv2"
)

func TestProjectorBuildsStronglyTypedPackets(t *testing.T) {
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
	snapshot := skillv2.RuntimeStateSnapshot{Tick: 3, WorldRevision: 4, Casts: []skillv2.CastStateSnapshot{{ID: 1}}, LatestStateEventSequence: 8}
	state, err := projector.StateSnapshotPacket(observer, 7, snapshot)
	if err != nil || !state.Full || state.Stream.Topic != TopicState {
		t.Fatalf("state packet = %#v err=%v", state, err)
	}
	delta, err := projector.StateDeltaPacket(observer, 7, skillv2.StateEvent{Sequence: 9, Event: skillv2.RuntimeEvent{Kind: "damage", Tick: 3, Revision: 4}})
	if err != nil || delta.Full || !delta.Critical {
		t.Fatalf("delta packet = %#v err=%v", delta, err)
	}
	if _, err := projector.StateDeltaPacket(observer, 7, skillv2.StateEvent{}); !errors.Is(err, ErrRecordInvalid) {
		t.Fatalf("invalid delta error = %v", err)
	}
	presentation, err := projector.PresentationPacket(observer, 7, skillv2.PresentationEvent{Sequence: 10, Kind: skillv2.PresentationEffect, Tick: 3, WorldRevision: 4, GameplayDigest: "game", PresentationDigest: "view"})
	if err != nil || presentation.Full || presentation.Critical || presentation.Stream.Topic != TopicPresentation {
		t.Fatalf("presentation packet = %#v err=%v", presentation, err)
	}
}

type recordingConsumer struct {
	manifests     int
	snapshots     int
	deltas        int
	presentations int
	resets        int
}

func (consumer *recordingConsumer) ApplyManifest(int64, skillv2.PresentationPlan) error {
	consumer.manifests++
	return nil
}
func (consumer *recordingConsumer) ApplyStateSnapshot(int64, skillv2.RuntimeStateSnapshot) error {
	consumer.snapshots++
	return nil
}
func (consumer *recordingConsumer) ApplyStateDelta(int64, skillv2.StateEvent) error {
	consumer.deltas++
	return nil
}
func (consumer *recordingConsumer) ResetPresentation(int64, PresentationReset) error {
	consumer.resets++
	return nil
}
func (consumer *recordingConsumer) ApplyPresentation(int64, skillv2.PresentationEvent) error {
	consumer.presentations++
	return nil
}

func TestApplierValidatesChainObserverAndManifestDependency(t *testing.T) {
	projector, _ := NewProjector(1)
	observer := syncstream.Observer{ID: 3, Scope: "match"}
	consumer := &recordingConsumer{}
	applier, _ := NewApplier(ApplierOptions{Observer: observer, SchemaVersion: 1, Manifest: consumer, State: consumer, Presentation: consumer})
	history := syncstream.NewHistory(syncstream.HistoryOptions{SchemaVersion: 1})
	plan := skillv2.PresentationPlan{Identity: skillv2.ProgramIdentityView{PresentationDigest: "visual-v1"}}
	manifest, _ := projector.ManifestPacket(observer, 11, plan)
	manifest, _ = history.Append(manifest)
	if result, err := applier.Apply(manifest); err != nil || !result.Applied || consumer.manifests != 1 {
		t.Fatalf("manifest apply = %#v, %v", result, err)
	}
	event := skillv2.PresentationEvent{Sequence: 1, Kind: skillv2.PresentationCast, PresentationDigest: "visual-v1"}
	presentation, _ := projector.PresentationPacket(observer, 11, event)
	presentation, _ = history.Append(presentation)
	if _, err := applier.Apply(presentation); err != nil || consumer.presentations != 1 {
		t.Fatalf("presentation apply error = %v", err)
	}
	if result, err := applier.Apply(presentation); err != nil || !result.Duplicate || consumer.presentations != 1 {
		t.Fatalf("duplicate apply = %#v, %v", result, err)
	}

	missing, _ := projector.PresentationPacket(observer, 12, skillv2.PresentationEvent{Sequence: 1, Kind: skillv2.PresentationCast, PresentationDigest: "missing"})
	missing.Sequence = 1
	if _, err := applier.Apply(missing); !errors.Is(err, ErrManifestMissing) {
		t.Fatalf("missing manifest error = %v", err)
	}
	gap := presentation.Clone()
	gap.Sequence = 3
	gap.BaseSequence = 2
	if _, err := applier.Apply(gap); !errors.Is(err, ErrSequenceGap) {
		t.Fatalf("gap error = %v", err)
	}
	foreign := manifest.Clone()
	foreign.Observer.ID++
	if _, err := applier.Apply(foreign); !errors.Is(err, ErrObserverMismatch) {
		t.Fatalf("observer error = %v", err)
	}
}

type recordingPublisher struct {
	packets  []syncstream.Packet
	failNext bool
}

func (publisher *recordingPublisher) Publish(packet syncstream.Packet) error {
	if publisher.failNext {
		publisher.failNext = false
		return errors.New("backpressure")
	}
	publisher.packets = append(publisher.packets, packet.Clone())
	return nil
}

func TestCoordinatorRetainsPublishFailureForRecovery(t *testing.T) {
	host := skillv2.NewMemoryHost(skillv2.AuthorityIdentity{})
	runtime := skillv2.NewRuntime(host, skillv2.RuntimeOptions{})
	history := syncstream.NewHistory(syncstream.HistoryOptions{SchemaVersion: 1})
	projector, _ := NewProjector(1)
	publisher := &recordingPublisher{failNext: true}
	coordinator, err := NewCoordinator(CoordinatorOptions{Runtime: runtime, History: history, Publisher: publisher, Projector: projector, Visibility: AllowAllVisibility{}})
	if err != nil {
		t.Fatal(err)
	}
	observer := syncstream.Observer{ID: 4}
	if err := coordinator.PublishSnapshot(observer, 8); err == nil {
		t.Fatal("expected injected publish failure")
	}
	result, err := coordinator.Recover(syncstream.ResyncRequest{Observer: observer, Stream: syncstream.Stream{Topic: TopicState, Key: 8}, SchemaVersion: 1})
	if err != nil || result.FullRequired || len(result.Packets) != 1 || len(publisher.packets) != 1 {
		t.Fatalf("recover = %#v, packets=%d, err=%v", result, len(publisher.packets), err)
	}
	metrics := coordinator.Metrics()
	if metrics.PublishFailures != 1 || metrics.Published != 1 {
		t.Fatalf("metrics = %#v", metrics)
	}
}

func TestCoordinatorRequiresExplicitVisibilityPolicy(t *testing.T) {
	projector, _ := NewProjector(1)
	_, err := NewCoordinator(CoordinatorOptions{
		Runtime: skillv2.NewRuntime(skillv2.NewMemoryHost(skillv2.AuthorityIdentity{}), skillv2.RuntimeOptions{}),
		History: syncstream.NewHistory(syncstream.HistoryOptions{}), Publisher: &recordingPublisher{}, Projector: projector,
	})
	if !errors.Is(err, ErrVisibilityRequired) {
		t.Fatalf("visibility error = %v", err)
	}
}
