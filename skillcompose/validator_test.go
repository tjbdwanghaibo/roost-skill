package skillcompose

import "testing"

func TestValidateCandidateRejectsUngrantableGrowthAndDisconnectedFlow(t *testing.T) {
	contract := SkillCompositionContract{Grants: []SourceGrant{{SourceID: "a", Feature: "effect.damage"}}, Budgets: CompositionBudgets{Targets: 1, Processes: 1, Mutations: 1, LifetimeTicks: 1}}
	valid := SkillProfile{Features: []FeatureKey{"effect.damage"}, Operations: []string{"damage"}, Metrics: Metrics{Targets: 1, Processes: 1, Mutations: 1, LifetimeTicks: 1}}
	if !ValidateCandidate(contract, valid).Valid {
		t.Fatal("valid candidate rejected")
	}
	invalid := valid
	invalid.Features = []FeatureKey{"effect.heal"}
	invalid.Operations = []string{"finish"}
	invalid.Metrics.Targets = 2
	if ValidateCandidate(contract, invalid).Valid {
		t.Fatal("invalid candidate accepted")
	}
}
