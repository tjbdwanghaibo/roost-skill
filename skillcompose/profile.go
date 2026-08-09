package skillcompose

import "github.com/tjbdwanghaibo/cube-skill/skillv2"

type SkillProfile struct {
	SkillID, GameplayDigest   string
	ActivationMode, InputKind string
	Features                  []FeatureKey
	Operations                []string
	Selections                []SelectionFact
	Metrics                   Metrics
	PresentationDigest        string
}

type SelectionFact struct {
	ElementKind, Shape, ConsumerMode string
	Limit                            int
	HasEmpty                         bool
}
type Metrics struct {
	Targets, Processes, Mutations, EventsPerRoot, RandomSites int
	LifetimeTicks                                             skillv2.Tick
}
