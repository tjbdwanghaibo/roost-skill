package combatcomponent

import (
	"errors"
	"testing"

	"github.com/tjbdwanghaibo/roost-skill/skillv2"

	"github.com/tjbdwanghaibo/roost-skill/combat"
)

func newBridgeFixture() (*StatusBridge, *testRevision, *CombatComponent, *skillv2.Tick) {
	target := NewCombatComponent(NewCombatDao(2, "game"))
	target.InitCombatant(combat.Combatant{Alive: true, Health: 100, MaxHealth: 100})
	target.SetAttributeBase(3, 100) // haste channel
	revision := &testRevision{}
	tick := skillv2.Tick(0)
	bridge := &StatusBridge{
		Resolver: mapResolver{2: target},
		Revision: revision,
		Catalog: skillv2.GameplayCatalog{Statuses: skillv2.StatusCatalog{Entries: []skillv2.StatusCatalogEntry{
			{Handle: 10, Key: "burn", Category: "dot", DispelCategory: "magic", Dispellable: true, MaxStacks: 3,
				AttributeModifiers: []skillv2.StatusAttributeModifier{{Attribute: 3, Operation: "mul_bp", Value: 12000}}},
			{Handle: 11, Key: "stun", Category: "control", DispelCategory: "control", Dispellable: true,
				TenacityPolicy: "scale_duration", MaximumDurationTicks: 6, ImmunityTags: []skillv2.GameplayTagHandle{7}},
			{Handle: 12, Key: "fresh", RefreshPolicy: "replace", MaxStacks: 5},
		}}},
		CurrentTick: func() skillv2.Tick { return tick },
	}
	return bridge, revision, target, &tick
}

func TestStatusBridgeAppliesStacksAndModifiers(t *testing.T) {
	bridge, revision, target, _ := newBridgeFixture()
	result, handled, err := bridge.Apply(skillv2.EffectCommand{Payload: skillv2.StatusCommand{SourceOwner: 1, Target: 2, Status: 10, DurationTicks: 20, Stacks: 2}})
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	payload := result.Payload.(skillv2.StatusEffectResult)
	if !payload.Succeeded || !payload.Result.Applied || payload.Result.PreviousStacks != 0 || payload.Result.CurrentStacks != 2 || payload.Result.DueTick != 20 {
		t.Fatalf("payload = %+v", payload)
	}
	// Two stacks of ×1.2 aggregate additively: 100 * (1 + 0.2*2) = 140.
	if got := target.AttributeCurrent(3); got != 140 {
		t.Fatalf("modifier aggregation = %d, want 140", got)
	}
	if len(revision.events) != 1 || revision.events[0].Kind != "status_applied" {
		t.Fatalf("events = %+v", revision.events)
	}
	// Stacking beyond MaxStacks caps at 3.
	again, _, _ := bridge.Apply(skillv2.EffectCommand{Payload: skillv2.StatusCommand{SourceOwner: 1, Target: 2, Status: 10, DurationTicks: 20, Stacks: 5}})
	if got := again.Payload.(skillv2.StatusEffectResult).Result.CurrentStacks; got != 3 {
		t.Fatalf("capped stacks = %d, want 3", got)
	}
}

