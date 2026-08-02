package dialogs

import (
	"strings"

	"terva.sh/terva/packages/agent/tools/tasks"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// TasksDialog is the read-only /tasks panel over the built-in task board
// (the task_* tools' list). It holds no task data of its own: snapshotFn is
// pulled every render, so the panel live-updates while open — the daemon
// broadcasts surface_updated("taskboard") at turn end, which invalidates the
// cache snapshotFn reads. Display-only, so tasks stay agent-owned, matching the
// old terva-tasks extension panel it restores.
type TasksDialog struct {
	active     bool
	showDone   bool
	vp         Viewport
	snapshotFn func() []tasks.Task

	// MaxRows caps the body height; the overlay sets it from the terminal size
	// each frame so a long list stays inside the bottom band. 0 = unlimited.
	MaxRows int
}

// ChromeRows is the non-body rows Render emits: header, the blank, the footer
// and the closing rule.
func (d *TasksDialog) ChromeRows() int { return 4 }

func NewTasksDialog() *TasksDialog { return &TasksDialog{} }

func (d *TasksDialog) Active() bool { return d != nil && d.active }

// Open shows the panel over the given live task source, resetting the transient
// view state (collapsed done, scroll) so each open starts clean.
func (d *TasksDialog) Open(snapshotFn func() []tasks.Task) {
	d.active = true
	d.showDone = false
	d.vp.Reset()
	d.snapshotFn = snapshotFn
}

func (d *TasksDialog) Close() {
	d.active = false
	d.snapshotFn = nil
	d.vp.Reset()
}

// TasksAction reports what the host should do after a key. Close is the only
// outward effect; the done-toggle and scroll are internal view state.
type TasksAction struct{ Close bool }

func (d *TasksDialog) HandleKey(k tui.Key) TasksAction {
	switch k.Kind {
	case tui.KeyEsc:
		return TasksAction{Close: true}
	case tui.KeyRune:
		if k.Rune == 'd' || k.Rune == 'D' {
			d.showDone = !d.showDone
			d.vp.Reset()
		}
	default:
		d.vp.HandleKey(k)
	}
	return TasksAction{}
}

func (d *TasksDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var rows []tasks.Task
	if d.snapshotFn != nil {
		rows = d.snapshotFn()
	}
	out := []string{FrameHeader(th, tasks.PanelTitle(rows, ""), width)}

	// tasks.PanelLines is the single source of the layout (sort, done/cancelled
	// collapse, empty-state text); we only theme and window its output.
	body := tasks.PanelLines(rows, d.showDone)
	d.vp.Fit(len(body), d.MaxRows)
	scrollable := d.MaxRows > 0 && d.vp.Scrollable()
	visible := body
	if scrollable {
		start, end := d.vp.Window()
		visible = body[start:end]
	}
	for _, l := range visible {
		out = append(out, colorTaskLine(th, l))
	}

	// 'd' toggles completed tasks; name the action it will DO next, not "expand".
	var footer string
	switch {
	case d.showDone && scrollable:
		footer = i18n.T("d hide done · ↑/↓ scroll · esc close")
	case d.showDone:
		footer = i18n.T("d hide done · esc close")
	case scrollable:
		footer = i18n.T("d show done · ↑/↓ scroll · esc close")
	default:
		footer = i18n.T("d show done · esc close")
	}
	out = append(out, "", th.FG256(th.Muted, "  "+footer))
	out = append(out, FrameRule(th, width))
	return out
}

// colorTaskLine themes one PanelLines row by its leading status token so
// blocked work reads prominently and completed work recedes. It matches the
// row's own format (packages/agent/tools/tasks/render.go panelRow:
// "  <status> <label>") against the Status constants, keeping render.go the
// single source of the layout while the colour lives with the frontend.
func colorTaskLine(th tui.Theme, line string) string {
	switch trimmed := strings.TrimLeft(line, " "); {
	case strings.HasPrefix(trimmed, string(tasks.StatusBlocked)):
		return th.FG256(th.Warning, line)
	case strings.HasPrefix(trimmed, string(tasks.StatusActive)):
		return th.FG256(th.Accent, line)
	case strings.HasPrefix(trimmed, string(tasks.StatusPending)):
		return th.FG256(th.FG, line)
	default:
		// done (N) / cancelled (N) collapse lines and the empty-state text all
		// recede into the muted colour.
		return th.FG256(th.Muted, line)
	}
}
