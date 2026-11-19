package workspace

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/provider"
)

// pricedModel is a model that the tests give a price: one dollar for a
// million prompt tokens. The fake provider reports a count of tokens, and the
// price makes it a cost. A run that reports 100,000 tokens costs ten cents.
//
// ⚠️ The model goes into the process's registry, as a login adds the models
// of an endpoint. It stays there for the other tests of this package, which
// name another model.
const pricedModel = "priced-model"

func priceTheModel() {
	modelreg.RegisterExtraModel(provider.Model{
		Provider: "openai-compatible", ID: pricedModel, DisplayName: "Priced", ContextWindow: 128000, MaxOutput: 16384, PriceInput: 1.0,
	})
}

// limitsCrew is runCrew with the two switches that part 2 reads, on a model
// that has a price. wakes says whether the person turned wakes on.
func limitsCrew(t *testing.T, p *runScript, wakes bool) (w *Workspace, seat talkootSeat, clock *stepClock, cwd string) {
	t.Helper()
	cwd = talkootHome(t)
	limitsConfig(t, wakes, true)
	clock = &stepClock{now: time.Now()}
	w = limitsDaemon(t, cwd, clock, p)
	if _, err := w.talkootCreate(t.Context(), "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	return w, helmAfterOneTurn(t, w), clock, cwd
}

func limitsConfig(t *testing.T, wakes, routines bool) {
	t.Helper()
	cfg := `{"talkoot_enabled": true`
	if routines {
		cfg += `, "talkoot_routines": {"max": 5}`
	}
	if wakes {
		cfg += `, "talkoot_wakes": {"per_member": 3, "max_delay": "2h"}`
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("TERVA_HOME"), "config.json"), []byte(cfg+"}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func limitsDaemon(t *testing.T, cwd string, clock *stepClock, p *runScript) *Workspace {
	t.Helper()
	priceTheModel()
	srv := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(srv.Close)
	w, err := NewWorkspace(build.Args{
		Provider: "openai-compatible", BaseURL: srv.URL, APIKey: "k", Model: pricedModel,
		CWD: cwd, NoExt: true, NoMCP: true,
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { _ = w.Close() }) })
	w.timers.clock = clock
	w.LoadTalkoots()
	w.LoadTimers()
	if w.Timers() == nil {
		t.Fatalf("the daemon runs no timer service: %q", w.timers.problem)
	}
	return w
}

// spendTask is ciTask with a daily limit of one cent.
func spendTask() talkoot.RoutineAsk {
	ask := ciTask
	ask.SpendUSDPerDay = new(0.01)
	return ask
}

// 🚨 Criterion 5, and decision 0029 rule 17. A run whose cost passed its
// routine's daily limit pauses the routine, and the room gives the two
// amounts. The run's last response passed the limit, so the run did its
// work: its report still reaches the member.
func TestARunThatPassesItsRoutinesDailyLimitPausesTheRoutine(t *testing.T) {
	p := &runScript{reply: "Checked. All green.", usage: 100_000}
	w, seat, clock, _ := limitsCrew(t, p, false)
	rid := approved(t, w, seat, spendTask())
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the routine to pause", func() bool { return len(routineSteps(t, talkoot.ActionPaused)) == 1 })
	tl := runTurns(t)
	if len(tl) != 1 || tl[0].CostUSD <= 0.01 || tl[0].Guard != "" || tl[0].Interrupted {
		t.Fatalf("the run ended well, and the room holds a cost above the limit: %+v", tl)
	}
	if n := len(runEnds(t, talkoot.KindReport)); n != 1 {
		t.Fatalf("a run whose last response passed the limit still reports, got %d reports", n)
	}
	paused := routineSteps(t, talkoot.ActionPaused)[0]
	if paused.By != "" || !strings.Contains(paused.Reason, "its runs spent $") || !strings.Contains(paused.Reason, "and its limit is $0.01 a day") {
		t.Fatalf("the room must say that the limit paused the routine, and give the amounts: %+v", paused)
	}
	r := routineOf(t, w, rid)
	if r.Status != talkoot.RoutinePaused || r.SpentTodayUSD != tl[0].CostUSD {
		t.Fatalf("the list must show the pause and what the routine spent today: %+v", r)
	}
	if ts := routineTimers(w); len(ts) != 0 {
		t.Fatalf("a paused routine holds no timer: %+v", ts)
	}

	// 🚨 A person who resumes it on the same day gets no run. The limit is for
	// the day, and the room says so one time.
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineResume, ""); err != nil {
		t.Fatal(err)
	}
	fire(t, w, clock, 16*time.Minute)
	fire(t, w, clock, 16*time.Minute)
	if n := len(runRoots(t)); n != 1 {
		t.Fatalf("a routine past its limit started %d more runs after a resume", n-1)
	}
	skipped := routineSteps(t, talkoot.RoutineSkipped)
	if len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "and its limit is $0.01 a day") {
		t.Fatalf("the room must say one time why the routine starts no run: %+v", skipped)
	}
}

