package combatcomponent

import (
	"fmt"

	"github.com/tjbdwanghaibo/roost-skill/skillv2"

	"github.com/tjbdwanghaibo/roost-skill/combat"
)

// AttributeModifierBuffID is the reserved buff definition id carrying
// standalone AttributeModifierCommand grants. Modifier commands apply as
// independent instances under this id (each with its own duration, matching
// the MemoryHost's per-command modifier semantics), tagged
// "attribute_modifier" so dispels can address or avoid them.
const AttributeModifierBuffID combat.BuffID = 0xFFFFFFFF

// AttributeModifierTag classifies bridge-created modifier instances.
const AttributeModifierTag combat.Tag = "attribute_modifier"

// StatusBridge lands the skillv2 status and attribute-modifier effect
// commands on combat containers: StatusCommand becomes a BuffContainer
// application driven by the status catalog entry, Remove/Dispel become
// container removals, and AttributeModifierCommand becomes an independent
// timed modifier grant. It follows the MemoryHost event vocabulary
// (status_applied, status_immune, status_removed, status_dispelled,
// attribute_modifier_applied).
//
// One deliberate semantic difference from the MemoryHost: mul_bp modifiers
// aggregate additively in basis-point deltas (combat.AttributeSet's rule,
// order-independent and exactly reversible for transaction rollback), while
// the MemoryHost chains them multiplicatively. Two +20% modifiers yield
// +40% here and +44% there; pick one host semantics per game and keep it.
type StatusBridge struct {
	Resolver Resolver
	Revision RevisionSource
	// Catalog supplies status policies (stacking, dispel category, immunity
	// tags, attribute modifiers, tenacity and duration caps).
	Catalog skillv2.GameplayCatalog
	// CurrentTick supplies the authoritative tick durations anchor to.
	CurrentTick func() skillv2.Tick
	// HasGameplayTag reports whether the target carries a gameplay tag —
	// immunity tags are world facts the component does not own. nil disables
	// immunity-tag checks.
	HasGameplayTag func(target skillv2.EntityID, tag skillv2.GameplayTagHandle) bool
}

// Apply handles a status-domain effect command. handled=false means the
// payload is not a status command and the caller must process it.
func (bridge *StatusBridge) Apply(command skillv2.EffectCommand) (skillv2.EffectResult, bool, error) {
	switch payload := command.Payload.(type) {
	case skillv2.StatusCommand:
		result, err := bridge.applyStatus(payload)
		return result, true, err
	case skillv2.RemoveStatusCommand:
		result, err := bridge.removeStatus(payload)
		return result, true, err
	case skillv2.DispelStatusCommand:
		result, err := bridge.dispelStatus(payload)
		return result, true, err
	case skillv2.AttributeModifierCommand:
		result, err := bridge.applyAttributeModifier(payload)
		return result, true, err
	}
	return skillv2.EffectResult{}, false, nil
}

func (bridge *StatusBridge) statusPolicy(handle skillv2.StatusHandle) (skillv2.StatusCatalogEntry, bool) {
	for _, entry := range bridge.Catalog.Statuses.Entries {
		if entry.Handle == handle {
			return entry, true
		}
	}
	return skillv2.StatusCatalogEntry{}, false
}

