package workspace

// The runs of a team's routines (decision 0029 rules 7 to 10 and 13). An
// approved routine's timer comes due, and this file starts a run: a session
// apart from the member's own, with the run at the root of a new chain.
//
// One run goes through these steps:
//
//   - the firing reads the routine's file, and asks every test that a resume
//     asks. A routine that is paused, ended or barred starts nothing,
//   - the router writes the root of the chain, and refuses while a pause
//     holds the member or the team,
//   - the run's session takes the member's posture, directory and tools
//     list, and no tool that sends,
//   - the session's one turn is the run. Its last text is the report, which
//     the router sends to the member's own session.
//
// 🔑 The run speaks to nobody but its member. Grok Bot's client showed a
// reviewer and its own watcher both approving one commit. One member speaks
// one time here: the member reads the report and decides what to post.
//
// A run spends as its member, so it starts only while the member and the
// team are inside their daily budgets. The routine's own daily spend stops a
// run at work, in this file. A run has talkoot_wake only for a follow-up
// that its routine allows.
//
// ⚠️ The two budgets are no strict cap. A budget is tested when a run starts,
// and the cost is counted when the run ends. The runs of several routines
// that work at one time can together pass a budget by what they spend before
// they end.

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/timers"
	"terva.sh/terva/packages/session"
)

// routineLateAfter is how late a firing may come and still be on time. The
// timer service polls, so a firing is always a little late.
const routineLateAfter = 5 * time.Minute

// routineRunLimit bounds one run. A run that waits for a person's approval of
// a tool call waits with nobody present. The deadline of a question ends
// that wait (workspace_talkoot_unanswered.go), and the limit does not count
// it. The limit is for a run that works too long. A run past the limit is
// stopped and counts as failed, so its routine pauses and a person sees why.
//
// routineRunGrace is how long a stopped run has to report its cost. After it
// the run is ended without one, so a turn that never started, or never
// returns, cannot hold its routine for good.
var (
	routineRunLimit = 30 * time.Minute
	routineRunGrace = time.Minute
)

// routineRun is the state of one run, on the seat of its session.
type routineRun struct {
	// run is the router's record: the chain's root, the routine, the member.
	run talkoot.Run
	// report holds the run's last text, from the turn that produced it.
	report atomic.Pointer[string]
	// timedOut is set when the run passed routineRunLimit.
	timedOut atomic.Bool
	// ended is set by the one call that ends the run.
	ended atomic.Bool
	// opened is set when the run's session opens a turn (talkootTurn), under
	// w.talkoot.mu. Shutdown reads it to find a run that never got a turn.
	opened atomic.Bool
	// over is set when the run's cost passed what its routine may still
	// spend today (cost). capped is set when the run was stopped for it at
	// its next step (step).
	over   atomic.Bool
	capped atomic.Bool
	// unanswered holds what the run asked a person, when nobody answered at
	// the deadline. wentOn is set when the run then called a tool and was
	// stopped for it (workspace_talkoot_unanswered.go).
	unanswered atomic.Pointer[string]
	wentOn     atomic.Bool
	// span is the limit that this run began under, and limit is the timer
	// that stops the run at it. Guarded by mu.
	//
	// The limit does not count the time that a card of the run is open
	// (hold). holds counts the open cards. left is what the limit had left
	// when the timer last started, and since is when that was. held says
	// that hold stopped the timer, and limitOver that the run's end did.
	mu        sync.Mutex
	span      time.Duration
	limit     *time.Timer
	left      time.Duration
	since     time.Time
	holds     int
	held      bool
	limitOver bool
}

func (rr *routineRun) stopLimit() {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	rr.limitOver = true
	if rr.limit != nil {
		rr.limit.Stop()
	}
}

// limitReason is the cause of a run that passed its limit.
func (rr *routineRun) limitReason() string {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return fmt.Sprintf("the run passed its limit of %s and was stopped", wakeSpan(rr.span))
}

// attachRoutineTimers registers the handler for the routines, once the
// daemon has both its talkoots and its timers, as attachWakeTimers does.
func (w *Workspace) attachRoutineTimers(svc *timers.Service) {
	if svc == nil || !w.talkoot.loaded.Load() {
		return
	}
	svc.Handle(routineTimerOwner, func(f timers.Firing) { w.routineTimerFired(svc, f) })
}

// routineTimerFired is the handler of a routine's timer. The timer service
// calls its handlers one at a time, so this only hands the firing to the
// team's own goroutine, where a run may take its time to start.
//
// ⚠️ A daemon that stops after this returns and before the run starts loses
// that one time. The timer recurs, and the next time is a new firing.
func (w *Workspace) routineTimerFired(svc *timers.Service, f timers.Firing) {
	var d routineData
	if err := json.Unmarshal(f.Timer.Data, &d); err != nil || d.Talkoot == "" {
		w.diagf("talkoot: the routine timer %s names no team, so it starts nothing", f.Timer.ID)
		return
	}
	run, err := w.talkootRunOf(d.Talkoot)
	if err != nil {
		w.diagf("talkoot %s: the routine %s came due, and this daemon does not run the team, so it starts nothing: %v", d.Talkoot, f.Timer.ID, err)
		return
	}
	// The service's own reading of the clock, which a test moves.
	now := f.Due.Add(f.Late)
	run.later(func() { w.routineFired(svc, run, f, now) })
}

