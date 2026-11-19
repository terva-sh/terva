package talkoot

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// A wake is one follow-up that a member sets for itself, inside work that a
// person started (decision 0029 rules 4 and 9). The member gives a delay and
// a short reason. When the delay is over, the router sends the member one
// envelope of kind wake in the same chain, and its body is that reason.
//
// 🔑 The router holds no clock for a wake. The host keeps the due time in its
// timer service, which survives a restart, and calls FireWake when the time
// comes. The router's part is the gate, the room's record, and the delivery.
//
// The firing is an envelope, so that everything the router decides for an
// envelope holds for a wake too: a pause holds it, a working member takes it
// at a safe boundary, and the room records the delivery and the read.
//
// 🚨 A wake takes one hop of its chain. A turn that a wake started can set
// the next wake, so with no hop one post from a person would give a member
// turns for as long as its budget lasts, and that is a routine that nobody
// approved. The chain's hop limit ends it: the chain pauses, the room says
// so, and a person resumes it or does not.

// The actions a wake line records.
const (
	// WakeSet records a wake that a member set.
	WakeSet = "set"
	// WakeCancelled records a wake that a person cancelled before it fired.
	WakeCancelled = "cancelled"
	// WakeDropped records a wake that came due and was not delivered.
	WakeDropped = "dropped"
)

// MaxWakeReason bounds the reason in bytes. The reason is the whole text
// that the wake brings back, so it is a pointer and not a payload.
const MaxWakeReason = 240

// ErrWakeReason refuses a wake with no reason.
var ErrWakeReason = errors.New("talkoot: a wake needs a short reason; say what to check when it fires")

// ErrWakeGone refuses a cancel of a wake that already fired, was dropped, or
// was cancelled before.
var ErrWakeGone = errors.New("talkoot: that wake is no longer open")

// Wake is one follow-up that a member set for itself.
type Wake struct {
	// ID is the wake's id. The envelope that the firing sends has the same
	// id, so the set line and the envelope join on it.
	ID string
	// Member set the wake, and receives it.
	Member string
	// Chain is the root of the chain that the member worked in.
	Chain  string
	Reason string
	Set    time.Time
	Due    time.Time
	// Routine and Follow are set for the follow-up of a routine's run, and
	// zero for a member's wake. Such a wake starts one more run of the
	// routine, and Follow is that run's number (run.go).
	Routine string
	Follow  int
}

// wakeReason makes a wake's reason one line and checks its size.
func wakeReason(reason string, delay time.Duration) (string, error) {
	reason = strings.Join(strings.Fields(reason), " ")
	if reason == "" {
		return "", ErrWakeReason
	}
	if len(reason) > MaxWakeReason {
		return "", fmt.Errorf("talkoot: the wake's reason is %d bytes, above the %d limit; keep a pointer, and fetch the detail when the wake fires", len(reason), MaxWakeReason)
	}
	if delay <= 0 {
		return "", errors.New("talkoot: a wake needs a delay above zero")
	}
	return reason, nil
}

