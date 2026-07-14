package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

func flowManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(NewStore(filepath.Join(testsupport.TempDir(t), "auth.json")))
	m.SetOpenBrowser(false)
	return m
}

// drain collects the events emitted so far. The channel is buffered and the
// manager never blocks on it, so a short settle is enough — and every assertion
// built on this is about an event that has ALREADY been emitted synchronously by
// the call under test, or about one that must never be emitted at all.
func drain(m *Manager) []Event {
	time.Sleep(50 * time.Millisecond)
	var evs []Event
	for {
		select {
		case e := <-m.Events():
			evs = append(evs, e)
		default:
			return evs
		}
	}
}

// The bug this whole handle exists for.
//
// The manual OAuth flow leaves its pkce verifier and state parameter on the
// Manager, and the code the user pastes back is only meaningful against the flow
// that minted them. There is exactly one slot. With one user at one keyboard
// that is invisible; with a daemon serving a phone and a laptop it means the
// second login silently overwrites the first one's verifier, and the first user's
// paste is then exchanged against the second user's flow — two people's logins
// crossed, with no error anywhere.
//
// A superseded handle must be refused, not exchanged.
func TestAPasteFromASupersededLoginIsRefused(t *testing.T) {
	m := flowManager(t)

	first, err := m.StartManualOAuth("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.StartManualOAuth("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("two logins were handed the same handle; they cannot be told apart")
	}

	// If this were not refused it would reach op.Exchange — a real network call
	// to Anthropic with the wrong verifier. The refusal is what keeps it local.
	err = m.CompleteManualOAuth(context.Background(), first.ID, "some-code")
	if !errors.Is(err, ErrFlowSuperseded) {
		t.Fatalf("a paste from the superseded login returned %v, want ErrFlowSuperseded — "+
			"it would have been exchanged against the newer login's pkce verifier", err)
	}
}

// An empty handle is the shape a caller that has not been updated will pass.
// It must not be read as "whatever flow happens to be current".
func TestAnEmptyHandleIsNotAWildcard(t *testing.T) {
	m := flowManager(t)
	if _, err := m.StartManualOAuth("anthropic"); err != nil {
		t.Fatal(err)
	}
	if err := m.CompleteManualOAuth(context.Background(), "", "some-code"); !errors.Is(err, ErrFlowSuperseded) {
		t.Fatalf("an empty handle returned %v, want ErrFlowSuperseded", err)
	}
}

func TestCompletingWithNoLoginInProgressSaysSo(t *testing.T) {
	m := flowManager(t)
	if err := m.CompleteManualOAuth(context.Background(), "1", "some-code"); !errors.Is(err, ErrNoFlow) {
		t.Fatalf("got %v, want ErrNoFlow", err)
	}
}

// CancelOAuth used to tear down the callback server and leave the manual flow
// standing, so a code pasted after the user had explicitly abandoned the login
// was still exchanged and could still store a credential. Cancel means cancelled.
func TestCancelReallyEndsTheFlow(t *testing.T) {
	m := flowManager(t)
	f, err := m.StartManualOAuth("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	m.CancelOAuth()

	if err := m.CompleteManualOAuth(context.Background(), f.ID, "some-code"); !errors.Is(err, ErrNoFlow) {
		t.Fatalf("a code pasted after cancel returned %v, want ErrNoFlow — the abandoned flow could still log you in", err)
	}
}

// "canceled" has been in Event's documented vocabulary since the beginning and
// was never once emitted, so a client that cancelled and waited for a terminal
// event waited forever.
func TestCancelIsAnnounced(t *testing.T) {
	m := flowManager(t)
	if _, err := m.StartManualOAuth("anthropic"); err != nil {
		t.Fatal(err)
	}
	m.CancelOAuth()

	var kinds []string
	for _, e := range drain(m) {
		kinds = append(kinds, e.Kind)
	}
	if !contains(kinds, "canceled") {
		t.Errorf("cancelling emitted %v — no terminal event, so a client waiting on one hangs", kinds)
	}
}

// The daemon's browser is not the user's. `terva web` serves a panel that may be
// on a phone; opening a URL here would at best do nothing and at worst pop a
// window on an unattended machine.
func TestADaemonDoesNotOpenABrowserOnItsOwnHost(t *testing.T) {
	m := flowManager(t) // SetOpenBrowser(false)

	if _, err := m.StartAPIKey("anthropic"); err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	var kinds []string
	for _, e := range drain(m) {
		kinds = append(kinds, e.Kind)
	}
	// Positive control: the flow really did start, so a missing browser_open
	// below means "suppressed", not "nothing happened".
	if !contains(kinds, "started") {
		t.Fatalf("the flow never started: %v", kinds)
	}
	if contains(kinds, "browser_open") {
		t.Error("the manager opened a browser on the daemon's host")
	}
}

// Every started flow is identifiable, and no two share a handle.
func TestEveryFlowGetsItsOwnHandle(t *testing.T) {
	m := flowManager(t)
	defer m.Close()

	a, err := m.StartAPIKey("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.StartManualOAuth("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == "" || b.ID == "" {
		t.Fatalf("a flow was started with no handle: %q, %q", a.ID, b.ID)
	}
	if a.ID == b.ID {
		t.Fatalf("two flows share the handle %q", a.ID)
	}
	if a.URL == "" || b.URL == "" {
		t.Error("a started flow carries no URL to show the user")
	}
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
