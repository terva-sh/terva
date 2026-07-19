package modes

import (
	"context"
	"errors"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/worktree"
	"terva.sh/terva/packages/i18n"
)

// The /worktree panel and the status-bar worktree glance render the managed
// worktrees of the session's repo. The engine runs daemon-side (the repo lives
// with the daemon, not the — possibly remote — TUI), so the data rides the
// explicit-id "worktrees" ctrlproto surface, cached here and mapped back to
// engine types so the shared renderers in packages/agent/worktree drive both
// the panel and the glance (the taskboard pattern, tasks_view.go).
//
// Unlike the task board there is no push event for worktree changes: the cache
// fills on session bind, on /worktree open, and on the panel's r key — the
// same freshness the retired extension's panel had.

// refreshCarrierWorktrees fetches the surface off the pump goroutine and
// replaces the cache, then repaints. NotFound (no git repo at the session cwd)
// clears the cache so the glance vanishes.
func (i *Interactive) refreshCarrierWorktrees() {
	if i.cfg.Carrier == nil {
		return
	}
	sess := i.carrierSession()
	sf, err := i.cfg.Carrier.Surface(context.Background(), sess, "worktrees")
	i.mu.Lock()
	if i.cfg.CarrierSession != sess {
		// A switch landed while this fetch was in flight; drop the stale result.
		i.mu.Unlock()
		return
	}
	if err != nil || sf.Worktrees == nil {
		i.carrierWorktrees = nil
	} else {
		i.carrierWorktrees = sf.Worktrees
	}
	i.carrierWorktreesSession = sess
	i.mu.Unlock()
	i.invalidate()
}

// worktreeListRows maps the cached wire view to the engine's ListResult for
// the shared renderers. Reads the cache under mu; no fetch.
func (i *Interactive) worktreeListRows() *worktree.ListResult {
	i.mu.Lock()
	var view *ctrlproto.WorktreeView
	if i.carrierWorktreesSession == i.cfg.CarrierSession {
		view = i.carrierWorktrees
	}
	i.mu.Unlock()
	if view == nil {
		return nil
	}
	res := &worktree.ListResult{RepoKey: view.RepoKey, CWDWorktree: view.CWDWorktree}
	for _, it := range view.Items {
		res.Worktrees = append(res.Worktrees, &worktree.ListItem{
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
	return res
}

// worktreeCollectRows maps the cached wire view to the engine's CollectResult.
func (i *Interactive) worktreeCollectRows() *worktree.CollectResult {
	i.mu.Lock()
	var view *ctrlproto.WorktreeView
	if i.carrierWorktreesSession == i.cfg.CarrierSession {
		view = i.carrierWorktrees
	}
	i.mu.Unlock()
	if view == nil {
		return nil
	}
	res := &worktree.CollectResult{RepoKey: view.RepoKey}
	for _, it := range view.Collect {
		res.Worktrees = append(res.Worktrees, &worktree.CollectItem{
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
	return res
}

// worktreeGlance is the status-bar segment text (empty until the cache has
// data). Computed from the cache each frame; cheap for a short list.
func (i *Interactive) worktreeGlance() string {
	return worktree.StatusGlance(i.worktreeListRows())
}

// openWorktreeDialog backs /worktree: fetch the surface once (freshen +
// capability check), then open the panel over the live cache. NotFound means
// the session cwd has no git repo. collect opens the merge-back overview
// directly (`/worktree collect`).
func (i *Interactive) openWorktreeDialog(collect bool) {
	if i.cfg.Carrier == nil {
		i.setStatusErr(i18n.T("worktrees are unavailable in this mode"))
		return
	}
	sess := i.carrierSession()
	sf, err := i.cfg.Carrier.Surface(context.Background(), sess, "worktrees")
	if err != nil {
		var ce *ctrlproto.Error
		if errors.As(err, &ce) && (ce.Code == ctrlproto.CodeNotFound || ce.Code == ctrlproto.CodeUnsupported) {
			i.setStatusErr(i18n.T("no git repository here — worktrees are unavailable"))
		} else {
			i.setStatusErr(i18n.T("worktrees unavailable: %s", err.Error()))
		}
		return
	}
	if sf.Worktrees == nil {
		i.setStatusErr(i18n.T("no git repository here — worktrees are unavailable"))
		return
	}
	i.mu.Lock()
	if i.cfg.CarrierSession == sess {
		i.carrierWorktrees = sf.Worktrees
		i.carrierWorktreesSession = sess
	}
	i.mu.Unlock()
	i.worktreeDialog.Open(i.worktreeListRows, i.worktreeCollectRows, collect)
	i.invalidate()
}
