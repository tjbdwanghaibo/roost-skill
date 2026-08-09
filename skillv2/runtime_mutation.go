package skillv2

import (
	"errors"
	"reflect"
	"sort"
)

var ErrStateMutationInvalid = errors.New("skillv2: state mutation is invalid")

type StateMutationKind string

const (
	StateMutationClock            StateMutationKind = "clock"
	StateMutationCastUpsert       StateMutationKind = "cast_upsert"
	StateMutationCastRemove       StateMutationKind = "cast_remove"
	StateMutationCooldownUpsert   StateMutationKind = "cooldown_upsert"
	StateMutationCooldownRemove   StateMutationKind = "cooldown_remove"
	StateMutationResourceUpsert   StateMutationKind = "resource_upsert"
	StateMutationResourceRemove   StateMutationKind = "resource_remove"
	StateMutationAbilityUpsert    StateMutationKind = "ability_upsert"
	StateMutationAbilityRemove    StateMutationKind = "ability_remove"
	StateMutationProcessUpsert    StateMutationKind = "process_upsert"
	StateMutationProcessRemove    StateMutationKind = "process_remove"
	StateMutationPolicyUpsert     StateMutationKind = "policy_upsert"
	StateMutationPolicyRemove     StateMutationKind = "policy_remove"
	StateMutationPersistentUpsert StateMutationKind = "persistent_upsert"
	StateMutationPersistentRemove StateMutationKind = "persistent_remove"
)

// StateMutation is a closed, canonical patch union. Folding these mutations
// over a RuntimeStateSnapshot must reproduce a later snapshot exactly.
type StateMutation struct {
	Sequence                   uint64                   `json:"sequence"`
	Tick                       Tick                     `json:"tick"`
	WorldRevision              WorldRevision            `json:"world_revision"`
	LatestStateEventSequence   uint64                   `json:"latest_state_event_sequence"`
	LatestPresentationSequence uint64                   `json:"latest_presentation_sequence"`
	Kind                       StateMutationKind        `json:"kind"`
	CastID                     CastID                   `json:"cast_id,omitempty"`
	Caster                     EntityID                 `json:"caster,omitempty"`
	Owner                      EntityID                 `json:"owner,omitempty"`
	ProgramID                  string                   `json:"program_id,omitempty"`
	AbilityHandle              AbilityHandle            `json:"ability_handle,omitempty"`
	ProcessID                  ProcessID                `json:"process_id,omitempty"`
	StateHandle                StateHandle              `json:"state_handle,omitempty"`
	Binding                    StateScopeBinding        `json:"binding,omitempty"`
	Cast                       *CastStateSnapshot       `json:"cast,omitempty"`
	Cooldown                   *CooldownStateSnapshot   `json:"cooldown,omitempty"`
	Resource                   *SkillResourceSnapshot   `json:"resource,omitempty"`
	Ability                    *AbilityStateSnapshot    `json:"ability,omitempty"`
	Process                    *ProcessStateSnapshot    `json:"process,omitempty"`
	Policy                     *ActivePolicySnapshot    `json:"policy,omitempty"`
	Persistent                 *PersistentStateSnapshot `json:"persistent,omitempty"`
}

type StateMutationBatch struct {
	Mutations      []StateMutation `json:"mutations,omitempty"`
	OldestSequence uint64          `json:"oldest_sequence"`
	LatestSequence uint64          `json:"latest_sequence"`
	CursorExpired  bool            `json:"cursor_expired"`
	More           bool            `json:"more"`
	Dropped        uint64          `json:"dropped"`
}

func (runtime *Runtime) StateDeltas(after uint64, limit int) StateMutationBatch {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	batch := StateMutationBatch{LatestSequence: runtime.stateMutationSequence, Dropped: runtime.stateMutationDropped}
	if len(runtime.stateMutations) == 0 {
		batch.OldestSequence = runtime.stateMutationSequence + 1
		batch.CursorExpired = after < runtime.stateMutationSequence
		return batch
	}
	batch.OldestSequence = runtime.stateMutations[0].Sequence
	if after+1 < batch.OldestSequence {
		batch.CursorExpired = true
		return batch
	}
	index := sort.Search(len(runtime.stateMutations), func(index int) bool { return runtime.stateMutations[index].Sequence > after })
	end := len(runtime.stateMutations)
	if limit > 0 && index+limit < end {
		end = index + limit
		batch.More = true
	}
	batch.Mutations = cloneStateMutations(runtime.stateMutations[index:end])
	return batch
}

