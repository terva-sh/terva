package modes

// The idle offer's retract on a session switch, and the goroutine it runs on.
//
// Stage 3 of docs/proposals/idle-suggestions.md put the retract in
// SwitchCarrierSession. The reasoning was right — the offer belongs to the
// conversation being left — and the placement was wrong. The editor is
// MAIN-LOOP-ONLY state (see composer() in interactive_nextstep_test.go):
// Editor.Render reads it on the main loop, the Editor has no lock of its own,
// and SwitchCarrierSession runs on whichever goroutine asked for the switch
// (cli_ctrlproto's resume and new-session paths, interactive_auth). So the
// retract raced the renderer:
//
//	WARNING: DATA RACE
//	Read at  ... tui.(*Editor).ghostVisible() / Render() / modes.redraw()
//	Previous write at ... tui.(*Editor).SetGhost() / modes.SwitchCarrierSession()
//
// A production race, not a test artifact — and a quiet one. A full local
// `go test -race ./...` was green twice while CI caught it.
//
// ⚠️ -race is NOT a usable guard for this, and it was tried before this comment
// was written. The write sits between mutex operations (releaseCarrier/CancelAll
// before it, i.mu.Lock after; and in retractOfferOnBind, its own i.mu section).
// Those acquisitions order the write against the renderer in almost every
// interleaving, so the detector needs a render that is mid-flight at the exact
// write — which is why CI hit it once and 200 sequential switches under render
// pressure hit it never. An ablated runOnMain still passes -race here.
//
// So the marshalling is held by ORDER instead, deterministically, in
// TestTheRetractIsMarshalledOntoTheMainLoop: block the main loop, queue a probe
// ahead of the retract, and see which one ran first. That guard fails every run
// when the retract is written inline.
//
// Events are delivered with handleCarrierEvent rather than through fc.stream on
// purpose. The fake carrier hands back the SAME channel for every subscription
// and never closes it, so the pump's `for ev := range ch` cannot exit, never
// re-subscribes after a switch, and its own guard then DROPS every event pushed
// for the new binding. handleCarrierEvent is also what the pump goroutine calls,
// so this delivers the event the way production does — off the main loop.

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/tui/tuitest"
)

// flushMain waits until everything already queued for the main loop has run.
func flushMain(h *harness) {
	done := make(chan struct{})
	h.i.runOnMain(func() { close(done) })
	<-done
}

// standOffer puts an offer on the composer from the main loop, where the editor
// lives, and waits until it is really on screen.
func standOffer(t *testing.T, h *harness, line string) {
	t.Helper()
	done := make(chan struct{})
	h.i.runOnMain(func() { h.i.ed.SetGhost(line); close(done) })
	<-done
	// Wait for the offer on the PAINTED screen, not merely in the editor. Only
	// the screen proves Editor.Render ran and read the offer field, which is the
	// read side of the race this file guards. An earlier version polled
	// composer() instead — that reads the editor THROUGH the main loop and so
	// says nothing about rendering, and the guard could not bite.
	h.waitScreen("the offer to be painted on the composer", func(s *tuitest.Screen) bool {
		return strings.Contains(s.Text(), line)
	})
}

// renderPressure keeps redraws coming, so the renderer is reading the offer
// field throughout whatever the caller does next. Every redraw runs
// Editor.Render -> ghostVisible, which is the read side of the race.
func renderPressure(t *testing.T, h *harness) {
	t.Helper()
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-stop:
				return
			default:
			}
			h.i.invalidate()
			time.Sleep(time.Millisecond)
		}
	}()
	t.Cleanup(func() { close(stop); <-stopped })
}

func snapshotFor(id string) ctrlproto.Event {
	return ctrlproto.SnapshotEvent(ctrlproto.Snapshot{Session: ctrlproto.SessionInfo{ID: id}})
}

