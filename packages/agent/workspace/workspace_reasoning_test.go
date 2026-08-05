package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// reasoningSession wires a session with a real transcript file, which
// SetSessionReasoning needs: it persists through the session record and
// broadcasts, so a bare struct would not exercise the path that matters.
func reasoningSession(t *testing.T, w *Workspace, id string) *wsSession {
	t.Helper()
	sess, err := core.NewSessionAtPath(filepath.Join(testsupport.TempDir(t), id+".jsonl"), "/ws", "anthropic", "claude-opus-4-8", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	s := newTestSession()
	// The id is set BEFORE registering: newTestSession names every session
	// "test", so registering first and renaming after would key two sessions
	// the same and let resolve hand back the wrong one.
	s.id = id
	s.ws = w
	s.sess = sess
	s.agent = &core.Agent{}
	if w.sessions == nil {
		w.sessions = map[string]*wsSession{}
	}
	w.sessions[s.id] = s
	return s
}

// The whole point of the feature: a level set on one session outranks the
// global, and a later change to the global leaves it alone.
//
// Driven THROUGH SetSessionReasoning and applyReasoning rather than by poking
// the fields, because the interesting behaviour lives in how those two agree.
func TestSessionOverrideSurvivesAGlobalChange(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if err := config.MutateConfig(func(c *config.Config) { c.Reasoning = "medium" }); err != nil {
		t.Fatal(err)
	}
	w := &Workspace{}
	overridden := reasoningSession(t, w, "overridden")
	plain := reasoningSession(t, w, "plain")

	if err := w.SetSessionReasoning(context.Background(), overridden.id, "max"); err != nil {
		t.Fatalf("SetSessionReasoning: %v", err)
	}
	if got := overridden.agent.Reasoning; got != "max" {
		t.Fatalf("agent reasoning = %q, want max", got)
	}

	// The global moves to "high".
	w.applyReasoning("high")

	if got := overridden.agent.Reasoning; got != "max" {
		t.Errorf("a global change clobbered the session's own level: %q, want max", got)
	}
	if got := overridden.currentReasoning(); got != "max" {
		t.Errorf("the override record was lost: %q", got)
	}
	// ...and the session that never chose still follows the global.
	if got := plain.agent.Reasoning; got != "high" {
		t.Errorf("an un-overridden session did not follow the global: %q, want high", got)
	}
}

// Clearing has to put the session back UNDER the global, not freeze it at the
// global's current value — otherwise "inherit" would silently become a private
// copy that stops tracking.
func TestClearingAnOverrideReturnsTheSessionToTheGlobal(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if err := config.MutateConfig(func(c *config.Config) { c.Reasoning = "medium" }); err != nil {
		t.Fatal(err)
	}
	w := &Workspace{}
	s := reasoningSession(t, w, "s1")

	if err := w.SetSessionReasoning(context.Background(), s.id, "max"); err != nil {
		t.Fatal(err)
	}
	if err := w.SetSessionReasoning(context.Background(), s.id, "inherit"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := s.currentReasoning(); got != "" {
		t.Errorf("override not cleared: %q", got)
	}
	if got := s.agent.Reasoning; got != "medium" {
		t.Errorf("cleared session did not pick up the global: %q, want medium", got)
	}
	// And it TRACKS the global again rather than holding a copy.
	w.applyReasoning("low")
	if got := s.agent.Reasoning; got != "low" {
		t.Errorf("cleared session stopped following the global: %q, want low", got)
	}
}

// "off" is a real rung, not the absence of one. If the two collapsed, a session
// deliberately set to off would be re-levelled by the next global change.
func TestExplicitOffIsAnOverrideNotAnAbsentOne(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if err := config.MutateConfig(func(c *config.Config) { c.Reasoning = "high" }); err != nil {
		t.Fatal(err)
	}
	w := &Workspace{}
	s := reasoningSession(t, w, "s1")

	if err := w.SetSessionReasoning(context.Background(), s.id, "off"); err != nil {
		t.Fatal(err)
	}
	if got := s.currentReasoning(); got != "off" {
		t.Fatalf("stored override = %q, want the raw \"off\"", got)
	}
	// Normalized for the agent, but still an explicit choice.
	if got := s.agent.Reasoning; got != "" {
		t.Errorf("agent reasoning = %q, want \"\" (off)", got)
	}
	if !s.agent.ReasoningSet {
		t.Error("explicit off must set ReasoningSet, or a per-model default would beat it")
	}
	w.applyReasoning("maximum")
	if got := s.agent.Reasoning; got != "" {
		t.Errorf("a global change overrode an explicit off: %q", got)
	}
}

// With no global level at all, clearing must hand the session back to its
// MODEL's default — which means ReasoningSet false, not Reasoning "".
// SetReasoning("") and ClearReasoning() leave the same Reasoning field and
// differ only here, so this is the only assertion that can tell them apart.
func TestClearingWithNoGlobalFallsBackToTheModelDefault(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	w := &Workspace{}
	s := reasoningSession(t, w, "s1")

	if err := w.SetSessionReasoning(context.Background(), s.id, "high"); err != nil {
		t.Fatal(err)
	}
	if !s.agent.ReasoningSet {
		t.Fatal("precondition: an explicit level should set ReasoningSet")
	}
	if err := w.SetSessionReasoning(context.Background(), s.id, ""); err != nil {
		t.Fatal(err)
	}
	if s.agent.ReasoningSet {
		t.Error("clearing with no global must leave ReasoningSet false so the model's DefaultReasoning applies")
	}
}

// The override is persisted, or a daemon restart silently drops a session back
// to the global depth.
func TestOverrideIsPersistedToSessionMeta(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	w := &Workspace{}
	s := reasoningSession(t, w, "s1")

	if err := w.SetSessionReasoning(context.Background(), s.id, "max"); err != nil {
		t.Fatal(err)
	}
	if got := s.sess.Meta.Reasoning; got != "max" {
		t.Errorf("session meta reasoning = %q, want max", got)
	}
	// args carries it too: Resolve reads args ahead of the global, and a
	// rebuild re-resolves from args — this is what makes the override survive
	// an extension reload rather than only a restart.
	if got := s.args.Reasoning; got != "max" {
		t.Errorf("args reasoning = %q, want max", got)
	}
}

func TestUnknownReasoningLevelIsRefusedByName(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	w := &Workspace{}
	s := reasoningSession(t, w, "s1")

	err := w.SetSessionReasoning(context.Background(), s.id, "extremely")
	if err == nil {
		t.Fatal("an unknown level was accepted — it would reach the provider as a level it has never heard of")
	}
	// The refusal must not have moved anything.
	if got := s.currentReasoning(); got != "" {
		t.Errorf("a refused level was still stored: %q", got)
	}

	// The "must still succeed" half: a real rung, and the documented aliases,
	// still go through. Without this the test cannot tell a targeted refusal
	// from one that rejects everything.
	for _, ok := range []string{"minimum", "minimal", "hi", "max", "MAXIMUM"} {
		if err := w.SetSessionReasoning(context.Background(), s.id, ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
}