func (runtime *Runtime) commitStateMutationsLocked() {
	if !runtime.stateMutationDirty {
		return
	}
	runtime.stateMutationDirty = false
	current := runtime.stateSnapshotLocked()
	if !runtime.stateMutationReady {
		runtime.stateMutationBaseline = current
		runtime.stateMutationReady = true
		return
	}
	mutations := diffRuntimeState(runtime.stateMutationBaseline, current)
	for index := range mutations {
		runtime.stateMutationSequence++
		mutations[index].Sequence = runtime.stateMutationSequence
		mutations[index].Tick = current.Tick
		mutations[index].WorldRevision = current.WorldRevision
		mutations[index].LatestStateEventSequence = current.LatestStateEventSequence
		mutations[index].LatestPresentationSequence = current.LatestPresentationSequence
		runtime.stateMutations = append(runtime.stateMutations, cloneStateMutation(mutations[index]))
	}
	current.LatestStateMutationSequence = runtime.stateMutationSequence
	runtime.stateMutationBaseline = current
	if overflow := len(runtime.stateMutations) - runtime.options.StateMutationLimit; overflow > 0 {
		copy(runtime.stateMutations, runtime.stateMutations[overflow:])
		runtime.stateMutations = runtime.stateMutations[:runtime.options.StateMutationLimit]
		runtime.stateMutationDropped += uint64(overflow)
	}
}

func (runtime *Runtime) beginStateMutationLocked() {
	runtime.stateMutationDirty = true
}

// CaptureExternalState records changes made by an authoritative extension
// provider outside a Runtime API call. Normal Runtime mutations are captured
// automatically before their public method returns.
func (runtime *Runtime) CaptureExternalState() {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	runtime.beginStateMutationLocked()
	runtime.commitStateMutationsLocked()
}