// buffSpec translates a status catalog entry plus one application command
// into the combat.BuffSpec the container applies.
func buffSpec(policy skillv2.StatusCatalogEntry, command skillv2.StatusCommand) combat.BuffSpec {
	spec := combat.BuffSpec{
		ID:               combat.BuffID(policy.Handle),
		MaxStacks:        int64(policy.MaxStacks),
		DurationTicks:    int64(command.DurationTicks),
		TenacityAffected: policy.TenacityPolicy == "scale_duration",
		MaxDurationTicks: int64(policy.MaximumDurationTicks),
	}
	if command.MaxStacks > 0 && (spec.MaxStacks <= 0 || int64(command.MaxStacks) < spec.MaxStacks) {
		spec.MaxStacks = int64(command.MaxStacks)
	}
	if policy.DispelCategory != "" {
		spec.Tags = append(spec.Tags, combat.Tag(policy.DispelCategory))
	}
	if policy.Category != "" && policy.Category != policy.DispelCategory {
		spec.Tags = append(spec.Tags, combat.Tag(policy.Category))
	}
	switch policy.RefreshPolicy {
	case "ignore":
		spec.StackPolicy = combat.BuffIgnore
	case "extend":
		spec.StackPolicy = combat.BuffExtend
	default: // "", "refresh", and "replace" (replace removes first, below)
		spec.StackPolicy = combat.BuffRefresh
	}
	for _, modifier := range policy.AttributeModifiers {
		translated := combat.Modifier{Attribute: combat.AttributeID(modifier.Attribute)}
		if modifier.Operation == "mul_bp" {
			// Catalog mul_bp values are absolute rates (12000 = ×1.2); the
			// AttributeSet aggregates additive deltas (+2000).
			translated.RateBP = modifier.Value - combat.BasisPointScale
		} else {
			translated.Flat = modifier.Value
		}
		spec.Modifiers = append(spec.Modifiers, translated)
	}
	return spec
}

func (bridge *StatusBridge) buffStacks(component *CombatComponent, id combat.BuffID) int {
	stacks := 0
	for _, instance := range component.ActiveBuffs() {
		if instance.Spec.ID == id {
			stacks += int(instance.Stacks)
		}
	}
	return stacks
}

func (bridge *StatusBridge) applyStatus(command skillv2.StatusCommand) (skillv2.EffectResult, error) {
	component, ok := bridge.Resolver.CombatComponent(command.Target)
	if !ok || !component.Combatant().Alive {
		return skillv2.EffectResult{Commit: skillv2.CommitReceipt{Revision: bridge.Revision.CurrentRevision()}, Payload: skillv2.StatusEffectResult{ResultOutcome: skillv2.ResultOutcome{FailureReason: skillv2.ExpectedFailureInvalidTarget}}}, nil
	}
	policy, ok := bridge.statusPolicy(command.Status)
	if !ok {
		return skillv2.EffectResult{}, fmt.Errorf("combatcomponent: unknown status handle %d", command.Status)
	}
	if command.DurationTicks <= 0 {
		return skillv2.EffectResult{}, fmt.Errorf("combatcomponent: status duration must be positive")
	}
	context := command.Event
	context.Owner, context.Target, context.EffectIndex = command.SourceOwner, command.Target, command.Meta.EffectIndex
	previousStacks := bridge.buffStacks(component, combat.BuffID(policy.Handle))
	if bridge.HasGameplayTag != nil {
		for _, immunity := range policy.ImmunityTags {
			if bridge.HasGameplayTag(command.Target, immunity) {
				context.Result = "immune"
				receipt := bridge.Revision.CommitEffect([]EffectEvent{{Kind: "status_immune", Entity: command.Target, Context: context}})
				receipt.Changed = true
				return skillv2.EffectResult{Commit: receipt, Payload: skillv2.StatusEffectResult{ResultOutcome: skillv2.ResultOutcome{Succeeded: true}, Result: skillv2.StatusResult{Immune: true, PreviousStacks: previousStacks, CurrentStacks: previousStacks}}}, nil
			}
		}
	}
	spec := buffSpec(policy, command)
	if policy.RefreshPolicy == "replace" {
		for _, instance := range component.ActiveBuffs() {
			if instance.Spec.ID == spec.ID {
				component.RemoveBuff(instance.Instance)
			}
		}
	}
	tick := int64(bridge.CurrentTick())
	stacks := command.Stacks
	if stacks <= 0 {
		stacks = 1
	}
	dueTick := int64(0)
	blocked := false
	for applied := 0; applied < stacks; applied++ {
		_, outcome := component.ApplyBuff(spec, tick, int64(command.SourceOwner))
		if outcome == combat.BuffBlockedImmune {
			blocked = true
			break
		}
	}
	if blocked && previousStacks == bridge.buffStacks(component, spec.ID) {
		context.Result = "immune"
		receipt := bridge.Revision.CommitEffect([]EffectEvent{{Kind: "status_immune", Entity: command.Target, Context: context}})
		receipt.Changed = true
		return skillv2.EffectResult{Commit: receipt, Payload: skillv2.StatusEffectResult{ResultOutcome: skillv2.ResultOutcome{Succeeded: true}, Result: skillv2.StatusResult{Immune: true, PreviousStacks: previousStacks, CurrentStacks: previousStacks}}}, nil
	}
	for _, instance := range component.ActiveBuffs() {
		if instance.Spec.ID == spec.ID && instance.DueTick > dueTick {
			dueTick = instance.DueTick
		}
	}
	context.Result = "status_applied"
	receipt := bridge.Revision.CommitEffect([]EffectEvent{{Kind: "status_applied", Entity: command.Target, Context: context}})
	receipt.Changed = true
	currentStacks := bridge.buffStacks(component, spec.ID)
	return skillv2.EffectResult{Commit: receipt, Payload: skillv2.StatusEffectResult{ResultOutcome: skillv2.ResultOutcome{Succeeded: true}, Result: skillv2.StatusResult{Applied: true, PreviousStacks: previousStacks, CurrentStacks: currentStacks, DueTick: skillv2.Tick(dueTick)}}}, nil
}

