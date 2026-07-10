package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/swarm"
)

// acquireSwarmWorktree is the workspace swarm's AcquireWorktree hook on the
// carrier — the daemon twin of the legacy swarmWorktreeAcquirer. It leases a
// dedicated git worktree per swarm sub-agent from the terva-git-worktree
// extension, wired only when --swarm-worktrees is on (see resolveSwarmWorktrees).
//
// The swarm is workspace-global but extensions are per-session, so it resolves a
// live session that provides worktree_create at call time (and again at release,
// since the acquiring session may have closed by then). Worktree isolation was
// explicitly requested, so any failure surfaces and fails the spawn rather than
// silently dropping back to the shared host tree. On release it calls
// worktree_release (NOT worktree_remove) so the worktree + branch survive for
// review/merge via the extension's `/worktree collect`.
func (w *Workspace) acquireSwarmWorktree(ctx context.Context, req swarm.WorktreeReq) (swarm.WorktreeLease, error) {
	name := build.SlugAgent(req.AgentID, req.Task)
	mgr := w.worktreeExtManager()
	if mgr == nil {
		return swarm.WorktreeLease{}, fmt.Errorf("swarm worktree isolation: no live session provides worktree_create (install the terva-git-worktree extension or drop --swarm-worktrees)")
	}
	args, err := json.Marshal(map[string]any{"name": name}) // base defaults to HEAD
	if err != nil {
		return swarm.WorktreeLease{}, fmt.Errorf("marshal worktree_create args: %w", err)
	}
	res, err := mgr.InvokeTool(ctx, "worktree_create", args, 30*time.Second)
	if err != nil {
		return swarm.WorktreeLease{}, fmt.Errorf("swarm worktree isolation: worktree_create via terva-git-worktree failed (install the extension or drop --swarm-worktrees): %w", err)
	}
	if res.IsError {
		return swarm.WorktreeLease{}, fmt.Errorf("swarm worktree isolation: worktree_create returned an error: %s", build.FirstText(res))
	}
	var cr struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(build.FirstText(res)), &cr)
	if cr.Path == "" {
		return swarm.WorktreeLease{}, fmt.Errorf("swarm worktree isolation: worktree_create returned no path (result: %s)", build.FirstText(res))
	}
	return swarm.WorktreeLease{
		Dir: cr.Path,
		Release: func() {
			// Release, never remove: keep the worktree + branch for review/merge.
			// Best-effort, detached from ctx (the agent is already terminal), and
			// re-resolves a live manager since the acquiring session may be gone.
			rmgr := w.worktreeExtManager()
			if rmgr == nil {
				return
			}
			rel, _ := json.Marshal(map[string]any{"name": name})
			_, _ = rmgr.InvokeTool(context.Background(), "worktree_release", rel, 10*time.Second)
		},
	}, nil
}

// worktreeExtManager returns a live session's extension manager that provides
// the worktree_create tool, or nil. Any session with the terva-git-worktree
// extension can service a lease, since the extension acts on the shared repo.
func (w *Workspace) worktreeExtManager() *extensions.Manager {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.sessions {
		if s.extMgr != nil && s.extMgr.HasTool("worktree_create") {
			return s.extMgr
		}
	}
	return nil
}
