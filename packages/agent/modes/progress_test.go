package modes

// OSC 9;4 busy/idle progress reporting, end to end through the real Run
// loop: the terminal is told when a turn starts and when it ends.
//
// These assert on the raw byte stream (FakeTerm.Output) rather than the
// emulated screen, because the whole point of the signal is that it is
// NOT screen content — it is out-of-band state for the terminal, and for
// anything reading a recording of one.

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/tui"
	"terva.sh/terva/packages/tui/tuitest"
)

// waitOutput polls the raw terminal byte stream until it contains want,
// failing with a readable dump on timeout. The TUI paints on a throttle
// and a 120ms animation tick, so an immediate read races the redraw.
func waitOutput(t *testing.T, term *tuitest.FakeTerm, desc, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(term.Output(), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s (%q in the terminal byte stream)", desc, want)
}

// enableProgress turns the process-wide gate on AFTER the harness has
// booted. Order matters: Run() calls SetProgress(DetectProgressSupport())
// during startup, so flipping it before the first paint would simply be
// overwritten by the harness's pinned "off". dismissLoginDialog has
// already waited for a frame, which means startup is past that call.
func enableProgress(t *testing.T) {
	t.Helper()
	tui.SetProgress(true)
	// The harness registered a Cleanup restoring the previous value.
	t.Cleanup(func() { tui.SetProgress(false) })
}

// The core contract: one sequence when the turn starts, one when it ends.
//
// "Exactly once" is the substance of the test, not a detail. The status
// bar repaints on every animation tick for the whole turn, so a naive
// implementation that emitted from the render path would put hundreds of
// identical escapes into the stream — which still looks correct on a
// terminal (the state is idempotent) while destroying the property that
// makes the signal worth having: that a recording contains one greppable
// event per transition.
func TestProgressAnnouncesTurnStartAndEndExactlyOnce(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()
	enableProgress(t)

	// A turn goes in flight.
	if !h.i.turns.claimSlot(func() {}) {
		t.Fatal("could not claim the turn slot")
	}
	waitOutput(t, h.term, "the busy announcement", tui.SeqProgressBusy)

	// Let several animation ticks land while still busy. Any per-frame
	// emission shows up here as a second copy.
	time.Sleep(400 * time.Millisecond)
	if n := strings.Count(h.term.Output(), tui.SeqProgressBusy); n != 1 {
		t.Errorf("busy sequence written %d times during one turn, want exactly 1 — "+
			"the emission is not edge-triggered", n)
	}

	// The turn ends.
	h.i.turns.releaseSlot()
	waitOutput(t, h.term, "the idle announcement", tui.SeqProgressIdle)

	time.Sleep(200 * time.Millisecond)
	if n := strings.Count(h.term.Output(), tui.SeqProgressIdle); n != 1 {
		t.Errorf("idle sequence written %d times after one turn, want exactly 1", n)
	}
}

// The gate has to hold at the point of emission, not only in the
// detector. A user on iTerm2 gets a notification toast per turn if this
// regresses, so silence on an unsupported terminal is the safety
// property.
func TestProgressStaysSilentWhenTheTerminalDoesNotSupportIt(t *testing.T) {
	h := startInteractive(t, nil) // harness pins TERVA_PROGRESS=off
	h.dismissLoginDialog()

	if !h.i.turns.claimSlot(func() {}) {
		t.Fatal("could not claim the turn slot")
	}
	time.Sleep(400 * time.Millisecond) // plenty of frames
	h.i.turns.releaseSlot()
	time.Sleep(200 * time.Millisecond)

	if out := h.term.Output(); strings.Contains(out, "\x1b]9;4") {
		t.Error("wrote an OSC 9;4 sequence on a terminal that was not detected as supporting it")
	}
}

// The indicator is terminal state that outlives the process: a terva that
// exits (or self-restarts) mid-turn and does not clear it leaves a busy
// progress bar on the tab of a shell that is no longer running anything.
func TestTeardownClearsProgressEvenWhenExitingMidTurn(t *testing.T) {
	prev := tui.ProgressEnabled()
	tui.SetProgress(true)
	t.Cleanup(func() { tui.SetProgress(prev) })

	// A bare Interactive rather than the harness: teardownTerminal touches
	// main-loop-only renderer state, so reaching for it from a test
	// goroutine while Run is live would be a data race. rend stays nil,
	// which teardownTerminal already guards for.
	term := tuitest.NewFakeTerm(80, 24)
	i := &Interactive{cfg: InteractiveConfig{Terminal: term}}
	i.lastProgressBusy = true // as if a turn were in flight

	i.teardownTerminal()

	if !strings.Contains(term.Output(), tui.SeqProgressIdle) {
		t.Error("teardown did not clear the progress indicator; a busy bar would " +
			"outlive the process on the terminal's tab")
	}
}
