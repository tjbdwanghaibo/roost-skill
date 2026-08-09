package skillcompose

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

func CanonicalContract(contract SkillCompositionContract) ([]byte, string, error) {
	contract.Digest = ""
	sort.Slice(contract.Sources, func(i, j int) bool { return contract.Sources[i].SkillID < contract.Sources[j].SkillID })
	sort.Slice(contract.Grants, func(i, j int) bool {
		if contract.Grants[i].SourceID != contract.Grants[j].SourceID {
			return contract.Grants[i].SourceID < contract.Grants[j].SourceID
		}
		return contract.Grants[i].Feature < contract.Grants[j].Feature
	})
	for index := range contract.Grants {
		sort.Slice(contract.Grants[index].AllowedTransforms, func(i, j int) bool {
			return contract.Grants[index].AllowedTransforms[i] < contract.Grants[index].AllowedTransforms[j]
		})
	}
	sort.Slice(contract.Constraints, func(i, j int) bool { return contract.Constraints[i].Key < contract.Constraints[j].Key })
	data, err := json.Marshal(contract)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(data)
	return data, hex.EncodeToString(digest[:]), nil
}
