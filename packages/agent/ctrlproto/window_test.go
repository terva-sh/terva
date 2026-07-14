package ctrlproto

import (
	"testing"

	"terva.sh/terva/packages/core"
)

func snapOf(n int) Event {
	msgs := make([]core.WireMessage, n)
	for i := range msgs {
		msgs[i] = core.WireMessage{Role: "user", Content: []core.WireBlock{
			{Type: "text", Text: string(rune('a' + i%26))},
		}}
	}
	return SnapshotEvent(Snapshot{Messages: msgs, Total: n, Epoch: 7})
}

// TestWindowSnapshotKeepsTheTail: a windowed client gets the END of the conversation
// — the part it is looking at — and the Base to place it by.
func TestWindowSnapshotKeepsTheTail(t *testing.T) {
	ev := windowSnapshot(snapOf(100), 30)

	if got := len(ev.Snapshot.Messages); got != 30 {
		t.Fatalf("window carried %d messages, want 30", got)
	}
	if ev.Snapshot.Base != 70 {
		t.Errorf("Base = %d, want 70 — the index the window starts at", ev.Snapshot.Base)
	}
	if ev.Snapshot.Total != 100 {
		t.Errorf("Total = %d, want 100 — the transcript's true length, not the window's", ev.Snapshot.Total)
	}
	// The tail, not the head: the last message of the transcript must be the last of
	// the window, or the client is reading an old part of the conversation as if it
	// were the live end of it.
	full := snapOf(100).Snapshot.Messages
	if ev.Snapshot.Messages[29].Content[0].Text != full[99].Content[0].Text {
		t.Error("the window is not anchored to the end of the transcript")
	}
}

// TestWindowSnapshotDoesNotMutateTheSharedEvent is the invariant every helper in
// strip.go is built around: ONE Event value is broadcast to every subscriber of a
// session, and each connection transforms it for its own contract. A helper that
// mutated in place would truncate the transcript for everyone else on that session —
// including the in-process TUI, which negotiated no window at all.
func TestWindowSnapshotDoesNotMutateTheSharedEvent(t *testing.T) {
	shared := snapOf(100)

	windowed := windowSnapshot(shared, 10)
	if len(windowed.Snapshot.Messages) != 10 {
		t.Fatalf("window carried %d, want 10", len(windowed.Snapshot.Messages))
	}
	if got := len(shared.Snapshot.Messages); got != 100 {
		t.Fatalf("the shared snapshot was truncated to %d — every other subscriber just lost its transcript", got)
	}
	if shared.Snapshot.Base != 0 {
		t.Errorf("the shared snapshot was stamped with Base %d", shared.Snapshot.Base)
	}
}

// A client that did not negotiate the feature passes limit 0 and must see exactly
// what it has always seen. That is the entire compatibility story: no flag day.
func TestWindowSnapshotIsOffByDefault(t *testing.T) {
	ev := windowSnapshot(snapOf(100), 0)
	if len(ev.Snapshot.Messages) != 100 || ev.Snapshot.Base != 0 {
		t.Fatalf("an un-negotiated client got a window: %d messages, base %d",
			len(ev.Snapshot.Messages), ev.Snapshot.Base)
	}
}

// A transcript shorter than the window is already the whole story: Base stays 0, and
// the client knows it has reached the top without asking.
func TestWindowSnapshotShorterThanTheWindow(t *testing.T) {
	ev := windowSnapshot(snapOf(5), 80)
	if len(ev.Snapshot.Messages) != 5 || ev.Snapshot.Base != 0 {
		t.Fatalf("short transcript windowed: %d messages, base %d",
			len(ev.Snapshot.Messages), ev.Snapshot.Base)
	}
}

// windowSnapshot must ignore anything that is not a snapshot — it runs on EVERY
// event on the connection's hot path.
func TestWindowSnapshotIgnoresOtherEvents(t *testing.T) {
	ev := ConversationEvent(core.WireEvent{Type: "text_delta", Delta: "hi"})
	if got := windowSnapshot(ev, 10); got.Snapshot != nil {
		t.Fatal("windowSnapshot invented a snapshot on a delta event")
	}
}
