package workspace

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// TestWorkspaceNoteSet drives note.set end to end on a live immersive session:
// it writes the meta, surfaces on SessionInfo, updates the live record, and lands
// in the uncached per-turn tail — then clears cleanly. A coding session, which
// carries no note record, is a bad request.
func TestWorkspaceNoteSet(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatal(err)
	}

	const note = "Stay in character. It is raining hard."
	if err := w.NoteSet(ctx, info.ID, ctrlproto.NoteSetParams{Text: note}); err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)
	if live.sess.Meta.Note != note {
		t.Errorf("meta note = %q, want %q", live.sess.Meta.Note, note)
	}
	if live.info().Note != note {
		t.Errorf("SessionInfo.Note = %q, want %q", live.info().Note, note)
	}
	if live.note.Get() != note {
		t.Errorf("live record = %q, want %q", live.note.Get(), note)
	}
	// The live per-turn tail injects the note.
	if cp := live.agent.ContextProvider; cp == nil || !strings.Contains(cp(), note) {
		t.Error("author's note is not in the per-turn tail")
	}

	// Clearing (whitespace-only trims to empty) empties everything and drops it
	// from the tail.
	if err := w.NoteSet(ctx, info.ID, ctrlproto.NoteSetParams{Text: "  "}); err != nil {
		t.Fatal(err)
	}
	if live.sess.Meta.Note != "" || live.note.Get() != "" {
		t.Errorf("note not cleared: meta=%q record=%q", live.sess.Meta.Note, live.note.Get())
	}
	if cp := live.agent.ContextProvider; cp != nil && strings.Contains(cp(), "raining") {
		t.Error("cleared note still in the tail")
	}

	// A coding (non-immersive) session carries no note record -> bad request.
	coding, err := w.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.NoteSet(ctx, coding.ID, ctrlproto.NoteSetParams{Text: "x"}); err == nil {
		t.Error("note.set on a coding session should be a bad request")
	}
}

// TestNoteReseedsOnResume proves restart durability: a note set on one workspace
// is re-seeded into the live record — and back into the per-turn tail — when a
// fresh workspace resumes the session from disk (the honest daemon-restart sim).
func TestNoteReseedsOnResume(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	cwd := testsupport.TempDir(t)
	w1, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	info, err := w1.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	const note = "The pass is snowbound after dusk."
	if err := w1.NoteSet(context.Background(), info.ID, ctrlproto.NoteSetParams{Text: note}); err != nil {
		t.Fatal(err)
	}
	// A note is a meta row, not a message, so seed one message to keep the session
	// from being pruned on close.
	if err := w1.live(info.ID).sess.AppendMessage(swipeMsg(provider.RoleUser, "hello")); err != nil {
		t.Fatal(err)
	}
	w1.Close()

	w2, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	if _, err := w2.ResumeSession(context.Background(), info.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	live := w2.live(info.ID)
	if live == nil {
		t.Fatal("resumed session is not live")
	}
	if live.note == nil || live.note.Get() != note {
		t.Errorf("note not reseeded from meta on restart: %v", live.note)
	}
	if cp := live.agent.ContextProvider; cp == nil || !strings.Contains(cp(), note) {
		t.Error("reseeded note not injected into the tail after restart")
	}
}
