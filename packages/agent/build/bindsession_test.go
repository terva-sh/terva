package build

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/tools/tasks"
	"terva.sh/terva/packages/agent/tools/tasks/tasktool"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// boardFixture returns a controller backed by a real on-disk store, plus the
// directory it writes to.
func boardFixture(t *testing.T) (*tasktool.Controller, string) {
	t.Helper()
	dir := testsupport.TempDir(t)
	upper := filepath.Join(dir, "tasks")
	return tasktool.New(tasks.NewStore(tasks.NewLayeredFS(upper, filepath.Join(dir, "ext-data")), "agent")), upper
}

func boardFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// The board is keyed by exactly one thing, and this is it. An unbound store has
// no file name and declines to persist, so a host that binds a session and
// forgets the board has one that works in memory and is never written — which is
// what rpc and ACP shipped.
func TestBindingASessionIsWhatMakesTheBoardPersist(t *testing.T) {
	home := testsupport.TempDir(t)
	sess, err := core.NewSession(home, home, "anthropic", "some-model", "test")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer sess.Close()

	ctrl, boardDir := boardFixture(t)

	// Unbound: a task is accepted and nothing reaches disk.
	if _, err := ctrl.Store().Create([]tasks.CreateSpec{{Title: "before binding"}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := boardFiles(t, boardDir); len(got) != 0 {
		t.Fatalf("an unbound board wrote %v; it has no session to write under", got)
	}

	ag := core.NewAgent(nil, "some-model", "", nil)
	BindSession(SessionBinding{Agent: ag, Tasks: ctrl, Session: sess})

	// Bound: the pre-bind work is carried into the session's own file.
	files := boardFiles(t, boardDir)
	if len(files) != 1 {
		t.Fatalf("board files after binding = %v, want exactly the session's", files)
	}
	if list := ctrl.Store().List(); len(list) != 1 || list[0].Title != "before binding" {
		t.Errorf("the board lost the work it held before binding: %+v", list)
	}
	// The identity is the transcript this agent writes to — the file, which is
	// what terva_status reports; sess.ID is the meta UUID and a different thing.
	if _, path := ag.SessionIdentity(); path != sess.Path {
		t.Errorf("agent session path = %q, want %q", path, sess.Path)
	}
}

// Binding to no session is the close case: the identity clears. One verb covers
// open, switch and close, which is why Session is a field rather than a
// precondition.
func TestBindingNoSessionClearsTheIdentity(t *testing.T) {
	home := testsupport.TempDir(t)
	sess, err := core.NewSession(home, home, "anthropic", "some-model", "test")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer sess.Close()

	ag := core.NewAgent(nil, "some-model", "", nil)
	BindSession(SessionBinding{Agent: ag, Session: sess})
	if id, _ := ag.SessionIdentity(); id == "" {
		t.Fatal("binding a session left no identity")
	}
	BindSession(SessionBinding{Agent: ag})
	if id, path := ag.SessionIdentity(); id != "" || path != "" {
		t.Errorf("identity after binding to no session = %q / %q, want cleared", id, path)
	}
}

// Supplying a manager is what announces the session to extensions. The manager
// remembers the last-announced identity — that record is what Manager.Stop later
// turns into the matching session_end, so a host that never announces gets
// neither event. ACP was that host.
func TestSupplyingAManagerAnnouncesTheSession(t *testing.T) {
	home := testsupport.TempDir(t)
	sess, err := core.NewSession(home, home, "anthropic", "some-model", "test")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer sess.Close()

	mgr := extensions.New(home, home, "test", "anthropic", "some-model", NonInteractiveExtHooks{})

	// Without a manager there is nothing to announce to, and nothing recorded.
	BindSession(SessionBinding{Session: sess})
	if _, ok := mgr.TakeAnnouncedSession(); ok {
		t.Fatal("a binding with no manager announced something")
	}

	BindSession(SessionBinding{Ext: mgr, Session: sess})
	got, ok := mgr.TakeAnnouncedSession()
	if !ok {
		t.Fatal("the session was not announced, so Manager.Stop will emit no session_end for it either")
	}
	if got.Path != sess.Path {
		t.Errorf("announced session path = %q, want %q", got.Path, sess.Path)
	}
}

// Every field is optional, because the hosts genuinely differ: a swarm child has
// no board, and rpc and the daemon split the event in two so the announcement
// can wait for their background extension start.
func TestEveryFieldIsOptional(t *testing.T) {
	BindSession(SessionBinding{}) // must not panic
}
