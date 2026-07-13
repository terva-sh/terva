package workspace

import (
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tools/tasks/tasktool"
)

// The task board is the per-session built-in task_* list (distinct from the
// workspace swarm dashboard in workspace_tasks.go, which shares the "Tasks"
// name). It is surfaced by explicit id ("taskboard") to the TUI's /tasks panel
// and status glance; unlike the swarm pane it is NOT enumerated in surfaceList,
// so it adds no web tab (a future web renderer is a one-entry follow-up). The
// board mutates only inside a turn, so no poller is needed — buildSession
// broadcasts surface_updated("taskboard") once per turn end.

// hasTaskBoard reports whether this session owns a task board (present whenever
// the session was built with base workspace tools; nil for chat/play/--no-tools).
func (s *wsSession) hasTaskBoard() bool { return s != nil && s.tasks != nil }

// taskBoardView maps the controller's live task store to the wire view. A nil
// controller (no board) yields an empty view so the pane renders "no tasks"
// rather than erroring.
func taskBoardView(ctrl *tasktool.Controller) *ctrlproto.TaskBoardView {
	if ctrl == nil {
		return &ctrlproto.TaskBoardView{}
	}
	list := ctrl.Store().List()
	items := make([]ctrlproto.TaskBoardItem, 0, len(list))
	for _, t := range list {
		items = append(items, ctrlproto.TaskBoardItem{
			ID:         t.ID,
			Status:     string(t.Status),
			Title:      t.Title,
			ActiveForm: t.ActiveForm,
			Evidence:   t.Evidence,
			Note:       t.Note,
		})
	}
	return &ctrlproto.TaskBoardView{Tasks: items}
}
