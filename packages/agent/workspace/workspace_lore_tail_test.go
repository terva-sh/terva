package workspace

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tools/tasks"
	"terva.sh/terva/packages/testsupport"
)

// The end a user reaches: the model's own open work disappearing from its
// context because someone edited a lorebook entry.
//
// reloadLore re-derives the run's per-turn tail and installs it on the running
// agent. It installed it BARE — so the task card and the extension cards the
// session build had stacked on top went with it, for the rest of the session.
// Three verbs run reloadLore (a lore edit, a user-persona change, a trust flip),
// and none of them has anything to do with the task board.
//
// Nothing surfaced it: what remained was a valid tail, just two layers short.
func TestALoreReloadKeepsTheTaskCardInTheModelsContext(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{
		Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true,
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })

	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)
	if live == nil {
		t.Fatal("session is not live")
	}
	if live.tasks == nil {
		t.Fatal("session has no task controller")
	}
	if _, err := live.tasks.Store().Create([]tasks.CreateSpec{
		{Title: "finish the migration", ActiveForm: "finishing the migration"},
	}); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	if got := live.agent.ContextPreview(); !strings.Contains(got, "finish the migration") {
		t.Fatalf("precondition: the task card is not in the model's context at build:\n%s", got)
	}

	// What a user does: edit a lorebook entry. reloadLore is the shared tail
	// re-derivation all three verbs run.
	live.reloadLore()

	if got := live.agent.ContextPreview(); !strings.Contains(got, "finish the migration") {
		t.Errorf("after a lore edit the model can no longer see its own open work — the "+
			"task card is gone from the per-turn tail for the rest of the session:\n%s", got)
	}

	// And it is still there exactly once after several edits: rebuilding the
	// stack from its parts, not layering onto what is already installed.
	for i := 0; i < 3; i++ {
		live.reloadLore()
	}
	if n := strings.Count(live.agent.ContextPreview(), "finish the migration"); n != 1 {
		t.Errorf("the task card appears %d times after four lore edits, want 1 — each "+
			"reload is stacking another copy onto the context window", n)
	}
}

// A trust flip runs the same re-derivation, through ApplyTrust's Lore surface
// rather than a lore verb. Same loss, different door — and this is the one the
// last review found, so it gets its own assertion rather than riding on the
// shared helper's.
func TestATrustFlipKeepsTheTaskCardInTheModelsContext(t *testing.T) {
	w, s := trustSession(t)

	if _, err := s.tasks.Store().Create([]tasks.CreateSpec{
		{Title: "finish the migration", ActiveForm: "finishing the migration"},
	}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if got := s.agent.ContextPreview(); !strings.Contains(got, "finish the migration") {
		t.Fatalf("precondition: the task card is not in the model's context at build:\n%s", got)
	}

	if err := w.Trust(context.Background(), false); err != nil {
		t.Fatalf("Trust: %v", err)
	}

	if got := s.agent.ContextPreview(); !strings.Contains(got, "finish the migration") {
		t.Errorf("a trust flip took the task card out of the model's per-turn context:\n%s", got)
	}
}
