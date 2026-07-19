package worktree

import (
	"strings"
	"testing"
)

func TestClaimAvailableReclaimsStale(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	// sess-1 creates and holds it.
	if _, err := fixedManager(true).Create(env(repoDir, dataDir, "sess-1"), CreateArgs{Name: "w", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}
	// sess-2, with sess-1's pid dead, sees a stale claim and can claim it.
	m2 := fixedManager(false)
	res, err := m2.Claim(env(repoDir, dataDir, "sess-2"), ClaimArgs{Name: "w"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "claimed" || res.ClaimedBy != "self" || !res.ReclaimedStale {
		t.Errorf("claim of a stale worktree: %+v", res)
	}
	// sess-2 now holds it.
	lst, _ := m2.List(env(repoDir, dataDir, "sess-2"), ListFilter{})
	if got := lst.Worktrees[0]; got.Status != "claimed" || got.ClaimedBy == nil || *got.ClaimedBy != "self" {
		t.Errorf("after claim: %+v", got)
	}
}

func TestClaimIdempotentSelf(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	if _, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "w", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}
	res, err := m.Claim(env(repoDir, dataDir, "s1"), ClaimArgs{Name: "w"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ClaimedBy != "self" || res.ReclaimedStale {
		t.Errorf("re-claiming own worktree should be a clean no-op: %+v", res)
	}
}

func TestClaimRefusedWhenClaimedByOther(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	if _, err := fixedManager(true).Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "w", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}
	// pid alive + different session => live claim => refuse.
	_, err := fixedManager(true).Claim(env(repoDir, dataDir, "s2"), ClaimArgs{Name: "w"})
	if err == nil || !strings.Contains(err.Error(), "claimed by") {
		t.Errorf("expected claimed-by-other error, got %v", err)
	}
}

func TestClaimNonexistent(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	_, err := fixedManager(true).Claim(env(repoDir, dataDir, "s1"), ClaimArgs{Name: "ghost"})
	if err == nil || !strings.Contains(err.Error(), "no managed worktree") {
		t.Errorf("claiming an unknown worktree should error, got %v", err)
	}
}

func TestReleaseOwnClaim(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	if _, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "w", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}
	rel, err := m.Release(env(repoDir, dataDir, "s1"), ReleaseArgs{Name: "w"})
	if err != nil {
		t.Fatal(err)
	}
	if !rel.Released || rel.Status != "available" {
		t.Errorf("release own claim: %+v", rel)
	}
	// Now available with no claim record.
	lst, _ := m.List(env(repoDir, dataDir, "s1"), ListFilter{})
	if got := lst.Worktrees[0]; got.Status != "available" || got.ClaimedBy != nil || got.StaleReason != "" {
		t.Errorf("after release should be cleanly available: %+v", got)
	}
}

func TestReleaseStaleClaimAllowed(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	if _, err := fixedManager(true).Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "w", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}
	// sess-2 with sess-1's pid dead: the claim is stale, so release (cleanup) is allowed.
	rel, err := fixedManager(false).Release(env(repoDir, dataDir, "s2"), ReleaseArgs{Name: "w"})
	if err != nil {
		t.Fatal(err)
	}
	if !rel.Released {
		t.Errorf("releasing a stale claim should clear it: %+v", rel)
	}
}

func TestReleaseRefusedWhenClaimedByOtherLive(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	if _, err := fixedManager(true).Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "w", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}
	_, err := fixedManager(true).Release(env(repoDir, dataDir, "s2"), ReleaseArgs{Name: "w"})
	if err == nil || !strings.Contains(err.Error(), "only its owner") {
		t.Errorf("releasing another live session's claim should error, got %v", err)
	}
}

func TestReleaseNoClaimIsNoop(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	if _, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "w", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Release(env(repoDir, dataDir, "s1"), ReleaseArgs{Name: "w"}); err != nil {
		t.Fatal(err)
	}
	// Already released — releasing again is a no-op, not an error.
	rel, err := m.Release(env(repoDir, dataDir, "s1"), ReleaseArgs{Name: "w"})
	if err != nil {
		t.Fatal(err)
	}
	if rel.Released {
		t.Errorf("second release should report released=false: %+v", rel)
	}
}

