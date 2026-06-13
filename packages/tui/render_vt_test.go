package tui_test

// VT-emulator golden tests for the Renderer (roadmap A3 / TUI plan
// Phase 0.1). The Renderer's main-screen flow mode tracks the hardware
// cursor and viewport open-loop — it never reads terminal state back —
// so these tests replay its byte output through a real terminal
// emulator (vt10x) and assert the final cell grid. A drift between the
// renderer's bookkeeping and actual terminal semantics shows up here
// as a wrong grid, the same way it would show up on screen.

import (
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
	"terva.sh/terva/packages/tui/tuitest"
)

// newVTRenderer returns a renderer wired to a fake terminal of the
// given size, with TERM_PROGRAM pinned so host-environment detection
// (the VS Code keepScrollback quirk) doesn't leak into tests.
func newVTRenderer(t *testing.T, cols, rows int, termProgram string) (*tuitest.FakeTerm, *tui.Renderer) {
	t.Helper()
	t.Setenv("TERM_PROGRAM", termProgram)
	ft := tuitest.NewFakeTerm(cols, rows)
	r := tui.NewRenderer(ft)
	r.Resize(cols, rows)
	return ft, r
}

// assertGrid compares the emulated screen against expected rows
// (top-aligned; missing trailing rows are expected blank).
func assertGrid(t *testing.T, s *tuitest.Screen, want []string) {
	t.Helper()
	got := s.Rows()
	for y := range got {
		w := ""
		if y < len(want) {
			w = w_trim(want[y])
		}
		if got[y] != w {
			t.Fatalf("screen row %d:\n got: %q\nwant: %q\nfull grid:\n%s", y, got[y], w, s.Text())
		}
	}
}

func w_trim(s string) string { return strings.TrimRight(s, " ") }

func TestDrawLogBasicFrame(t *testing.T) {
	ft, r := newVTRenderer(t, 40, 10, "")
	chat := []string{"hello", "world"}
	bottom := []string{"-- status --", "> "}
	r.DrawLog(chat, bottom, 1, 2)

	assertGrid(t, ft.Screen(), []string{
		"hello",
		"world",
		"-- status --",
		"> ",
		"", // renderer-owned bottom margin row
	})
	x, y := ft.Screen().Cursor()
	if x != 2 || y != 3 {
		t.Fatalf("cursor = (%d,%d), want (2,3)", x, y)
	}
	if !ft.Screen().CursorVisible() {
		t.Fatal("cursor should be visible")
	}
}

func TestDrawLogHidesCursorWhenAsked(t *testing.T) {
	ft, r := newVTRenderer(t, 40, 10, "")
	r.DrawLog([]string{"hi"}, []string{"> "}, -1, 0)
	if ft.Screen().CursorVisible() {
		t.Fatal("cursor should be hidden for cursorBottomRow=-1")
	}
}

func TestDrawLogIdleFrameEmitsNothing(t *testing.T) {
	ft, r := newVTRenderer(t, 40, 10, "")
	chat := []string{"hello"}
	bottom := []string{"-- status --", "> "}
	r.DrawLog(chat, bottom, 1, 2)
	before := len(ft.Output())
	r.DrawLog(chat, bottom, 1, 2)
	if after := len(ft.Output()); after != before {
		t.Fatalf("idle DrawLog wrote %d bytes; the no-op fast path should write none", after-before)
	}
}

func TestDrawLogBottomBandEdit(t *testing.T) {
	ft, r := newVTRenderer(t, 40, 10, "")
	chat := []string{"transcript line"}
	r.DrawLog(chat, []string{"-- status --", "> "}, 1, 2)
	// Simulate typing: only the editor row changes, cursor advances.
	r.DrawLog(chat, []string{"-- status --", "> x"}, 1, 3)

	assertGrid(t, ft.Screen(), []string{
		"transcript line",
		"-- status --",
		"> x",
	})
	x, y := ft.Screen().Cursor()
	if x != 3 || y != 2 {
		t.Fatalf("cursor = (%d,%d), want (3,2)", x, y)
	}
}

// TestDrawLogStreamingGrowth grows the chat one line per frame well
// past the viewport height, exercising the incremental scroll path
// (moveToLogicalRow + viewport tracking) rather than full repaints.
// The emulator must end up showing exactly the tail of the logical
// buffer.
func TestDrawLogStreamingGrowth(t *testing.T) {
	const cols, rows, total = 40, 10, 30
	ft, r := newVTRenderer(t, cols, rows, "")
	bottom := []string{"-- status --", "> "}

	var chat []string
	for i := range total {
		chat = append(chat, fmt.Sprintf("chat line %02d", i))
		r.DrawLog(chat, bottom, 1, 2)
	}

	// Logical buffer: 30 chat + 2 bottom + 1 margin = 33 rows on a
	// 10-row screen; the last 10 must be visible.
	logical := append(append([]string{}, chat...), bottom...)
	logical = append(logical, "")
	want := logical[len(logical)-rows:]
	assertGrid(t, ft.Screen(), want)

	// Cursor sits on the editor row: second-to-last visible row
	// (margin row below it), column 2.
	x, y := ft.Screen().Cursor()
	if x != 2 || y != rows-2 {
		t.Fatalf("cursor = (%d,%d), want (2,%d)", x, y, rows-2)
	}
}

// TestDrawLogBottomBandShrink closes a "dialog": the bottom band drops
// from six rows to two, and the now-dead rows must be cleared in
// place, not left as ghost chrome.
func TestDrawLogBottomBandShrink(t *testing.T) {
	ft, r := newVTRenderer(t, 40, 12, "")
	chat := []string{"one", "two"}
	dialog := []string{"-- dialog --", "option a", "option b", "option c", "-- status --", "> "}
	r.DrawLog(chat, dialog, 5, 2)
	r.DrawLog(chat, []string{"-- status --", "> "}, 1, 2)

	assertGrid(t, ft.Screen(), []string{
		"one",
		"two",
		"-- status --",
		"> ",
	})
}

