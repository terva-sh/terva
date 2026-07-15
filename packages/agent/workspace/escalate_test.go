package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// The target is read live from config, and both fields are required — a partial
// target is no target, so escalation stays inert rather than switching to a
// half-specified model.
func TestSessionEscalatorTargetFromConfig(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	e := &sessionEscalator{s: &wsSession{}}

	if _, ok := e.Target(); ok {
		t.Error("no escalation config must yield no target (inert)")
	}

	if err := config.MutateConfig(func(c *config.Config) {
		c.Escalation = &config.EscalationConfig{Provider: "anthropic"} // model missing
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Target(); ok {
		t.Error("a partial target (no model) must be treated as no target")
	}

	if err := config.MutateConfig(func(c *config.Config) {
		c.Escalation = &config.EscalationConfig{Provider: "anthropic", Model: "claude-sonnet-5"}
	}); err != nil {
		t.Fatal(err)
	}
	tgt, ok := e.Target()
	if !ok || tgt.Provider != "anthropic" || tgt.Model != "claude-sonnet-5" {
		t.Errorf("a full target must resolve, got %+v ok=%v", tgt, ok)
	}
}

// Already on the target: Escalate declines without touching switchModel, so a
// session that stalled while already on the strong model doesn't "swap" to
// itself (which would rebuild the client for nothing).
func TestSessionEscalatorDeclinesWhenAlreadyOnTarget(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if err := config.MutateConfig(func(c *config.Config) {
		c.Escalation = &config.EscalationConfig{Provider: "anthropic", Model: "claude-sonnet-5"}
	}); err != nil {
		t.Fatal(err)
	}
	e := &sessionEscalator{s: &wsSession{provider: "anthropic", model: "claude-sonnet-5"}}
	out, err := e.Escalate(context.Background(), core.EscalationRequest{})
	if err != nil {
		t.Fatalf("Escalate errored: %v", err)
	}
	if !out.Declined || out.Switched {
		t.Errorf("already-on-target must decline, not switch: %+v", out)
	}
}

// No target configured: Escalate declines cleanly (the runLoop driver treats this
// as "nothing to escalate to" and continues on the current model).
func TestSessionEscalatorDeclinesWithoutATarget(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	e := &sessionEscalator{s: &wsSession{provider: "openai-compatible", model: "gemma"}}
	out, err := e.Escalate(context.Background(), core.EscalationRequest{})
	if err != nil {
		t.Fatalf("Escalate errored: %v", err)
	}
	if !out.Declined {
		t.Errorf("no target must decline: %+v", out)
	}
}
