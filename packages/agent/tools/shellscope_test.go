package tools

import (
	"slices"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestDecomposeBashCommand(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string // nil means "treat as one unit"
	}{
		{"simple", "ls -la", nil},
		{"empty", "   ", nil},
		{"assignment only", "FOO=bar", nil},
		{"and", "git diff && rm -rf /", []string{"git diff", "rm -rf /"}},
		{"semicolon", "make; ./run", []string{"make", "./run"}},
		{"or", "test -f x || touch x", []string{"test -f x", "touch x"}},
		{"pipeline", "echo hi | grep h", []string{"echo hi", "grep h"}},
		{"unparsable falls back to one unit", "if [", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecomposeBashCommand(tc.in)
			if !slices.Equal(got, tc.want) {
				t.Errorf("DecomposeBashCommand(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestDecomposeBashCommandCapturesNested(t *testing.T) {
	// A command hidden in a substitution still executes, so it must be
	// in the list — deny-first scoping depends on completeness.
	got := DecomposeBashCommand("echo $(rm -rf /tmp/x)")
	if !slices.Contains(got, "rm -rf /tmp/x") {
		t.Errorf("nested command not captured: %v", got)
	}
}

func TestCheckCommandCompoundScopes(t *testing.T) {
	sb := NewSandbox(testsupport.TempDir(t))
	sb.Lock()

	// A banned or escaping command must be caught even when it is not
	// first on the line — the old single-segment heuristic missed these.
	for _, bad := range []string{
		"ls && cd /etc",
		"echo ok && rm -rf /",
		"true; cd ~",
	} {
		if err := sb.CheckCommand(bad); err == nil {
			t.Errorf("CheckCommand(%q) should be rejected when jailed", bad)
		}
	}

	// A line where every command is harmless must pass.
	for _, ok := range []string{
		"ls && pwd",
		"echo hi | grep h",
	} {
		if err := sb.CheckCommand(ok); err != nil {
			t.Errorf("CheckCommand(%q) should pass: %v", ok, err)
		}
	}
}
