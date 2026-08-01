package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/testsupport"
)

// The tasks pane, scoped to the session asking for it.
//
// The swarm is workspace-global and its agents outlive the run that spawned
// them, so before this every conversation inherited every other conversation's
// background work — including yesterday's, in another repo. The filter existed
// and the agents were correctly stamped; nothing ever armed it.
//
// It could not have been armed as it stood. The scope was a single mutable
// field on the shared Swarm, and a workspace serves many sessions at once: two
// browser tabs on the web daemon would have taken turns renarrowing each
// other's dashboard. The fix is that scope is an argument.

func scopeSwarm(t *testing.T) *swarm.Swarm {
	t.Helper()
	root := testsupport.TempDir(t)
	return swarm.New(swarm.Config{
		Root: root, RepoRoot: root,
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			return swarm.RunnerFunc(func(ctx context.Context, _ swarm.Sink) error {
				<-ctx.Done()
				return ctx.Err()
			})
		},
	})
}

// The property the old design could not express: one workspace answers two
// sessions differently, at the same moment, with no state change between the
// two calls.
func TestTwoSessionsSeeOnlyTheirOwnTasks(t *testing.T) {
	sw := scopeSwarm(t)
	w := &Workspace{swarm: sw}

	a, err := sw.SpawnReq(context.Background(), swarm.SpawnRequest{Task: "for A", SessionID: "sess-A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := sw.SpawnReq(context.Background(), swarm.SpawnRequest{Task: "for B", SessionID: "sess-B"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = sw.Stop(a.ID)
		_ = sw.Stop(b.ID)
		a.Wait()
		b.Wait()
	}()

	listA := w.taskList("sess-A")
	if len(listA.Tasks) != 1 || listA.Tasks[0].ID != a.ID {
		t.Errorf("session A saw %v; want only %s", listedIDs(listA), a.ID)
	}
	listB := w.taskList("sess-B")
	if len(listB.Tasks) != 1 || listB.Tasks[0].ID != b.ID {
		t.Errorf("session B saw %v; want only %s", listedIDs(listB), b.ID)
	}
}

// A session with no agents of its own must get an empty pane, not the
// workspace's. This is the whole of the reported symptom: eleven agents from
// one project showing up in every session in every repo.
func TestASessionWithNoTasksOfItsOwnSeesNone(t *testing.T) {
	sw := scopeSwarm(t)
	w := &Workspace{swarm: sw}

	a, err := sw.SpawnReq(context.Background(), swarm.SpawnRequest{Task: "elsewhere", SessionID: "sess-other"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sw.Stop(a.ID); a.Wait() }()

	if got := w.taskList("sess-fresh"); len(got.Tasks) != 0 {
		t.Errorf("a fresh session saw %d task(s); want none", len(got.Tasks))
	}
	// The pane must not even be offered — an empty tab is still noise. Read the
	// real config rather than stubbing it: with auto-swarm on the pane is
	// offered to everyone by design, and asserting otherwise would fail on the
	// machine of anyone who turned it on.
	if !config.AutoSwarmEnabled() && w.hasTasks("sess-fresh") {
		t.Error("a fresh session was offered a tasks pane with nothing in it")
	}
	if !w.hasTasks("sess-other") {
		t.Error("the owning session was not offered its own tasks pane")
	}
}

// Spawning from the board used to send no session at all, so every agent
// started from the pane landed unscoped and reappeared in every session
// forever — the same defect the pane was about to stop showing.
func TestBoardSpawnStampsTheActingSession(t *testing.T) {
	sw := scopeSwarm(t)
	w := &Workspace{ctx: context.Background(), swarm: sw}

	if err := w.taskAction("sess-A", "spawn", map[string]string{"task": "from the board"}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer sw.StopAll()

	if got := w.taskList("sess-A"); len(got.Tasks) != 1 {
		t.Errorf("the spawning session saw %d of its own task(s); want 1", len(got.Tasks))
	}
	if got := w.taskList("sess-B"); len(got.Tasks) != 0 {
		t.Errorf("another session saw %d task(s) from a board spawn; want 0", len(got.Tasks))
	}
}

// An agent whose meta.json predates the session stamp stays visible from
// everywhere. Scoping must not orphan work that is still running.
func TestAnUnstampedAgentStaysVisibleEverywhere(t *testing.T) {
	sw := scopeSwarm(t)
	w := &Workspace{swarm: sw}

	a, err := sw.Spawn(context.Background(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sw.Stop(a.ID); a.Wait() }()

	for _, sess := range []string{"", "sess-A", "sess-B"} {
		if got := w.taskList(sess); len(got.Tasks) != 1 {
			t.Errorf("session %q saw %d task(s); an unstamped agent must stay reachable", sess, len(got.Tasks))
		}
	}
}

func listedIDs(l *ctrlproto.TaskList) []string {
	out := make([]string, 0, len(l.Tasks))
	for _, t := range l.Tasks {
		out = append(out, t.ID)
	}
	return out
}

// Archive is reachable as a board action, and afterwards the agent is gone from
// terva entirely — not moved to another view, not counted somewhere. That total
// disappearance IS the contract; the record's only remaining reader is the
// filesystem.
func TestArchiveActionRemovesTheAgentFromTerva(t *testing.T) {
	sw := scopeSwarm(t)
	w := &Workspace{ctx: context.Background(), swarm: sw}

	a, err := sw.SpawnReq(context.Background(), swarm.SpawnRequest{Task: "finish me", SessionID: "sess-A"})
	if err != nil {
		t.Fatal(err)
	}
	_ = sw.Stop(a.ID)
	a.Wait()

	if before := w.taskList("sess-A"); len(before.Tasks) != 1 {
		t.Fatalf("before archive: %d task(s); want 1", len(before.Tasks))
	}
	if err := w.taskAction("sess-A", "archive", map[string]string{"id": a.ID}); err != nil {
		t.Fatalf("archive action: %v", err)
	}

	// Gone from its own session, from an unscoped listing, and from the swarm.
	if got := w.taskList("sess-A"); len(got.Tasks) != 0 {
		t.Errorf("archived agent still listed for its session: %d", len(got.Tasks))
	}
	if got := w.taskList(""); len(got.Tasks) != 0 {
		t.Errorf("archived agent visible in an unscoped listing: %d", len(got.Tasks))
	}
	if sw.Get(a.ID) != nil {
		t.Error("archived agent is still in the swarm")
	}
	// And every action that could reach it now fails, including archive itself.
	for _, action := range []string{"archive", "resume", "stop", "send"} {
		if err := w.taskAction("sess-A", action, map[string]string{"id": a.ID, "text": "x"}); err == nil {
			t.Errorf("%q reached an archived agent; want an error", action)
		}
	}
}
