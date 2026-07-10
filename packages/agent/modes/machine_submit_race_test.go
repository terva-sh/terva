package modes

// Machine callers (the chat bridge's receive goroutine, the swarm
// finalizer, an extension's Submit hook) enter turn dispatch from off the
// main loop, while turn start clears main-loop-only render state —
// resetTurnUI writes view.TailLimit and the scroll baselines, which the
// render path reads without i.mu by design. SubmitOrQueue and Submit
// marshal the dispatch through runOnMain; this test pins that.
//
// It is only meaningful under -race: it drives real submits from a
// background goroutine in lockstep with the live Run() loop's renderer,
// which is exactly the interleaving that tripped the detector on the
// bridge path (TestPairedDMCannotShellEscape) before the marshalling.

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestMachineSubmitsDoNotRaceTheRenderLoop(t *testing.T) {
	fc := newFakeCarrier()
	h := startInteractive(t, func(c *InteractiveConfig) {
		c.Carrier = fc
		c.Ready = true // logged in, so submits reach turn dispatch
	})

	// Acknowledge every dispatch and release the turn slot after each
	// prompt, so the NEXT submit re-claims and re-arms the per-turn UI —
	// the racy path — instead of parking in the queue behind turn one.
	delivered := make(chan struct{})
	drainCtx, stopDrain := context.WithCancel(context.Background())
	defer stopDrain()
	go func() {
		for {
			select {
			case <-drainCtx.Done():
				return
			case <-fc.prompts:
				h.i.turns.releaseCarrier()
			case <-fc.queued:
			}
			select {
			case <-drainCtx.Done():
				return
			case delivered <- struct{}{}:
			}
		}
	}()

	// Lockstep: each submit waits for its dispatch to land before the
	// next fires. That keeps the actions buffer from ever saturating
	// (runOnMain's overflow fallback runs inline — the pre-fix behavior
	// this test exists to rule out) while every iteration still overlaps
	// the renderer, which repaints on each turn start's invalidate.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for n := 0; n < 40; n++ {
			if n%2 == 0 {
				h.i.SubmitOrQueue(fmt.Sprintf("machine prompt %d", n), nil)
			} else {
				h.i.Submit(fmt.Sprintf("ext prompt %d", n))
			}
			select {
			case <-delivered:
			case <-time.After(5 * time.Second):
				t.Errorf("submit %d never reached the carrier", n)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("machine submit burst never completed")
	}
}
