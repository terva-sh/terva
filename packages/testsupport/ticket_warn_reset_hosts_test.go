package testsupport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The ticket direct-edit warning fires once per turn. The turn boundary reaches
// it as an event observer, build.TicketEditWarnResetObserver, and every host
// that runs an agent has to compose it.
//
// A host that drops it does not break. It degrades to one warning per session,
// which is the behaviour this replaced and is invisible from the outside: no
// error, no log line, just a model that stops being reminded. That is why the
// wiring is held here by name rather than left to review.
//
// This is a source gate and it knows it. It cannot prove the observer runs. It
// proves the call is present and, where a host has a conditional that would
// swallow it, that the call sits outside that conditional. The behaviour itself
// is tested in packages/agent/build.

func hostSource(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{repoRoot}, parts...)...)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(parts...), err)
	}
	return string(src)
}

const resetObserver = "TicketEditWarnResetObserver"

func TestTicketWarnResetIsComposedByEveryHost(t *testing.T) {
	hosts := []struct {
		name  string
		parts []string
	}{
		{"the CLI and TUI", []string{"packages", "agent", "cli.go"}},
		{"ACP", []string{"packages", "agent", "acp_mode.go"}},
		{"the workspace session", []string{"packages", "agent", "workspace", "workspace_session.go"}},
	}

	for _, h := range hosts {
		t.Run(h.name, func(t *testing.T) {
			if !strings.Contains(hostSource(t, h.parts...), resetObserver) {
				t.Errorf("%s no longer composes %s.\n"+
					"Its sessions fall back to one ticket-edit warning per session, "+
					"silently. If this host genuinely should not have it, say so here "+
					"rather than deleting the case.", h.name, resetObserver)
			}
		})
	}
}

// The trap, held open. In cli.go the workspace observer is composed inside
// `if extMgr != nil`, so the obvious place to add a second observer is right
// beside it. The ticket-edit warning owes nothing to extensions, and a session
// running without them would then lose the per-turn reset with no sign of it.
//
// The rule is an ordering one, the same shape as the merge-style guard: the
// reset must be composed BEFORE the extension gate it must not live in.
func TestTicketWarnResetIsNotGatedOnExtensionsInCLI(t *testing.T) {
	body := hostSource(t, "packages", "agent", "cli.go")

	reset := strings.Index(body, resetObserver)
	if reset < 0 {
		t.Fatalf("cli.go no longer composes %s at all", resetObserver)
	}
	gate := strings.Index(body, "if extMgr != nil {")
	if gate < 0 {
		t.Skip("cli.go no longer has an extension gate; re-anchor this test")
	}

	if reset > gate {
		t.Errorf("cli.go composes %s (offset %d) AFTER the `if extMgr != nil` gate "+
			"(offset %d), which suggests it moved inside it.\n"+
			"The per-turn reset has nothing to do with extensions. Inside that gate, "+
			"a session with extensions off silently reverts to one warning per "+
			"session, and nothing reports it.", resetObserver, reset, gate)
	}
}

// The same trap in ACP, which cannot be checked by ordering because its
// observer flows through a variable. There, `observe` is deliberately nil when
// neither extensions nor hooks are on, so the reset needs its own branch or it
// disappears with the rest.
func TestTicketWarnResetSurvivesACPWithoutExtensions(t *testing.T) {
	body := hostSource(t, "packages", "agent", "acp_mode.go")

	if !strings.Contains(body, "observe = ticketWarnReset") {
		t.Error("acp_mode.go has no branch that keeps the ticket-edit reset when " +
			"extensions and hooks are both off.\n" +
			"`observe` is nil in that case, so composing the reset only into the " +
			"extension branch loses it entirely for a plain ACP session.")
	}
}
