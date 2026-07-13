package tools

import (
	"context"
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// TestSwarmSpawnStampsHostSession pins the R5.7 fix: a spawn made from a
// conversation with a persistent transcript stamps the child with that
// session id (via the dispatch-context agent), so meta.json records which
// conversation the child belongs to — the stamp SetActiveSession was supposed
// to provide but no production host ever set. A live-only conversation
// leaves the stamp empty rather than inventing one.
func TestSwarmSpawnStampsHostSession(t *testing.T) {
	root := testsupport.TempDir(t)
	sw := swarm.New(swarm.Config{
		Root:     root,
		RepoRoot: root,
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			return swarm.RunnerFunc(func(ctx context.Context, sink swarm.Sink) error { return nil })
		},
	})
	defer sw.StopAll()
	tool := &SwarmSpawnTool{Swarm: sw, Enabled: func() bool { return true }}

	host := core.NewAgent(nil, "m", "s", core.Registry{})
	host.AdoptSessionIdentity(&core.Session{Path: "/x/20260712-010137-abcd1234.jsonl", ID: "meta-uuid"})
	ctx := core.ContextWithAgent(context.Background(), host)

	res, err := tool.Execute(ctx, json.RawMessage(`{"task":"stamped child"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("Execute: err=%v isError=%v", err, res.IsError)
	}
	agents := sw.List()
	if len(agents) != 1 {
		t.Fatalf("want 1 spawned agent, got %d", len(agents))
	}
	if got := agents[0].SessionID; got != "20260712-010137-abcd1234" {
		t.Errorf("child SessionID = %q, want the host transcript id", got)
	}

	// Live-only host (no transcript): no stamp, no invented id.
	bare := core.NewAgent(nil, "m", "s", core.Registry{})
	res, err = tool.Execute(core.ContextWithAgent(context.Background(), bare), json.RawMessage(`{"task":"live-only child"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("Execute: err=%v isError=%v", err, res.IsError)
	}
	agents = sw.List()
	if len(agents) != 2 {
		t.Fatalf("want 2 spawned agents, got %d", len(agents))
	}
	for _, a := range agents {
		if a.Task == "live-only child" && a.SessionID != "" {
			t.Errorf("live-only spawn SessionID = %q, want empty", a.SessionID)
		}
	}
}
