package workspace

// A team's routines (decision 0029 rules 4 and 5). A routine is a recurring
// timer that a person approved. A member asks for one with talkoot_routine,
// the ask waits in the inbox as a proposal, and only a person's approval
// stores it and sets its timer.
//
// Three parts keep one routine:
//
//   - the routine's file under the team's directory says what was approved,
//     and whether it is active or paused,
//   - the timer service keeps the schedule across a restart,
//   - the room records each step: approved, paused, resumed, deleted.
//
// 🔑 This file starts no run. workspace_talkoot_runs.go holds the handler for
// the timer, and it reads a routine's record before each run. So a pause or
// a delete here stops the next run, whatever the timer does after.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/timers"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/i18n"
)

// routineTimerOwner names the teams' routines in the daemon's timer service.
const routineTimerOwner = "talkoot-routine"

// routineData is the timer's record of one routine. The timer's id is the
// routine's id, and the routine's file holds the rest.
type routineData struct {
	Talkoot string `json:"talkoot"`
	Member  string `json:"member"`
	Kind    string `json:"kind"`
}

var (
	errRoutinesOff     = errors.New("routines are off on this daemon. A person turns them on with talkoot_routines in the user configuration")
	errRoutineNoTimers = errors.New("this host runs no timer service, so it cannot keep a routine")
)

// routineDefaults gives the defaults of one kind as the policy holds them.
func routineDefaults(pol config.TalkootRoutinePolicy, kind string) talkoot.RoutineFields {
	d := pol.Kinds[kind]
	return talkoot.RoutineFields{
		EndAfterRuns: d.EndAfterRuns, EndAfterDays: d.EndAfterDays, SpendUSDPerDay: d.SpendUSDPerDay,
		ReviewsPerDay: d.ReviewsPerDay, FollowUps: d.FollowUps, CatchUp: d.CatchUp,
	}
}

// routineSchedule reads a routine's schedule, and returns it with the text
// form that the record keeps.
func routineSchedule(text string, now time.Time) (timers.Schedule, string, error) {
	s, err := timers.ParseSchedule(text, now)
	if err != nil {
		return timers.Schedule{}, "", err
	}
	if !s.Recurs() {
		return timers.Schedule{}, "", fmt.Errorf("talkoot: a routine recurs, and %q is one time. Write it as \"every 15m\" or \"daily 07:30\", or set a wake for one later turn", text)
	}
	return s, s.String(), nil
}

// ProposeRoutine makes a native seat a tools.TalkootRoutiner.
func (s talkootSeat) ProposeRoutine(ask talkoot.RoutineAsk, why string) (tools.TalkootRoutineReceipt, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	if s.b.revoked {
		return tools.TalkootRoutineReceipt{}, errSeatRevoked
	}
	if s.b.routine != nil {
		return tools.TalkootRoutineReceipt{}, errRunSeat
	}
	return s.w.talkootProposeRoutine(s.b, ask, why)
}

