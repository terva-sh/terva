package tui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// reflowSetup paints a transcript taller than the viewport, then returns the
// renderer and the frame, ready for a change ABOVE the viewport.
func reflowSetup(t *testing.T, keepScrollback bool) (*Renderer, *bytes.Buffer, []string, []string) {
	t.Helper()
	buf := &bytes.Buffer{}
	r := NewRenderer(buf)
	r.keepScrollback = keepScrollback
	r.Resize(80, 10)

	chat := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		chat = append(chat, fmt.Sprintf("line-%02d", i))
	}
	bottom := []string{"> "}
	r.DrawLog(chat, bottom, 0, 2)
	if r.logViewportTop == 0 {
		t.Fatalf("setup is broken: transcript did not exceed the viewport (top=%d)", r.logViewportTop)
	}
	buf.Reset()
	return r, buf, chat, bottom
}

// reflowAbove replaces one row above the viewport with two, the classic
// "earlier content re-wrapped" case, shifting every logical row below it.
func reflowAbove(chat []string) []string {
	out := make([]string, 0, len(chat)+1)
	out = append(out, chat[:2]...)
	out = append(out, "line-02-wrapped-a", "line-02-wrapped-b")
	return append(out, chat[3:]...)
}

// The bug: a height change above the viewport used to wipe the user's
// scrollback and any native text selection, to fix rows nobody can see.
func TestAboveViewportReflowDoesNotClearScrollback(t *testing.T) {
	r, buf, chat, bottom := reflowSetup(t, false)
	r.DrawLog(reflowAbove(chat), bottom, 0, 2)
	out := buf.String()

	if strings.Contains(out, "\x1b[3J") {
		t.Error("emitted \\x1b[3J: scrollback wiped for a change above the viewport")
	}
	if strings.Contains(out, SeqClearScreenNoHome) {
		t.Error("cleared the screen for a change above the viewport")
	}
}

// The VS Code half. There scrollback cannot be cleared, so a full replay
// pushed the whole transcript into scrollback a second time.
func TestAboveViewportReflowDoesNotReplayTheTranscript(t *testing.T) {
	r, buf, chat, bottom := reflowSetup(t, true)
	r.DrawLog(reflowAbove(chat), bottom, 0, 2)
	out := buf.String()

	// Rows well above the viewport must not be re-emitted at all.
	for _, row := range []string{"line-00", "line-05", "line-10", "line-20"} {
		if strings.Contains(out, row) {
			t.Errorf("re-emitted %q, which is above the viewport — that is the duplication", row)
		}
	}
}

// ...and the half that stops the fix from being "emit nothing": rows that ARE
// visible and genuinely changed must still be repainted, or the screen goes
// stale after a reflow.
func TestAboveViewportReflowStillRepaintsVisibleChanges(t *testing.T) {
	r, buf, chat, bottom := reflowSetup(t, false)

	next := reflowAbove(chat)
	next[len(next)-1] = "line-39-CHANGED" // last row, firmly inside the viewport
	r.DrawLog(next, bottom, 0, 2)

	if out := buf.String(); !strings.Contains(out, "line-39-CHANGED") {
		t.Errorf("a visible changed row was not repainted after the reflow:\n%q", out)
	}
}

// The renumbering has to leave the cache describing the same physical rows, or
// the NEXT draw patches the wrong ones. Drawing the reflowed frame twice must
// settle: the second draw has nothing to say.
func TestAboveViewportReflowLeavesTheCacheConsistent(t *testing.T) {
	r, buf, chat, bottom := reflowSetup(t, false)
	next := reflowAbove(chat)

	r.DrawLog(next, bottom, 0, 2)
	buf.Reset()
	r.DrawLog(next, bottom, 0, 2)

	if buf.Len() != 0 {
		t.Errorf("redrawing the identical frame after a reflow emitted %d bytes; "+
			"the rebased cache does not match what was painted:\n%q", buf.Len(), buf.String())
	}
}

// A shrink is the same problem with the opposite sign, and the clamps only get
// exercised in this direction.
func TestAboveViewportReflowHandlesAShrink(t *testing.T) {
	r, buf, chat, bottom := reflowSetup(t, false)

	shrunk := append([]string{}, chat[:2]...)
	shrunk = append(shrunk, chat[5:]...) // three rows above the viewport vanish

	r.DrawLog(shrunk, bottom, 0, 2)
	if strings.Contains(buf.String(), "\x1b[3J") {
		t.Error("a shrink above the viewport wiped scrollback")
	}
	buf.Reset()
	r.DrawLog(shrunk, bottom, 0, 2)
	if buf.Len() != 0 {
		t.Errorf("cache inconsistent after a shrink: %q", buf.String())
	}
}

// After a rebase the re-diff runs from the viewport top, so the old
// "changes above the viewport" branch can no longer be reached. If it can, the
// rebase did not do its job and something would fall back to a full repaint.
func TestRebaseLeavesNothingChangedAboveTheViewport(t *testing.T) {
	r, _, chat, _ := reflowSetup(t, false)
	next := reflowAbove(chat)
	chatFrame := make([]string, len(next))
	for i, l := range next {
		chatFrame[i] = paintBackgroundRow(r.truncateCached(l), r.cols, r.theme)
	}
	lines := append(append([]string{}, chatFrame...), paintBackgroundRow("> ", r.cols, r.theme),
		paintBackgroundRow("", r.cols, r.theme))

	r.rebaseForAboveViewportChange(lines, chatFrame)
	first, _ := diffRows(r.logLines, lines, r.logViewportTop)
	if first >= 0 && first < r.logViewportTop {
		t.Fatalf("first changed row %d is still above the viewport top %d", first, r.logViewportTop)
	}
}

