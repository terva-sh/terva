package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// bareWorkspace is a Workspace with just a cwd and a trust verdict — enough for
// the config-level verbs, which touch neither sessions nor a provider. The trust
// verdict is atomic (it moves with Trust/Untrust), so it can't be a field in a
// struct literal.
func bareWorkspace(cwd string, trusted bool) *Workspace {
	w := &Workspace{cwd: cwd}
	w.trusted.Store(trusted)
	return w
}

// SetDefaultModel is what the web panel and an attach-mode TUI's ctrl+d both
// land on. It writes config and must NOT touch a live session — switching a
// model and adopting it as the default are separate acts.
func TestSetDefaultModelWritesScope(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	cwd := testsupport.TempDir(t)
	ctx := context.Background()
	w := bareWorkspace(cwd, true)

	if err := w.SetDefaultModel(ctx, "anthropic", "claude-opus-4-8", ctrlproto.ScopeGlobal); err != nil {
		t.Fatalf("global: %v", err)
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Provider != "anthropic" || cfg.Model != "claude-opus-4-8" {
		t.Errorf("global default = %s/%s", cfg.Provider, cfg.Model)
	}

	if err := w.SetDefaultModel(ctx, "openai-codex", "gpt-5.6-sol", ctrlproto.ScopeProject); err != nil {
		t.Fatalf("project: %v", err)
	}
	pc, err := config.LoadProjectConfig(cwd)
	if err != nil || pc == nil {
		t.Fatalf("load project config: %v (pc=%v)", err, pc)
	}
	if pc.Provider != "openai-codex" || pc.Model != "gpt-5.6-sol" {
		t.Errorf("project default = %s/%s", pc.Provider, pc.Model)
	}
	// The project write must not have disturbed the global one.
	if cfg, _ := config.LoadConfig(); cfg.Model != "claude-opus-4-8" {
		t.Errorf("project write clobbered the global default: %s", cfg.Model)
	}
}

func TestSetDefaultModelRejectsBadInput(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	ctx := context.Background()
	w := bareWorkspace(testsupport.TempDir(t), true)

	if err := w.SetDefaultModel(ctx, "anthropic", "claude-opus-4-8", "everywhere"); err == nil {
		t.Error("an unknown scope must be refused, not silently written somewhere")
	}
	if err := w.SetDefaultModel(ctx, "", "claude-opus-4-8", ctrlproto.ScopeGlobal); err == nil {
		t.Error("an empty provider must be refused")
	}
	if err := w.SetDefaultModel(ctx, "anthropic", "", ctrlproto.ScopeGlobal); err == nil {
		t.Error("an empty model must be refused")
	}
}

// Stage 2 of the per-session model-selection plan: a new session starts on the
// CONFIGURED default (models.set_default → config), read live, rather than the
// workspace's boot model or whatever a live session was last switched to. The
// boot model (openai/gpt-5, from Args) is deliberately distinct from the
// configured default (openai/gpt-5.5) so the assertion proves which one wins.
func TestNewSessionSeedsFromConfiguredDefault(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key") // credential so CreateSession's resolve succeeds
	if err := config.MutateConfig(func(c *config.Config) {
		c.Provider, c.Model = "openai", "gpt-5.5"
	}); err != nil {
		t.Fatal(err)
	}
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true}, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if info.Provider != "openai" || info.Model != "gpt-5.5" {
		t.Errorf("new session = %s/%s, want openai/gpt-5.5 (the configured default, not boot gpt-5)", info.Provider, info.Model)
	}
}

// With no configured default, a new session falls back to the workspace's
// boot-resolved model — which honors a launch --model (here Args.Model) and the
// catalog. This is the branch that keeps launch-time behavior intact.
func TestNewSessionFallsBackToBootDefault(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key") // credential so CreateSession's resolve succeeds
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true}, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if info.Model != "gpt-5" {
		t.Errorf("new session model = %s, want gpt-5 (boot fallback when config names no default)", info.Model)
	}
}

