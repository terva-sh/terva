package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func newSpawnToolForTrust(t *testing.T, trusted bool) (*SwarmSpawnTool, *swarm.Swarm) {
	t.Helper()
	root := testsupport.TempDir(t)
	sw := swarm.New(swarm.Config{
		Root:     root,
		RepoRoot: root,
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			return swarm.RunnerFunc(func(ctx context.Context, sink swarm.Sink) error { return nil })
		},
	})
	return &SwarmSpawnTool{
		Swarm:   sw,
		Enabled: func() bool { return true },
		Trusted: func() bool { return trusted },
	}, sw
}

func spawnToolText(res provider.Content) string {
	if tb, ok := res.(provider.TextBlock); ok {
		return tb.Text
	}
	return ""
}

// TestSwarmSpawnRefusesUntrustedWorkspace: an untrusted workspace
// degrades sub-agents silently (no project extensions/skills/context),
// so the spawn must fail loudly with guidance the calling agent can
// relay — the user resolves trust and the spawn is retried, instead of
// discovering the degradation in a child's event log after the fact.
func TestSwarmSpawnRefusesUntrustedWorkspace(t *testing.T) {
	tool, sw := newSpawnToolForTrust(t, false)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"review the repo"}`), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error %v; want a soft tool error", err)
	}
	if !res.IsError {
		t.Fatal("untrusted spawn did not soft-fail")
	}
	text := spawnToolText(res.Content[0])
	if !strings.Contains(text, "untrusted") || !strings.Contains(text, "terva trust") {
		t.Fatalf("refusal must explain trust and how to fix it: %q", text)
	}
	if got := len(sw.List()); got != 0 {
		t.Fatalf("agents spawned despite refusal: %d", got)
	}
}

// TestSwarmSpawnAllowUntrustedProceedsWithNote: the explicit escape
// hatch spawns a degraded sub-agent and says so in the ack, so the
// transcript records that the children ran without project context.
func TestSwarmSpawnAllowUntrustedProceedsWithNote(t *testing.T) {
	tool, sw := newSpawnToolForTrust(t, false)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"review the repo","allow_untrusted":true}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("allow_untrusted spawn failed: %+v", res)
	}
	text := spawnToolText(res.Content[0])
	if !strings.Contains(text, "UNTRUSTED workspace") {
		t.Fatalf("degraded spawn ack must carry the untrusted note: %q", text)
	}
	if got := len(sw.List()); got != 1 {
		t.Fatalf("agents = %d, want 1", got)
	}
}

// TestSwarmSpawnTrustedHasNoNote: the gate is invisible in the normal,
// trusted case — and a nil Trusted hook (older construction sites)
// skips the gate entirely.
func TestSwarmSpawnTrustedHasNoNote(t *testing.T) {
	tool, sw := newSpawnToolForTrust(t, true)
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"review the repo"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("trusted spawn failed: err=%v res=%+v", err, res)
	}
	if text := spawnToolText(res.Content[0]); strings.Contains(text, "UNTRUSTED") {
		t.Fatalf("trusted ack must not carry the untrusted note: %q", text)
	}
	if got := len(sw.List()); got != 1 {
		t.Fatalf("agents = %d, want 1", got)
	}

	nilGate, sw2 := newSpawnToolForTrust(t, false)
	nilGate.Trusted = nil
	res, err = nilGate.Execute(context.Background(), json.RawMessage(`{"task":"review the repo"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("nil-Trusted spawn failed: err=%v res=%+v", err, res)
	}
	if got := len(sw2.List()); got != 1 {
		t.Fatalf("agents = %d, want 1", got)
	}
}
