package skillsync

import (
	"errors"

	"github.com/tjbdwanghaibo/cube-core/syncstream"
	"github.com/tjbdwanghaibo/cube-skill/skillv2"
)

var ErrVisibilityEvaluatorRequired = errors.New("skillsync: entity visibility evaluator is required")

type EntityVisibilityEvaluator func(syncstream.Observer, skillv2.EntityID) (bool, error)

type EntityVisibilityPolicy struct{ Visible EntityVisibilityEvaluator }

func (policy EntityVisibilityPolicy) visible(observer syncstream.Observer, entity skillv2.EntityID) (bool, error) {
	if entity == 0 {
		return true, nil
	}
	if policy.Visible == nil {
		return false, ErrVisibilityEvaluatorRequired
	}
	return policy.Visible(observer, entity)
}

func (policy EntityVisibilityPolicy) FilterStateSnapshot(observer syncstream.Observer, snapshot skillv2.RuntimeStateSnapshot) (skillv2.RuntimeStateSnapshot, error) {
	result := snapshot
	result.Casts, result.Cooldowns, result.SkillResources, result.Abilities, result.Processes, result.ActivePolicies, result.PersistentStates = nil, nil, nil, nil, nil, nil, nil
	for _, value := range snapshot.Casts {
		allowed, err := policy.visible(observer, value.Caster)
		if err != nil {
			return result, err
		}
		if allowed {
			target, err := policy.visible(observer, value.PrimaryTarget)
			if err != nil {
				return result, err
			}
			if !target {
				value.PrimaryTarget = 0
			}
			result.Casts = append(result.Casts, value)
		}
	}
	for _, value := range snapshot.Cooldowns {
		allowed, err := policy.visible(observer, value.Caster)
		if err != nil {
			return result, err
		}
		if allowed {
			result.Cooldowns = append(result.Cooldowns, value)
		}
	}
	for _, value := range snapshot.SkillResources {
		allowed, err := policy.visible(observer, value.Caster)
		if err != nil {
			return result, err
		}
		if allowed {
			result.SkillResources = append(result.SkillResources, value)
		}
	}
	for _, value := range snapshot.Abilities {
		allowed, err := policy.visible(observer, value.Owner)
		if err != nil {
			return result, err
		}
		if allowed {
			result.Abilities = append(result.Abilities, value)
		}
	}
	for _, value := range snapshot.Processes {
		allowed, err := policy.visible(observer, value.Owner)
		if err != nil {
			return result, err
		}
		if allowed {
			target, err := policy.visible(observer, value.LifecycleEntity)
			if err != nil {
				return result, err
			}
			if !target {
				value.LifecycleEntity = 0
			}
			result.Processes = append(result.Processes, value)
		}
	}
	for _, value := range snapshot.ActivePolicies {
		allowed, err := policy.visible(observer, value.Caster)
		if err != nil {
			return result, err
		}
		if allowed {
			result.ActivePolicies = append(result.ActivePolicies, value)
		}
	}
	for _, value := range snapshot.PersistentStates {
		owner, err := policy.visible(observer, value.Binding.Owner)
		if err != nil {
			return result, err
		}
		subject, err := policy.visible(observer, value.Binding.Subject)
		if err != nil {
			return result, err
		}
		if owner && subject {
			result.PersistentStates = append(result.PersistentStates, value)
		}
	}
	return result, nil
}

func (policy EntityVisibilityPolicy) FilterStateMutation(observer syncstream.Observer, mutation skillv2.StateMutation) (skillv2.StateMutation, bool, error) {
	entity := mutation.Caster
	if entity == 0 {
		entity = mutation.Owner
	}
	if entity == 0 && mutation.Cast != nil {
		entity = mutation.Cast.Caster
	}
	if entity == 0 && mutation.Process != nil {
		entity = mutation.Process.Owner
	}
	if entity == 0 && mutation.Persistent != nil {
		entity = mutation.Persistent.Binding.Owner
	}
	allowed, err := policy.visible(observer, entity)
	if err != nil || !allowed {
		return mutation, false, err
	}
	if mutation.Cast != nil {
		target, err := policy.visible(observer, mutation.Cast.PrimaryTarget)
		if err != nil {
			return mutation, false, err
		}
		if !target {
			copyValue := *mutation.Cast
			copyValue.PrimaryTarget = 0
			mutation.Cast = &copyValue
		}
	}
	if mutation.Process != nil {
		target, err := policy.visible(observer, mutation.Process.LifecycleEntity)
		if err != nil {
			return mutation, false, err
		}
		if !target {
			copyValue := *mutation.Process
			copyValue.LifecycleEntity = 0
			mutation.Process = &copyValue
		}
	}
	return mutation, true, nil
}

func (policy EntityVisibilityPolicy) FilterPresentation(observer syncstream.Observer, event skillv2.PresentationEvent) (skillv2.PresentationEvent, bool, error) {
	allowed, err := policy.visible(observer, event.Anchor.Source)
	if err != nil || !allowed {
		return event, false, err
	}
	target, err := policy.visible(observer, event.Anchor.Target)
	if err != nil {
		return event, false, err
	}
	if !target {
		event.Anchor.Target, event.PrimaryTarget = 0, 0
	}
	return event, true, nil
}
