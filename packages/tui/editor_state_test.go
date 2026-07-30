package tui

import (
	"strings"
	"testing"
)

// State/Restore must round-trip the one thing SetValue cannot: the
// hidden bodies behind paste placeholders. A draft parked via State and
// brought back with Restore still expands its [pasted text #N] token at
// submit time.
func TestEditorStateRoundTripsPastePlaceholders(t *testing.T) {
	e := NewEditor("> ")
	e.Insert("see: ")
	// 12 lines — past pasteCollapseLineThreshold, so it collapses.
	body := strings.Repeat("a line of pasted text\n", 11) + "the last line"
	e.HandleKey(Key{Kind: KeyPaste, Paste: body})

	if !strings.Contains(e.Value(), "[pasted text #1") {
		t.Fatalf("setup: expected a collapsed placeholder, got %q", e.Value())
	}
	saved := e.State()

	// Displace the draft entirely — the interposed answer.
	e.Clear()
	e.Insert("the answer")
	if got := e.SubmitValue(); got != "the answer" {
		t.Fatalf("interposed submit = %q, want %q", got, "the answer")
	}

	e.Restore(saved)
	if got := e.Value(); !strings.Contains(got, "[pasted text #1") {
		t.Fatalf("restored visible value = %q, want the placeholder back", got)
	}
	if got := e.SubmitValue(); !strings.Contains(got, body) {
		t.Fatalf("restored SubmitValue = %q, want the paste body expanded", got)
	}
}

// The snapshot restores the cursor where the user left it, not at the
// end of the buffer the way SetValue does.
func TestEditorStateRestoresCursor(t *testing.T) {
	e := NewEditor("> ")
	e.Insert("alpha\nbeta")
	e.CursorR, e.CursorC = 0, 2 // mid-word on the first line

	saved := e.State()
	e.Clear()
	e.Insert("something else")
	e.Restore(saved)

	if e.CursorR != 0 || e.CursorC != 2 {
		t.Fatalf("cursor = (%d,%d), want (0,2)", e.CursorR, e.CursorC)
	}
	if got := e.Value(); got != "alpha\nbeta" {
		t.Fatalf("value = %q, want %q", got, "alpha\nbeta")
	}
}

// A snapshot is a copy, not a view: editing after State() must not
// bleed into the saved draft.
func TestEditorStateIsACopy(t *testing.T) {
	e := NewEditor("> ")
	e.Insert("original")
	saved := e.State()

	e.Insert(" plus edits")
	if got := saved.Value(); got != "original" {
		t.Fatalf("snapshot mutated by later edits: %q", got)
	}
}

// Ctrl+S must arrive as its own chord — raw mode clears IXON, so 0x13
// is an ordinary byte, and the parser may not swallow it as KeyUnknown.
func TestReaderParsesCtrlS(t *testing.T) {
	r := NewReader(func() (byte, error) { return 0x13, nil })
	k, err := r.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if k.Kind != KeyCtrlS {
		t.Fatalf("kind = %v, want KeyCtrlS", k.Kind)
	}
}
