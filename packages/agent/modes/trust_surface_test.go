package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/tui"
)

// The /permissions inspector answers "what may this session do". It listed the
// approval mode, the compiled rules and the session grants, and said nothing
// about Workspace Trust — which is the boundary that decides whether the
// project's own extensions, skills, and context are loaded at all. A pane that
// omits it reads as a complete answer when it is half of one.

func permissionsText(t *testing.T, pv ctrlproto.PermissionsView) string {
	t.Helper()
	info, _ := renderPermissionsWireView(tui.Theme{}, pv)
	return strings.Join(info, "\n")
}

func TestPermissionsInspectorStatesTheTrustPosture(t *testing.T) {
	restricted := permissionsText(t, ctrlproto.PermissionsView{
		Mode: "ask", CWD: "/work/cloned-repo", Trusted: false,
	})
	if !strings.Contains(restricted, "/work/cloned-repo") {
		t.Errorf("the inspector does not name the directory the verdict applies to:\n%s", restricted)
	}
	if !strings.Contains(restricted, "restricted") {
		t.Errorf("an untrusted workspace is not reported as restricted:\n%s", restricted)
	}
	if !strings.Contains(restricted, "/trust") {
		t.Errorf("the inspector reports the restriction without saying how to lift it:\n%s", restricted)
	}

	trusted := permissionsText(t, ctrlproto.PermissionsView{
		Mode: "ask", CWD: "/work/mine", Trusted: true,
	})
	if !strings.Contains(trusted, "trusted") || strings.Contains(trusted, "restricted") {
		t.Errorf("a trusted workspace is not reported as trusted:\n%s", trusted)
	}

	// Yolo takes an early return that used to print one line and stop. Trust is
	// the only boundary still standing there, so it is the line that matters most.
	yolo := permissionsText(t, ctrlproto.PermissionsView{
		Mode: "yolo", CWD: "/work/cloned-repo", Trusted: false,
	})
	if !strings.Contains(yolo, "restricted") {
		t.Errorf("the yolo short-circuit drops the trust posture entirely:\n%s", yolo)
	}

	// A daemon that predates the field sends no cwd. Render nothing rather than
	// assert "restricted" from a zero value — a false report about a security
	// boundary is worse than no report.
	old := permissionsText(t, ctrlproto.PermissionsView{Mode: "ask"})
	if strings.Contains(old, "workspace trust") {
		t.Errorf("a cwd-less view invented a trust verdict:\n%s", old)
	}
}

// /trust told ctrlproto users to restart terva for a change that had already
// taken effect across every open session — the most reliable way to make a
// working feature look broken.
func TestSlashTrustDoesNotAskForARestartWhenTheHostAppliedIt(t *testing.T) {
	i := &Interactive{
		turns: newTurnEngine(),
		cfg: InteractiveConfig{
			CWD:              "/work/repo",
			TrustAppliesLive: true,
			TrustWorkspace:   func(bool) error { return nil },
			UntrustWorkspace: func() error { return nil },
		},
	}

	i.slashTrust(nil, []string{"/trust"}, "/trust")
	if strings.Contains(i.statusOK, "restart") {
		t.Errorf("/trust still asks for a restart: %q", i.statusOK)
	}
	if !strings.Contains(i.statusOK, "/work/repo") || i.statusErr != "" {
		t.Errorf("/trust should confirm against the directory, got %q / err %q", i.statusOK, i.statusErr)
	}

	i.slashUntrust(nil, []string{"/untrust"}, "/untrust")
	if strings.Contains(i.statusOK, "next launch") {
		t.Errorf("/untrust defers a change the host already applied: %q", i.statusOK)
	}
}

// The host that does NOT re-apply must keep saying so — the note is only wrong
// where it is untrue.
func TestSlashTrustStillWarnsWhenNothingReapplied(t *testing.T) {
	i := &Interactive{
		turns: newTurnEngine(),
		cfg: InteractiveConfig{
			CWD:            "/work/repo",
			TrustWorkspace: func(bool) error { return nil },
		},
	}
	i.slashTrust(nil, []string{"/trust"}, "/trust")
	if !strings.Contains(i.statusOK, "restart") {
		t.Errorf("a host with no live re-apply should still say a restart is needed, got %q", i.statusOK)
	}
}

// Toggling trust from /settings has to move the TUI's own copy of the verdict:
// it feeds /status and the untrusted reminder, which would otherwise keep
// nagging about a workspace the user just trusted from the pane next door.
func TestSettingsTrustToggleMovesTheLocalVerdict(t *testing.T) {
	i := &Interactive{turns: newTurnEngine(), cfg: InteractiveConfig{CWD: "/work/repo"}}

	i.applyLocalSettingEffect("trust", "true")
	if !i.cfg.Trusted {
		t.Error("the settings toggle left the TUI believing the workspace is untrusted")
	}
	i.applyLocalSettingEffect("trust", "false")
	if i.cfg.Trusted {
		t.Error("toggling trust off left the TUI believing the workspace is trusted")
	}
}