// routineEnd says why the routine r has reached the end that its approval
// gave it, or "" when it has not.
func routineEnd(r talkoot.Routine, now time.Time) string {
	switch {
	case !r.EndsAt.IsZero() && !now.Before(r.EndsAt):
		return fmt.Sprintf("its last day was %s", r.EndsAt.UTC().Format(time.DateOnly))
	case r.Fields.EndAfterRuns > 0 && r.Runs >= r.Fields.EndAfterRuns:
		return fmt.Sprintf("it started %d of its %d runs", r.Runs, r.Fields.EndAfterRuns)
	}
	return ""
}

// routineFired decides what one firing of a routine does, and does it.
// routineReady holds the tests that a firing shares with an outside event.
func (w *Workspace) routineFired(svc *timers.Service, run *talkootRun, f timers.Firing, now time.Time) {
	run.routineMu.Lock()
	defer run.routineMu.Unlock()
	r, ok := w.routineReady(run, f.Timer.ID, now)
	if !ok {
		return
	}
	start := talkoot.RunStart{Routine: r}
	if f.Missed > 0 || f.Late > routineLateAfter {
		if !r.Fields.CatchUp {
			w.skipRoutine(run, r, fmt.Sprintf("the daemon was not at work at %s, and this routine skips a time that it missed",
				f.Due.UTC().Format("2006-01-02 15:04 UTC")), now)
			return
		}
		start.Late, start.Missed = f.Late, f.Missed
	}
	if r.Kind == talkoot.RoutineWatcher {
		// A watcher's time runs its command, and no model. A late check is
		// a check like any other.
		w.watcherFired(run, r, now)
		return
	}
	w.startRoutineRun(run, r, start, now)
}

// routineReady reads the routine rid, and asks every test that holds for
// whatever starts a run of it: a firing of its timer, or an outside event.
// It reports false when the routine starts nothing now, and it has then done
// what follows from that. The caller holds run.routineMu.
//
// 🚨 Every test reads what holds now. The policy is read live, so a person
// who turned routines off gets no run from a routine that was approved
// before. The routine's file is read here and not from the timer, so a pause
// or a delete stops the next run whatever its timer still holds.
func (w *Workspace) routineReady(run *talkootRun, rid string, now time.Time) (talkoot.Routine, bool) {
	if !config.TalkootRoutines().On {
		return talkoot.Routine{}, false
	}
	r, err := talkoot.LoadRoutine(run.dir, rid)
	if err != nil {
		w.diagf("talkoot %s: routine %s came due, and its record does not read, so its timer was removed: %v", run.id, rid, err)
		w.cancelRoutineTimer(run, rid)
		return talkoot.Routine{}, false
	}
	if r.Status != talkoot.RoutineActive {
		// A pause or an end that could not remove the timer left it. The
		// file decides, and a resume sets the timer again.
		w.cancelRoutineTimer(run, rid)
		return talkoot.Routine{}, false
	}
	// 🚨 The room counts the runs too, and its count has a seal. A record
	// that counts fewer runs gets no more of them.
	r.Runs = max(r.Runs, routineStanding(run, rid).runs)
	if why := routineEnd(r, now); why != "" {
		w.endRoutine(run, r, why, now)
		return talkoot.Routine{}, false
	}
	bar, err := routineBar(run, r, now)
	if err != nil {
		w.diagf("talkoot %s: routine %s came due, and the room does not read, so it starts nothing: %v", run.id, rid, err)
		return talkoot.Routine{}, false
	}
	if bar != nil {
		// The list says why an active routine has no timer.
		w.diagf("talkoot %s: routine %s came due and may not run, so its timer was removed: %v", run.id, rid, bar)
		w.cancelRoutineTimer(run, rid)
		return talkoot.Routine{}, false
	}
	return r, true
}

// cancelRoutineTimer removes the timer of a routine that may not run.
func (w *Workspace) cancelRoutineTimer(run *talkootRun, rid string) {
	svc := w.Timers()
	if svc == nil {
		return
	}
	if _, err := svc.Cancel(routineTimerOwner, rid); err != nil {
		w.diagf("talkoot %s: the timer of routine %s was not removed. It starts nothing, because a run reads the routine's record first: %v", run.id, rid, err)
	}
}

