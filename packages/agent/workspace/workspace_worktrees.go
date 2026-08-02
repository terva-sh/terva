package workspace

import (
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/worktree"
	"terva.sh/terva/packages/i18n"
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
	env, err := s.worktreeEnv()
	if err != nil {
		return nil, err
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

// worktreeEnv is the engine env for this session's checkout, shared by the view
// and the actions so a removal can never address a different repo than the list
// the user is looking at.
func (s *wsSession) worktreeEnv() (worktree.Env, error) {
	if !worktree.InRepo(s.cwd) {
		return worktree.Env{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("no git repository at %s", s.cwd))
	}
	sessID := ""
	if s.agent != nil {
		sessID, _ = s.agent.SessionIdentity()
	}
	return worktree.Env{
		Root:       filepath.Join(config.TervaHome(), "worktrees"),
		LegacyRoot: filepath.Join(config.TervaHome(), "ext-data", "git-worktree"),
		CWD:        s.cwd,
		SessionID:  sessID,
	}, nil
}

// worktreeAction serves the worktrees surface's mutations. Two today, both
// removals: one named worktree, or every AVAILABLE one at once.
//
// "available" is the engine's own classification and it already means what a
// human means by "unclaimed, stale ones included": deriveStatus reports a claim
// whose owning process is gone, or whose TTL has expired, as available with a
// StaleReason — never silently reclaimed. So there is no separate staleness
// filter here, and a LIVE claim is simply not in the set.
//
// Force is never passed. Manager.Remove refuses a worktree with uncommitted
// changes or unmerged work, and that refusal is the whole safety story for a
// bulk removal — terva declines to delete work it cannot see anywhere else.
// The refusal comes back as the error, so a sweep says which ones it kept.
func (s *wsSession) worktreeAction(action string, args map[string]string) error {
	env, err := s.worktreeEnv()
	if err != nil {
		return err
	}
	switch action {
	case "remove":
		name := strings.TrimSpace(args["name"])
		if name == "" {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("remove needs a worktree name"))
		}
		if _, err := swarmWorktreeMgr.Remove(env, worktree.RemoveArgs{Name: name}); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", err.Error())
		}
		return nil
	case "remove_available":
		return removeAvailableWorktrees(env)
	}
	return ctrlproto.Errorf(ctrlproto.CodeUnsupported, "%s", i18n.T("unknown worktrees action %q", action))
}

// removeAvailableWorktrees sweeps every available worktree, and is deliberately
// NOT all-or-nothing: it removes what it can and reports what it could not.
//
// A sweep that aborted on the first refusal would be useless exactly when it is
// wanted — one worktree with uncommitted changes among forty released ones
// would block all forty, and the user would have no way to tell which one
// stopped it. Each failure keeps its name so the message names them.
func removeAvailableWorktrees(env worktree.Env) error {
	lst, err := swarmWorktreeMgr.List(env, worktree.ListFilter{Status: "available"})
	if err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "worktree list: %v", err)
	}
	var kept []string
	removed := 0
	for _, it := range lst.Worktrees {
		if it == nil || it.Unmanaged {
			continue
		}
		if _, rerr := swarmWorktreeMgr.Remove(env, worktree.RemoveArgs{Name: it.Name}); rerr != nil {
			kept = append(kept, it.Name)
			continue
		}
		removed++
	}
	if len(kept) > 0 {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", i18n.T(
			"removed %d, kept %d with uncommitted or unmerged work: %s",
			removed, len(kept), strings.Join(kept, ", ")))
	}
	return nil
}
