package skillcompose

import "github.com/tjbdwanghaibo/cube-skill/skillv2"

type SkillCompositionContract struct {
	Version     string                    `json:"version"`
	Sources     []SourceIdentity          `json:"sources"`
	Authority   skillv2.AuthorityIdentity `json:"authority"`
	Policy      PolicyIdentity            `json:"policy"`
	Grants      []SourceGrant             `json:"grants"`
	Obligations []SourceObligation        `json:"obligations"`
	Packages    []GenericPackageGrant     `json:"packages"`
	Constraints []Constraint              `json:"constraints"`
	Budgets     CompositionBudgets        `json:"budgets"`
	Digest      string                    `json:"digest"`
}
type SourceGrant struct {
	SourceID          string          `json:"source_id"`
	Feature           FeatureKey      `json:"feature"`
	AllowedTransforms []TransformKind `json:"allowed_transforms"`
}
type SourceObligation struct{ SourceID, Key string }
type GenericPackageGrant struct{ Key string }
type Constraint struct{ Key string }
