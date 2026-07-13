package modes

import (
	"context"
	"errors"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tools/tasks"
	"terva.sh/terva/packages/i18n"
)

// The /tasks panel and the status-bar task glance both render the per-session
// task board (the built-in task_* list). The board lives daemon-side and the
// TUI holds no *core.Agent, so it rides the "taskboard" ctrlproto surface —
// distinct from the workspace swarm dashboard ("tasks", see swarm_slash.go).
//
// The cache is push-filled by the pump (refreshCarrierTaskBoard on
// surface_updated("taskboard") and on snapshot resync) so the per-frame render
// path only reads it — never a synchronous fetch mid-frame. /tasks itself does
// one explicit fetch on open, both to freshen and to learn whether the session
// even has a board (chat/play/--no-tools => NotFound => "unavailable").

// refreshCarrierTaskBoard fetches the board surface off the pump goroutine and
// replaces the cache, then repaints. A NotFound (no board in this session)
// clears the cache so the glance vanishes and a stale panel empties.
func (i *Interactive) refreshCarrierTaskBoard() {
	if i.cfg.Carrier == nil {
		return
	}
	sess := i.carrierSession()
	sf, err := i.cfg.Carrier.Surface(context.Background(), sess, "taskboard")
	i.mu.Lock()
	if i.cfg.CarrierSession != sess {
		// A switch landed while this fetch was in flight — its result belongs to
		// the previous binding. Drop it; the new binding's own refresh fills the
		// board (a delayed A response must not overwrite B's newer board).
		i.mu.Unlock()
		return
	}
	if err != nil || sf.TaskBoard == nil {
		i.carrierTaskBoard = nil
	} else {
		i.carrierTaskBoard = sf.TaskBoard.Tasks
	}
	i.carrierTaskBoardSession = sess
	i.mu.Unlock()
	i.invalidate()
}

// taskBoardRows maps the cached wire items to tasks.Task so the shared
// renderers (tasks.PanelTitle/PanelLines/StatusGlance) drive both the panel and
// the glance — one renderer, no drift. Reads the cache under mu; no fetch.
func (i *Interactive) taskBoardRows() []tasks.Task {
	i.mu.Lock()
	var items []ctrlproto.TaskBoardItem
	if i.carrierTaskBoardSession == i.cfg.CarrierSession {
		items = i.carrierTaskBoard
	}
	i.mu.Unlock()
	if len(items) == 0 {
		return nil
	}
	rows := make([]tasks.Task, 0, len(items))
	for _, it := range items {
		rows = append(rows, tasks.Task{
			ID:         it.ID,
			Title:      it.Title,
			ActiveForm: it.ActiveForm,
			Status:     tasks.Status(it.Status),
			Evidence:   it.Evidence,
			Note:       it.Note,
		})
	}
	return rows
}

// taskBoardGlance is the status-bar segment text (empty when there are no
// tasks). Computed from the cached board each frame; cheap for a short list.
func (i *Interactive) taskBoardGlance() string {
	return tasks.StatusGlance(i.taskBoardRows())
}

// openTasksDialog backs /tasks: fetch the board once (freshen + capability
// check), then open the read-only panel over the live cache. A fetch error
// means the session has no task board (chat/play/--no-tools).
func (i *Interactive) openTasksDialog() {
	if i.cfg.Carrier == nil {
		i.setStatusErr(i18n.T("tasks are unavailable in this mode"))
		return
	}
	sess := i.carrierSession()
	sf, err := i.cfg.Carrier.Surface(context.Background(), sess, "taskboard")
	if err != nil {
		// A session that simply has no board (chat/play/--no-tools) answers
		// NotFound/Unsupported — a real "not available here", keep the terse
		// message. Anything else — a disconnected carrier (transport), a missing
		// session, or a server fault — is surfaced with context so a broken
		// carrier doesn't masquerade as an intentional limitation.
		var ce *ctrlproto.Error
		if errors.As(err, &ce) && (ce.Code == ctrlproto.CodeNotFound || ce.Code == ctrlproto.CodeUnsupported) {
			i.setStatusErr(i18n.T("tasks are unavailable in this mode"))
		} else {
			i.setStatusErr(i18n.T("tasks unavailable: %s", err.Error()))
		}
		return
	}
	if sf.TaskBoard == nil {
		i.setStatusErr(i18n.T("tasks are unavailable in this mode"))
		return
	}
	i.mu.Lock()
	if i.cfg.CarrierSession == sess {
		i.carrierTaskBoard = sf.TaskBoard.Tasks
		i.carrierTaskBoardSession = sess
	}
	i.mu.Unlock()
	i.tasksDialog.Open(i.taskBoardRows)
	i.invalidate()
}
