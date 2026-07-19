package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// TestCreateSessionPersistsImmersiveSpec proves the Phase-0 wire→meta path: a
// session created with an experience and a persona reports them on its
// SessionInfo AND persists them to session meta, so a fresh loader (a daemon
// restart) recovers the spec rather than falling back to the workspace default —
// which also fixes the pre-existing per-session-persona-not-persisted gap.
func TestCreateSessionPersistsImmersiveSpec(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{
		Experience: "play",
		Persona:    "kertoja",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if info.Experience != "play" {
		t.Errorf("SessionInfo.Experience = %q, want %q", info.Experience, "play")
	}

	// Reopen from disk — the honest simulation of a daemon restart.
	s, _, err := core.OpenSession(info.Path)
	if err != nil {
		t.Fatalf("reopen %s: %v", info.Path, err)
	}
	defer s.Close()
	if s.Meta.Experience != "play" || s.Meta.Persona != "kertoja" {
		t.Errorf("persisted meta = {experience:%q persona:%q}, want {play kertoja}",
			s.Meta.Experience, s.Meta.Persona)
	}

	// An unknown experience is a clean bad-request, not a silent default.
	if _, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "bogus"}); err == nil {
		t.Error("create with unknown experience: want error, got nil")
	}
}
