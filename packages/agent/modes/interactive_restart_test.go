package modes

// Tests for TUI recovery when a Tier-1 self-restart's deferred exec fails.
// relaunch keeps the process serving on exec failure, but the pre-exec hook has
// already torn the terminal down — so without resumeAfterFailedRestart the
// survived process is a dead TUI holding a raw, half-painted terminal.

import (
	"errors"
	"sync"
	"testing"

	"terva.sh/terva/packages/tui/tuitest"
)

// TestResumeAfterFailedRestartRecoversTerminal drives the failed-restart
// sequence through the real Run loop the way the relaunch hooks would: the
// pre-exec hook tears the terminal down (shuttingDown, cooked mode, band
// erased), then the OnFailure hook runs resumeAfterFailedRestart. Recovery must
// repaint (surfacing the error) and leave the TUI live — input still echoes.
func TestResumeAfterFailedRestartRecoversTerminal(t *testing.T) {
	h := startInteractive(t, nil)
	h.dismissLoginDialog()

	// Pre-exec teardown (what the OnPreExec hook does), marshalled onto main.
	done := make(chan struct{})
	h.i.runOnMain(func() { h.i.teardownTerminal(); close(done) })
	<-done
	if !h.i.shuttingDown.Load() {
		t.Fatal("precondition: teardown should have set shuttingDown")
	}

	// Deferred exec failed → relaunch keeps serving and fires OnFailure.
	h.i.runOnMain(func() { h.i.resumeAfterFailedRestart(errors.New("binary vanished")) })

	// The survived process repaints and surfaces the failure…
	h.waitText("restart failed")
	if h.i.shuttingDown.Load() {
		t.Fatal("resume should clear shuttingDown so frames paint again")
	}
	// …and input still works: the loop is alive and raw mode is back.
	h.term.Type("recovered-input")
	h.waitText("recovered-input")

	// teardownOnce is re-armed, so a later real exit still restores the terminal.
	h.i.runOnMain(func() { h.i.teardownTerminal() })
	h.waitScreen("shuttingDown to re-latch on a fresh teardown", func(*tuitest.Screen) bool {
		return h.i.shuttingDown.Load()
	})
}

// rawFailTerm is a terminal whose raw mode can no longer be entered — the
// unrecoverable case resumeAfterFailedRestart must exit cleanly on rather than
// live on half-torn.
type rawFailTerm struct{ *tuitest.FakeTerm }

func (rawFailTerm) EnterRaw() (func() error, error) { return nil, errors.New("no tty") }

// TestResumeAfterFailedRestartExitsWhenTerminalUnrecoverable: if raw mode can't
// be re-entered, resume must ask the run loop to exit cleanly (close
// restartFailQuit) instead of claiming the process can continue.
func TestResumeAfterFailedRestartExitsWhenTerminalUnrecoverable(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	i.cfg.Terminal = rawFailTerm{tuitest.NewFakeTerm(80, 24)}
	i.restartFailQuit = make(chan struct{})
	i.teardownOnce = sync.Once{}

	i.resumeAfterFailedRestart(errors.New("boom"))

	select {
	case <-i.restartFailQuit:
		// closed as required — the main loop's select would return nil.
	default:
		t.Fatal("resume must close restartFailQuit when the terminal can't be recovered")
	}
}
