package workspace

import (
	"context"
	"fmt"
	"path/filepath"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/worktree"
)

// swarmWorktreeMgr drives the leases. One package-level engine: the in-process
// mutex is per-Manager, and the registry flock serializes across processes (and
// across any other Manager instance) regardless.
var swarmWorktreeMgr = worktree.NewManager()

// acquireSwarmWorktree is the workspace swarm's AcquireWorktree hook on the
// carrier: it leases a dedicated git worktree per swarm sub-agent straight from
// the in-tree worktree engine (stage 1 of the terva-git-worktree fold-in).
//
// This used to dial the extension's worktree_create through whichever live
// session happened to carry it — and failed the spawn when none did (a fresh
// daemon, a headless run, an uninstalled extension). A direct engine call has
// no such mode: --swarm-worktrees works whenever the binary does. Worktree
// isolation was explicitly requested, so any failure still surfaces and fails
// the spawn rather than silently dropping back to the shared host tree. On
// release it releases the claim (NOT remove), so the worktree + branch survive
// for review/merge.
//
// The claim is owned by a per-agent identity ("swarm:<agent-id>"), not the
// acquiring session — honest attribution, and release works no matter which
// sessions have come or gone in between.
func (w *Workspace) acquireSwarmWorktree(ctx context.Context, req swarm.WorktreeReq) (swarm.WorktreeLease, error) {
	name := build.SlugAgent(req.AgentID, req.Task)
	env := worktree.Env{
		Root:       filepath.Join(config.TervaHome(), "worktrees"),
		LegacyRoot: filepath.Join(config.TervaHome(), "ext-data", "git-worktree"),
		CWD:        w.cwd,
		SessionID:  "swarm:" + req.AgentID,
	}
	res, err := swarmWorktreeMgr.Create(env, worktree.CreateArgs{Name: name, ReuseIfAvailable: true}) // base defaults to HEAD
	if err != nil {
		return swarm.WorktreeLease{}, fmt.Errorf("swarm worktree isolation: %w", err)
	}
	// The swarm_spawn gate checks the HOST cwd's trust, but this sub-agent boots
	// with --cwd <worktree> and re-resolves trust for the worktree path. A
	// worktree outside the trusted path boots UNTRUSTED even when the host is
	// trusted — the gate would pass while the child silently loses this project's
	// extensions, skills, and context. Surface the full provenance record here
	// (retro H5·ux, Phase 7 §7a/§7c) so the operator sees repo/branch/base/commit
	// and the trust state — and, when restricted, the exact-path grant hint —
	// BEFORE the child boots. Never auto-trust: the record reflects the store
	// verdict verbatim (a worktree can carry different executable project content
	// than the host). Gated on the host being trusted, matching the swarm_spawn
	// gate: a restricted host is handled there, so there's no lease to narrate.
	// The engine result fills the git facts directly — the contract
	// worktree_provenance.go held open for the extension to populate.
	if w.diag != nil && w.Trusted() {
		if store, terr := config.LoadTrustStore(); terr == nil {
			short := res.HeadCommit
			if len(short) > 12 {
				short = short[:12]
			}
			facts := &extProvenance{Branch: res.Branch, Base: res.BaseRef, Commit: short}
			w.diag(newWorktreeProvenance(ctx, store, w.cwd, res.Path, facts).Render())
		}
	}
	return swarm.WorktreeLease{
		Dir: res.Path,
		Release: func() {
			// Release, never remove: keep the worktree + branch for
			// review/merge. Best-effort — the agent is already terminal.
			_, _ = swarmWorktreeMgr.Release(env, worktree.ReleaseArgs{Name: name})
		},
	}, nil
}
