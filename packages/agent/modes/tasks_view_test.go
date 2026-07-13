package modes

import (
	"errors"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/agent/tools/tasks"
)

func taskboardSurface(items ...ctrlproto.TaskBoardItem) ctrlproto.Surface {
	return ctrlproto.Surface{ID: "taskboard", Kind: "taskboard", TaskBoard: &ctrlproto.TaskBoardView{Tasks: items}}
}

// TestCarrierTaskBoard exercises the /tasks panel + status glance seam: /tasks
// fetches and opens over the live board, the cache maps wire items to
// tasks.Task for the shared renderers, a surface_updated refetch tracks the
// daemon, and a boardless session (NotFound) empties the glance and refuses to
// open the panel.
func TestCarrierTaskBoard(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc
	i.tasksDialog = dialogs.NewTasksDialog()
	fc.panels = map[string]ctrlproto.Surface{
		"taskboard": taskboardSurface(
			ctrlproto.TaskBoardItem{ID: "task-1", Status: string(tasks.StatusActive), Title: "Wire the panel", ActiveForm: "Wiring the panel"},
			ctrlproto.TaskBoardItem{ID: "task-2", Status: string(tasks.StatusPending), Title: "Add the segment"},
			ctrlproto.TaskBoardItem{ID: "task-3", Status: string(tasks.StatusDone), Title: "Ship the wire type"},
		),
	}

	// /tasks fetches, opens the panel, and populates the cache.
	i.openTasksDialog()
	if !i.tasksDialog.Active() {
		t.Fatal("/tasks should open the panel when a board exists")
	}
	rows := i.taskBoardRows()
	if len(rows) != 3 {
		t.Fatalf("taskBoardRows = %d, want 3", len(rows))
	}
	if rows[0].Status != tasks.StatusActive || rows[0].ActiveForm != "Wiring the panel" {
		t.Fatalf("first row not mapped from the wire: %+v", rows[0])
	}

	// The glance leads with the active task (its present-continuous form).
	if g := i.taskBoardGlance(); !strings.Contains(g, "Wiring the panel") {
		t.Fatalf("glance = %q, want the active task", g)
	}

	// A surface_updated refetch tracks the daemon moving the board.
	fc.mu.Lock()
	fc.panels["taskboard"] = taskboardSurface(
		ctrlproto.TaskBoardItem{ID: "task-2", Status: string(tasks.StatusActive), Title: "Add the segment", ActiveForm: "Adding the segment"},
	)
	fc.mu.Unlock()
	i.refreshCarrierTaskBoard()
	if got := len(i.taskBoardRows()); got != 1 {
		t.Fatalf("post-refresh rows = %d, want 1", got)
	}

	// A boardless session (NotFound) empties the glance and refuses to open.
	fc.mu.Lock()
	delete(fc.panels, "taskboard")
	fc.mu.Unlock()
	i.tasksDialog.Close()
	i.refreshCarrierTaskBoard()
	if g := i.taskBoardGlance(); g != "" {
		t.Fatalf("glance after the board is gone = %q, want empty", g)
	}
	i.openTasksDialog()
	if i.tasksDialog.Active() {
		t.Fatal("/tasks must not open when the board is unavailable")
	}
}

// TestOpenTasksDialogClassifiesErrors pins finding #7: a boardless session
// (NotFound/Unsupported) keeps the terse "unavailable in this mode", but a
// transport or server failure is surfaced with context so a broken carrier
// doesn't masquerade as an intentional limitation.
func TestOpenTasksDialogClassifiesErrors(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc
	i.tasksDialog = dialogs.NewTasksDialog()

	// Boardless session → terse message, panel stays closed.
	fc.surfErr = ctrlproto.Errorf(ctrlproto.CodeNotFound, "no task board in this session")
	i.openTasksDialog()
	if got := i.statusErr; !strings.Contains(got, "unavailable in this mode") {
		t.Fatalf("NotFound status = %q, want the terse 'unavailable in this mode'", got)
	}
	if i.tasksDialog.Active() {
		t.Fatal("panel must not open on NotFound")
	}

	// Transport failure (not a coded ctrlproto error) → preserved with context.
	fc.surfErr = errors.New("disconnected from daemon")
	i.openTasksDialog()
	if got := i.statusErr; !strings.Contains(got, "disconnected from daemon") {
		t.Fatalf("transport status = %q, want the preserved error text", got)
	}

	// A server fault (coded, but not NotFound/Unsupported) is likewise preserved.
	fc.surfErr = ctrlproto.Errorf(ctrlproto.CodeInternal, "boom")
	i.openTasksDialog()
	if got := i.statusErr; !strings.Contains(got, "boom") {
		t.Fatalf("internal-error status = %q, want the preserved server error", got)
	}
}

