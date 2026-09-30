package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/agent/talkoot"
)

// The introduction turn a kickoff estimate assumes: the member's system prompt,
// its tool definitions, and the instruction in, and one short post out. The
// roster's own size is added to the input, and each other member's note to the
// coordinator's. These are assumptions, and the card calls its figure an
// estimate.
const (
	introInputTokens     = 12000
	introOutputTokens    = 400
	introNoteInputTokens = 300
)

// kickoffWait bounds how long a kickoff waits for the other members'
// introductions before the coordinator writes. A member held behind a pause
// may not take its turn for hours, and the coordinator still writes.
var kickoffWait = 10 * time.Minute

// kickoffPoll is how often a kickoff checks whether the introductions ended.
var kickoffPoll = 100 * time.Millisecond

// kickoffOrder is the order a kickoff introduces the members in: the roster's,
// with the coordinator last.
func kickoffOrder(r talkoot.Roster) []talkoot.Member {
	out := make([]talkoot.Member, 0, len(r.Members))
	var lead []talkoot.Member
	for _, m := range r.Members {
		if m.Role == talkoot.RoleCoordinator {
			lead = append(lead, m)
			continue
		}
		out = append(out, m)
	}
	return append(out, lead...)
}

// kickoffPlan is what a kickoff of run would do now, and its estimate.
func (w *Workspace) kickoffPlan(run *talkootRun, state string) (ctrlproto.TalkootKickoff, error) {
	r := *run.roster.Load()
	// The roster file's size only feeds the estimate, so a read that fails
	// leaves it out rather than stopping the kickoff.
	text, _ := os.ReadFile(filepath.Join(run.dir, talkoot.FileName))
	plan := ctrlproto.TalkootKickoff{State: state, Members: []ctrlproto.TalkootKickoffMember{}}
	err := run.do(func(rt *talkoot.Router) error {
		for _, m := range kickoffOrder(r) {
			km := ctrlproto.TalkootKickoffMember{Member: m.ID}
			switch {
			case w.memberUnbound(m) != nil:
				km.Plain = w.memberUnbound(m).Error()
			case pausedWhy(rt, m.ID) != "":
				km.Plain = pausedWhy(rt, m.ID)
			}
			if km.Plain == "" {
				in := introInputTokens + len(text)/4
				if m.Role == talkoot.RoleCoordinator {
					in += introNoteInputTokens * (len(r.Members) - 1)
				}
				km.Model, km.EstimateUSD = w.estimateIntro(m, in, introOutputTokens)
				if km.EstimateUSD == nil {
					plan.UnpricedTurns++
				} else {
					plan.EstimateUSD += *km.EstimateUSD
				}
			}
			plan.Members = append(plan.Members, km)
		}
		return nil
	})
	return plan, err
}

// estimateIntro prices one introduction turn of m. The estimate is nil when
// the model cannot be resolved or has no price.
func (w *Workspace) estimateIntro(m talkoot.Member, in, out int) (string, *float64) {
	prov, model, _, err := w.memberModel(m)
	if err != nil {
		return "", nil
	}
	found, err := modelreg.FindModel(prov, model)
	if err != nil || (found.PriceInput == 0 && found.PriceOutput == 0) {
		return model, nil
	}
	usd := float64(in)*found.PriceInput/1e6 + float64(out)*found.PriceOutput/1e6
	return model, &usd
}

// kickoffCard is the inbox card of a kickoff that waits for a person.
func (w *Workspace) kickoffCard(run *talkootRun) (ctrlproto.TalkootCard, bool) {
	k, ok, err := talkoot.LoadKickoff(run.dir)
	if err != nil {
		w.diagf("talkoot %s: %v", run.id, err)
		return ctrlproto.TalkootCard{}, false
	}
	if !ok || k.State != ctrlproto.TalkootKickoffWaiting {
		return ctrlproto.TalkootCard{}, false
	}
	plan, err := w.kickoffPlan(run, k.State)
	if err != nil {
		return ctrlproto.TalkootCard{}, false
	}
	return ctrlproto.TalkootCard{Kind: ctrlproto.TalkootCardKickoff, ID: ctrlproto.TalkootCardKickoff, At: k.At, Kickoff: &plan}, true
}

