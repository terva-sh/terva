package provider

import (
	"regexp"
	"testing"
)

func TestParseClaudeVersion(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"2.1.267 (Claude Code)", "2.1.267", true},
		{"2.1.267 (Claude Code)\n", "2.1.267", true},
		{"claude version 3.0.1", "3.0.1", true},
		{"10.20.30", "10.20.30", true},
		{"", "", false},
		{"garbage", "", false},
		{"2.1", "", false},
		{"v2", "", false},
		{"v22.1.0", "", false},                  // no word boundary after the v prefix, so no match
		{"needs 1.2.3 or 4.5.6", "1.2.3", true}, // first triple wins; pick still floors it
	}
	for _, c := range cases {
		got, ok := parseClaudeVersion(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseClaudeVersion(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestVersionTripleNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"2.1.268", "2.1.267", true},
		{"2.1.267", "2.1.267", false}, // equal is not newer
		{"2.1.266", "2.1.267", false},
		{"2.2.0", "2.1.999", true},
		{"3.0.0", "2.9.9", true},
		{"2.1.67", "2.1.267", false}, // numeric, not lexical
		{"2.10.0", "2.9.0", true},    // same trap on the middle part
	}
	for _, c := range cases {
		if got := versionTripleNewer(c.a, c.b); got != c.want {
			t.Errorf("versionTripleNewer(%q, %q) = %v; want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestPickClaudeCodeVersionFloorsAtBaseline(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"newer install wins", "99.0.0 (Claude Code)", "99.0.0"},
		{"older install floors", "0.1.0 (Claude Code)", claudeCodeVersion},
		{"equal install floors", claudeCodeVersion + " (Claude Code)", claudeCodeVersion},
		{"garbage floors", "command not found", claudeCodeVersion},
		{"empty floors", "", claudeCodeVersion},
	}
	for _, c := range cases {
		if got := pickClaudeCodeVersion(c.out); got != c.want {
			t.Errorf("%s: pickClaudeCodeVersion(%q) = %q; want %q", c.name, c.out, got, c.want)
		}
	}
}

// The effective version is what the OAuth user-agent claims, so whatever the
// probe finds on the machine running the tests, the result must be a dotted
// version triple and never older than the compiled baseline.
func TestEffectiveClaudeCodeVersionNeverBelowBaseline(t *testing.T) {
	got := effectiveClaudeCodeVersion()
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(got) {
		t.Fatalf("effectiveClaudeCodeVersion() = %q, not a dotted version triple", got)
	}
	if got != claudeCodeVersion && !versionTripleNewer(got, claudeCodeVersion) {
		t.Errorf("effectiveClaudeCodeVersion() = %q is older than the baseline %q", got, claudeCodeVersion)
	}
}

// The baseline itself must stay a parseable triple, or the floor comparison
// in pickClaudeCodeVersion silently degrades.
func TestBaselineClaudeCodeVersionIsATriple(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(claudeCodeVersion) {
		t.Fatalf("claudeCodeVersion = %q is not a dotted version triple", claudeCodeVersion)
	}
}
