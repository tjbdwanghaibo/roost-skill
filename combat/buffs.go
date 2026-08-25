package combat

// BuffID identifies a buff definition in the host's catalog.
type BuffID uint32

// BuffInstanceID identifies one application inside a container. Instance ids
// are a per-container sequence, so they double as AttributeSet modifier
// handles and as deterministic ordering keys.
type BuffInstanceID uint64

// Tag classifies buffs for dispel and immunity matching.
type Tag string

// BuffStackPolicy controls what re-applying an already-present buff does.
type BuffStackPolicy uint8

const (
	// BuffRefresh adds a stack (up to MaxStacks) and resets the duration.
	BuffRefresh BuffStackPolicy = iota
	// BuffExtend adds a stack and extends the remaining duration by the
	// spec's (tenacity-adjusted) duration.
	BuffExtend
	// BuffIgnore keeps the existing instance untouched.
	BuffIgnore
)

// BuffSpec is a buff definition. Specs are value data owned by the caller;
// the container copies what it stores.
type BuffSpec struct {
	ID   BuffID
	Tags []Tag
	// GrantsImmunityTo blocks later applications of any buff carrying one of
	// these tags while this buff is active.
	GrantsImmunityTo []Tag
	MaxStacks        int64 // <=1 means no stacking
	StackPolicy      BuffStackPolicy
	DurationTicks    int64 // <=0 means permanent until dispelled
	// TenacityAffected durations are reduced by the container's tenacity:
	// duration * (10000 - tenacityBP) / 10000, floored at 1 tick.
	TenacityAffected bool
	// Modifiers are granted to the linked AttributeSet per stack.
	Modifiers []Modifier
}

// BuffApplyOutcome reports what Apply did.
type BuffApplyOutcome uint8

const (
	BuffApplied BuffApplyOutcome = iota
	BuffStacked
	BuffRefreshed
	BuffIgnored
	BuffBlockedImmune
)

// BuffInstance is one active buff.
type BuffInstance struct {
	Instance    BuffInstanceID
	Spec        BuffSpec
	Stacks      int64
	Source      int64
	AppliedTick int64
	DueTick     int64 // 0 means permanent
}

// BuffContainer holds a combatant's active buffs with stacking, dispel-tag,
// immunity, and tenacity semantics. Instances keep application order (by
// instance id), so every walk is deterministic. Linking an AttributeSet
// makes buff modifiers materialize as attribute grants automatically, scaled
// by stack count.
type BuffContainer struct {
	sequence   uint64
	active     []BuffInstance
	tenacityBP int64
	attributes *AttributeSet
}

func NewBuffContainer() *BuffContainer { return &BuffContainer{} }

// LinkAttributes connects the container to an AttributeSet; active buffs are
// re-granted immediately.
func (container *BuffContainer) LinkAttributes(attributes *AttributeSet) {
	container.attributes = attributes
	for index := range container.active {
		container.grantModifiers(&container.active[index])
	}
}

// SetTenacityBP sets crowd-control resistance in basis points (2000 = -20%
// duration on tenacity-affected buffs). Values are clamped into [0, 10000].
func (container *BuffContainer) SetTenacityBP(tenacityBP int64) {
	if tenacityBP < 0 {
		tenacityBP = 0
	}
	if tenacityBP > BasisPointScale {
		tenacityBP = BasisPointScale
	}
	container.tenacityBP = tenacityBP
}

func (container *BuffContainer) effectiveDuration(spec BuffSpec) int64 {
	if spec.DurationTicks <= 0 {
		return 0
	}
	if !spec.TenacityAffected || container.tenacityBP == 0 {
		return spec.DurationTicks
	}
	duration := ScaleBasisPoints(spec.DurationTicks, BasisPointScale-container.tenacityBP)
	if duration < 1 {
		duration = 1
	}
	return duration
}

// ImmuneTo reports whether an active buff grants immunity against the tag.
func (container *BuffContainer) ImmuneTo(tags []Tag) bool {
	for _, instance := range container.active {
		for _, immunity := range instance.Spec.GrantsImmunityTo {
			for _, tag := range tags {
				if tag == immunity {
					return true
				}
			}
		}
	}
	return false
}

// Apply adds (or re-applies) a buff at the given tick.
func (container *BuffContainer) Apply(spec BuffSpec, tick, source int64) (BuffInstanceID, BuffApplyOutcome) {
	if container.ImmuneTo(spec.Tags) {
		return 0, BuffBlockedImmune
	}
	duration := container.effectiveDuration(spec)
	for index := range container.active {
		instance := &container.active[index]
		if instance.Spec.ID != spec.ID {
			continue
		}
		switch spec.StackPolicy {
		case BuffIgnore:
			return instance.Instance, BuffIgnored
		case BuffExtend:
			if duration > 0 {
				if instance.DueTick == 0 {
					instance.DueTick = saturatingInt64Add(tick, duration)
				} else {
					instance.DueTick = saturatingInt64Add(instance.DueTick, duration)
				}
			}
		default: // BuffRefresh
			if duration > 0 {
				instance.DueTick = saturatingInt64Add(tick, duration)
			}
		}
		outcome := BuffRefreshed
		maxStacks := maxInt64(spec.MaxStacks, 1)
		if instance.Stacks < maxStacks {
			instance.Stacks++
			outcome = BuffStacked
		}
		instance.Spec = copyBuffSpec(spec)
		instance.Source = source
		container.grantModifiers(instance)
		return instance.Instance, outcome
	}
	container.sequence++
	instance := BuffInstance{
		Instance: BuffInstanceID(container.sequence), Spec: copyBuffSpec(spec),
		Stacks: 1, Source: source, AppliedTick: tick,
	}
	if duration > 0 {
		instance.DueTick = saturatingInt64Add(tick, duration)
	}
	container.active = append(container.active, instance)
	container.grantModifiers(&container.active[len(container.active)-1])
	return instance.Instance, BuffApplied
}

