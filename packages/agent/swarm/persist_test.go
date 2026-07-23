package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// TestSpawnWritesMetaJSON asserts the durability contract: every
// successful Spawn leaves a meta.json on disk with the agent's
// identity bits. Without this, Reload on the next terva launch can't
// find the agent and the user loses access to the worktree.
func TestSpawnWritesMetaJSON(t *testing.T) {
	root := testsupport.TempDir(t)
	f := New(Config{
		Root:     root,
		RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, _ Sink) error {
				<-ctx.Done()
				return ctx.Err()
			})
		},
	})
	defer f.StopAll()

	a, err := f.Spawn(context.Background(), "investigate widget")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	// Read meta.json straight off disk.
	metaBytes, err := os.ReadFile(filepath.Join(root, "agents", a.ID, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var got agentMeta
	if err := json.Unmarshal(metaBytes, &got); err != nil {
		t.Fatalf("parse meta.json: %v", err)
	}
	if got.ID != a.ID {
		t.Errorf("meta.ID = %q; want %q", got.ID, a.ID)
	}
	if got.Task != "investigate widget" {
		t.Errorf("meta.Task = %q", got.Task)
	}
	if got.Dir != a.Dir {
		t.Errorf("meta paths drifted: %+v vs agent %+v", got, a)
	}
	if got.InboxPath == "" || got.EventLogPath == "" || got.SessionPath == "" {
		t.Errorf("meta paths empty: %+v", got)
	}
	if got.Started.IsZero() {
		t.Error("meta.Started is zero")
	}
}

// TestReloadRebuildsDetachedAgents simulates a terva restart by spawning
// in one Swarm, throwing it away, then opening a fresh Swarm against
// the same root and calling Reload. The user-visible state — id,
// task, branch, dir — must come back, and status must be Detached so
// the dashboard can offer resume.
func TestReloadRebuildsDetachedAgents(t *testing.T) {
	root := testsupport.TempDir(t)

	// First incarnation: spawn two agents, then drop the supervisor.
	first := New(Config{
		Root:     root,
		RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, _ Sink) error {
				<-ctx.Done()
				return ctx.Err()
			})
		},
	})
	a1, err := first.Spawn(context.Background(), "alpha task")
	if err != nil {
		t.Fatalf("spawn a1: %v", err)
	}
	a2, err := first.Spawn(context.Background(), "beta task")
	if err != nil {
		t.Fatalf("spawn a2: %v", err)
	}
	first.StopAll()
	// Wait briefly for the runner goroutines to settle so their
	// stop event reaches the log before we move on. Reload itself
	// doesn't need this; it makes the assertions below deterministic.
	a1.Wait()
	a2.Wait()

	// Second incarnation against the same root.
	second := New(Config{
		Root:     root,
		RepoRoot: root,
	})
	loaded, errs := second.Reload()
	if len(errs) > 0 {
		t.Fatalf("reload errs: %v", errs)
	}
	if loaded != 2 {
		t.Fatalf("loaded = %d; want 2", loaded)
	}
	snap := second.SnapshotAll()
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d; want 2", len(snap))
	}
	ids := map[string]AgentSnapshot{}
	for _, s := range snap {
		ids[s.ID] = s
	}
	if _, ok := ids[a1.ID]; !ok {
		t.Errorf("reloaded set missing %s", a1.ID)
	}
	if _, ok := ids[a2.ID]; !ok {
		t.Errorf("reloaded set missing %s", a2.ID)
	}
	for _, s := range snap {
		if s.Status != StatusDetached {
			t.Errorf("agent %s status = %q; want detached", s.ID, s.Status)
		}
		if s.Task == "" {
			t.Errorf("agent %s lost its task", s.ID)
		}
	}
}

