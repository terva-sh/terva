package chat

import (
	"os"
	"path/filepath"
	"strings"
	"terva.sh/terva/packages/testsupport"
	"testing"
	"time"
)

// TestLoopEditedReplacesQueued: an edit that lands before the turn
// rewrites the queued prompt in place; one that lands after becomes a
// note on the chat's next prompt.
func TestLoopEditedReplacesQueued(t *testing.T) {
	l := &Loop{Info: func(string) {}, Warn: func(string) {}}
	l.mu.Lock()
	l.queue = []Message{{ID: "m2", ChatID: "c1", Text: "old text"}}
	l.mu.Unlock()

	l.onMessageEdited(MessageEdited{ChatID: "c1", ID: "m2", Text: "new text",
		Entities: []Entity{{Kind: "code", Offset: 0, Length: 3}}})
	l.mu.Lock()
	q := l.queue[0]
	l.mu.Unlock()
	if q.Text != "new text" || len(q.Entities) != 1 {
		t.Errorf("queued message not replaced: %+v", q)
	}
	if notes := l.takeNotes("c1"); len(notes) != 0 {
		t.Errorf("in-queue edit must not leave a note: %v", notes)
	}

	// Too late: the message is no longer queued.
	l.onMessageEdited(MessageEdited{ChatID: "c1", ID: "m1", Text: "even newer"})
	p := l.promptText(Message{ChatID: "c1", Text: "next prompt"})
	if !strings.Contains(p, "edited an earlier message") || !strings.Contains(p, "even newer") ||
		!strings.HasSuffix(p, "next prompt") {
		t.Errorf("prompt = %q", p)
	}
	// Notes drain once.
	if p2 := l.promptText(Message{ChatID: "c1", Text: "again"}); strings.Contains(p2, "edited") {
		t.Errorf("note leaked into a second prompt: %q", p2)
	}
}

