package swarm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// The swarm carries a backend label it does not understand. That is the whole
// design: it knows what a backend IS exactly as much as it knows what a git
// worktree is, which is nothing. It stores the label, persists it, and hands it
// to the host's NewRunner.
func TestSwarmCarriesTheBackendLabelWithoutUnderstandingIt(t *testing.T) {
	root := testsupport.TempDir(t)
	var sawBackend string
	f := New(Config{
		Root:     root,
		RepoRoot: testsupport.TempDir(t),
		NewRunner: func(a *Agent) Runner {
			sawBackend = a.Backend
			return RunnerFunc(func(ctx context.Context, s Sink) error { return nil })
		},
	})
	defer f.StopAllAndWait(2 * time.Second)

	a, err := f.SpawnReq(context.Background(), SpawnRequest{
		Task:    "port the retry test",
		Backend: "a-backend-the-swarm-has-never-heard-of",
	})
	if err != nil {
		t.Fatalf("the swarm must not validate a backend name it cannot know: %v", err)
	}
	a.Wait()
	if sawBackend != "a-backend-the-swarm-has-never-heard-of" {
		t.Errorf("NewRunner saw backend %q; the label must reach the host verbatim", sawBackend)
	}
}

// Empty backend is a native terva swarm child, exactly as it has always been.
// Every spawn in the tree today passes no backend, and every one of them must
// keep getting the runner it got yesterday.
func TestEmptyBackendIsTheNativeAgentUnchanged(t *testing.T) {
	root := testsupport.TempDir(t)
	f := New(Config{
		Root:      root,
		RepoRoot:  testsupport.TempDir(t),
		NewRunner: func(a *Agent) Runner { return RunnerFunc(func(context.Context, Sink) error { return nil }) },
	})
	defer f.StopAllAndWait(2 * time.Second)

	a, err := f.Spawn(context.Background(), "the historical spawn path")
	if err != nil {
		t.Fatal(err)
	}
	a.Wait()
	if a.Backend != "" {
		t.Errorf("a plain Spawn must stay native, got backend %q", a.Backend)
	}
	// ...and the label must not appear in meta.json at all, so an old terva
	// reading a new meta.json (and a new terva reading an old one) both see the
	// same thing: nothing.
	raw, err := os.ReadFile(filepath.Join(root, "agents", a.ID, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, present := m["backend"]; present {
		t.Errorf("native agents must not write a backend key: %s", raw)
	}
}

// An agent revived after a terva restart must come back on the SAME backend.
// Revived onto the wrong one, it would be handed an event stream and a resume
// cursor its runner cannot read — and it would fail in the confusing way, not
// the loud way.
func TestBackendSurvivesAReload(t *testing.T) {
	root := testsupport.TempDir(t)
	repo := testsupport.TempDir(t)
	f := New(Config{
		Root:      root,
		RepoRoot:  repo,
		NewRunner: func(a *Agent) Runner { return RunnerFunc(func(context.Context, Sink) error { return nil }) },
	})
	a, err := f.SpawnReq(context.Background(), SpawnRequest{Task: "long job", Backend: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	a.Wait()
	f.StopAllAndWait(2 * time.Second)

	// A fresh terva process reads the same state directory.
	next := New(Config{Root: root, RepoRoot: repo})
	if _, errs := next.Reload(); len(errs) > 0 {
		t.Fatal(errs)
	}
	defer next.StopAllAndWait(2 * time.Second)

	var found *AgentSnapshot
	for _, s := range next.SnapshotAll() {
		if s.ID == a.ID {
			snap := s
			found = &snap
		}
	}
	if found == nil {
		t.Fatalf("agent %s did not survive the reload", a.ID)
	}
	if found.Backend != "claude" {
		t.Errorf("revived on backend %q, want %q — a cross-backend revival hands a runner a stream it cannot parse", found.Backend, "claude")
	}
}
