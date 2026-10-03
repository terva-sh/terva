package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/worktree"
)

// A worker member's worktree.
//
// 🔑 The member owns its worktree, not the worker. A worker is one process of
// the member, and the roster can retire it while the member's work goes on,
// so the swarm never acquires or releases this lease (SpawnRequest.Dir). The
// talkoot gives it back only when the member leaves its worktree: it leaves
// the roster, turns native, or moves to the home checkout.
//
// 🔑 The name and the claim owner come from the talkoot and member ids, so no
// record is needed to find the lease again. The worktree engine reads a claim
// with the same owner as its own, whatever process holds it, so the next
// process re-acquires the member's worktree after a restart. A worktree that
// was removed from disk comes back on its branch, with the work committed
// there.
//
// 🚨 An acquire that fails fails the delivery. Nothing falls back to the home
// checkout, where the member's posture was never meant to run
// (TKT-01M396QWR5).

// memberLeaser acquires and gives back a worker member's worktree. The
// workspace's worktree manager serves it, and a test puts a fake in its place.
type memberLeaser interface {
	// acquire returns the member's worktree, creating it or claiming it
	// again as needed.
	acquire(talkootID, member string) (string, error)
	// release gives the worktree back. A worktree that holds no work is
	// removed, and one that holds work is kept with its claim dropped.
	release(talkootID, member string)
	// held reports whether the member may still hold a worktree. A lookup
	// that fails says it may.
	held(talkootID, member string) bool
}

// gitLeases is the memberLeaser of a workspace's own repository.
type gitLeases struct{ w *Workspace }

// memberWorktreeName names a member's worktree. The engine folds repeated
// dashes and cuts a name at 64 characters, and an id may hold dashes, so the
// readable part alone could name two members' worktrees alike. The hash of
// the exact pair keeps each apart.
func memberWorktreeName(talkootID, member string) string {
	cut := func(s string) string {
		parts := strings.FieldsFunc(s[:min(len(s), 20)], func(r rune) bool { return r == '-' })
		return strings.Join(parts, "-")
	}
	sum := sha256.Sum256([]byte(talkootID + "/" + member))
	return "talkoot-" + cut(talkootID) + "-" + cut(member) + "-" + hex.EncodeToString(sum[:6])
}

func (g gitLeases) env(talkootID, member string) worktree.Env {
	return worktree.HostEnv(config.TervaHome(), g.w.cwd, "talkoot:"+talkootID+"/"+member)
}

func (g gitLeases) acquire(talkootID, member string) (string, error) {
	res, err := swarmWorktreeMgr.Create(g.env(talkootID, member), worktree.CreateArgs{
		Name: memberWorktreeName(talkootID, member), ReuseIfAvailable: true,
	})
	if err != nil {
		return "", err
	}
	if res == nil || res.Path == "" {
		return "", errors.New("the worktree engine returned no path")
	}
	return res.Path, nil
}

func (g gitLeases) held(talkootID, member string) bool {
	list, err := swarmWorktreeMgr.List(g.env(talkootID, member), worktree.ListFilter{})
	if err != nil {
		return true
	}
	name := memberWorktreeName(talkootID, member)
	for _, it := range list.Worktrees {
		if it.Name == name {
			return true
		}
	}
	return false
}

func (g gitLeases) release(talkootID, member string) {
	env, name := g.env(talkootID, member), memberWorktreeName(talkootID, member)
	// A member that never had a worktree, or gave it back already, has
	// nothing to release.
	if !g.held(talkootID, member) {
		return
	}
	// Reclaim reports a kept worktree as a result, not an error. The claim
	// goes either way, or the worktree reads as owned by a member that left.
	rec, err := swarmWorktreeMgr.Reclaim(env, worktree.ReclaimArgs{Name: name})
	if err == nil && rec != nil && rec.Removed {
		return
	}
	if _, rerr := swarmWorktreeMgr.Release(env, worktree.ReleaseArgs{Name: name}); rerr != nil {
		g.w.diagf("talkoot %s: member %s's worktree %s was not given back: reclaim: %v; release: %v", talkootID, member, name, err, rerr)
	}
}