// endRoutine gives a routine the status ended: it reached the end that its
// approval gave it. It does not resume. The caller holds run.routineMu.
func (w *Workspace) endRoutine(run *talkootRun, r talkoot.Routine, why string, now time.Time) {
	r.Status, r.ChangedBy, r.ChangedAt, r.Reason = talkoot.RoutineEnded, "", now, why
	if err := talkoot.SaveRoutine(run.dir, r); err != nil {
		// The timer goes all the same. routineBar holds the same two tests,
		// so the list says why an active routine has no timer.
		w.diagf("talkoot %s: routine %s reached its end, and its record could not say so: %v", run.id, r.ID, err)
	}
	w.cancelRoutineTimer(run, r.ID)
	if err := run.room.Append(talkoot.RoutineLine(r, talkoot.RoutineEnded, "", why, now)); err != nil {
		w.diagf("talkoot %s: routine %s reached its end, and the room could not record it: %v", run.id, r.ID, err)
	}
	run.flush()
}

// skipRoutine records a time at which the routine started no run, and why.
//
// 🔑 A reason that repeats is written one time. A routine of every quarter
// hour whose member is paused for a night would otherwise fill the room with
// one line. A run or another step of the routine between two skips makes the
// second one new again. The run's marks hold the newest line about the
// routine, so this reads no file.
func (w *Workspace) skipRoutine(run *talkootRun, r talkoot.Routine, why string, now time.Time) {
	if routineStanding(run, r.ID).skip == why {
		return
	}
	if err := run.room.Append(talkoot.RoutineLine(r, talkoot.RoutineSkipped, "", why, now)); err != nil {
		w.diagf("talkoot %s: routine %s started no run (%s), and the room could not record it: %v", run.id, r.ID, why, err)
	}
	run.flush()
}

// newRunSession makes the session file of a run. The caller holds w.mu.
func (w *Workspace) newRunSession(run *talkootRun, m talkoot.Member, r talkoot.Routine) (*session.Session, string, error) {
	prov, model, reasoning, err := w.memberModel(m)
	if err != nil {
		return nil, "", err
	}
	sess, err := session.NewSession(w.root, w.cwd, prov, model, w.version)
	if err != nil {
		return nil, "", fmt.Errorf("create session: %w", err)
	}
	fail := func(err error) (*session.Session, string, error) {
		_ = sess.Close()
		_ = os.Remove(sess.Path)
		return nil, "", err
	}
	title := run.id + " · " + m.ID + " · " + r.Kind + " run"
	_ = session.RenameSession(sess.Path, title)
	sess.Meta.Title = title
	if err := sess.SetCreationSpec(m.Persona, "", "", nil, 0); err != nil {
		return fail(fmt.Errorf("persist session spec: %w", err))
	}
	if reasoning != "" {
		if err := sess.UpdateReasoning(reasoning); err != nil {
			return fail(fmt.Errorf("persist reasoning: %w", err))
		}
	}
	return sess, build.SessionIDFromPath(sess.Path), nil
}

// startRoutineRun starts one run of the routine r. The caller holds
// run.routineMu.
//
// The order is the count, the session's file, the root in the room with the
// seat, and then the session itself. Each failure undoes the steps before it.
//
// 🚨 The count is saved first. It is a limit, so a run that the record could
// not count does not start. A count that could not be taken back ends the
// routine one run early, which is the safe side.
//
// 🚨 The seat is set inside the router call that writes the root. A roster
// update holds the run's lock for its commit, so it finds the seat of every
// run that began before it, and it stops the run of a member that it changed
// (stopChangedRuns). A run that begins after it reads the new roster.
//
// 🚨 The session is made from the member as the roster held it before that
// call. The call compares that member with the roster again, under the run's
// lock. An update that committed in between found no seat to stop, and the
// run would work with a persona, a model or a directory that the roster no
// longer gives. Such a start is undone and tried one more time.
func (w *Workspace) startRoutineRun(run *talkootRun, r talkoot.Routine, start talkoot.RunStart, now time.Time) (started bool) {
	settled, started := w.tryRoutineRun(run, r, start, now)
	if settled {
		return started
	}
	if settled, started = w.tryRoutineRun(run, r, start, now); !settled {
		w.noRun(run, r, start, "the roster changed while the run started", now)
	}
	return started
}

// noRun records that a start of the routine r began no run, and why. A time
// of the routine gets a skipped line. A follow-up is dropped, and its line
// in the room gives the reason.
func (w *Workspace) noRun(run *talkootRun, r talkoot.Routine, start talkoot.RunStart, why string, now time.Time) {
	if start.Wake == nil {
		w.skipRoutine(run, r, why, now)
		return
	}
	w.dropRunWake(run, *start.Wake, why)
}

// dropRunWake closes a follow-up that starts no run. The room's line gives
// the reason.
func (w *Workspace) dropRunWake(run *talkootRun, wk talkoot.Wake, why string) {
	err := run.do(func(rt *talkoot.Router) error { return rt.DropWake(wk, why) })
	if err != nil && !errors.Is(err, ErrTalkootClosed) {
		w.diagf("talkoot %s: the follow-up %s of routine %s starts no run (%s), and the room could not record it: %v", run.id, wk.ID, wk.Routine, why, err)
	}
	run.flush()
}