// 🚨 A run that would go on past its routine's limit is stopped at its next
// step. The router tests the limit only at a start and counts the cost only
// at an end, so without this a run spends for up to half an hour past it.
func TestARunIsStoppedAtItsNextStepPastTheLimit(t *testing.T) {
	p := &runScript{reply: "Done.", usage: 100_000, callName: "talkoot_roster", callArgs: "{}", stall: true}
	w, seat, clock, _ := limitsCrew(t, p, false)
	rid := approved(t, w, seat, spendTask())
	fire(t, w, clock, 16*time.Minute)
	waitRunEnds(t, w, "the end of the run", 1)
	tl := runTurns(t)[0]
	if tl.Guard != talkoot.GuardFailed || !strings.Contains(tl.Reason, "the run reached the routine's limit of $0.01 a day and was stopped") {
		t.Fatalf("a run that was stopped at the limit ends as failed, with that cause: %+v", tl)
	}
	if tl.CostUSD <= 0.01 {
		t.Fatalf("the cost of the response that passed the limit must be on record: %+v", tl)
	}
	if len(runEnds(t, talkoot.KindReport)) != 0 || len(runEnds(t, talkoot.KindNote)) != 1 {
		t.Fatal("a run that was stopped sends a note about the failure, and no report")
	}
	waitTalkoot(t, "the routine to pause", func() bool { return routineOf(t, w, rid).Status == talkoot.RoutinePaused })
	if r := routineOf(t, w, rid); !strings.Contains(r.Reason, "a run failed: the run reached the routine's limit") {
		t.Fatalf("the list must say why the routine paused: %+v", r)
	}
	// 🚨 The stop is before the next request, and not a cut of it. The
	// fixture keeps a request after the tool call open, so one that reached
	// it is on this list.
	if n := len(p.runs()); n != 1 {
		t.Fatalf("a run that was stopped at its tool call made %d requests, and it may make one", n)
	}
}

// 🚨 A run can take one more step with no tool call: a text that was queued
// while it worked makes the loop ask the model again. That step is stopped
// before its request too. A stop at the event after the request cut a request
// that the provider had already begun to count.
func TestARunPastTheLimitMakesNoFurtherRequest(t *testing.T) {
	p := &runScript{reply: "Done.", usage: 100_000}
	w, seat, clock, _ := limitsCrew(t, p, false)
	approved(t, w, seat, spendTask())
	// A text joins the queue of the run's session while its first request is
	// open, so the turn goes on after the response that passes the limit.
	queued := make(chan bool, 1)
	p.set(func() {
		p.onRun = func() {
			var sid string
			w.talkoot.mu.Lock()
			for id, b := range w.talkoot.seats {
				if b.routine != nil {
					sid = id
				}
			}
			w.talkoot.mu.Unlock()
			s := w.existing(sid)
			if s != nil {
				s.queueFrom(ctrlproto.QueueSourceTalkootDelivery, "One more thing.")
			}
			queued <- s != nil
		}
	})
	fire(t, w, clock, 16*time.Minute)
	waitRunEnds(t, w, "the end of the run", 1)
	if !<-queued {
		t.Fatal("the test found no session of the run to queue a text in")
	}
	if tl := runTurns(t)[0]; tl.Guard != talkoot.GuardFailed || !strings.Contains(tl.Reason, "the run reached the routine's limit") {
		t.Fatalf("the run must be stopped at the step that the queued text began: %+v", tl)
	}
	if n := len(p.runs()); n != 1 {
		t.Fatalf("a run past its limit made %d requests, and it may make one", n)
	}
}

