package sdk

import "testing"

// TestEffectiveMaxSteps pins the SDK's deliberate divergence from core:
// 0 maps to the finite DefaultMaxSteps (an embedded session has no
// interactive interrupt), while explicit values — including a very
// large "effectively unbounded" one — pass through untouched.
func TestEffectiveMaxSteps(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, DefaultMaxSteps},
		{1, 1},
		{50, 50},
		{1_000_000, 1_000_000},
	}
	for _, c := range cases {
		if got := effectiveMaxSteps(c.in); got != c.want {
			t.Errorf("effectiveMaxSteps(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