// errRunRosterMoved reports a roster update between the read of a run's
// member and the seat of the run.
var errRunRosterMoved = errors.New("talkoot: the roster changed while the run started")

// runSeated is called after a run has its root and its seat, and before its
// text is queued. A test stops the daemon there.
var runSeated = func() {}

// runPrepared is called after a run's session file is made and before its
// root is written. A test replaces it to change the roster at that point.
var runPrepared = func() {}

// runBuilding is called after a run has its root and before its session is
// built. A test replaces it to make that build fail.
var runBuilding = func() error { return nil }

// tryRoutineRun is one try of startRoutineRun. settled is false only when a
// roster update moved the member under it, and it then left nothing behind.
// started says that the run's session took the run's first text. A run
// whose session could not be built has a root in the room and ends as
// failed, and it did not start.
func (w *Workspace) tryRoutineRun(run *talkootRun, r talkoot.Routine, start talkoot.RunStart, now time.Time) (settled, started bool) {
	m, ok := memberOf(*run.roster.Load(), r.Member)
	switch {
	case !ok:
		return true, false
	case m.Driver != talkoot.DriverNative:
		w.noRun(run, r, start, m.ID+" is a worker member now, and a run needs a native member", now)
		return true, false
	}
	// A worktree member's run works in the member's worktree, as its own
	// session does. The lease runs git, so it comes before w.mu.
	w.talkoot.mu.Lock()
	inside := run.inside[m.ID]
	w.talkoot.mu.Unlock()
	if m.Workspace == talkoot.WorkspaceWorktree || m.Workspace == talkoot.WorkspaceEither && inside {
		if err := w.prepareMemberDir(run, m.ID); err != nil {
			w.noRun(run, r, start, "the member's worktree could not be acquired: "+failureReason(err.Error()), now)
			return true, false
		}
	}
	r.Runs++
	if err := talkoot.SaveRoutine(run.dir, r); err != nil {
		w.diagf("talkoot %s: routine %s starts no run, because its record could not count it: %v", run.id, r.ID, err)
		return true, false
	}
	uncount := func() {
		r.Runs--
		if err := talkoot.SaveRoutine(run.dir, r); err != nil {
			w.diagf("talkoot %s: routine %s counts a run that did not start: %v", run.id, r.ID, err)
		}
	}
	w.mu.Lock()
	sess, id, err := w.newRunSession(run, m, r)
	w.mu.Unlock()
	if err != nil {
		uncount()
		w.noRun(run, r, start, "the run's session could not be made: "+failureReason(err.Error()), now)
		return true, false
	}
	discard := func() {
		_ = sess.Close()
		_ = os.Remove(sess.Path)
	}
	b := &seatBinding{run: run, member: m.ID, routine: &routineRun{}}
	start.Routine, start.Session = r, id
	var text string
	runPrepared()
	err = run.do(func(rt *talkoot.Router) error {
		// The roster changes only under the lock that this call holds.
		if cur, kept := memberOf(*run.roster.Load(), m.ID); !kept || !sameWorker(m, cur) {
			return errRunRosterMoved
		}
		rr, t, err := rt.StartRun(start)
		if err != nil {
			return err
		}
		b.routine.run, text = rr, t
		w.talkoot.mu.Lock()
		if w.talkoot.seats == nil {
			w.talkoot.seats = map[string]*seatBinding{}
		}
		w.talkoot.seats[id] = b
		w.talkoot.markMemberLocked(id)
		w.talkoot.mu.Unlock()
		return nil
	})
	if err != nil {
		discard()
		uncount()
		switch {
		case errors.Is(err, errRunRosterMoved):
			return false, false
		case errors.Is(err, ErrTalkootClosed), errors.Is(err, talkoot.ErrRunStopping):
			// The daemon is stopping. No pause holds the routine, so the
			// room gets no line that blames one.
		case errors.Is(err, talkoot.ErrWakeGone):
			// A person cancelled the follow-up, or it started its run at an
			// earlier firing. The room holds that already.
		case errors.Is(err, talkoot.ErrRunAtWork) && start.Wake != nil:
			// 🔑 A follow-up waits for the run that is at work. That run is
			// often the one that set it, when its work takes longer than
			// the delay it asked for. The follow-up stays open, and its
			// timer tries again (runWakeTimerFired).
		case errors.Is(err, talkoot.ErrRunAtWork):
			w.noRun(run, r, start, "its last run is still at work", now)
		case errors.Is(err, talkoot.ErrRunMemberAtWork) && start.Wake != nil:
			// A follow-up waits for the checkout as it waits for its own
			// routine's run. It stays open, and its timer tries again.
		case errors.Is(err, talkoot.ErrRunMemberAtWork):
			w.noRun(run, r, start, strings.TrimPrefix(err.Error(), talkoot.ErrRunMemberAtWork.Error()+": "), now)
		case errors.Is(err, talkoot.ErrRunHeld):
			w.noRun(run, r, start, "a pause holds it: "+strings.TrimPrefix(err.Error(), talkoot.ErrRunHeld.Error()+": "), now)
		case errors.Is(err, talkoot.ErrRunSpent):
			// 🚨 The routine passed its own daily limit (decision 0029 rule
			// 17). The run that passed it pauses the routine at its end. A
			// routine that a person resumed on the same day arrives here.
			w.noRun(run, r, start, strings.TrimPrefix(err.Error(), talkoot.ErrRunSpent.Error()+": "), now)
		default:
			w.diagf("talkoot %s: routine %s started no run: %v", run.id, r.ID, err)
		}
		return true, false
	}
	if why := routineEnd(r, now); why != "" {
		// This run is the last one. The routine ends now, so no timer of it
		// fires while the run works.
		w.endRoutine(run, r, why, now)
	}
	w.mu.Lock()
	var s *wsSession
	err = runBuilding()
	if err == nil {
		s, err = w.buildSession(id, sess, nil, build.ResumeWindow{})
	}
	if err == nil {
		// The session hears its own cost, so it holds its run.
		s.run.Store(b.routine)
		w.sessions[id] = s
	}
	w.mu.Unlock()
	if err != nil {
		discard()
		w.unseatTalkoot(id)
		w.finishRun(run, b.routine.run, talkoot.RunEnd{Failed: "the run's session could not be built: " + failureReason(err.Error())}, true)
		// 🚨 The room holds the root, and no session read the run's text.
		// So the run did not start for whatever began it: a watcher keeps
		// its change, and finds it again after a person resumes it.
		return true, false
	}
	w.BroadcastAll(ctrlproto.SessionsChangedEvent())
	rr := b.routine
	grace := routineRunGrace
	rr.mu.Lock()
	rr.span = routineRunLimit
	rr.left, rr.since = rr.span, time.Now()
	rr.limit = time.AfterFunc(rr.span, func() {
		rr.timedOut.Store(true)
		s.cancelTurn()
		// 🚨 A turn that ends reports the run. A turn that never began, or
		// that does not return, reports nothing, and the routine would then
		// start no run again. This ends the run with no cost on record.
		time.AfterFunc(grace, func() { w.endRunOnce(id, b, talkoot.RunEnd{Failed: rr.limitReason()}) })
	})
	rr.mu.Unlock()
	runSeated()
	s.queueFrom(ctrlproto.QueueSourceTalkootDelivery, text)
	// 🚨 After the text is queued. A roster update that changed the member
	// between the seat and the turn found no turn to stop.
	if b.retired.Load() {
		s.cancelTurn()
	}
	return true, true
}