// 🚨 A request that fails after it reported a cost is tried again, and the
// retry is a request too. A run past its limit is stopped before the retry.
func TestARunPastTheLimitIsNotTriedAgain(t *testing.T) {
	p := &runScript{usage: 100_000, cut: true}
	w, seat, clock, _ := limitsCrew(t, p, false)
	approved(t, w, seat, spendTask())
	fire(t, w, clock, 16*time.Minute)
	// A run that is tried again does not end for a long time, so the wait
	// ends at a second request too.
	waitTalkoot(t, "the end of the run, or a second request", func() bool { return len(runTurns(t)) == 1 || len(p.runs()) > 1 })
	if n := len(p.runs()); n != 1 {
		t.Fatalf("a run past its limit made %d requests, and it may make one", n)
	}
	if tl := runTurns(t)[0]; tl.Guard != talkoot.GuardFailed || !strings.Contains(tl.Reason, "the run reached the routine's limit") {
		t.Fatalf("the run must be stopped before the retry of a request that passed the limit: %+v", tl)
	}
}

// 🚨 The stop must not depend on a lock that is free. The first version
// found the run through the seats with a lock that it did not wait for. A
// usage event that met a busy lock was dropped, and the run went on past its
// limit until its end.
func TestABusyLockDoesNotLetARunPassItsLimit(t *testing.T) {
	p := &runScript{reply: "Done.", usage: 100_000, callName: "talkoot_roster", callArgs: "{}", stall: true}
	w, seat, clock, _ := limitsCrew(t, p, false)
	approved(t, w, seat, spendTask())
	// The seats' lock is busy while the response that passes the limit
	// arrives, and for a moment after.
	p.set(func() {
		p.onRun = func() {
			w.talkoot.mu.Lock()
			time.AfterFunc(300*time.Millisecond, w.talkoot.mu.Unlock)
		}
	})
	fire(t, w, clock, 16*time.Minute)
	waitRunEnds(t, w, "the end of the run", 1)
	if tl := runTurns(t)[0]; tl.Guard != talkoot.GuardFailed || !strings.Contains(tl.Reason, "the run reached the routine's limit") {
		t.Fatalf("the run must be stopped at the limit though the lock was busy: %+v", tl)
	}
}

// 🚨 A run that works across midnight is not stopped for what its routine
// spent the day before. Its own cost still counts against the limit.
func TestARunAcrossMidnightCountsOnlyItsOwnCost(t *testing.T) {
	began := time.Date(2026, 11, 17, 23, 50, 0, 0, time.Local)
	rr := &routineRun{run: talkoot.Run{At: began, SpentUSD: 0.9, SpendCapUSD: 1}}
	rr.cost(0.2, began.Add(20*time.Minute))
	if rr.over.Load() || rr.step() {
		t.Fatal("after midnight, 0.20 of a limit of 1.00 is not over it")
	}
	rr.cost(1.0, began.Add(20*time.Minute))
	if !rr.step() {
		t.Fatal("a run whose own cost reached the limit is over it on any day")
	}
	// The control: on the day it began, the same cost passes the limit.
	same := &routineRun{run: talkoot.Run{At: began, SpentUSD: 0.9, SpendCapUSD: 1}}
	same.cost(0.2, began.Add(5*time.Minute))
	if !same.step() {
		t.Fatal("before midnight, 0.90 and 0.20 pass a limit of 1.00")
	}
	if same.step() {
		t.Fatal("a run is stopped one time")
	}
}

