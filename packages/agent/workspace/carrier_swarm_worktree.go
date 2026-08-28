package workspace

import (
	"context"
	"fmt"
	"os"
	"time"

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
// the spawn rather than silently dropping back to the shared host tree.
//
// On release it RECLAIMS: a worktree holding nothing — no uncommitted changes,
// no commits that exist only there — is removed, and one holding work is kept
// with its claim dropped, exactly as before. This used to keep every worktree
// unconditionally, which is correct for the agent that wrote something and pure
// litter for the many that read files and exited. The engine decides (see
// worktree.Manager.Reclaim); the rule it applies is the one a person would:
// keep it if this is the only place the work exists.
//
// The claim is owned by a per-agent identity ("swarm:<agent-id>"), not the
// acquiring session — honest attribution, and release works no matter which
// sessions have come or gone in between.
func (w *Workspace) acquireSwarmWorktree(ctx context.Context, req swarm.WorktreeReq) (swarm.WorktreeLease, error) {
	name := build.SlugAgent(req.AgentID, req.Task)
	env := worktree.HostEnv(config.TervaHome(), w.cwd, "swarm:"+req.AgentID)
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
	if w.Trusted() {
		if store, terr := config.LoadTrustStore(); terr == nil {
			short := res.HeadCommit
			if len(short) > 12 {
				short = short[:12]
			}
			facts := &extProvenance{Branch: res.Branch, Base: res.BaseRef, Commit: short}
			w.recordSwarmLease(newWorktreeProvenance(ctx, store, w.cwd, res.Path, facts))
		}
	}
	// Captured by name rather than reaching through the Create result inside
	// the closure: the release path has its own result value, and two things
	// called res one scope apart is how the wrong one gets read.
	leasedPath := res.Path
	return swarm.WorktreeLease{
		Dir: leasedPath,
		Release: func() {
			// Reclaim first, and fall back to dropping the claim when the
			// worktree turns out to hold work. Reclaim reports "kept" as an
			// ordinary result rather than an error, so the only thing an error
			// here means is that the engine could not decide — in which case
			// keeping it is the safe answer, and the claim still has to go or
			// the worktree reads as owned by an agent that no longer exists.
			//
			// Best-effort throughout: the agent is already terminal, and there
			// is nobody left to report a failure to.
			rec, rerr := swarmWorktreeMgr.Reclaim(env, worktree.ReclaimArgs{Name: name})
			reclaimed := rerr == nil && rec != nil && rec.Removed
			if !reclaimed {
				_, _ = swarmWorktreeMgr.Release(env, worktree.ReleaseArgs{Name: name})
			}
			// And retract the "runs restricted" claim. Agent.finish guarantees
			// this runs exactly once on every terminal path — done, failed,
			// killed, Stop, StopAll, detached cleanup — which is why the record
			// can be live state rather than a log line. Nothing else in the
			// system knows the sub-agent stopped.
			w.releaseSwarmLease(leasedPath, reclaimed)
		},
	}, nil
}

// recordSwarmLease adds a leased worktree to the live batch and rewrites the
// note. A lease arriving with nothing live starts a fresh batch: the previous
// swarm's record has already been read (or cleared with the block on the next
// prompt), and carrying its count forward would report worktrees that this
// swarm never leased.
func (w *Workspace) recordSwarmLease(p worktreeProvenance) {
	w.mu.Lock()
	if w.swarmLive == 0 {
		w.swarmBatch = nil
	}
	w.swarmBatch = append(w.swarmBatch, p)
	w.swarmLive++
	w.startSwarmTrustWatchLocked()
	msg := renderSwarmWorktreeNote(w.swarmBatch, w.swarmLive)
	w.mu.Unlock()
	w.note(swarmWorktreeNoteKey, msg, "info")
}

// swarmTrustPollInterval paces the trust-store mtime probe behind the lease
// note's grant hint. A var so tests can crank it down.
var swarmTrustPollInterval = 2 * time.Second

// startSwarmTrustWatchLocked starts the goroutine that keeps the lease note's
// grant hint tracking the LIVE trust store. Caller holds w.mu. Lazy — a
// workspace that never swarms never runs it — and singular: one watcher serves
// every batch for the workspace's lifetime, exiting with w.ctx.
//
// A poll, not a change-notification API: the writer that matters is `terva
// trust` in ANOTHER process (the note's own hint says to run it in a shell),
// which no in-process hook can see, and one stat every couple of seconds is
// cheaper than growing a filesystem-watcher dependency for a single file.
func (w *Workspace) startSwarmTrustWatchLocked() {
	if w.swarmTrustWatch || w.ctx == nil {
		return
	}
	w.swarmTrustWatch = true
	go w.watchSwarmTrust()
}

func (w *Workspace) watchSwarmTrust() {
	// seen==false forces a re-probe on the first tick that finds the file,
	// closing the window between the lease's own store read and this
	// goroutine's first stat.
	var lastMod time.Time
	var lastSize int64
	seen := false
	t := time.NewTicker(swarmTrustPollInterval)
	defer t.Stop()
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-t.C:
		}
		fi, err := os.Stat(config.TrustStorePath())
		if err != nil {
			// A vanished store is an empty store (LoadTrustStore says so) —
			// still a change the note must reflect, exactly once.
			if seen {
				seen = false
				w.refreshSwarmWorktreeTrust()
			}
			continue
		}
		if seen && fi.ModTime().Equal(lastMod) && fi.Size() == lastSize {
			continue
		}
		seen, lastMod, lastSize = true, fi.ModTime(), fi.Size()
		w.refreshSwarmWorktreeTrust()
	}
}

