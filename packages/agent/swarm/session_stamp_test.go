package swarm

import (
	"context"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// TestSpawnReqStampsSessionID pins the R5.7 fix: the per-spawn SessionID is
// the stamp (a daemon hosts many sessions, so nothing swarm-wide can be
// trusted), an absent one leaves the agent unscoped rather than guessing, and
// the stamp persists to meta.json so it survives a reload.
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

	a, err := f.SpawnReq(context.Background(), SpawnRequest{Task: "stamped task", SessionID: "20260712-req-wins"})
	if err != nil {
		t.Fatal(err)
	}
	if a.SessionID != "20260712-req-wins" {
		t.Errorf("agent SessionID = %q, want the per-spawn stamp", a.SessionID)
	}

	// No per-spawn stamp → unscoped, and therefore visible from everywhere.
	// The alternative — inheriting some swarm-wide "current" session — is what
	// this replaced: it is wrong for every session but the last one to set it.
	b, err := f.SpawnReq(context.Background(), SpawnRequest{Task: "unstamped task"})
	if err != nil {
		t.Fatal(err)
	}
	if b.SessionID != "" {
		t.Errorf("unstamped SessionID = %q, want empty", b.SessionID)
	}
	// Visible from a scope it was never stamped with — and note what is NOT
	// there: `a` carries a different session and is correctly filtered out, so
	// this asserts the pass-through without also asserting a broken filter.
	if got := snapshotIDs(f.SnapshotFor("any-session")); len(got) != 1 || got[0] != b.ID {
		t.Errorf("unstamped agent should be the only one visible from a foreign scope; ids = %v", got)
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
