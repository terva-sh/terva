package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func commandArgs(cmd string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return b
}

// The preamble elision, and the reason it exists: a `cd` into the directory the
// command already runs in identifies nothing, and it lands in front of the
// ledger's clip.
//
// Measured on a dogfooded session — 1,090 of 1,112 `cd`s pointed at the agent's
// own cwd, eating 92 of the 160 characters the ledger allots to naming a call.
// The assertion is the one that matters: after eliding, the first 160
// characters reach the command. Before it, the entry named no command at all.
func TestLedgerElidesTheCdThatGoesNowhere(t *testing.T) {
	const cwd = "/Users/dev/Workspace/git.example.com/someone/a-project"
	cmd := "cd " + cwd + "\nset +e\ngo test ./internal/world/spec -run TestContestResolution"
	got := (&BashTool{CWD: cwd}).LedgerArgs(commandArgs(cmd))

	if strings.Contains(got, cwd) {
		t.Errorf("the no-op cd survived into the entry:\n%s", got)
	}
	if strings.Contains(got, "set +e") {
		t.Errorf("the no-op `set +e` survived into the entry:\n%s", got)
	}
	// The point of the whole exercise.
	if i := strings.Index(got, "TestContestResolution"); i < 0 || i > 160 {
		t.Errorf("the clip still falls inside the preamble; the entry names no command:\n%s", got)
	}
}

// The other direction, and the one that must never break: a `cd` somewhere else
// changed WHERE the work happened. Eliding it would misreport the action — a
// `make` in /tmp/build is not the `make` this ledger would then claim ran.
func TestLedgerKeepsACdThatMoved(t *testing.T) {
	const cwd = "/srv/app"
	for _, tc := range []struct{ name, command, keep string }{
		{"elsewhere", "cd /tmp/build && make install", "/tmp/build"},
		// Shares a prefix with cwd. Requiring a separator after the match is what
		// stops HasPrefix from rewriting this into a command that never ran.
		{"prefix-similar", "cd /srv/app2 && make install", "/srv/app2"},
		// Same, one character the other way.
		{"trailing-slash", "cd /srv/app/ && make install", "/srv/app/"},
		// Not a statement: `set +export` merely starts like `set +e`.
		{"set-lookalike", "set +export FOO=1; make", "+export"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := (&BashTool{CWD: cwd}).LedgerArgs(commandArgs(tc.command)); !strings.Contains(got, tc.keep) {
				t.Errorf("elided %q, which changes what the entry claims ran:\n%s", tc.keep, got)
			}
		})
	}
}

// Without a directory to compare against, no `cd` can be verified as a no-op,
// and anything that is not a command passes through byte-identical.
func TestLedgerElidesOnlyAgainstAKnownDirectory(t *testing.T) {
	if got := bashLedgerArgs(commandArgs("cd /srv/app && make"), ""); !strings.Contains(got, "cd /srv/app") {
		t.Errorf("an unknown directory elided anyway, which cannot be verified as a no-op:\n%s", got)
	}
	for _, raw := range []string{`{"path":"/srv/app"}`, `{"command":7}`, `not json`} {
		if got := bashLedgerArgs(json.RawMessage(raw), "/srv/app"); got != raw {
			t.Errorf("%s was rewritten to %s", raw, got)
		}
	}
}

// A command with no preamble keeps the bytes the model sent, JSON escaping
// included: the rendering is only re-encoded when something was elided.
func TestLedgerKeepsAnUntouchedCommandAsSent(t *testing.T) {
	raw := `{"command":"cd /elsewhere && make"}`
	if got := (&BashTool{CWD: "/srv/app"}).LedgerArgs(json.RawMessage(raw)); got != raw {
		t.Errorf("got %s, want the arguments as sent", got)
	}
}