// A run under its routine's limit pauses nothing: the control for the two
// tests above, on the same model and the same usage.
func TestARunUnderItsRoutinesLimitPausesNothing(t *testing.T) {
	p := &runScript{reply: "Checked.", usage: 100_000}
	w, seat, clock, _ := limitsCrew(t, p, false)
	rid := approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the report", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })
	settleRuns(t, w)
	r := routineOf(t, w, rid)
	if r.Status != talkoot.RoutineActive || r.SpentTodayUSD <= 0.01 || r.SpentTodayUSD >= r.Fields.SpendUSDPerDay {
		t.Fatalf("a run under the limit leaves the routine active, and the list shows its spend: %+v", r)
	}
}

// wakeArgs is the follow-up that a scripted run sets.
const wakeArgs = `{"delay_minutes": 20, "reason": "Look at the CI run of the import again."}`

// fireWake moves the test's clock to the time of the one follow-up that
// holds a timer, and no later, and lets the timer service fire it.
//
// ⚠️ The router reads the real clock, and the timer service reads the
// test's. A run sets its follow-up for 20 minutes after the real time. A
// test clock that is past that time makes the next run's follow-up due at
// the moment it is set. The service's own loop then starts a third run with
// no call from the test, and a wait for exactly two reports can miss its
// moment. A Docs Gate run on 2026-11-18 failed that way in two tests. The
// next follow-up is set later in real time, so it is not due at this time.
func fireWake(t *testing.T, w *Workspace, clock *stepClock) {
	t.Helper()
	ts := wakeTimers(w)
	if len(ts) != 1 {
		t.Fatalf("want one follow-up with a timer, got %+v", ts)
	}
	// ⚠️ Round(0) drops the monotonic reading of each time. With it, Sub
	// measures on the monotonic clock, and the wall time that the service
	// compares lands some nanoseconds before the timer's.
	fire(t, w, clock, ts[0].Schedule.At.Round(0).Sub(clock.Now().Round(0)))
}

