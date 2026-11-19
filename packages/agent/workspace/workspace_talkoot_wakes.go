package workspace

// A member's wakes (decision 0029 rules 4 and 9). A wake is one follow-up
// turn that a member sets for itself, inside work that a person started.
//
// The follow-up of a routine's run is a wake too (rule 11). It has the same
// timer, the same lines in the room and the same place in the list, so a
// person finds and cancels it there. Its firing starts one more run of the
// routine, and gives the member's own session no turn
// (workspace_talkoot_runs.go).
//
// Three parts keep one wake, and each keeps what it is good at:
//
//   - the timer service keeps the due time across a restart,
//   - the room keeps the history: set, cancelled, dropped, and the firing,
//   - the router decides whether the wake may be set and where it lands.
//
// 🔑 The room is the record, and the timer only says when. Each step below is
// in the order that fails toward the room: a wake that the room shows as set
// has a timer, and a wake that the room shows as closed never reaches its
// member, whatever the timer does after.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/timers"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/i18n"
)

// wakeTimerOwner names the members' wakes in the daemon's timer service.
const wakeTimerOwner = "talkoot-wake"

// wakeData is the timer's record of one wake. The timer's id is the wake's
// id, and its one time is the wake's due time.
type wakeData struct {
	Talkoot string    `json:"talkoot"`
	Member  string    `json:"member"`
	Chain   string    `json:"chain"`
	Reason  string    `json:"reason"`
	Set     time.Time `json:"set"`
	// Due is the time the member asked for. The timer's own time moves when
	// a firing is tried again, and this one does not.
	Due time.Time `json:"due,omitzero"`
	// Tries counts the firings that the room did not take.
	Tries int `json:"tries,omitempty"`
	// Late is how long the daemon was away at the firings before this one,
	// and how long the wake waited for a team that the daemon did not run.
	Late time.Duration `json:"late,omitempty"`
	// Waits is set once the wake came due while the daemon did not run its
	// team. The list of teams counts such a wake.
	Waits bool `json:"waits,omitempty"`
	// Routine is set for the follow-up of a routine's run. The firing then
	// starts a run of that routine.
	//
	// 🚨 No seal covers this file. The router holds the routine of each open
	// follow-up from the room, and it starts no run of another routine.
	Routine string `json:"routine,omitempty"`
}

var (
	errWakesOff     = errors.New("wakes are off on this daemon. A person turns them on with talkoot_wakes in the user configuration")
	errWakeNoTimers = errors.New("this host runs no timer service, so it cannot keep a wake")
)

// attachWakeTimers registers the handler for the wakes, once the daemon has
// both its talkoots and its timers. LoadTalkoots and LoadTimers each call it,
// so a host may call the two in either order.
//
// 🔑 A wake waits for both. The timer service holds a timer whose owner has
// no handler, and says so in its list. A daemon with Talkoot off therefore
// keeps its wakes, where a person can see them, and fires none.
func (w *Workspace) attachWakeTimers(svc *timers.Service) {
	if svc == nil || !w.talkoot.loaded.Load() {
		return
	}
	svc.Handle(wakeTimerOwner, func(f timers.Firing) { w.wakeTimerFired(svc, f) })
}

