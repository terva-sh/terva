package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/testsupport"
)

// TestLoreViewActivationTrace: loreView annotates each triggered entry with the
// last turn's activation trace — fired + why (matched keys) + dropped-for-budget —
// while leaving an unfired triggered entry and a constant entry unmarked. It also
// proves the record handle is wired (non-nil right after create).
func TestLoreViewActivationTrace(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)
	if live.loreFired == nil {
		t.Fatal("buildSession must retain the lore activation-trace record")
	}

	live.mu.Lock()
	live.loreEntries = []lore.Entry{
		{Name: "The Pass", Source: "pass.md", Keys: []string{"pass", "mountain"}},
		{Name: "The Sea", Source: "sea.md", Keys: []string{"sea"}},
		{Name: "The River", Source: "river.md", Keys: []string{"river"}},
		{Name: "Guardian oath", Source: "oath.md", Constant: true},
	}
	live.mu.Unlock()
	// Stand in for a turn: "The Pass" fired on key "pass"; "The Sea" fired but the
	// budget dropped it; "The River" never triggered; a constant entry is baked and
	// never appears in the per-turn trace.
	live.loreFired.Set([]build.LoreFired{
		{Name: "The Pass", Source: "pass.md", Keys: []string{"pass"}},
		{Name: "The Sea", Source: "sea.md", Keys: []string{"sea"}, Dropped: true},
	})

	byName := map[string]ctrlproto.LoreEntry{}
	for _, e := range live.loreView().Entries {
		byName[e.Name] = e
	}
	if got := byName["The Pass"]; !got.Fired || got.DroppedForBudget || len(got.MatchedKeys) != 1 || got.MatchedKeys[0] != "pass" {
		t.Errorf("The Pass: want fired on key [pass], not dropped; got %+v", got)
	}
	if got := byName["The Sea"]; !got.Fired || !got.DroppedForBudget {
		t.Errorf("The Sea: want fired AND dropped-for-budget; got %+v", got)
	}
	if got := byName["The River"]; got.Fired || got.DroppedForBudget || got.MatchedKeys != nil {
		t.Errorf("The River never triggered, should be unmarked; got %+v", got)
	}
	if got := byName["Guardian oath"]; got.Fired {
		t.Errorf("a constant entry is baked, not in the per-turn trace; got %+v", got)
	}
}

// TestContextBreakdownLoreFired: the last turn's lore activation trace surfaces
// on ContextBreakdown (the Usage-pane home for the trace), from the same record
// the drawer reads.
func TestContextBreakdownLoreFired(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)
	if live.loreFired == nil {
		t.Fatal("buildSession must retain the lore activation-trace record")
	}
	live.loreFired.Set([]build.LoreFired{
		{Name: "The Pass", Source: "pass.md", Keys: []string{"pass"}},
		{Name: "The Sea", Source: "sea.md", Keys: []string{"sea"}, Dropped: true},
		{Name: "Guardian oath", Source: "oath.md", Constant: true},
	})

	b := live.contextBreakdown()
	if len(b.LoreFired) != 3 {
		t.Fatalf("ContextBreakdown.LoreFired = %d, want 3", len(b.LoreFired))
	}
	byName := map[string]ctrlproto.ContextLoreEntry{}
	for _, e := range b.LoreFired {
		byName[e.Name] = e
	}
	if got := byName["The Pass"]; got.Source != "pass.md" || got.Dropped || len(got.Keys) != 1 || got.Keys[0] != "pass" {
		t.Errorf("The Pass: want source pass.md, key [pass], not dropped; got %+v", got)
	}
	if got := byName["The Sea"]; !got.Dropped {
		t.Errorf("The Sea should be dropped-for-budget; got %+v", got)
	}
	if got := byName["Guardian oath"]; !got.Constant {
		t.Errorf("Guardian oath should be constant; got %+v", got)
	}
}