func diffRuntimeState(before, after RuntimeStateSnapshot) []StateMutation {
	var result []StateMutation
	beforeCasts := make(map[CastID]CastStateSnapshot, len(before.Casts))
	for _, value := range before.Casts {
		beforeCasts[value.ID] = value
	}
	afterCasts := make(map[CastID]CastStateSnapshot, len(after.Casts))
	for _, value := range after.Casts {
		afterCasts[value.ID] = value
		if previous, ok := beforeCasts[value.ID]; !ok || !reflect.DeepEqual(previous, value) {
			copyValue := value
			result = append(result, StateMutation{Kind: StateMutationCastUpsert, CastID: value.ID, Cast: &copyValue})
		}
	}
	for _, value := range before.Casts {
		if _, ok := afterCasts[value.ID]; !ok {
			result = append(result, StateMutation{Kind: StateMutationCastRemove, CastID: value.ID})
		}
	}

	type skillKey struct {
		entity  EntityID
		program string
	}
	beforeCooldowns := make(map[skillKey]CooldownStateSnapshot, len(before.Cooldowns))
	for _, value := range before.Cooldowns {
		beforeCooldowns[skillKey{value.Caster, value.ProgramID}] = value
	}
	afterCooldowns := make(map[skillKey]CooldownStateSnapshot, len(after.Cooldowns))
	for _, value := range after.Cooldowns {
		key := skillKey{value.Caster, value.ProgramID}
		afterCooldowns[key] = value
		if previous, ok := beforeCooldowns[key]; !ok || previous != value {
			copyValue := value
			result = append(result, StateMutation{Kind: StateMutationCooldownUpsert, Caster: value.Caster, ProgramID: value.ProgramID, Cooldown: &copyValue})
		}
	}
	for key := range beforeCooldowns {
		if _, ok := afterCooldowns[key]; !ok {
			result = append(result, StateMutation{Kind: StateMutationCooldownRemove, Caster: key.entity, ProgramID: key.program})
		}
	}

	beforeResources := make(map[skillKey]SkillResourceSnapshot, len(before.SkillResources))
	for _, value := range before.SkillResources {
		beforeResources[skillKey{value.Caster, value.ProgramID}] = value
	}
	afterResources := make(map[skillKey]SkillResourceSnapshot, len(after.SkillResources))
	for _, value := range after.SkillResources {
		key := skillKey{value.Caster, value.ProgramID}
		afterResources[key] = value
		if previous, ok := beforeResources[key]; !ok || previous != value {
			copyValue := value
			result = append(result, StateMutation{Kind: StateMutationResourceUpsert, Caster: value.Caster, ProgramID: value.ProgramID, Resource: &copyValue})
		}
	}
	for key := range beforeResources {
		if _, ok := afterResources[key]; !ok {
			result = append(result, StateMutation{Kind: StateMutationResourceRemove, Caster: key.entity, ProgramID: key.program})
		}
	}

	type abilityKeyView struct {
		owner  EntityID
		handle AbilityHandle
	}
	beforeAbilities := make(map[abilityKeyView]AbilityStateSnapshot, len(before.Abilities))
	for _, value := range before.Abilities {
		beforeAbilities[abilityKeyView{value.Owner, value.Handle}] = value
	}
	afterAbilities := make(map[abilityKeyView]AbilityStateSnapshot, len(after.Abilities))
	for _, value := range after.Abilities {
		key := abilityKeyView{value.Owner, value.Handle}
		afterAbilities[key] = value
		if previous, ok := beforeAbilities[key]; !ok || !reflect.DeepEqual(previous, value) {
			copyValue := cloneAbilityState(value)
			result = append(result, StateMutation{Kind: StateMutationAbilityUpsert, Owner: value.Owner, AbilityHandle: value.Handle, Ability: &copyValue})
		}
	}
	for key := range beforeAbilities {
		if _, ok := afterAbilities[key]; !ok {
			result = append(result, StateMutation{Kind: StateMutationAbilityRemove, Owner: key.owner, AbilityHandle: key.handle})
		}
	}

	beforeProcesses := make(map[ProcessID]ProcessStateSnapshot, len(before.Processes))
	for _, value := range before.Processes {
		beforeProcesses[value.ID] = value
	}
	afterProcesses := make(map[ProcessID]ProcessStateSnapshot, len(after.Processes))
	for _, value := range after.Processes {
		afterProcesses[value.ID] = value
		if previous, ok := beforeProcesses[value.ID]; !ok || !reflect.DeepEqual(previous, value) {
			copyValue := cloneProcessState(value)
			result = append(result, StateMutation{Kind: StateMutationProcessUpsert, ProcessID: value.ID, Process: &copyValue})
		}
	}
	for id := range beforeProcesses {
		if _, ok := afterProcesses[id]; !ok {
			result = append(result, StateMutation{Kind: StateMutationProcessRemove, ProcessID: id})
		}
	}

	beforePolicies := make(map[skillKey]ActivePolicySnapshot, len(before.ActivePolicies))
	for _, value := range before.ActivePolicies {
		beforePolicies[skillKey{value.Caster, value.ProgramID}] = value
	}
	afterPolicies := make(map[skillKey]ActivePolicySnapshot, len(after.ActivePolicies))
	for _, value := range after.ActivePolicies {
		key := skillKey{value.Caster, value.ProgramID}
		afterPolicies[key] = value
		if previous, ok := beforePolicies[key]; !ok || previous != value {
			copyValue := value
			result = append(result, StateMutation{Kind: StateMutationPolicyUpsert, Caster: value.Caster, ProgramID: value.ProgramID, Policy: &copyValue})
		}
	}
	for key := range beforePolicies {
		if _, ok := afterPolicies[key]; !ok {
			result = append(result, StateMutation{Kind: StateMutationPolicyRemove, Caster: key.entity, ProgramID: key.program})
		}
	}

	type persistentKey struct {
		handle  StateHandle
		binding StateScopeBinding
	}
	beforePersistent := make(map[persistentKey]PersistentStateSnapshot, len(before.PersistentStates))
	for _, value := range before.PersistentStates {
		beforePersistent[persistentKey{value.Handle, value.Binding}] = value
	}
	afterPersistent := make(map[persistentKey]PersistentStateSnapshot, len(after.PersistentStates))
	for _, value := range after.PersistentStates {
		key := persistentKey{value.Handle, value.Binding}
		afterPersistent[key] = value
		if previous, ok := beforePersistent[key]; !ok || !reflect.DeepEqual(previous, value) {
			copyValue := clonePersistentState(value)
			result = append(result, StateMutation{Kind: StateMutationPersistentUpsert, StateHandle: value.Handle, Binding: value.Binding, Persistent: &copyValue})
		}
	}
	for key := range beforePersistent {
		if _, ok := afterPersistent[key]; !ok {
			result = append(result, StateMutation{Kind: StateMutationPersistentRemove, StateHandle: key.handle, Binding: key.binding})
		}
	}

	if len(result) == 0 && (before.Tick != after.Tick || before.WorldRevision != after.WorldRevision || before.LatestStateEventSequence != after.LatestStateEventSequence || before.LatestPresentationSequence != after.LatestPresentationSequence) {
		result = append(result, StateMutation{Kind: StateMutationClock})
	}
	sort.SliceStable(result, func(i, j int) bool { return mutationSortKey(result[i]) < mutationSortKey(result[j]) })
	return result
}

