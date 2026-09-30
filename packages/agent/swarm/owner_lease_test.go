package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// A spawn with Dir runs in the caller's lease. The swarm asks for no lease of
// its own and gives back none when the agent ends.
func TestAnOwnersLeaseIsNeitherAcquiredNorReleased(t *testing.T) {
	spy := &acquirerSpy{dir: testsupport.TempDir(t)}
	f := newWorktreeSwarm(t, spy, func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error { return nil })
	})
	lease := testsupport.TempDir(t)
	a, err := f.SpawnReq(context.Background(), SpawnRequest{Task: "work", Dir: lease, Approval: "auto-edit"})
	if err != nil {
		t.Fatal(err)
	}
	a.Wait()
	if a.Dir != lease || !a.Leased || !a.OwnerLease {
		t.Errorf("agent dir %q leased %v owner %v, want the caller's lease", a.Dir, a.Leased, a.OwnerLease)
	}
	if int(atomic.LoadInt32(&spy.calls)) != 0 || spy.releaseCount() != 0 {
		t.Errorf("the swarm acquired %d and released %d leases", int(atomic.LoadInt32(&spy.calls)), spy.releaseCount())
	}
	homeSub := filepath.Join(f.cfg.RepoRoot, "sub")
	if err := os.MkdirAll(homeSub, 0o755); err != nil {
		t.Fatal(err)
	}
	// A lease that is no lease, or one with no posture of its own, is
	// refused: a leased worker with no posture defaults to yolo.
	cases := map[string]SpawnRequest{
		"relative":      {Task: "work", Dir: "relative/dir", Approval: "auto-edit"},
		"blank":         {Task: "work", Dir: "   ", Approval: "auto-edit"},
		"home checkout": {Task: "work", Dir: f.cfg.RepoRoot, Approval: "auto-edit"},
		"home subdir":   {Task: "work", Dir: homeSub, Approval: "auto-edit"},
		"missing":       {Task: "work", Dir: filepath.Join(lease, "gone"), Approval: "auto-edit"},
		"no posture":    {Task: "work", Dir: lease},
	}
	// A symlink names the home checkout under another path. Only this case
	// needs one, so a platform without symlinks skips it alone.
	if link := filepath.Join(testsupport.TempDir(t), "home"); os.Symlink(f.cfg.RepoRoot, link) == nil {
		cases["home by link"] = SpawnRequest{Task: "work", Dir: link, Approval: "auto-edit"}
	} else {
		t.Log("symlinks unavailable; the home-by-link case is skipped")
	}
	for name, req := range cases {
		if _, err := f.SpawnReq(context.Background(), req); err == nil {
			t.Errorf("a %s lease was accepted", name)
		}
	}
}

// An agent in its owner's lease is revived only into the lease its owner
// re-acquires, before a reload and after one. A plain Resume refuses it, so
// nothing revives it in the home checkout.
func TestAnOwnersLeaseRevivesOnlyWithItsDir(t *testing.T) {
	root := testsupport.TempDir(t)
	lease := testsupport.TempDir(t)
	cfg := Config{Root: root, RepoRoot: root, NewRunner: func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error { return nil })
	}}
	f := New(cfg)
	a, err := f.SpawnReq(context.Background(), SpawnRequest{Task: "work", Dir: lease, Approval: "auto-edit"})
	if err != nil {
		t.Fatal(err)
	}
	a.Wait()
	if _, err := f.Resume(context.Background(), a.ID); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Errorf("a plain resume = %v, want a refusal", err)
	}

	g := New(cfg)
	if n, errs := g.Reload(); n != 1 || len(errs) > 0 {
		t.Fatalf("reload loaded %d, errs %v", n, errs)
	}
	reloaded := g.Get(a.ID)
	if !reloaded.OwnerLease || reloaded.Leased || reloaded.Dir != root {
		t.Errorf("reloaded dir %q leased %v owner %v, want the home dir, unleased, still the owner's", reloaded.Dir, reloaded.Leased, reloaded.OwnerLease)
	}
	if _, err := g.Resume(context.Background(), a.ID); err == nil {
		t.Error("a plain resume after a reload revived the agent")
	}
	for _, bad := range []string{"relative/dir", root} {
		if _, err := g.ResumeIn(context.Background(), a.ID, bad, Hooks{}); err == nil {
			t.Errorf("the lease %q revived the agent", bad)
		}
	}
	b, err := g.ResumeIn(context.Background(), a.ID, lease, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	b.Wait()
	if b.Dir != lease || !b.Leased || !b.OwnerLease {
		t.Errorf("revived dir %q leased %v owner %v, want the re-acquired lease", b.Dir, b.Leased, b.OwnerLease)
	}
}

// A dir for an agent without an owner's lease is refused, so ResumeIn cannot
// move an ordinary agent anywhere.
func TestResumeInRefusesAnAgentWithoutAnOwnersLease(t *testing.T) {
	root := testsupport.TempDir(t)
	f := New(Config{Root: root, RepoRoot: root, NewRunner: func(a *Agent) Runner {
		return RunnerFunc(func(ctx context.Context, sink Sink) error { return nil })
	}})
	a, err := f.SpawnReq(context.Background(), SpawnRequest{Task: "work"})
	if err != nil {
		t.Fatal(err)
	}
	a.Wait()
	if _, err := f.ResumeIn(context.Background(), a.ID, testsupport.TempDir(t), Hooks{}); err == nil {
		t.Error("ResumeIn moved an agent with no owner's lease")
	}
}
