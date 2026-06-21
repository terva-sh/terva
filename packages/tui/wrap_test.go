package tui

import (
	"strings"
	"testing"
)

// TestWrapLineFirstContinuationHasIndent is a regression test for a
// bug where wrapLine()'s internal newLine() toggled the firstLine
// flag and THEN checked it, so the very first wrap continuation
// flushed without the cont indent. Any subsequent continuation
// (second wrap onwards) got the indent. That was visible as a
// misaligned second row and caused the editor's cursor to land in
// the wrong column after a multi-line paste (locateCursor assumes
// continuations carry cont, so when they didn't, the cursor drifted
// one-indent-worth to the right).
func TestWrapLineFirstContinuationHasIndent(t *testing.T) {
	s := "prefix this is a long line that will wrap around at forty cells"
	out := wrapLine(s, 40, "  ")

	if len(out) < 2 {
		t.Fatalf("want at least 2 wrapped rows, got %d: %v", len(out), out)
	}
	// Row 0 is the first line (no indent; it's the lead).
	// Every row from index 1 onward is a continuation and MUST start
	// with the cont prefix.
	for i := 1; i < len(out); i++ {
		if !strings.HasPrefix(out[i], "  ") {
			t.Errorf("row %d missing cont indent: %q", i, out[i])
		}
	}
}

// TestEditorCursorAfterMultilinePaste is the downstream test: the
// rendered editor cursor must land at the logical end of the paste,
// with its visual column equal to leadW + runewidth(last-line).
//
// Uses Insert directly to bypass the KeyPaste collapse path (which
// would turn this into a placeholder token); the test's concern is
// wrap / cursor math, not paste behaviour.
func TestEditorCursorAfterMultilinePaste(t *testing.T) {
	e := NewEditor("▌ ")
	e.Insert("aaa\nbbb\nccccc")

	// Logical end: last line "ccccc", cursor past its 5 runes.
	if e.CursorR != 2 || e.CursorC != 5 {
		t.Fatalf("logical cursor: want (2, 5), got (%d, %d)", e.CursorR, e.CursorC)
	}

	lines, row, col := e.Render(80)
	if len(lines) != 3 {
		t.Fatalf("want 3 rendered rows, got %d: %v", len(lines), lines)
	}
	// Row 0 "▌ aaa", row 1 "  bbb", row 2 "  ccccc".
	// Cursor lives at row 2; column = 2 (cont indent) + 5 = 7.
	if row != 2 {
		t.Errorf("visual row: want 2, got %d", row)
	}
	if col != 7 {
		t.Errorf("visual col: want 7, got %d", col)
	}
}

// TestEditorCursorAfterLongPasteWithWrap verifies the cursor lands
// correctly when a pasted line is long enough to wrap at the given
// render width. This is the scenario that was broken: before the
// fix, the first wrap continuation missed its cont indent, so the
// terminal cursor drifted when typed after pasting a wrapped path.
func TestEditorCursorAfterLongPasteWithWrap(t *testing.T) {
	e := NewEditor("▌ ")
	// Direct Insert bypasses the multi-line collapse path; the
	// concern here is wrap-column math, not the collapse logic.
	e.Insert("this is a very long line that should wrap\nshort")

	lines, row, col := e.Render(30)

	// Every continuation row (anything after row 0) must be
	// cont-indented so locateCursor's rune-counting stays honest.
	for i := 1; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "  ") {
			t.Errorf("continuation row %d missing indent: %q", i, lines[i])
		}
	}
	// Cursor should be at the end of "short" on the last rendered row.
	if row != len(lines)-1 {
		t.Errorf("visual row: want %d (last), got %d", len(lines)-1, row)
	}
	if col != 2+5 { // cont indent + len("short")
		t.Errorf("visual col: want 7, got %d", col)
	}
}

