package skillcompose

import (
	"errors"
	"sort"

	"github.com/tjbdwanghaibo/cube-skill/skillv2"
)

var ErrSourceProfileRequired = errors.New("skillcompose: at least one source profile is required")

func BuildContract(profiles []SkillProfile, authority skillv2.AuthorityIdentity, policy CompositionPolicy, caller CallerPolicy) (SkillCompositionContract, error) {
	if len(profiles) == 0 {
		return SkillCompositionContract{}, ErrSourceProfileRequired
	}
	contract := SkillCompositionContract{Version: "skillcompose/v1", Authority: authority, Policy: PolicyIdentity{ID: policy.ID}, Constraints: []Constraint{{Key: "commit_order_preserved"}, {Key: "passive_dispatch_bound"}, {Key: "quantity_compatible"}, {Key: "random_site_bound"}}}
	for _, profile := range profiles {
		contract.Sources = append(contract.Sources, SourceIdentity{SkillID: profile.SkillID, GameplayDigest: profile.GameplayDigest})
		for _, feature := range profile.Features {
			contract.Grants = append(contract.Grants, SourceGrant{SourceID: profile.SkillID, Feature: feature})
		}
		contract.Budgets.Targets += profile.Metrics.Targets
		contract.Budgets.Processes += profile.Metrics.Processes
		contract.Budgets.Mutations += profile.Metrics.Mutations
		contract.Budgets.LifetimeTicks += profile.Metrics.LifetimeTicks
	}
	contract.Budgets = tightenBudgets(contract.Budgets, policy.Maximum)
	contract.Budgets = tightenBudgets(contract.Budgets, caller.Maximum)
	if policy.AllowGenericPackages && !caller.DisableGenericPackages {
		contract.Packages = []GenericPackageGrant{{Key: "generic"}}
	}
	_, digest, err := CanonicalContract(contract)
	if err != nil {
		return SkillCompositionContract{}, err
	}
	contract.Digest = digest
	return contract, nil
}
func tightenBudgets(value CompositionBudgets, limit Metrics) CompositionBudgets {
	if limit.Targets > 0 && value.Targets > limit.Targets {
		value.Targets = limit.Targets
	}
	if limit.Processes > 0 && value.Processes > limit.Processes {
		value.Processes = limit.Processes
	}
	if limit.Mutations > 0 && value.Mutations > limit.Mutations {
		value.Mutations = limit.Mutations
	}
	if limit.LifetimeTicks > 0 && value.LifetimeTicks > limit.LifetimeTicks {
		value.LifetimeTicks = limit.LifetimeTicks
	}
	return value
}
func sortedFeatures(profiles []SkillProfile) []FeatureKey {
	set := map[FeatureKey]struct{}{}
	for _, profile := range profiles {
		for _, feature := range profile.Features {
			set[feature] = struct{}{}
		}
	}
	result := make([]FeatureKey, 0, len(set))
	for feature := range set {
		result = append(result, feature)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
