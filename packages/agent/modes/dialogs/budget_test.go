package dialogs

import (
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/tools/tasks"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

func budgetTheme() tui.Theme {
	return tui.Theme{Muted: 8, Accent: 4, Warning: 3, Error: 1, MeterLow: 2, MeterMid: 3, MeterHigh: 1}
}

func lines(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("row %d", i)
	}
	return out
}

// sized is one dialog under test: set its budget, render it, and report how tall
// it came out. Every case is given far more content than fits, so the scroll
// indicators are showing — the state where chrome is at its maximum and the one
// a naive measurement misses.
type sized struct {
	name string
	// render sizes the dialog by its OWN budget, draws it, and reports the rows
	// it drew alongside the chrome it declared. Both come from the dialog, so
	// the table never restates a number the dialog is responsible for.
	render func(th tui.Theme, termRows, width int) (rendered, chrome int)
}

func budgetCases() []sized {
	return []sized{
		{"changelog", func(th tui.Theme, termRows, w int) (int, int) {
			d := NewChangelogDialog()
			d.Open("v0.130.1", "https://example.invalid/notes", strings.Repeat("a release note\n", 200))
			d.MaxRows = BodyBudget(termRows, d.ChromeRows())
			for range 3 {
				d.HandleKey(tui.Key{Kind: tui.KeyPageDown}) // scroll in, so BOTH markers show
			}
			return len(d.Render(th, w)), d.ChromeRows()
		}},
		{"context", func(th tui.Theme, termRows, w int) (int, int) {
			d := NewContextDialog()
			d.Open("20260801-abcdef", "/very/long/path/to/a/session/transcript.jsonl", lines(400), lines(400))
			d.MaxRows = BodyBudget(termRows, d.ChromeRows())
			for range 3 {
				d.HandleKey(tui.Key{Kind: tui.KeyPageDown})
			}
			return len(d.Render(th, w)), d.ChromeRows()
		}},
		{"log", func(th tui.Theme, termRows, w int) (int, int) {
			d := NewLogDialog()
			d.Open("build log", lines(400))
			d.MaxRows = BodyBudget(termRows, d.ChromeRows())
			d.HandleKey(tui.Key{Kind: tui.KeyHome})
			for range 3 {
				d.HandleKey(tui.Key{Kind: tui.KeyPageDown})
			}
			return len(d.Render(th, w)), d.ChromeRows()
		}},
		{"permissions", func(th tui.Theme, termRows, w int) (int, int) {
			d := NewPermissionsDialog()
			grants := make([]PermGrant, 200)
			d.Open(lines(3), grants)
			d.MaxRows = BodyBudget(termRows, d.ChromeRows())
			return len(d.Render(th, w)), d.ChromeRows()
		}},
		{"skills/list", func(th tui.Theme, termRows, w int) (int, int) {
			d := NewSkillsDialog()
			d.Open(nil)
			d.MaxRows = BodyBudget(termRows, d.ChromeRows())
			return len(d.Render(th, w)), d.ChromeRows()
		}},
		{"settings", func(th tui.Theme, termRows, w int) (int, int) {
			d := NewSettingsDialog()
			items := make([]SettingsItem, 200)
			for i := range items {
				items[i] = SettingsItem{Label: fmt.Sprintf("option %d", i), Desc: "what this option does"}
			}
			d.Open(items)
			d.MaxRows = BodyBudget(termRows, d.ChromeRows())
			for range 3 {
				d.HandleKey(tui.Key{Kind: tui.KeyDown})
			}
			return len(d.Render(th, w)), d.ChromeRows()
		}},
		{"tasks", func(th tui.Theme, termRows, w int) (int, int) {
			d := NewTasksDialog()
			d.Open(func() []tasks.Task {
				out := make([]tasks.Task, 200)
				for i := range out {
					out[i] = tasks.Task{Title: fmt.Sprintf("task %d", i), Status: "pending"}
				}
				return out
			})
			d.MaxRows = BodyBudget(termRows, d.ChromeRows())
			return len(d.Render(th, w)), d.ChromeRows()
		}},
		{"memory", func(th tui.Theme, termRows, w int) (int, int) {
			d := NewMemoryDialog()
			d.Open([]MemoryScopeInfo{{}}, make([]MemoryRow, 300))
			d.MaxRows = BodyBudget(termRows, d.ChromeRows())
			d.cursor = 150 // mid-list: both scroll indicators showing
			return len(d.Render(th, w)), d.ChromeRows()
		}},
		{"extensions", func(th tui.Theme, termRows, w int) (int, int) {
			d := NewExtensionsDialog()
			d.Open(make([]ExtInfo, 300))
			d.MaxRows = BodyBudget(termRows, d.ChromeRows())
			d.cursor = 150
			return len(d.Render(th, w)), d.ChromeRows()
		}},
		{"mcp", func(th tui.Theme, termRows, w int) (int, int) {
			d := NewMCPDialog()
			d.Open(make([]MCPInfo, 300))
			d.MaxRows = BodyBudget(termRows, d.ChromeRows())
			d.cursor = 150
			return len(d.Render(th, w)), d.ChromeRows()
		}},
		{"jump", func(th tui.Theme, termRows, w int) (int, int) {
			d := NewJumpDialog()
			msgs := make([]provider.Message, 300)
			for i := range msgs {
				msgs[i] = provider.Message{Role: provider.RoleUser,
					Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf("message %d", i)}}}
			}
			d.Open(msgs, "")
			d.MaxRows = BodyBudget(termRows, d.ChromeRows())
			d.cursor = 150
			return len(d.Render(th, w)), d.ChromeRows()
		}},
	}
}

