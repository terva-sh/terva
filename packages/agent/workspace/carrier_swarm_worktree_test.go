package workspace

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/swarm"
)

// TestCarrierSwarmWorktreeAcquirerNoExt: worktree isolation is opt-in and
// explicit, so when no live session provides worktree_create the acquirer must
// fail the spawn LOUDLY rather than silently dropping back to the shared host
// tree (which would defeat the point of --swarm-worktrees).
func TestCarrierSwarmWorktreeAcquirerNoExt(t *testing.T) {
	w := &Workspace{sessions: map[string]*wsSession{}}

	if mgr := w.worktreeExtManager(); mgr != nil {
		t.Fatalf("worktreeExtManager = %v with no sessions, want nil", mgr)
	}

	_, err := w.acquireSwarmWorktree(context.Background(), swarm.WorktreeReq{AgentID: "a1", Task: "do the thing"})
	if err == nil {
		t.Fatal("acquireSwarmWorktree should fail when no worktree extension is available")
	}
	if !strings.Contains(err.Error(), "worktree_create") {
		t.Errorf("error should name the missing tool: %v", err)
	}
}