// TestWrapLongTokenKeepsPromptOnFirstLine guards against a bug where
// pasting a single very long path (no spaces within it) stranded the
// prompt on its own line and started the path on line 2 with the
// cont indent. That happened because wrapLine broke to a new line
// before checking whether the oncoming token would need rune-by-rune
// splitting anyway. Stranding the prompt offset locateCursor's
// rune walk and the terminal cursor drew in the wrong column after
// any typing past the paste.
func TestWrapLongTokenKeepsPromptOnFirstLine(t *testing.T) {
	// Long single token (a path) + prompt "▌ ". Width 40, the token
	// itself is way longer than (40 - cont).
	s := "▌ /var/folders/xq/hdh5qm6j66nbzd0sh3ljsxyc0000gn/T/TemporaryItems/verylongnamehere"
	out := wrapLine(s, 40, "  ")

	if len(out) < 2 {
		t.Fatalf("expected multiple wrapped rows, got %d: %v", len(out), out)
	}
	// Row 0 must contain BOTH the prompt and the start of the path,
	// not just the prompt alone. A stranded-prompt regression would
	// show up as a short first row and the path starting on row 1.
	if !strings.HasPrefix(out[0], "▌ /") {
		t.Errorf("row 0 should start with the prompt AND path content, got %q", out[0])
	}
	if visibleWidth(out[0]) < 20 {
		t.Errorf("row 0 width %d too small; prompt got stranded", visibleWidth(out[0]))
	}
}

// TestEditorCursorAfterLongPathPaste verifies the on-screen cursor
// lines up with the logical end of the buffer after a drag-dropped
// long path is pasted AND the user types additional characters.
// Screenshot scenario from 20:15 on 2026-04-19.
func TestEditorCursorAfterLongPathPaste(t *testing.T) {
	e := NewEditor("▌ ")
	path := "/very/long/path/that/exceeds/our/terminal/width/by/a/comfortable/margin/file.png"
	e.HandleKey(Key{Kind: KeyPaste, Paste: path})
	for _, r := range "hello" {
		e.HandleKey(Key{Kind: KeyRune, Rune: r})
	}
	lines, row, col := e.Render(40)

	// Cursor must be on the last rendered row.
	if row != len(lines)-1 {
		t.Errorf("visual row: want %d (last), got %d (lines: %v)", len(lines)-1, row, lines)
	}
	// And its column should equal the width of the last row (cursor
	// at end-of-line).
	if col != visibleWidth(lines[len(lines)-1]) {
		t.Errorf("visual col %d != last-row width %d", col, visibleWidth(lines[len(lines)-1]))
	}
}

// wrapANSILineKeepStyle keeps a single coloured span coloured on EVERY wrapped
// row, not just the first — each rendered row resets SGR independently (see
// paintBackgroundRow), so the carried style must be re-emitted per row.
func TestWrapANSILineKeepStyleCarriesColor(t *testing.T) {
	red := sgrFG(196)
	rows := wrapANSILineKeepStyle(red+"aaaa bbbb cccc dddd"+reset, 9)
	if len(rows) < 2 {
		t.Fatalf("expected wrapping, got %d rows: %v", len(rows), rows)
	}
	for i, r := range rows {
		if !strings.Contains(r, red) {
			t.Errorf("row %d lost the colour: %q", i, r)
		}
		if !strings.HasSuffix(r, reset) {
			t.Errorf("row %d should be reset-terminated: %q", i, r)
		}
	}
}

// When no styled span actually crosses a wrap boundary, keep-style must be
// byte-identical to plain wrapANSILine — that's what makes it a safe drop-in
// for the direct-emit callers (their golden tests stay green).
func TestWrapANSILineKeepStyleByteIdenticalWhenNoSpanWraps(t *testing.T) {
	red := sgrFG(196)
	join := func(ss []string) string { return strings.Join(ss, "\n") }
	cases := []struct {
		name, in string
		limit    int
	}{
		{"plain", "alpha beta gamma delta epsilon zeta", 11},
		{"balanced per-token", "foo " + red + "x" + reset + " barbar bazbaz quux", 8},
		{"short, no wrap", red + "hi" + reset, 40},
	}
	for _, c := range cases {
		got, want := join(wrapANSILineKeepStyle(c.in, c.limit)), join(wrapANSILine(c.in, c.limit))
		if got != want {
			t.Errorf("%s: keep-style diverged from plain wrap\n got %q\nwant %q", c.name, got, want)
		}
	}
}

func TestSgrStateAfter(t *testing.T) {
	red, blue := sgrFG(196), sgrFG(21)
	if got := sgrStateAfter("", red+"x"); got != red {
		t.Errorf("opening a colour: got %q want %q", got, red)
	}
	if got := sgrStateAfter(red, "y"+reset); got != "" {
		t.Errorf("a reset should clear the state, got %q", got)
	}
	if got := sgrStateAfter("", red+"a"+blue+"b"); got != red+blue {
		t.Errorf("colours accumulate until reset: got %q want %q", got, red+blue)
	}
	if got := sgrStateAfter("", "no escapes here"); got != "" {
		t.Errorf("plain text leaves no state, got %q", got)
	}
}
