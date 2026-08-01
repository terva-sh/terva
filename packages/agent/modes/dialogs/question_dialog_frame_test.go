package dialogs

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// The skip warning is the longest line this dialog has ever put up — at 64
// columns it ran 9 past the frame — and the hint lines it stands in for were
// never folded either, so they overflowed on any narrow terminal. Both are
// wrapped now.
//
// This asserts the RULE across every state and a spread of widths, rather
// than the length of the two lines that broke: the next thing added to a
// hint fails here instead of on someone's screen. It is the question
// dialog's copy of the memory pane's TestNoRenderedLineOverflowsTheFrame.
func TestQuestionDialogNoRenderedRowOverflowsTheFrame(t *testing.T) {
	th := tui.Theme{Muted: 8, Tool: 2, Accent: 4, Warning: 214, SelectionFG: 7, SelectionBG: 4}

	// Each state is a named recipe of keys applied to a freshly-armed dialog,
	// so every one is rendered at every width.
	states := []struct {
		name  string
		build func() *QuestionDialog
	}{
		{"choosing", func() *QuestionDialog {
			d := NewQuestionDialog()
			_ = ask1(d, core.UserQuestion{
				Question: "Which database should the new service use, and why that one?",
				Options:  []string{"Postgres — one more instance to run", "SQLite"}, AllowCustom: true,
			})
			return d
		}},
		{"skip armed", func() *QuestionDialog {
			d := NewQuestionDialog()
			_ = ask1(d, core.UserQuestion{Question: "Pick", Options: []string{"a", "b"}})
			d.HandleKey(tui.Key{Kind: tui.KeyEsc})
			return d
		}},
		{"skip armed, whole set", func() *QuestionDialog {
			d := NewQuestionDialog()
			_ = askN(d,
				core.UserQuestion{Question: "one?", Options: []string{"a"}},
				core.UserQuestion{Question: "two?", Options: []string{"b"}},
				core.UserQuestion{Question: "three?", Options: []string{"c"}})
			d.HandleKey(tui.Key{Kind: tui.KeyEsc})
			return d
		}},
		{"typing a custom answer", func() *QuestionDialog {
			d := NewQuestionDialog()
			_ = ask1(d, core.UserQuestion{
				Question: "Which?", Options: []string{"alpha"}, AllowCustom: true,
			})
			d.HandleKey(tui.Key{Kind: tui.KeyDown})
			d.HandleKey(tui.Key{Kind: tui.KeyEnter})
			for _, r := range "an answer long enough to wrap more than once at any width" {
				d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
			}
			return d
		}},
		{"free text, no options", func() *QuestionDialog {
			d := NewQuestionDialog()
			_ = ask1(d, core.UserQuestion{Question: "What should it be called?"})
			return d
		}},
		{"submit tab", func() *QuestionDialog {
			d := NewQuestionDialog()
			_ = askN(d,
				core.UserQuestion{Question: "one?", Options: []string{"a rather long answer to review"}},
				core.UserQuestion{Question: "two?", Options: []string{"b"}})
			d.HandleKey(tui.Key{Kind: tui.KeyTab})
			d.HandleKey(tui.Key{Kind: tui.KeyTab})
			return d
		}},
		{"submit tab, skip armed", func() *QuestionDialog {
			d := NewQuestionDialog()
			_ = askN(d,
				core.UserQuestion{Question: "one?", Options: []string{"a"}},
				core.UserQuestion{Question: "two?", Options: []string{"b"}})
			d.HandleKey(tui.Key{Kind: tui.KeyTab})
			d.HandleKey(tui.Key{Kind: tui.KeyTab})
			d.HandleKey(tui.Key{Kind: tui.KeyEsc})
			return d
		}},
	}

	for _, width := range []int{30, 40, 64, 80, 100} {
		for _, st := range states {
			for _, maxRows := range []int{0, 8, 20} {
				d := st.build()
				d.MaxRows = maxRows
				rows := d.Render(th, width)
				for i, r := range rows {
					plain := widgets.StripANSIBytes(r)
					if w := runewidth.StringWidth(plain); w > width {
						t.Errorf("%s @ width %d, MaxRows %d: row %d is %d columns:\n%q",
							st.name, width, maxRows, i, w, plain)
					}
				}
				// The body budget has to hold too — a wrapped hint used to be
				// counted as one row and quietly overran it.
				if body := len(rows) - 2; maxRows > 0 && body > maxRows {
					t.Errorf("%s @ width %d: body is %d rows, want <= %d:\n%s",
						st.name, width, body, maxRows, strings.Join(rows, "\n"))
				}
			}
		}
	}
}