// runTurn returns the callbacks of a turn in a run's session (talkootTurn).
// The turn is the run: its last text is the report, and its end ends the run.
//
// 🔑 The report is the last text of a turn that ended well, whoever opened
// the turn. A member's session publishes only a turn that one delivery
// opened, because a person may type into it. A run's session has one purpose.
func (w *Workspace) runTurn(sessID string, b *seatBinding) (end func(costUSD float64, ran bool, failed string, interrupted, stopped bool), publish func(body string, delivered bool)) {
	rr := b.routine
	publish = func(body string, _ bool) { rr.report.Store(&body) }
	end = func(costUSD float64, ran bool, failed string, interrupted, stopped bool) {
		defer w.talkoot.turns.Add(-1)
		if !ran {
			return
		}
		e := talkoot.RunEnd{CostUSD: costUSD}
		switch {
		case rr.capped.Load():
			e.Failed = fmt.Sprintf("the run reached the routine's limit of $%.2f a day and was stopped", rr.run.SpendCapUSD)
		case rr.timedOut.Load():
			e.Failed = rr.limitReason()
		case failed != "":
			e.Failed = failureReason(failed)
		case rr.wentOn.Load():
			// 🚨 The run asked a person, got no answer, and called a tool
			// all the same. It was stopped there, so it has no last text.
			e.Unanswered = *rr.unanswered.Load()
		case interrupted || stopped:
			// A person, a roster update or the end of the daemon stopped the
			// run. That is no failure of the routine.
			e.Interrupted = true
		default:
			if p := rr.report.Load(); p != nil {
				e.Report = *p
			}
			// A run that nobody answered ends with a note, and its last
			// text is in the note (talkoot.RunEnd).
			if q := rr.unanswered.Load(); q != nil {
				e.Unanswered = *q
			}
		}
		w.endRunOnce(sessID, b, e)
	}
	return end, publish
}

