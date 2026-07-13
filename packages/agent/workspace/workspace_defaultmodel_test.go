package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

// SetDefaultModel is what the web panel and an attach-mode TUI's ctrl+d both
// land on. It writes config and must NOT touch a live session — switching a
// model and adopting it as the default are separate acts.
func TestSetDefaultModelWritesScope(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	cwd := testsupport.TempDir(t)
	ctx := context.Background()
	w := &Workspace{cwd: cwd, trusted: true}

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
	w := &Workspace{cwd: testsupport.TempDir(t), trusted: true}

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

// A project default shadows the global one — but only while the workspace is
// trusted, because that is the only condition under which the project config is
// honored at all. Advertising an untrusted project default as "in force" would
// tell the picker a lie the loader would not back up.
func TestDefaultModelPrecedence(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	cwd := testsupport.TempDir(t)
	ctx := context.Background()

	trusted := &Workspace{cwd: cwd, trusted: true}
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
	untrusted := &Workspace{cwd: cwd, trusted: false}
	prov, model, scope = untrusted.defaultModel()
	if prov != "anthropic" || model != "claude-opus-4-8" || scope != ctrlproto.ScopeGlobal {
		t.Errorf("an untrusted project default must not shadow the global one, got %s/%s@%s", prov, model, scope)
	}
}
