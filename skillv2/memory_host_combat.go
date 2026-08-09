package skillv2

func (host *MemoryHost) applyDamageLocked(command DamageCommand) (EffectResult, error) {
	if len(fixedCombatPipeline) != 12 {
		panic("skillv2: combat pipeline invariant violated")
	}
	if policy := host.gameplay.Combat.FormulaPolicy; policy != "" && policy != "twelve_stage_v1" {
		return EffectResult{}, ErrCombatPolicyUnsupported
	}
	if len(host.gameplay.Combat.DamageTypes) > 0 && !containsDamageTypeHandle(host.gameplay.Combat.DamageTypes, command.DamageType) {
		return EffectResult{}, ErrCombatHandleInvalid
	}
	if len(host.gameplay.Elements.Entries) > 0 && !containsElementHandle(host.gameplay.Elements.Entries, command.Element) {
		return EffectResult{}, ErrCombatHandleInvalid
	}
	target, ok := host.entities[command.Target]
	if !ok || !target.Alive {
		return EffectResult{Commit: CommitReceipt{Revision: host.revision}, Payload: DamageEffectResult{ResultOutcome: failedResultOutcome(ExpectedFailureInvalidTarget)}}, nil
	}
	source := MemoryEntity{}
	if command.Source != 0 {
		var sourceExists bool
		source, sourceExists = host.entities[command.Source]
		if !sourceExists {
			return EffectResult{Commit: CommitReceipt{Revision: host.revision}, Payload: DamageEffectResult{ResultOutcome: failedResultOutcome(ExpectedFailureInvalidTarget)}}, nil
		}
	}
	result := DamageResult{Attempted: maxInt64(command.Amount, 0)}
	amount := result.Attempted
	context := command.Event
	context.Source, context.Owner, context.Target = command.Source, command.Owner, command.Target
	context.EffectIndex, context.DamageType, context.Element = command.Meta.EffectIndex, command.DamageType, command.Element
	context.Result = combatResultHit

	if host.spellTag != 0 && containsGameplayTag(command.Tags, host.spellTag) {
		_, hook, ok := host.firstCombatHookStatusLocked(command.Target, "spell_shield")
		if ok {
			result.Immune = true
			result.CombatHooks = append(result.CombatHooks, hook)
			context.Result = hook
			host.consumeCombatHookStatusLocked(command.Target, hook)
			return host.commitDamageLocked(command, result, context), nil
		}
	}
	if target.DamageImmune || target.SpellShield {
		result.Immune = true
		context.Result = combatResultImmune
		if target.SpellShield {
			target.SpellShield = false
			host.entities[command.Target] = target
		}
		return host.commitDamageLocked(command, result, context), nil
	}
	if target.Dodge {
		result.Dodged, amount, context.Result = true, 0, combatResultDodged
	} else if target.Parry {
		result.Parried, amount, context.Result = true, 0, combatResultParried
	} else if target.Block {
		result.Blocked, amount, context.Result = true, amount/2, combatResultBlocked
	}

	resistance := int64(0)
	switch command.DamageType {
	case 1:
		resistance = target.Armor
	case 2:
		resistance = target.MagicResistance
	}
	resistance = maxInt64(0, saturatingInt64Sub(resistance, source.Penetration))
	if resistance > 0 {
		denominator := saturatingInt64Add(10000, saturatingInt64Mul(resistance, 100))
		amount = saturatingInt64Mul(amount, 10000) / denominator
	}
	result.Mitigated = amount

	elementBP := target.ElementMultipliers[command.Element]
	if elementBP == 0 {
		elementBP = 10000
	}
	amount = scaleBasisPoints(amount, elementBP)
	dealtBP, takenBP := source.DamageDealtBP, target.DamageTakenBP
	if dealtBP == 0 {
		dealtBP = 10000
	}
	if takenBP == 0 {
		takenBP = 10000
	}
	amount = scaleBasisPoints(scaleBasisPoints(amount, dealtBP), takenBP)
	_, criticalHook, criticalOverride := host.firstCombatHookStatusLocked(command.Source, "critical_override")
	if command.CanCritical && (source.ForceCritical || criticalOverride) {
		criticalBP := source.CriticalMultiplierBP
		if criticalBP == 0 {
			criticalBP = 15000
		}
		amount = scaleBasisPoints(amount, criticalBP)
		result.Critical = true
		if criticalOverride {
			result.CombatHooks = append(result.CombatHooks, criticalHook)
			host.consumeCombatHookStatusLocked(command.Source, criticalHook)
		}
	}
	if target.DamageCap > 0 && amount > target.DamageCap {
		amount = target.DamageCap
	}
	if target.MinimumDamage > 0 && amount > 0 && amount < target.MinimumDamage {
		amount = target.MinimumDamage
	}
	result.Absorbed = minInt64(target.Shield, amount)
	target.Shield -= result.Absorbed
	amount -= result.Absorbed
	remainingAbsorb := result.Absorbed
	statusRemove := map[int]bool{}
	for index := range host.statuses {
		instance := &host.statuses[index]
		policy, _ := host.statusPolicy(instance.status)
		if instance.target != command.Target || policy.Category != "shield" || remainingAbsorb <= 0 {
			continue
		}
		absorbed := minInt64(instance.shield, remainingAbsorb)
		instance.shield -= absorbed
		remainingAbsorb -= absorbed
		if instance.shield == 0 {
			statusRemove[index] = true
		}
	}
	if len(statusRemove) > 0 {
		host.filterStatusesLocked(statusRemove)
	}
	if amount >= target.Health && target.Health > 0 {
		if _, hook, ok := host.firstCombatHookStatusLocked(command.Target, "death_prevention", "execute_immunity"); ok {
			amount = maxInt64(0, target.Health-1)
			result.CombatHooks = append(result.CombatHooks, hook)
			context.Result = hook
			host.consumeCombatHookStatusLocked(command.Target, hook)
		}
	}
	result.HealthDamage = minInt64(target.Health, amount)
	target.Health -= result.HealthDamage
	if target.Health == 0 && result.HealthDamage > 0 {
		target.Alive = false
		result.Killed = true
		context.Result = combatResultKilled
	}
	if command.Source != 0 && source.VampBP > 0 && result.HealthDamage > 0 {
		heal := scaleBasisPoints(result.HealthDamage, source.VampBP)
		source.Health = minInt64(source.MaxHealth, saturatingInt64Add(source.Health, heal))
		host.entities[command.Source] = source
	}
	host.entities[command.Target] = target
	return host.commitDamageLocked(command, result, context), nil
}

