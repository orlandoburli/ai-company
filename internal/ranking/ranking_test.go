package ranking

import (
	"testing"
)

func TestLLMOutputValidation(t *testing.T) {
	items := []Candidate{{ItemID: "a"}, {ItemID: "b"}}
	valid := []LLMEntry{{ItemID: "a", Score: 900, Reason: "Urgent."}, {ItemID: "b", Score: 400, Reason: "Next."}}
	if !validOutput(items, valid) {
		t.Fatal("valid complete monotonic ranking rejected")
	}
	bad := []LLMEntry{{ItemID: "a", Score: 400, Reason: "First."}, {ItemID: "b", Score: 900, Reason: "Second."}}
	if validOutput(items, bad) {
		t.Fatal("non-monotonic score accepted")
	}
	bad = []LLMEntry{{ItemID: "a", Score: 900, Reason: "First."}, {ItemID: "a", Score: 400, Reason: "Duplicate."}}
	if validOutput(items, bad) {
		t.Fatal("duplicate/missing item accepted")
	}
}

func TestFingerprintStableAcrossInputOrderAndSensitiveToInputs(t *testing.T) {
	a := Candidate{ItemID: "a", BaseScore: 10, Input: []byte(`{"label":"bug"}`)}
	b := Candidate{ItemID: "b", BaseScore: 2, Input: []byte(`{"label":"feature"}`)}
	if fingerprint([]Candidate{a, b}) != fingerprint([]Candidate{b, a}) {
		t.Fatal("fingerprint depends on order")
	}
	changed := b
	changed.Input = []byte(`{"label":"urgent"}`)
	if fingerprint([]Candidate{a, b}) == fingerprint([]Candidate{a, changed}) {
		t.Fatal("fingerprint ignored changed input")
	}
}
