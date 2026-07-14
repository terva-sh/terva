package modes

// --resume boots into the session picker (docs/proposals/resume-picker.md
// stage 2): OpenSessionsOnBoot opens the /sessions dialog over the freshly
// booted session instead of the retired pre-TUI stderr picker. Esc falls
// through to the session the boot already bound. On a credential-less boot
// the login dialog keeps priority and the picker opens on the first
// successful login — one-shot, so a later re-login must not reopen it.

import (
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

func bootPickerSummaries() []core.SessionSummary {
	return []core.SessionSummary{
		{
			Path:         "/tmp/sessions/one.jsonl",
			Started:      time.Now().Add(-2 * time.Hour),
			Provider:     "test",
			Model:        "test-model",
			MessageCount: 4,
			Title:        "resume-me-title",
		},
		{
			Path:         "/tmp/sessions/empty.jsonl",
			Started:      time.Now().Add(-1 * time.Hour),
			Provider:     "test",
			Model:        "test-model",
			MessageCount: 0, // filtered: resuming an empty session is a no-op
		},
	}
}

// A ready --resume boot opens the picker on the first frame, lists via the
// cfg seam (titles included), and Esc closes it onto the booted session.
func TestResumeBootOpensSessionPicker(t *testing.T) {
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.OpenSessionsOnBoot = true
		cfg.ListSessions = bootPickerSummaries
	})
	h.waitText("── sessions")
	h.waitText("resume-me-title")
	// Esc: fall through to the session the boot already bound.
	h.term.Type("\x1b")
	h.waitGone("── sessions")
	// The TUI is a normal prompt afterwards.
	h.term.Type("still alive")
	h.waitText("still alive")
}

// A ready boot WITHOUT the flag must not open the picker (the dialog is
// opt-in, not a new default).
func TestNoResumeFlagNoBootPicker(t *testing.T) {
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.ListSessions = bootPickerSummaries
	})
	h.waitText("i'm Mieli")
	if h.i.sessionDialog.Active() {
		t.Fatal("session picker opened without OpenSessionsOnBoot")
	}
}

// The picker's `g` runs on-demand title generation through the cfg seam off
// the main loop and lands the fresh title back on the open dialog's row —
// the full stage-3 TUI flow against the real Run loop.
func TestSessionPickerGenerateTitleUpdatesRow(t *testing.T) {
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.OpenSessionsOnBoot = true
		cfg.ListSessions = bootPickerSummaries
		cfg.GenerateSessionTitle = func(path string) (string, error) {
			if path != "/tmp/sessions/one.jsonl" {
				t.Errorf("generate called for %q", path)
			}
			return "a much better title", nil
		}
	})
	h.waitText("resume-me-title")
	h.term.Type("g")
	h.waitText("a much better title")
	h.waitGone("resume-me-title")
}

// Without a wired seam (e.g. attached to a pre-generate-title daemon) the
// binding reports unavailability instead of dying or firing blind.
func TestSessionPickerGenerateTitleUnwired(t *testing.T) {
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.OpenSessionsOnBoot = true
		cfg.ListSessions = bootPickerSummaries
	})
	h.waitText("resume-me-title")
	h.term.Type("g")
	h.waitText("title generation isn't available here")
}

// A credential-less --resume boot defers to the login dialog; the picker
// opens on the first successful login and only once — a re-login later in
// the session must not resurface it.
func TestResumeBootDefersToLoginThenOpensOnce(t *testing.T) {
	fc := newFakeCarrier()
	fc.infos = map[string]ctrlproto.SessionInfo{"fresh": {ID: "fresh"}}
	i := NewInteractive(InteractiveConfig{
		Theme:              tui.Dark,
		Carrier:            fc,
		Ready:              false,
		OpenSessionsOnBoot: true,
		ListSessions:       bootPickerSummaries,
		CarrierLogin: func(current string) (ctrlproto.SessionInfo, error) {
			return ctrlproto.SessionInfo{ID: "fresh"}, nil
		},
	})
	if i.sessionDialog.Active() {
		t.Fatal("picker opened before login on a credential-less boot")
	}
	i.finishCarrierLogin(ctrlproto.AuthState{Kind: "success", Provider: "test", Method: "oauth"})
	if !i.sessionDialog.Active() {
		t.Fatal("picker did not open after the first login")
	}
	if i.bootSessionsPending {
		t.Fatal("boot picker still armed after opening")
	}
	// Re-login: the one-shot must not fire again.
	i.sessionDialog.Close()
	i.finishCarrierLogin(ctrlproto.AuthState{Kind: "success", Provider: "test", Method: "oauth"})
	if i.sessionDialog.Active() {
		t.Fatal("re-login reopened the boot picker")
	}
}
