package skillv2

import (
	"errors"
	"testing"
)

// Regression: reading ammo_stock used to write the authoritative stock back
// into the ability cache on the read path, without a write point — after a
// recharge, one read permanently diverged the incremental mutation baseline
// from the live snapshot and every later Checkpoint failed with
// ErrCheckpointHostMismatch.
func TestAmmoReadAfterRechargeKeepsCheckpointHealthy(t *testing.T) {
	environment := abilityTestEnvironment()
	program := compileAbilityTestSkill(t, environment, "ammo-read", `{"mode":"ammo","max_stock":3,"recharge_ticks":3,"initial_stock":3}`, 0, `{"flow":"finish"}`)
	runtime := NewRuntime(runtimeTestHost(environment), RuntimeOptions{})
	if _, err := runtime.Activate(program, CastInput{Caster: 1}); err != nil {
		t.Fatal(err)
	}
	handle := runtime.abilityByProgram[skillStateKey{Caster: 1, Skill: program.id}]
	if err := runtime.Advance(3); err != nil { // recharge fires
		t.Fatal(err)
	}
	if _, err := runtime.Checkpoint(); err != nil {
		t.Fatalf("checkpoint before read: %v", err)
	}
	stock, err := runtime.ReadAbilityState(1, handle, "ammo_stock")
	if err != nil {
		t.Fatal(err)
	}
	if value, _ := stock.Int(); value != 3 {
		t.Fatalf("ammo_stock = %d, want 3 (recharged)", value)
	}
	if _, err := runtime.Checkpoint(); err != nil {
		t.Fatalf("checkpoint after ammo read diverged the baseline: %v", err)
	}
	// The recharge itself must be visible to ability-state consumers via the
	// snapshot, not only through a read-path side effect.
	for _, ability := range runtime.StateSnapshot().Abilities {
		if ability.Handle == handle && ability.AmmoStock != 3 {
			t.Fatalf("snapshot ammo = %d, want 3", ability.AmmoStock)
		}
	}
}

// Regression: Cancel (and Interrupt) left policyActive set and the
// activePolicies entry behind — a cancelled toggle could never be activated
// again (permanent ErrCastInputRejected against the dead cast) and the cast
// stayed unevictable forever.
func TestCancelledToggleCanBeActivatedAgain(t *testing.T) {
	policy := `{"mode":"toggle","pulse_interval_ticks":2,"max_duration_ticks":8,"sustain_costs":[]}`
	program, environment := compilePolicySkill(t, "toggle-cancel", policy, 4)
	runtime := NewRuntime(runtimeTestHost(environment), RuntimeOptions{})
	castID, err := runtime.Activate(program, CastInput{Caster: 1, Target: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Cancel(castID); err != nil {
		t.Fatal(err)
	}
	if got := len(runtime.StateSnapshot().ActivePolicies); got != 0 {
		t.Fatalf("cancel left %d active policy entries", got)
	}
	if evictable := runtime.castEvictableLocked(runtime.casts[castID]); !evictable {
		t.Fatal("cancelled toggle cast is not evictable")
	}
	if err := runtime.Advance(6); err != nil { // past the cooldown started by Cancel
		t.Fatal(err)
	}
	second, err := runtime.Activate(program, CastInput{Caster: 1, Target: 2})
	if err != nil {
		t.Fatalf("re-activation after cancel: %v", err)
	}
	if second == castID {
		t.Fatal("re-activation resolved to the dead cast")
	}
	// And the same for Interrupt-style cancellation via the shared helper:
	// the fresh toggle can be cancelled and re-activated repeatedly.
	if err := runtime.Cancel(second); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Advance(12); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Activate(program, CastInput{Caster: 1, Target: 2}); err != nil {
		t.Fatalf("second re-activation: %v", err)
	}
	if _, err := runtime.Activate(program, CastInput{Caster: 99, Target: 2}); errors.Is(err, ErrCastInputRejected) {
		t.Fatalf("unrelated caster affected: %v", err)
	}
}

// Regression: mutationSortKey omitted the StateHandle, so two
// persistent_remove mutations under the same binding had byte-equal sort
// keys — their sequence order fell to map iteration and differed across
// runs, breaking replay/sequence accounting between replicas.
func TestPersistentRemoveOrderingIsDeterministic(t *testing.T) {
	binding := StateScopeBinding{Owner: 7}
	first := StateMutation{Kind: StateMutationPersistentRemove, StateHandle: StateHandle{GameplayDigest: "aa", Slot: 1}, Binding: binding}
	second := StateMutation{Kind: StateMutationPersistentRemove, StateHandle: StateHandle{GameplayDigest: "bb", Slot: 2}, Binding: binding}
	if mutationSortKey(first) == mutationSortKey(second) {
		t.Fatal("distinct persistent handles share a sort key")
	}
	before := []PersistentStateSnapshot{
		{Handle: first.StateHandle, Binding: binding, Sequence: 1},
		{Handle: second.StateHandle, Binding: binding, Sequence: 2},
	}
	for run := 0; run < 32; run++ {
		result := diffPersistentStates(nil, before, nil)
		if len(result) != 2 || result[0].StateHandle != first.StateHandle || result[1].StateHandle != second.StateHandle {
			t.Fatalf("run %d: removal order changed: %+v", run, result)
		}
	}
}
