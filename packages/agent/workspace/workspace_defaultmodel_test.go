package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
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