func TestStatusBridgeImmunityTenacityAndReplace(t *testing.T) {
	bridge, revision, target, _ := newBridgeFixture()
	bridge.HasGameplayTag = func(entity skillv2.EntityID, tag skillv2.GameplayTagHandle) bool {
		return entity == 2 && tag == 7
	}
	// Immunity tag blocks the stun without touching the container.
	result, _, err := bridge.Apply(skillv2.EffectCommand{Payload: skillv2.StatusCommand{Target: 2, Status: 11, DurationTicks: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if payload := result.Payload.(skillv2.StatusEffectResult); !payload.Result.Immune || len(target.ActiveBuffs()) != 0 {
		t.Fatalf("immunity leaked: %+v buffs=%d", payload, len(target.ActiveBuffs()))
	}
	if revision.events[0].Kind != "status_immune" {
		t.Fatalf("events = %+v", revision.events)
	}
	// Without the tag: tenacity scales the duration, then the policy cap clamps.
	bridge.HasGameplayTag = nil
	target.Dao().buffs.SetTenacityBP(5000) // -50%: 10 -> 5, below the 6-tick cap
	applied, _, _ := bridge.Apply(skillv2.EffectCommand{Payload: skillv2.StatusCommand{Target: 2, Status: 11, DurationTicks: 10}})
	if due := applied.Payload.(skillv2.StatusEffectResult).Result.DueTick; due != 5 {
		t.Fatalf("tenacity due = %d, want 5", due)
	}
	// replace policy: re-application discards the old instance entirely.
	bridge.Apply(skillv2.EffectCommand{Payload: skillv2.StatusCommand{Target: 2, Status: 12, DurationTicks: 10, Stacks: 4}})
	replaced, _, _ := bridge.Apply(skillv2.EffectCommand{Payload: skillv2.StatusCommand{Target: 2, Status: 12, DurationTicks: 10, Stacks: 1}})
	if got := replaced.Payload.(skillv2.StatusEffectResult).Result.CurrentStacks; got != 1 {
		t.Fatalf("replace stacks = %d, want 1", got)
	}
	if errored, _, err := bridge.Apply(skillv2.EffectCommand{Payload: skillv2.StatusCommand{Target: 2, Status: 99, DurationTicks: 1}}); err == nil {
		t.Fatalf("unknown status accepted: %+v", errored)
	}
	if _, _, err := bridge.Apply(skillv2.EffectCommand{Payload: skillv2.StatusCommand{Target: 2, Status: 10, DurationTicks: 0}}); err == nil {
		t.Fatal("zero duration accepted")
	}
}

func TestStatusBridgeRemoveAndDispel(t *testing.T) {
	bridge, revision, target, _ := newBridgeFixture()
	bridge.Apply(skillv2.EffectCommand{Payload: skillv2.StatusCommand{SourceOwner: 1, Target: 2, Status: 10, DurationTicks: 20, Stacks: 2}})
	// Remove scoped to a different source owner is a no-op.
	miss, _, _ := bridge.Apply(skillv2.EffectCommand{Payload: skillv2.RemoveStatusCommand{SourceOwner: 9, Target: 2, Status: 10}})
	if payload := miss.Payload.(skillv2.StatusEffectResult); payload.Result.Removed || payload.Result.CurrentStacks != 2 {
		t.Fatalf("scoped remove hit: %+v", payload)
	}
	removed, _, _ := bridge.Apply(skillv2.EffectCommand{Payload: skillv2.RemoveStatusCommand{SourceOwner: 1, Target: 2, Status: 10}})
	if payload := removed.Payload.(skillv2.StatusEffectResult); !payload.Result.Removed || payload.Result.RemovedStacks != 2 || target.AttributeCurrent(3) != 100 {
		t.Fatalf("remove = %+v haste=%d", payload, target.AttributeCurrent(3))
	}
	// Dispel by category.
	bridge.Apply(skillv2.EffectCommand{Payload: skillv2.StatusCommand{SourceOwner: 1, Target: 2, Status: 10, DurationTicks: 20}})
	dispelled, _, _ := bridge.Apply(skillv2.EffectCommand{Payload: skillv2.DispelStatusCommand{Target: 2, Category: "magic", Count: 0}})
	if payload := dispelled.Payload.(skillv2.StatusEffectResult); !payload.Result.Removed || payload.Result.CurrentStacks != 0 {
		t.Fatalf("dispel = %+v", payload)
	}
	kinds := []string{}
	for _, event := range revision.events {
		kinds = append(kinds, event.Kind)
	}
	want := []string{"status_applied", "status_removed", "status_applied", "status_dispelled"}
	for index, kind := range want {
		if kinds[index] != kind {
			t.Fatalf("events = %v, want %v", kinds, want)
		}
	}
}

func TestStatusBridgeAttributeModifierIsIndependent(t *testing.T) {
	bridge, _, target, tickPtr := newBridgeFixture()
	command := skillv2.AttributeModifierCommand{SourceOwner: 1, Target: 2, Attribute: 3, Operation: "mul_bp", Value: 12000, DurationTicks: 10}
	bridge.Apply(skillv2.EffectCommand{Payload: command})
	*tickPtr = 4
	bridge.Apply(skillv2.EffectCommand{Payload: command})
	// Two independent ×1.2 grants aggregate additively: 100 * 1.4 = 140.
	if got := target.AttributeCurrent(3); got != 140 {
		t.Fatalf("modifiers = %d, want 140", got)
	}
	// Each keeps its own expiry: the first lapses at 10, the second at 14.
	if expired := target.TickBuffs(10); len(expired) != 1 || target.AttributeCurrent(3) != 120 {
		t.Fatalf("first expiry: expired=%d haste=%d", len(expired), target.AttributeCurrent(3))
	}
	if expired := target.TickBuffs(14); len(expired) != 1 || target.AttributeCurrent(3) != 100 {
		t.Fatalf("second expiry: expired=%d haste=%d", len(expired), target.AttributeCurrent(3))
	}
	if _, _, err := bridge.Apply(skillv2.EffectCommand{Payload: skillv2.AttributeModifierCommand{Target: 2, Attribute: 3, Operation: "pow", Value: 2, DurationTicks: 1}}); err == nil {
		t.Fatal("unsupported operation accepted")
	}
}

func TestHostAdapterResourceCommand(t *testing.T) {
	adapter, revision, _, defender := newAdapterFixture()
	spend, handled, err := adapter.Apply(skillv2.EffectCommand{Payload: skillv2.ResourceCommand{Target: 2, Resource: 5, Operation: "spend", Amount: 20}})
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if value, _ := spend.Value.Int(); value != 30 || defender.AttributeBase(5) != 30 {
		t.Fatalf("spend result = %d base=%d", value, defender.AttributeBase(5))
	}
	if _, _, err := adapter.Apply(skillv2.EffectCommand{Payload: skillv2.ResourceCommand{Target: 2, Resource: 5, Operation: "spend", Amount: 99}}); !errors.Is(err, skillv2.ErrInsufficientResource) {
		t.Fatalf("overspend accepted: %v", err)
	}
	// No-op change commits nothing.
	noop, _, _ := adapter.Apply(skillv2.EffectCommand{Payload: skillv2.ResourceCommand{Target: 2, Resource: 5, Operation: "add", Amount: 0}})
	if noop.Commit.Changed || revision.revision != 1 {
		t.Fatalf("no-op bumped revision: %+v revision=%d", noop.Commit, revision.revision)
	}
	if len(revision.events) != 1 || revision.events[0].Kind != "resource_changed" {
		t.Fatalf("events = %+v", revision.events)
	}
}
