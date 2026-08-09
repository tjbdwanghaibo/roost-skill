package skillcompose

type PromptContractView struct {
	AllowedFeatures     []FeatureKey
	PresentationDigests []string
}

func DerivePromptView(profiles []SkillProfile) PromptContractView {
	result := PromptContractView{AllowedFeatures: sortedFeatures(profiles)}
	for _, profile := range profiles {
		result.PresentationDigests = append(result.PresentationDigests, profile.PresentationDigest)
	}
	return result
}