func mutationSortKey(value StateMutation) string {
	return string(value.Kind) + ":" + value.ProgramID + ":" + formatMutationID(uint64(value.CastID), uint64(value.Caster), uint64(value.Owner), uint64(value.AbilityHandle), uint64(value.ProcessID), uint64(value.Binding.Owner), uint64(value.Binding.Subject), value.Binding.Team)
}

func formatMutationID(values ...uint64) string {
	const digits = "0123456789abcdef"
	buffer := make([]byte, 0, len(values)*17)
	for _, value := range values {
		for shift := 60; shift >= 0; shift -= 4 {
			buffer = append(buffer, digits[value>>uint(shift)&15])
		}
		buffer = append(buffer, ':')
	}
	return string(buffer)
}

func cloneStateMutations(values []StateMutation) []StateMutation {
	result := make([]StateMutation, len(values))
	for index := range values {
		result[index] = cloneStateMutation(values[index])
	}
	return result
}

func cloneStateMutation(value StateMutation) StateMutation {
	if value.Cast != nil {
		copyValue := *value.Cast
		value.Cast = &copyValue
	}
	if value.Cooldown != nil {
		copyValue := *value.Cooldown
		value.Cooldown = &copyValue
	}
	if value.Resource != nil {
		copyValue := *value.Resource
		value.Resource = &copyValue
	}
	if value.Ability != nil {
		copyValue := cloneAbilityState(*value.Ability)
		value.Ability = &copyValue
	}
	if value.Process != nil {
		copyValue := cloneProcessState(*value.Process)
		value.Process = &copyValue
	}
	if value.Policy != nil {
		copyValue := *value.Policy
		value.Policy = &copyValue
	}
	if value.Persistent != nil {
		copyValue := clonePersistentState(*value.Persistent)
		value.Persistent = &copyValue
	}
	return value
}

func cloneAbilityState(value AbilityStateSnapshot) AbilityStateSnapshot {
	value.Tags = append([]GameplayTagHandle(nil), value.Tags...)
	value.Overlays = append([]AbilityOverlaySnapshot(nil), value.Overlays...)
	return value
}

func cloneProcessState(value ProcessStateSnapshot) ProcessStateSnapshot {
	value.Numeric = append([]NumericPropertySnapshot(nil), value.Numeric...)
	return value
}

func clonePersistentState(value PersistentStateSnapshot) PersistentStateSnapshot {
	value.Value = cloneStateRuntimeValue(value.Value)
	value.ClearOn = append([]string(nil), value.ClearOn...)
	return value
}

