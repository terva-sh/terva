package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
	"terva.sh/terva/packages/tui"
	"terva.sh/terva/packages/tui/tuitest"
)

// The caret must land on the row the editor drew, whatever else the bottom
// band is showing.
//
// The regression: the band was assembled in one place and the caret row
// re-derived from the same pieces in another. When the draft stash added its
// rows to the band, the arithmetic did not learn about them, so the caret drew
// two rows high — up on the status bar — while typing still edited the line
// below it. Only the row was wrong, which is why the column tracked perfectly
// and nothing else looked broken.
//
// Asserting "cursor row == the row the draft is on" is the property that
// matters and the one no per-section term can satisfy by accident: add a
// section to the band and forget the caret, and this fails.
func TestRedrawPutsCaretOnTheEditorRow(t *testing.T) {
	const draft = "the caret belongs on this row"

	for _, tc := range []struct {
		name string
		arm  func(*Interactive)
	}{
		{"nothing parked", func(*Interactive) {}},
		// Two rows: a blank and the nudge.
		{"stash hint armed", func(i *Interactive) { i.stashHintArmed = true }},
		// Three rows: a blank, the "set aside:" chip, and its recovery hint.
		{"draft parked", func(i *Interactive) {
			parked := tui.NewEditor("")
			parked.SetValue("a draft that was set aside earlier")
			i.stash = &draftStash{ed: parked.State()}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TERM_PROGRAM", "")
			term := tuitest.NewFakeTerm(80, 24)
			noImages := false
			// Run() is deliberately NOT started: redraw is called directly on
			// the test's own goroutine, so the state below is set without
			// racing a live paint loop.
			i := NewInteractive(InteractiveConfig{
				Terminal:            term,
				Theme:               tui.Dark,
				Model:               "test-model",
				Provider:            "test",
				CWD:                 testsupport.TempDir(t),
				TervaHome:           testsupport.TempDir(t),
				Version:             "v0.0.0-test",
				InlineImagesEnabled: &noImages,
			})
			// Run() normally does this off the terminal's size; the
			// renderer draws nothing until it knows how big it is.
			cols, rows := term.Size()
			i.rend.Resize(cols, rows)
			i.ed.SetValue(draft)
			tc.arm(i)

			i.redraw()

			_, cursorRow := term.Screen().Cursor()
			screen := strings.Split(term.Screen().Text(), "\n")
			editorRow := -1
			for n, line := range screen {
				if strings.Contains(line, draft) {
					editorRow = n
					break
				}
			}
			if editorRow < 0 {
				t.Fatalf("the draft never reached the screen:\n%s", term.Screen().Text())
			}
			if cursorRow != editorRow {
				t.Errorf("caret on row %d, editor on row %d (off by %d)\n%s",
					cursorRow, editorRow, editorRow-cursorRow, term.Screen().Text())
			}
		})
	}
}
