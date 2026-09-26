package cliversion

import "testing"

func TestParseCodexVersion(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"codex-cli 0.153.4", "0.153.4", true},
		{"codex-cli 0.153.4\n", "0.153.4", true},
		{"0.153.4", "0.153.4", true},
		{"codex-cli 0.153.4 (rust)", "0.153.4", true},
		// Refused rather than guessed at: a two-part version is not a triple,
		// and the floor comparison needs three parts to be meaningful.
		{"codex-cli 0.153", "", false},
		{"", "", false},
		{"command not found", "", false},
		{"codex-cli unknown", "", false},
	}
	for _, c := range cases {
		got, ok := parseCodexVersion(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseCodexVersion(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

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
		{"needs 1.2.3 or 4.5.6", "1.2.3", true}, // first triple wins; the wire still floors it
	}
	for _, c := range cases {
		got, ok := parseClaudeVersion(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseClaudeVersion(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
