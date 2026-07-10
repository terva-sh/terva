package modes

import "testing"

// Tests for the helpers in interactive_context.go.

func TestHumanBytes(t *testing.T) {
	cases := map[int]string{
		500:           "500 B",
		2048:          "2.0 KB",
		3 * (1 << 20): "3.0 MB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q; want %q", in, got, want)
		}
	}
}