func TestReleaseNonexistent(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	_, err := fixedManager(true).Release(env(repoDir, dataDir, "s1"), ReleaseArgs{Name: "ghost"})
	if err == nil || !strings.Contains(err.Error(), "no managed worktree") {
		t.Errorf("releasing an unknown worktree should error, got %v", err)
	}
}

// TestClaimReleaseHandoff is the headline use case: sess-1 makes a worktree and
// releases it; sess-2 claims the now-available worktree without create/remove.
func TestClaimReleaseHandoff(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m1 := fixedManager(true)
	if _, err := m1.Create(env(repoDir, dataDir, "sess-1"), CreateArgs{Name: "shared", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := m1.Release(env(repoDir, dataDir, "sess-1"), ReleaseArgs{Name: "shared"}); err != nil {
		t.Fatal(err)
	}
	// sess-2 (even with pids alive) can claim it — there's no live claim now.
	res, err := fixedManager(true).Claim(env(repoDir, dataDir, "sess-2"), ClaimArgs{Name: "shared"})
	if err != nil {
		t.Fatalf("hand-off claim should succeed: %v", err)
	}
	if res.ClaimedBy != "self" || res.ReclaimedStale {
		t.Errorf("hand-off should be a clean claim (no stale reclaim): %+v", res)
	}
}

// --- list filter -----------------------------------------------------------

func TestListFilterStatus(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	for _, n := range []string{"held", "freed"} {
		if _, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: n, ReuseIfAvailable: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Release(env(repoDir, dataDir, "s1"), ReleaseArgs{Name: "freed"}); err != nil {
		t.Fatal(err)
	}
	avail, _ := m.List(env(repoDir, dataDir, "s1"), ListFilter{Status: "available"})
	if len(avail.Worktrees) != 1 || avail.Worktrees[0].Name != "freed" {
		t.Errorf("status=available filter: %+v", names(avail.Worktrees))
	}
	claimed, _ := m.List(env(repoDir, dataDir, "s1"), ListFilter{Status: "claimed"})
	if len(claimed.Worktrees) != 1 || claimed.Worktrees[0].Name != "held" {
		t.Errorf("status=claimed filter: %+v", names(claimed.Worktrees))
	}
}

func TestListFilterBaseRef(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	runT(t, repoDir, "branch", "dev")
	if _, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "onmain", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "ondev", Base: "dev", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}
	main, _ := m.List(env(repoDir, dataDir, "s1"), ListFilter{BaseRef: "main"})
	if len(main.Worktrees) != 1 || main.Worktrees[0].Name != "onmain" {
		t.Errorf("base_ref=main filter: %+v", names(main.Worktrees))
	}
	dev, _ := m.List(env(repoDir, dataDir, "s1"), ListFilter{BaseRef: "dev"})
	if len(dev.Worktrees) != 1 || dev.Worktrees[0].Name != "ondev" {
		t.Errorf("base_ref=dev filter: %+v", names(dev.Worktrees))
	}
}

// TestListFilterDoesNotHideCWD verifies the filter narrows the worktree list but
// cwd_worktree still reports where the agent is, even when it's filtered out.
func TestListFilterDoesNotHideCWD(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)
	res, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "w", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	// From inside the (claimed) worktree, filter to available only — it's excluded
	// from the list, but cwd_worktree must still name it.
	lst, _ := m.List(env(res.Path, dataDir, "s1"), ListFilter{Status: "available"})
	if len(lst.Worktrees) != 0 {
		t.Errorf("claimed worktree should be filtered out: %+v", names(lst.Worktrees))
	}
	if lst.CWDWorktree == nil || *lst.CWDWorktree != "w" {
		t.Errorf("cwd_worktree should survive filtering: %+v", lst.CWDWorktree)
	}
}

func names(items []*ListItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Name
	}
	return out
}
