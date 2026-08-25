package skillcompose

import "github.com/tjbdwanghaibo/cube-skill/v2/skillv2"

type CompositionPolicy struct {
	ID                   string
	AllowGenericPackages bool
	Maximum              Metrics
}
type CallerPolicy struct {
	Maximum                Metrics
	DisableGenericPackages bool
}
type PolicyIdentity struct {
	ID string `json:"id"`
}
type SourceIdentity struct {
	SkillID        string `json:"skill_id"`
	GameplayDigest string `json:"gameplay_digest"`
}
type CompositionBudgets struct {
	Targets       int          `json:"targets"`
	Processes     int          `json:"processes"`
	Mutations     int          `json:"mutations"`
	EventsPerRoot int          `json:"events_per_root"`
	RandomSites   int          `json:"random_sites"`
	LifetimeTicks skillv2.Tick `json:"lifetime_ticks"`
}