// talkootProposeRoutine files a member's ask for a routine as a proposal.
// Nothing is stored and no timer is set: a person decides from the card.
//
// 🚨 The policy is read here, at each call, and not only when the tool was
// attached. A person who turns routines off stops the next ask at once.
func (w *Workspace) talkootProposeRoutine(b *seatBinding, ask talkoot.RoutineAsk, why string) (tools.TalkootRoutineReceipt, error) {
	none := tools.TalkootRoutineReceipt{}
	pol := config.TalkootRoutines()
	if !pol.On {
		return none, errRoutinesOff
	}
	if w.Timers() == nil {
		return none, errRoutineNoTimers
	}
	if len(why) > maxWhyBytes {
		return none, fmt.Errorf("talkoot: the reason is %d bytes, above the %d limit", len(why), maxWhyBytes)
	}
	if err := ask.Check(); err != nil {
		return none, err
	}
	_, text, err := routineSchedule(ask.Schedule, time.Now())
	if err != nil {
		return none, err
	}
	ask.Schedule = text
	run := b.run
	// As talkootPropose does: run.propose is safe to wait on from a seat
	// call, and run.update is not.
	run.propose.Lock()
	defer run.propose.Unlock()
	held, err := routinesHeld(run.dir)
	if err != nil {
		return none, err
	}
	if held >= pol.Max {
		return none, fmt.Errorf("the team has %d routine(s), and the limit is %d. The person sets that limit with talkoot_routines.max. Ask the person to delete a routine, or do the work in a turn that the person starts",
			held, pol.Max)
	}
	pending, err := w.pendingProposals(run.id, run.dir)
	if err != nil {
		return none, err
	}
	if len(pending) >= talkoot.MaxPendingProposals {
		return none, fmt.Errorf("talkoot: %d proposals already wait for a person, the limit", len(pending))
	}
	// One ask for a member at a time. A member in a loop would otherwise
	// fill the inbox with the same card.
	for _, p := range pending {
		if p.Routine != nil && p.Proposer == b.member {
			return none, fmt.Errorf("your request for a routine, proposal %s, still waits for the person. Wait for the decision before you ask for another", p.ID)
		}
	}
	summary := ask.Summary()
	var env talkoot.Envelope
	err = run.do(func(rt *talkoot.Router) (err error) {
		if b.retired.Load() {
			return errSeatRevoked
		}
		env, err = rt.Propose(b.member, summary)
		return err
	})
	if err != nil {
		return none, err
	}
	now := time.Now()
	p := talkoot.Proposal{
		ID: talkoot.NewProposalID(now), Proposer: b.member, At: now, Ops: []talkoot.Op{}, Summary: summary, Why: why,
		Envelope: env.ID, Changes: []talkoot.MemberChange{}, Status: talkoot.ProposalPending, Routine: &ask,
	}
	if err := talkoot.SaveProposal(run.dir, p); err != nil {
		// ⚠️ The room holds the envelope, and no card waits for it. The
		// member is told, and asks again.
		return none, err
	}
	w.talkootEmit(talkootEvent{Talkoot: run.id, Kind: "inbox", Wire: new(ctrlproto.TalkootInboxEvent(run.id, proposalCard(run.id, p)))})
	_, diffs := ask.Resolve(routineDefaults(pol, ask.Kind))
	return tools.TalkootRoutineReceipt{Proposal: p, Schedule: text, Diffs: diffs}, nil
}

// routinesHeld counts the routines that hold a place under the team's cap.
//
// 🚨 A record that does not read counts. Its file and maybe its timer are
// still there, so a damaged record that left the count would let a team hold
// more routines than the person allowed. A delete clears it.
func routinesHeld(dir string) (int, error) {
	list, damaged, err := talkoot.ListRoutines(dir)
	return len(list) + len(damaged), err
}

// lastRoutineStep returns the room's last line for the routine rid, and
// false when the room holds none.
//
// ⚠️ It reads the whole room. Only the second try of a pause or a delete
// calls it, to learn whether the first try wrote its line.
func lastRoutineStep(run *talkootRun, rid string) (talkoot.Line, bool, error) {
	lines, err := run.room.Read()
	if err != nil {
		return talkoot.Line{}, false, err
	}
	for _, l := range slices.Backward(lines) {
		if l.Type == talkoot.LineRoutine && l.Ref == rid {
			return l, true, nil
		}
	}
	return talkoot.Line{}, false, nil
}

// routineMark is what the room holds about one routine. The run keeps one
// for each routine: it reads the room's lines when the team starts, and each
// line that the room seals after that (talkootRun.observe). So a firing asks
// the room about its routine and reads no file.
type routineMark struct {
	// approved is the routine as the line of its approval holds it, and nil
	// when the room holds no approval.
	approved *talkoot.Routine
	// deleted and ended say that the room holds such a line. Both are final.
	deleted, ended bool
	// state is the last of approved, resumed and paused that the room holds.
	state string
	// runs counts the roots of the routine's runs.
	runs int
	// skip is the reason of the room's newest line about the routine, when
	// that line is a skipped time. Any other line, and a run, empties it.
	skip string
}

// noteRoutine keeps the run's marks current with one line of the room.
func (r *talkootRun) noteRoutine(l talkoot.Line) {
	rid, isRun := "", false
	switch e := l.Envelope; {
	case l.Type == talkoot.LineRoutine:
		rid = l.Ref
	case l.Type == talkoot.LineEnvelope && e != nil && e.Kind == talkoot.KindRun && talkoot.IsRoutineActor(e.From):
		rid, isRun = strings.TrimPrefix(e.From, talkoot.RoutinePrefix), true
	}
	if rid == "" {
		return
	}
	r.markMu.Lock()
	defer r.markMu.Unlock()
	if r.marks == nil {
		r.marks = map[string]*routineMark{}
	}
	m := r.marks[rid]
	if m == nil {
		m = &routineMark{}
		r.marks[rid] = m
	}
	m.skip = ""
	if isRun {
		m.runs++
		return
	}
	switch l.Action {
	case talkoot.RoutineApproved:
		if l.Routine != nil {
			held := *l.Routine
			m.approved = &held
		}
		m.state = l.Action
	case talkoot.RoutineResumed, talkoot.ActionPaused:
		m.state = l.Action
	case talkoot.RoutineEnded:
		m.ended = true
	case talkoot.RoutineDeleted:
		m.deleted = true
	case talkoot.RoutineSkipped:
		m.skip = l.Reason
	}
}

