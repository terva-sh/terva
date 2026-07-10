package workspace

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// stallHub returns a hub with one reliable subscriber whose buffer is
// already full and a broadcaster goroutine parked on the overflow send.
// The returned wait func joins that broadcaster.
func stallHub(t *testing.T) (h *wsHub, stalled chan ctrlproto.Event, wait func()) {
	t.Helper()
	h = newWSHub()
	stalled = h.add(nil, true)
	for range hubBuffer {
		h.broadcast(ctrlproto.ConversationEvent(core.WireEvent{Type: "fill"}))
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.broadcast(ctrlproto.ConversationEvent(core.WireEvent{Type: "overflow"})) // parks: buffer full, nobody draining
	}()
	// Give the broadcaster a beat to park on the send.
	time.Sleep(20 * time.Millisecond)
	return h, stalled, func() {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("parked broadcast never finished")
		}
	}
}

// TestWSHubStalledReliableConsumerDoesNotWedgeHub is the review finding:
// broadcast used to hold h.mu across the blocking reliable send, so one
// stuck consumer blocked add/remove/closeAll and every other broadcast.
// Now the map operations must stay live while a broadcast is parked, and
// removing the stuck subscriber must unblock the parked broadcast.
func TestWSHubStalledReliableConsumerDoesNotWedgeHub(t *testing.T) {
	h, stalled, wait := stallHub(t)

	// add() must not block behind the parked broadcast.
	addDone := make(chan chan ctrlproto.Event, 1)
	go func() {
		addDone <- h.add(nil, false)
	}()
	var other chan ctrlproto.Event
	select {
	case other = <-addDone:
	case <-time.After(time.Second):
		t.Fatal("add blocked behind a stalled reliable consumer")
	}

	// remove() of an unrelated subscriber must not block either.
	removed := make(chan struct{})
	go func() {
		h.remove(other)
		close(removed)
	}()
	select {
	case <-removed:
	case <-time.After(time.Second):
		t.Fatal("remove blocked behind a stalled reliable consumer")
	}

	// Removing the STUCK subscriber unblocks the parked broadcast.
	h.remove(stalled)
	wait()

	// The channel closed exactly once and drains its buffered backlog.
	drained := 0
	for range stalled {
		drained++
	}
	if drained != hubBuffer {
		t.Fatalf("drained %d buffered events, want %d", drained, hubBuffer)
	}
}

// TestWSHubCloseAllUnblocksParkedBroadcast: session teardown must not
// deadlock on a consumer that stopped draining.
func TestWSHubCloseAllUnblocksParkedBroadcast(t *testing.T) {
	h, stalled, wait := stallHub(t)

	closed := make(chan struct{})
	go func() {
		h.closeAll()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("closeAll blocked behind a stalled reliable consumer")
	}
	wait()
	for range stalled {
	} // channel closed: range terminates
}

// TestWSHubReliableTotalOrder: sendMu serializes broadcasts, so two
// reliable subscribers must observe the SAME sequence even when many
// goroutines broadcast concurrently.
func TestWSHubReliableTotalOrder(t *testing.T) {
	h := newWSHub()
	a := h.add(nil, true)
	b := h.add(nil, true)

	const n = 200 // < hubBuffer: nothing parks
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h.broadcast(ctrlproto.ConversationEvent(core.WireEvent{Type: fmt.Sprintf("ev-%d", i)}))
		}(i)
	}
	wg.Wait()
	h.closeAll()

	seqA := collect(a)
	seqB := collect(b)
	if len(seqA) != n || len(seqB) != n {
		t.Fatalf("delivered %d/%d events, want %d for both", len(seqA), len(seqB), n)
	}
	for i := range seqA {
		if seqA[i] != seqB[i] {
			t.Fatalf("order diverged at %d: %q vs %q", i, seqA[i], seqB[i])
		}
	}
}

func collect(ch chan ctrlproto.Event) []string {
	var out []string
	for ev := range ch {
		out = append(out, ev.Type)
	}
	return out
}