// leases returns the workspace's memberLeaser.
func (w *Workspace) leases() memberLeaser {
	if w.talkootLeases != nil {
		return w.talkootLeases
	}
	return gitLeases{w: w}
}

// memberLease returns worker member's worktree in talkoot run, or an error
// that fails the delivery.
func (w *Workspace) memberLease(run *talkootRun, member string) (string, error) {
	dir, err := w.leases().acquire(run.id, member)
	if err != nil {
		return "", fmt.Errorf("the member's worktree could not be acquired, so it takes no turn: %w", err)
	}
	return dir, nil
}

// prepareMemberDir acquires a native member's worktree lease and caches its
// directory on the run. It runs git, so it must not run under w.mu. The lease
// is the member's, keyed by talkoot and member, so acquiring it again after a
// restart returns the same checkout.
func (w *Workspace) prepareMemberDir(run *talkootRun, member string) error {
	dir, err := w.memberLease(run, member)
	if err != nil {
		return err
	}
	w.talkoot.mu.Lock()
	if run.memberDirs == nil {
		run.memberDirs = map[string]string{}
	}
	run.memberDirs[member] = dir
	w.talkoot.mu.Unlock()
	return nil
}

// nativeWorktreeSeat returns the run and member of a seated native member in
// a worktree, and false for any other session.
func (w *Workspace) nativeWorktreeSeat(sessID string) (*talkootRun, string, bool) {
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	b := w.talkoot.seats[sessID]
	if b == nil {
		return nil, "", false
	}
	m, ok := memberOf(*b.run.roster.Load(), b.member)
	if !ok || m.Driver != talkoot.DriverNative {
		return nil, "", false
	}
	// A workspace: either member is in its worktree only after it entered.
	if m.Workspace != talkoot.WorkspaceWorktree && !(m.Workspace == talkoot.WorkspaceEither && b.run.inside[b.member]) {
		return nil, "", false
	}
	return b.run, b.member, true
}

// outOfWorktree reports whether the roster and the member's moves keep
// member out of its worktree now, so its lease may go back.
func (w *Workspace) outOfWorktree(run *talkootRun, member string) bool {
	m, ok := memberOf(*run.roster.Load(), member)
	if !ok {
		return true
	}
	switch m.Workspace {
	case talkoot.WorkspaceWorktree:
		return false
	case talkoot.WorkspaceEither:
		w.talkoot.mu.Lock()
		defer w.talkoot.mu.Unlock()
		return !run.inside[member]
	}
	return true
}

// prepareSessionDir acquires the worktree of the native member a session is
// seated as, before resolve takes w.mu to build it. Any other session needs
// nothing.
func (w *Workspace) prepareSessionDir(sessID string) error {
	run, member, ok := w.nativeWorktreeSeat(sessID)
	if !ok {
		return nil
	}
	return w.prepareMemberDir(run, member)
}

// talkootMemberDir returns the worktree a seated native member's session runs
// in, and false for any other session. It only reads the directory cached by
// prepareMemberDir, so a build under w.mu runs no git.
//
// 🚨 A member whose lease was not acquired fails the session build. Nothing
// falls back to the home checkout, where the roster's one-writer rule assumed
// the member would not write (TKT-01M396QWR5 for workers).
func (w *Workspace) talkootMemberDir(sessID string) (string, bool, error) {
	run, member, ok := w.nativeWorktreeSeat(sessID)
	if !ok {
		return "", false, nil
	}
	w.talkoot.mu.Lock()
	dir := run.memberDirs[member]
	w.talkoot.mu.Unlock()
	if dir == "" {
		return "", false, fmt.Errorf("member %s has no worktree lease, so its session is not built", member)
	}
	return dir, true, nil
}

// workerStopWait bounds how long a leaving member's worktree waits for its
// worker to stop before it is given back.
const workerStopWait = 2 * time.Minute

