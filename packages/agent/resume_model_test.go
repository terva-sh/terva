package agent

import (
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// Stage 3 of the per-session model-selection plan. A headless/ACP resume builds
// the agent on the resolved default, so applyResumedModel must re-point it at the
// session's OWN stored model. The returned (prov, model) is what the ACP menu
// shows and the note is what the headless path logs, so both are asserted here
// across the three branches: re-point, no-op, and the non-fatal fallback when the
// stored model can't be used.
func TestApplyResumedModel(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "") // the anthropic branch must have no credential

	dir := testsupport.TempDir(t)
	base := build.Args{Provider: "openai", Model: "gpt-5", CWD: dir, NoExt: true, NoMCP: true}
	r, err := build.Resolve(base, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	newSessionOn := func(prov, model string) *core.Session {
		t.Helper()
		s, err := core.NewSession(testsupport.TempDir(t), dir, prov, model, "test")
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		// An open session file inside the temp base can fail the TempDir
		// RemoveAll on filesystems that silly-rename open files.
		t.Cleanup(func() { _ = s.Close() })
		return s
	}

	t.Run("re-points to a resolvable stored model", func(t *testing.T) {
		prov, model, note := applyResumedModel(r.NewAgent(), base, newSessionOn("openai", "gpt-5.5"), r.Provider, r.Model)
		if prov != "openai" || model != "gpt-5.5" || note != "" {
			t.Fatalf("got %s/%s note=%q, want openai/gpt-5.5 with no note", prov, model, note)
		}
	})

	t.Run("no-op when the stored model equals the built one", func(t *testing.T) {
		prov, model, note := applyResumedModel(r.NewAgent(), base, newSessionOn("openai", "gpt-5"), r.Provider, r.Model)
		if prov != "openai" || model != "gpt-5" || note != "" {
			t.Fatalf("got %s/%s note=%q, want openai/gpt-5 with no note", prov, model, note)
		}
	})

	t.Run("keeps the built model and warns when the stored model has no credential", func(t *testing.T) {
		prov, model, note := applyResumedModel(r.NewAgent(), base, newSessionOn("anthropic", "claude-opus-4-8"), r.Provider, r.Model)
		if prov != "openai" || model != "gpt-5" {
			t.Fatalf("got %s/%s, want the built openai/gpt-5 to stand", prov, model)
		}
		if note == "" {
			t.Fatal("a skipped swap must return a note, not swallow it")
		}
	})
}