// TestReloadIsIdempotent calls Reload twice in a row and asserts the
// second call neither duplicates rows nor errors.
func TestReloadIsIdempotent(t *testing.T) {
	root := testsupport.TempDir(t)
	first := New(Config{
		Root: root, RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, _ Sink) error { <-ctx.Done(); return ctx.Err() })
		},
	})
	if _, err := first.Spawn(context.Background(), "only one"); err != nil {
		t.Fatal(err)
	}
	first.StopAll()

	second := New(Config{Root: root, RepoRoot: root})
	loaded1, _ := second.Reload()
	loaded2, errs := second.Reload()
	if len(errs) > 0 {
		t.Fatalf("errs on second reload: %v", errs)
	}
	if loaded1 != 1 || loaded2 != 0 {
		t.Fatalf("loaded counts = (%d, %d); want (1, 0)", loaded1, loaded2)
	}
	if got := len(second.SnapshotAll()); got != 1 {
		t.Fatalf("snapshot len = %d; want 1", got)
	}
}

// TestReloadReplaysTranscriptFromEventLog drops a curated events.jsonl
// next to a meta.json and checks that the transcript surfaces in the
// reloaded agent. This is the user-facing payoff of Reload: opening
// /swarm logs <id> after a restart shows what the agent said before.
func TestReloadReplaysTranscriptFromEventLog(t *testing.T) {
	root := testsupport.TempDir(t)
	id := "alpha-9"
	stateDir := filepath.Join(root, "agents", id)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// meta.json
	m := agentMeta{
		ID: id, Task: "do thing",
		Dir: root, Started: time.Now().Add(-time.Hour),
		InboxPath:    filepath.Join(stateDir, "in.sock"),
		EventLogPath: filepath.Join(stateDir, "events.jsonl"),
		SessionPath:  filepath.Join(stateDir, "session.json"),
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(stateDir, "meta.json"), mb, 0o644); err != nil {
		t.Fatal(err)
	}
	// Event log with one assistant turn and a graceful stop.
	log, err := OpenEventLog(m.EventLogPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = log.Append(NewEvent("assistant_message", map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "hello from the past"}},
	}))
	_ = log.Append(NewEvent("agent_stopped", map[string]any{"reason": "shutdown"}))
	_ = log.Close()

	f := New(Config{Root: root, RepoRoot: root})
	loaded, errs := f.Reload()
	if len(errs) > 0 || loaded != 1 {
		t.Fatalf("reload loaded=%d errs=%v", loaded, errs)
	}
	a := f.Get(id)
	if a == nil {
		t.Fatal("reloaded agent not found")
	}
	// Status should reflect the lifecycle terminator we wrote.
	if a.Status() != StatusDone {
		t.Errorf("status = %q; want done (offline)", a.Status())
	}
	got := a.Transcript()
	found := false
	for _, line := range got {
		if line == "hello from the past" {
			found = true
		}
	}
	if !found {
		t.Errorf("transcript missing replayed line: %v", got)
	}
}