// routineStanding returns what the room holds about the routine rid.
func routineStanding(run *talkootRun, rid string) routineMark {
	run.markMu.Lock()
	defer run.markMu.Unlock()
	if m := run.marks[rid]; m != nil {
		return *m
	}
	return routineMark{}
}

// sameApproval reports whether the record rec is the routine that the room's
// approval line holds. The state of a routine changes after its approval:
// its status, its last change and its count of runs. Every other field is
// what the person read on the card, and it must not differ.
func sameApproval(approved, rec talkoot.Routine) bool {
	if !approved.EndsAt.Equal(rec.EndsAt) || !approved.ApprovedAt.Equal(rec.ApprovedAt) {
		return false
	}
	for _, r := range []*talkoot.Routine{&approved, &rec} {
		r.EndsAt, r.ApprovedAt, r.ChangedAt = time.Time{}, time.Time{}, time.Time{}
		r.Status, r.ChangedBy, r.Reason, r.Runs = "", "", "", 0
	}
	return approved == rec
}

// routineBar says why the routine r may hold no timer now, or nil when it
// may. A resume, the start of the daemon and the list all ask it, so the
// three never disagree about one routine.
//
// 🚨 The room decides, and not the routine's file. The room's lines have a
// seal and the file has none, so a hand, a process or a member's own shell
// can write the file. Four tests rest on the room:
//
//   - an approval is on record. An approval that failed part way can leave
//     a file with no line,
//   - no deletion is on record after it. A deletion is final,
//   - the file is the routine that the person approved. A run reads its
//     prompt from the file, and tells the model that a person approved it,
//   - the file does not say active while the room says paused.
//
// The count of runs is the larger of the file's and the room's, so a file
// that counts fewer runs gets no more of them.
//
// The second error is a failure to read, and not a reason.
func routineBar(run *talkootRun, r talkoot.Routine, now time.Time) (bar, err error) {
	mark := routineStanding(run, r.ID)
	if !r.EndsAt.IsZero() && !now.Before(r.EndsAt) {
		return fmt.Errorf("talkoot: routine %s ended on %s. Delete it, and let the member ask for a new one", r.ID, r.EndsAt.UTC().Format("2006-01-02")), nil
	}
	// 🚨 The count is an end too. Without this test a pause and a resume
	// would give a routine that used all of its runs a timer again.
	if r.Status == talkoot.RoutineEnded || mark.ended || r.Fields.EndAfterRuns > 0 && max(r.Runs, mark.runs) >= r.Fields.EndAfterRuns {
		return fmt.Errorf("talkoot: routine %s reached the end that its approval gave it. Delete it, and let the member ask for a new one", r.ID), nil
	}
	if ok, err := rosterHolds(run, r.Member); err != nil {
		return nil, err
	} else if !ok {
		return fmt.Errorf("talkoot: %s left the roster, and a routine belongs to a member. Delete the routine", r.Member), nil
	}
	switch {
	case mark.approved == nil:
		return fmt.Errorf("talkoot: the room holds no approval of routine %s. Delete it, and let the member ask again", r.ID), nil
	case mark.deleted:
		// 🚨 An id is approved one time, so a deletion is final. A file
		// that came back, from a backup or a stray write, is the same as
		// its approval, and the room still says that a person deleted it.
		return fmt.Errorf("talkoot: the room says that a person deleted routine %s. Delete its record again, and let the member ask for a new one", r.ID), nil
	case !sameApproval(*mark.approved, r):
		return fmt.Errorf("talkoot: the record of routine %s is not the routine that %s approved. Delete it, and let the member ask again",
			r.ID, strings.TrimPrefix(mark.approved.ApprovedBy, talkoot.HumanPrefix)), nil
	case r.Status == talkoot.RoutineActive && mark.state == talkoot.ActionPaused:
		// A resume that could not write its line and could not set the
		// record back leaves this state too, and its error names the pause.
		return fmt.Errorf("talkoot: the room says that routine %s is paused, and its record says active. Pause it again, and then resume it", r.ID), nil
	}
	return nil, nil
}