func (bridge *StatusBridge) removeStatus(command skillv2.RemoveStatusCommand) (skillv2.EffectResult, error) {
	component, ok := bridge.Resolver.CombatComponent(command.Target)
	if !ok {
		return skillv2.EffectResult{Commit: skillv2.CommitReceipt{Revision: bridge.Revision.CurrentRevision()}, Payload: skillv2.StatusEffectResult{ResultOutcome: skillv2.ResultOutcome{FailureReason: skillv2.ExpectedFailureInvalidTarget}}}, nil
	}
	id := combat.BuffID(command.Status)
	previousStacks := bridge.buffStacks(component, id)
	removed := 0
	for _, instance := range component.ActiveBuffs() {
		if instance.Spec.ID != id {
			continue
		}
		if command.SourceOwner != 0 && instance.Source != int64(command.SourceOwner) {
			continue
		}
		if _, ok := component.RemoveBuff(instance.Instance); ok {
			removed++
		}
	}
	if removed == 0 {
		return skillv2.EffectResult{Commit: skillv2.CommitReceipt{Revision: bridge.Revision.CurrentRevision()}, Payload: skillv2.StatusEffectResult{ResultOutcome: skillv2.ResultOutcome{Succeeded: true}, Result: skillv2.StatusResult{PreviousStacks: previousStacks, CurrentStacks: previousStacks}}}, nil
	}
	context := command.Event
	context.Owner, context.Target, context.Result = command.SourceOwner, command.Target, "status_removed"
	receipt := bridge.Revision.CommitEffect([]EffectEvent{{Kind: "status_removed", Entity: command.Target, Context: context}})
	receipt.Changed = true
	currentStacks := bridge.buffStacks(component, id)
	return skillv2.EffectResult{Commit: receipt, Payload: skillv2.StatusEffectResult{ResultOutcome: skillv2.ResultOutcome{Succeeded: true}, Result: skillv2.StatusResult{Removed: true, PreviousStacks: previousStacks, CurrentStacks: currentStacks, RemovedStacks: previousStacks - currentStacks}}}, nil
}