// Dispel removes up to limit active buffs carrying the tag, newest first
// (limit <= 0 means all). It returns the removed instances.
func (container *BuffContainer) Dispel(tag Tag, limit int) []BuffInstance {
	var removed []BuffInstance
	for index := len(container.active) - 1; index >= 0; index-- {
		if limit > 0 && len(removed) >= limit {
			break
		}
		if !containsTag(container.active[index].Spec.Tags, tag) {
			continue
		}
		removed = append(removed, container.removeAt(index))
	}
	return removed
}

// Remove drops one instance by id.
func (container *BuffContainer) Remove(id BuffInstanceID) (BuffInstance, bool) {
	for index := range container.active {
		if container.active[index].Instance == id {
			return container.removeAt(index), true
		}
	}
	return BuffInstance{}, false
}

// Tick expires every instance due at or before now and returns them in
// application order.
func (container *BuffContainer) Tick(now int64) []BuffInstance {
	var expired []BuffInstance
	for index := 0; index < len(container.active); {
		instance := container.active[index]
		if instance.DueTick != 0 && instance.DueTick <= now {
			expired = append(expired, container.removeAt(index))
			continue
		}
		index++
	}
	return expired
}

// Active returns the live instances in application order. The slice is a
// copy; instances inside are the container's values.
func (container *BuffContainer) Active() []BuffInstance {
	return append([]BuffInstance(nil), container.active...)
}

// HasTag reports whether any active buff carries the tag.
func (container *BuffContainer) HasTag(tag Tag) bool {
	for _, instance := range container.active {
		if containsTag(instance.Spec.Tags, tag) {
			return true
		}
	}
	return false
}

func (container *BuffContainer) removeAt(index int) BuffInstance {
	instance := container.active[index]
	container.active = append(container.active[:index], container.active[index+1:]...)
	if container.attributes != nil {
		container.attributes.Revoke(ModifierHandle(instance.Instance))
	}
	return instance
}

func (container *BuffContainer) grantModifiers(instance *BuffInstance) {
	if container.attributes == nil || len(instance.Spec.Modifiers) == 0 {
		return
	}
	scaled := make([]Modifier, len(instance.Spec.Modifiers))
	for index, modifier := range instance.Spec.Modifiers {
		scaled[index] = Modifier{
			Attribute: modifier.Attribute,
			Flat:      saturatingInt64Mul(modifier.Flat, instance.Stacks),
			RateBP:    saturatingInt64Mul(modifier.RateBP, instance.Stacks),
		}
	}
	container.attributes.Grant(ModifierHandle(instance.Instance), scaled...)
}

func copyBuffSpec(spec BuffSpec) BuffSpec {
	spec.Tags = append([]Tag(nil), spec.Tags...)
	spec.GrantsImmunityTo = append([]Tag(nil), spec.GrantsImmunityTo...)
	spec.Modifiers = append([]Modifier(nil), spec.Modifiers...)
	return spec
}

func containsTag(tags []Tag, want Tag) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

// BuffContainerState is the container's persistable state. Instance ids are
// part of the state so restored containers keep issuing unique ids and
// attribute grant handles stay stable across a reload.
type BuffContainerState struct {
	Sequence   uint64         `json:"sequence"`
	TenacityBP int64          `json:"tenacity_bp,omitempty"`
	Active     []BuffInstance `json:"active,omitempty"`
}

// State snapshots the container. The result shares nothing with the live
// container and is safe to retain across further mutations.
func (container *BuffContainer) State() BuffContainerState {
	state := BuffContainerState{Sequence: container.sequence, TenacityBP: container.tenacityBP}
	if len(container.active) > 0 {
		state.Active = make([]BuffInstance, len(container.active))
		for index, instance := range container.active {
			instance.Spec = copyBuffSpec(instance.Spec)
			state.Active[index] = instance
		}
	}
	return state
}

// RestoreBuffContainer rebuilds a container from a State snapshot. Link an
// AttributeSet afterwards to re-materialize modifier grants.
func RestoreBuffContainer(state BuffContainerState) *BuffContainer {
	container := NewBuffContainer()
	container.sequence = state.Sequence
	container.SetTenacityBP(state.TenacityBP)
	container.active = make([]BuffInstance, len(state.Active))
	for index, instance := range state.Active {
		instance.Spec = copyBuffSpec(instance.Spec)
		container.active[index] = instance
	}
	return container
}