// endRunOnce ends a run one time: the room gets its end, a failed run pauses
// its routine, and the run's session closes.
func (w *Workspace) endRunOnce(sessID string, b *seatBinding, e talkoot.RunEnd) {
	rr := b.routine
	if !rr.ended.CompareAndSwap(false, true) {
		return
	}
	rr.stopLimit()
	w.finishRun(b.run, rr.run, e, false)
	// On a goroutine of its own. The turn that calls this still holds its
	// session, and a close waits for it.
	go w.closeRunSession(sessID)
}

// closeRunSession takes a run's seat away and closes its session. The file
// stays, so a person can read what the run did.
//
// 🔑 The session closes before the seat goes. A session that lost its seat is
// built again as a session with no posture from the roster, and a run's
// session must not take a turn in that state.
func (w *Workspace) closeRunSession(sessID string) {
	w.mu.Lock()
	if s := w.sessions[sessID]; s != nil {
		s.close()
		delete(w.sessions, sessID)
	}
	w.mu.Unlock()
	w.unseatTalkoot(sessID)
	w.BroadcastAll(ctrlproto.SessionsChangedEvent())
}

// finishRun records the end of a run in the room, delivers what the end
// sends, and pauses the routine of a run that failed. locked says that the
// caller holds run.routineMu.
//
// 🚨 A failed run holds routineMu from before its end is in the room until
// its routine is paused. The router lets a routine start its next run as
// soon as the last one ended. A firing that came between the end and the
// pause would read an active routine and start a run that the pause then
// does not stop.
func (w *Workspace) finishRun(run *talkootRun, rr talkoot.Run, e talkoot.RunEnd, locked bool) {
	if e.Failed != "" && !locked {
		run.routineMu.Lock()
		defer run.routineMu.Unlock()
	}
	var deliver func()
	err := run.do(func(rt *talkoot.Router) (err error) {
		deliver, err = rt.EndRun(rr.ID, e)
		return err
	})
	if errors.Is(err, ErrTalkootClosed) {
		// 🚨 The team is closed, and nothing writes to its room or its
		// records now: the next daemon may hold the lock. The room shows the
		// run as at work, so the next start ends it and pauses the routine
		// (endLeftRuns).
		return
	}
	if err != nil {
		w.diagf("talkoot %s: the end of the run %s of routine %s is not all in the room: %v", run.id, rr.ID, rr.Routine, err)
	}
	if deliver != nil {
		if err := run.do(func(*talkoot.Router) error { deliver(); return nil }); err != nil && !errors.Is(err, ErrTalkootClosed) {
			w.diagf("talkoot %s: the report of the run %s is in the room and was not delivered: %v", run.id, rr.ID, err)
		}
	}
	if e.Failed != "" {
		w.pauseRoutine(run, rr.Routine, "a run failed: "+e.Failed)
		return
	}
	// 🚨 A run that passed its routine's daily limit pauses the routine
	// (decision 0029 rule 17). The router refuses a start of a routine past
	// its limit, so a firing between the end above and this pause starts
	// nothing, and the lock is taken only here.
	var spent float64
	if err := run.do(func(rt *talkoot.Router) error { spent = rt.RoutineSpend(rr.Routine); return nil }); err != nil {
		return
	}
	if !talkoot.RoutineSpendOver(spent, rr.SpendCapUSD) {
		return
	}
	if !locked {
		run.routineMu.Lock()
		defer run.routineMu.Unlock()
	}
	w.pauseRoutine(run, rr.Routine, fmt.Sprintf("its runs spent $%.2f today, and its limit is $%.2f a day", spent, rr.SpendCapUSD))
}

// cost hears what the run's turn has cost so far. It marks the run as over
// its limit when the routine's spend today, with this cost, passed the
// routine's daily limit.
//
// 🚨 This is what bounds a run's spend while it works. The router tests the
// limit when a run starts and counts the cost when it ends, and a run can
// work for half an hour in between.
//
// 🚨 The run's session holds the run (wsSession.run), so this takes no lock
// and no event is lost. An earlier version found the run through the seats,
// with a lock that it did not wait for. A usage event that met a busy lock
// was dropped, and the step after it then read no mark.
func (rr *routineRun) cost(costUSD float64, now time.Time) {
	if talkoot.RoutineSpendOver(rr.run.SpentAt(now)+costUSD, rr.run.SpendCapUSD) {
		rr.over.Store(true)
	}
}

// step hears that the run's turn takes its next step: a tool call, or one
// more request to the model. It reports true one time for a run that is over
// its limit, and the caller stops the turn.
//
// 🔑 The run is stopped at its next step, and not at the response that
// passed the limit. A run whose last response passed the limit has done its
// work and has paid for it. It ends with its report, and finishRun pauses
// the routine. A run that would go on is stopped, and it ends as failed.
func (rr *routineRun) step() (stop bool) {
	return rr.over.Load() && !rr.ended.Load() && rr.capped.CompareAndSwap(false, true)
}