func (bridge *StatusBridge) dispelStatus(command skillv2.DispelStatusCommand) (skillv2.EffectResult, error) {
	component, ok := bridge.Resolver.CombatComponent(command.Target)
	if !ok {
		return skillv2.EffectResult{Commit: skillv2.CommitReceipt{Revision: bridge.Revision.CurrentRevision()}, Payload: skillv2.StatusEffectResult{ResultOutcome: skillv2.ResultOutcome{FailureReason: skillv2.ExpectedFailureInvalidTarget}}}, nil
	}
	tag := combat.Tag(command.Category)
	previousStacks := 0
	for _, instance := range component.ActiveBuffs() {
		if containsBuffTag(instance.Spec.Tags, tag) {
			previousStacks += int(instance.Stacks)
		}
	}
	removed := component.DispelBuffs(tag, command.Count)
	if len(removed) == 0 {
		return skillv2.EffectResult{Commit: skillv2.CommitReceipt{Revision: bridge.Revision.CurrentRevision()}, Payload: skillv2.StatusEffectResult{ResultOutcome: skillv2.ResultOutcome{Succeeded: true}, Result: skillv2.StatusResult{PreviousStacks: previousStacks, CurrentStacks: previousStacks}}}, nil
	}
	removedStacks := 0
	for _, instance := range removed {
		removedStacks += int(instance.Stacks)
	}
	context := command.Event
	context.Target, context.Result = command.Target, "status_dispelled"
	receipt := bridge.Revision.CommitEffect([]EffectEvent{{Kind: "status_dispelled", Entity: command.Target, Context: context}})
	receipt.Changed = true
	return skillv2.EffectResult{Commit: receipt, Payload: skillv2.StatusEffectResult{ResultOutcome: skillv2.ResultOutcome{Succeeded: true}, Result: skillv2.StatusResult{Removed: true, PreviousStacks: previousStacks, CurrentStacks: previousStacks - removedStacks, RemovedStacks: removedStacks}}}, nil
}

func (bridge *StatusBridge) applyAttributeModifier(command skillv2.AttributeModifierCommand) (skillv2.EffectResult, error) {
	component, ok := bridge.Resolver.CombatComponent(command.Target)
	if !ok {
		return skillv2.EffectResult{Commit: skillv2.CommitReceipt{Revision: bridge.Revision.CurrentRevision()}, Payload: skillv2.AttributeModifierEffectResult{ResultOutcome: skillv2.ResultOutcome{FailureReason: skillv2.ExpectedFailureInvalidTarget}}}, nil
	}
	if command.Operation != "add" && command.Operation != "mul_bp" {
		return skillv2.EffectResult{}, fmt.Errorf("combatcomponent: unsupported modifier operation %q", command.Operation)
	}
	if command.DurationTicks <= 0 {
		return skillv2.EffectResult{}, fmt.Errorf("combatcomponent: modifier duration must be positive")
	}
	modifier := combat.Modifier{Attribute: combat.AttributeID(command.Attribute)}
	if command.Operation == "mul_bp" {
		modifier.RateBP = command.Value - combat.BasisPointScale
	} else {
		modifier.Flat = command.Value
	}
	spec := combat.BuffSpec{
		ID: AttributeModifierBuffID, Tags: []combat.Tag{AttributeModifierTag},
		StackPolicy: combat.BuffIndependent, DurationTicks: int64(command.DurationTicks),
		Modifiers: []combat.Modifier{modifier},
	}
	tick := int64(bridge.CurrentTick())
	component.ApplyBuff(spec, tick, int64(command.SourceOwner))
	context := command.Event
	context.Owner, context.Target, context.Result = command.SourceOwner, command.Target, "attribute_modifier_applied"
	receipt := bridge.Revision.CommitEffect([]EffectEvent{{Kind: "attribute_modifier_applied", Entity: command.Target, Context: context}})
	receipt.Changed = true
	return skillv2.EffectResult{Commit: receipt, Payload: skillv2.AttributeModifierEffectResult{ResultOutcome: skillv2.ResultOutcome{Succeeded: true}, Result: skillv2.AttributeModifierResult{Applied: true, DueTick: skillv2.Tick(tick) + command.DurationTicks}}}, nil
}

func containsBuffTag(tags []combat.Tag, want combat.Tag) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}