// ApplyStateMutation is the canonical client reducer used by tests and
// production consumers.
func ApplyStateMutation(snapshot *RuntimeStateSnapshot, mutation StateMutation) error {
	if snapshot == nil || mutation.Sequence == 0 || mutation.Kind == "" {
		return ErrStateMutationInvalid
	}
	snapshot.Tick, snapshot.WorldRevision = mutation.Tick, mutation.WorldRevision
	snapshot.LatestStateEventSequence = mutation.LatestStateEventSequence
	snapshot.LatestPresentationSequence = mutation.LatestPresentationSequence
	snapshot.LatestStateMutationSequence = mutation.Sequence
	switch mutation.Kind {
	case StateMutationClock:
	case StateMutationCastUpsert:
		if mutation.Cast == nil {
			return ErrStateMutationInvalid
		}
		snapshot.Casts = upsertBy(snapshot.Casts, *mutation.Cast, func(v CastStateSnapshot) CastID { return v.ID })
	case StateMutationCastRemove:
		snapshot.Casts = removeBy(snapshot.Casts, mutation.CastID, func(v CastStateSnapshot) CastID { return v.ID })
	case StateMutationCooldownUpsert:
		if mutation.Cooldown == nil {
			return ErrStateMutationInvalid
		}
		snapshot.Cooldowns = upsertSkillState(snapshot.Cooldowns, *mutation.Cooldown, func(v CooldownStateSnapshot) (EntityID, string) { return v.Caster, v.ProgramID })
	case StateMutationCooldownRemove:
		snapshot.Cooldowns = removeSkillState(snapshot.Cooldowns, mutation.Caster, mutation.ProgramID, func(v CooldownStateSnapshot) (EntityID, string) { return v.Caster, v.ProgramID })
	case StateMutationResourceUpsert:
		if mutation.Resource == nil {
			return ErrStateMutationInvalid
		}
		snapshot.SkillResources = upsertSkillState(snapshot.SkillResources, *mutation.Resource, func(v SkillResourceSnapshot) (EntityID, string) { return v.Caster, v.ProgramID })
	case StateMutationResourceRemove:
		snapshot.SkillResources = removeSkillState(snapshot.SkillResources, mutation.Caster, mutation.ProgramID, func(v SkillResourceSnapshot) (EntityID, string) { return v.Caster, v.ProgramID })
	case StateMutationAbilityUpsert:
		if mutation.Ability == nil {
			return ErrStateMutationInvalid
		}
		value := cloneAbilityState(*mutation.Ability)
		snapshot.Abilities = upsertAbility(snapshot.Abilities, value)
	case StateMutationAbilityRemove:
		snapshot.Abilities = removeAbility(snapshot.Abilities, mutation.Owner, mutation.AbilityHandle)
	case StateMutationProcessUpsert:
		if mutation.Process == nil {
			return ErrStateMutationInvalid
		}
		value := cloneProcessState(*mutation.Process)
		snapshot.Processes = upsertBy(snapshot.Processes, value, func(v ProcessStateSnapshot) ProcessID { return v.ID })
	case StateMutationProcessRemove:
		snapshot.Processes = removeBy(snapshot.Processes, mutation.ProcessID, func(v ProcessStateSnapshot) ProcessID { return v.ID })
	case StateMutationPolicyUpsert:
		if mutation.Policy == nil {
			return ErrStateMutationInvalid
		}
		snapshot.ActivePolicies = upsertSkillState(snapshot.ActivePolicies, *mutation.Policy, func(v ActivePolicySnapshot) (EntityID, string) { return v.Caster, v.ProgramID })
	case StateMutationPolicyRemove:
		snapshot.ActivePolicies = removeSkillState(snapshot.ActivePolicies, mutation.Caster, mutation.ProgramID, func(v ActivePolicySnapshot) (EntityID, string) { return v.Caster, v.ProgramID })
	case StateMutationPersistentUpsert:
		if mutation.Persistent == nil {
			return ErrStateMutationInvalid
		}
		value := clonePersistentState(*mutation.Persistent)
		snapshot.PersistentStates = upsertPersistent(snapshot.PersistentStates, value)
	case StateMutationPersistentRemove:
		snapshot.PersistentStates = removePersistent(snapshot.PersistentStates, mutation.StateHandle, mutation.Binding)
	default:
		return ErrStateMutationInvalid
	}
	sortRuntimeStateSnapshot(snapshot)
	return nil
}