// 🚨 Criterion 4, and decision 0029 rule 11. A run sets one follow-up, the
// follow-up starts one more run of the routine, and the line of runs ends at
// the card's allowance. The last run of the line has no tool to set one.
func TestARunSetsFollowUpsUpToItsCardsAllowance(t *testing.T) {
	p := &runScript{reply: "Still at work.", callName: "talkoot_wake", callArgs: wakeArgs}
	w, seat, clock, _ := limitsCrew(t, p, true)
	rid := approved(t, w, seat, ciTask) // a task's default allows 2 follow-ups
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the first report", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })

	sets := func() []talkoot.Line {
		return roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineWake && l.Action == talkoot.WakeSet })
	}
	first := runRoots(t)[0]
	if ws := sets(); len(ws) != 1 || ws[0].Chain != first.Envelope.ID || ws[0].Member != "helm" {
		t.Fatalf("the room must hold the follow-up in the chain of the run that set it: %+v", ws)
	}
	open := w.talkootWakesView("crew").Open
	if len(open) != 1 || open[0].Routine != rid || open[0].Member != "helm" {
		t.Fatalf("the list of wakes must show the follow-up with its routine: %+v", open)
	}
	if ts := wakeTimers(w); len(ts) != 1 || ts[0].ID != open[0].ID {
		t.Fatalf("the follow-up must hold a timer: %+v", ts)
	}

	// The follow-up comes due, and a second run starts from it.
	fireWake(t, w, clock)
	waitTalkoot(t, "the second report", func() bool { return len(runEnds(t, talkoot.KindReport)) == 2 })
	roots := runRoots(t)
	if len(roots) != 2 || roots[1].Envelope.ReplyTo != open[0].ID || roots[1].Envelope.Chain.Root != roots[1].Envelope.ID {
		t.Fatalf("the follow-up's run is the root of a new chain, and it names its wake: %+v", roots)
	}
	if !runRead(p, "terva started this run at the time of a follow-up", "This run is follow-up 1 of 2", "Look at the CI run of the import again.") {
		t.Fatal("the follow-up's run must read its number and the reason")
	}
	// 🚨 The member's own session got no turn from the wake.
	if n := len(wakeEnvelopes(t)); n != 0 {
		t.Fatalf("a run's follow-up must send the member no wake, got %d", n)
	}

	// The second run set the last follow-up of the line. It is not due yet,
	// so no third run started (fireWake).
	if ws := sets(); len(ws) != 2 {
		t.Fatalf("follow-up 1 sets follow-up 2, got %d wakes", len(ws))
	}
	if n := len(runRoots(t)); n != 2 {
		t.Fatalf("follow-up 2 is not due, and it started a run: %d roots", n)
	}
	fireWake(t, w, clock)
	waitTalkoot(t, "the third report", func() bool { return len(runEnds(t, talkoot.KindReport)) == 3 })
	settleRuns(t, w)
	if ws := sets(); len(ws) != 2 {
		t.Fatalf("the run at the allowance must set no follow-up, got %d wakes", len(ws))
	}
	p.mu.Lock()
	offered := slices.Clone(p.tools)
	p.mu.Unlock()
	if len(offered) != 3 || !slices.Contains(offered[0], "talkoot_wake") || !slices.Contains(offered[1], "talkoot_wake") || slices.Contains(offered[2], "talkoot_wake") {
		t.Fatalf("the first two runs hold talkoot_wake, and the run at the allowance does not: %v", offered)
	}
	if ts := wakeTimers(w); len(ts) != 0 {
		t.Fatalf("a follow-up that started its run holds no timer: %+v", ts)
	}
	if open := w.talkootWakesView("crew").Open; len(open) != 0 {
		t.Fatalf("no follow-up is open at the end of the line: %+v", open)
	}
	if r := routineOf(t, w, rid); r.Runs != 3 || r.Status != talkoot.RoutineActive {
		t.Fatalf("a follow-up is a run of its routine, so the routine counts 3: %+v", r)
	}
}

// 🚨 A follow-up is a timer that an agent set, so each switch that stops a
// timer stops it. Every case reads what holds when the follow-up comes due.
func TestAFollowUpThatMayNotRunIsDropped(t *testing.T) {
	cases := map[string]struct {
		change func(t *testing.T, w *Workspace, rid string)
		why    string
	}{
		"wakes off":    {func(t *testing.T, _ *Workspace, _ string) { limitsConfig(t, false, true) }, "wakes are off in the user configuration"},
		"routines off": {func(t *testing.T, _ *Workspace, _ string) { limitsConfig(t, true, false) }, "routines are off in the user configuration"},
		"paused": {func(t *testing.T, w *Workspace, rid string) {
			if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutinePause, "Too noisy."); err != nil {
				t.Fatal(err)
			}
		}, "the routine is paused"},
		"deleted": {func(t *testing.T, w *Workspace, rid string) {
			if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineDelete, ""); err != nil {
				t.Fatal(err)
			}
		}, "a person deleted the routine"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := &runScript{reply: "Still at work.", callName: "talkoot_wake", callArgs: wakeArgs}
			w, seat, clock, _ := limitsCrew(t, p, true)
			rid := approved(t, w, seat, ciTask)
			fire(t, w, clock, 16*time.Minute)
			waitTalkoot(t, "the first report", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })
			if ts := wakeTimers(w); len(ts) != 1 {
				t.Fatalf("the run must have set its follow-up: %+v", ts)
			}
			c.change(t, w, rid)
			fire(t, w, clock, 5*time.Minute)
			if n := len(runRoots(t)); n != 1 {
				t.Fatalf("the follow-up started a run, got %d roots", n)
			}
			dropped := roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineWake && l.Action == talkoot.WakeDropped })
			if len(dropped) != 1 || dropped[0].Reason != c.why {
				t.Fatalf("the room must say why the follow-up started no run, want %q: %+v", c.why, dropped)
			}
			if ts := wakeTimers(w); len(ts) != 0 {
				t.Fatalf("a dropped follow-up holds no timer: %+v", ts)
			}
		})
	}
}