// The effective-default resolver is the one authority every "what model is
// default here?" surface routes through — the wire's models.default_for and the
// session seed (createSeededLocked) alike. A card's stored pref outranks the
// workspace default; a pref naming a model this workspace can't run degrades to
// the workspace floor rather than seeding an unrunnable session.
func TestEffectiveDefaultModelCardRung(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	cwd := testsupport.TempDir(t)
	// Boot default openai/gpt-5, no configured default → the workspace floor is
	// the boot model, source "workspace".
	w := bareWorkspace(cwd, true)
	w.provider, w.model = "openai", "gpt-5"

	if p, m, src := w.effectiveDefaultModel("", ""); p != "openai" || m != "gpt-5" || src != ctrlproto.DefaultSourceWorkspace {
		t.Fatalf("no card: %s/%s@%s, want openai/gpt-5@workspace", p, m, src)
	}

	// A card pref (a real catalog model, distinct from boot) wins, source "card".
	if err := build.NewCardModelStore().Set("alice-abc123", "openai", "gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	if p, m, src := w.effectiveDefaultModel("alice-abc123", ""); p != "openai" || m != "gpt-5.5" || src != ctrlproto.DefaultSourceCard {
		t.Errorf("card pref: %s/%s@%s, want openai/gpt-5.5@card", p, m, src)
	}

	// A card with no pref inherits the workspace floor.
	if _, _, src := w.effectiveDefaultModel("bob-def456", ""); src != ctrlproto.DefaultSourceWorkspace {
		t.Errorf("unset card should inherit the workspace, got source %s", src)
	}

	// A pref naming a model the catalog doesn't hold degrades to the floor rather
	// than seeding a session that can't resolve.
	if err := build.NewCardModelStore().Set("carol-777aaa", "openai", "no-such-model-xyz"); err != nil {
		t.Fatal(err)
	}
	if p, m, src := w.effectiveDefaultModel("carol-777aaa", ""); p != "openai" || m != "gpt-5" || src != ctrlproto.DefaultSourceWorkspace {
		t.Errorf("unresolvable pref should fall through: %s/%s@%s, want openai/gpt-5@workspace", p, m, src)
	}
}

// The world rung, which stood reserved and unreachable until worlds.set_model
// gave it a writer. Every assertion here is about ORDER, because a ladder whose
// rungs are all wired but mis-ranked reads as working until two of them disagree.
func TestEffectiveDefaultModelWorldRung(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	w := bareWorkspace(testsupport.TempDir(t), true)
	w.provider, w.model = "openai", "gpt-5" // the workspace floor

	store := build.NewWorldStore()
	doc, err := store.Save(build.WorldDoc{Name: "Bellhaven"})
	if err != nil {
		t.Fatal(err)
	}

	// A World with no default of its own changes nothing — the floor shows
	// through, and the source still names the workspace.
	if p, m, src := w.effectiveDefaultModel("", doc.ID); p != "openai" || m != "gpt-5" || src != ctrlproto.DefaultSourceWorkspace {
		t.Fatalf("world with no default: %s/%s@%s, want openai/gpt-5@workspace", p, m, src)
	}

	// Given one, it outranks the workspace.
	doc.Model = core.CastRoute{Provider: "openai", Model: "gpt-5.5"}
	if doc, err = store.Save(doc); err != nil {
		t.Fatal(err)
	}
	if p, m, src := w.effectiveDefaultModel("", doc.ID); p != "openai" || m != "gpt-5.5" || src != ctrlproto.DefaultSourceWorld {
		t.Errorf("world default: %s/%s@%s, want openai/gpt-5.5@world", p, m, src)
	}

	// ...and is in turn outranked by a card's own pref. This is the ordering
	// decision the ladder encodes: "this character runs on X" is the narrower
	// statement, so a World setting the room's floor does not overrule a cast
	// member who was given a voice on purpose.
	if err := build.NewCardModelStore().Set("alice-abc123", "anthropic", "claude-opus-4-8"); err != nil {
		t.Fatal(err)
	}
	if p, m, src := w.effectiveDefaultModel("alice-abc123", doc.ID); p != "anthropic" || m != "claude-opus-4-8" || src != ctrlproto.DefaultSourceCard {
		t.Errorf("card over world: %s/%s@%s, want anthropic/claude-opus-4-8@card", p, m, src)
	}

	// A card with no pref of its own falls to the WORLD rung, not past it to the
	// workspace — the case that makes the middle rung worth having at all.
	if p, m, src := w.effectiveDefaultModel("bob-def456", doc.ID); p != "openai" || m != "gpt-5.5" || src != ctrlproto.DefaultSourceWorld {
		t.Errorf("unpinned card in a World: %s/%s@%s, want openai/gpt-5.5@world", p, m, src)
	}

	// A World naming a model this workspace cannot run degrades to the floor,
	// exactly like the card rung — a default that resolves to nothing must not
	// seed an unrunnable session.
	doc.Model = core.CastRoute{Provider: "openai", Model: "no-such-model-xyz"}
	if _, err = store.Save(doc); err != nil {
		t.Fatal(err)
	}
	if p, m, src := w.effectiveDefaultModel("", doc.ID); p != "openai" || m != "gpt-5" || src != ctrlproto.DefaultSourceWorkspace {
		t.Errorf("unresolvable world default should fall through: %s/%s@%s, want openai/gpt-5@workspace", p, m, src)
	}

	// A world id that names nothing is not an error — the ladder is consulted on
	// paths where the World is simply absent.
	if _, _, src := w.effectiveDefaultModel("", "no-such-world"); src != ctrlproto.DefaultSourceWorkspace {
		t.Errorf("unknown world should inherit the workspace, got source %s", src)
	}
}

