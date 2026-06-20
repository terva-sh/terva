package extensions

import "testing"

func newSessTestManager(t *testing.T) *Manager {
	t.Helper()
	return New(t.TempDir(), "", "0.0.0-test", "anthropic", "claude-test", nil)
}

// SwapAnnouncedSession bookends a session_start with session_end only on a
// genuine session change: the first announce ends nothing, re-announcing
// the same id (a /cd) ends nothing, a switch ends the outgoing session,
// and a close-to-no-session ends the one that was active.
func TestSwapAnnouncedSessionBookends(t *testing.T) {
	m := newSessTestManager(t)

	if _, changed := m.SwapAnnouncedSession(SessionIdentity{ID: "s1"}); changed {
		t.Error("first session_start must not bookend a prior session")
	}
	if _, changed := m.SwapAnnouncedSession(SessionIdentity{ID: "s1", CWD: "/moved"}); changed {
		t.Error("re-announcing the same id (a /cd) must not end the session")
	}

	prev, changed := m.SwapAnnouncedSession(SessionIdentity{ID: "s2"})
	if !changed || prev.ID != "s1" {
		t.Errorf("switch should end s1: changed=%v prev=%q", changed, prev.ID)
	}

	prev, changed = m.SwapAnnouncedSession(SessionIdentity{})
	if !changed || prev.ID != "s2" {
		t.Errorf("close to no-session should end s2: changed=%v prev=%q", changed, prev.ID)
	}

	// With no active session left, shutdown has nothing to end.
	if _, ok := m.TakeAnnouncedSession(); ok {
		t.Error("a closed session must leave nothing for shutdown to end")
	}
}

// TakeAnnouncedSession returns the active session once (for the final
// shutdown session_end) and clears it so a second call — or a Stop after
// a switch already ended it — emits nothing.
func TestTakeAnnouncedSessionClears(t *testing.T) {
	m := newSessTestManager(t)

	if _, ok := m.TakeAnnouncedSession(); ok {
		t.Error("no session announced yet — Take should report nothing")
	}

	m.SwapAnnouncedSession(SessionIdentity{ID: "s9", Path: "/p", ProjectID: "proj"})
	got, ok := m.TakeAnnouncedSession()
	if !ok || got.ID != "s9" || got.Path != "/p" || got.ProjectID != "proj" {
		t.Fatalf("Take = %+v ok=%v, want s9/p/proj", got, ok)
	}
	if _, ok := m.TakeAnnouncedSession(); ok {
		t.Error("Take must clear — a second call reports nothing")
	}
}