// The retract must be queued for the main loop, not written where the snapshot
// is delivered. This is the guard that replaces the unreliable -race one: it
// pins the ORDER, which is deterministic, rather than a timing window.
//
// The main loop is parked, a probe that reads the offer is queued behind the
// parking function, and only then is the snapshot delivered. A marshalled
// retract queues BEHIND that probe, so the probe still sees the offer. A retract
// written inline on the delivering goroutine has already wiped it.
func TestTheRetractIsMarshalledOntoTheMainLoop(t *testing.T) {
	fc := newFakeCarrier()
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})

	standOffer(t, h, "run the tests")
	if err := h.i.SwitchCarrierSession("s2"); err != nil {
		t.Fatalf("switch session: %v", err)
	}

	// Park the main loop. Nothing queued for it runs until release is closed.
	parked, release := make(chan struct{}), make(chan struct{})
	h.i.runOnMain(func() { close(parked); <-release })
	<-parked

	// Queued while the loop is parked, so it sits ahead of anything the snapshot
	// below queues.
	var seen string
	probed := make(chan struct{})
	h.i.runOnMain(func() { seen = h.i.ed.Ghost(); close(probed) })

	h.i.handleCarrierEvent(snapshotFor("s2"))

	close(release)
	<-probed

	if seen != "run the tests" {
		t.Fatalf("the offer was already gone when the main loop resumed: ghost = %q, want %q\n"+
			"the retract ran on the delivering goroutine instead of being marshalled with "+
			"runOnMain, which is a data race against Editor.Render", seen, "run the tests")
	}

	// And it does still retract, once the loop drains.
	flushMain(h)
	if got := composer(h).ghost; got != "" {
		t.Fatalf("after the main loop drained, ghost = %q, want it retracted", got)
	}
}

// A fresh binding retracts the offer. The render pressure and the sleep keep the
// -race half honest for free, though see the header: they cannot be relied on.
func TestSwitchingSessionsRetractsTheOffer(t *testing.T) {
	fc := newFakeCarrier()
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})

	// No snapshot before the switch, so the ONLY one in this test is the new
	// binding's and the retract asserted below can have come from nowhere else.
	standOffer(t, h, "run the tests")
	renderPressure(t, h)

	// This sleep is the guard, not padding. composer() and waitScreen both
	// synchronise with the main loop, which ORDERS the renders around them
	// against this goroutine — and an ordered pair is not a race. Sleeping lets
	// renders land with no happens-before edge to the delivery below.
	time.Sleep(150 * time.Millisecond)

	if err := h.i.SwitchCarrierSession("s2"); err != nil {
		t.Fatalf("switch session: %v", err)
	}
	// The new binding's first snapshot, delivered as the pump delivers it: off
	// the main loop.
	h.i.handleCarrierEvent(snapshotFor("s2"))

	h.waitScreen("the offer to be retracted by the new binding", func(*tuitest.Screen) bool {
		return composer(h).ghost == ""
	})
}

// Only a FRESH BINDING retracts. A snapshot also arrives whenever the transcript
// is replaced under every client — compact, auto-compact, clear — and stage 3
// deliberately keeps the offer through those: it is still the same conversation
// the offer was computed against. Retracting on every snapshot would put the bug
// back in a new place, so the arm is one-shot per binding.
func TestASnapshotThatIsNotAFreshBindingKeepsTheOffer(t *testing.T) {
	fc := newFakeCarrier()
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})

	// Spend the boot binding's one-shot arm, and let its retract run, before
	// standing up the offer that must survive.
	h.i.handleCarrierEvent(snapshotFor("s1"))
	flushMain(h)

	standOffer(t, h, "run the tests")
	renderPressure(t, h)

	// A second snapshot on the SAME binding — what a compact or a clear sends.
	h.i.handleCarrierEvent(snapshotFor("s1"))
	flushMain(h)

	if got := composer(h).ghost; got != "run the tests" {
		t.Fatalf("a compact/clear snapshot retracted the offer: ghost = %q, want %q\n"+
			"the retract must be armed once per BINDING, not fired on every snapshot", got, "run the tests")
	}
}