// NewRunWake checks that the run with this root may set its follow-up, and
// returns the wake. Like NewWake it records nothing: the host sets the timer
// next and then calls RecordWake.
//
// 🚨 This is the limit of decision 0029 rule 11. A run at the routine's time
// is follow-up 0, and the run that its wake starts is follow-up 1. A run
// whose number is the card's allowance sets no wake, and no run sets two.
// Without the limit a run could set itself again for good, and that is a
// timer that nobody approved.
func (rt *Router) NewRunWake(root, reason string, delay time.Duration) (Wake, error) {
	reason, err := wakeReason(reason, delay)
	if err != nil {
		return Wake{}, err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	st := rt.runs[root]
	if st == nil {
		return Wake{}, fmt.Errorf("%w: %s", ErrNoRun, root)
	}
	run := st.run
	switch {
	case run.FollowUps <= 0:
		return Wake{}, fmt.Errorf("%w: the routine's card allows none", ErrNoFollowUp)
	case run.Follow >= run.FollowUps:
		return Wake{}, fmt.Errorf("%w: the routine's card allows %d, and this run is follow-up %d", ErrNoFollowUp, run.FollowUps, run.Follow)
	case st.wake != "":
		return Wake{}, fmt.Errorf("%w: this run set its one follow-up, wake %s", ErrNoFollowUp, st.wake)
	}
	now := rt.now()
	return Wake{
		ID: newID(now), Member: run.Member, Chain: root, Reason: reason, Set: now, Due: now.Add(delay),
		Routine: run.Routine, Follow: run.Follow + 1,
	}, nil
}

// NewWake checks that member may set a wake now, and returns the wake with
// its id and its due time. It records nothing for a wake that it allows: the
// host sets the timer next and then calls RecordWake, so that the room never
// shows a wake that no timer will fire.
//
// 🚨 The test is the one a send uses. The member's active chain must have a
// person's post at its root. A chain that a routine started gets its wakes
// from the routine's card, and not from here.
func (rt *Router) NewWake(member, reason string, delay time.Duration) (Wake, error) {
	reason, err := wakeReason(reason, delay)
	if err != nil {
		return Wake{}, err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	m, ok := rt.roster.member(member)
	if !ok {
		return Wake{}, fmt.Errorf("talkoot: %q is not a member of %s", member, rt.roster.ID)
	}
	now := rt.now()
	root := rt.active[m.ID]
	if cs := rt.chains[root]; cs == nil || cs.human == "" {
		// One refusal line per member and guard per window, as for a send.
		key := m.ID + "\x00" + GuardHumanRoot
		if now.Sub(rt.refused[key]) >= rt.limits.SendWindow {
			if err := rt.room.Append(Line{Type: LineGuard, At: now, Guard: GuardHumanRoot, Action: ActionRefused,
				Member: m.ID, Reason: "a wake with no human post at the root of the chain"}); err != nil {
				return Wake{}, err
			}
			rt.refused[key] = now
		}
		return Wake{}, ErrNoHumanRoot
	}
	if why := rt.pausedWhy(m.ID, root); why != "" {
		return Wake{}, fmt.Errorf("%w (%s)", ErrPaused, why)
	}
	// The wake's firing takes a hop. A chain with none left pauses here, as
	// it does for a send, so the member hears it now and the person sees it.
	if cs := rt.chains[root]; cs.hops+1 > cs.allowance {
		reason, err := rt.pauseAtHopsLocked(root, now)
		if err != nil {
			return Wake{}, err
		}
		return Wake{}, fmt.Errorf("%w (this chain: %s)", ErrPaused, reason)
	}
	return Wake{ID: newID(now), Member: m.ID, Chain: root, Reason: reason, Set: now, Due: now.Add(delay)}, nil
}

func (w Wake) line(action string, at time.Time) Line {
	return Line{Type: LineWake, At: at, Action: action, Member: w.Member, Chain: w.Chain,
		Ref: w.ID, Text: w.Reason, Due: w.Due}
}

// RecordWake writes the line for a wake that the host's timer now holds.
//
// 🚨 A run's follow-up is tested again here, under the lock that writes the
// line. The run may have ended since NewRunWake, or set another follow-up.
func (rt *Router) RecordWake(w Wake) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var st *runState
	if w.Routine != "" {
		st = rt.runs[w.Chain]
		switch {
		case st == nil:
			return fmt.Errorf("%w: %s", ErrNoRun, w.Chain)
		case st.wake != "":
			return fmt.Errorf("%w: this run set its one follow-up, wake %s", ErrNoFollowUp, st.wake)
		}
	}
	if err := rt.room.Append(w.line(WakeSet, rt.now())); err != nil {
		return err
	}
	if st != nil {
		rt.runWakeSetLocked(st, w.ID)
	}
	return nil
}

// runWakeSetLocked records that the run st set the follow-up id. The live
// router and replay both call it.
func (rt *Router) runWakeSetLocked(st *runState, id string) {
	st.wake = id
	rt.runWakes[id] = runWake{routine: st.run.Routine, follow: st.run.Follow + 1}
}

// CancelWake records that the person by cancelled a wake. The host calls it
// before it cancels the timer. A firing that the timer starts after this
// finds the wake closed, and sends nothing.
func (rt *Router) CancelWake(w Wake, by string) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.wakesClosed[w.ID] {
		return ErrWakeGone
	}
	l := w.line(WakeCancelled, rt.now())
	l.By = by
	if err := rt.room.Append(l); err != nil {
		return err
	}
	rt.wakesClosed[w.ID] = true
	delete(rt.runWakes, w.ID)
	return nil
}

// OpenWakes returns the wakes of ws that the room has not closed, in the
// order of ws. The host lists wakes from its timers, and a timer can stay
// after the room closed its wake: a cancel writes the room first, and the
// timer's removal can fail.
func (rt *Router) OpenWakes(ws []Wake) []Wake {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := make([]Wake, 0, len(ws))
	for _, w := range ws {
		if !rt.wakesClosed[w.ID] {
			out = append(out, w)
		}
	}
	return out
}

// DropWake records that a wake came due and was not delivered, with the
// reason. A wake that is already closed writes nothing.
func (rt *Router) DropWake(w Wake, why string) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.wakesClosed[w.ID] {
		return nil
	}
	return rt.dropWakeLocked(w, why)
}