// routineTimer sets the timer of an active routine.
func (w *Workspace) routineTimer(svc *timers.Service, id string, r talkoot.Routine, sched timers.Schedule) error {
	data, err := json.Marshal(routineData{Talkoot: id, Member: r.Member, Kind: r.Kind})
	if err != nil {
		return err
	}
	_, err = svc.Set(timers.Timer{
		Owner: routineTimerOwner, ID: r.ID, Data: data, Schedule: sched,
		Label: i18n.T("A %s routine of %s in the team %s", r.Kind, r.Member, id),
	})
	return err
}

// rosterHolds reports whether the run's roster holds member now.
func rosterHolds(run *talkootRun, member string) (bool, error) {
	found := false
	err := run.do(func(rt *talkoot.Router) error {
		found = slices.ContainsFunc(rt.Members(), func(m talkoot.Member) bool { return m.ID == member })
		return nil
	})
	return found, err
}

// approveRoutine stores the routine that the proposal p asks for, and sets
// its timer. The caller holds run.update, and p is as the store holds it.
//
// waiting is the record before the decision, which a failed step puts back.
// refused keeps the card with the reason on it.
//
// 🔑 The order is the record, the routine's file, the timer, and then the
// room's line. Each failure undoes the steps before it. The timer comes
// before the line, as for a wake: the room must not show a routine that no
// timer holds.
func (w *Workspace) approveRoutine(run *talkootRun, by, digest string, p, waiting talkoot.Proposal, refused func(error) (talkoot.Proposal, error)) (talkoot.Proposal, error) {
	run.routineMu.Lock()
	defer run.routineMu.Unlock()
	// 🚨 Read live. A card can wait for days, and a person who turned
	// routines off since must not store one by approving an old card.
	pol := config.TalkootRoutines()
	if !pol.On {
		return refused(errRoutinesOff)
	}
	svc := w.Timers()
	if svc == nil {
		return refused(errRoutineNoTimers)
	}
	// 🚨 The record is a file, so it can change after the ask was checked.
	// Every check of the ask runs again on what the file holds now.
	if !isMemberProposer(p.Proposer) {
		return refused(fmt.Errorf("talkoot: a member asks for a routine, and this proposal came from %s", p.Proposer))
	}
	ask := *p.Routine
	if err := ask.Check(); err != nil {
		return refused(err)
	}
	now := time.Now()
	sched, text, err := routineSchedule(ask.Schedule, now)
	if err != nil {
		return refused(err)
	}
	if ok, err := rosterHolds(run, p.Proposer); err != nil {
		return talkoot.Proposal{}, err
	} else if !ok {
		return refused(fmt.Errorf("talkoot: %s left the roster, and a routine belongs to a member. Decline this card", p.Proposer))
	}
	held, err := routinesHeld(run.dir)
	if err != nil {
		return talkoot.Proposal{}, err
	}
	if held >= pol.Max {
		return refused(fmt.Errorf("talkoot: the team has %d routine(s), and the limit is %d (talkoot_routines.max). Delete a routine or raise the limit, and approve again", held, pol.Max))
	}
	r := talkoot.NewRoutine(p.ID, p.Proposer, ask, text, routineDefaults(pol, ask.Kind), humanBy(by), now)
	// 🚨 The ask passed its check, and the routine also holds the defaults
	// that the ask left out. The whole routine passes the check that every
	// later read makes, or it is not stored.
	if err := r.Check(); err != nil {
		return refused(fmt.Errorf("talkoot: the routine that this approval would store does not pass its own check, so nothing is stored: %w", err))
	}
	// 🚨 The approval is for the card that the person read. The proposal is
	// a file, and the defaults come from a file too. Either can change
	// between the read of the card and the decision. The card carried a
	// digest of what it showed, and the decision gives it back.
	if want := ask.Digest(p.ID, p.Proposer, r.Fields); digest == "" || digest != want {
		return refused(errors.New("talkoot: this request, or a default it takes, is not what the card showed when it was approved. Nothing is stored. Read the card again, and approve it again"))
	}

	p.Status, p.Problem = talkoot.ProposalApproved, ""
	if err := talkoot.SaveProposal(run.dir, p); err != nil {
		return talkoot.Proposal{}, fmt.Errorf("talkoot: the approval of proposal %s could not be recorded, so no routine is stored: %w", p.ID, err)
	}
	undo := func(steps ...func() error) {
		for _, step := range steps {
			if err := step(); err != nil {
				// ⚠️ What is left is named, so a person can clear it. The
				// start of the daemon sets a missing timer, and a routine
				// with no line in the room still shows in the app.
				w.diagf("talkoot %s: the approval of routine %s failed, and a step of it could not be undone: %v", run.id, p.ID, err)
			}
		}
	}
	unapprove := func() error { return talkoot.SaveProposal(run.dir, waiting) }
	unfile := func() error { return talkoot.RemoveRoutine(run.dir, r.ID) }
	untime := func() error { _, err := svc.Cancel(routineTimerOwner, r.ID); return err }
	if err := talkoot.SaveRoutine(run.dir, r); err != nil {
		undo(unapprove)
		return talkoot.Proposal{}, err
	}
	if err := w.routineTimer(svc, run.id, r, sched); err != nil {
		undo(unfile, unapprove)
		return talkoot.Proposal{}, fmt.Errorf("talkoot: the routine's timer was not set, so no routine is stored: %w", err)
	}
	if err := run.room.Append(talkoot.RoutineLine(r, talkoot.RoutineApproved, humanBy(by), "", now)); err != nil {
		undo(untime, unfile, unapprove)
		return talkoot.Proposal{}, fmt.Errorf("talkoot: the room could not record the approval, so no routine is stored: %w", err)
	}
	run.flush()
	return p, nil
}

