package build_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// The live escalation probe: the one thing no unit test can answer, because it
// is about a real provider — does the configured escalation target actually
// resolve to a credentialed, swappable client, and does a turn run on it after
// the swap?
//
// It deliberately does NOT drive the detector → nudge → escalate trigger: that
// is fully unit-tested (packages/core/escalate_test.go and stall_test.go), and a
// real model cannot be made to stall deterministically. What only a live run can
// prove is the tail the fakes stub out — config target → build.Resolve →
// credential → NewClient → a real request on the new model — which is exactly the
// cross-provider swap a persistent loop would perform through
// Workspace.switchModel. For the full end-to-end (a weak model stalling and a
// strong one finishing), see the manual dogfood recipe in
// docs/plans/stuck-loop-escalation-rung3.md; that one is a human observation, not
// an assertion.
//
// Skipped unless TERVA_LIVE_ESCALATION_PROBE is set, because it SPENDS REAL MONEY
// (one short turn on the escalation target). The scratch TERVA_HOME needs
// auth.json plus a config.json selecting a default provider/model AND an
// escalation block:
//
//	{
//	  "provider": "openai-compatible", "model": "some-local-model",
//	  "escalation": { "provider": "anthropic", "model": "claude-sonnet-5" }
//	}
//
//	TERVA_HOME=/tmp/scratch-home TERVA_LIVE_ESCALATION_PROBE=1 \
//	  go test ./packages/agent/build -run TestLiveEscalation -v
func TestLiveEscalation(t *testing.T) {
	if os.Getenv("TERVA_LIVE_ESCALATION_PROBE") == "" {
		t.Skip("live probe: set TERVA_LIVE_ESCALATION_PROBE=1 (spends real money)")
	}
	ctx := context.Background()

	args, err := build.ParseArgs([]string{"--no-ext", "--no-mcp"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := build.Resolve(args, true)
	if err != nil {
		t.Fatal(err)
	}
	ag := r.NewAgent()
	weak := r.Model

	cfg, _ := config.LoadConfig()
	if cfg.Escalation == nil || cfg.Escalation.Provider == "" || cfg.Escalation.Model == "" {
		t.Fatal("set escalation.provider + escalation.model in the scratch config")
	}

	// Resolve and swap to the target exactly as sessionEscalator.Escalate would
	// (build.Resolve → credential check → NewClient → SetClientAndModel), just
	// without the workspace bookkeeping switchModel adds around it.
	targetArgs, err := build.ParseArgs([]string{"--no-ext", "--no-mcp", "--provider", cfg.Escalation.Provider, "--model", cfg.Escalation.Model})
	if err != nil {
		t.Fatal(err)
	}
	rr, err := build.Resolve(targetArgs, true)
	if err != nil {
		t.Fatalf("resolve escalation target: %v", err)
	}
	if !rr.HasCredential() {
		t.Fatalf("no credential for escalation target %q", cfg.Escalation.Provider)
	}
	ag.SetClientAndModel(rr.NewClient(), rr.Model)
	if ag.Model == weak {
		t.Fatalf("model did not change after the swap (still %q)", weak)
	}
	t.Logf("escalated %s → %s", weak, rr.Model)

	// A real turn on the escalated model proves the rebuilt client actually talks
	// to the target provider.
	sessPath := filepath.Join(testsupport.TempDir(t), "probe.jsonl")
	sess, err := core.NewSessionAtPath(sessPath, r.CWD, rr.Provider, rr.Model, "probe")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	build.WireHeadlessSessionPersist(ag, sess)

	if err := ag.PromptWithPolicy(ctx, "Reply with only the word OK.", nil, func(core.AgentEvent) {}); err != nil {
		t.Fatalf("turn on the escalated model: %v", err)
	}
	u := ag.LastTurnUsage()
	t.Logf("turn on %s: input=%d output=%d $%.4f", rr.Model, u.InputTokens, u.OutputTokens, u.CostUSD)
	if u.InputTokens == 0 {
		t.Error("no real request landed on the escalated model")
	}
}