// TestReloadSkipsBareDirsAndCorruptMeta ensures one bad meta.json
// doesn't blow up the whole reload. Directories with no meta.json
// at all are silently ignored (Spawn that failed mid-way leaves
// these); corrupt meta files are reported as errors but don't stop
// the rest of the load.
func TestReloadSkipsBareDirsAndCorruptMeta(t *testing.T) {
	root := testsupport.TempDir(t)
	agentsDir := filepath.Join(root, "agents")
	// Bare directory, no meta.json.
	if err := os.MkdirAll(filepath.Join(agentsDir, "leftover"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Directory with garbage meta.json.
	if err := os.MkdirAll(filepath.Join(agentsDir, "corrupt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "corrupt", "meta.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	// One good agent.
	good := "good-1"
	stateDir := filepath.Join(agentsDir, good)
	_ = os.MkdirAll(stateDir, 0o755)
	m := agentMeta{ID: good, Task: "x", Dir: "/tmp/x", Started: time.Now()}
	mb, _ := json.MarshalIndent(m, "", "  ")
	_ = os.WriteFile(filepath.Join(stateDir, "meta.json"), mb, 0o644)

	f := New(Config{Root: root, RepoRoot: root})
	loaded, errs := f.Reload()
	if loaded != 1 {
		t.Errorf("loaded = %d; want 1", loaded)
	}
	if len(errs) != 1 {
		t.Errorf("errs len = %d; want 1 (the corrupt entry): %v", len(errs), errs)
	}
}

// TestResumeRestartsRunnerOnSameSession is the headline test. We:
//
//  1. Spawn an agent with a counting runner (records each Run call).
//  2. Stop it.
//  3. Resume it.
//  4. Assert Run was called twice — once for spawn, once for resume —
//     against the same SessionPath / InboxPath / Dir.
func TestResumeRestartsRunnerOnSameSession(t *testing.T) {
	root := testsupport.TempDir(t)
	var (
		mu       sync.Mutex
		runs     []*Agent
		runsDone int32
	)
	f := New(Config{
		Root: root, RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, sink Sink) error {
				mu.Lock()
				runs = append(runs, a)
				mu.Unlock()
				sink.Activity("ran")
				atomic.AddInt32(&runsDone, 1)
				<-ctx.Done()
				return ctx.Err()
			})
		},
	})
	defer f.StopAll()

	a, err := f.Spawn(context.Background(), "do thing")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	originalSession := a.SessionPath
	originalDir := a.Dir
	originalInbox := a.InboxPath

	// Wait for the runner to record its presence, then stop the agent.
	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(&runsDone) < 1 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if err := f.Stop(a.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	a.Wait()
	if a.Status() != StatusKilled {
		t.Fatalf("post-stop status = %q; want killed", a.Status())
	}

	// Now resume.
	a2, err := f.Resume(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if a2.ID != a.ID {
		t.Errorf("resume returned different id: %s vs %s", a2.ID, a.ID)
	}
	if a2.SessionPath != originalSession {
		t.Errorf("resume changed session path: %s vs %s", a2.SessionPath, originalSession)
	}
	if a2.Dir != originalDir {
		t.Errorf("resume changed dir: %s vs %s", a2.Dir, originalDir)
	}
	if a2.InboxPath != originalInbox {
		t.Errorf("resume changed inbox path: %s vs %s", a2.InboxPath, originalInbox)
	}
	// Two runner invocations: spawn + resume.
	deadline = time.Now().Add(time.Second)
	for atomic.LoadInt32(&runsDone) < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(runs) != 2 {
		t.Fatalf("runner invocations = %d; want 2", len(runs))
	}
	if runs[1].ID != a.ID {
		t.Errorf("second run had wrong id: %s", runs[1].ID)
	}
	if got := len(f.SnapshotAll()); got != 1 {
		t.Errorf("snapshot len = %d; want 1 (in-place replace)", got)
	}
}

// TestResumeSetsResumingFlag pins the contract execRunner relies on:
// after Resume, the new Agent has Resuming == true; after a fresh
// Spawn it's false. The runner branches on this to decide whether
// to pass the original Task as a positional argv to the child, so
// getting it wrong silently re-runs the initial task on every
// resume and surfaces "agent busy; send 'cancel' first" between
// assistant messages.
func TestResumeSetsResumingFlag(t *testing.T) {
	root := testsupport.TempDir(t)
	f := New(Config{
		Root: root, RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, _ Sink) error { <-ctx.Done(); return ctx.Err() })
		},
	})
	defer f.StopAll()

	a, err := f.Spawn(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if a.Resuming {
		t.Fatal("fresh Spawn produced Resuming==true; want false")
	}
	if err := f.Stop(a.ID); err != nil {
		t.Fatal(err)
	}
	a.Wait()

	a2, err := f.Resume(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !a2.Resuming {
		t.Error("Resume produced Resuming==false; want true so the runner skips the duplicate initial-task argv")
	}
}

// TestResumeRejectsRunningAgent prevents the user from double-running
// an agent: two runners on the same session.json would race.
func TestResumeRejectsRunningAgent(t *testing.T) {
	root := testsupport.TempDir(t)
	f := New(Config{
		Root: root, RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, _ Sink) error { <-ctx.Done(); return ctx.Err() })
		},
	})
	defer f.StopAll()
	a, err := f.Spawn(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	// Wait for the runner to actually start.
	deadline := time.Now().Add(time.Second)
	for a.Status() != StatusRunning && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if _, err := f.Resume(context.Background(), a.ID); err == nil {
		t.Fatal("resume on running agent did not error")
	}
}