// talkootRoutineSet pauses, resumes or deletes one routine for the person by.
//
// 🔑 A pause and a delete write the routine's file first. The file is what a
// run reads before it starts, so a routine that its file shows as paused or
// gone does not run, whatever its timer does after. A resume gives authority
// back, so it follows the order of an approval: the timer, the file, and
// then the line, with each failure undone.
func (w *Workspace) talkootRoutineSet(_ context.Context, id, by, rid, action, reason string) error {
	if !talkoot.ValidPerson(strings.TrimPrefix(by, talkoot.HumanPrefix)) {
		return fmt.Errorf("talkoot: the person's name %q must be 1 to 64 letters, digits, and . _ @ -", by)
	}
	if len(reason) > maxReasonBytesRoutine || strings.ContainsFunc(reason, func(r rune) bool { return r == '\n' || r == '\r' }) {
		return fmt.Errorf("talkoot: the reason must be one line of at most %d bytes", maxReasonBytesRoutine)
	}
	run, err := w.talkootRunOf(id)
	if err != nil {
		return err
	}
	run.update.Lock()
	defer run.update.Unlock()
	run.routineMu.Lock()
	defer run.routineMu.Unlock()
	now := time.Now()
	svc := w.Timers()
	cancel := func() error {
		if svc == nil {
			return nil
		}
		_, err := svc.Cancel(routineTimerOwner, rid)
		return err
	}
	r, err := talkoot.LoadRoutine(run.dir, rid)
	if errors.Is(err, talkoot.ErrRoutineDamaged) && action == ctrlproto.TalkootRoutineDelete {
		// A delete clears a record that does not read, so it does not hold
		// a place under the cap for good. Only its id is known, and the
		// room's last line for it names its member.
		r, err = talkoot.Routine{ID: rid}, nil
		if last, found, rerr := lastRoutineStep(run, rid); rerr == nil && found {
			r.Member = last.Member
		}
	}
	if errors.Is(err, talkoot.ErrNoRoutine) && action == ctrlproto.TalkootRoutineDelete {
		// 🔑 A delete that removed the file and then failed left a timer, or
		// no line in the room. The error told the person to delete again, and
		// this is that second try. The room says whether the routine was
		// ever approved, and whether its deletion is recorded.
		last, found, rerr := lastRoutineStep(run, rid)
		if rerr != nil {
			return rerr
		}
		if !found {
			return err
		}
		timed := false
		if svc != nil {
			var cerr error
			if timed, cerr = svc.Cancel(routineTimerOwner, rid); cerr != nil {
				return fmt.Errorf("talkoot: the routine is deleted, and its timer was not removed. Delete it again: %w", cerr)
			}
		}
		if last.Action == talkoot.RoutineDeleted {
			if timed {
				return nil
			}
			// The first delete finished, so this one names no routine.
			return err
		}
		r, err = talkoot.Routine{ID: rid, Member: last.Member}, nil
		if aerr := run.room.Append(talkoot.RoutineLine(r, talkoot.RoutineDeleted, humanBy(by), reason, now)); aerr != nil {
			return fmt.Errorf("talkoot: the routine is deleted, and the room could not record it. Delete it again: %w", aerr)
		}
		run.flush()
		return nil
	}
	if err != nil {
		return err
	}
	line := func(action string) error {
		if err := run.room.Append(talkoot.RoutineLine(r, action, humanBy(by), reason, now)); err != nil {
			return err
		}
		run.flush()
		return nil
	}
	switch action {
	case ctrlproto.TalkootRoutinePause:
		if r.Status == talkoot.RoutineEnded {
			// 🚨 An ended routine that a pause made paused could resume.
			return fmt.Errorf("talkoot: routine %s reached its end and starts no run. Delete it, and let the member ask for a new one", rid)
		}
		if r.Status == talkoot.RoutinePaused {
			// A second pause removes a timer that the first one left. It
			// writes the room's line only when the first one did not: the
			// record says who paused the routine and why, and the line
			// repeats that.
			if err := cancel(); err != nil {
				return fmt.Errorf("talkoot: the routine is paused, and its timer was not removed. Pause it again: %w", err)
			}
			last, found, err := lastRoutineStep(run, rid)
			if err != nil {
				return err
			}
			if found && last.Action == talkoot.ActionPaused {
				return nil
			}
			who, at := r.ChangedBy, r.ChangedAt
			if who == "" {
				who, at = humanBy(by), now
			}
			if err := run.room.Append(talkoot.RoutineLine(r, talkoot.ActionPaused, who, r.Reason, at)); err != nil {
				return fmt.Errorf("talkoot: the routine is paused, and the room could not record it. Pause it again: %w", err)
			}
			run.flush()
			return nil
		}
		r.Status, r.ChangedBy, r.ChangedAt, r.Reason = talkoot.RoutinePaused, humanBy(by), now, reason
		if err := talkoot.SaveRoutine(run.dir, r); err != nil {
			return err
		}
		// The command of a watcher's check does not go on after the pause.
		run.stopWatcher(rid)
		if err := cancel(); err != nil {
			return fmt.Errorf("talkoot: the routine is paused, and its timer was not removed. Pause it again: %w", err)
		}
		if err := line(talkoot.ActionPaused); err != nil {
			return fmt.Errorf("talkoot: the routine is paused, and the room could not record it. Pause it again: %w", err)
		}
		return nil
	case ctrlproto.TalkootRoutineDelete:
		if err := talkoot.RemoveRoutine(run.dir, rid); err != nil {
			return err
		}
		// A watcher's state goes with its record. RemoveRoutine has read
		// the id as a routine's, so the path stays inside the team. A check
		// that is at work is cancelled first, and the function that takes
		// its result removes what the command wrote after this.
		run.stopWatcher(rid)
		if err := os.RemoveAll(watcherDir(run, rid)); err != nil {
			w.diagf("talkoot %s: routine %s is deleted, and the directory of its watcher's state stays: %v", run.id, rid, err)
		}
		if err := cancel(); err != nil {
			return fmt.Errorf("talkoot: the routine is deleted, and its timer was not removed. Delete it again. Until then the timer shows in the Timers pane, and it starts nothing: %w", err)
		}
		if err := line(talkoot.RoutineDeleted); err != nil {
			return fmt.Errorf("talkoot: the routine is deleted, and the room could not record it. Delete it again: %w", err)
		}
		return nil
	case ctrlproto.TalkootRoutineResume:
		// 🚨 Read live, as an approval does. A resume sets a timer.
		if !config.TalkootRoutines().On {
			return errRoutinesOff
		}
		if svc == nil {
			return errRoutineNoTimers
		}
		if r.Status != talkoot.RoutinePaused {
			return fmt.Errorf("talkoot: routine %s is %s, and only a paused routine resumes", rid, r.Status)
		}
		if bar, err := routineBar(run, r, now); err != nil {
			return err
		} else if bar != nil {
			return bar
		}
		sched, _, err := routineSchedule(r.Schedule, now)
		if err != nil {
			return err
		}
		paused := r
		r.Status, r.ChangedBy, r.ChangedAt, r.Reason = talkoot.RoutineActive, humanBy(by), now, reason
		if err := w.routineTimer(svc, id, r, sched); err != nil {
			return err
		}
		if err := talkoot.SaveRoutine(run.dir, r); err != nil {
			if cerr := cancel(); cerr != nil {
				return fmt.Errorf("talkoot: the routine stays paused, and the timer of the failed resume was not removed. Pause it again to remove the timer: %w", errors.Join(err, cerr))
			}
			return err
		}
		if err := line(talkoot.RoutineResumed); err != nil {
			// 🚨 Each step back is tried, whatever the other did, and the
			// error says what the record holds now. A person who reads
			// "stays paused" over an active record would stop looking.
			serr := talkoot.SaveRoutine(run.dir, paused)
			cerr := cancel()
			switch {
			case serr != nil:
				return fmt.Errorf("talkoot: the room could not record the resume, and the routine's record could not go back to paused. The record says active, with no line in the room. Pause the routine: %w", errors.Join(err, serr, cerr))
			case cerr != nil:
				return fmt.Errorf("talkoot: the room could not record the resume, so the routine stays paused. Its timer was not removed. Pause it again to remove the timer: %w", errors.Join(err, cerr))
			}
			return fmt.Errorf("talkoot: the room could not record the resume, so the routine stays paused: %w", err)
		}
		return nil
	}
	return fmt.Errorf("talkoot: the action must be pause, resume or delete, not %q", action)
}

