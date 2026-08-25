package skillv2

import (
	"fmt"
	"testing"
)

func castWindowExpressionJSON(id, windowJSON string) string {
	return fmt.Sprintf(`{"schema":"cube.skill/v2","id":%q,"name":"Dynamic","description":"Dynamic window.",`+
		`"activation":{"type":"active","policy":{"mode":"tap"},"cast_window":%s},`+
		`"input_schema":{"type":"entity"},"cooldown_ticks":0,"costs":[],"memory":{},"initial_phase":"cast",`+
		`"phases":[{"id":"cast","timeout_ticks":0,"on":{"enter":{"flow":"sequence","steps":[{"flow":"effect","effect":{"type":"damage","target":"$input.target","amount":10,"damage_type":"physical"}},{"flow":"finish"}]}}}]}`,
		id, windowJSON)
}

func TestCastWindowExpressionsAreClampedToDeclaredBounds(t *testing.T) {
	// The windup expression yields 9, far above its [2,5] bound; the recovery
	// expression yields 1, below its [3,6] bound. The declared bounds must
	// win in both directions, keeping the compile-time worst case authoritative.
	window := `{"windup_ticks_expression":{"op":"add","args":[4,5]},"windup_ticks_min":2,"windup_ticks_max":5,"commit_tick":2,` +
		`"recovery_ticks_expression":1,"recovery_ticks_min":3,"recovery_ticks_max":6}`
	program, environment := compileRuntimeJSON(t, castWindowExpressionJSON("skill.test.dynamic-window", window))
	host := runtimeTestHost(environment)
	runtime := NewRuntime(host, RuntimeOptions{})
	castID, err := runtime.Activate(program, CastInput{Caster: 1, Target: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Advance(4); err != nil || host.HealthForTest(2) != 100 {
		t.Fatalf("windup clamped wrong: executed before tick 5 (health=%d err=%v)", host.HealthForTest(2), err)
	}
	if err := runtime.Advance(5); err != nil {
		t.Fatal(err)
	}
	if host.HealthForTest(2) != 90 {
		t.Fatal("windup expression did not execute at the clamped maximum")
	}
	assertCastWindowState(t, runtime, castID, CastSuspended, CastWindowRecovering)
	if err := runtime.Advance(7); err != nil {
		t.Fatal(err)
	}
	assertCastWindowState(t, runtime, castID, CastSuspended, CastWindowRecovering)
	if err := runtime.Advance(8); err != nil {
		t.Fatal(err)
	}
	// Recovery ran for the clamped minimum of 3 ticks (5 -> 8).
	assertCastWindowState(t, runtime, castID, CastFinished, CastWindowComplete)
}

func TestCastWindowExpressionShapeRules(t *testing.T) {
	cases := map[string]string{
		"bounds without expression":  `{"windup_ticks":3,"windup_ticks_min":1,"windup_ticks_max":4,"commit_tick":0,"recovery_ticks":0}`,
		"commit beyond windup min":   `{"windup_ticks_expression":5,"windup_ticks_min":2,"windup_ticks_max":8,"commit_tick":3,"recovery_ticks":0}`,
		"literal with expression":    `{"windup_ticks":3,"windup_ticks_expression":5,"windup_ticks_min":2,"windup_ticks_max":8,"commit_tick":0,"recovery_ticks":0}`,
		"inverted recovery bounds":   `{"windup_ticks":0,"commit_tick":0,"recovery_ticks_expression":5,"recovery_ticks_min":6,"recovery_ticks_max":2}`,
		"recovery bounds without":    `{"windup_ticks":0,"commit_tick":0,"recovery_ticks":2,"recovery_ticks_min":1,"recovery_ticks_max":4}`,
		"literal recovery with expr": `{"windup_ticks":0,"commit_tick":0,"recovery_ticks":2,"recovery_ticks_expression":5,"recovery_ticks_min":1,"recovery_ticks_max":6}`,
	}
	for name, window := range cases {
		t.Run(name, func(t *testing.T) {
			definition := mustParseJSON(t, castWindowExpressionJSON("skill.test.dynamic-window-invalid", window))
			_, diagnostics := Compile(definition, DefaultCompileEnvironment())
			requireDiagnostic(t, diagnostics, DiagnosticShapeInvalid)
		})
	}
}
