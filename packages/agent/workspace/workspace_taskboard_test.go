package workspace

import (
	"testing"

	"terva.sh/terva/packages/agent/tools/tasks"
	"terva.sh/terva/packages/agent/tools/tasks/tasktool"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// TestTaskBoardSurface covers the per-session task-board pane: NotFound when the
// session has no board (chat/play/--no-tools), and a well-formed surface that
// reflects the store — including the one-active invariant riding the wire.
func TestTaskBoardSurface(t *testing.T) {
	tmp := testsupport.TempDir(t)
	w := &Workspace{sessions: map[string]*wsSession{}}

	// A session with no board => NotFound, so the TUI reports "unavailable"
	// rather than showing an empty, never-populated panel.
	bare := bareBoardSession(w)
	if bare.hasTaskBoard() {
		t.Fatal("hasTaskBoard should be false with a nil controller")
	}
	if _, err := bare.surface("taskboard"); err == nil {
		t.Fatal("taskboard on a boardless session should be NotFound")
	}

	// A session with a board: the surface mirrors the store.
	store := tasks.NewStore(tasks.NewDirFS(tmp), "agent")
	if _, err := store.Create([]tasks.CreateSpec{
		{Title: "Wire the panel", ActiveForm: "Wiring the panel", Status: tasks.StatusActive},
		{Title: "Add the segment"},
	}); err != nil {
		t.Fatalf("seed tasks: %v", err)
	}
	s := bareBoardSession(w)
	s.tasks = tasktool.New(store)
	if !s.hasTaskBoard() {
		t.Fatal("hasTaskBoard should be true once a controller is wired")
	}
	sf, err := s.surface("taskboard")
	if err != nil || sf.Kind != "taskboard" || sf.TaskBoard == nil {
		t.Fatalf("taskboard surface: %+v err=%v", sf, err)
	}
	if len(sf.TaskBoard.Tasks) != 2 {
		t.Fatalf("want 2 tasks on the board, got %d", len(sf.TaskBoard.Tasks))
	}
	active := 0
	for _, it := range sf.TaskBoard.Tasks {
		if it.Status == string(tasks.StatusActive) {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("want exactly one active task on the wire, got %d", active)
	}
}

func bareBoardSession(w *Workspace) *wsSession {
	return &wsSession{
		id:        "x",
		ws:        w,
		hub:       newWSHub(),
		agent:     core.NewAgent(nil, "fake", "", core.Registry{}),
		extPanels: map[string]*webPanel{},
	}
}
