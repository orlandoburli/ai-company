package failure

import (
	"testing"
	"time"
)

func TestDelayCurvesAndCap(t *testing.T) {
	exp := Params{XSeconds: 10, Y: 2, MaxBackoffSeconds: 25}
	for n, want := range map[int]time.Duration{1: 10 * time.Second, 2: 20 * time.Second, 3: 25 * time.Second} {
		if got := exp.Delay(n); got != want {
			t.Errorf("exponential delay(%d)=%s, want %s", n, got, want)
		}
	}
	linear := Params{XSeconds: 10, StepSeconds: 7, MaxBackoffSeconds: 20}
	for n, want := range map[int]time.Duration{1: 10 * time.Second, 2: 17 * time.Second, 3: 20 * time.Second} {
		if got := linear.delay(CurveLinear, n); got != want {
			t.Errorf("linear delay(%d)=%s, want %s", n, got, want)
		}
	}
}

func TestFirstFailureWaitsXAndNonpositiveFailureWaitsNothing(t *testing.T) {
	p := DefaultParams(CurveExponential)
	if got := p.Delay(1); got != time.Duration(p.XSeconds)*time.Second {
		t.Fatalf("first delay=%s", got)
	}
	if got := p.Delay(0); got != 0 {
		t.Fatalf("zero-failure delay=%s", got)
	}
}
