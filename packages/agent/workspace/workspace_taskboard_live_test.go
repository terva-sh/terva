package workspace

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

// The task board is what a human reads to see what the model is tracking: the
// TUI's /tasks panel, the status glance, and (soon) a web pane all render it.
//
// Every Resolve mints a FRESH task controller over a FRESH store, and
// rebuildTools installs that resolve's task tools on the agent. The session's
// own pointer (s.tasks) and the agent's per-turn context card were both bound
// once at session build. So after any rebuild the three parties disagree: the
// model's task_create writes into the new store, while the pane and the card
// keep reading the old one — a board that exists, renders, and never changes.
//
// A rebuild is not exotic. It fires when extensions finish loading, when an
// extension asserts its tool policy, on entering plan mode, on a trust flip.
// In a session with extensions it happens before the first turn.

func taskSession(t *testing.T) (*Workspace, *wsSession) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	w, err := NewWorkspace(build.Args{
		Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true,
	}, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	s := w.existing(info.ID)
	if s == nil || !s.hasTaskBoard() {
		t.Fatal("session has no task board")
	}
	return w, s
}

// createTask drives the tool the MODEL calls, reached through the agent's live
// registry — not the controller the host happens to hold. That distinction is
// the whole bug.
func createTask(t *testing.T, s *wsSession, title string) {
	t.Helper()
	tl, ok := s.agent.LookupTool("task_create")
	if !ok {
		t.Fatal("task_create is not in the agent's registry")
	}
	res, err := tl.Execute(context.Background(),
		[]byte(`{"tasks":[{"title":"`+title+`","active_form":"doing `+title+`"}]}`), func(string) {})
	if err != nil {
		t.Fatalf("task_create(%q): %v", title, err)
	}
	if res.IsError {
		t.Fatalf("task_create(%q) failed: %s", title, toolText(res))
	}
}

// boardTitles is what a client actually renders.
func boardTitles(s *wsSession) []string {
	var out []string
	for _, it := range taskBoardView(s.tasks).Tasks {
		out = append(out, it.Title)
	}
	return out
}

func TestTheTaskBoardKeepsTrackingAcrossARebuild(t *testing.T) {
	_, s := taskSession(t)

	createTask(t, s, "before")
	if got := boardTitles(s); len(got) != 1 || got[0] != "before" {
		t.Fatalf("precondition: board = %v, want [before]", got)
	}

	s.rebuildTools("extensions-ready")

	// The board the pane renders must still contain what the model recorded
	// before the rebuild, or a client shows an empty list mid-session.
	if got := boardTitles(s); len(got) != 1 || got[0] != "before" {
		t.Errorf("after a rebuild the board lost the model's existing work: %v", got)
	}

	// And a task created AFTER the rebuild has to land where the pane reads.
	createTask(t, s, "after")
	got := boardTitles(s)
	if len(got) != 2 {
		t.Errorf("the board shows %v — the model's task_create is writing to a store "+
			"nobody renders, so the panel exists and never populates", got)
	}
}

// The board was reachable only by explicit id, so it existed for the TUI and
// nowhere else. Enumerating it is what gives the web a tab — and the two panes
// must not both be called "Tasks", which is how a swarm dashboard and a todo
// list came to be indistinguishable in the tab strip.
func TestBothTaskPanesAreListedAndDistinguishable(t *testing.T) {
	_, s := taskSession(t)

	var board, swarmPane *ctrlproto.SurfaceMeta
	for i, m := range s.surfaceList() {
		switch m.ID {
		case "taskboard":
			board = &s.surfaceList()[i]
		case "tasks":
			swarmPane = &s.surfaceList()[i]
		}
	}
	if board == nil {
		t.Fatal("the task board is not enumerated, so no web client can find it")
	}
	if board.Title != "Tasks" {
		t.Errorf("task board title = %q, want Tasks", board.Title)
	}
	if board.Scope != "session" {
		t.Errorf("task board scope = %q, want session — each session tracks its own work", board.Scope)
	}
	if board.Actions {
		t.Error("the task board must not advertise actions: the model owns the list")
	}
	if swarmPane != nil && swarmPane.Title == board.Title {
		t.Errorf("both panes are titled %q — the swarm pane should read Agents", board.Title)
	}
}

// A session with no task board (chat/play/--no-tools) must not advertise the
// tab at all, or a client shows a pane whose fetch 404s.
func TestASessionWithoutATaskBoardDoesNotAdvertiseOne(t *testing.T) {
	_, s := taskSession(t)
	s.tasks = nil
	for _, m := range s.surfaceList() {
		if m.ID == "taskboard" {
			t.Fatal("a board-less session still lists the taskboard pane")
		}
	}
	if _, err := s.surface("taskboard"); err == nil {
		t.Error("fetching an absent board should fail, not return an empty pane")
	}
}

// The model's own per-turn card is fed by the same controller. If it drifts, the
// agent stops seeing the list it is supposed to be working from — a worse
// failure than the pane, because nothing on screen reveals it.
func TestTheModelsTaskCardSurvivesARebuild(t *testing.T) {
	_, s := taskSession(t)

	createTask(t, s, "keep me")
	before := s.agent.ContextProvider
	if before == nil {
		t.Fatal("precondition: no per-turn context provider wired")
	}
	if !strings.Contains(before(), "keep me") {
		t.Fatalf("precondition: the card does not carry the task: %q", before())
	}

	s.rebuildTools("approval-mode")

	card := s.agent.ContextProvider
	if card == nil {
		t.Fatal("the rebuild dropped the per-turn context provider")
	}
	if !strings.Contains(card(), "keep me") {
		t.Errorf("after a rebuild the model's task card no longer shows its own open work: %q", card())
	}

	// The sharp end: work recorded AFTER the rebuild. Checking only that the
	// pre-rebuild task survived passes even when the card is frozen on a stale
	// store — which is exactly the broken state, so it has to be the new task
	// that is asserted.
	createTask(t, s, "added later")
	if !strings.Contains(card(), "added later") {
		t.Errorf("the model cannot see a task it just created — its card reads a store "+
			"its own tools no longer write to: %q", card())
	}
}