// 🚨 A follow-up that comes due while a run of its routine still works is
// not lost. That run is often the one that set it. The follow-up stays open
// with its timer, and it starts its run when the routine is free.
func TestAFollowUpWaitsForARunThatIsStillAtWork(t *testing.T) {
	p := &runScript{reply: "Still at work.", callName: "talkoot_wake", callArgs: wakeArgs}
	release := p.held(t)
	w, seat, clock, _ := limitsCrew(t, p, true)
	approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	select {
	case <-p.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the run did not come back from its tool call")
	}
	if ts := wakeTimers(w); len(ts) != 1 {
		t.Fatalf("the run must have set its follow-up: %+v", ts)
	}
	// The follow-up comes due, and the run that set it is still at work.
	fireWake(t, w, clock)
	if n := len(runRoots(t)); n != 1 {
		t.Fatalf("a follow-up must start no run beside a run of its routine, got %d roots", n)
	}
	closed := roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineWake && l.Action != talkoot.WakeSet })
	if ts := wakeTimers(w); len(closed) != 0 || len(ts) != 1 || !ts[0].Schedule.At.After(clock.Now()) {
		t.Fatalf("the follow-up must stay open, with a timer for a later try: closed %+v, timers %+v", closed, ts)
	}
	release()
	waitTalkoot(t, "the first report", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })
	// ⚠️ The follow-up's run sets no follow-up of its own. The later try is
	// past the time of such a follow-up, so it would start a third run at
	// once, and the wait below would miss the moment of two roots.
	p.mu.Lock()
	p.callName = ""
	p.mu.Unlock()
	fire(t, w, clock, time.Minute)
	waitTalkoot(t, "the follow-up's run", func() bool { return len(runRoots(t)) == 2 })
}

// 🚨 The seat reads both switches at each call. A run that still holds
// talkoot_wake sets no follow-up after a person turned routines off, and the
// router's own limit refuses a second one.
func TestARunsSeatReadsTheSwitchesAndTheAllowanceAtEachCall(t *testing.T) {
	p := &runScript{reply: "Done."}
	release := p.held(t)
	w, seat, clock, _ := limitsCrew(t, p, true)
	rid := approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	select {
	case <-p.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the run made no request")
	}
	runSeat, ok := w.talkootSeatOf(routineOf(t, w, rid).RunSession)
	if !ok || runSeat.b.routine == nil {
		t.Fatal("the run's session holds no run seat")
	}
	limitsConfig(t, true, false)
	if _, err := runSeat.Wake(10*time.Minute, "Look again."); !errors.Is(err, errRoutinesOff) {
		t.Fatalf("a run must set no follow-up while routines are off, got %v", err)
	}
	limitsConfig(t, true, true)
	rec, err := runSeat.Wake(10*time.Minute, "Look again.")
	if err != nil || rec.Wake.Follow != 1 || rec.FollowUps != 2 || rec.Wake.Routine != rid {
		t.Fatalf("with both switches on the run sets follow-up 1 of 2: %+v, %v", rec, err)
	}
	if _, err := runSeat.Wake(10*time.Minute, "And again."); !errors.Is(err, talkoot.ErrNoFollowUp) {
		t.Fatalf("a run sets one follow-up, got %v", err)
	}
	if ts := wakeTimers(w); len(ts) != 1 {
		t.Fatalf("the refused follow-up must leave no timer: %+v", ts)
	}
	release()
}