// wakeSpan writes a delay as a person sets it in the configuration.
func wakeSpan(d time.Duration) string {
	s := strings.TrimSuffix(d.Round(time.Minute).String(), "0s")
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// Wake makes a native seat a tools.TalkootWaker.
func (s talkootSeat) Wake(delay time.Duration, reason string) (tools.TalkootWakeReceipt, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	if s.b.revoked {
		return tools.TalkootWakeReceipt{}, errSeatRevoked
	}
	return s.w.setWake(wakeSetter{run: s.b.run, member: s.b.member, routine: s.b.routine, gone: s.b.retired.Load, lost: errSeatRevoked}, delay, reason)
}

// wakeSetter is the seat that sets a wake: a native member's session, the
// session of a routine's run, or a worker member's bridge call.
type wakeSetter struct {
	run    *talkootRun
	member string
	// routine is set for the session of a routine's run. Its wake is the
	// run's follow-up.
	routine *routineRun
	// gone reports, inside the router call, that the seat was taken away
	// since the caller looked it up. lost is the refusal for that.
	gone func() bool
	lost error
}

// setWake sets one wake for the member of b. For the seat of a routine's
// run it sets the run's follow-up.
//
// 🚨 The policy is read here, at each call, and not only when the tool was
// attached. A person who turns wakes off stops the next wake at once, though
// a session still holds the tool.
func (w *Workspace) setWake(b wakeSetter, delay time.Duration, reason string) (tools.TalkootWakeReceipt, error) {
	pol := config.TalkootWakes()
	if !pol.On {
		return tools.TalkootWakeReceipt{}, errWakesOff
	}
	rr := b.routine
	if rr != nil && !config.TalkootRoutines().On {
		return tools.TalkootWakeReceipt{}, errRoutinesOff
	}
	svc := w.Timers()
	if svc == nil {
		return tools.TalkootWakeReceipt{}, errWakeNoTimers
	}
	if delay > pol.MaxDelay {
		return tools.TalkootWakeReceipt{}, fmt.Errorf("a wake may be at most %s from now, and you asked for %s. The person sets that limit with talkoot_wakes.max_delay. Set a shorter wake, or tell the person what to check and when",
			wakeSpan(pol.MaxDelay), wakeSpan(delay))
	}
	// One wake at a time for the whole daemon, so two calls in one step
	// cannot both read a count below the cap.
	w.talkoot.wakeMu.Lock()
	defer w.talkoot.wakeMu.Unlock()
	open := 0
	var soonest time.Time
	for _, o := range w.openWakes(b.run.id) {
		if o.Member != b.member {
			continue
		}
		// A wake that waits behind a pause has no time to fire at.
		if !o.Held && soonest.IsZero() {
			soonest = o.Due
		}
		open++
	}
	if open >= pol.PerMember && soonest.IsZero() {
		return tools.TalkootWakeReceipt{}, fmt.Errorf("you have %d open wake(s), and the limit is %d. The person sets that limit with talkoot_wakes.per_member. Each of your wakes came due and waits behind a pause. Ask the person to resume, or to cancel a wake",
			open, pol.PerMember)
	}
	if open >= pol.PerMember {
		return tools.TalkootWakeReceipt{}, fmt.Errorf("you have %d open wake(s), and the limit is %d. The person sets that limit with talkoot_wakes.per_member. Your next wake fires at %s. Do the check then, or ask the person to cancel a wake",
			open, pol.PerMember, soonest.UTC().Format("2006-01-02 15:04 UTC"))
	}
	var wk talkoot.Wake
	err := b.run.do(func(rt *talkoot.Router) (err error) {
		if b.gone() {
			return b.lost
		}
		if rr != nil {
			// 🚨 The router holds the routine's allowance (rule 11).
			wk, err = rt.NewRunWake(rr.run.ID, reason, delay)
			return err
		}
		wk, err = rt.NewWake(b.member, reason, delay)
		return err
	})
	if err != nil {
		return tools.TalkootWakeReceipt{}, err
	}
	data, err := json.Marshal(wakeData{Talkoot: b.run.id, Member: wk.Member, Chain: wk.Chain, Reason: wk.Reason, Set: wk.Set, Due: wk.Due, Routine: wk.Routine})
	if err != nil {
		return tools.TalkootWakeReceipt{}, err
	}
	label := i18n.T("A wake of %s in the team %s", wk.Member, b.run.id)
	if rr != nil {
		label = i18n.T("A follow-up of a routine of %s in the team %s", wk.Member, b.run.id)
	}
	// The timer comes before the room's line. A line with no timer would
	// show a person a wake that never fires.
	if _, err := svc.Set(timers.Timer{
		Owner: wakeTimerOwner, ID: wk.ID, Data: data,
		Label:    label,
		Schedule: timers.Schedule{At: wk.Due},
	}); err != nil {
		return tools.TalkootWakeReceipt{}, err
	}
	if err := b.run.do(func(rt *talkoot.Router) error { return rt.RecordWake(wk) }); err != nil {
		if _, cerr := svc.Cancel(wakeTimerOwner, wk.ID); cerr != nil {
			w.diagf("talkoot %s: the wake %s has a timer and no line in the room, and the timer was not cancelled: %v", b.run.id, wk.ID, cerr)
		}
		return tools.TalkootWakeReceipt{}, err
	}
	out := tools.TalkootWakeReceipt{Wake: wk, Open: open + 1, Cap: pol.PerMember}
	if rr != nil {
		out.FollowUps = rr.run.FollowUps
	}
	return out, nil
}

// openWakes lists the open wakes of the talkoot id. The wakes that came due
// and wait behind a pause are first, in the order that they fired. The wakes
// that a timer holds follow, the soonest first, which is the order of the
// timer service's own list.
//
// 🚨 A timer alone does not make a wake open. A cancel writes the room first,
// and the timer stays when its removal fails. The router knows that such a
// wake is closed, so it is left out here. Without that, a cancelled wake
// would keep its place under the member's cap until its time came.
//
// 🚨 A wake with no timer can be open too. A wake that fires while its member
// is paused writes its envelope and waits, and the timer service forgets it.
// The router holds it, so it is listed from there. Without that, the wake
// would leave the list, a person could not cancel it, and the member's cap
// would stop counting it (TKT-01M7PKC0).
func (w *Workspace) openWakes(id string) []talkoot.Wake {
	var out []talkoot.Wake
	if svc := w.Timers(); svc != nil {
		for _, t := range svc.List() {
			if t.Owner != wakeTimerOwner {
				continue
			}
			var d wakeData
			if err := json.Unmarshal(t.Data, &d); err != nil || d.Talkoot != id {
				continue
			}
			if d.Due.IsZero() {
				d.Due = t.Schedule.At
			}
			out = append(out, talkoot.Wake{ID: t.ID, Member: d.Member, Chain: d.Chain, Reason: d.Reason, Set: d.Set, Due: d.Due, Routine: d.Routine})
		}
	}
	// A team that this daemon does not run has no router to ask.
	if run, err := w.talkootRunOf(id); err == nil {
		_ = run.do(func(rt *talkoot.Router) error {
			out = append(rt.HeldWakes(), rt.OpenWakes(out)...)
			return nil
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// wakeTimerFired is the timer service's handler for a wake that came due.
//
// 🔑 The service forgets a one-time timer when this returns, and it runs
// every handler on one goroutine. So the handler writes the firing into the
// room, which is one line, and leaves the driver's call to the run's own
// goroutine. A daemon that stops between the two owes the envelope, and the
// next router over the room delivers it.
//
// 🚨 The timer is given up only for a line in the room. When the room does
// not take the firing, the handler sets the timer again for a short time
// ahead, so the wake is still in the timer's file if the daemon stops. Two
// things keep the room from taking it: a roster update, which holds the
// run's lock for its whole apply, and a write that fails.
func (w *Workspace) wakeTimerFired(svc *timers.Service, f timers.Firing) {
	var d wakeData
	if err := json.Unmarshal(f.Timer.Data, &d); err != nil || d.Talkoot == "" || d.Member == "" {
		w.diagf("talkoot: the wake timer %s holds no wake, so it was dropped", f.Timer.ID)
		return
	}
	if d.Due.IsZero() {
		d.Due = f.Due
	}
	// The service's own reading of the clock, which a test moves.
	now := f.Due.Add(f.Late)
	// 🔑 The member is told that a wake is late because the daemon was away.
	// The wait before a second try is not that, so it does not count. Each
	// firing adds only how late the timer itself fired.
	d.Late += f.Late
	run, err := w.talkootRunOf(d.Talkoot)
	if err != nil {
		w.keepWake(svc, f, d, now, err)
		return
	}
	wk := talkoot.Wake{ID: f.Timer.ID, Member: d.Member, Chain: d.Chain, Reason: d.Reason, Set: d.Set, Due: d.Due, Routine: d.Routine}
	if d.Routine != "" {
		w.runWakeTimerFired(svc, run, f, wk, d, now)
		return
	}
	var deliver func()
	recErr := errors.New("the team was not free to take it, as during a roster update")
	run.tryDo(func(rt *talkoot.Router) error {
		// 🚨 Read live. A person who turned wakes off must get no turn from a
		// wake that was set before.
		if !config.TalkootWakes().On {
			deliver, recErr = func() {}, rt.DropWake(wk, "wakes are off in the user configuration")
			return recErr
		}
		deliver, recErr = rt.FireWake(wk, d.Late)
		return recErr
	})
	if recErr != nil {
		w.retryWake(svc, run, f, wk, d, now, recErr)
		return
	}
	run.later(func() {
		if err := run.do(func(*talkoot.Router) error { deliver(); return nil }); err != nil && !errors.Is(err, ErrTalkootClosed) {
			w.diagf("talkoot %s: the wake %s of %s is in the room and was not delivered: %v", d.Talkoot, wk.ID, wk.Member, err)
		}
	})
}

// A wake that came due while the daemon does not run its team waits for the
// team: its timer is set again each wakeTeamRetry. A team that did not start
// can start only when the daemon starts, so the firing after the next start
// finds it. The wait ends after wakeTeamWait, which is long past the use of a
// check that a member set at most max_delay ahead.
const (
	wakeTeamRetry = 5 * time.Minute
	wakeTeamWait  = 7 * 24 * time.Hour
)

// keepWake holds a wake whose team this daemon does not run. No router means
// no room to write, so the timer is all that holds the wake, and the service
// keeps a timer that its own handler set again.
//
// 🚨 Without an end, a wake of a team that never runs here again would fire
// for ever, and no person can remove it: a cancel needs the team's run. So a
// wake is dropped when its team is gone, and when it waited wakeTeamWait. The
// daemon's log is all that says so, because no room is open to write.
func (w *Workspace) keepWake(svc *timers.Service, f timers.Firing, d wakeData, now time.Time, why error) {
	if errors.Is(why, ErrTalkootNotFound) {
		w.diagf("talkoot %s: the wake %s of %s came due, and the team is gone, so it was dropped", d.Talkoot, f.Timer.ID, d.Member)
		return
	}
	if now.Sub(d.Due) > wakeTeamWait {
		w.diagf("talkoot %s: the wake %s of %s was dropped. It came due on %s, and this daemon did not run the team in %d days: %v",
			d.Talkoot, f.Timer.ID, d.Member, d.Due.UTC().Format("2006-01-02"), int(wakeTeamWait/(24*time.Hour)), why)
		return
	}
	first := !d.Waits
	d.Waits = true
	// 🔑 The wait is time that the member did not plan for, so it counts as
	// late. The next firing adds only how late its own timer was.
	d.Late += wakeTeamRetry
	data, err := json.Marshal(d)
	if err == nil {
		_, err = svc.Set(timers.Timer{
			Owner: wakeTimerOwner, ID: f.Timer.ID, Label: f.Timer.Label, Data: data,
			Schedule: timers.Schedule{At: now.Add(wakeTeamRetry)},
		})
	}
	switch {
	case err != nil:
		w.diagf("talkoot %s: the wake %s of %s is lost. This daemon does not run the team (%v), and its timer was not set again: %v", d.Talkoot, f.Timer.ID, d.Member, why, err)
	case first:
		// One line for the wake, and not one for each try.
		w.diagf("talkoot %s: the wake %s of %s came due, and this daemon does not run the team, so it waits for the team: %v", d.Talkoot, f.Timer.ID, d.Member, why)
	}
}

// wakesWaiting counts, for each team, the wakes that came due and wait for
// the team to run.
func (w *Workspace) wakesWaiting() map[string]int {
	svc := w.Timers()
	if svc == nil {
		return nil
	}
	out := map[string]int{}
	for _, t := range svc.List() {
		if t.Owner != wakeTimerOwner {
			continue
		}
		var d wakeData
		if err := json.Unmarshal(t.Data, &d); err == nil && d.Waits {
			out[d.Talkoot]++
		}
	}
	return out
}

// A firing that the room did not take is tried again after wakeRetryAfter,
// at most wakeMaxTries times. Ten minutes is far past a roster update, and a
// room that cannot be written for that long has paused its team already.
const (
	wakeRetryAfter = 30 * time.Second
	wakeMaxTries   = 20
)

// retryWake sets the timer of a wake again, because its firing is not in the
// room. The service keeps a timer that its own handler set again.
//
// After the last try the timer is given up. The room then still shows the
// wake as set, so the run writes the drop when it is free again. That write
// waits on the run's own goroutine, and never in the timer's handler.
func (w *Workspace) retryWake(svc *timers.Service, run *talkootRun, f timers.Firing, wk talkoot.Wake, d wakeData, now time.Time, why error) {
	d.Tries++
	if d.Tries > wakeMaxTries {
		w.diagf("talkoot %s: the wake %s of %s was dropped after %d tries, because the room did not take it: %v", d.Talkoot, f.Timer.ID, d.Member, wakeMaxTries, why)
		run.later(func() {
			err := run.do(func(rt *talkoot.Router) error {
				return rt.DropWake(wk, fmt.Sprintf("the room did not take it in %d tries", wakeMaxTries))
			})
			if err != nil && !errors.Is(err, ErrTalkootClosed) {
				w.diagf("talkoot %s: the room still shows the wake %s of %s as set, because the drop was not recorded: %v", d.Talkoot, wk.ID, wk.Member, err)
			}
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
		w.diagf("talkoot %s: the wake %s of %s is lost. The room did not take it (%v), and its timer was not set again: %v", d.Talkoot, f.Timer.ID, d.Member, why, err)
	}
}

// talkootWakeCancel cancels one open wake for the person by.
func (w *Workspace) talkootWakeCancel(_ context.Context, id, by, wake string) error {
	if !talkoot.ValidPerson(strings.TrimPrefix(by, talkoot.HumanPrefix)) {
		return fmt.Errorf("talkoot: the person's name %q must be 1 to 64 letters, digits, and . _ @ -", by)
	}
	run, err := w.talkootRunOf(id)
	if err != nil {
		return err
	}
	var wk talkoot.Wake
	found := false
	for _, o := range w.openWakes(id) {
		if o.ID == wake {
			wk, found = o, true
		}
	}
	if !found {
		return talkoot.ErrWakeGone
	}
	svc := w.Timers()
	if wk.Held {
		// The timer service forgot this wake when it fired, so the room's
		// line is the whole cancel.
		return run.do(func(rt *talkoot.Router) error { return rt.CancelWake(wk, humanBy(by)) })
	}
	if svc == nil {
		return errWakeNoTimers
	}
	// 🔑 The room first. A cancel that the room holds stops the wake even
	// when its timer stays, because the firing finds the wake closed. The
	// other order could remove the timer and leave no word of why.
	if err := run.do(func(rt *talkoot.Router) error { return rt.CancelWake(wk, humanBy(by)) }); err != nil {
		return err
	}
	_, err = svc.Cancel(wakeTimerOwner, wk.ID)
	return err
}

// talkootWakesView is the state of wakes for one talkoot, or nil when the
// person set no policy and the team has no open wake. Decision 0029 rule 23
// asks that a feature that changes authority shows its state while it is on.
func (w *Workspace) talkootWakesView(id string) *ctrlproto.TalkootWakes {
	pol := config.TalkootWakes()
	open := w.openWakes(id)
	if !pol.On && pol.Problem == "" && len(open) == 0 {
		return nil
	}
	out := &ctrlproto.TalkootWakes{On: pol.On, Problem: pol.Problem, Open: make([]ctrlproto.TalkootWake, 0, len(open))}
	if pol.On {
		out.PerMember, out.MaxDelay = pol.PerMember, wakeSpan(pol.MaxDelay)
		if w.Timers() == nil {
			out.On, out.Problem = false, errWakeNoTimers.Error()
		}
	}
	for _, o := range open {
		out.Open = append(out.Open, ctrlproto.TalkootWake{ID: o.ID, Member: o.Member, Chain: o.Chain, Reason: o.Reason, Set: o.Set, Due: o.Due, Routine: o.Routine, Held: o.Held})
	}
	return out
}
