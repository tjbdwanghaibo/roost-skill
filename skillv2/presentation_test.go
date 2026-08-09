package skillv2

import (
	"strings"
	"testing"
)

func TestPresentationPlanExposesCastAndEffectMounts(t *testing.T) {
	input := visualDamageJSON(t, `{"category":"impact","theme":"default","elements":["default"]}`)
	input = strings.Replace(input, `"activation": {`, `"presentation":{"cast":{"category":"cast","theme":"default","elements":["default"]}},"activation": {`, 1)
	program := mustCompileProgram(t, mustParseJSON(t, input))

	plan := InspectPresentationPlan(program)
	if plan.Cast == nil || len(plan.Effects) != 1 || !plan.Effects[0].HasEffect {
		t.Fatalf("presentation plan = %#v", plan)
	}
	if plan.Identity.PresentationDigest == "" || plan.Manifest.Digest == "" {
		t.Fatal("presentation plan identity is incomplete")
	}
	plan.Manifest.Entries[0].Elements[0] = "mutated"
	if InspectPresentationPlan(program).Manifest.Entries[0].Elements[0] == "mutated" {
		t.Fatal("presentation plan aliases immutable program storage")
	}
}

func TestRuntimePresentationEventsAreCommittedOrderedAndPollable(t *testing.T) {
	input := visualDamageJSON(t, `{"category":"impact","theme":"default","elements":["default"]}`)
	input = strings.Replace(input, `"activation": {`, `"presentation":{"cast":{"category":"cast","theme":"default","elements":["default"]}},"activation": {`, 1)
	program := mustCompileProgram(t, mustParseJSON(t, input))
	environment := DefaultCompileEnvironment()
	host := runtimeTestHost(environment)
	runtime := NewRuntime(host, RuntimeOptions{PresentationLimit: 8})

	castID, err := runtime.Activate(program, CastInput{Caster: 1, Target: 2})
	if err != nil {
		t.Fatal(err)
	}
	events := runtime.PresentationEvents(0)
	if len(events) != 2 || events[0].Kind != PresentationCast || events[1].Kind != PresentationEffect {
		t.Fatalf("presentation events = %#v", events)
	}
	if events[0].Sequence != 1 || events[1].Sequence != 2 || events[1].WorldRevision == 0 {
		t.Fatalf("presentation ordering/revision = %#v", events)
	}
	if events[1].CastID != castID || events[1].Source != 1 || events[1].PrimaryTarget != 2 || !events[1].HasEffect {
		t.Fatalf("presentation routing = %#v", events[1])
	}
	if tail := runtime.PresentationEvents(1); len(tail) != 1 || tail[0].Sequence != 2 {
		t.Fatalf("presentation tail = %#v", tail)
	}
}

func TestRuntimeDoesNotEmitEffectPresentationForExpectedFailure(t *testing.T) {
	program := mustCompileProgram(t, mustParseJSON(t, visualDamageJSON(t, `{"category":"impact","theme":"default","elements":["default"]}`)))
	host := runtimeTestHost(DefaultCompileEnvironment())
	runtime := NewRuntime(host, RuntimeOptions{})
	if _, err := runtime.Activate(program, CastInput{Caster: 1, Target: 999}); err != nil {
		t.Fatal(err)
	}
	if events := runtime.PresentationEvents(0); len(events) != 0 {
		t.Fatalf("failed effect emitted presentation: %#v", events)
	}
}