// The property that matters: a dialog sized by its OWN budget must leave the
// transcript alive.
//
// The host sizes the chat pane by subtraction — `chatRows = termRows -
// len(bottom)`, floored at 1 — so an oversized dialog is not clipped. It
// squeezes the conversation, and past the floor it pushes its own header off the
// top of the screen. Measured before this existed: changelog drew 23 rows,
// context 22, log 20, against the ~18 an 80x24 terminal can spare.
//
// This is also what checks every ChromeRows(): understate the chrome and the
// render overruns the budget derived from it, right here. A first draft of
// SettingsDialog.ChromeRows said 4, measured from a fixture showing only one
// scroll indicator; the real worst case is 5, and this is the shape of test that
// catches that rather than a measurement taken once by hand.
func TestEveryDialogFitsItsOwnBudget(t *testing.T) {
	th := budgetTheme()
	cases := budgetCases()
	if len(cases) < 11 {
		t.Fatalf("only %d dialogs under test; the guard is shrinking", len(cases))
	}
	for _, termRows := range []int{80, 40, 30, 24, 20, 16} {
		for _, c := range cases {
			got, chrome := c.render(th, termRows, 80)
			room := termRows - chatBandRows - minChatRows
			// Below a certain height nothing fits, and minBodyRows says the
			// dialog wins and the transcript gives way — the person opened the
			// dialog on purpose. Assert the fit only where a fit was possible;
			// asserting it everywhere would assert against the design.
			if BodyBudget(termRows, chrome) <= minBodyRows {
				continue
			}
			if got > room {
				t.Errorf("%s at %d rows: rendered %d rows, but only %d are spare "+
					"(%d reserved for the editor band, %d for the transcript). "+
					"ChromeRows() is understated or Render grew a row.",
					c.name, termRows, got, room, chatBandRows, minChatRows)
			}
		}
	}
}

// On a terminal too small to satisfy everyone, the dialog wins its floor and the
// transcript gives way — the person opened the dialog on purpose. Pinned so the
// floor is a decision rather than an accident of arithmetic.
func TestATinyTerminalKeepsTheDialogUsable(t *testing.T) {
	if got := BodyBudget(8, 6); got != minBodyRows {
		t.Errorf("BodyBudget(8, 6) = %d, want the floor %d", got, minBodyRows)
	}
	if got := BodyBudget(0, 0); got != minBodyRows {
		t.Errorf("BodyBudget(0, 0) = %d, want the floor %d", got, minBodyRows)
	}
	// And it never goes negative, whatever chrome claims.
	if got := BodyBudget(24, 500); got != minBodyRows {
		t.Errorf("BodyBudget(24, 500) = %d, want the floor %d", got, minBodyRows)
	}
}

// The budget must grow with the terminal, or a tall window buys nothing.
func TestTheBudgetTracksTheTerminal(t *testing.T) {
	small, large := BodyBudget(24, 4), BodyBudget(60, 4)
	if large-small != 36 {
		t.Errorf("36 more terminal rows produced %d more body rows, want 36", large-small)
	}
}