// TestResumeAfterReload exercises the full lifecycle the user cares
// about: spawn in process A, exit A, start process B, Reload, Resume.
// The resumed agent must keep its id and start producing new output
// against the same on-disk state.
//
// Note on transcript persistence: in production the runner is
// execRunner, which writes every event to events.jsonl, and Reload's
// replayEventsIntoAgent rebuilds the transcript from there. The fake
// RunnerFunc in this test calls sink.Transcript directly (which only
// touches in-memory state), so the "first run" line is *expected* to
// be lost across the restart — the in-memory Agent went away. We
// have TestReloadReplaysTranscriptFromEventLog covering the
// log-replay path with a curated events.jsonl.
func TestResumeAfterReload(t *testing.T) {
	root := testsupport.TempDir(t)
	// Process A
	a := New(Config{
		Root: root, RepoRoot: root,
		NewRunner: func(ag *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, sink Sink) error {
				sink.Transcript("first run for " + ag.ID)
				<-ctx.Done()
				return ctx.Err()
			})
		},
	})
	ag, err := a.Spawn(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	// Let the runner record its transcript line before we tear it down.
	deadline := time.Now().Add(time.Second)
	for len(ag.Transcript()) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	id := ag.ID
	a.StopAll()
	ag.Wait()

	// Process B: a new Swarm against the same root.
	resumed := make(chan struct{}, 1)
	b := New(Config{
		Root: root, RepoRoot: root,
		NewRunner: func(ag *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, sink Sink) error {
				sink.Transcript("second run for " + ag.ID)
				select {
				case resumed <- struct{}{}:
				default:
				}
				<-ctx.Done()
				return ctx.Err()
			})
		},
	})
	if loaded, errs := b.Reload(); loaded != 1 || len(errs) > 0 {
		t.Fatalf("reload loaded=%d errs=%v", loaded, errs)
	}
	a2, err := b.Resume(context.Background(), id)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer b.StopAll()

	select {
	case <-resumed:
	case <-time.After(time.Second):
		t.Fatal("resume runner did not start")
	}
	if a2.ID != id {
		t.Errorf("resume id = %s; want %s", a2.ID, id)
	}

	// Wait for the new transcript line to land.
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, l := range a2.Transcript() {
			if l == "second run for "+id {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("resumed transcript missing fresh line: %v", a2.Transcript())
}

// TestSpawnReqPersistsModel confirms that the per-agent model
// override is captured at Spawn time, surfaced via Snapshot, and
// written to meta.json so a later Reload + Resume reuses it.
func TestSpawnReqPersistsModel(t *testing.T) {
	root := testsupport.TempDir(t)
	f := New(Config{
		Root: root, RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, _ Sink) error { <-ctx.Done(); return ctx.Err() })
		},
	})
	a, err := f.SpawnReq(context.Background(), SpawnRequest{
		Task: "x", Model: "claude-sonnet-4-5", Provider: "anthropic",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if a.Model != "claude-sonnet-4-5" || a.Provider != "anthropic" {
		t.Fatalf("agent fields = (%q,%q); want (claude-sonnet-4-5, anthropic)", a.Model, a.Provider)
	}
	snap := a.Snapshot()
	if snap.Model != "claude-sonnet-4-5" || snap.Provider != "anthropic" {
		t.Fatalf("snapshot = (%q,%q); model fields not surfaced", snap.Model, snap.Provider)
	}

	// Stop so we can read meta.json without racing the run loop.
	if err := f.Stop(a.ID); err != nil {
		t.Fatal(err)
	}
	a.Wait()

	metaBytes, err := os.ReadFile(filepath.Join(root, "agents", a.ID, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var got agentMeta
	if err := json.Unmarshal(metaBytes, &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "claude-sonnet-4-5" || got.Provider != "anthropic" {
		t.Errorf("meta = (%q,%q); want model + provider persisted", got.Model, got.Provider)
	}

	// Reload in a fresh Swarm and confirm the detached agent still
	// carries the model/provider so Resume can route the child
	// subprocess back to the same model.
	g := New(Config{Root: root, RepoRoot: root})
	if loaded, errs := g.Reload(); loaded != 1 || len(errs) > 0 {
		t.Fatalf("reload loaded=%d errs=%v", loaded, errs)
	}
	re := g.Get(a.ID)
	if re == nil {
		t.Fatal("reloaded agent missing")
	}
	if re.Model != "claude-sonnet-4-5" || re.Provider != "anthropic" {
		t.Errorf("reloaded fields = (%q,%q); want preserved", re.Model, re.Provider)
	}
}

// TestSpawnRecordsLeaseAndApproval pins the two inputs to a worker's posture
// policy: Spawn sets Leased iff AcquireWorktree granted a dedicated dir, records
// an explicit Approval override, and both survive a Reload — so a resumed worker
// keeps the posture it ran with (a leased worker stays autonomous; an override
// persists).
func TestSpawnRecordsLeaseAndApproval(t *testing.T) {
	root := testsupport.TempDir(t)
	leaseDir := testsupport.TempDir(t)
	f := New(Config{
		Root: root, RepoRoot: root,
		AcquireWorktree: func(ctx context.Context, req WorktreeReq) (WorktreeLease, error) {
			return WorktreeLease{Dir: leaseDir, Release: func() {}}, nil
		},
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, _ Sink) error { <-ctx.Done(); return ctx.Err() })
		},
	})
	a, err := f.SpawnReq(context.Background(), SpawnRequest{Task: "x", Approval: "workspace"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if !a.Leased {
		t.Error("a dedicated worktree lease must set Agent.Leased")
	}
	if a.Dir != leaseDir {
		t.Errorf("agent dir = %q, want the lease %q", a.Dir, leaseDir)
	}
	if a.Approval != "workspace" {
		t.Errorf("Agent.Approval = %q, want the SpawnRequest override", a.Approval)
	}

	if err := f.Stop(a.ID); err != nil {
		t.Fatal(err)
	}
	a.Wait()

	// Persisted to meta.json...
	m, err := readAgentMeta(f.agentStateDir(a.ID))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if !m.Leased || m.Approval != "workspace" {
		t.Errorf("meta lost posture inputs: leased=%v approval=%q", m.Leased, m.Approval)
	}

	// ...and carried across a Reload, so Resume keeps the posture.
	g := New(Config{Root: root, RepoRoot: root})
	if loaded, errs := g.Reload(); loaded != 1 || len(errs) > 0 {
		t.Fatalf("reload loaded=%d errs=%v", loaded, errs)
	}
	re := g.Get(a.ID)
	if re == nil || !re.Leased || re.Approval != "workspace" {
		t.Errorf("reloaded agent lost posture inputs: %+v", re)
	}
}

// TestSpawnUnleasedIsNotLeased: with no AcquireWorktree hook the agent shares the
// host cwd and must NOT read as leased — the guard that keeps an unleased worker
// from silently going yolo in the shared working directory.
func TestSpawnUnleasedIsNotLeased(t *testing.T) {
	root := testsupport.TempDir(t)
	f := New(Config{
		Root: root, RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, _ Sink) error { <-ctx.Done(); return ctx.Err() })
		},
	})
	a, err := f.SpawnReq(context.Background(), SpawnRequest{Task: "x"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if a.Leased {
		t.Error("an agent sharing RepoRoot must not be Leased")
	}
	if a.Dir != root {
		t.Errorf("unleased agent dir = %q, want RepoRoot %q", a.Dir, root)
	}
	_ = f.Stop(a.ID)
	a.Wait()
}

// TestSwarmAgentArgsIncludesModelFlags pins the argv contract that
// connects the supervisor's per-agent model override to the child's
// --model / --provider flag set. Adding a new path to argv without
// updating this assertion is the failure mode this catches.
func TestSwarmAgentArgsIncludesModelFlags(t *testing.T) {
	args := swarmAgentArgs(swarmAgentArgsOpts{
		Exe: "/terva", Dir: "/wt", SessionPath: "/s.json", InboxPath: "/in.sock",
		Task: "do x", Model: "gpt-5", Provider: "openai",
	})
	want := []string{"--model", "gpt-5", "--provider", "openai"}
	for i := 0; i+1 < len(want); i += 2 {
		flag, value := want[i], want[i+1]
		found := false
		for j := 0; j+1 < len(args); j++ {
			if args[j] == flag && args[j+1] == value {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("argv missing %s %s: %v", flag, value, args)
		}
	}
	// And the task still ends up positional last.
	if args[len(args)-1] != "do x" {
		t.Errorf("task should be last positional; argv=%v", args)
	}
}

// TestStopOnDetachedAgentIsNoopAndDoesNotPanic regression-tests the
// segfault that hit when the user pressed 'k' on a reloaded
// (detached) agent: Swarm.Stop unconditionally called a.cancel(),
// but buildDetachedAgent never assigns a cancel func because there's
// no in-process runner to cancel. The fix is to short-circuit Stop
// for StatusDetached and — belt-and-braces — nil-check a.cancel.
func TestStopOnDetachedAgentIsNoopAndDoesNotPanic(t *testing.T) {
	root := testsupport.TempDir(t)
	// Build a detached agent the same way Reload does: drop a
	// meta.json on disk, then ask a fresh Swarm to pick it up.
	id := "alpha-1"
	stateDir := filepath.Join(root, "agents", id)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := agentMeta{
		ID: id, Task: "t",
		Dir:          root,
		Started:      time.Now().Add(-time.Hour),
		InboxPath:    filepath.Join(stateDir, "in.sock"),
		EventLogPath: filepath.Join(stateDir, "events.jsonl"),
		SessionPath:  filepath.Join(stateDir, "session.json"),
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(stateDir, "meta.json"), mb, 0o644); err != nil {
		t.Fatal(err)
	}

	f := New(Config{Root: root, RepoRoot: root})
	if loaded, errs := f.Reload(); loaded != 1 || len(errs) > 0 {
		t.Fatalf("reload loaded=%d errs=%v", loaded, errs)
	}
	a := f.Get(id)
	if a == nil {
		t.Fatal("detached agent missing from swarm")
	}
	if a.Status() != StatusDetached {
		t.Fatalf("setup: agent status = %q; want detached", a.Status())
	}

	// The real assertion: Stop returns cleanly without panicking.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Stop on detached agent panicked: %v", r)
		}
	}()
	if err := f.Stop(id); err != nil {
		t.Fatalf("Stop on detached agent: %v", err)
	}
	// Status must remain Detached (Stop is a no-op).
	if got := a.Status(); got != StatusDetached {
		t.Errorf("after Stop status = %q; want detached (no-op)", got)
	}

	// StopAll, which the cli defers on exit, must also be safe.
	f.StopAll()
}

// TestRemoveAlsoCleansStateDir ensures Remove deletes the on-disk
// state directory in addition to the worktree, so a removed agent
// doesn't reappear on the next Reload.
func TestRemoveAlsoCleansStateDir(t *testing.T) {
	root := testsupport.TempDir(t)
	f := New(Config{
		Root: root, RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, _ Sink) error { <-ctx.Done(); return ctx.Err() })
		},
	})
	a, err := f.Spawn(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "agents", a.ID)
	if _, err := os.Stat(filepath.Join(stateDir, "meta.json")); err != nil {
		t.Fatalf("meta.json missing pre-remove: %v", err)
	}
	if err := f.Stop(a.ID); err != nil {
		t.Fatal(err)
	}
	a.Wait()
	if err := f.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state dir should be gone after remove; got %v", err)
	}

	// A fresh Swarm + Reload should find nothing.
	g := New(Config{Root: root, RepoRoot: root})
	if loaded, _ := g.Reload(); loaded != 0 {
		t.Fatalf("reload after remove loaded=%d; want 0", loaded)
	}
}

