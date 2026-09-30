package workspace

import (
	"errors"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/talkoot"
)

// A worker member's idle stop.
//
// 🔑 A worker process that ended its turn waits for its member's next
// envelope. After the member's idle_stop it stops, and the member keeps its
// seat, its worktree, and its conversation. The next envelope revives the
// worker with Resume, under the limits the roster gives it then, as any
// revival after a restart does.
//
// 🔑 A stop needs the worker idle at the moment it happens: its process is
// the one whose turn ended, and no turn is open. The check runs under
// run.workerMu, the lock a binding holds while it opens a turn. The member's
// send lock, which a delivery takes first, is held through the check and the
// stop, so a delivery and an idle stop cannot cross.
//
// 🔑 The worker is idle only once its process owes no turn: each message it
// was sent, the spawn's task included, ends in a turn of its own, and a
// message sent while a turn runs is one more turn after it.
//
// ⚠️ A process this run did not start gets no timer. A worker still live when
// its talkoot loads again could be in a turn of the run before, which no count
// here holds, and the delivery that adopts it does not change that. It runs
// until its next process: a revival, a retirement, or a restart.

// idleAfter returns how long member m's worker may idle. A test shortens it.
func (w *Workspace) idleAfter(m talkoot.Member) (time.Duration, bool) {
	d, on := m.IdleStopAfter()
	if on && w.talkootIdleClock != nil {
		d = w.talkootIdleClock(d)
	}
	return d, on
}

// idleArm is one member's armed idle timer: the worker process it waits on,
// when that process's turn ended, and the arm's generation.
type idleArm struct {
	timer *time.Timer
	id    string
	token uint64
	at    time.Time
	gen   uint64
}

// armIdleStop starts the idle timer of member's worker id, whose process
// token has just ended a turn.
func (w *Workspace) armIdleStop(run *talkootRun, member, id string, token uint64) {
	w.armIdleStopAt(run, member, id, token, time.Now(), 0)
}

// armIdleStopAt arms member's idle timer to fire at the idle stop the roster
// gives it now, counted from at. A member the roster no longer runs as a
// worker, or runs with idle_stop off, gets no timer. A nonzero expect arms
// only while the member's arm generation is still expect, so an arm that a
// delivery or a turn end replaced is not restored.
func (w *Workspace) armIdleStopAt(run *talkootRun, member, id string, token uint64, at time.Time, expect uint64) {
	m, ok := memberOf(*run.roster.Load(), member)
	on := ok && m.Driver != talkoot.DriverNative
	var d time.Duration
	if on {
		d, on = w.idleAfter(m)
	}
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	if expect != 0 && run.idleGen[member] != expect {
		return
	}
	// 🔑 The check and the arm share the lock a delivery opens its turn
	// under, so no delivery lands between them.
	if !idleLocked(run, member, token) {
		return
	}
	gen := disarmIdleStopLocked(run, member)
	if !on || run.idleClosed {
		return
	}
	if run.idleArm == nil {
		run.idleArm = map[string]idleArm{}
	}
	run.idleArm[member] = idleArm{
		timer: time.AfterFunc(max(d-time.Since(at), 0), func() { w.idleStop(run, member, id, token, gen, at) }),
		id:    id, token: token, at: at, gen: gen,
	}
}

// disarmIdleStopLocked stops member's idle timer and returns the generation a
// new arm takes. A timer that already fired finds its generation old. The
// caller holds w.talkoot.mu.
func disarmIdleStopLocked(run *talkootRun, member string) uint64 {
	if a, ok := run.idleArm[member]; ok {
		a.timer.Stop()
		delete(run.idleArm, member)
	}
	if run.idleGen == nil {
		run.idleGen = map[string]uint64{}
	}
	run.idleGen[member]++
	return run.idleGen[member]
}

// reconcileIdleStops brings a run's idle timers to the roster as it is now,
// after an update. Each armed timer is armed again from the moment its turn
// ended, so an idle_stop that changes or turns off takes effect on a worker
// already waiting. A waiting worker with no timer, whose idle_stop was off,
// gets one from now. A member left with no seated worker shows no idle stop.
func (w *Workspace) reconcileIdleStops(run *talkootRun) {
	w.talkoot.mu.Lock()
	arms := maps.Clone(run.idleArm)
	seats := maps.Clone(run.seats)
	w.talkoot.mu.Unlock()
	for member, a := range arms {
		w.armIdleStopAt(run, member, a.id, a.token, a.at, a.gen)
	}
	r := *run.roster.Load()
	for _, m := range r.Members {
		if m.Driver != talkoot.DriverNative {
			w.rearmIdleStop(run, m.ID)
		}
	}
	for _, member := range run.idleMembers() {
		if m, ok := memberOf(r, member); !ok || m.Driver == talkoot.DriverNative || seats[member] == "" {
			run.setIdle(member, false)
		}
	}
}