func (rt *Router) dropWakeLocked(w Wake, why string) error {
	l := w.line(WakeDropped, rt.now())
	l.Reason = why
	if err := rt.room.Append(l); err != nil {
		return err
	}
	rt.wakesClosed[w.ID] = true
	delete(rt.runWakes, w.ID)
	return nil
}

// FireWake records that a wake came due, and returns the delivery for the
// caller to run. late is how long after its due time the host calls, which is
// above zero when the daemon was not at work.
//
// 🔑 The two steps are apart for the host's timer. Its handler must return
// quickly, and the timer forgets a one-time wake when the handler returns.
// The record is one line in the room, so the handler writes it and hands
// deliver to another goroutine. A daemon that stops between the two owes the
// envelope, as it owes any envelope that the room holds with no delivery.
//
// It sends at most one envelope for a wake. The timer delivers a firing at
// least once, and the second call finds the envelope in the room.
//
// A wake whose member left the roster is dropped, and the room says so.
// deliver is never nil.
func (rt *Router) FireWake(w Wake, late time.Duration) (deliver func(), err error) {
	nothing := func() {}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.wakesClosed[w.ID] {
		return nothing, nil
	}
	if _, ok := rt.roster.member(w.Member); !ok {
		return nothing, rt.dropWakeLocked(w, "the member left the roster")
	}
	cs := rt.chains[w.Chain]
	if cs == nil || cs.human == "" {
		return nothing, rt.dropWakeLocked(w, "the chain has no human post at its root")
	}
	now := rt.now()
	// 🚨 The reason comes back from the host's timer file, which no seal
	// covers. It is made one line again, so that an edited file cannot put a
	// second line into the body, where the late notice goes.
	body := strings.Join(strings.Fields(w.Reason), " ")
	// A late wake says so. The member planned for a time, and what it meant
	// to check may have changed while the daemon was away.
	if late >= time.Minute {
		body += "\n" + lateNotice + lateText(late) + " late, because the daemon was not at work at the time you set."
	}
	// One hop, and no count against the send rate. A chain past its hop
	// limit pauses, and the envelope then waits for a person's resume.
	//
	// 🚨 The pause must be in the room before the envelope is. A pause that
	// only this process holds is gone after a restart, and the envelope
	// would then reach its member past the limit. So a pause whose line an
	// earlier call did not write is written here, and a failure keeps the
	// envelope out.
	hops := cs.hops + 1
	if hops > cs.allowance && (!rt.pauses.has(chainScope(w.Chain), pauseGuard) || rt.hopPauseOwed[w.Chain]) {
		if _, err := rt.pauseAtHopsLocked(w.Chain, now); err != nil {
			return nothing, err
		}
	}
	e := Envelope{
		ID: w.ID, Talkoot: rt.roster.ID, From: w.Member, To: []string{w.Member}, Kind: KindWake,
		Body: body, At: now, Chain: Chain{Root: w.Chain, Hops: hops},
	}
	if err := rt.room.Append(Line{Type: LineEnvelope, At: now, Envelope: &e}); err != nil {
		return nothing, err
	}
	cs.hops = hops
	rt.wakesClosed[w.ID] = true
	ds := rt.routeLocked(e, w.Member, false)
	return func() { rt.dispatch(ds) }, nil
}

// lateNotice starts the line that the router adds to a late wake's body.
// renderWake knows it, and shows that line outside the member's quote.
const lateNotice = "terva delivered this wake "

// pauseAtHopsLocked pauses a chain that has no hop left, and records the
// guard line. It returns the reason. A send and a wake both pause here.
func (rt *Router) pauseAtHopsLocked(root string, now time.Time) (string, error) {
	reason := fmt.Sprintf("the chain reached %d hops", rt.chain(root).hops)
	// The pause first: a line that cannot be written must not leave the
	// chain running. hopPauseOwed then says that the room has no line for it.
	rt.pauses.set(chainScope(root), pauseGuard, reason)
	if err := rt.room.Append(Line{Type: LineGuard, At: now, Guard: GuardHops, Action: ActionPaused, Reason: reason, Chain: root}); err != nil {
		rt.hopPauseOwed[root] = true
		return "", err
	}
	delete(rt.hopPauseOwed, root)
	return reason, nil
}

func lateText(late time.Duration) string {
	if late < 2*time.Hour {
		if m := int(late.Round(time.Minute) / time.Minute); m != 1 {
			return fmt.Sprintf("%d minutes", m)
		}
		return "1 minute"
	}
	return fmt.Sprintf("%d hours", int(late.Round(time.Hour)/time.Hour))
}
