package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/agent/tools/tasks"
	"terva.sh/terva/packages/tui"
)

func tasksFixture() []tasks.Task {
	return []tasks.Task{
		{ID: "task-1", Title: "Wire the panel", ActiveForm: "Wiring the panel", Status: tasks.StatusActive},
		{ID: "task-2", Title: "Add the segment", Status: tasks.StatusPending},
		{ID: "task-3", Title: "Web review", Status: tasks.StatusBlocked, Evidence: "needs the wire type"},
		{ID: "task-4", Title: "Ship the wire type", Status: tasks.StatusDone},
	}
}

// The panel headers with the active task, lists open work, and collapses
// completed tasks to a count until 'd' expands them.
func TestTasksDialogRenderAndExpand(t *testing.T) {
	th := tui.Dark
	d := NewTasksDialog()
	rows := tasksFixture()
	d.Open(func() []tasks.Task { return rows })

	body := stripANSILines(d.Render(th, 60))
	joined := strings.Join(body, "\n")
	if !strings.Contains(body[0], "Wiring the panel") {
		t.Errorf("header should surface the active task, got %q", body[0])
	}
	for _, want := range []string{"Wiring the panel", "Add the segment", "Web review", "done (1)"} {
		if !strings.Contains(joined, want) {
			t.Errorf("panel body missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "Ship the wire type") {
		t.Errorf("completed task should be collapsed, not listed:\n%s", joined)
	}

	// 'd' expands completed tasks.
	if act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'd'}); act.Close {
		t.Fatal("'d' should not close the panel")
	}
	joined = strings.Join(stripANSILines(d.Render(th, 60)), "\n")
	if !strings.Contains(joined, "Ship the wire type") {
		t.Errorf("expanded panel should list completed tasks:\n%s", joined)
	}

	// esc closes.
	if act := d.HandleKey(tui.Key{Kind: tui.KeyEsc}); !act.Close {
		t.Fatal("esc should request close")
	}
}

// An empty board renders the friendly placeholder rather than a bare frame.
func TestTasksDialogEmpty(t *testing.T) {
	d := NewTasksDialog()
	d.Open(func() []tasks.Task { return nil })
	joined := strings.Join(stripANSILines(d.Render(tui.Dark, 60)), "\n")
	if !strings.Contains(joined, "No tasks yet") {
		t.Errorf("empty board should show the placeholder:\n%s", joined)
	}
}

func stripANSILines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = widgets.StripANSIBytes(l)
	}
	return out
}