// waitKickoff records a new talkoot's kickoff as waiting for a person, and
// shows its card in the inbox. It wakes nobody, so a create stays a write.
func (w *Workspace) waitKickoff(run *talkootRun) error {
	if err := talkoot.SaveKickoff(run.dir, talkoot.Kickoff{State: ctrlproto.TalkootKickoffWaiting, At: time.Now()}); err != nil {
		return err
	}
	if c, ok := w.kickoffCard(run); ok {
		ev := ctrlproto.TalkootInboxEvent(run.id, c)
		w.talkootEmit(talkootEvent{Talkoot: run.id, Kind: "inbox", Wire: &ev})
	}
	return nil
}

// talkootKickoff runs a team's introductions in the name of by, or with skip
// writes a plain card for each member. It refuses a team that had its
// kickoff. A team made before kickoffs shipped has no record, and runs one.
//
// It returns once the kickoff has started. The members introduce themselves
// in roster order, and the coordinator writes after the others' turns end.
func (w *Workspace) talkootKickoff(ctx context.Context, id, by string, skip bool) (ctrlproto.TalkootKickoff, error) {
	if !talkoot.ValidPerson(strings.TrimPrefix(by, talkoot.HumanPrefix)) {
		return ctrlproto.TalkootKickoff{}, fmt.Errorf("talkoot: %q must name a person in 1 to 64 letters, digits, and . _ @ -", by)
	}
	run, err := w.talkootRunOf(id)
	if err != nil {
		return ctrlproto.TalkootKickoff{}, err
	}
	// 🔑 update serializes a kickoff with the roster updates and with another
	// kickoff, so two callers cannot both find the card waiting.
	run.update.Lock()
	defer run.update.Unlock()
	k, ok, err := talkoot.LoadKickoff(run.dir)
	if err != nil {
		return ctrlproto.TalkootKickoff{}, err
	}
	// A running kickoff that no goroutine here runs was cut short by a stop
	// or a restart. It may run again. Members it already introduced then
	// introduce themselves a second time, which beats a team that never
	// hears from its coordinator.
	interrupted := k.State == ctrlproto.TalkootKickoffRunning && !run.kicking.Load()
	if ok && k.State != ctrlproto.TalkootKickoffWaiting && !interrupted {
		return ctrlproto.TalkootKickoff{}, fmt.Errorf("%w: its introductions are %s", ErrKickoffRan, k.State)
	}
	state := ctrlproto.TalkootKickoffRunning
	if skip {
		state = ctrlproto.TalkootKickoffSkipped
	}
	plan, err := w.kickoffPlan(run, state)
	if err != nil {
		return ctrlproto.TalkootKickoff{}, err
	}
	plan.By = humanBy(by)
	order := kickoffOrder(*run.roster.Load())
	if skip {
		// 🔑 The cards go before the record. A card that fails leaves the
		// kickoff waiting, so a person can try again. A retry writes a
		// second card for a member that got one, which costs nothing.
		plan.EstimateUSD, plan.UnpricedTurns = 0, 0
		for i := range plan.Members {
			plan.Members[i].Model, plan.Members[i].EstimateUSD = "", nil
			plan.Members[i].Plain = skippedIntro
		}
		for _, m := range order {
			if _, err := w.introduce(run, by, m, talkoot.IntroKickoff, skippedIntro); err != nil {
				return ctrlproto.TalkootKickoff{}, err
			}
		}
	}
	if err := talkoot.SaveKickoff(run.dir, talkoot.Kickoff{State: state, By: plan.By, At: time.Now()}); err != nil {
		return ctrlproto.TalkootKickoff{}, err
	}
	if ok && k.State == ctrlproto.TalkootKickoffWaiting {
		ev := ctrlproto.TalkootInboxResolvedEvent(id, ctrlproto.TalkootCard{Kind: ctrlproto.TalkootCardKickoff, ID: ctrlproto.TalkootCardKickoff})
		w.talkootEmit(talkootEvent{Talkoot: id, Kind: "inbox", Wire: &ev})
	}
	if !skip {
		run.kickMu.Lock()
		run.kickWoke, run.kickLed = nil, false
		run.kickMu.Unlock()
		run.kicking.Store(true)
		go w.runKickoff(run, by, order)
	}
	return plan, nil
}

