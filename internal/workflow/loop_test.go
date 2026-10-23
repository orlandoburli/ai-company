package workflow

import (
	"encoding/json"
	"testing"
)

func TestEvaluateUntil(t *testing.T) {
	cases := []struct {
		name  string
		until string
		value string
		want  bool
	}{
		{"boolean true", `true`, `{}`, true},
		{"boolean false", `false`, `{}`, false},
		{"key present and true", `"passed"`, `{"passed": true}`, true},
		{"key present and false", `"passed"`, `{"passed": false}`, false},
		{"prefixed key", `"output.passed"`, `{"passed": true}`, true},
		{"key missing", `"passed"`, `{"other": 1}`, false},
		{"value is not an object", `"passed"`, `"all good"`, false},
		{"empty value", `"passed"`, ``, false},
		{"numeric truthiness", `"count"`, `{"count": 3}`, true},
		{"numeric zero", `"count"`, `{"count": 0}`, false},
		{"string truthiness", `"state"`, `{"state": "green"}`, true},
		{"string falsy", `"state"`, `{"state": ""}`, false},
		{"null value", `"state"`, `{"state": null}`, false},
		{"unparseable condition", `{"weird": true}`, `{}`, false},
		{"missing condition", ``, `{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateUntil(json.RawMessage(tc.until), json.RawMessage(tc.value))
			if got != tc.want {
				t.Fatalf("EvaluateUntil(%s, %s) = %v, want %v", tc.until, tc.value, got, tc.want)
			}
		})
	}
}

func TestLoopCycleValue(t *testing.T) {
	inputs := map[string]Receipt{
		"e1": {EdgeID: "e1", State: ReceiptDelivered, Value: json.RawMessage(`{"passed": true}`)},
		"e2": {EdgeID: "e2", State: ReceiptSkipped},
	}
	got := LoopCycleValue(inputs)
	if string(got) != `{"passed": true}` {
		t.Fatalf("cycle value = %s", got)
	}
	if EvaluateUntil(json.RawMessage(`"passed"`), got) != true {
		t.Fatal("the cycle value should satisfy the until condition")
	}
}
