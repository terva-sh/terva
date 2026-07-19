package workspace

import (
	"context"
	"os"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func draftWorkspace(t *testing.T) *Workspace {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func listHasSession(sessions []ctrlproto.SessionInfo, id string) bool {
	for _, s := range sessions {
		if s.ID == id {
			return true
		}
	}
	return false
}

// A fresh Stage session is a DRAFT: its greeting is deferred (in memory only), so
// it is not listed — previewing a character does not clutter the session list.
// The first real turn flushes the greeting and the session appears.
func TestDraftSessionNotListedUntilFirstTurn(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()

	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Ivy","first_mes":"Hi there."}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}

	// The character greets the user live (the greeting is seeded in memory)...
	live := w.live(info.ID)
	if live == nil {
		t.Fatal("session is not live")
	}
	if got := reviseTexts(live.agent.Messages()); len(got) != 1 || got[0] != "Hi there." {
		t.Errorf("greeting not seeded live: %v", got)
	}
	if !live.sess.HasPendingGreeting() {
		t.Error("a fresh immersive session should carry a deferred (pending) greeting")
	}
	// ...but it is a draft: absent from the list.
	sessions, _ := w.Sessions(ctx)
	if listHasSession(sessions, info.ID) {
		t.Error("a greeting-only draft must not appear in the session list")
	}

	// The first real turn's user message flushes the greeting → promoted.
	if err := live.sess.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	if live.sess.HasPendingGreeting() {
		t.Error("the greeting should have flushed on the first append")
	}
	sessions, _ = w.Sessions(ctx)
	if !listHasSession(sessions, info.ID) {
		t.Error("a session with a real turn must appear in the list")
	}
}

// Changing the user persona name BEFORE the first turn re-seeds the deferred
// greeting with the new {{user}} — the fresh chat's opening reflects the persona
// instead of staying frozen on the default (rough-edge #6). It does not promote
// the draft.
func TestUserBindReseedsDeferredGreeting(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()

	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Ivy","first_mes":"Hello {{user}}, welcome."}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)

	// Before any bind the greeting resolves {{user}} to the default ("User").
	if got := reviseTexts(live.agent.Messages()); len(got) != 1 || !strings.Contains(got[0], "User") {
		t.Fatalf("default greeting = %v, want it to contain User", got)
	}

	if err := w.UserBind(ctx, info.ID, ctrlproto.UserBindParams{Name: "Aria"}); err != nil {
		t.Fatal(err)
	}
	if got := reviseTexts(live.agent.Messages()); len(got) != 1 || !strings.Contains(got[0], "Aria") {
		t.Errorf("greeting after user.bind = %v, want it to contain Aria (re-seeded)", got)
	}
	if !live.sess.HasPendingGreeting() {
		t.Error("user.bind before the first turn must not promote the draft")
	}
	// Still a draft — not listed.
	sessions, _ := w.Sessions(ctx)
	if listHasSession(sessions, info.ID) {
		t.Error("a re-seeded draft is still a draft and must not be listed")
	}
}

// DiscardDraft reclaims an unpromoted draft (the navigate-away cleanup) but is a
// guarded no-op on a promoted session, so it can never discard real work.
func TestDiscardDraft(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Ivy","first_mes":"Hi."}`)})
	if err != nil {
		t.Fatal(err)
	}

	// A draft is discarded: its live session and file are gone.
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	if w.live(info.ID) == nil {
		t.Fatal("draft not live")
	}
	if err := w.DiscardDraft(ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	if w.live(info.ID) != nil {
		t.Error("a discarded draft should no longer be live")
	}
	if _, err := os.Stat(w.sessionPath(info.ID)); !os.IsNotExist(err) {
		t.Errorf("a discarded draft's file should be gone: %v", err)
	}

	// A promoted session (had a real turn) is NOT discarded.
	info2, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	live2 := w.live(info2.ID)
	if err := live2.sess.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if err := w.DiscardDraft(ctx, info2.ID); err != nil {
		t.Fatal(err)
	}
	if w.live(info2.ID) == nil {
		t.Error("DiscardDraft must not discard a promoted session")
	}
}

// A coding session (no experience) has no greeting and no deferral: it lists and
// prunes exactly as before, unaffected by the draft machinery.
func TestCodingSessionUnaffectedByDraftDeferral(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()

	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)
	if live.sess.HasPendingGreeting() {
		t.Error("a coding session must never carry a deferred greeting")
	}
	if got := reviseTexts(live.agent.Messages()); len(got) != 0 {
		t.Errorf("a coding session opens with no messages, got %v", got)
	}
}