// TestLoopDeletedDropsQueued: deletion withdraws a queued prompt and
// its staged files; consumed messages are left alone.
func TestLoopDeletedDropsQueued(t *testing.T) {
	dir := testsupport.TempDir(t)
	staged := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(staged, []byte("PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := &Loop{Info: func(string) {}, Warn: func(string) {}}
	l.mu.Lock()
	l.queue = []Message{
		{ID: "m2", ChatID: "c1", Text: "delete me", Files: []FileAttachment{{Path: staged}}},
		{ID: "m3", ChatID: "c1", Text: "keep me"},
	}
	l.mu.Unlock()

	l.onMessageDeleted(MessageDeleted{ChatID: "c1", ID: "m2"})
	l.mu.Lock()
	n, first := len(l.queue), l.queue[0].ID
	l.mu.Unlock()
	if n != 1 || first != "m3" {
		t.Errorf("queue after delete = %d, first %s", n, first)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Error("staged file should have been cleaned with the withdrawal")
	}
	// Unknown id: nothing happens.
	l.onMessageDeleted(MessageDeleted{ChatID: "c1", ID: "m99"})
}

// TestLoopReactionNotes: reactions on the bot's own messages become
// notes; everything else is ignored (the lossy-channel doctrine).
func TestLoopReactionNotes(t *testing.T) {
	l := &Loop{Info: func(string) {}, Warn: func(string) {}}
	l.onReaction(Reaction{ChatID: "c1", MessageID: "m-90", UserID: "u1", Username: "drew",
		Key: "👍", OwnMessage: true})
	l.onReaction(Reaction{ChatID: "c1", MessageID: "m-77", UserID: "u1", Key: "🔥"}) // not ours
	p := l.promptText(Message{ChatID: "c1", Text: "hi"})
	if !strings.Contains(p, "@drew") || !strings.Contains(p, "👍") {
		t.Errorf("prompt = %q", p)
	}
	if strings.Contains(p, "🔥") {
		t.Errorf("foreign reaction leaked: %q", p)
	}
	// Removal reads differently.
	l.onReaction(Reaction{ChatID: "c1", MessageID: "m-90", UserID: "u1", Username: "drew",
		Key: "👍", Removed: true, OwnMessage: true})
	if p := l.promptText(Message{ChatID: "c1", Text: "x"}); !strings.Contains(p, "removed their reaction") {
		t.Errorf("removal prompt = %q", p)
	}
}

// TestLoopFileManifestAndCleanup: staged files ride the prompt as a
// manifest and are cleaned after the turn.
func TestLoopFileManifestAndCleanup(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	l := startLoop(t, conn, &scriptedClient{reply: "ok"}, pairedWith("7"))

	dir := filepath.Join(testsupport.TempDir(t), "incoming", "m5")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "note.ogg")
	if err := os.WriteFile(staged, []byte("OGG"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := msgFrom("7", "listen to this")
	m.Files = []FileAttachment{{Path: staged, Kind: "voice", MimeType: "audio/ogg",
		Size: 3, Duration: 4200 * time.Millisecond}}
	// The manifest the agent would see:
	p := l.promptText(m)
	if !strings.Contains(p, staged) || !strings.Contains(p, "voice") || !strings.Contains(p, "4.2s") {
		t.Errorf("manifest prompt = %q", p)
	}

	conn.inbound <- m
	conn.waitSends(t, 1)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(staged); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("staged file not cleaned after the turn")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("empty per-message dir should be removed")
	}
}

// TestLoopNotesAreTypedAndDeduped: notes carry a bracketed event type
// (the model must read them as connector state, not user text), no-op
// edits (Discord embed unfurls re-deliver unchanged content) produce
// no note, and re-delivered identical edits coalesce to one.
func TestLoopNotesAreTypedAndDeduped(t *testing.T) {
	l := &Loop{Info: func(string) {}, Warn: func(string) {}}
	// Consume the one-time chat intro so note assertions stand alone.
	_ = l.promptText(Message{ChatID: "c1", Text: "hi"})

	// The consumed message m1, as drainQueue would have recorded it.
	l.mu.Lock()
	l.recordMsgTextLocked("c1", "m1", "look at https://example.com")
	l.mu.Unlock()

	// Unfurl echo: content unchanged, no note.
	l.onMessageEdited(MessageEdited{ChatID: "c1", ID: "m1", Text: "look at https://example.com"})
	if notes := l.takeNotes("c1"); len(notes) != 0 {
		t.Fatalf("unchanged edit produced a note: %v", notes)
	}
	// A real edit notes once; the same edit re-delivered doesn't stack.
	l.onMessageEdited(MessageEdited{ChatID: "c1", ID: "m1", Text: "look at example.org instead"})
	l.onMessageEdited(MessageEdited{ChatID: "c1", ID: "m1", Text: "look at example.org instead"})
	notes := l.takeNotes("c1")
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "[chat event: message_edited] ") {
		t.Fatalf("notes = %v", notes)
	}

	// Reactions are typed the same way.
	l.onReaction(Reaction{ChatID: "c1", OwnMessage: true, Username: "drew", Key: "👍"})
	notes = l.takeNotes("c1")
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "[chat event: reaction] ") {
		t.Fatalf("reaction notes = %v", notes)
	}
}

// TestLoopChatIntroAndAttribution: each chat's first prompt carries the
// [chat context] line (where the conversation lives + formatting
// guidance); multi-user chats attribute every message, the DM never.
func TestLoopChatIntroAndAttribution(t *testing.T) {
	l := &Loop{Service: "discord", Info: func(string) {}, Warn: func(string) {}}

	p := l.promptText(Message{ChatID: "g1", ChatKind: "group", ChatTitle: "ops", Username: "drew", Text: "hello"})
	for _, want := range []string{"[chat context]", "discord", `"ops"`, "tables", "@drew: hello"} {
		if !strings.Contains(p, want) {
			t.Errorf("first group prompt missing %q:\n%s", want, p)
		}
	}
	p2 := l.promptText(Message{ChatID: "g1", ChatKind: "group", Username: "sam", Text: "yo"})
	if strings.Contains(p2, "[chat context]") {
		t.Errorf("intro repeated: %q", p2)
	}
	if !strings.Contains(p2, "@sam: yo") {
		t.Errorf("attribution missing: %q", p2)
	}

	pd := l.promptText(Message{ChatID: "d1", ChatKind: "dm", Username: "drew", Text: "hi"})
	if !strings.Contains(pd, "[chat context]") || !strings.Contains(pd, "direct chat") {
		t.Errorf("dm intro wrong: %q", pd)
	}
	if strings.Contains(pd, "@drew:") {
		t.Errorf("dm prompt must not be attributed: %q", pd)
	}
}
