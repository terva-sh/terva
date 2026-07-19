package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/testsupport"
)

// TestWorktreesSurface covers the "worktrees" pane: NotFound outside a git
// repo (so the TUI says "unavailable"), the surfaceList gate (the web tab
// appears exactly where the fetch succeeds), and a well-formed view over a
// live lease — items, claim state, and the collect overview riding one fetch.
func TestWorktreesSurface(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	w := &Workspace{sessions: map[string]*wsSession{}}

	// No repo at the session cwd => NotFound, and no tab in surfaceList.
	bare := &wsSession{ws: w, cwd: testsupport.TempDir(t)}
	if _, err := bare.surface("worktrees"); err == nil {
		t.Fatal("worktrees surface outside a git repo should be NotFound")
	}
	if hasWorktreesMeta(bare.surfaceList()) {
		t.Error("surfaceList outside a git repo should not offer the worktrees tab")
	}

	// A real repo with one leased worktree: the view reflects the engine.
	repo := newCarrierRepo(t) // resets TERVA_HOME to its own scratch dir
	wr := &Workspace{sessions: map[string]*wsSession{}, cwd: repo}
	if _, err := wr.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: "a1", Task: "panel test"}); err != nil {
		t.Fatalf("lease: %v", err)
	}
	s := &wsSession{ws: wr, cwd: repo}
	if !hasWorktreesMeta(s.surfaceList()) {
		t.Error("surfaceList inside a git repo should offer the worktrees tab")
	}
	sf, err := s.surface("worktrees")
	if err != nil {
		t.Fatalf("surface: %v", err)
	}
	v := sf.Worktrees
	if v == nil || sf.Kind != "worktrees" {
		t.Fatalf("surface should carry a worktrees view, got %+v", sf)
	}
	if v.RepoKey == "" || len(v.Items) != 1 {
		t.Fatalf("view should list the leased worktree: %+v", v)
	}
	it := v.Items[0]
	if it.Status != "claimed" || it.ClaimedBy == nil || *it.ClaimedBy != "swarm:a1" {
		t.Errorf("the lease's claim should ride the wire: %+v", it)
	}
	if it.Path == "" || it.Branch == "" {
		t.Errorf("item should carry path and branch: %+v", it)
	}
	if len(v.Collect) != 1 || v.Collect[0].Name != it.Name {
		t.Errorf("the collect overview should ride the same fetch: %+v", v.Collect)
	}
}

func hasWorktreesMeta(metas []ctrlproto.SurfaceMeta) bool {
	for _, m := range metas {
		if m.ID == "worktrees" {
			return m.Kind == "worktrees"
		}
	}
	return false
}