// maxReasonBytesRoutine bounds the reason a person gives for a pause, a
// resume or a delete. The room and the routine's record carry it.
const maxReasonBytesRoutine = 1024

// syncRoutineTimers sets the timer of each active routine that has none. A
// daemon that stopped between a routine's file and its timer leaves such a
// routine, and so does a timer file that was lost.
//
// 🚨 It sets no timer while routines are off, and none for a routine whose
// record does not read. It removes none: a timer whose routine is paused or
// gone starts nothing, because a run reads the routine's file first.
func (w *Workspace) syncRoutineTimers(svc *timers.Service) {
	if svc == nil || !w.talkoot.loaded.Load() || !config.TalkootRoutines().On {
		return
	}
	w.talkoot.mu.Lock()
	runs := make([]*talkootRun, 0, len(w.talkoot.runs))
	for _, run := range w.talkoot.runs {
		runs = append(runs, run)
	}
	w.talkoot.mu.Unlock()
	for _, run := range runs {
		w.syncRunRoutineTimers(svc, run)
	}
}

// syncRunRoutineTimers is syncRoutineTimers for one team.
//
// 🚨 It holds run.update, which a pause, a resume, a delete and an approval
// hold. A pause that ran between the read of a record and the set of its
// timer would otherwise leave a paused routine with a timer.
func (w *Workspace) syncRunRoutineTimers(svc *timers.Service, run *talkootRun) {
	run.update.Lock()
	defer run.update.Unlock()
	run.routineMu.Lock()
	defer run.routineMu.Unlock()
	now := time.Now()
	list, damaged, err := talkoot.ListRoutines(run.dir)
	if err != nil {
		w.diagf("talkoot %s: the routines do not read: %v", run.id, err)
		return
	}
	for _, rid := range damaged {
		w.diagf("talkoot %s: the record of routine %s does not read, so it has no timer", run.id, rid)
	}
	for _, r := range list {
		if r.Status != talkoot.RoutineActive {
			continue
		}
		if _, ok := svc.Get(routineTimerOwner, r.ID); ok {
			continue
		}
		// What a resume refuses gets no timer here. The routine stays in
		// the list, and the list says why it has no timer.
		if bar, err := routineBar(run, r, now); err != nil || bar != nil {
			w.diagf("talkoot %s: routine %s is active and gets no timer: %v", run.id, r.ID, errors.Join(bar, err))
			continue
		}
		sched, _, err := routineSchedule(r.Schedule, now)
		if err == nil {
			err = w.routineTimer(svc, run.id, r, sched)
		}
		if err != nil {
			w.diagf("talkoot %s: routine %s is active and has no timer, and one could not be set: %v", run.id, r.ID, err)
			continue
		}
		w.diagf("talkoot %s: routine %s was active with no timer, so its timer was set again", run.id, r.ID)
	}
}

