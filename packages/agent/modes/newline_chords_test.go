package modes

// The newline chords that survive a terminal reporting no modified Enter
// at all, driven through the real key loop as the raw bytes a terminal
// actually sends.

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
	"terva.sh/terva/packages/tui/tuitest"
)

// Ctrl+J is the newline chord that needs no terminal support. Driven as
// the raw 0x0a byte a terminal actually sends.
func TestInteractiveCtrlJInsertsNewline(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()

	h.term.Type("above\nbelow")
	h.waitScreen("both halves on separate rows", func(s *tuitest.Screen) bool {
		var aboveRow, belowRow = -1, -1
		for y, row := range s.Rows() {
			if strings.Contains(row, "above") {
				aboveRow = y
			}
			if strings.Contains(row, "below") {
				belowRow = y
			}
		}
		return aboveRow >= 0 && belowRow == aboveRow+1
	})
}

// A trailing backslash turns Enter into a newline and is consumed, for
// terminals that report neither a modified Enter nor anything else.
func TestInteractiveBackslashEnterInsertsNewline(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()

	h.term.Type("above\\\rbelow")
	h.waitScreen("both halves on separate rows, no backslash", func(s *tuitest.Screen) bool {
		var aboveRow, belowRow = -1, -1
		for y, row := range s.Rows() {
			if strings.Contains(row, "above") {
				if strings.Contains(row, `above\`) {
					return false
				}
				aboveRow = y
			}
			if strings.Contains(row, "below") {
				belowRow = y
			}
		}
		return aboveRow >= 0 && belowRow == aboveRow+1
	})
}

// The keymap's modified-Enter bindings exist so the chord beats the slash
// popup, which otherwise claims every KeyEnter to commit its selection.
func TestModifiedEnterBeatsSlashPopup(t *testing.T) {
	for _, k := range []tui.Key{
		{Kind: tui.KeyEnter, Shift: true},
		{Kind: tui.KeyEnter, Alt: true},
		{Kind: tui.KeyEnter, Ctrl: true},
	} {
		i := &Interactive{ed: tui.NewEditor("> ")}
		i.keymap = i.buildGlobalKeymap()
		handled, _ := i.dispatchGlobalKey(t.Context(), k)
		if !handled {
			t.Fatalf("%+v was not claimed by the keymap", k)
		}
		if got := i.ed.Value(); got != "\n" {
			t.Fatalf("%+v produced %q, want a newline", k, got)
		}
	}
}
