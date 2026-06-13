package modes

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"terva.sh/terva/packages/core"
)

func newTestEngine() *turnEngine {
	t := newTurnEngine()
	t.SetAgent(core.NewAgent(nil, "fake", "", core.Registry{}))
	return t
}

// Exactly one of N concurrent producers claims the slot; every other
// prompt queues. Nothing is lost, nothing runs twice.
func TestEngineClaimOrQueueExactlyOneWinner(t *testing.T) {
	e := newTestEngine()
	const producers = 32
	var wg sync.WaitGroup
	claims := make(chan int, producers)
	for n := range producers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e.claimOrQueue(fmt.Sprintf("p%02d", n), func() {}) {
				claims <- n
			}
		}()
	}
	wg.Wait()
	close(claims)
	var winners []int
	for n := range claims {
		winners = append(winners, n)
	}
	if len(winners) != 1 {
		t.Fatalf("%d producers claimed the slot, want exactly 1", len(winners))
	}
	if got := e.QueuedCount(); got != producers-1 {
		t.Fatalf("queued = %d, want %d", got, producers-1)
	}
}

// The strand regression: a prompt queued in the final instants of a
// turn must be picked up by release, never left in the queue with no
// turn running. Before the engine, the busy check and the enqueue
// were separated by a lock release, so a message could land after
// the turn's last queue check and before busy flipped false.
func TestEngineReleaseShiftsLateQueuedPrompt(t *testing.T) {
	e := newTestEngine()
	for round := range 200 {
		if !e.claimOrQueue("turn", func() {}) {
			t.Fatalf("round %d: claim failed on idle engine", round)
		}
		done := make(chan struct{})
		go func() {
			// Producer racing the release below.
			e.claimOrQueue("late", func() {})
			close(done)
		}()
		next, hasNext := e.release(false)
		<-done
		if hasNext {
			// release won the race and shifted the late prompt.
			if next != "late" {
				t.Fatalf("round %d: shifted %q, want late", round, next)
			}
			if e.Busy() {
				t.Fatalf("round %d: busy after release", round)
			}
		} else {
			// The producer lost the queue race and claimed the freed
			// slot directly instead — equally fine, nothing stranded.
			if !e.Busy() {
				t.Fatalf("round %d: no next prompt AND engine idle — prompt stranded", round)
			}
			e.release(true)
		}
		e.DrainQueued()
	}
}

// Cancelled or errored turns drop the queue so stale follow-ups
// don't fire after an interrupt.
func TestEngineReleaseDropsQueueOnFailure(t *testing.T) {
	e := newTestEngine()
	e.claimOrQueue("turn", func() {})
	e.claimOrQueue("queued-behind", func() {})
	next, hasNext := e.release(true)
	if hasNext || next != "" {
		t.Fatalf("release(drop) returned %q,%v; want none", next, hasNext)
	}
	if got := e.QueuedCount(); got != 0 {
		t.Fatalf("queue not dropped: %d left", got)
	}
}

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
	if e.claimOrQueue("p", func() {}) {
		t.Fatal("turn claimed while compacting")
	}
	if got := e.QueuedCount(); got != 1 {
		t.Fatalf("prompt should have queued behind the compact; queued = %d", got)
	}
	if next, ok := e.release(false); !ok || next != "p" {
		t.Fatalf("release = %q,%v; want p,true", next, ok)
	}
	if e.AutoCompacting() {
		t.Fatal("autoCompacting survived release")
	}
}

// cancelActive runs the registered cancel exactly while a slot is
// claimed, and release clears it.
func TestEngineCancelActive(t *testing.T) {
	e := newTestEngine()
	if e.cancelActive() {
		t.Fatal("cancelActive on idle engine reported true")
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.claimOrQueue("p", cancel)
	if !e.cancelActive() {
		t.Fatal("cancelActive with a claimed slot reported false")
	}
	if ctx.Err() == nil {
		t.Fatal("cancel func did not run")
	}
	e.release(true)
	if e.cancelActive() {
		t.Fatal("cancelActive after release reported true")
	}
}