// TestSwitchCarrierSessionClearsTaskBoard: switching sessions must drop the old
// board synchronously, so the /tasks glance and panel never flash the previous
// session's tasks in the beat before the new binding's refresh lands.
func TestSwitchCarrierSessionClearsTaskBoard(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	fc.infos = map[string]ctrlproto.SessionInfo{"s2": {ID: "s2"}}
	i.cfg.Carrier = fc
	i.cfg.CarrierSession = "s1"
	fc.panels = map[string]ctrlproto.Surface{
		"taskboard": taskboardSurface(
			ctrlproto.TaskBoardItem{ID: "task-1", Status: string(tasks.StatusActive), Title: "s1 work", ActiveForm: "Doing s1 work"},
		),
	}

	// s1 has a live board.
	i.refreshCarrierTaskBoard()
	if got := len(i.taskBoardRows()); got != 1 {
		t.Fatalf("precondition: s1 rows = %d, want 1", got)
	}

	if err := i.SwitchCarrierSession("s2"); err != nil {
		t.Fatalf("SwitchCarrierSession: %v", err)
	}

	// The board is gone immediately — before any s2 refresh could have run.
	if got := i.taskBoardRows(); got != nil {
		t.Fatalf("post-switch rows = %+v, want nil (board cleared synchronously)", got)
	}
	if g := i.taskBoardGlance(); g != "" {
		t.Fatalf("post-switch glance = %q, want empty", g)
	}
}

// TestCarrierTaskBoardReorderedResponse: a board fetch is async, so a switch can
// land while a fetch is in flight. A delayed response for the switched-away
// session must NOT overwrite the newer board. Scenario: request A, switch to B
// (whose board lands), then A returns — only B stays visible.
func TestCarrierTaskBoardReorderedResponse(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc
	i.cfg.CarrierSession = "A"
	// The fetch A launches returns A's board…
	fc.panels = map[string]ctrlproto.Surface{
		"taskboard": taskboardSurface(
			ctrlproto.TaskBoardItem{ID: "a-1", Status: string(tasks.StatusActive), Title: "from A", ActiveForm: "From A"},
		),
	}
	// …but while A's fetch is in flight the session switches to B and B's board
	// lands. Model that by mutating state inside the fetch, exactly once.
	var once bool
	fc.onSurface = func(id string) {
		if id != "taskboard" || once {
			return
		}
		once = true
		i.mu.Lock()
		i.cfg.CarrierSession = "B"
		i.carrierTaskBoard = []ctrlproto.TaskBoardItem{{ID: "b-1", Status: string(tasks.StatusActive), Title: "from B", ActiveForm: "From B"}}
		i.carrierTaskBoardSession = "B"
		i.mu.Unlock()
	}

	// A's (now stale) response comes back and tries to commit.
	i.refreshCarrierTaskBoard()

	rows := i.taskBoardRows()
	if len(rows) != 1 || rows[0].ID != "b-1" {
		t.Fatalf("visible board = %+v, want only B's b-1 (A's late response must not overwrite it)", rows)
	}
}

// TestInteractiveTasksPanel drives /tasks end-to-end through the real Run()
// loop: the command opens the overlay, the panel headers with the active task
// and collapses completed work, 'd' expands it, and esc closes it.
func TestInteractiveTasksPanel(t *testing.T) {
	fc := newFakeCarrier()
	fc.panels = map[string]ctrlproto.Surface{
		"taskboard": taskboardSurface(
			ctrlproto.TaskBoardItem{ID: "task-1", Status: string(tasks.StatusActive), Title: "Wire the panel", ActiveForm: "Wiring the panel"},
			ctrlproto.TaskBoardItem{ID: "task-2", Status: string(tasks.StatusPending), Title: "Add the segment"},
			ctrlproto.TaskBoardItem{ID: "task-3", Status: string(tasks.StatusDone), Title: "Ship the wire type"},
		),
	}
	h := startInteractive(t, func(c *InteractiveConfig) { c.Carrier = fc })
	h.dismissLoginDialog()

	h.term.Type("/tasks\r")
	h.waitText("Tasks · Wiring the panel") // header surfaces the active task
	h.waitText("Add the segment")          // open work is listed
	h.waitText("done (1)")                 // completed collapses to a count
	h.waitGone("Ship the wire type")       // …and isn't listed until expanded

	h.term.Type("d") // expand completed
	h.waitText("Ship the wire type")

	h.term.Type("\x1b") // esc closes
	h.waitGone("Tasks · Wiring the panel")
}
