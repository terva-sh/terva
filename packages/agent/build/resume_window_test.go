package build

import (
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/provider"
)

func userMsg(text string) provider.Message {
	return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

func callMsg(id string) provider.Message {
	return provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
		provider.ToolCallBlock{ID: id, Name: "read", Arguments: json.RawMessage(`{}`)},
	}}
}

func resultMsg(callID string) provider.Message {
	return provider.Message{Role: provider.RoleTool, Content: []provider.Content{
		provider.ToolResultBlock{CallID: callID, Content: []provider.Content{provider.TextBlock{Text: "ok"}}},
	}}
}

func compactionMsg() provider.Message {
	m := userMsg("## Context Summary (compacted)")
	m.Meta = map[string]string{"compaction": "true"}
	return m
}

// The caller used to RECONSTRUCT this function's contract by subtracting
// lengths: base := len(full) - len(trimmed), which assumes every message removed
// came off the FRONT. TrimMessagesForResume now states what it did, because that
// assumption is false and the untrimmed path is the clearest proof.
func TestAnUntrimmedWindowIsTheIdentityMapping(t *testing.T) {
	msgs := []provider.Message{userMsg("a"), userMsg("b"), userMsg("c")}
	win := TrimMessagesForResume(msgs, 100)

	if len(win.Messages) != 3 {
		t.Fatalf("kept %d messages, want 3", len(win.Messages))
	}
	if win.Base != 0 || win.Head || win.Inexact {
		t.Errorf("an untrimmed transcript is not the identity mapping: %+v", win)
	}
}

// The live bug, on the path nobody expected to move anything.
//
// A user deletes the assistant message that carried a tool_use; core's loader
// stubs missing tool_RESULTS but never drops orphans, so the orphaned result
// survives on disk. On the next resume of that session — under a hundred
// messages, so the trim's early return — RepairOrphanedToolResults removes ONE
// row from the middle. The old subtraction read that as base=1, and from then on
// every delete and edit persisted its amend one index too far, rewriting a
// message the user never touched on the next reload.
func TestAnOrphanRemovedFromInsideAnUntrimmedWindowIsNotReadAsALeadingDrop(t *testing.T) {
	msgs := []provider.Message{
		userMsg("a"),
		resultMsg("t-deleted"), // its tool_use was deleted; nothing pairs with it
		userMsg("b"),
		userMsg("c"),
	}
	win := TrimMessagesForResume(msgs, 100)

	if len(win.Messages) != 3 {
		t.Fatalf("the repair should have removed the orphan: kept %d of 4", len(win.Messages))
	}
	// The length delta is 1. A caller subtracting lengths would call that base=1
	// and shift every subsequent amend by one.
	if win.Base != 0 {
		t.Errorf("Base = %d, want 0 — nothing was dropped from the FRONT", win.Base)
	}
	if !win.Inexact {
		t.Error("Inexact is false, so a caller will map indices with an offset that does not describe " +
			"this window — the amend lands on a message the user never touched")
	}
}

// The ordinary trim: a leading prefix drops, and Base is exactly that prefix.
func TestATrimmedWindowReportsTheLeadingCountItActuallySkipped(t *testing.T) {
	var msgs []provider.Message
	for i := 0; i < 10; i++ {
		msgs = append(msgs, userMsg(string(rune('a'+i))))
	}
	win := TrimMessagesForResume(msgs, 4)

	if len(win.Messages) != 4 {
		t.Fatalf("kept %d, want 4", len(win.Messages))
	}
	if win.Base != 6 {
		t.Errorf("Base = %d, want 6", win.Base)
	}
	if win.Head {
		t.Error("Head is set on a transcript with no compaction summary")
	}
	if win.Inexact {
		t.Error("a clean leading trim is exactly describable by an offset")
	}
	// And the mapping actually holds: in-memory 0 is on-disk 6.
	if got, want := win.Messages[0], msgs[6]; got.Content[0].(provider.TextBlock).Text != want.Content[0].(provider.TextBlock).Text {
		t.Errorf("in-memory index 0 is not on-disk index %d", win.Base)
	}
}

// The compaction summary is carried over from index 0, so it is NOT part of the
// offset — which is exactly what Head marks, and why Base must not simply be a
// length delta here either.
func TestTheCompactionHeadIsMarkedAndExcludedFromTheOffset(t *testing.T) {
	msgs := []provider.Message{compactionMsg()}
	for i := 0; i < 9; i++ {
		msgs = append(msgs, userMsg(string(rune('a'+i))))
	}
	win := TrimMessagesForResume(msgs, 3)

	if !win.Head {
		t.Fatal("Head not set: an already-compacted session must stay compacted after resume")
	}
	if len(win.Messages) != 4 {
		t.Fatalf("kept %d, want the head plus 3", len(win.Messages))
	}
	if win.Base != 7 {
		t.Errorf("Base = %d, want 7 — the head is carried from index 0 and is not part of the offset", win.Base)
	}
	// The length delta here is 10-4 = 6, which is NOT the offset. That gap is
	// the whole reason the caller cannot compute this itself.
	if len(msgs)-len(win.Messages) == win.Base {
		t.Error("this fixture no longer distinguishes the length delta from the real offset")
	}
	if win.Inexact {
		t.Error("a leading trim with a carried head is still exactly describable")
	}
}

// A tail that begins with orphan tool_result rows is skipped past, so Base grows
// beyond len(msgs)-keepTail. Another case a length delta gets right only by
// coincidence — and does not, once the repair also fires.
func TestALeadingOrphanSkipIsCountedInTheOffset(t *testing.T) {
	msgs := []provider.Message{
		userMsg("a"), callMsg("t1"), resultMsg("t1"),
		resultMsg("t-orphan"), // first row of the would-be window
		userMsg("b"), userMsg("c"),
	}
	win := TrimMessagesForResume(msgs, 3)

	if win.Base != 4 {
		t.Errorf("Base = %d, want 4 — the window must start past the orphan row", win.Base)
	}
	if len(win.Messages) != 2 {
		t.Fatalf("kept %d, want 2", len(win.Messages))
	}
	if win.Inexact {
		t.Error("skipping leading orphans is a leading drop, which an offset describes exactly")
	}
}
