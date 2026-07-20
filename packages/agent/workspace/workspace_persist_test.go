package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// persistConfirmer answers every prompt with the confirm dialog's "always this
// tool — save to config" choice (ConfirmDecision.PersistTool).
type persistConfirmer struct{}

func (persistConfirmer) Confirm(tool, preview string) core.ConfirmDecision {
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

	if ok, _, _ := live.gate.Check("bash", nil, "ls"); !ok {
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