// TestActiveSessionScopesSnapshotAll proves the session filter the
// user asked for: a single Swarm with agents spawned under two
// different active sessions only surfaces the agents matching the
// currently-active session via SnapshotAll. Switching the active
// session re-narrows the view without touching agent state.
func TestActiveSessionScopesSnapshotAll(t *testing.T) {
	root := testsupport.TempDir(t)
	f := New(Config{
		Root: root, RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, _ Sink) error { <-ctx.Done(); return ctx.Err() })
		},
	})

	// Spawn one agent under session A and one under session B.
	f.SetActiveSession("sess-A")
	aA, err := f.Spawn(context.Background(), "task A")
	if err != nil {
		t.Fatal(err)
	}
	f.SetActiveSession("sess-B")
	aB, err := f.Spawn(context.Background(), "task B")
	if err != nil {
		t.Fatal(err)
	}

	// Both agents must carry the session id they were spawned under,
	// regardless of any later SetActiveSession.
	if aA.SessionID != "sess-A" {
		t.Errorf("aA.SessionID = %q; want sess-A", aA.SessionID)
	}
	if aB.SessionID != "sess-B" {
		t.Errorf("aB.SessionID = %q; want sess-B", aB.SessionID)
	}

	// Active session B → only aB visible.
	only := snapshotIDs(f.SnapshotAll())
	if len(only) != 1 || only[0] != aB.ID {
		t.Errorf("scoped to sess-B, snapshot ids = %v; want [%s]", only, aB.ID)
	}

	// Switch back to A → only aA visible.
	f.SetActiveSession("sess-A")
	only = snapshotIDs(f.SnapshotAll())
	if len(only) != 1 || only[0] != aA.ID {
		t.Errorf("scoped to sess-A, snapshot ids = %v; want [%s]", only, aA.ID)
	}

	// Clear the scope → both visible.
	f.SetActiveSession("")
	all := snapshotIDs(f.SnapshotAll())
	if len(all) != 2 {
		t.Errorf("unscoped snapshot ids = %v; want both agents", all)
	}

	// Cleanup.
	_ = f.Stop(aA.ID)
	_ = f.Stop(aB.ID)
	aA.Wait()
	aB.Wait()
}

