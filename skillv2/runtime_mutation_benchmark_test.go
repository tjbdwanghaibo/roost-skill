package skillv2

import "testing"

func BenchmarkStateMutationCommit1000Cooldowns(b *testing.B) {
	host := NewMemoryHost(AuthorityIdentity{})
	runtime := NewRuntime(host, RuntimeOptions{})
	for index := 1; index <= 1000; index++ {
		runtime.cooldowns[cooldownKey{Caster: EntityID(index), Skill: "skill"}] = 10
	}
	runtime.beginStateMutationLocked()
	runtime.commitStateMutationsLocked()
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		runtime.beginStateMutationLocked()
		runtime.commitStateMutationsLocked()
	}
}