// The whole point of the world rung: a scene started in a World opens on the
// World's model. Through CreateSession, not through the resolver directly —
// five tests once passed against a helper while the one production caller had a
// bug, so the assertion has to travel the path the author does.
func TestSessionCreatedInAWorldOpensOnItsModel(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	store := build.NewWorldStore()
	doc, err := store.Save(build.WorldDoc{Name: "Bellhaven", Model: core.CastRoute{Provider: "openai", Model: "gpt-5.5"}})
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true}, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{World: doc.ID})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if info.Model != "gpt-5.5" {
		t.Errorf("scene in a World = %s, want gpt-5.5 (the World's default, not boot gpt-5)", info.Model)
	}

	// And an explicit pick still wins over the World — the author asking for a
	// model in the moment is the narrowest statement of all.
	info, err = w.CreateSession(context.Background(), ctrlproto.CreateOpts{World: doc.ID, Provider: "openai", Model: "gpt-5"})
	if err != nil {
		t.Fatalf("CreateSession with a pick: %v", err)
	}
	if info.Model != "gpt-5" {
		t.Errorf("explicit pick = %s, want gpt-5 — the World must not override it", info.Model)
	}
}

// A project default shadows the global one — but only while the workspace is
// trusted, because that is the only condition under which the project config is
// honored at all. Advertising an untrusted project default as "in force" would
// tell the picker a lie the loader would not back up.
func TestDefaultModelPrecedence(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	cwd := testsupport.TempDir(t)
	ctx := context.Background()

	trusted := bareWorkspace(cwd, true)
	if err := trusted.SetDefaultModel(ctx, "anthropic", "claude-opus-4-8", ctrlproto.ScopeGlobal); err != nil {
		t.Fatal(err)
	}

	prov, model, scope := trusted.defaultModel()
	if prov != "anthropic" || model != "claude-opus-4-8" || scope != ctrlproto.ScopeGlobal {
		t.Fatalf("with only a global default: %s/%s@%s", prov, model, scope)
	}

	if err := trusted.SetDefaultModel(ctx, "openai-codex", "gpt-5.6-sol", ctrlproto.ScopeProject); err != nil {
		t.Fatal(err)
	}
	prov, model, scope = trusted.defaultModel()
	if prov != "openai-codex" || model != "gpt-5.6-sol" || scope != ctrlproto.ScopeProject {
		t.Errorf("a trusted project default must shadow the global one, got %s/%s@%s", prov, model, scope)
	}

	// Same directory, same project config — but untrusted, so it does not count.
	untrusted := bareWorkspace(cwd, false)
	prov, model, scope = untrusted.defaultModel()
	if prov != "anthropic" || model != "claude-opus-4-8" || scope != ctrlproto.ScopeGlobal {
		t.Errorf("an untrusted project default must not shadow the global one, got %s/%s@%s", prov, model, scope)
	}
}
