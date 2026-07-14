package modes

import (
	"errors"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// errNoStream is what a carrier that cannot serve an address returns.
var errNoStream = errors.New("no such stream")

// The TUI subscribes to the WORKSPACE as well as to its session.
//
// Workspace-scoped events — a workspace surface changing, the locale changing, a
// notice — used to be stamped with each live session's id by the workspace
// itself, which is the only reason a session-only subscriber ever saw them. They
// are published once now, to their own address, so a client that does not
// subscribe there simply stops receiving them. This is that subscription: a
// settings surface_updated on the workspace stream must still drive the TUI's
// approval-mode refresh, which it can only do if the event arrived at all.
func TestTheTUIHearsTheWorkspace(t *testing.T) {
	fc := newFakeCarrier()
	fc.wsStream = make(chan ctrlproto.Event, 16)
	fetched := make(chan string, 8)
	fc.onSurface = func(id string) {
		select {
		case fetched <- id:
		default:
		}
	}

	startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})

	fc.wsStream <- ctrlproto.SurfaceUpdatedEvent("settings")

	if !sawSurface(fetched, "settings", 3*time.Second) {
		t.Fatal("a workspace surface update never reached the TUI — the workspace pump is not subscribed, " +
			"and every workspace-scoped event (settings, tasks, mcp, chat, permissions, raati, locale) is now invisible to it")
	}
}

// The pump is NOT bound to a session, so a session switch must not tear it down:
// the workspace does not change when the focused session does. runCarrierLoop
// re-binds on a switch (that is its job); runWorkspaceLoop must not care.
func TestTheWorkspacePumpSurvivesASessionSwitch(t *testing.T) {
	fc := newFakeCarrier()
	fc.wsStream = make(chan ctrlproto.Event, 16)
	fetched := make(chan string, 8)
	fc.onSurface = func(id string) {
		select {
		case fetched <- id:
		default:
		}
	}

	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})

	fc.wsStream <- ctrlproto.SurfaceUpdatedEvent("settings")
	if !sawSurface(fetched, "settings", 3*time.Second) {
		t.Fatal("the first workspace event never arrived")
	}

	if err := h.i.SwitchCarrierSession("s2"); err != nil {
		t.Fatalf("switch session: %v", err)
	}

	fc.wsStream <- ctrlproto.SurfaceUpdatedEvent("settings")
	if !sawSurface(fetched, "settings", 3*time.Second) {
		t.Fatal("a workspace event after a session switch never arrived — the workspace pump was torn down " +
			"with the session binding, but the workspace did not change")
	}
}

// A carrier with no workspace stream at all — a replay carrier, or a daemon too
// old to know the address — must not wedge the pump. It gives up, and those
// events reach the client the old way: the daemon's compat relay stamps them
// onto the session subscription instead.
func TestAPumpWithNoWorkspaceStreamGivesUpQuietly(t *testing.T) {
	fc := newFakeCarrier()
	fc.subErr = errNoStream

	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})

	done := make(chan struct{})
	go func() {
		h.i.runWorkspaceLoop(t.Context())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the workspace pump never gave up on a carrier with no workspace stream; it would spin forever")
	}
}

func sawSurface(ch <-chan string, want string, d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case got := <-ch:
			if got == want {
				return true
			}
		case <-deadline:
			return false
		}
	}
}
