package combatcomponent

import (
	"fmt"

	skillv2 "github.com/tjbdwanghaibo/cube-skill/v2/skillv2"

	"github.com/tjbdwanghaibo/cube-skill/v2/combat"
)

// Resolver locates the combat component for a skillv2 entity id. The entity
// must already be locked by the caller (skillv2.Host methods run under the
// Runtime lock inside a nest handler, which holds the entity locks).
type Resolver interface {
	CombatComponent(id skillv2.EntityID) (*CombatComponent, bool)
}

// RevisionSource owns the world revision and the host event stream.
type RevisionSource interface {
	CurrentRevision() skillv2.WorldRevision
	// CommitEffect advances the revision once and appends the events in
	// order, returning the receipt for the mutation.
	CommitEffect(events []EffectEvent) skillv2.CommitReceipt
}

// EffectEvent is one host event an effect produced.
type EffectEvent struct {
	Kind    string
	Entity  skillv2.EntityID
	Context skillv2.EventContext
}

// HostAdapter implements the combat effect surface of skillv2.Host over
// combat components: damage, heal, and shield commands, attribute and
// resource reads, and cost payment. Embed it in a business Host and delegate
// the commands it recognizes; world queries (Select), motion, processes, and
// spawning stay with the business host. It follows the MemoryHost event
// vocabulary (damage_resolved, combat_hook_*, shield_absorbed, ...) so proc
// filters behave identically on both hosts.
type HostAdapter struct {
	Resolver Resolver
	Revision RevisionSource
	// Catalog supplies attribute quantities for reads and the critical tag.
	Catalog skillv2.GameplayCatalog
	// SpellTag marks damage commands carrying it as spell damage; zero
	// disables spell-shield interception.
	SpellTag skillv2.GameplayTagHandle
	// CriticalTag is appended to damage event tags on critical hits.
	CriticalTag skillv2.GameplayTagHandle
	// Hooks supplies combat-hook interceptors for one damage instance; nil
	// disables status-driven hooks.
	Hooks func(source, target skillv2.EntityID) combat.Hooks
	// ResourceAttribute maps a resource to the attribute channel that backs
	// it; required for PayCosts and resource reads.
	ResourceAttribute func(resource string, handle skillv2.ResourceHandle) (combat.AttributeID, bool)
}

// Apply handles a combat effect command. handled=false means the payload is
// not a combat command and the business host must process it.
func (adapter *HostAdapter) Apply(command skillv2.EffectCommand) (skillv2.EffectResult, bool, error) {
	switch payload := command.Payload.(type) {
	case skillv2.DamageCommand:
		result, err := adapter.applyDamage(payload)
		return result, true, err
	case skillv2.HealCommand:
		result, err := adapter.applyHeal(payload)
		return result, true, err
	case skillv2.ShieldCommand:
		result, err := adapter.applyShield(payload)
		return result, true, err
	}
	return skillv2.EffectResult{}, false, nil
}

