package combatcomponent

import (
	"errors"
	"testing"

	skillv2 "github.com/tjbdwanghaibo/cube-skill/v2/skillv2"

	"github.com/tjbdwanghaibo/cube-skill/v2/combat"
)

type mapResolver map[skillv2.EntityID]*CombatComponent

func (m mapResolver) CombatComponent(id skillv2.EntityID) (*CombatComponent, bool) {
	component, ok := m[id]
	return component, ok
}

type testRevision struct {
	revision skillv2.WorldRevision
	events   []EffectEvent
}

func (r *testRevision) CurrentRevision() skillv2.WorldRevision { return r.revision }
func (r *testRevision) CommitEffect(events []EffectEvent) skillv2.CommitReceipt {
	r.revision++
	r.events = append(r.events, events...)
	return skillv2.CommitReceipt{Revision: r.revision}
}

func newAdapterFixture() (*HostAdapter, *testRevision, *CombatComponent, *CombatComponent) {
	attacker := NewCombatComponent(NewCombatDao(1, "game"))
	attacker.InitCombatant(combat.Combatant{Alive: true, Health: 80, MaxHealth: 80, Penetration: 10})
	defender := NewCombatComponent(NewCombatDao(2, "game"))
	defender.InitCombatant(combat.Combatant{Alive: true, Health: 100, MaxHealth: 100, Shield: 20, Armor: 40})
	defender.SetAttributeBase(5, 50) // mana pool
	revision := &testRevision{}
	adapter := &HostAdapter{
		Resolver: mapResolver{1: attacker, 2: defender},
		Revision: revision,
		ResourceAttribute: func(resource string, _ skillv2.ResourceHandle) (combat.AttributeID, bool) {
			if resource == "mana" {
				return 5, true
			}
			return 0, false
		},
	}
	return adapter, revision, attacker, defender
}

func TestHostAdapterAppliesDamageWithEvents(t *testing.T) {
	adapter, revision, _, defender := newAdapterFixture()
	result, handled, err := adapter.Apply(skillv2.EffectCommand{Payload: skillv2.DamageCommand{Source: 1, Target: 2, Amount: 100, DamageType: 1, CanCritical: true}})
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	payload := result.Payload.(skillv2.DamageEffectResult)
	if !payload.Succeeded || payload.Result.HealthDamage == 0 || payload.Result.Absorbed != 20 {
		t.Fatalf("payload = %+v", payload)
	}
	if defender.Combatant().Shield != 0 {
		t.Fatal("shield not consumed")
	}
	kinds := make([]string, 0, len(revision.events))
	for _, event := range revision.events {
		kinds = append(kinds, event.Kind)
	}
	if len(kinds) != 3 || kinds[0] != "damage_resolved" || kinds[1] != "shield_absorbed" || kinds[2] != "shield_broken" {
		t.Fatalf("events = %v", kinds)
	}
	if result.Commit.Revision != 1 || !result.Commit.Changed {
		t.Fatalf("receipt = %+v", result.Commit)
	}
	// Unknown target fails without a revision bump.
	miss, handled, err := adapter.Apply(skillv2.EffectCommand{Payload: skillv2.DamageCommand{Source: 1, Target: 99, Amount: 10}})
	if err != nil || !handled || miss.Payload.(skillv2.DamageEffectResult).Succeeded || revision.revision != 1 {
		t.Fatalf("missing target: %+v revision=%d err=%v", miss.Payload, revision.revision, err)
	}
	// Non-combat payloads pass through unhandled.
	if _, handled, _ := adapter.Apply(skillv2.EffectCommand{Payload: skillv2.TeleportCommand{Target: 2}}); handled {
		t.Fatal("teleport handled by combat adapter")
	}
}

func TestHostAdapterHealShieldAndReads(t *testing.T) {
	adapter, revision, attacker, _ := newAdapterFixture()
	attacker.ApplyDamage(nil, combat.DamageInput{Amount: 30}, nil)
	healResult, handled, err := adapter.Apply(skillv2.EffectCommand{Payload: skillv2.HealCommand{Target: 1, Amount: 100}})
	if err != nil || !handled || healResult.Payload.(skillv2.HealEffectResult).Result.Effective != 30 {
		t.Fatalf("heal = %+v err=%v", healResult.Payload, err)
	}
	shieldResult, _, err := adapter.Apply(skillv2.EffectCommand{Payload: skillv2.ShieldCommand{Target: 1, Amount: 15, DurationTicks: 10}})
	if err != nil || shieldResult.Payload.(skillv2.ShieldEffectResult).Result.Added != 15 {
		t.Fatalf("shield = %+v err=%v", shieldResult.Payload, err)
	}
	if got := len(revision.events); got != 2 {
		t.Fatalf("events = %d", got)
	}
	read, handled, err := adapter.Read(skillv2.ReadRequest{Payload: skillv2.ResourceRead{Entity: 2, Resource: "mana"}})
	if err != nil || !handled {
		t.Fatalf("read err=%v handled=%v", err, handled)
	}
	if value, ok := read.Value.Int(); !ok || value != 50 {
		t.Fatalf("mana read = %+v", read.Value)
	}
	if _, handled, _ := adapter.Read(skillv2.ReadRequest{Payload: skillv2.PositionRead{Entity: 2}}); handled {
		t.Fatal("position read handled by combat adapter")
	}
}

func TestHostAdapterPayCostsIsAtomic(t *testing.T) {
	adapter, revision, _, defender := newAdapterFixture()
	payment := skillv2.CostPayment{Entity: 2, Entries: []skillv2.CostEntry{{Resource: "mana", Amount: 30}, {Resource: "mana", Amount: 15}}}
	receipt, err := adapter.PayCosts(payment)
	if err != nil || !receipt.Changed || defender.AttributeBase(5) != 5 {
		t.Fatalf("pay = %+v base=%d err=%v", receipt, defender.AttributeBase(5), err)
	}
	// Insufficient totals must not partially deduct.
	if _, err := adapter.PayCosts(payment); !errors.Is(err, skillv2.ErrInsufficientResource) {
		t.Fatalf("expected insufficient, got %v", err)
	}
	if defender.AttributeBase(5) != 5 {
		t.Fatalf("partial deduction: base=%d", defender.AttributeBase(5))
	}
	if _, err := adapter.PayCosts(skillv2.CostPayment{Entity: 2, Entries: []skillv2.CostEntry{{Resource: "unknown", Amount: 1}}}); err == nil {
		t.Fatal("unmapped resource accepted")
	}
	if revision.revision != 1 {
		t.Fatalf("revision = %d, want 1", revision.revision)
	}
}