// The case the six tests above all missed, and that TestInteractiveHelpBlock
// caught instead: a shrink big enough to push the viewport anchor above the
// start of the buffer — dismissing a help block taller than the screen.
//
// Here the rebase must REFUSE. Clamping the anchor to 0 instead silently
// breaks screenRow = logHardwareRow - logViewportTop, and the new frame paints
// below the old content rather than over it, leaving the dismissed block on
// screen. The full repaint is the correct answer in this case, not a fallback
// worth avoiding.
func TestOversizedShrinkRefusesToRebaseAndRepaints(t *testing.T) {
	r, buf, chat, bottom := reflowSetup(t, false)
	oldTop := r.logViewportTop

	// Collapse 40 rows to 3: delta is far more negative than oldTop.
	tiny := chat[:3]
	if len(tiny)-len(chat)+oldTop >= 0 {
		t.Fatalf("fixture does not push the anchor negative (oldTop=%d delta=%d)",
			oldTop, len(tiny)-len(chat))
	}
	r.DrawLog(tiny, bottom, 0, 2)

	out := buf.String()
	// It must repaint (the cached model cannot describe the screen any more)...
	if !strings.Contains(out, SeqCursorHome) || !strings.Contains(out, SeqClearToEnd) {
		t.Error("an anchor-breaking shrink did not repaint; stale rows would stay on screen")
	}
	// ...but it must NOT take the user's scrollback and selection with it. The
	// user toggled a display mode; they did not ask for history to be dropped.
	if strings.Contains(out, "\x1b[3J") {
		t.Error("the anchor-breaking fallback wiped scrollback")
	}
	// And it must still settle: a redraw of the same frame says nothing.
	buf.Reset()
	r.DrawLog(tiny, bottom, 0, 2)
	if buf.Len() != 0 {
		t.Errorf("cache inconsistent after the repaint: %q", buf.String())
	}
}

// The invariant the guard protects, asserted directly: whenever the rebase
// accepts, the cursor's SCREEN row is unchanged. Everything downstream
// translates logical moves into physical ones through that equality.
func TestRebasePreservesTheCursorScreenRow(t *testing.T) {
	for _, name := range []string{"grow", "shrink"} {
		t.Run(name, func(t *testing.T) {
			r, _, chat, _ := reflowSetup(t, false)
			before := r.logHardwareRow - r.logViewportTop

			next := reflowAbove(chat) // +1 row above the viewport
			if name == "shrink" {
				next = append(append([]string{}, chat[:2]...), chat[4:]...) // -2
			}
			chatFrame := make([]string, len(next))
			for i, l := range next {
				chatFrame[i] = paintBackgroundRow(r.truncateCached(l), r.cols, r.theme)
			}
			lines := append(append([]string{}, chatFrame...),
				paintBackgroundRow("> ", r.cols, r.theme), paintBackgroundRow("", r.cols, r.theme))

			if !r.rebaseForAboveViewportChange(lines, chatFrame) {
				t.Skip("rebase declined for this fixture; covered by the refusal test")
			}
			if after := r.logHardwareRow - r.logViewportTop; after != before {
				t.Errorf("cursor screen row moved across the rebase: %d -> %d", before, after)
			}
		})
	}
}

// Which path does a ctrl+t / ctrl+o style collapse take? The answer depends on
// whether the COLLAPSED transcript still exceeds the screen, and the two cases
// behave very differently for the user — so pin both.
func TestToolDisplayCollapseTakesTheExpectedPath(t *testing.T) {
	build := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("row-%03d", i)
		}
		return out
	}
	bottom := []string{"> "}

	t.Run("still taller than the screen: rebases, no scrollback wipe", func(t *testing.T) {
		buf := &bytes.Buffer{}
		r := NewRenderer(buf)
		r.Resize(80, 50)
		r.DrawLog(build(200), bottom, 0, 2)
		buf.Reset()

		r.DrawLog(build(60), bottom, 0, 2) // collapsed, still > 50 rows
		if strings.Contains(buf.String(), "\x1b[3J") {
			t.Error("wiped scrollback: the anchor should have survived this collapse")
		}
	})

	t.Run("collapses under the screen: refuses, repaints", func(t *testing.T) {
		buf := &bytes.Buffer{}
		r := NewRenderer(buf)
		r.Resize(80, 50)
		r.DrawLog(build(200), bottom, 0, 2)
		buf.Reset()

		r.DrawLog(build(20), bottom, 0, 2) // collapsed below the 50-row screen
		out := buf.String()
		if !strings.Contains(out, SeqCursorHome) || !strings.Contains(out, SeqClearToEnd) {
			t.Error("expected the anchor-breaking fallback to repaint")
		}
		if strings.Contains(out, "\x1b[3J") {
			t.Error("the fallback wiped scrollback; a mode toggle must not drop history")
		}
	})
}
