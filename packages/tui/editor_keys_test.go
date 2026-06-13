package tui

// Keystroke-sequence tests for the Editor (TUI plan Phase 0.3). These
// drive HandleKey the way the interactive loop does — one Key at a
// time — and assert buffer + cursor state, so editing regressions
// surface at the behavior level instead of via manual testing.

import (
	"io"
	"strings"
	"testing"
)

func typeRunes(e *Editor, s string) {
	for _, r := range s {
		e.HandleKey(Key{Kind: KeyRune, Rune: r})
	}
}

func press(e *Editor, kind KeyKind, times int) {
	for range times {
		e.HandleKey(Key{Kind: kind})
	}
}

func TestEditorTypingAndSubmit(t *testing.T) {
	e := NewEditor("> ")
	typeRunes(e, "hello world")
	if e.Value() != "hello world" {
		t.Fatalf("Value = %q", e.Value())
	}
	if !e.HandleKey(Key{Kind: KeyEnter}) {
		t.Fatal("Enter should report submit")
	}
	if got := e.SubmitValue(); got != "hello world" {
		t.Fatalf("SubmitValue = %q", got)
	}
}

func TestEditorAltEnterSplitsLineAtCursor(t *testing.T) {
	e := NewEditor("> ")
	typeRunes(e, "abcdef")
	press(e, KeyLeft, 3)
	e.HandleKey(Key{Kind: KeyRune, Rune: '\r', Alt: true})
	if len(e.Lines) != 2 || e.Lines[0] != "abc" || e.Lines[1] != "def" {
		t.Fatalf("Lines = %q", e.Lines)
	}
	if e.CursorR != 1 || e.CursorC != 0 {
		t.Fatalf("cursor = (%d,%d), want (1,0)", e.CursorR, e.CursorC)
	}
	typeRunes(e, "X")
	if e.Value() != "abc\nXdef" {
		t.Fatalf("Value = %q", e.Value())
	}
}

func TestEditorAltEnterMidBuffer(t *testing.T) {
	// Split while later lines exist: the tail must shift down intact
	// (regression guard for the append+copy insert in newline()).
	e := NewEditor("> ")
	typeRunes(e, "one")
	e.HandleKey(Key{Kind: KeyRune, Rune: '\n', Alt: true})
	typeRunes(e, "three")
	// Cursor to the middle of line 0: up, then end, then left once.
	e.HandleKey(Key{Kind: KeyUp})
	e.HandleKey(Key{Kind: KeyEnd})
	press(e, KeyLeft, 1)
	e.HandleKey(Key{Kind: KeyRune, Rune: '\r', Alt: true})
	if got := e.Value(); got != "on\ne\nthree" {
		t.Fatalf("Value = %q, want %q", got, "on\ne\nthree")
	}
}

func TestEditorBackspaceJoinsLines(t *testing.T) {
	e := NewEditor("> ")
	e.SetValue("abc\ndef")
	e.CursorR, e.CursorC = 1, 0
	e.HandleKey(Key{Kind: KeyBackspace})
	if e.Value() != "abcdef" {
		t.Fatalf("Value = %q", e.Value())
	}
	if e.CursorR != 0 || e.CursorC != 3 {
		t.Fatalf("cursor = (%d,%d), want (0,3)", e.CursorR, e.CursorC)
	}
}

func TestEditorDeleteAtEOLJoinsNextLine(t *testing.T) {
	e := NewEditor("> ")
	e.SetValue("abc\ndef")
	e.CursorR, e.CursorC = 0, 3
	e.HandleKey(Key{Kind: KeyDelete})
	if e.Value() != "abcdef" {
		t.Fatalf("Value = %q", e.Value())
	}
}

func TestEditorKillLineHalves(t *testing.T) {
	e := NewEditor("> ")
	typeRunes(e, "hello world")
	press(e, KeyLeft, 5) // cursor after "hello "
	e.HandleKey(Key{Kind: KeyCtrlK})
	if e.Value() != "hello " {
		t.Fatalf("after Ctrl+K: Value = %q", e.Value())
	}
	e.HandleKey(Key{Kind: KeyCtrlU})
	if e.Value() != "" {
		t.Fatalf("after Ctrl+U: Value = %q", e.Value())
	}
}

func TestEditorWordDeleteAndMovement(t *testing.T) {
	e := NewEditor("> ")
	typeRunes(e, "foo bar baz")
	e.HandleKey(Key{Kind: KeyCtrlW})
	if e.Value() != "foo bar " {
		t.Fatalf("after Ctrl+W: Value = %q", e.Value())
	}
	e.HandleKey(Key{Kind: KeyLeft, Alt: true}) // word left → start of "bar"
	if e.CursorC != 4 {
		t.Fatalf("after Alt+Left: CursorC = %d, want 4", e.CursorC)
	}
	// Word-right skips the word plus trailing separators, landing at
	// the start of the next word (here: end of line).
	e.HandleKey(Key{Kind: KeyRight, Alt: true})
	if e.CursorC != 8 {
		t.Fatalf("after Alt+Right: CursorC = %d, want 8", e.CursorC)
	}
	e.HandleKey(Key{Kind: KeyBackspace, Alt: true}) // delete "bar "
	if e.Value() != "foo " {
		t.Fatalf("after Alt+Backspace: Value = %q", e.Value())
	}
}

