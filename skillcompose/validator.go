package skillcompose

type ValidationReport struct {
	Valid       bool
	Diagnostics []Diagnostic
	Matches     []Match
}

func ValidateCandidate(contract SkillCompositionContract, candidate SkillProfile) ValidationReport {
	report := ValidationReport{Valid: true, Matches: MatchFeatures(contract, candidate)}
	allowed := map[FeatureKey]bool{}
	for _, grant := range contract.Grants {
		allowed[grant.Feature] = true
	}
	for _, feature := range candidate.Features {
		if !allowed[feature] {
			report.Valid = false
			report.Diagnostics = append(report.Diagnostics, Diagnostic{Code: "FEATURE_NOT_GRANTED", Message: string(feature)})
		}
	}
	if candidate.Metrics.Targets > contract.Budgets.Targets || candidate.Metrics.Processes > contract.Budgets.Processes || candidate.Metrics.Mutations > contract.Budgets.Mutations || candidate.Metrics.LifetimeTicks > contract.Budgets.LifetimeTicks {
		report.Valid = false
		report.Diagnostics = append(report.Diagnostics, Diagnostic{Code: "BUDGET_EXCEEDED", Message: "candidate exceeds contract budget"})
	}
	if !GraphFromProfile(candidate).ReachesSink() {
		report.Valid = false
		report.Diagnostics = append(report.Diagnostics, Diagnostic{Code: "CAUSAL_DISCONNECTED", Message: "activation does not reach gameplay sink"})
	}
	return report
}
