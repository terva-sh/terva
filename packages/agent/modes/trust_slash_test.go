package modes

import (
	"strings"
	"testing"
)

// /trust dispatches to TrustWorkspace, marks the session trusted, and
// (with no ChangeCWD wired) reports a restart-to-apply note. The
// `parent` argument is threaded through.
func TestSlashTrustInvokesHostHook(t *testing.T) {
	var gotParent *bool
	i := &Interactive{
		turns: newTurnEngine(),
		cfg: InteractiveConfig{
			CWD:     "/work/repo",
			Trusted: false,
			TrustWorkspace: func(parent bool) error {
				p := parent
				gotParent = &p
				return nil
			},
		},
	}

	if done := i.slashTrust(nil, []string{"/trust"}, "/trust"); done {
		t.Fatal("/trust should not end the session")
	}
	if gotParent == nil || *gotParent {
		t.Fatalf("plain /trust should call TrustWorkspace(false), got %v", gotParent)
	}
	if !i.cfg.Trusted {
		t.Error("/trust should mark the session trusted")
	}
	if !strings.Contains(i.statusOK, "trusted") {
		t.Errorf("status should confirm the trust, got %q / err %q", i.statusOK, i.statusErr)
	}

	// /trust parent threads parent=true.
	gotParent = nil
	i.slashTrust(nil, []string{"/trust", "parent"}, "/trust parent")
	if gotParent == nil || !*gotParent {
		t.Fatalf("/trust parent should call TrustWorkspace(true), got %v", gotParent)
	}
}

// /trust without a wired host hook reports a clear error instead of
// no-oping or panicking.
func TestSlashTrustUnwiredErrors(t *testing.T) {
	i := &Interactive{turns: newTurnEngine(), cfg: InteractiveConfig{}}
	i.slashTrust(nil, []string{"/trust"}, "/trust")
	if !strings.Contains(i.statusErr, "TrustWorkspace") {
		t.Errorf("unwired /trust should name the missing hook, got %q", i.statusErr)
	}
}

// /untrust dispatches to UntrustWorkspace and clears the session trust.
func TestSlashUntrustInvokesHostHook(t *testing.T) {
	called := false
	i := &Interactive{
		turns: newTurnEngine(),
		cfg: InteractiveConfig{
			CWD:              "/work/repo",
			Trusted:          true,
			UntrustWorkspace: func() error { called = true; return nil },
		},
	}
	i.slashUntrust(nil, []string{"/untrust"}, "/untrust")
	if !called {
		t.Error("/untrust should call UntrustWorkspace")
	}
	if i.cfg.Trusted {
		t.Error("/untrust should clear the session trust flag")
	}
	if !strings.Contains(i.statusOK, "untrusted") {
		t.Errorf("status should confirm untrust, got %q / err %q", i.statusOK, i.statusErr)
	}
}

// Both trust commands are registered, visible, and cancel the active
// turn (they rebuild the agent / mutate trust state).
func TestTrustCommandsRegistered(t *testing.T) {
	for _, name := range []string{"/trust", "/untrust"} {
		spec, ok := lookupSlash(name)
		if !ok {
			t.Errorf("%q not registered", name)
			continue
		}
		if spec.hidden {
			t.Errorf("%q should be visible in the popup/help", name)
		}
		if !slashCancelsTurn(name) {
			t.Errorf("%q should cancel the active turn", name)
		}
	}
}