// refreshSwarmWorktreeTrust re-probes the CURRENT trust verdict for every
// worktree in the batch and rewrites the note when any changed. Trusted — the
// lease-time verdict — is deliberately left alone: it reports how the
// sub-agent actually boots, and a grant cannot reach an agent already running.
// Only the grant hint (and its all-granted acknowledgment) follows the store.
func (w *Workspace) refreshSwarmWorktreeTrust() {
	store, err := config.LoadTrustStore()
	if err != nil {
		// A corrupt store is a hard error everywhere trust is ENFORCED; here
		// it just means the note keeps its last-known facts.
		return
	}
	w.mu.Lock()
	changed := false
	for i := range w.swarmBatch {
		// A reclaimed worktree has no path left to probe, and its lease-time
		// verdict is already the final word on how that sub-agent booted.
		if w.swarmBatch[i].Reclaimed {
			continue
		}
		now, _ := worktreeTrustVerdict(store, w.swarmBatch[i].Path)
		if w.swarmBatch[i].TrustedNow != now {
			w.swarmBatch[i].TrustedNow = now
			changed = true
		}
	}
	var msg string
	if changed {
		msg = renderSwarmWorktreeNote(w.swarmBatch, w.swarmLive)
	}
	w.mu.Unlock()
	if changed {
		w.note(swarmWorktreeNoteKey, msg, "info")
	}
}

// releaseSwarmLease counts one lease down and rewrites the note, which flips to
// the past tense once the last one goes.
//
// The batch entry is KEPT either way, but a reclaimed one is marked: its path
// is gone, so the note must stop offering it for review and stop naming it in
// the `terva trust` hint. A worktree that survived is still the thing an
// operator would act on and reads exactly as it did before.
//
// Clamped at zero rather than trusting the count. The release hook is
// exactly-once per agent by contract, but this is host state mutated from swarm
// goroutines, and a negative live count would render "ran" over a note that is
// in fact still live.
func (w *Workspace) releaseSwarmLease(path string, reclaimed bool) {
	w.mu.Lock()
	if w.swarmLive > 0 {
		w.swarmLive--
	}
	if reclaimed && path != "" {
		for i := range w.swarmBatch {
			if w.swarmBatch[i].Path == path {
				w.swarmBatch[i].Reclaimed = true
				break
			}
		}
	}
	msg := renderSwarmWorktreeNote(w.swarmBatch, w.swarmLive)
	w.mu.Unlock()
	w.note(swarmWorktreeNoteKey, msg, "info")
}
