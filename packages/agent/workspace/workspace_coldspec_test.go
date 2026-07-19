package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// TestColdSessionCarriesImmersiveSpec — a session created immersive (experience +
// card) still reports those in the sessions list after the daemon forgets it
// (a fresh workspace reads the spec from meta on the disk-scan path), so the
// Stage library can badge and group a character's chats without waking them.
func TestColdSessionCarriesImmersiveSpec(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")

	w1, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	card, err := w1.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Iris"}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w1.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: card.ID})
	if err != nil {
		t.Fatal(err)
	}
	// A greeting-only immersive session is empty until something is said, and an
	// empty session is pruned on Close — seed a message so it survives the restart.
	if err := w1.live(info.ID).sess.AppendMessage(swipeMsg(provider.RoleAssistant, "Hello, traveler.")); err != nil {
		t.Fatal(err)
	}
	w1.Close()

	// A fresh workspace over the same home/cwd sees the session only via the disk
	// scan (cold — never materialized this run).
	w2, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()

	sessions, err := w2.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got *ctrlproto.SessionInfo
	for i := range sessions {
		if sessions[i].ID == info.ID {
			got = &sessions[i]
		}
	}
	if got == nil {
		t.Fatal("the created session is missing from the cold list")
	}
	if got.Live {
		t.Fatal("session should be cold (not materialized) in the fresh workspace")
	}
	if got.Experience != "chat" {
		t.Errorf("cold experience = %q, want chat", got.Experience)
	}
	if got.Card != card.ID {
		t.Errorf("cold card = %q, want %q", got.Card, card.ID)
	}
}
