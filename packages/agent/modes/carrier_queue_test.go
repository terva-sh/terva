package modes

import (
	"reflect"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
)

func newQueueTestInteractive() (*Interactive, *fakeCarrier) {
	fc := newFakeCarrier()
	i := &Interactive{dirty: make(chan struct{}, 1), turns: newTurnEngine()}
	i.cfg.Carrier = fc
	i.cfg.CarrierSession = "s1"
	return i, fc
}

func (f *fakeCarrier) lastSetQueue(t *testing.T) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.setQueues) == 0 {
		t.Fatal("no SetQueue call recorded")
	}
	return f.setQueues[len(f.setQueues)-1]
}

// The daemon owns the queue and broadcasts the whole list on every mutation.
// The TUI mirrors it; before this it rendered the chips off the in-process
// crutch agent and ignored queue_updated entirely.
func TestCarrierQueueMirrorsBroadcast(t *testing.T) {
	i, _ := newQueueTestInteractive()

	if got := i.carrierQueuedCount(); got != 0 {
		t.Fatalf("fresh mirror = %d, want 0", got)
	}

	i.handleCarrierEvent(ctrlproto.QueueUpdatedEvent([]string{"one", "two"}))
	if got := i.carrierQueuedList(); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("mirror = %v, want [one two]", got)
	}

	// An empty queue serializes with Queued absent; the event type is the
	// signal, so the mirror must clear rather than keep the stale list.
	i.handleCarrierEvent(ctrlproto.QueueUpdatedEvent(nil))
	if got := i.carrierQueuedCount(); got != 0 {
		t.Fatalf("mirror after clear = %d, want 0", got)
	}
}

// alt+up peels the newest queued prompt back into the editor: the mirror
// supplies the text, and the shortened list is committed through SetQueue.
func TestCarrierQueuePopCommitsShortenedList(t *testing.T) {
	i, fc := newQueueTestInteractive()
	i.handleCarrierEvent(ctrlproto.QueueUpdatedEvent([]string{"a", "b", "c"}))

	text, ok := i.popCarrierQueue()
	if !ok || text != "c" {
		t.Fatalf("pop = %q,%v; want c,true", text, ok)
	}
	if got := fc.lastSetQueue(t); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("SetQueue got %v, want [a b]", got)
	}

	// Nothing to pop on an empty queue, and no service call for it.
	i.handleCarrierEvent(ctrlproto.QueueUpdatedEvent(nil))
	if _, ok := i.popCarrierQueue(); ok {
		t.Fatal("popped from an empty queue")
	}
}

// ctrl+c on an idle session clears the queue through the service, and is a
// no-op when there is nothing queued (no needless round-trip).
func TestCarrierQueueClear(t *testing.T) {
	i, fc := newQueueTestInteractive()

	i.clearCarrierQueue()
	fc.mu.Lock()
	n := len(fc.setQueues)
	fc.mu.Unlock()
	if n != 0 {
		t.Fatalf("clear on an empty queue made %d SetQueue calls, want 0", n)
	}

	i.handleCarrierEvent(ctrlproto.QueueUpdatedEvent([]string{"x"}))
	i.clearCarrierQueue()
	if got := fc.lastSetQueue(t); len(got) != 0 {
		t.Fatalf("SetQueue got %v, want empty", got)
	}
}

// A session switch drops the old session's queue with its transcript: a beat
// of empty beats a beat of another session's pending prompts.
func TestCarrierQueueClearedOnSnapshot(t *testing.T) {
	i, _ := newQueueTestInteractive()
	i.handleCarrierEvent(ctrlproto.QueueUpdatedEvent([]string{"stale"}))

	i.handleCarrierEvent(ctrlproto.SnapshotEvent(ctrlproto.Snapshot{Queued: []string{"fresh"}}))
	if got := i.carrierQueuedList(); !reflect.DeepEqual(got, []string{"fresh"}) {
		t.Fatalf("mirror = %v, want [fresh] (the snapshot is authoritative)", got)
	}
}
