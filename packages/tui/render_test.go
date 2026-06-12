package tui

import (
	"bytes"
	"strings"
	"testing"
)

// TestDrawLogIdleNoOpEmitsNothing pins the cursor-blink fix: when
// DrawLog is called with the exact same buffer and cursor position
// as the previous call, it must emit ZERO bytes.
//
// The bug this regresses: at the 120ms animation tick the renderer
// used to always emit SeqHideCursor + cursor-position +
// SeqShowCursor, which resets the terminal's blink timer. Faster
// than the OS blink interval, so an idle dialog editor (e.g. a
// re-opened swarm transcript whose agent isn't producing output)
// rendered the caret as a solid non-blinking block.
//
// With the no-op fast path the renderer leaves the screen alone
// on idle frames, letting the terminal run its own blink cycle.
func TestDrawLogIdleNoOpEmitsNothing(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf)
	r.Resize(80, 24)

	chat := []string{"hello", "world"}
	bottom := []string{"▌ "}
	// First draw populates the renderer's cached buffer.
	r.DrawLog(chat, bottom, 0, 2)
	first := buf.Len()
	if first == 0 {
		t.Fatal("first DrawLog wrote nothing; setup is broken")
	}
	buf.Reset()

	// Identical second draw: same content, same cursor placement.
	r.DrawLog(chat, bottom, 0, 2)
	if buf.Len() != 0 {
		t.Fatalf("idle re-draw emitted %d bytes; expected 0 so terminal blink keeps ticking\n%q",
			buf.Len(), buf.String())
	}
}

// TestDrawLogContentChangeBreaksFastPath proves the no-op fast path
// only fires when nothing changed. A buffer mutation must still
// produce output, otherwise streaming agent replies would freeze on
// screen.
func TestDrawLogContentChangeBreaksFastPath(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf)
	r.Resize(80, 24)

	r.DrawLog([]string{"hello"}, []string{"▌ "}, 0, 2)
	buf.Reset()

	// New chat row lands.
	r.DrawLog([]string{"hello", "world"}, []string{"▌ "}, 0, 2)
	if buf.Len() == 0 {
		t.Fatal("content change suppressed by fast path; streaming output would freeze")
	}
}

// TestDrawLogCursorMoveBreaksFastPath proves a cursor-only change
// (no buffer change) still produces output. Without this, typing in
// the editor would visually move the caret but the terminal would
// keep drawing it at the old column.
func TestDrawLogCursorMoveBreaksFastPath(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf)
	r.Resize(80, 24)

	r.DrawLog([]string{"hi"}, []string{"▌ "}, 0, 2)
	buf.Reset()

	// Same buffer, different cursor column.
	r.DrawLog([]string{"hi"}, []string{"▌ "}, 0, 3)
	if buf.Len() == 0 {
		t.Fatal("cursor-only change suppressed by fast path; caret would lag behind typing")
	}
	// And the emitted bytes must at least reposition the cursor.
	if !strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("cursor move emission missing CSI escapes: %q", buf.String())
	}
}

// TestDrawLogResizeForcesFullRedraw confirms a resize invalidates
// the cache so the next DrawLog with identical inputs still emits.
// Resize sets logInit=false; without that, a resize followed by an
// identical buffer would falsely no-op and leave a stale frame.
func TestDrawLogResizeForcesFullRedraw(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf)
	r.Resize(80, 24)
	r.DrawLog([]string{"hi"}, []string{"▌ "}, 0, 2)
	buf.Reset()

	r.Resize(100, 30)
	r.DrawLog([]string{"hi"}, []string{"▌ "}, 0, 2)
	if buf.Len() == 0 {
		t.Fatal("post-resize redraw skipped; the new frame would never reach the terminal")
	}
}

// TestTeardownLogErasesBandKeepsChat pins the exit cleanup: after the
// last frame, TeardownLog must move the cursor up off the live band and
// erase to the end of the screen (dropping the status/input chrome)
// without emitting any screen/scrollback clear that would wipe the chat
// transcript sitting in scrollback above.
func TestTeardownLogErasesBandKeepsChat(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf)
	r.Resize(80, 24)

	// Two chat rows, a two-row live band; cursor on the band's 2nd row.
	r.DrawLog([]string{"hello", "world"}, []string{"status", "▌ "}, 1, 2)
	buf.Reset()

	r.TeardownLog()
	out := buf.String()

	if !strings.Contains(out, SeqEraseToEnd) {
		t.Fatalf("teardown must erase to end of screen; got %q", out)
	}
	// Must NOT wipe the screen or scrollback — that would destroy the
	// conversation the user just had.
	if strings.Contains(out, SeqClearScreenNoHome) || strings.Contains(out, SeqClearScreen) || strings.Contains(out, SeqClearScrollback) {
		t.Fatalf("teardown wiped screen/scrollback; chat transcript must survive: %q", out)
	}
	// Cursor sits on band row index 1 (logical row 3: 2 chat rows + the
	// "status" row); the band starts at logical row 2, so teardown moves
	// up exactly one row before erasing.
	if !strings.Contains(out, "\x1b[1A") {
		t.Fatalf("teardown should move cursor up to the band start; got %q", out)
	}
}

// TestTeardownLogBeforeDrawIsNoOp guards the early-exit guard: tearing
// down before anything was ever painted must not emit stray escapes.
func TestTeardownLogBeforeDrawIsNoOp(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf)
	r.Resize(80, 24) // Resize itself emits a clear; ignore that.
	buf.Reset()

	r.TeardownLog()
	if buf.Len() != 0 {
		t.Fatalf("teardown before first DrawLog emitted %d bytes; want 0: %q", buf.Len(), buf.String())
	}
}
