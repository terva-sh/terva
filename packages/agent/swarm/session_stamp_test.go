package swarm

import (
	"context"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// TestSpawnReqStampsSessionID pins the R5.7 fix: a per-spawn SessionID wins
// (a daemon hosts many sessions, so the swarm-wide scope can't be trusted),
// the SetActiveSession scope is the fallback, and the stamp persists to
// meta.json so it survives a reload.
func TestSpawnReqStampsSessionID(t *testing.T) {
	root := testsupport.TempDir(t)
	f := New(Config{
		Root:     root,
		RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, sink Sink) error { return nil })
		},
	})
	defer f.StopAll()

	// Explicit per-spawn stamp wins even with a swarm-wide scope set.
	f.SetActiveSession("scope-session")
	a, err := f.SpawnReq(context.Background(), SpawnRequest{Task: "stamped task", SessionID: "20260712-req-wins"})
	if err != nil {
		t.Fatal(err)
	}
	if a.SessionID != "20260712-req-wins" {
		t.Errorf("agent SessionID = %q, want the per-spawn stamp", a.SessionID)
	}

	// No per-spawn stamp → the SetActiveSession scope.
	b, err := f.SpawnReq(context.Background(), SpawnRequest{Task: "scoped task"})
	if err != nil {
		t.Fatal(err)
	}
	if b.SessionID != "scope-session" {
		t.Errorf("fallback SessionID = %q, want the active-session scope", b.SessionID)
	}

	// The stamp is durable: meta.json carries it.
	m, err := readAgentMeta(f.agentStateDir(a.ID))
	if err != nil {
		t.Fatal(err)
	}
	if m.SessionID != "20260712-req-wins" {
		t.Errorf("meta.json session_id = %q, want the stamp persisted", m.SessionID)
	}
}
