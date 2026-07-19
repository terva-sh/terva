package workspace

import (
	"path/filepath"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/worktree"
)

// worktreesView serves the "worktrees" surface: the managed-worktree list plus
// the merge-back (collect) overview, computed by the built-in engine against
// the session's cwd. The TUI's /worktree panel and status glance fetch it by
// explicit id; the web panel gets it as a listed tab (surfaceList gates on the
// same InRepo check, so the tab only appears where this fetch succeeds). The
// engine runs daemon-side, where the repo actually lives, so an attached
// (possibly remote) TUI or browser renders honest data.
//
// The claim-owner identity is this session's own (agent.SessionIdentity), so
// "claimed_by: self" in the view means claimed by THIS session — matching what
// the worktree_* tools would report for the same session.
func (s *wsSession) worktreesView() (*ctrlproto.WorktreeView, error) {
	if !worktree.InRepo(s.cwd) {
		return nil, ctrlproto.Errorf(ctrlproto.CodeNotFound, "no git repository at %s", s.cwd)
	}
	sessID := ""
	if s.agent != nil {
		sessID, _ = s.agent.SessionIdentity()
	}
	env := worktree.Env{
		Root:       filepath.Join(config.TervaHome(), "worktrees"),
		LegacyRoot: filepath.Join(config.TervaHome(), "ext-data", "git-worktree"),
		CWD:        s.cwd,
		SessionID:  sessID,
	}
	lst, err := swarmWorktreeMgr.List(env, worktree.ListFilter{})
	if err != nil {
		return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "worktree list: %v", err)
	}
	view := &ctrlproto.WorktreeView{RepoKey: lst.RepoKey, CWDWorktree: lst.CWDWorktree}
	for _, it := range lst.Worktrees {
		view.Items = append(view.Items, ctrlproto.WorktreeViewItem{
			Name:        it.Name,
			Path:        it.Path,
			Branch:      it.Branch,
			BaseCommit:  it.BaseCommit,
			BaseRef:     it.BaseRef,
			HeadCommit:  it.HeadCommit,
			Status:      it.Status,
			ClaimedBy:   it.ClaimedBy,
			StaleReason: it.StaleReason,
			Dirty:       it.Dirty,
			Unmanaged:   it.Unmanaged,
		})
	}
	// The collect overview rides the same fetch: a repo has few worktrees, so
	// the extra rev-lists are cheap, and the panel's c/l toggle then needs no
	// second round-trip.
	col, err := swarmWorktreeMgr.Collect(env)
	if err == nil {
		for _, it := range col.Worktrees {
			view.Collect = append(view.Collect, ctrlproto.WorktreeCollectItem{
				Name:       it.Name,
				Branch:     it.Branch,
				BaseRef:    it.BaseRef,
				BaseCommit: it.BaseCommit,
				HeadCommit: it.HeadCommit,
				Ahead:      it.Ahead,
				Commits:    it.Commits,
				Dirty:      it.Dirty,
				Unpushed:   it.Unpushed,
			})
		}
	}
	return view, nil
}