// runWakeTimerFired is the handler of a follow-up that came due. The timer
// service calls its handlers one at a time, so the start of the run goes to
// the team's own goroutine, as a routine's firing does.
//
// 🚨 The service forgets a one-time timer when its handler returns. A daemon
// that stopped before the run started would lose the follow-up, and the room
// would show it as open for good. So the handler sets the timer again for a
// short time ahead, and runWakeFired removes it when the room holds the
// follow-up's run or its drop. A second firing finds the wake closed.
func (w *Workspace) runWakeTimerFired(svc *timers.Service, run *talkootRun, f timers.Firing, wk talkoot.Wake, d wakeData, now time.Time) {
	d.Tries++
	if d.Tries > wakeMaxTries {
		run.later(func() {
			w.dropRunWake(run, wk, fmt.Sprintf("its run did not start in %d tries, %s apart", wakeMaxTries, wakeRetryAfter))
		})
		return
	}
	data, err := json.Marshal(d)
	if err == nil {
		_, err = svc.Set(timers.Timer{
			Owner: wakeTimerOwner, ID: f.Timer.ID, Label: f.Timer.Label, Data: data,
			Schedule: timers.Schedule{At: now.Add(wakeRetryAfter)},
		})
	}
	if err != nil {
		w.diagf("talkoot %s: the follow-up %s of routine %s has no timer now. It is lost if the daemon stops before its run starts: %v", d.Talkoot, wk.ID, d.Routine, err)
	}
	run.later(func() { w.runWakeFired(svc, run, wk, d, now) })
}

// runWakeFired decides what a follow-up that came due does, and does it. A
// follow-up is a run of its routine, so it asks every test that a firing of
// the routine asks (routineFired), and the wakes' own switch.
//
// 🚨 Every test reads what holds now. A person who turned wakes or routines
// off, or who paused or deleted the routine, gets no run from a follow-up
// that was set before.
func (w *Workspace) runWakeFired(svc *timers.Service, run *talkootRun, wk talkoot.Wake, d wakeData, now time.Time) {
	run.routineMu.Lock()
	defer run.routineMu.Unlock()
	defer w.settleRunWake(svc, run, wk)
	drop := func(why string) { w.dropRunWake(run, wk, why) }
	switch {
	case !config.TalkootRoutines().On:
		drop("routines are off in the user configuration")
		return
	case !config.TalkootWakes().On:
		drop("wakes are off in the user configuration")
		return
	case routineStanding(run, d.Routine).deleted:
		drop("a person deleted the routine")
		return
	}
	r, err := talkoot.LoadRoutine(run.dir, d.Routine)
	if err != nil {
		drop("the routine's record does not read")
		return
	}
	if r.Status != talkoot.RoutineActive {
		drop("the routine is " + r.Status)
		return
	}
	r.Runs = max(r.Runs, routineStanding(run, r.ID).runs)
	if why := routineEnd(r, now); why != "" {
		w.endRoutine(run, r, why, now)
		drop("the routine reached its end: " + why)
		return
	}
	bar, err := routineBar(run, r, now)
	if err != nil {
		// The timer stays, and the next firing asks again.
		w.diagf("talkoot %s: the follow-up %s of routine %s came due, and the room does not read: %v", run.id, wk.ID, r.ID, err)
		return
	}
	if bar != nil {
		drop("the routine may not run: " + strings.TrimPrefix(bar.Error(), "talkoot: "))
		return
	}
	start := talkoot.RunStart{Routine: r, Wake: &wk}
	if d.Late >= time.Minute {
		start.Late = d.Late
	}
	w.startRoutineRun(run, r, start, now)
}

// settleRunWake removes the timer of a follow-up that the room has closed:
// its run began, or its drop is recorded. A follow-up that is still open
// keeps the timer that runWakeTimerFired set, so it is tried again.
func (w *Workspace) settleRunWake(svc *timers.Service, run *talkootRun, wk talkoot.Wake) {
	open := true
	if err := run.do(func(rt *talkoot.Router) error {
		open = len(rt.OpenWakes([]talkoot.Wake{wk})) > 0
		return nil
	}); err != nil || open {
		return
	}
	if _, err := svc.Cancel(wakeTimerOwner, wk.ID); err != nil {
		w.diagf("talkoot %s: the timer of the follow-up %s was not removed. It starts nothing, because the room shows the follow-up as closed: %v", run.id, wk.ID, err)
	}
}