func containsDamageTypeHandle(values []DamageTypeHandle, want DamageTypeHandle) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsElementHandle(values []ElementCatalogEntry, want ElementHandle) bool {
	for _, value := range values {
		if value.Handle == want {
			return true
		}
	}
	return false
}

func (host *MemoryHost) commitDamageLocked(command DamageCommand, result DamageResult, context EventContext) EffectResult {
	tags := append([]GameplayTagHandle(nil), command.Tags...)
	if result.Critical && host.criticalTag != 0 {
		tags = append(tags, host.criticalTag)
	}
	context = context.WithGameplayTags(tags)
	host.revision++
	host.appendContextEventLocked("damage_resolved", command.Target, 0, context)
	for _, hook := range result.CombatHooks {
		hookContext := context
		hookContext.Result = hook
		host.appendContextEventLocked("combat_hook_"+hook, command.Target, 0, hookContext)
	}
	if result.Absorbed > 0 {
		host.appendContextEventLocked("shield_absorbed", command.Target, 0, context)
		if host.entities[command.Target].Shield == 0 {
			host.appendContextEventLocked("shield_broken", command.Target, 0, context)
		}
	}
	return EffectResult{Commit: CommitReceipt{Revision: host.revision, Changed: true}, Payload: DamageEffectResult{ResultOutcome: successfulResultOutcome(), Result: result}}
}

