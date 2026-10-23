package workflow

import (
	"encoding/json"
	"strings"
)

// EvaluateUntil evaluates a loop's `until` condition against the value the
// loop received on its cycle-return edge.
//
// The condition is one of:
//
//   - a boolean — always true or always false;
//   - a string — the name of an output key ("passed" or "output.passed"),
//     looked up in the value. A JSON object with that key uses its
//     truthiness.
//
// A condition that cannot be evaluated is FALSE. The loop keeps iterating
// until its iteration budget runs out, and the node then fails with
// `cycle_budget_exhausted`. That is deliberate: a timeout would be the wrong
// backstop for a body that can wait behind a human checkpoint for days, and a
// condition nobody can evaluate must not be able to end the loop by accident.
func EvaluateUntil(until json.RawMessage, value json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(until))
	if trimmed == "" || trimmed == "null" {
		return false
	}
	var flag bool
	if json.Unmarshal(until, &flag) == nil {
		return flag
	}
	var key string
	if json.Unmarshal(until, &key) != nil {
		return false
	}
	key = strings.TrimSpace(key)
	key = strings.TrimPrefix(key, "output.")
	if key == "" {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(value, &object) != nil {
		return false
	}
	raw, ok := object[key]
	if !ok {
		return false
	}
	return truthy(raw)
}

// truthy is the JSON truthiness used for the values of a condition: false,
// zero, the empty string and null are false, everything else is true.
func truthy(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return false
	}
	var flag bool
	if json.Unmarshal(raw, &flag) == nil {
		return flag
	}
	var number float64
	if json.Unmarshal(raw, &number) == nil {
		return number != 0
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text != "" && !strings.EqualFold(text, "false")
	}
	return true
}

// LoopCycleValue returns the value the loop received on its cycle-return
// edge: the receipt from the node inside the body, not from the loop's own
// entry. It is what the `until` condition is evaluated against.
func LoopCycleValue(inputs map[string]Receipt) json.RawMessage {
	best := json.RawMessage(nil)
	for _, receipt := range inputs {
		if receipt.State != ReceiptDelivered {
			continue
		}
		if best == nil || receipt.EdgeID > string(best) {
			best = receipt.Value
		}
	}
	return best
}