// TestDrawLogLongLineTruncated proves the renderer's hard truncation
// keeps over-width lines from soft-wrapping in the terminal (which
// would push every row below it out of position).
func TestDrawLogLongLineTruncated(t *testing.T) {
	const cols = 20
	ft, r := newVTRenderer(t, cols, 8, "")
	long := strings.Repeat("x", 50)
	r.DrawLog([]string{long}, []string{"> "}, 0, 2)

	assertGrid(t, ft.Screen(), []string{
		strings.Repeat("x", cols),
		"> ",
	})
}

func TestDrawLogResizeRepaintsClean(t *testing.T) {
	ft, r := newVTRenderer(t, 40, 10, "")
	chat := []string{strings.Repeat("a", 38), strings.Repeat("b", 38)}
	bottom := []string{"-- status --", "> "}
	r.DrawLog(chat, bottom, 1, 2)

	// Shrink the terminal; renderer must clear and repaint without
	// stale wide-layout garbage past the new right edge.
	ft.Resize(30, 8)
	r.Resize(30, 8)
	chatNarrow := []string{strings.Repeat("a", 28), strings.Repeat("b", 28)}
	r.DrawLog(chatNarrow, bottom, 1, 2)

	assertGrid(t, ft.Screen(), []string{
		strings.Repeat("a", 28),
		strings.Repeat("b", 28),
		"-- status --",
		"> ",
	})
}

// TestTeardownLog is the exit path: the bottom band is erased, the
// transcript stays, and the cursor parks on the row right after the
// chat — where the shell prompt will appear.
func TestTeardownLog(t *testing.T) {
	ft, r := newVTRenderer(t, 40, 10, "")
	chat := []string{"keep me 1", "keep me 2", "keep me 3"}
	r.DrawLog(chat, []string{"-- status --", "> "}, 1, 2)
	r.TeardownLog()

	assertGrid(t, ft.Screen(), []string{
		"keep me 1",
		"keep me 2",
		"keep me 3",
	})
	x, y := ft.Screen().Cursor()
	if x != 0 || y != 3 {
		t.Fatalf("cursor parked at (%d,%d), want (0,3)", x, y)
	}
	if !ft.Screen().CursorVisible() {
		t.Fatal("cursor should be visible after teardown")
	}
}

// TestTeardownLogAfterScrolledTranscript reproduces the harder exit
// case: the chat is taller than the viewport, so the teardown target
// must clamp to the viewport top instead of addressing rows that
// scrolled away.
func TestTeardownLogAfterScrolledTranscript(t *testing.T) {
	const rows = 8
	ft, r := newVTRenderer(t, 40, rows, "")
	bottom := []string{"-- status --", "> "}
	var chat []string
	for i := range 20 {
		chat = append(chat, fmt.Sprintf("chat line %02d", i))
		r.DrawLog(chat, bottom, 1, 2)
	}
	r.TeardownLog()

	// Visible before teardown: chat tail + band + margin. After:
	// band rows erased, chat tail intact.
	logical := append(append([]string{}, chat...), bottom...)
	logical = append(logical, "")
	visible := logical[len(logical)-rows:]
	want := visible[:rows-3] // band (2 rows) + margin erased
	assertGrid(t, ft.Screen(), want)
}

// TestDrawLogKeepScrollbackMode pins the VS Code quirk handling: under
// TERM_PROGRAM=vscode the renderer must never emit \x1b[3J during
// frame painting (it snaps xterm.js's viewport), and full repaints
// must clear in place (home + erase-to-end) rather than via \x1b[2J
// (which stacks duplicate frames into xterm.js scrollback).
func TestDrawLogKeepScrollbackMode(t *testing.T) {
	ft, r := newVTRenderer(t, 40, 10, "vscode")
	if !r.KeepsScrollback() {
		t.Fatal("TERM_PROGRAM=vscode must enable keepScrollback")
	}
	// Pre-existing screen content, as after a VS Code session restore.
	ft.Screen().Write([]byte("stale shell prompt\r\n"))

	// The initial Resize in the helper legitimately clears scrollback
	// (a resize is a discrete user action, like Ctrl+L); only the
	// frame-painting output below must avoid the snap-inducing clears.
	setupLen := len(ft.Output())

	chat := []string{"fresh chat"}
	bottom := []string{"-- status --", "> "}
	r.DrawLog(chat, bottom, 1, 2)

	assertGrid(t, ft.Screen(), []string{
		"fresh chat",
		"-- status --",
		"> ",
	})
	painted := ft.Output()[setupLen:]
	if strings.Contains(painted, "\x1b[3J") {
		t.Fatal("keepScrollback renderer emitted \\x1b[3J during DrawLog")
	}
	if strings.Contains(painted, "\x1b[2J") {
		t.Fatal("keepScrollback renderer emitted \\x1b[2J during DrawLog; should clear in place")
	}
}

// TestDrawLogDefaultModeClearsScrollback is the inverse: ordinary
// terminals do get \x1b[3J on the initial full repaint so stale
// scrollback from the previous program doesn't linger above terva.
func TestDrawLogDefaultModeClearsScrollback(t *testing.T) {
	ft, r := newVTRenderer(t, 40, 10, "")
	r.DrawLog([]string{"hi"}, []string{"> "}, 0, 2)
	if !strings.Contains(ft.Output(), "\x1b[3J") {
		t.Fatal("default renderer should clear scrollback on the initial full repaint")
	}
}
