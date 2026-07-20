package workspace

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// noteExec runs world_note with the given args against a session.
func noteExec(t *testing.T, s *wsSession, args string) core.ToolResult {
	t.Helper()
	res, err := (&worldNoteTool{s: s}).Execute(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("world_note: %v", err)
	}
	return res
}

func revealExec(t *testing.T, s *wsSession, args string) core.ToolResult {
	t.Helper()
	res, err := (&worldRevealTool{s: s}).Execute(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("world_reveal: %v", err)
	}
	return res
}

// world_note appends model-flagged entries (constant when key-less), refuses a
// name collision (append-only), and the live tail record follows.
func TestWorldNoteAppendsModelEntry(t *testing.T) {
	s := worldTestSession(t, &scriptedClient{}, nil)
	s.sess.Meta.Experience = "play" // the director's session; tail sees all
	s.worldLore = &build.WorldLoreRecord{}

	if r := noteExec(t, s, `{"name":"The betrayal","content":"The guildmaster sold them out.","audience":[" Elira ","elira"]}`); r.IsError {
		t.Fatalf("note should succeed: %+v", r)
	}
	got := s.sess.Meta.WorldLore
	if len(got) != 1 || !got[0].Model || !got[0].Constant || len(got[0].Audience) != 1 || got[0].Audience[0] != "Elira" {
		t.Fatalf("noted entry = %+v (want model, constant, deduped audience)", got)
	}
	// Keyed note: not constant.
	if r := noteExec(t, s, `{"name":"The pass","content":"Snowbound after dusk.","keys":["pass"]}`); r.IsError {
		t.Fatalf("keyed note should succeed: %+v", r)
	}
	if e := s.sess.Meta.WorldLore[1]; e.Constant || len(e.Keys) != 1 {
		t.Errorf("keyed note = %+v", e)
	}
	// Append-only: a collision (case-insensitive) is an error result, not an edit.
	if r := noteExec(t, s, `{"name":"the betrayal","content":"overwrite attempt"}`); !r.IsError {
		t.Error("a name collision must be refused (append-only)")
	}
	if len(s.sess.Meta.WorldLore) != 2 {
		t.Errorf("collision must not append: %d entries", len(s.sess.Meta.WorldLore))
	}
	// The live record followed (play: unfiltered).
	if rec := s.worldLore.Get(); len(rec) != 2 {
		t.Errorf("tail record should track notes, got %d", len(rec))
	}
}

// world_reveal moves a character onto a targeted entry's audience and stamps
// the learned-when ledger; world-shared and unknown entries are refused.
func TestWorldRevealLearns(t *testing.T) {
	s := worldTestSession(t, &scriptedClient{}, nil)
	s.sess.Meta.Experience = "play"
	s.worldLore = &build.WorldLoreRecord{}
	s.sess.Meta.WorldLore = []core.WorldLoreEntry{
		{Name: "shared", Constant: true, Content: "x"},
		{Name: "secret", Constant: true, Content: "y", Audience: []string{"Elira"}},
	}

	if r := revealExec(t, s, `{"entry":"secret","character":"Rook"}`); r.IsError {
		t.Fatalf("reveal should succeed: %+v", r)
	}
	e := s.sess.Meta.WorldLore[1]
	if len(e.Audience) != 2 || e.Audience[1] != "Rook" {
		t.Fatalf("Rook should join the audience: %+v", e)
	}
	if e.Learned["Rook"] == "" {
		t.Error("the learned-when ledger should stamp Rook")
	}
	if e.Learned["Elira"] != "" {
		t.Error("Elira knew from the start — no ledger stamp")
	}
	// Idempotent: already-known is a benign OK, not a duplicate.
	if r := revealExec(t, s, `{"entry":"secret","character":"rook"}`); r.IsError {
		t.Errorf("already-known should be benign: %+v", r)
	}
	if e := s.sess.Meta.WorldLore[1]; len(e.Audience) != 2 {
		t.Errorf("no duplicate audience: %+v", e.Audience)
	}
	// World-shared: nothing to reveal.
	if r := revealExec(t, s, `{"entry":"shared","character":"Rook"}`); !r.IsError {
		t.Error("revealing world-shared knowledge should be refused")
	}
	// Unknown entry: error names the existing entries.
	if r := revealExec(t, s, `{"entry":"ghost","character":"Rook"}`); !r.IsError {
		t.Error("an unknown entry should be refused")
	}
}

// A user edit through world.lore.put preserves the learned ledger (provenance)
// but takes ownership (drops the Model flag).
func TestWorldLorePutPreservesLedger(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Elira","first_mes":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)
	if err := live.setWorldLore([]core.WorldLoreEntry{{
		Name: "secret", Constant: true, Content: "y", Model: true,
		Audience: []string{"Elira", "Rook"}, Learned: map[string]string{"Rook": "2026-07-19T00:00:00Z"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := w.WorldLorePut(ctx, info.ID, ctrlproto.WorldLorePutParams{
		Entry: ctrlproto.WorldLoreEntry{Name: "secret", Constant: true, Content: "y — edited", Audience: []string{"Elira", "Rook"}},
	}); err != nil {
		t.Fatal(err)
	}
	e := live.sess.Meta.WorldLore[0]
	if e.Learned["Rook"] != "2026-07-19T00:00:00Z" {
		t.Errorf("the ledger must survive a user edit, got %v", e.Learned)
	}
	if e.Model {
		t.Error("a user edit takes ownership — Model should clear")
	}
	if e.Content != "y — edited" {
		t.Errorf("edit content = %q", e.Content)
	}
}

// The World tools are the play director's — a chat session's registry must not
// carry them (chat is pure conversation), a play session's must.
func TestWorldToolsPlayOnly(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Elira","first_mes":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	chat, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	if tools := w.live(chat.ID).agent.Tools; tools["world_note"] != nil || tools["world_reveal"] != nil {
		t.Error("chat is pure conversation — no World tools")
	}
	play, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "play", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	if tools := w.live(play.ID).agent.Tools; tools["world_note"] == nil || tools["world_reveal"] == nil {
		t.Error("the play director should hold world_note + world_reveal")
	}
}

// The duplicate-knowledge steer: live play showed the director re-recording an
// already-covered fact under a fresh name (a confessed secret) instead of
// revealing the existing entry — only the exact-name collision path pointed at
// world_reveal. The description must carry the redirect, since a semantic
// duplicate never trips the name guard.
func TestWorldNoteDescriptionRedirectsToReveal(t *testing.T) {
	desc := (&worldNoteTool{}).Description()
	if !strings.Contains(desc, "world_reveal") {
		t.Fatalf("world_note's description must steer semantic duplicates to world_reveal:\n%s", desc)
	}
	if !strings.Contains(desc, "already covers") {
		t.Errorf("the redirect should name the failure mode (an entry that already covers the fact):\n%s", desc)
	}
}
