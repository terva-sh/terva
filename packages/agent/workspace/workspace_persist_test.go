package workspace

import (
	"context"
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// persistConfirmer answers every prompt with the confirm dialog's "always this
// tool — save to config" choice (ConfirmDecision.PersistTool).
type persistConfirmer struct{}

func (persistConfirmer) Confirm(_ context.Context, tool, preview string) core.ConfirmDecision {
	return core.ConfirmDecision{Allow: true, PersistTool: true}
}

// A PersistTool answer must reach durable USER config — the promise the TUI's
// "save (adds a permanent allow rule to your config)" option makes, and the one
// that silently did nothing until buildSession installed the persist callback.
// buildSession is the ONLY production gate that installs it, so this drives a
// real session's gate to a PersistTool decision and asserts the allow rule
// landed on disk: delete that SetPersist line and this turns red.
func TestDurableGrantPersistsToUserConfig(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()

	// A non-yolo mode is what gives the session a gate at all (yolo + no rules
	// resolves to a nil policy, hence no gate). Seed it before the session builds.
	if err := config.SaveConfig(config.Config{Approval: "ask"}); err != nil {
		t.Fatal(err)
	}

	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Ivy","first_mes":"Hello."}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)
	if live == nil || live.gate == nil {
		t.Fatal("session should have a gate under approval=ask")
	}

	// Swap the human-facing confirmer (webConfirmer broadcasts and blocks) for
	// one that always answers PersistTool, keeping the persist callback that
	// buildSession wired onto this gate.
	live.gate.SetConfirmer(persistConfirmer{})

	if ok, _, _ := live.gate.Check(context.Background(), "bash", nil, "ls", ""); !ok {
		t.Fatal("a persist answer should allow the call")
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, r := range cfg.Permissions {
		if r.Tool == "bash" && r.Args == "" && r.Decision == string(core.RuleAllow) {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("durable grant did not reach user config as one bash allow rule; permissions = %+v", cfg.Permissions)
	}
}

// scopedPersistConfirmer answers with the dialog's scoped option: persist,
// but narrowed to the derived command patterns the gate offered.
type scopedPersistConfirmer struct{}

func (scopedPersistConfirmer) Confirm(_ context.Context, tool, preview string) core.ConfirmDecision {
	return core.ConfirmDecision{Allow: true}
}
func (scopedPersistConfirmer) ConfirmWithRequest(_ context.Context, req core.ConfirmRequest) core.ConfirmDecision {
	if len(req.Scopes) == 0 {
		return core.ConfirmDecision{Allow: true}
	}
	patterns := make([]string, len(req.Scopes))
	for i, s := range req.Scopes {
		patterns[i] = s.Pattern
	}
	return core.ConfirmDecision{Allow: true, PersistTool: true, PersistScopes: patterns}
}

// The scoped end-to-end: buildSession's deriver turns the bash command into
// grant scopes, the scoped answer persists an Args-narrowed rule, and after
// the refresh the SAME command auto-allows while a different one still
// consults the confirmer. Delete the SetScopeDeriver line, the Args field in
// the persist callback, or the refresh, and this turns red.
func TestScopedGrantPersistsAndComposes(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()

	if err := config.SaveConfig(config.Config{Approval: "ask"}); err != nil {
		t.Fatal(err)
	}

	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Ivy","first_mes":"Hello."}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)
	if live == nil || live.gate == nil {
		t.Fatal("session should have a gate under approval=ask")
	}
	live.gate.SetConfirmer(scopedPersistConfirmer{})

	gitArgs, _ := json.Marshal(map[string]string{"command": "git status"})
	if ok, _, _ := live.gate.Check(context.Background(), "bash", gitArgs, "git status", ""); !ok {
		t.Fatal("the scoped persist answer should allow the call")
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := `^git status(?:\s|$)`
	found := 0
	for _, r := range cfg.Permissions {
		if r.Tool == "bash" && r.Args == wantArgs && r.Decision == string(core.RuleAllow) {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("scoped grant did not land as one Args-narrowed rule; permissions = %+v", cfg.Permissions)
	}

	// After refreshAllPolicies (run by the persist callback), the same
	// command must auto-allow WITHOUT the confirmer: swap in a confirmer
	// that fails the test if consulted.
	live.gate.SetConfirmer(tripwireConfirmer{t: t, note: "a matching call must ride the persisted rule, not the prompt"})
	if ok, _, _ := live.gate.Check(context.Background(), "bash", gitArgs, "git status", ""); !ok {
		t.Fatal("the persisted scoped rule should auto-allow the same command")
	}

	// A different bash command must still consult the confirmer — the
	// scoped grant is not a blanket.
	live.gate.SetConfirmer(persistConfirmer{})
	rmArgs, _ := json.Marshal(map[string]string{"command": "rm -rf /tmp/x"})
	if ok, _, _ := live.gate.Check(context.Background(), "bash", rmArgs, "rm -rf /tmp/x", ""); !ok {
		t.Fatal("a non-matching command should still prompt (and this confirmer allows)")
	}
}

// tripwireConfirmer fails the test if the gate consults it at all.
type tripwireConfirmer struct {
	t    *testing.T
	note string
}

func (c tripwireConfirmer) Confirm(_ context.Context, tool, preview string) core.ConfirmDecision {
	c.t.Errorf("confirmer consulted for %s %q: %s", tool, preview, c.note)
	return core.ConfirmDecision{Allow: false, Reason: "tripwire"}
}