// runKickoff introduces each member but the coordinator, waits until their
// turns end, then introduces the coordinator, and records the kickoff done.
// A member a roster update removed while the kickoff ran is owed nothing, and
// the coordinator is the one on the roster when its turn comes.
//
// A kickoff with an introduction that failed stays running on disk. It ends
// here, so a person may run it again.
func (w *Workspace) runKickoff(run *talkootRun, by string, order []talkoot.Member) {
	defer run.kicking.Store(false)
	failed := 0
	// introduce introduces the member as the roster holds it now, and
	// reports whether the kickoff should go on.
	introduce := func(m talkoot.Member, mode talkoot.IntroMode) (talkoot.Envelope, bool) {
		cur, ok := memberOf(*run.roster.Load(), m.ID)
		if !ok {
			return talkoot.Envelope{}, true
		}
		e, err := w.introduce(run, by, cur, mode, "")
		switch {
		case err == nil:
		case errors.Is(err, ErrTalkootClosed):
			return e, false
		case !onRoster(run, m.ID):
			// The member left the roster between the check and the envelope.
		default:
			w.diagf("talkoot %s: could not introduce %s: %v", run.id, m.ID, err)
			failed++
		}
		return e, true
	}
	for _, m := range order {
		if m.Role == talkoot.RoleCoordinator {
			continue
		}
		e, ok := introduce(m, talkoot.IntroKickoff)
		if !ok {
			return
		}
		if e.ID != "" {
			run.kickMu.Lock()
			run.kickWoke = append(run.kickWoke, m.ID)
			run.kickMu.Unlock()
		}
	}
	// 🔑 A member that joins while the kickoff waits adds itself to
	// kickWoke. The coordinator goes only once a wait saw every member in
	// the list, and kickLed turns later joins to the ordinary instruction.
	deadline := time.Now().Add(kickoffWait)
	for {
		seen, ok := w.awaitIntroductions(run, deadline)
		if !ok {
			return
		}
		run.kickMu.Lock()
		done := len(run.kickWoke) == seen
		if done {
			run.kickLed = true
		}
		run.kickMu.Unlock()
		if done {
			break
		}
	}
	for _, m := range kickoffOrder(*run.roster.Load()) {
		if m.Role != talkoot.RoleCoordinator {
			continue
		}
		if _, ok := introduce(m, talkoot.IntroKickoffLead); !ok {
			return
		}
	}
	if failed > 0 {
		w.diagf("talkoot %s: %d introductions failed, so the kickoff is not done; run talkoot.kickoff again to retry", run.id, failed)
		return
	}
	if err := talkoot.SaveKickoff(run.dir, talkoot.Kickoff{State: ctrlproto.TalkootKickoffDone, By: humanBy(by), At: time.Now()}); err != nil {
		w.diagf("talkoot %s: %v", run.id, err)
	}
}

// onRoster reports whether member is on run's roster now.
func onRoster(run *talkootRun, member string) bool {
	_, ok := memberOf(*run.roster.Load(), member)
	return ok
}

// awaitIntroductions waits until no member the kickoff woke is working or
// holds a delivery, or until deadline passes. It returns how many members it
// saw woken, and false when the talkoot stopped and the kickoff should end.
func (w *Workspace) awaitIntroductions(run *talkootRun, deadline time.Time) (int, bool) {
	for {
		run.kickMu.Lock()
		woke := slices.Clone(run.kickWoke)
		run.kickMu.Unlock()
		busy := false
		err := run.do(func(rt *talkoot.Router) error {
			held := rt.Held()
			for _, s := range rt.Statuses() {
				if slices.Contains(woke, s.Member) && (s.Working || slices.Contains(held, s.Member)) {
					busy = true
				}
			}
			return nil
		})
		if err != nil {
			return 0, false
		}
		if !busy {
			return len(woke), true
		}
		if time.Now().After(deadline) {
			w.diagf("talkoot %s: the kickoff stopped waiting for the introductions after %s", run.id, kickoffWait)
			return len(woke), true
		}
		time.Sleep(kickoffPoll)
	}
}