// holdLeaseLocked records that worker id runs in member's worktree. The
// caller holds w.talkoot.mu.
func holdLeaseLocked(run *talkootRun, member, id string) {
	if run.leaseHolders == nil {
		run.leaseHolders = map[string]map[string]bool{}
	}
	if run.leaseHolders[member] == nil {
		run.leaseHolders[member] = map[string]bool{}
	}
	run.leaseHolders[member][id] = true
}

// nextUnleaseLocked starts a new release of member's worktree and returns its
// generation. The caller holds w.talkoot.mu.
func nextUnleaseLocked(run *talkootRun, member string) uint64 {
	if run.unleaseGen == nil {
		run.unleaseGen = map[string]uint64{}
	}
	run.unleaseGen[member]++
	return run.unleaseGen[member]
}

// leaseHeld reports whether a worker or a native session that ran in
// member's worktree may still run there.
//
// 🔑 A native member that leaves its worktree keeps its old session until
// that session's turn ends, and the turn's tools still work in the worktree.
// So a native holder holds the lease while its session runs a turn.
func (w *Workspace) leaseHeld(run *talkootRun, h workerHost, member string) bool {
	w.talkoot.mu.Lock()
	ids := make([]string, 0, len(run.leaseHolders[member]))
	for id := range run.leaseHolders[member] {
		ids = append(ids, id)
	}
	w.talkoot.mu.Unlock()
	for _, id := range ids {
		if h != nil {
			if _, live := h.state(id); live {
				return true
			}
		}
		if s := w.existing(id); s != nil && s.busy() {
			return true
		}
	}
	return false
}

// releaseMemberLease gives back the worktree of a member that left it, once
// every worker that ran there has stopped.
//
// 🔑 A retired worker can still run after its seat is gone, and a member can
// leave, return, and leave again while an earlier release waits. So the
// release waits for each worker the member's worktree ever held in this run,
// not for the one seated when the update committed.
//
// 🔑 The last check and the release run under run.workerMu, the lock a
// binding acquires the lease under, and only while the roster still keeps the
// member out of its worktree. No binding can take the lease between them.
//
// A newer release of the same member stands the older one down, so a member
// that leaves more than once gives its worktree back once.
func (w *Workspace) releaseMemberLease(run *talkootRun, member string, gen uint64) {
	h := w.workers()
	deadline := time.Now().Add(workerStopWait)
	for {
		if !w.leaseHeld(run, h, member) {
			run.workerMu.Lock()
			if w.leaseHeld(run, h, member) {
				run.workerMu.Unlock()
				continue
			}
			w.talkoot.mu.Lock()
			newest := run.unleaseGen[member] == gen
			w.talkoot.mu.Unlock()
			if !newest {
				run.workerMu.Unlock()
				return
			}
			if w.outOfWorktree(run, member) {
				w.leases().release(run.id, member)
				w.talkoot.mu.Lock()
				delete(run.leaseHolders, member)
				delete(run.memberDirs, member)
				w.talkoot.mu.Unlock()
			}
			run.workerMu.Unlock()
			return
		}
		if time.Now().After(deadline) {
			// ⚠️ A worker or a turn that will not stop keeps the worktree
			// claimed. The claim reads as stale once this process ends.
			w.diagf("talkoot %s: member %s still ran in its worktree, so the worktree stays claimed", run.id, member)
			return
		}
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// retryMemberReleases gives back, at a run's start, the worktree of each member
// the room ever seated that the roster no longer puts in a worktree. A release
// that the last process began and did not finish, because it shut down while a
// worker still ran, ends here. A member that holds no worktree, such as a
// worker in the home checkout, waits for nothing.
func (w *Workspace) retryMemberReleases(run *talkootRun, seated []string) {
	for _, member := range seated {
		// A movable member starts each run in the home checkout, so its
		// lease from the last run goes back too.
		if !w.outOfWorktree(run, member) {
			continue
		}
		go func() {
			if !w.leases().held(run.id, member) {
				return
			}
			w.talkoot.mu.Lock()
			if sid := run.seats[member]; sid != "" {
				holdLeaseLocked(run, member, sid)
			}
			gen := nextUnleaseLocked(run, member)
			w.talkoot.mu.Unlock()
			w.releaseMemberLease(run, member, gen)
		}()
	}
}
