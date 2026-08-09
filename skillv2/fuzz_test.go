package skillv2

import "testing"

func FuzzParseGeneratedNeverPanics(f *testing.F) {
	f.Add([]byte(minimalSkillJSON))
	f.Add([]byte(`{"schema":"cube.skill/v2","error":{"code":"UNSUPPORTED_CAPABILITY","message":"x","unsupported":[]}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParseGenerated(data)
	})
}
