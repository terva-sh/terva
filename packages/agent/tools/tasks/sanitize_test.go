package tasks

import (
	"strings"
	"testing"
)

func TestCleanOneLine(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"a\nb", 0, "a b"},
		{"a\tb\rc", 0, "a b c"},
		{"x\x1b[31my", 0, "x[31my"}, // ESC dropped, no color control survives
		{"  lots   of    space ", 0, "lots of space"},
		{"line\x00null", 0, "linenull"}, // NUL dropped
		{"abcdef", 3, "abc…"},           // truncation + ellipsis
		{"abc", 3, "abc"},
		{"plain", 0, "plain"},
	}
	for _, c := range cases {
		if got := CleanOneLine(c.in, c.max); got != c.want {
			t.Errorf("CleanOneLine(%q,%d)=%q want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestSafeSessionID(t *testing.T) {
	for _, ok := range []string{"d00754e5-75b1-4a66-9213-2a47f4b3e977", "abc_123.def", "X"} {
		if !safeSessionID(ok) {
			t.Errorf("%q should be safe", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "a..b", "x/../../y", "with space", "esc\x1b"} {
		if safeSessionID(bad) {
			t.Errorf("%q should be unsafe", bad)
		}
	}
}

func TestSessionFileName(t *testing.T) {
	uuid := "d00754e5-75b1-4a66-9213-2a47f4b3e977"
	if got := sessionFileName(uuid); got != "tasks-"+uuid+".json" {
		t.Errorf("uuid should map to a readable name, got %q", got)
	}
	bad := sessionFileName("sess/../../escape")
	if strings.ContainsAny(bad, `/\`) || strings.Contains(bad, "..") {
		t.Errorf("hostile id produced an unsafe file name: %q", bad)
	}
	// Deterministic and collision-distinct for different ids.
	if sessionFileName("a/b") == sessionFileName("c/d") {
		t.Errorf("distinct hostile ids should hash to distinct names")
	}
}
