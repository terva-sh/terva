package build

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

func unjailScratch(t *testing.T) string {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	return testsupport.TempDir(t)
}

// A saved rule takes the jail down in a mode that would otherwise be jailed.
// That is the whole point of the feature.
func TestPersistedUnjailLowersTheJail(t *testing.T) {
	dir := unjailScratch(t)
	args := Args{Mode: ModeInteractive, CWD: dir}

	if !resolveJail(args) {
		t.Fatal("precondition: interactive should be jailed by default")
	}
	if err := config.UnjailPath(dir, false); err != nil {
		t.Fatal(err)
	}
	if resolveJail(args) {
		t.Error("a saved unjail rule did not lower the jail")
	}
}

// Flags settle it. A stored rule must never override what the user just typed —
// in either direction.
func TestFlagsBeatTheStore(t *testing.T) {
	dir := unjailScratch(t)
	if err := config.UnjailPath(dir, false); err != nil {
		t.Fatal(err)
	}

	if !resolveJail(Args{Mode: ModeInteractive, CWD: dir, Jail: true}) {
		t.Error("--jail did not override a saved unjail rule")
	}
	// And the long-standing flag precedence is untouched.
	if resolveJail(Args{Mode: ModeInteractive, CWD: dir, Jail: true, NoJail: true}) {
		t.Error("--no-jail no longer beats --jail")
	}
}

// A directory with no rule stays exactly as it was.
func TestUnrelatedDirectoryIsUnaffected(t *testing.T) {
	dir := unjailScratch(t)
	other := testsupport.TempDir(t)
	if err := config.UnjailPath(other, false); err != nil {
		t.Fatal(err)
	}
	if !resolveJail(Args{Mode: ModeInteractive, CWD: dir}) {
		t.Error("unjailing one directory lowered the jail in another")
	}
}

// The combination worth naming: the sandbox is down by a saved rule AND the
// approval mode auto-approves the built-ins. Each is a deliberate choice;
// together they mean built-in tools may write anywhere without asking, and the
// only other signal is the ABSENCE of a "jailed" badge.
func TestNoticeCallsOutTheAutoApprovedCombination(t *testing.T) {
	dir := unjailScratch(t)
	if err := config.UnjailPath(dir, false); err != nil {
		t.Fatal(err)
	}

	n := ResolveJailNotice(Args{Mode: ModeInteractive, CWD: dir}, config.Config{})
	if !n.Persisted {
		t.Fatal("notice did not report the saved rule")
	}
	if !n.AutoApproved {
		t.Fatal("interactive defaults to workspace approval — the notice must flag the pairing")
	}
	msg := n.Message()
	if !strings.Contains(msg, "without asking") {
		t.Errorf("message = %q, want it to say tools may act without asking", msg)
	}

	// Under an always-prompting mode the pairing does not apply: the user is
	// asked before every foreign call, so the message stays plain.
	n = ResolveJailNotice(Args{Mode: ModeInteractive, CWD: dir, Approval: "ask"}, config.Config{})
	if n.AutoApproved {
		t.Error("ask mode does not auto-approve; the notice must not claim it does")
	}
	if strings.Contains(n.Message(), "without asking") {
		t.Errorf("message = %q, want no auto-approval claim under ask", n.Message())
	}
}

// An explicit flag means the user just said it, so there is nothing to remind
// them of.
func TestNoNoticeWhenTheUserPassedAFlag(t *testing.T) {
	dir := unjailScratch(t)
	if err := config.UnjailPath(dir, false); err != nil {
		t.Fatal(err)
	}
	if msg := ResolveJailNotice(Args{Mode: ModeInteractive, CWD: dir, NoJail: true}, config.Config{}).Message(); msg != "" {
		t.Errorf("message = %q, want silence when --no-jail was passed", msg)
	}
}

// No rule, no noise.
func TestNoNoticeWithoutARule(t *testing.T) {
	dir := unjailScratch(t)
	if msg := ResolveJailNotice(Args{Mode: ModeInteractive, CWD: dir}, config.Config{}).Message(); msg != "" {
		t.Errorf("message = %q, want silence for an ordinary directory", msg)
	}
}