// talkootRoutinesView is the state of routines for one talkoot, or nil when
// the person set no policy and the team has no routine. Decision 0029 rule
// 23 asks that a feature that changes authority shows its state while it is
// on.
func (w *Workspace) talkootRoutinesView(id string) *ctrlproto.TalkootRoutines {
	pol := config.TalkootRoutines()
	var list []talkoot.Routine
	var damaged []string
	run, err := w.talkootRunOf(id)
	if err == nil {
		if list, damaged, err = talkoot.ListRoutines(run.dir); err != nil {
			w.diagf("talkoot %s: the routines do not read: %v", id, err)
		}
	} else {
		run = nil
	}
	now := time.Now()
	if !pol.On && pol.Problem == "" && len(list) == 0 && len(damaged) == 0 {
		return nil
	}
	// The run of each routine that is at work now, by routine, and what each
	// routine's runs spent today.
	atWork, spent := map[string]string{}, map[string]float64{}
	if run != nil {
		_ = run.do(func(rt *talkoot.Router) error {
			for _, rr := range rt.OpenRuns() {
				atWork[rr.Routine] = rr.Session
			}
			for _, r := range list {
				spent[r.ID] = rt.RoutineSpend(r.ID)
			}
			return nil
		})
	}
	out := &ctrlproto.TalkootRoutines{On: pol.On, Problem: pol.Problem, Routines: make([]ctrlproto.TalkootRoutine, 0, len(list)+len(damaged))}
	svc := w.Timers()
	if pol.On {
		out.Max = pol.Max
		if svc == nil {
			out.On, out.Problem = false, errRoutineNoTimers.Error()
		}
	}
	infos := map[string]timers.Info{}
	if svc != nil {
		for _, ti := range svc.List() {
			if ti.Owner == routineTimerOwner {
				infos[ti.ID] = ti
			}
		}
	}
	for _, r := range list {
		wr := wireRoutine(r)
		wr.RunSession, wr.SpentTodayUSD = atWork[r.ID], spent[r.ID]
		if r.Status == talkoot.RoutineActive {
			if ti, ok := infos[r.ID]; ok {
				wr.Next, wr.Problem = ti.Next, ti.Problem
				if r.Kind == talkoot.RoutineWatcher && wr.Problem == "" && !watcherRuns() {
					wr.Problem = i18n.T("A watcher does not run yet. This version of terva runs no watcher command.")
				}
			} else {
				// The advice must be one that works. A resume refuses what
				// routineBar names, so the list names it too.
				wr.Problem = i18n.T("This routine has no timer, so it does not run. Pause it and resume it to set the timer again.")
				if run != nil {
					if bar, err := routineBar(run, r, now); err == nil && bar != nil {
						wr.Problem = i18n.T("This routine has no timer, so it does not run: %s.", strings.TrimPrefix(bar.Error(), "talkoot: "))
					}
				}
			}
		}
		out.Routines = append(out.Routines, wr)
	}
	for _, rid := range damaged {
		out.Routines = append(out.Routines, ctrlproto.TalkootRoutine{ID: rid, Problem: talkoot.ErrRoutineDamaged.Error() + "; delete it to clear it"})
	}
	slices.SortFunc(out.Routines, func(a, b ctrlproto.TalkootRoutine) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func wireRoutineFields(f talkoot.RoutineFields) ctrlproto.TalkootRoutineFields {
	return ctrlproto.TalkootRoutineFields{
		EndAfterRuns: f.EndAfterRuns, EndAfterDays: f.EndAfterDays, SpendUSDPerDay: f.SpendUSDPerDay,
		ReviewsPerDay: f.ReviewsPerDay, FollowUps: f.FollowUps, CatchUp: f.CatchUp,
	}
}

func wireRoutine(r talkoot.Routine) ctrlproto.TalkootRoutine {
	return ctrlproto.TalkootRoutine{
		ID: r.ID, Member: r.Member, Kind: r.Kind, Schedule: r.Schedule, Prompt: r.Prompt, Command: r.Command,
		Fields: wireRoutineFields(r.Fields), EndsAt: r.EndsAt, Status: r.Status,
		ApprovedBy: r.ApprovedBy, ApprovedAt: r.ApprovedAt, ChangedBy: r.ChangedBy, Reason: r.Reason, Runs: r.Runs,
	}
}

// wireRoutineAsk is a routine's ask as its card shows it: the limits that
// differ from the defaults of its kind, read from the configuration now.
//
// 🚨 While routines are off the policy holds no defaults, and a compare with
// zeros would show wrong ones. The card then lists each limit that the member
// set, with no default beside it, and DefaultsUnknown says why.
func wireRoutineAsk(p talkoot.Proposal) *ctrlproto.TalkootRoutineAsk {
	a := *p.Routine
	pol := config.TalkootRoutines()
	out := &ctrlproto.TalkootRoutineAsk{Kind: a.Kind, Schedule: a.Schedule, Prompt: a.Prompt, Command: a.Command}
	if !pol.On {
		out.DefaultsUnknown = true
		for _, d := range a.Set() {
			out.Differs = append(out.Differs, ctrlproto.TalkootRoutineDiff{Field: d.Field, Value: d.Value})
		}
		return out
	}
	f, diffs := a.Resolve(routineDefaults(pol, a.Kind))
	out.Fields = new(wireRoutineFields(f))
	out.Digest = a.Digest(p.ID, p.Proposer, f)
	for _, d := range diffs {
		out.Differs = append(out.Differs, ctrlproto.TalkootRoutineDiff{Field: d.Field, Value: d.Value, Default: d.Default})
	}
	return out
}
