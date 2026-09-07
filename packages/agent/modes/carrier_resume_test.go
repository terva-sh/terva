package modes

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestCarrierResumeRoutesToService: /continue asks the service to run the loop
// again and holds the local slot while the reply streams, exactly as a prompt
// does. The epoch assertion pins the deliberate choice: the TUI tracks no
// transcript revision, and 0 means "do not check" (see ctrlproto.TurnResumeParams).
func TestCarrierResumeRoutesToService(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc

	i.runCarrierResume(context.Background())

	p := recv(t, fc.resumes, "resume")
	if p.Epoch != 0 {
		t.Errorf("epoch = %d; want 0 — the TUI tracks no transcript revision", p.Epoch)
	}
	if !i.turns.Busy() {
		t.Error("resume should hold the busy slot while the reply streams")
	}
}

// TestCarrierResumeRefusesWhileATurnRuns covers the one place resume
// deliberately differs from a prompt. A prompt that loses the busy race is
// queued so the user's words survive; resume has no words, and a turn already
// running is proof the session is not stuck. Queueing it would fire a spurious
// extra turn as soon as the real one finished.
func TestCarrierResumeRefusesWhileATurnRuns(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc

	i.runCarrierResume(context.Background())
	recv(t, fc.resumes, "resume")

	i.runCarrierResume(context.Background())
	select {
	case <-fc.resumes:
		t.Error("a second resume reached the service while a turn was already running")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case q := <-fc.queued:
		t.Errorf("resume queued %q; it has no text to queue and must not invent a turn", q)
	default:
	}
}

// TestCarrierResumeReleasesTheSlotWhenRefused guards a wedge rather than a
// cosmetic slip. The daemon refuses resume whenever the session is not stuck, or
// when a cut-short reply meets a provider that cannot continue one. Nothing
// streams in that case, so no "done" event will ever arrive to release the slot:
// if the error path does not hand it back, the composer stays locked and the
// session looks broken by the very command meant to unbreak it.
func TestCarrierResumeReleasesTheSlotWhenRefused(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	fc.resumeErr = errors.New("this session is not waiting on a reply, so there is nothing to resume")
	i.cfg.Carrier = fc

	i.runCarrierResume(context.Background())
	recv(t, fc.resumes, "resume")

	deadline := time.Now().Add(2 * time.Second)
	for i.turns.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("a refused resume left the turn slot claimed; the composer would stay locked with nothing running")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