// A person cancels a follow-up from the list of wakes, as a member's wake.
func TestAPersonCancelsAFollowUp(t *testing.T) {
	p := &runScript{reply: "Still at work.", callName: "talkoot_wake", callArgs: wakeArgs}
	w, seat, clock, _ := limitsCrew(t, p, true)
	approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the first report", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })
	open := w.talkootWakesView("crew").Open
	if len(open) != 1 {
		t.Fatalf("want the one follow-up, got %+v", open)
	}
	if err := w.talkootWakeCancel(t.Context(), "crew", "sothr", open[0].ID); err != nil {
		t.Fatal(err)
	}
	fire(t, w, clock, 5*time.Minute)
	if n := len(runRoots(t)); n != 1 {
		t.Fatalf("a cancelled follow-up started a run, got %d roots", n)
	}
	if ts := wakeTimers(w); len(ts) != 0 {
		t.Fatalf("a cancelled follow-up holds no timer: %+v", ts)
	}
}

// A run gets talkoot_wake only while the person's configuration turns wakes
// on, and only while its routine's card allows a follow-up.
func TestARunGetsNoWakeToolWithoutTheSwitchOrTheAllowance(t *testing.T) {
	none := ciTask
	none.FollowUps = new(0)
	for name, c := range map[string]struct {
		wakes bool
		ask   talkoot.RoutineAsk
		want  bool
	}{
		"wakes on, and the card allows 2":    {true, ciTask, true},
		"wakes off, and the card allows 2":   {false, ciTask, false},
		"wakes on, and the card allows none": {true, none, false},
	} {
		t.Run(name, func(t *testing.T) {
			p := &runScript{reply: "Done."}
			w, seat, clock, _ := limitsCrew(t, p, c.wakes)
			approved(t, w, seat, c.ask)
			fire(t, w, clock, 16*time.Minute)
			waitTalkoot(t, "the report", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })
			p.mu.Lock()
			offered := slices.Clone(p.offered)
			p.mu.Unlock()
			if got := slices.Contains(offered, "talkoot_wake"); got != c.want {
				t.Fatalf("talkoot_wake in the run's tools = %v, want %v: %v", got, c.want, offered)
			}
		})
	}
}

// 🚨 The timer service forgets a one-time timer when its handler returns,
// and a run starts after that. A daemon that stopped in between would lose
// the follow-up. So the follow-up holds a timer until the room holds its
// run, and the timer goes after.
func TestAFollowUpKeepsItsTimerUntilItsRunIsInTheRoom(t *testing.T) {
	defer func(fn func()) { runPrepared = fn }(runPrepared)
	p := &runScript{reply: "Still at work.", callName: "talkoot_wake", callArgs: wakeArgs}
	w, seat, clock, _ := limitsCrew(t, p, true)
	approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the first report", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })
	id := wakeTimers(w)[0].ID
	during := -1
	var next time.Time
	runPrepared = func() {
		// The handler has returned, and the run's root is not written yet.
		ts := wakeTimers(w)
		during = len(ts)
		if len(ts) == 1 && ts[0].ID == id {
			next = ts[0].Schedule.At
		}
	}
	fireWake(t, w, clock)
	waitTalkoot(t, "the second report", func() bool { return len(runEnds(t, talkoot.KindReport)) == 2 })
	if during != 1 || !next.After(clock.Now()) {
		t.Fatalf("while its run starts, the follow-up must hold a timer for a later try: %d timers, next %v", during, next)
	}
	settleRuns(t, w)
	for _, ti := range wakeTimers(w) {
		if ti.ID == id {
			t.Fatalf("the follow-up's timer must go when the room holds its run: %+v", ti)
		}
	}
}

// runRead reports whether one request of a run held every one of wants.
func runRead(p *runScript, wants ...string) bool {
	for _, body := range p.runs() {
		all := true
		for _, want := range wants {
			if !strings.Contains(body, want) {
				all = false
			}
		}
		if all {
			return true
		}
	}
	return false
}
