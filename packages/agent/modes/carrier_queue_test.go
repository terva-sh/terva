package modes

import (
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
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

	i.handleCarrierEvent(ctrlproto.QueueUpdatedEvent(typedQueue("one", "two")))
	if got := i.carrierQueuedList(); !reflect.DeepEqual(got, []queuedChip{{text: "one"}, {text: "two"}}) {
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
	i.handleCarrierEvent(ctrlproto.QueueUpdatedEvent(typedQueue("a", "b", "c")))

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

	i.handleCarrierEvent(ctrlproto.QueueUpdatedEvent(typedQueue("x")))
	i.clearCarrierQueue()
	if got := fc.lastSetQueue(t); len(got) != 0 {
		t.Fatalf("SetQueue got %v, want empty", got)
	}
}

// A session switch drops the old session's queue with its transcript: a beat
// of empty beats a beat of another session's pending prompts.
func TestCarrierQueueClearedOnSnapshot(t *testing.T) {
	i, _ := newQueueTestInteractive()
	i.handleCarrierEvent(ctrlproto.QueueUpdatedEvent(typedQueue("stale")))

	i.handleCarrierEvent(ctrlproto.SnapshotEvent(ctrlproto.Snapshot{Queued: []string{"fresh"}}))
	if got := i.carrierQueuedList(); !reflect.DeepEqual(got, []queuedChip{{text: "fresh"}}) {
		t.Fatalf("mirror = %v, want [fresh] (the snapshot is authoritative)", got)
	}
}

// The mirror keeps the writer of each entry, from the event and from the
// snapshot. alt+up takes the newest text that a person typed and leaves a
// text that terva queued in its place.
func TestCarrierQueueKeepsSourcesAndPopsOnlyTypedText(t *testing.T) {
	i, fc := newQueueTestInteractive()
	queue := []core.QueuedMessage{
		{Text: "typed first"},
		{Text: "typed second"},
		{Text: "the recap", Source: ctrlproto.QueueSourceSwarmRecap},
	}
	i.handleCarrierEvent(ctrlproto.QueueUpdatedEvent(queue))
	want := []queuedChip{
		{text: "typed first"},
		{text: "typed second"},
		{text: "the recap", source: ctrlproto.QueueSourceSwarmRecap},
	}
	if got := i.carrierQueuedList(); !reflect.DeepEqual(got, want) {
		t.Fatalf("mirror = %+v, want %+v", got, want)
	}

	text, ok := i.popCarrierQueue()
	if !ok || text != "typed second" {
		t.Fatalf("pop = %q,%v; want the newest typed text", text, ok)
	}
	if got := fc.lastSetQueue(t); !reflect.DeepEqual(got, []string{"typed first", "the recap"}) {
		t.Fatalf("SetQueue got %v, want the recap kept in its place", got)
	}
	// 🔑 The sources go back with the texts, so the daemon knows which
	// entries stay and need not guess from the texts.
	fc.mu.Lock()
	sources := fc.setQueueSources[len(fc.setQueueSources)-1]
	fc.mu.Unlock()
	if !reflect.DeepEqual(sources, []string{"", ctrlproto.QueueSourceSwarmRecap}) {
		t.Fatalf("SetQueue sources = %v, want the recap's source beside its text", sources)
	}

	// The snapshot carries the same two lists.
	texts, sources := ctrlproto.QueueView(queue[2:])
	i.handleCarrierEvent(ctrlproto.SnapshotEvent(ctrlproto.Snapshot{Queued: texts, QueuedSources: sources}))
	if got := i.carrierQueuedList(); !reflect.DeepEqual(got, want[2:]) {
		t.Fatalf("mirror after the snapshot = %+v, want %+v", got, want[2:])
	}
	// Only the recap waits now, and it is not the person's draft.
	if text, ok := i.popCarrierQueue(); ok {
		t.Fatalf("popped %q, a text that terva queued", text)
	}
}

// A list of sources of another length does not belong to the queue beside
// it. The mirror then reads every entry as typed, which is how an older
// daemon's queue reads.
func TestCarrierQueueIgnoresSourcesOfAnotherLength(t *testing.T) {
	i, _ := newQueueTestInteractive()
	i.setCarrierQueue([]string{"a", "b"}, []string{"swarm:recap"})
	if got := i.carrierQueuedList(); !reflect.DeepEqual(got, []queuedChip{{text: "a"}, {text: "b"}}) {
		t.Fatalf("mirror = %+v, want two typed entries", got)
	}
}

// packages/tui does not import ctrlproto, so it spells the two sources that
// terva gives a queued text. This fails when a constant moves away from the
// name the renderer knows.
func TestTheRendererNamesEverySourceTervaUses(t *testing.T) {
	for _, source := range []string{ctrlproto.QueueSourceSwarmRecap, ctrlproto.QueueSourceTalkootDelivery} {
		if tui.SourceLabel(source) == source {
			t.Errorf("the renderer has no name for the source %q", source)
		}
	}
}

// A chip for a text that terva queued names the writer. The hint for alt+up
// shows only when the key has a typed text to take.
func TestQueueChipsNameTheWriter(t *testing.T) {
	plain := stripANSI

	mixed := plain(queueChipRows(tui.Dark, []queuedChip{
		{text: "my follow-up"},
		{text: "the recap", source: ctrlproto.QueueSourceSwarmRecap},
	}, 100))
	if !strings.Contains(mixed, "sliding in: my follow-up") {
		t.Errorf("the typed chip changed:\n%s", mixed)
	}
	if !strings.Contains(mixed, "sliding in: (sub-agent report) the recap") {
		t.Errorf("the recap's chip does not name its writer:\n%s", mixed)
	}
	if !strings.Contains(mixed, "to slide back into input") {
		t.Errorf("the hint is gone while a typed text waits:\n%s", mixed)
	}

	onlyRecap := plain(queueChipRows(tui.Dark, []queuedChip{
		{text: "the recap", source: ctrlproto.QueueSourceSwarmRecap},
	}, 100))
	if strings.Contains(onlyRecap, "to slide back into input") {
		t.Errorf("the hint offers a key that takes nothing:\n%s", onlyRecap)
	}
	if rows := queueChipRows(tui.Dark, nil, 100); rows != nil {
		t.Errorf("an empty queue drew %d rows", len(rows))
	}
}

// typedQueue is a queue of texts that a person typed, which have no source.
func typedQueue(texts ...string) []core.QueuedMessage {
	out := make([]core.QueuedMessage, len(texts))
	for i, t := range texts {
		out[i] = core.QueuedMessage{Text: t}
	}
	return out
}