// pauseRoutine pauses a routine with no person present, until a person
// resumes it. A run that failed pauses its routine (decision 0029 rule 13),
// and so does a run that passed the routine's daily limit (rule 17). The
// caller holds run.routineMu.
//
// It follows the order of a person's pause: the file, the timer, and then
// the room's line. The file is what the next firing reads.
func (w *Workspace) pauseRoutine(run *talkootRun, rid, why string) {
	r, err := talkoot.LoadRoutine(run.dir, rid)
	if err != nil {
		w.diagf("talkoot %s: routine %s must pause (%s), and its record does not read: %v", run.id, rid, why, err)
		return
	}
	if r.Status != talkoot.RoutineActive {
		// A person paused it while the run worked, or this run was its last.
		return
	}
	now := time.Now()
	if len(why) > maxReasonBytesRoutine {
		why = strings.ToValidUTF8(why[:maxReasonBytesRoutine], "")
	}
	r.Status, r.ChangedBy, r.ChangedAt, r.Reason = talkoot.RoutinePaused, "", now, why
	if err := talkoot.SaveRoutine(run.dir, r); err != nil {
		w.diagf("talkoot %s: routine %s must pause (%s), and its record could not be written: %v", run.id, rid, why, err)
		return
	}
	w.cancelRoutineTimer(run, rid)
	if err := run.room.Append(talkoot.RoutineLine(r, talkoot.ActionPaused, "", why, now)); err != nil {
		w.diagf("talkoot %s: routine %s is paused (%s), and the room could not record it: %v", run.id, rid, why, err)
	}
	run.flush()
}

// endLeftRuns ends each run that the last daemon left at work. A daemon that
// stops in order cancels a run's turn, and the turn reports before the team
// closes. So a run that the room still shows as at work never reported: the
// daemon died, or the turn did not return.
//
// 🚨 Such a run counts as failed, and its routine pauses. Its cost is not on
// record. A run that takes the daemon down would otherwise start again at
// each of its times, with none of its spend counted.
func (w *Workspace) endLeftRuns(run *talkootRun) {
	var left []talkoot.Run
	_ = run.do(func(rt *talkoot.Router) error {
		left = rt.OpenRuns()
		return nil
	})
	for _, rr := range left {
		w.finishRun(run, rr, talkoot.RunEnd{Failed: "the daemon stopped before the run ended, so the run's cost is not on record"}, false)
	}
}

// endUnopenedRunsLocked ends each run of this team whose session never
// opened a turn, as the daemon stops. The caller holds run.mu for writing,
// with w.talkoot.closing set, so no turn opens after the read of opened.
//
// 🚨 Such a run spent nothing, so it ends as interrupted and its routine
// stays active. Left in the room it would read as a run that the daemon died
// under, and the next start would pause its routine after an orderly stop.
// A run whose turn did open is left alone: its turn reports, or the next
// start ends it with its cost not on record.
//
// 🔑 The seats are read here, under run.mu, and not from the copy that
// closeTalkoots took before it. A run that got its root after that copy
// holds its seat by now, because a run's seat is set inside the router call
// that this lock keeps out.
func (w *Workspace) endUnopenedRunsLocked(run *talkootRun) {
	w.talkoot.mu.Lock()
	seats := maps.Clone(w.talkoot.seats)
	w.talkoot.mu.Unlock()
	for _, b := range seats {
		rr := b.routine
		if b.run != run || rr == nil {
			continue
		}
		rr.stopLimit()
		if rr.opened.Load() || !rr.ended.CompareAndSwap(false, true) {
			continue
		}
		if _, err := run.router.EndRun(rr.run.ID, talkoot.RunEnd{Interrupted: true}); err != nil {
			w.diagf("talkoot %s: could not record that the run %s of routine %s never began: %v", run.id, rr.run.ID, rr.run.Routine, err)
		}
	}
}

// runSessionOf returns the session id on the root of a routine's run, and
// false for any other line. Such a session is a member's, though no seat
// line names it.
func runSessionOf(l talkoot.Line) (string, bool) {
	if e := l.Envelope; l.Type == talkoot.LineEnvelope && e != nil && e.Kind == talkoot.KindRun && l.Ref != "" {
		return l.Ref, true
	}
	return "", false
}

// stopChangedRunsLocked retires the seat of each run whose member the roster
// update from prev to next removed or changed, and returns their sessions. A
// run takes its posture, its tools and its directory when it starts, as a
// worker does, so a change to any of them stops it. The caller holds
// w.talkoot.mu, and calls cancelRuns after it lets go.
func (w *Workspace) stopChangedRunsLocked(run *talkootRun, prev, next talkoot.Roster) []string {
	var out []string
	for sid, b := range w.talkoot.seats {
		if b.run != run || b.routine == nil {
			continue
		}
		old, _ := memberOf(prev, b.member)
		if m, kept := memberOf(next, b.member); !kept || !sameWorker(old, m) {
			b.retired.Store(true)
			out = append(out, sid)
		}
	}
	return out
}

// cancelRuns cancels the turn of each run's session in sids. The turn's end
// then ends the run as interrupted.
func (w *Workspace) cancelRuns(sids []string) {
	for _, sid := range sids {
		if s := w.existing(sid); s != nil {
			s.cancelTurn()
		}
	}
}
