package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/worktree"
)

// newRemoveRepo is a git repo plus a scratch TERVA_HOME, so leases land nowhere
// real. Mirrors newCarrierRepo; kept separate so a change to the carrier's
// fixture cannot silently retune these.
func newRemoveRepo(t *testing.T) worktree.Env {
	t.Helper()
	repo := newCarrierRepo(t) // sets TERVA_HOME and commits a README
	// Through the same constructor production uses. Hand-rolled, this set Root
	// and omitted LegacyRoot — so it drove removeAvailableWorktrees against an
	// env no host ever builds, and would have stayed green through a real drift
	// in the pair it was standing in for.
	return worktree.HostEnv(os.Getenv("TERVA_HOME"), repo, "test-session")
}

func mustCreate(t *testing.T, env worktree.Env, name string) string {
	t.Helper()
	res, err := swarmWorktreeMgr.Create(env, worktree.CreateArgs{Name: name})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	if _, err := swarmWorktreeMgr.Release(env, worktree.ReleaseArgs{Name: name}); err != nil {
		t.Fatalf("release %s: %v", name, err)
	}
	return res.Path
}

func names(t *testing.T, env worktree.Env) []string {
	t.Helper()
	lst, err := swarmWorktreeMgr.List(env, worktree.ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var out []string
	for _, it := range lst.Worktrees {
		out = append(out, it.Name)
	}
	return out
}

// The case this exists for: 41 released worktrees and one with work in it. A
// sweep that aborted on the first refusal would be useless exactly when it is
// wanted, and the user would have no way to tell which one stopped it.
func TestASweepRemovesWhatItCanAndNamesWhatItKept(t *testing.T) {
	env := newRemoveRepo(t)
	mustCreate(t, env, "clean-one")
	mustCreate(t, env, "clean-two")
	dirty := mustCreate(t, env, "has-work")

	// Uncommitted work in one of them. Manager.Remove refuses this without
	// force, and force is never passed: terva does not delete work that exists
	// nowhere else.
	if err := os.WriteFile(filepath.Join(dirty, "scratch.txt"), []byte("unsaved\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := removeAvailableWorktrees(env)
	if err == nil {
		t.Fatal("sweep reported success while refusing a worktree — the user would never learn it was kept")
	}
	if !strings.Contains(err.Error(), "has-work") {
		t.Errorf("the message does not name what it kept: %v", err)
	}

	left := names(t, env)
	if len(left) != 1 || left[0] != "has-work" {
		t.Errorf("after the sweep the repo holds %v; want only has-work", left)
	}
}

// The ordinary case, and the one that clears the backlog.
func TestASweepClearsEveryAvailableWorktree(t *testing.T) {
	env := newRemoveRepo(t)
	for _, n := range []string{"a", "b", "c"} {
		mustCreate(t, env, n)
	}
	if err := removeAvailableWorktrees(env); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if left := names(t, env); len(left) != 0 {
		t.Errorf("worktrees survived a clean sweep: %v", left)
	}
}

// A worktree still CLAIMED by a live session is not in the available set, so a
// sweep must leave it alone — someone is working in there.
func TestASweepLeavesALiveClaimAlone(t *testing.T) {
	env := newRemoveRepo(t)
	mustCreate(t, env, "released")
	if _, err := swarmWorktreeMgr.Create(env, worktree.CreateArgs{Name: "held"}); err != nil {
		t.Fatalf("create held: %v", err) // created and NOT released: this session holds it
	}

	if err := removeAvailableWorktrees(env); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	left := names(t, env)
	if len(left) != 1 || left[0] != "held" {
		t.Errorf("after the sweep the repo holds %v; want only the claimed one", left)
	}
}

// An empty sweep is success, not an error: "nothing to remove" is the state the
// panel is trying to reach, and reporting it as a failure would train the user
// to ignore the message that also carries the refusals.
func TestASweepOfNothingSucceeds(t *testing.T) {
	if err := removeAvailableWorktrees(newRemoveRepo(t)); err != nil {
		t.Errorf("sweeping an empty repo reported an error: %v", err)
	}
}