func TestEditorHomeEndKeys(t *testing.T) {
	e := NewEditor("> ")
	typeRunes(e, "abcdef")
	e.HandleKey(Key{Kind: KeyCtrlA})
	if e.CursorC != 0 {
		t.Fatalf("Ctrl+A: CursorC = %d", e.CursorC)
	}
	e.HandleKey(Key{Kind: KeyCtrlE})
	if e.CursorC != 6 {
		t.Fatalf("Ctrl+E: CursorC = %d", e.CursorC)
	}
}

func TestEditorEscClearsBuffer(t *testing.T) {
	e := NewEditor("> ")
	typeRunes(e, "draft text")
	e.HandleKey(Key{Kind: KeyEsc})
	if !e.IsEmpty() {
		t.Fatalf("after Esc: Value = %q", e.Value())
	}
}

func TestEditorCJKCursorIsRuneBasedButRendersWide(t *testing.T) {
	e := NewEditor("> ")
	typeRunes(e, "日本語ab")
	if e.CursorC != 5 {
		t.Fatalf("CursorC = %d, want 5 runes", e.CursorC)
	}
	press(e, KeyLeft, 2) // back over "ab"
	if e.CursorC != 3 {
		t.Fatalf("CursorC = %d, want 3", e.CursorC)
	}
	// Visual column counts cells: prompt (2) + three double-width
	// runes (6) = 8.
	_, row, col := e.Render(80)
	if row != 0 || col != 8 {
		t.Fatalf("Render cursor = (%d,%d), want (0,8)", row, col)
	}
}

func TestEditorVerticalMoveWithinWrappedLine(t *testing.T) {
	e := NewEditor("> ")
	typeRunes(e, "aaaa bbbb cccc dddd")
	lines, row, _ := e.Render(12) // forces soft wrap
	if len(lines) < 2 {
		t.Fatalf("expected wrap at width 12, got %d row(s): %q", len(lines), lines)
	}
	if row != len(lines)-1 {
		t.Fatalf("cursor should start on last visual row %d, got %d", len(lines)-1, row)
	}
	e.HandleKey(Key{Kind: KeyUp})
	_, rowAfter, _ := e.Render(12)
	if rowAfter != row-1 {
		t.Fatalf("Up moved visual row %d → %d, want %d", row, rowAfter, row-1)
	}
	if e.CursorR != 0 {
		t.Fatal("Up within a wrapped line must stay in the same logical line")
	}
}

func TestEditorPasteCollapseRoundTrip(t *testing.T) {
	e := NewEditor("> ")
	typeRunes(e, "context: ")
	body := strings.TrimSuffix(strings.Repeat("line\n", 12), "\n")
	e.HandleKey(Key{Kind: KeyPaste, Paste: body})
	if got := e.Value(); got != "context: [pasted text #1 +12 lines]" {
		t.Fatalf("visible Value = %q", got)
	}
	typeRunes(e, " done")
	want := "context: " + body + " done"
	if got := e.SubmitValue(); got != want {
		t.Fatalf("SubmitValue = %q, want %q", got, want)
	}
	// Clear must drop stored pastes so ids never leak across turns.
	e.Clear()
	e.HandleKey(Key{Kind: KeyPaste, Paste: body})
	if got := e.Value(); got != "[pasted text #1 +12 lines]" {
		t.Fatalf("after Clear, visible Value = %q", got)
	}
}

func TestEditorSmallPasteInsertsVerbatim(t *testing.T) {
	e := NewEditor("> ")
	e.HandleKey(Key{Kind: KeyPaste, Paste: "just words"})
	if e.Value() != "just words" {
		t.Fatalf("Value = %q", e.Value())
	}
	// CRLF content is normalized so \r never reaches the buffer.
	e.Clear()
	e.HandleKey(Key{Kind: KeyPaste, Paste: "a\r\nb"})
	if e.Value() != "a\nb" {
		t.Fatalf("Value = %q", e.Value())
	}
}

// TestEditorDrivenThroughReader covers the wire path: raw terminal
// bytes → Reader escape parsing → Keys → editor state, the same
// pipeline the interactive loop runs.
func TestEditorDrivenThroughReader(t *testing.T) {
	input := []byte("ab\x1b[D\x1b[Dc" + // type "ab", Left ×2, type "c"
		"\x1b[200~pasted bit\x1b[201~") // bracketed paste
	pos := 0
	read := func() (byte, error) {
		if pos >= len(input) {
			return 0, io.EOF
		}
		b := input[pos]
		pos++
		return b, nil
	}
	r := NewReader(read)
	e := NewEditor("> ")
	for {
		k, err := r.Read()
		if err != nil {
			break
		}
		e.HandleKey(k)
	}
	if got := e.Value(); got != "cpasted bitab" {
		t.Fatalf("Value = %q, want %q", got, "cpasted bitab")
	}
}
