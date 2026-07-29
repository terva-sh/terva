//go:build terva_acp

package agent

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// ACP's tool-set rebuild used to re-resolve from a copy of the launch args
// taken once at session build. A model switch moves the running agent and the
// tool instances hanging off it (build.ApplyModelSwap), but it did not move
// those args — so the next rebuild, fired by an extension reload or a /trust
// flip, re-minted terva_status naming the provider the session had switched
// AWAY from, and re-derived read's SupportsVision from the launch model.
//
// This is the daemon's #489 in the second host that can do both. The daemon's
// stale copy was s.args; ACP's was LiveToolSet.Args, frozen into a long-lived
// struct. Both now assemble the set at rebuild time from live state.
//
// The seam ACP needed and the daemon did not: SwitchModel is a factory method
// with no session, so ModelSwitch.Apply cannot reach the args. The host hands
// back RecordSwap, and the acp package calls it beside Apply.

// acpStatusProvider runs terva_status the way the model would and returns the
// provider line — the live tool in the agent's registry, so a rebuild that
// replaced the instance is visible here.
func acpStatusProvider(t *testing.T, ag *core.Agent) string {
	t.Helper()
	tl, ok := ag.LookupTool("terva_status")
	if !ok {
		t.Fatal("terva_status is not in the agent's registry")
	}
	res, err := tl.Execute(context.Background(), nil, func(string) {})
	if err != nil {
		t.Fatalf("terva_status: %v", err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}
	out := sb.String()
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "provider: "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("terva_status printed no provider line:\n%s", out)
	return ""
}

func TestACPRebuildTracksAModelSwap(t *testing.T) {
	cwd := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	ctx := context.Background()
	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd, NoExt: true, NoMCP: true}
	f := &acpFactory{ctx: ctx, args: args, version: "test"}
	r, ag, _, cleanup, _, _, hooks, err := f.buildAgent(ctx, cwd, nil, allowingConfirmer{})
	if err != nil {
		t.Fatalf("buildAgent: %v", err)
	}
	t.Cleanup(cleanup)
	if hooks.RecordSwap == nil {
		t.Fatal("buildAgent wired no RecordSwap — a model switch would last only until the next rebuild")
	}
	if got := acpStatusProvider(t, ag); got != "openai" {
		t.Fatalf("precondition: terva_status reports %q, want openai", got)
	}

	// What handleSetConfigOption does: apply the swap to the agent, then record
	// it into what the host re-resolves from. The two are one event.
	sw, err := f.SwitchModel(r.Provider, r.Model, "claude-sonnet-4-5-20250929")
	if err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	if sw.Apply == nil {
		t.Fatal("SwitchModel returned no applier")
	}
	sw.Apply(ag)
	hooks.RecordSwap(sw.Provider, sw.Model, sw.Client != nil)

	if got := acpStatusProvider(t, ag); got != "anthropic" {
		t.Fatalf("precondition: the swap itself did not move terva_status (got %q)", got)
	}

	// A /trust flip: one of the two things that re-resolve this session's tool
	// set, and the one ACP can do over its own wire.
	hooks.ApplyTrust(ctx, true)

	if got := acpStatusProvider(t, ag); got != "anthropic" {
		t.Errorf("after a rebuild terva_status reports %q — the model is told it is "+
			"running at the provider the session switched away from", got)
	}
	if ag.Model != "claude-sonnet-4-5-20250929" {
		t.Errorf("the agent's model is %q after the rebuild", ag.Model)
	}
}

// A resumed ACP session runs on the model stored in its meta, not the one it
// resolved with — applyResumedModel swaps it at build. That is a model swap
// too, and it needs the same second half, or the session's FIRST rebuild puts
// the built model back.
func TestACPResumeRecordsTheStoredModelForLaterRebuilds(t *testing.T) {
	cwd := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	ctx := context.Background()
	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd, NoExt: true, NoMCP: true}
	f := &acpFactory{ctx: ctx, args: args, version: "test"}

	// A session whose stored model is a DIFFERENT provider than the one this
	// factory resolves to.
	fresh, err := f.NewSessionAgent(ctx, cwd, nil, allowingConfirmer{})
	if err != nil {
		t.Fatalf("NewSessionAgent: %v", err)
	}
	path := fresh.Session.Path
	if err := fresh.Session.UpdateModel("anthropic", "claude-sonnet-4-5-20250929"); err != nil {
		t.Fatalf("UpdateModel: %v", err)
	}
	// A transcript with no messages is discarded on Close (freshFile pruning),
	// and there would be nothing to resume.
	if err := fresh.Session.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hello"}},
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	fresh.Cleanup()
	_ = fresh.Session.Close()

	sa, _, err := f.LoadSessionAgent(ctx, path, cwd, nil, allowingConfirmer{})
	if err != nil {
		t.Fatalf("LoadSessionAgent: %v", err)
	}
	t.Cleanup(sa.Cleanup)
	if sa.Provider != "anthropic" {
		t.Fatalf("precondition: resume reports provider %q, want anthropic", sa.Provider)
	}
	if got := acpStatusProvider(t, sa.Agent); got != "anthropic" {
		t.Fatalf("precondition: resume did not move terva_status (got %q)", got)
	}
	if sa.RecordModelSwap == nil {
		t.Fatal("the resumed SessionAgent carries no RecordModelSwap")
	}

	// The resumed session's own trust verb, which rebuilds its tool set.
	if sa.TrustWorkspace == nil {
		t.Fatal("the resumed SessionAgent carries no TrustWorkspace")
	}
	if err := sa.TrustWorkspace(false); err != nil {
		t.Fatalf("TrustWorkspace: %v", err)
	}

	if got := acpStatusProvider(t, sa.Agent); got != "anthropic" {
		t.Errorf("after the first rebuild a resumed session's terva_status reports %q — "+
			"the session RUNS on its stored model but re-resolves the one it was built with", got)
	}
}