// TestSessionIDPersistsAcrossReload confirms the SessionID field
// survives a Swarm restart: an agent spawned under session A in one
// Swarm instance is still SessionID="sess-A" after a fresh New +
// Reload reads it back from meta.json. Without persistence, the
// scope filter would forget which session owned each agent after
// a terva restart and the dashboard would leak everything again.
func TestSessionIDPersistsAcrossReload(t *testing.T) {
	root := testsupport.TempDir(t)
	mkSwarm := func() *Swarm {
		return New(Config{
			Root: root, RepoRoot: root,
			NewRunner: func(a *Agent) Runner {
				return RunnerFunc(func(ctx context.Context, _ Sink) error { <-ctx.Done(); return ctx.Err() })
			},
		})
	}

	f := mkSwarm()
	f.SetActiveSession("sess-keep")
	a, err := f.Spawn(context.Background(), "persist me")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Stop(a.ID)
	a.Wait()

	// Fresh Swarm, same root, reload from disk.
	g := mkSwarm()
	if loaded, errs := g.Reload(); loaded != 1 || len(errs) > 0 {
		t.Fatalf("reload loaded=%d errs=%v; want 1 / no errs", loaded, errs)
	}
	g.SetActiveSession("sess-keep")
	got := snapshotIDs(g.SnapshotAll())
	if len(got) != 1 || got[0] != a.ID {
		t.Errorf("after reload + scope to sess-keep, ids = %v; want [%s]", got, a.ID)
	}

	// Scope to a different session: agent must be hidden.
	g.SetActiveSession("sess-other")
	if got := snapshotIDs(g.SnapshotAll()); len(got) != 0 {
		t.Errorf("scoped to other session, ids = %v; want []", got)
	}
}