func (adapter *HostAdapter) applyDamage(command skillv2.DamageCommand) (skillv2.EffectResult, error) {
	target, ok := adapter.Resolver.CombatComponent(command.Target)
	if !ok || !target.Combatant().Alive {
		return skillv2.EffectResult{Commit: skillv2.CommitReceipt{Revision: adapter.Revision.CurrentRevision()}, Payload: skillv2.DamageEffectResult{ResultOutcome: skillv2.ResultOutcome{FailureReason: skillv2.ExpectedFailureInvalidTarget}}}, nil
	}
	var source *CombatComponent
	if command.Source != 0 {
		source, ok = adapter.Resolver.CombatComponent(command.Source)
		if !ok {
			return skillv2.EffectResult{Commit: skillv2.CommitReceipt{Revision: adapter.Revision.CurrentRevision()}, Payload: skillv2.DamageEffectResult{ResultOutcome: skillv2.ResultOutcome{FailureReason: skillv2.ExpectedFailureInvalidTarget}}}, nil
		}
	}
	var hooks combat.Hooks
	if adapter.Hooks != nil {
		hooks = adapter.Hooks(command.Source, command.Target)
	}
	input := combat.DamageInput{
		Amount: command.Amount, Type: combat.DamageType(command.DamageType), Element: combat.Element(command.Element),
		CanCritical: command.CanCritical,
		SpellTagged: adapter.SpellTag != 0 && containsTagHandle(command.Tags, adapter.SpellTag),
	}
	outcome, _ := target.ApplyDamage(source, input, hooks)
	result := skillv2.DamageResult{
		Attempted: outcome.Attempted, Mitigated: outcome.Mitigated, Absorbed: outcome.Absorbed,
		HealthDamage: outcome.HealthDamage,
		Dodged:       outcome.Dodged, Parried: outcome.Parried, Blocked: outcome.Blocked,
		Critical: outcome.Critical, Immune: outcome.Immune, Killed: outcome.Killed,
		CombatHooks: outcome.CombatHooks,
	}
	context := command.Event
	context.Source, context.Owner, context.Target = command.Source, command.Owner, command.Target
	context.EffectIndex, context.DamageType, context.Element = command.Meta.EffectIndex, command.DamageType, command.Element
	context.Result = outcome.Result
	tags := append([]skillv2.GameplayTagHandle(nil), command.Tags...)
	if result.Critical && adapter.CriticalTag != 0 {
		tags = append(tags, adapter.CriticalTag)
	}
	context = context.WithGameplayTags(tags)
	events := []EffectEvent{{Kind: "damage_resolved", Entity: command.Target, Context: context}}
	for _, hook := range result.CombatHooks {
		hookContext := context
		hookContext.Result = hook
		events = append(events, EffectEvent{Kind: "combat_hook_" + hook, Entity: command.Target, Context: hookContext})
	}
	if result.Absorbed > 0 {
		events = append(events, EffectEvent{Kind: "shield_absorbed", Entity: command.Target, Context: context})
		if target.Combatant().Shield == 0 {
			events = append(events, EffectEvent{Kind: "shield_broken", Entity: command.Target, Context: context})
		}
	}
	receipt := adapter.Revision.CommitEffect(events)
	receipt.Changed = true
	return skillv2.EffectResult{Commit: receipt, Payload: skillv2.DamageEffectResult{ResultOutcome: skillv2.ResultOutcome{Succeeded: true}, Result: result}}, nil
}

func (adapter *HostAdapter) applyHeal(command skillv2.HealCommand) (skillv2.EffectResult, error) {
	target, ok := adapter.Resolver.CombatComponent(command.Target)
	if !ok {
		return skillv2.EffectResult{Commit: skillv2.CommitReceipt{Revision: adapter.Revision.CurrentRevision()}, Payload: skillv2.HealEffectResult{ResultOutcome: skillv2.ResultOutcome{FailureReason: skillv2.ExpectedFailureInvalidTarget}}}, nil
	}
	outcome, alive := target.Heal(command.Amount)
	if !alive {
		return skillv2.EffectResult{Commit: skillv2.CommitReceipt{Revision: adapter.Revision.CurrentRevision()}, Payload: skillv2.HealEffectResult{ResultOutcome: skillv2.ResultOutcome{FailureReason: skillv2.ExpectedFailureInvalidTarget}}}, nil
	}
	context := command.Event
	context.Source, context.Target, context.Result = command.Source, command.Target, "healed"
	receipt := adapter.Revision.CommitEffect([]EffectEvent{{Kind: "heal_resolved", Entity: command.Target, Context: context}})
	receipt.Changed = true
	return skillv2.EffectResult{Commit: receipt, Payload: skillv2.HealEffectResult{ResultOutcome: skillv2.ResultOutcome{Succeeded: true}, Result: skillv2.HealResult{Attempted: outcome.Attempted, Effective: outcome.Effective}}}, nil
}

func (adapter *HostAdapter) applyShield(command skillv2.ShieldCommand) (skillv2.EffectResult, error) {
	target, ok := adapter.Resolver.CombatComponent(command.Target)
	if !ok {
		return skillv2.EffectResult{Commit: skillv2.CommitReceipt{Revision: adapter.Revision.CurrentRevision()}, Payload: skillv2.ShieldEffectResult{ResultOutcome: skillv2.ResultOutcome{FailureReason: skillv2.ExpectedFailureInvalidTarget}}}, nil
	}
	added, alive := target.AddShield(command.Amount)
	if !alive {
		return skillv2.EffectResult{Commit: skillv2.CommitReceipt{Revision: adapter.Revision.CurrentRevision()}, Payload: skillv2.ShieldEffectResult{ResultOutcome: skillv2.ResultOutcome{FailureReason: skillv2.ExpectedFailureInvalidTarget}}}, nil
	}
	context := command.Event
	context.Source, context.Target, context.Result = command.Source, command.Target, "shielded"
	receipt := adapter.Revision.CommitEffect([]EffectEvent{{Kind: "shield_resolved", Entity: command.Target, Context: context}})
	receipt.Changed = true
	return skillv2.EffectResult{Commit: receipt, Payload: skillv2.ShieldEffectResult{ResultOutcome: skillv2.ResultOutcome{Succeeded: true}, Result: skillv2.ShieldResult{Added: added}}}, nil
}