type orderedID interface{ ~uint64 }

func upsertBy[T any, K orderedID](values []T, value T, key func(T) K) []T {
	for i := range values {
		if key(values[i]) == key(value) {
			values[i] = value
			return values
		}
	}
	return append(values, value)
}
func removeBy[T any, K orderedID](values []T, target K, key func(T) K) []T {
	result := values[:0]
	for _, value := range values {
		if key(value) != target {
			result = append(result, value)
		}
	}
	return result
}
func upsertSkillState[T any](values []T, value T, key func(T) (EntityID, string)) []T {
	entity, program := key(value)
	for i := range values {
		e, p := key(values[i])
		if e == entity && p == program {
			values[i] = value
			return values
		}
	}
	return append(values, value)
}
func removeSkillState[T any](values []T, entity EntityID, program string, key func(T) (EntityID, string)) []T {
	result := values[:0]
	for _, value := range values {
		e, p := key(value)
		if e != entity || p != program {
			result = append(result, value)
		}
	}
	return result
}
func upsertAbility(values []AbilityStateSnapshot, value AbilityStateSnapshot) []AbilityStateSnapshot {
	for i := range values {
		if values[i].Owner == value.Owner && values[i].Handle == value.Handle {
			values[i] = value
			return values
		}
	}
	return append(values, value)
}
func removeAbility(values []AbilityStateSnapshot, owner EntityID, handle AbilityHandle) []AbilityStateSnapshot {
	result := values[:0]
	for _, value := range values {
		if value.Owner != owner || value.Handle != handle {
			result = append(result, value)
		}
	}
	return result
}
func upsertPersistent(values []PersistentStateSnapshot, value PersistentStateSnapshot) []PersistentStateSnapshot {
	for i := range values {
		if values[i].Handle == value.Handle && values[i].Binding == value.Binding {
			values[i] = value
			return values
		}
	}
	return append(values, value)
}
func removePersistent(values []PersistentStateSnapshot, handle StateHandle, binding StateScopeBinding) []PersistentStateSnapshot {
	result := values[:0]
	for _, value := range values {
		if value.Handle != handle || value.Binding != binding {
			result = append(result, value)
		}
	}
	return result
}

func sortRuntimeStateSnapshot(snapshot *RuntimeStateSnapshot) {
	sort.Slice(snapshot.Casts, func(i, j int) bool { return snapshot.Casts[i].ID < snapshot.Casts[j].ID })
	sort.Slice(snapshot.Cooldowns, func(i, j int) bool {
		if snapshot.Cooldowns[i].Caster != snapshot.Cooldowns[j].Caster {
			return snapshot.Cooldowns[i].Caster < snapshot.Cooldowns[j].Caster
		}
		return snapshot.Cooldowns[i].ProgramID < snapshot.Cooldowns[j].ProgramID
	})
	sort.Slice(snapshot.SkillResources, func(i, j int) bool {
		if snapshot.SkillResources[i].Caster != snapshot.SkillResources[j].Caster {
			return snapshot.SkillResources[i].Caster < snapshot.SkillResources[j].Caster
		}
		return snapshot.SkillResources[i].ProgramID < snapshot.SkillResources[j].ProgramID
	})
	sort.Slice(snapshot.Abilities, func(i, j int) bool {
		if snapshot.Abilities[i].Owner != snapshot.Abilities[j].Owner {
			return snapshot.Abilities[i].Owner < snapshot.Abilities[j].Owner
		}
		return snapshot.Abilities[i].Handle < snapshot.Abilities[j].Handle
	})
	sort.Slice(snapshot.Processes, func(i, j int) bool { return snapshot.Processes[i].ID < snapshot.Processes[j].ID })
	sort.Slice(snapshot.ActivePolicies, func(i, j int) bool {
		if snapshot.ActivePolicies[i].Caster != snapshot.ActivePolicies[j].Caster {
			return snapshot.ActivePolicies[i].Caster < snapshot.ActivePolicies[j].Caster
		}
		return snapshot.ActivePolicies[i].ProgramID < snapshot.ActivePolicies[j].ProgramID
	})
}
