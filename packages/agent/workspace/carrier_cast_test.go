package workspace

import (
	"context"
	"reflect"
	"strings"
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

// TestSessionInfoCarriesCast: a play session's cast (from CreateOpts →
// SessionMeta.Cast) is surfaced on SessionInfo, so the Stage drawer can show who
// the director can bring on stage. A solo chat carries no cast.
func TestSessionInfoCarriesCast(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	cast := map[string]string{"guide": "mieli", "rival": "kertoja"}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: build.ExperiencePlay, Cast: cast})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(info.Cast, cast) {
		t.Fatalf("play session info.Cast = %v, want %v", info.Cast, cast)
	}
	// It survives a cold reload through the live info() builder.
	reopened, err := w.ResumeSession(ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reopened.Cast, cast) {
		t.Errorf("reopened info.Cast = %v, want %v", reopened.Cast, cast)
	}

	solo, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: build.ExperienceChat})
	if err != nil {
		t.Fatal(err)
	}
	if len(solo.Cast) != 0 {
		t.Errorf("a solo chat should carry no cast, got %v", solo.Cast)
	}
}

// TestCastMutation drives cast.add / cast.remove: a play session's ensemble
// grows and shrinks mid-scene, the change surfaces on SessionInfo and rebuilds
// actor_spawn's roster, a bad ref is refused without side effects, and a chat
// session has no cast to edit.
func TestCastMutation(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := context.Background()

	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: build.ExperiencePlay, Cast: map[string]string{"guide": "mieli"}})
	if err != nil {
		t.Fatal(err)
	}
	s := w.existing(info.ID)
	if s == nil {
		t.Fatal("play session did not materialize")
	}

	// Add a second actor → persisted onto SessionInfo and actor_spawn rebuilt.
	if err := w.CastAdd(ctx, info.ID, ctrlproto.CastMemberParams{Name: "sage", Ref: "kertoja"}); err != nil {
		t.Fatalf("cast.add: %v", err)
	}
	if got := s.info().Cast; len(got) != 2 || got["sage"] != "kertoja" {
		t.Fatalf("after add, cast = %v, want guide+sage", got)
	}
	if len(s.actorCast) != 2 {
		t.Errorf("actor_spawn cast not rebuilt: %v", s.actorCast)
	}

	// An unresolvable ref is refused and changes nothing.
	if err := w.CastAdd(ctx, info.ID, ctrlproto.CastMemberParams{Name: "ghost", Ref: "no-such-persona-xyz"}); err == nil {
		t.Error("cast.add with an unresolvable ref should error")
	}
	if len(s.info().Cast) != 2 {
		t.Errorf("a rejected add must not change the cast: %v", s.info().Cast)
	}

	// Remove one → gone from the roster and the built cast.
	if err := w.CastRemove(ctx, info.ID, ctrlproto.CastMemberParams{Name: "guide"}); err != nil {
		t.Fatalf("cast.remove: %v", err)
	}
	if got := s.info().Cast; len(got) != 1 || got["guide"] != "" {
		t.Fatalf("after remove, cast = %v, want just sage", got)
	}
	if len(s.actorCast) != 1 {
		t.Errorf("actor_spawn cast not rebuilt on remove: %v", s.actorCast)
	}
	// Removing an unknown member is not found.
	if err := w.CastRemove(ctx, info.ID, ctrlproto.CastMemberParams{Name: "nobody"}); err == nil {
		t.Error("removing an unknown cast member should error")
	}

	// A chat session has no cast to edit.
	chat, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: build.ExperienceChat})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.CastAdd(ctx, chat.ID, ctrlproto.CastMemberParams{Name: "x", Ref: "mieli"}); err == nil {
		t.Error("cast.add on a chat session should be refused")
	}
}

// TestCastSpeak: the user-directs move refuses an empty/unknown actor and a
// non-play session before starting anything, and a valid actor on a play session
// starts a directed turn naming that actor.
func TestCastSpeak(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, cl)
	s.args.Cast = map[string]string{"guide": "mieli"}
	sub := s.hub.add(nil, true)

	// Refused cases start no turn.
	s.sess.Meta.Experience = build.ExperiencePlay
	if err := s.speak(""); err == nil {
		t.Error("empty actor should error")
	}
	if err := s.speak("ghost"); err == nil {
		t.Error("an actor not in the cast should error")
	}
	s.sess.Meta.Experience = build.ExperienceChat
	if err := s.speak("guide"); err == nil {
		t.Error("cast.speak on a chat session should error")
	}
	if s.busy() {
		t.Fatal("a refused cast.speak must not start a turn")
	}

	// A valid actor on a play session starts a directed turn naming the actor.
	s.sess.Meta.Experience = build.ExperiencePlay
	if err := s.speak("guide"); err != nil {
		t.Fatalf("speak: %v", err)
	}
	<-cl.started
	if !s.busy() {
		t.Error("cast.speak should have started a turn")
	}
	if got := reviseTexts(s.agent.Messages()); len(got) == 0 || !strings.Contains(got[len(got)-1], "guide") {
		t.Errorf("directive did not name the actor: %v", got)
	}
	close(cl.release)
	drainUntil(t, sub, "done")
	waitIdle(t, s)
}
