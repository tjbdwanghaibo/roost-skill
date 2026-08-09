package skillcompose

import "github.com/tjbdwanghaibo/cube-skill/skillv2"

type CompositionPolicy struct {
	ID                   string
	AllowGenericPackages bool
	Maximum              Metrics
}
type CallerPolicy struct {
	Maximum                Metrics
	DisableGenericPackages bool
}
type PolicyIdentity struct{ ID string }
type SourceIdentity struct{ SkillID, GameplayDigest string }
type CompositionBudgets struct {
	Targets, Processes, Mutations int
	LifetimeTicks                 skillv2.Tick
}
