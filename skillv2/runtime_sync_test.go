package skillv2

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRuntimeValueJSONRoundTrip(t *testing.T) {
	values := []RuntimeValue{
		{present: true, typ: valueType{Base: valueKindInt, Quantity: quantityCombatAmount}, integer: 42},
		{present: true, typ: valueType{Base: valueKindBool}, boolean: true},
		{present: true, typ: valueType{Base: valueKindString}, text: "ready"},
		{present: true, typ: valueType{Base: valueKindEntity}, entity: 7},
		{present: true, typ: valueType{Base: valueKindPosition}, position: Position{X: 1, Y: 2}},
		{present: true, typ: valueType{Base: valueKindPath}, path: []Position{{X: 1}, {X: 2}}},
		{present: true, typ: valueType{Base: valueKindEntityList}, entities: []EntityID{2, 3}},
		{present: true, typ: valueType{Base: valueKindStringList}, strings: []string{"a", "b"}},
		{present: true, typ: valueType{Base: valueKindProcess}, process: 9},
		{present: false, typ: valueType{Base: valueKindEntity, Optional: true}},
	}
	for _, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded RuntimeValue
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("decode %s: %v", data, err)
		}
		if !reflect.DeepEqual(decoded, value) {
			t.Fatalf("round trip = %#v, want %#v", decoded, value)
		}
	}
}

func TestRuntimeStateSnapshotAndEventCursorAreComplete(t *testing.T) {
	program, environment := compileRuntimeFixture(t, "simple_damage.json")
	runtime := NewRuntime(runtimeTestHost(environment), RuntimeOptions{StateEventLimit: 1})
	castID, err := runtime.Activate(program, CastInput{Caster: 1, Target: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Activate(program, CastInput{Caster: 1, Target: 2}); err != nil {
		t.Fatal(err)
	}
	snapshot := runtime.StateSnapshot()
	if snapshot.WorldRevision == 0 || snapshot.LatestStateEventSequence < 2 || len(snapshot.Casts) != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Casts[0].ID != castID || snapshot.Casts[0].ProgramID == "" || snapshot.Casts[0].GameplayDigest == "" {
		t.Fatalf("cast snapshot = %#v", snapshot.Casts[0])
	}
	batch := runtime.StateEvents(0, 1)
	if !batch.CursorExpired || batch.Dropped == 0 || batch.LatestSequence != snapshot.LatestStateEventSequence {
		t.Fatalf("state event batch = %#v", batch)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RuntimeStateSnapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("snapshot JSON decode failed: %v", err)
	}
	decodedData, err := json.Marshal(decoded)
	if err != nil || string(decodedData) != string(data) {
		t.Fatalf("snapshot JSON round trip failed: %v", err)
	}
}