// TestEmptySessionIDIsVisibleFromAnyScope pins the backward-compat
// rule: agents whose meta.json predates the SessionID field (or who
// were spawned without an active session, e.g. via a test rig or
// scripted caller) carry SessionID == "" and remain visible from
// every scope. Otherwise the schema bump would orphan every
// pre-existing agent the moment a user upgraded terva.
func TestEmptySessionIDIsVisibleFromAnyScope(t *testing.T) {
	root := testsupport.TempDir(t)
	f := New(Config{
		Root: root, RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, _ Sink) error { <-ctx.Done(); return ctx.Err() })
		},
	})
	// No SetActiveSession call → agent spawned with empty SessionID.
	a, err := f.Spawn(context.Background(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if a.SessionID != "" {
		t.Fatalf("unscoped spawn produced SessionID %q; want empty", a.SessionID)
	}

	for _, scope := range []string{"", "any-session", "some-other"} {
		f.SetActiveSession(scope)
		got := snapshotIDs(f.SnapshotAll())
		if len(got) != 1 || got[0] != a.ID {
			t.Errorf("scope=%q: ids = %v; want legacy agent visible", scope, got)
		}
	}

	_ = f.Stop(a.ID)
	a.Wait()
}

// TestResumePreservesSessionID guards the regression where Resume
// rebuilt the agentMeta / Agent without SessionID, so the trailing
// writeAgentMeta persisted an empty session_id and permanently
// un-scoped the agent from its host session. We spawn under a scope,
// Resume, and assert both the in-memory field and the on-disk
// meta.json still carry the original session_id.
func TestResumePreservesSessionID(t *testing.T) {
	root := testsupport.TempDir(t)
	f := New(Config{
		Root: root, RepoRoot: root,
		NewRunner: func(a *Agent) Runner {
			return RunnerFunc(func(ctx context.Context, _ Sink) error { <-ctx.Done(); return ctx.Err() })
		},
	})

	f.SetActiveSession("sess-scope")
	a, err := f.Spawn(context.Background(), "scoped task")
	if err != nil {
		t.Fatal(err)
	}
	id := a.ID
	if a.SessionID != "sess-scope" {
		t.Fatalf("spawn produced SessionID %q; want sess-scope", a.SessionID)
	}
	// Move the agent out of the running state so Resume accepts it.
	_ = f.Stop(id)
	a.Wait()

	a2, err := f.Resume(context.Background(), id)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer func() { _ = f.Stop(id); a2.Wait() }()

	// In-memory field must survive Resume.
	if a2.SessionID != "sess-scope" {
		t.Errorf("after resume, a2.SessionID = %q; want sess-scope", a2.SessionID)
	}

	// The on-disk meta.json that Resume rewrote must still scope the
	// agent. This is the field that was being clobbered.
	m, err := readAgentMeta(f.agentStateDir(id))
	if err != nil {
		t.Fatalf("read meta after resume: %v", err)
	}
	if m.SessionID != "sess-scope" {
		t.Errorf("meta.json session_id = %q after resume; want sess-scope", m.SessionID)
	}

	// And the scope filter must still find it only under its session.
	f.SetActiveSession("sess-scope")
	if got := snapshotIDs(f.SnapshotAll()); len(got) != 1 || got[0] != id {
		t.Errorf("scoped to sess-scope after resume, ids = %v; want [%s]", got, id)
	}
	f.SetActiveSession("other")
	if got := snapshotIDs(f.SnapshotAll()); len(got) != 0 {
		t.Errorf("scoped to other after resume, ids = %v; want []", got)
	}
}

func snapshotIDs(ss []AgentSnapshot) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.ID
	}
	return out
}

// TestAgentPathHelpersShareOneStateDir pins the layout the out-of-package
// readers depend on: the transcript and the event log are siblings in one
// agent state dir, and both exported helpers derive it the same way spawn
// does. session_inspect reads an empty transcript and consults the event log
// beside it to tell a working sub-agent from a finished-but-silent one; if
// these two drifted apart it would silently stop finding the log and start
// describing every running child as having produced nothing.
func TestAgentPathHelpersShareOneStateDir(t *testing.T) {
	root := testsupport.TempDir(t)
	const id = "review-x-123000"

	session := AgentSessionPath(root, id)
	events := AgentEventLogPath(root, id)
	if filepath.Dir(session) != filepath.Dir(events) {
		t.Fatalf("helpers disagree on the state dir: %q vs %q", filepath.Dir(session), filepath.Dir(events))
	}

	// And that dir is the one Spawn actually writes into.
	f := &Swarm{cfg: Config{Root: root}}
	if got := f.agentStateDir(id); got != filepath.Dir(session) {
		t.Errorf("agentStateDir = %q, helpers say %q", got, filepath.Dir(session))
	}
	if filepath.Base(events) != "events.jsonl" {
		t.Errorf("event log basename = %q, want events.jsonl", filepath.Base(events))
	}
}