// Read answers attribute and resource reads from component state.
// handled=false means the payload is not a combat read.
func (adapter *HostAdapter) Read(request skillv2.ReadRequest) (skillv2.ReadResult, bool, error) {
	switch payload := request.Payload.(type) {
	case skillv2.AttributeRead:
		component, ok := adapter.Resolver.CombatComponent(payload.Entity)
		if !ok {
			return skillv2.ReadResult{}, true, fmt.Errorf("combatcomponent: entity %d has no combat component", payload.Entity)
		}
		value := component.AttributeCurrent(combat.AttributeID(payload.Attribute))
		return skillv2.ReadResult{Meta: skillv2.QueryResultMeta{Revision: adapter.Revision.CurrentRevision()}, Value: skillv2.AttributeRuntimeValue(adapter.Catalog, payload.Attribute, value)}, true, nil
	case skillv2.ResourceRead:
		component, ok := adapter.Resolver.CombatComponent(payload.Entity)
		if !ok {
			return skillv2.ReadResult{}, true, fmt.Errorf("combatcomponent: entity %d has no combat component", payload.Entity)
		}
		attribute, mapped := adapter.ResourceAttribute(payload.Resource, 0)
		if !mapped {
			return skillv2.ReadResult{}, true, fmt.Errorf("combatcomponent: resource %q has no attribute mapping", payload.Resource)
		}
		return skillv2.ReadResult{Meta: skillv2.QueryResultMeta{Revision: adapter.Revision.CurrentRevision()}, Value: skillv2.ResourceRuntimeValue(component.AttributeCurrent(attribute))}, true, nil
	}
	return skillv2.ReadResult{}, false, nil
}

// PayCosts atomically validates and deducts every entry against the mapped
// attribute bases: either every entry is paid or nothing changes.
func (adapter *HostAdapter) PayCosts(payment skillv2.CostPayment) (skillv2.CommitReceipt, error) {
	component, ok := adapter.Resolver.CombatComponent(payment.Entity)
	if !ok {
		return skillv2.CommitReceipt{}, fmt.Errorf("combatcomponent: entity %d has no combat component", payment.Entity)
	}
	totals := make(map[combat.AttributeID]int64, len(payment.Entries))
	order := make([]combat.AttributeID, 0, len(payment.Entries))
	for _, entry := range payment.Entries {
		if entry.Amount < 0 {
			return skillv2.CommitReceipt{}, fmt.Errorf("combatcomponent: negative cost")
		}
		attribute, mapped := adapter.ResourceAttribute(entry.Resource, entry.Handle)
		if !mapped {
			return skillv2.CommitReceipt{}, fmt.Errorf("combatcomponent: resource %q has no attribute mapping", entry.Resource)
		}
		if _, seen := totals[attribute]; !seen {
			order = append(order, attribute)
		}
		totals[attribute] += entry.Amount
	}
	for _, attribute := range order {
		if component.AttributeBase(attribute) < totals[attribute] {
			return skillv2.CommitReceipt{}, skillv2.ErrInsufficientResource
		}
	}
	changed := false
	for _, attribute := range order {
		if amount := totals[attribute]; amount > 0 {
			component.SetAttributeBase(attribute, component.AttributeBase(attribute)-amount)
			changed = true
		}
	}
	if !changed {
		return skillv2.CommitReceipt{Revision: adapter.Revision.CurrentRevision()}, nil
	}
	receipt := adapter.Revision.CommitEffect([]EffectEvent{{Kind: "costs_paid", Entity: payment.Entity}})
	receipt.Changed = true
	return receipt, nil
}

func containsTagHandle(tags []skillv2.GameplayTagHandle, want skillv2.GameplayTagHandle) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}
