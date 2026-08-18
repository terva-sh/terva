package workspace

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
)

// Under --swarm-worktrees a sub-agent's files land in a leased worktree that
// outlives it (release keeps the checkout for review — carrier_swarm_worktree.go),
// and the coordinator never looks there. The system prompt says what that means;
// the recap has to supply the actual path, or the rule is unactionable.
//
// The failure it prevents is the confident one: the child says "I wrote
// packages/foo/bar.go", the coordinator reads its own untouched copy, and
// reports the work undone.
func TestRecapNamesTheLeasedWorktree(t *testing.T) {
	s := &wsSession{
		agent: core.NewAgent(nil, "fake-model", "", core.Registry{}),
		hub:   &wsHub{},
	}
	s.turnCancel = func(error) {}

	s.flushSwarmSummary([]*swarmWatchEntry{{
		agent: &swarm.Agent{
			ID:     "port-the-parser-114000",
			Dir:    "/home/dev/.terva/worktrees/port-the-parser-114000",
			Leased: true,
		},
		task: "Port the parser and leave the result in place",
		done: true,
	}})

	recap := waitQueuedRecap(t, s)[0]
	if !strings.Contains(recap, "/home/dev/.terva/worktrees/port-the-parser-114000") {
		t.Errorf("the recap does not name the leased worktree, so its file paths point nowhere:\n%s", recap)
	}
	if !strings.Contains(recap, "worktree:") {
		t.Errorf("the path is present but unlabelled; a bare path reads as one more field:\n%s", recap)
	}
}

// A shared-tree sub-agent's Dir IS the coordinator's own working directory.
// Printing it would state, per agent, that the files are somewhere other than
// where the coordinator is standing — the exact confusion this line exists to
// remove, inverted.
func TestRecapOmitsTheWorktreeWhenTheTreeIsShared(t *testing.T) {
	s := &wsSession{
		agent: core.NewAgent(nil, "fake-model", "", core.Registry{}),
		hub:   &wsHub{},
	}
	s.turnCancel = func(error) {}

	s.flushSwarmSummary([]*swarmWatchEntry{{
		agent: &swarm.Agent{
			ID:     "sweep-the-logs-114500",
			Dir:    "/home/dev/project", // the host tree, not a lease
			Leased: false,
		},
		task: "Sweep the logs",
		done: true,
	}})

	recap := waitQueuedRecap(t, s)[0]
	if strings.Contains(recap, "worktree:") {
		t.Errorf("an unleased sub-agent shares the host tree; the recap must not claim a worktree:\n%s", recap)
	}
}

// The recap reads Leased off the snapshot, so the snapshot has to carry it.
// Dir alone cannot answer the question: it is populated either way, and the two
// cases call for opposite advice.
func TestSnapshotCarriesTheLeaseFlag(t *testing.T) {
	leased := (&swarm.Agent{ID: "a", Dir: "/lease", Leased: true}).Snapshot()
	if !leased.Leased || leased.Dir != "/lease" {
		t.Errorf("Snapshot dropped the lease: Leased=%v Dir=%q", leased.Leased, leased.Dir)
	}
	shared := (&swarm.Agent{ID: "b", Dir: "/repo"}).Snapshot()
	if shared.Leased {
		t.Error("a shared-tree agent must not snapshot as leased")
	}
}
