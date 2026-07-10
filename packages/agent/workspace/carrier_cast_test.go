package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// TestCarrierPlayInjectsActorSpawn: a --play session with a declared cast gets
// the actor_spawn tool injected daemon-side (buildSession → injectExtraTools),
// so the model can actually call it — Resolve already advertises it via the cast
// addendum, and without the injection --play --cast promised a missing tool.
// A --play session with no cast (and a plain coding session) must not get it.
func TestCarrierPlayInjectsActorSpawn(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	newSession := func(t *testing.T, args build.Args) *core.Agent {
		w, err := NewWorkspace(args, "test")
		if err != nil {
			t.Fatalf("NewWorkspace: %v", err)
		}
		t.Cleanup(func() { w.Close() })
		info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		// White-box: reach the live session's agent directly (AgentFor is gone,
		// plan 4.1) to assert its resolved tool registry.
		s := w.existing(info.ID)
		if s == nil || s.agent == nil {
			t.Fatal("session did not materialize an agent")
		}
		return s.agent
	}

	t.Run("play with cast injects actor_spawn", func(t *testing.T) {
		ag := newSession(t, build.Args{
			Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t),
			NoExt: true, NoMCP: true,
			Experience: build.ExperiencePlay,
			Cast:       map[string]string{"guide": "mieli"},
		})
		if _, ok := ag.LookupTool("actor_spawn"); !ok {
			t.Fatal("--play --cast session is missing the actor_spawn tool")
		}
	})

	t.Run("play without cast has no actor_spawn", func(t *testing.T) {
		ag := newSession(t, build.Args{
			Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t),
			NoExt: true, NoMCP: true, Experience: build.ExperiencePlay,
		})
		if _, ok := ag.LookupTool("actor_spawn"); ok {
			t.Error("--play without a cast should not inject actor_spawn")
		}
	})

	t.Run("coding session has no actor_spawn", func(t *testing.T) {
		ag := newSession(t, build.Args{
			Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t),
			NoExt: true, NoMCP: true,
		})
		if _, ok := ag.LookupTool("actor_spawn"); ok {
			t.Error("a plain coding session should not inject actor_spawn")
		}
	})
}
