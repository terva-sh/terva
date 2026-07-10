package modes

import (
	"context"
	"sync"
	"testing"
)

func newTestEngine() *turnEngine { return newTurnEngine() }

// Exactly one of N concurrent producers claims the slot.
//
// The retired claimOrQueue also queued the losers under the same lock hold.
// Carrier mode does not: the daemon's wsSession is the busy arbiter, and a
// producer that loses either fails claimCarrier or gets CodeBusy back from
// Prompt, and queues through the service (startTurnCarrier). What the engine
// still owes is single-winner arbitration of its local UI slot.
func TestEngineClaimCarrierExactlyOneWinner(t *testing.T) {
	e := newTestEngine()
	const producers = 32
	var wg sync.WaitGroup
	claims := make(chan int, producers)
	for n := range producers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e.claimCarrier(func() {}) {
				claims <- n
			}
		}()
	}
	wg.Wait()
	close(claims)
	if got := len(claims); got != 1 {
		t.Fatalf("%d producers claimed the slot, want exactly 1", got)
	}
}

// The two tests that used to live here asserted the engine's queue policy:
// release(false) shifts a late-queued prompt, release(true) drops the queue on
// failure. Both are gone with the policy itself. The engine's agent WAS the
// daemon's, so that policy ran a second, unbroadcast copy of wsSession.endTurn
// against shared state — see shell_escape_queue_test.go. The daemon owns it now
// and always did; the workspace's own queue tests cover it.

// Compact and shell slots exclude turns and each other.
func TestEngineSlotExclusion(t *testing.T) {
	e := newTestEngine()
	if !e.claimCompact(func() {}, true) {
		t.Fatal("compact claim failed on idle engine")
	}
	if !e.AutoCompacting() {
		t.Fatal("autoCompacting not set")
	}
	if e.claimSlot(func() {}) {
		t.Fatal("shell slot claimed while compacting")
	}
	if e.claimCarrier(func() {}) {
		t.Fatal("turn claimed while compacting")
	}
	if !e.releaseCarrier() {
		t.Fatal("releaseCarrier reported an unheld slot")
	}
	if e.AutoCompacting() {
		t.Fatal("autoCompacting survived the release")
	}
	if !e.claimCarrier(func() {}) {
		t.Fatal("the slot was never released")
	}
}

// cancelActive runs the registered cancel exactly while a slot is
// claimed, and releasing clears it.
func TestEngineCancelActive(t *testing.T) {
	e := newTestEngine()
	if e.cancelActive() {
		t.Fatal("cancelActive on idle engine reported true")
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.claimCarrier(cancel)
	if !e.cancelActive() {
		t.Fatal("cancelActive with a claimed slot reported false")
	}
	if ctx.Err() == nil {
		t.Fatal("cancel func did not run")
	}
	e.releaseCarrier()
	if e.cancelActive() {
		t.Fatal("cancelActive after release reported true")
	}
}
