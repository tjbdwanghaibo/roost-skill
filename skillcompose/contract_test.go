package skillcompose

import (
	"github.com/tjbdwanghaibo/cube-skill/skillv2"
	"strings"
	"testing"
)

func TestContractCallerPolicyOnlyTightens(t *testing.T) {
	profiles := []SkillProfile{{SkillID: "b", GameplayDigest: "b", Features: []FeatureKey{"effect.damage"}, Metrics: Metrics{Targets: 4, Processes: 2, Mutations: 9, LifetimeTicks: 8}}, {SkillID: "a", GameplayDigest: "a", Features: []FeatureKey{"select.chain"}, Metrics: Metrics{Targets: 3, Processes: 1, Mutations: 2, LifetimeTicks: 4}}}
	contract, err := BuildContract(profiles, skillv2.AuthorityIdentity{Revision: "r", Digest: "d"}, CompositionPolicy{ID: "server", AllowGenericPackages: true, Maximum: Metrics{Targets: 6}}, CallerPolicy{Maximum: Metrics{Targets: 5}, DisableGenericPackages: true})
	if err != nil {
		t.Fatal(err)
	}
	if contract.Budgets.Targets != 5 || len(contract.Packages) != 0 {
		t.Fatalf("contract=%#v", contract)
	}
	if len(DerivePromptView(profiles).AllowedFeatures) != 2 {
		t.Fatal("prompt view missed grants")
	}
}
func TestContractCanonicalOmitsDerivedPromptFields(t *testing.T) {
	contract, err := BuildContract([]SkillProfile{{SkillID: "a", GameplayDigest: "d"}}, skillv2.AuthorityIdentity{}, CompositionPolicy{ID: "p"}, CallerPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	data, digest, err := CanonicalContract(contract)
	if err != nil || digest != contract.Digest {
		t.Fatalf("canonical=%s digest=%s err=%v", data, digest, err)
	}
	for _, forbidden := range []string{"allowed_feature_union", "prompt_projection", "repair_hints"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatal(forbidden)
		}
	}
}