func (host *MemoryHost) firstCombatHookStatusLocked(target EntityID, hooks ...string) (int, string, bool) {
	wanted := make(map[string]bool, len(hooks))
	for _, hook := range hooks {
		wanted[hook] = true
	}
	selectedIndex := -1
	selectedSequence := ^uint64(0)
	selectedHook := ""
	for index, instance := range host.statuses {
		if instance.target != target || instance.sequence >= selectedSequence {
			continue
		}
		policy, ok := host.statusPolicy(instance.status)
		if !ok {
			continue
		}
		for _, hook := range policy.CombatHooks {
			if wanted[hook] {
				selectedIndex, selectedSequence, selectedHook = index, instance.sequence, hook
				break
			}
		}
	}
	return selectedIndex, selectedHook, selectedIndex >= 0
}

func (host *MemoryHost) consumeCombatHookStatusLocked(target EntityID, hook string) bool {
	index, _, ok := host.firstCombatHookStatusLocked(target, hook)
	if !ok {
		return false
	}
	policy, ok := host.statusPolicy(host.statuses[index].status)
	if !ok || policy.RemovalPolicy != "consume" {
		return false
	}
	host.filterStatusesLocked(map[int]bool{index: true})
	return true
}

func (host *MemoryHost) applyHealLocked(command HealCommand) (EffectResult, error) {
	target, ok := host.entities[command.Target]
	if !ok || !target.Alive {
		return EffectResult{Commit: CommitReceipt{Revision: host.revision}, Payload: HealEffectResult{ResultOutcome: failedResultOutcome(ExpectedFailureInvalidTarget)}}, nil
	}
	attempted := maxInt64(command.Amount, 0)
	effective := minInt64(attempted, maxInt64(0, target.MaxHealth-target.Health))
	target.Health += effective
	host.entities[command.Target] = target
	context := command.Event
	context.Source, context.Target, context.Result = command.Source, command.Target, "healed"
	host.revision++
	host.appendContextEventLocked("heal_resolved", command.Target, 0, context)
	return EffectResult{Commit: CommitReceipt{Revision: host.revision, Changed: true}, Payload: HealEffectResult{ResultOutcome: successfulResultOutcome(), Result: HealResult{Attempted: attempted, Effective: effective}}}, nil
}

func (host *MemoryHost) applyShieldLocked(command ShieldCommand) (EffectResult, error) {
	target, ok := host.entities[command.Target]
	if !ok || !target.Alive {
		return EffectResult{Commit: CommitReceipt{Revision: host.revision}, Payload: ShieldEffectResult{ResultOutcome: failedResultOutcome(ExpectedFailureInvalidTarget)}}, nil
	}
	added := maxInt64(command.Amount, 0)
	target.Shield = saturatingInt64Add(target.Shield, added)
	shieldStatus := StatusHandle(0)
	for _, entry := range host.gameplay.Statuses.Entries {
		if entry.Category == "shield" {
			shieldStatus = entry.Handle
			break
		}
	}
	if shieldStatus == 0 || command.DurationTicks <= 0 {
		return EffectResult{}, ErrHostContractViolation
	}
	shieldPolicy, _ := host.statusPolicy(shieldStatus)
	duration := command.DurationTicks
	if shieldPolicy.MaximumDurationTicks > 0 && duration > shieldPolicy.MaximumDurationTicks {
		duration = shieldPolicy.MaximumDurationTicks
	}
	host.nextInstanceSequence++
	host.statuses = append(host.statuses, statusInstance{target: command.Target, status: shieldStatus, sourceOwner: command.Source, sourceSkill: command.SourceSkill, sourceCast: command.SourceCast, effect: command.Meta.EffectIndex, sequence: host.nextInstanceSequence, appliedTick: host.tick, stacks: 1, dueTick: saturatingTickAdd(host.tick, duration), shield: added})
	if target.Statuses == nil {
		target.Statuses = map[StatusHandle]bool{}
	}
	target.Statuses[shieldStatus] = true
	host.entities[command.Target] = target
	context := command.Event
	context.Source, context.Target, context.Result = command.Source, command.Target, "shielded"
	host.revision++
	host.appendContextEventLocked("shield_resolved", command.Target, 0, context)
	return EffectResult{Commit: CommitReceipt{Revision: host.revision, Changed: true}, Payload: ShieldEffectResult{ResultOutcome: successfulResultOutcome(), Result: ShieldResult{Added: added}}}, nil
}

func scaleBasisPoints(value, basisPoints int64) int64 {
	return saturatingInt64Mul(value, basisPoints) / 10000
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