// rearmIdleStop arms member's idle timer from now when its worker waits with
// none: a delivery that failed disarmed it, or idle_stop was off. The worker
// must be a process this run started or adopted, because only then does
// "no turn open" mean the worker is idle.
func (w *Workspace) rearmIdleStop(run *talkootRun, member string) {
	h := w.workers()
	if h == nil {
		return
	}
	w.talkoot.mu.Lock()
	id, token := run.seats[member], run.workerRun[member]
	_, armed := run.idleArm[member]
	idle := idleLocked(run, member, token)
	w.talkoot.mu.Unlock()
	if id == "" || token == 0 || armed || !idle {
		return
	}
	if _, live := h.state(id); live {
		w.armIdleStop(run, member, id, token)
	}
}

// closeIdleStops disarms every idle timer of a run that closes, and arms no
// more.
func (w *Workspace) closeIdleStops(run *talkootRun) {
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	run.idleClosed = true
	for member := range run.idleArm {
		disarmIdleStopLocked(run, member)
	}
}

// idleStop stops member's worker id if it is still idle: the same process,
// still seated, with no turn open, and no delivery since the timer armed. It
// reads the roster again first: idle_stop off keeps the worker, and a longer
// idle_stop arms the timer for the rest.
func (w *Workspace) idleStop(run *talkootRun, member, id string, token, gen uint64, at time.Time) {
	h := w.workers()
	if h == nil {
		return
	}
	m, ok := memberOf(*run.roster.Load(), member)
	if !ok || m.Driver == talkoot.DriverNative {
		return
	}
	d, on := w.idleAfter(m)
	if !on {
		return
	}
	if time.Since(at) < d {
		w.talkoot.mu.Lock()
		current := run.idleGen[member] == gen
		w.talkoot.mu.Unlock()
		if current {
			w.armIdleStopAt(run, member, id, token, at, gen)
		}
		return
	}
	stopped, failed := func() (bool, bool) {
		// 🔑 run.mu, read, keeps a roster update out, so the idle_stop read
		// here is the one in force at the stop. The member's send lock keeps
		// its next delivery out until the stop is done. run.workerMu is held
		// only for the check, so no other member's reports or bindings wait
		// on the stop. The order is the one a delivery takes.
		run.mu.RLock()
		defer run.mu.RUnlock()
		if run.closed {
			return false, false
		}
		send := w.memberSendMu(run, member)
		send.Lock()
		defer send.Unlock()
		now, ok := memberOf(*run.roster.Load(), member)
		if !ok || now.Driver == talkoot.DriverNative {
			return false, false
		}
		if d, on := w.idleAfter(now); !on || time.Since(at) < d {
			return false, false
		}
		run.workerMu.Lock()
		w.talkoot.mu.Lock()
		idle := run.idleGen[member] == gen && run.seats[member] == id &&
			idleLocked(run, member, token)
		// The timer is spent either way. An arm a delivery replaced has a
		// newer generation and stays.
		if run.idleGen[member] == gen {
			delete(run.idleArm, member)
		}
		w.talkoot.mu.Unlock()
		run.workerMu.Unlock()
		if !idle {
			return false, false
		}
		if _, live := h.state(id); !live {
			return false, false
		}
		if err := h.stop(id); err != nil {
			w.diagf("talkoot %s: member %s's idle worker %s did not stop: %v", run.id, member, id, err)
			return false, true
		}
		run.setIdle(member, true)
		return true, false
	}()
	// ⚠️ flush takes run.mu, and a binding takes run.workerMu under it, so
	// the flush waits until workerMu is free.
	if stopped {
		run.flush()
	}
	// A stop that failed tries again after another idle stop, unless a
	// delivery or a turn end armed the member since.
	if failed {
		w.armIdleStopAt(run, member, id, token, time.Now(), gen)
	}
}

// setIdle records whether member's worker stopped for idleness.
func (r *talkootRun) setIdle(member string, idle bool) {
	r.evMu.Lock()
	defer r.evMu.Unlock()
	if !idle {
		delete(r.idle, member)
		return
	}
	if r.idle == nil {
		r.idle = map[string]bool{}
	}
	r.idle[member] = true
}

// idleMembers lists the members whose worker stopped for idleness.
func (r *talkootRun) idleMembers() []string {
	r.evMu.Lock()
	defer r.evMu.Unlock()
	return slices.Collect(maps.Keys(r.idle))
}

// retainTalkootWorker is the swarm's retention rule for a talkoot's worker:
// the sweep keeps an agent a talkoot's room seats now, however long it has
// been stopped, so a member stopped for idleness keeps its conversation.
//
// A room that cannot be read keeps the agent: the sweep only tidies, and a
// worker archived by mistake loses a member's conversation.
func retainTalkootWorker(a *swarm.Agent) bool {
	id, ok := ctrlproto.TalkootFromAddr(a.SessionID)
	if !ok || !talkoot.ValidID(id) {
		return false
	}
	lines, err := talkoot.OpenRoom(filepath.Join(talkoot.Dir(), id)).Read()
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	seats := map[string]string{}
	for _, l := range lines {
		if l.Type == talkoot.LineSeat {
			seats[l.Member] = l.Ref
		}
	}
	for _, ref := range seats {
		if ref == a.ID {
			return true
		}
	}
	return false
}
