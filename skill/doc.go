// Package skill is roost-skill's stable public compiler and deterministic
// runtime API.
//
// Skill definitions use a separately versioned wire contract. The current
// JSON schema remains cube.skill/v2 and the compiler semantics revision remains
// skillv2-compiler-2; neither value is the Go package version. Applications
// should import github.com/tjbdwanghaibo/roost-skill/skill and persist the wire
// and compiler identities emitted by the package instead of deriving them from
// its import path.
//
// The supported lifecycle is Parse, Compile, inspect the immutable Program,
// and execute it through Runtime against a Host implementation. Production
// hosts must honor the serialization, non-reentrancy, bounded-work, authority,
// and determinism contracts documented on Host and Runtime.
package skill
