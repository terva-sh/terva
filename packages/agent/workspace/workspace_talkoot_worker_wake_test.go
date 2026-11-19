package workspace

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
)

// workerWakeCrew is the crew talkoot with jev on a fake worker, a timer
// service on a clock that the test moves, and the wakes key as given. jev's
// worker holds its seat, and its first turn is still open. idle shortens the
// idle stop, and 0 keeps the default.
func workerWakeCrew(t *testing.T, jev, wakes string, idle time.Duration) (*Workspace, *fakeWorkers, *stepClock) {
	t.Helper()
	cwd := workerHome(t, true)
	cfg := `{"talkoot_enabled": true, "external_workers_enabled": true}`
	if wakes != "" {
		cfg = `{"talkoot_enabled": true, "external_workers_enabled": true, "talkoot_wakes": ` + wakes + `}`
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("TERVA_HOME"), "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	clock := &stepClock{now: time.Now()}
	w := openTalkootWorkspace(t, cwd)
	w.timers.clock = clock
	w.LoadTalkoots()
	w.LoadTimers()
	if w.Timers() == nil {
		t.Fatalf("the daemon runs no timer service: %q", w.timers.problem)
	}
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	if idle > 0 {
		w.talkootIdleClock = func(time.Duration) time.Duration { return idle }
	}
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", workerCrew(cwd, jev)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Ship the import, and watch its CI run.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the seat", func() bool {
		w.talkoot.mu.Lock()
		defer w.talkoot.mu.Unlock()
		return w.talkoot.runs["crew"].seats["jev"] == "agent-1"
	})
	settleReads(t, w)
	return w, fw, clock
}

const workerWake = `{"delay_minutes":10,"reason":"Check the CI run."}`

// wokenWith reports whether the worker got a delivery that holds reason.
func wokenWith(fw *fakeWorkers, reason string) bool {
	_, sent, _, _ := fw.snapshot()
	return slices.ContainsFunc(sent, func(s string) bool { return strings.Contains(s, reason) })
}

// 🚨 TKT-01M7PGWM8V, and decision 0023 as amended on 2026-11-19. A worker
// member sets a wake through the bridge, under the caps of a native member's
// wake. The room records it as that member's. The firing is a delivery to the
// worker, and its turn counts as a turn of the member.
func TestAWorkerSetsAWakeUnderTheSameCapsAndItIsDelivered(t *testing.T) {
	w, fw, clock := workerWakeCrew(t, "", `{"per_member": 1}`, 0)
	r := bridgeCall(t, w, "agent-1", "talkoot_wake", workerWake)
	if r.IsError || !strings.Contains(r.Text, "is set for") || !strings.Contains(r.Text, "1 of 1 open wakes") {
		t.Fatalf("reply = %+v, want the native tool's text", r)
	}
	ls := wakeLines(t)
	if len(ls) != 1 || ls[0].Action != talkoot.WakeSet || ls[0].Member != "jev" || ls[0].Text != "Check the CI run." {
		t.Fatalf("the room must record the wake as jev's: %+v", ls)
	}
	view, err := w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	if o := view.Wakes.Open; len(o) != 1 || o[0].Member != "jev" {
		t.Fatalf("the view must list the worker's wake: %+v", o)
	}
	// The cap of a native member bounds a worker too.
	if r := bridgeCall(t, w, "agent-1", "talkoot_wake", `{"delay_minutes":20,"reason":"Check the deploy."}`); !r.IsError || !strings.Contains(r.Text, "1 open wake(s), and the limit is 1") {
		t.Fatalf("a second wake above the cap: %+v", r)
	}
	if len(wakeLines(t)) != 1 || len(wakeTimers(w)) != 1 {
		t.Fatal("a refused wake must leave no line and no timer")
	}

	fw.end(t, "agent-1", 0, "The import is out.")
	waitTalkoot(t, "the end of jev's first turn", func() bool {
		m := memberView(t, w, "crew", "jev")
		return m.Status.Turns == 1 && !m.Status.Working
	})
	if wokenWith(fw, "Check the CI run.") {
		t.Fatal("the worker got the wake before its time")
	}
	clock.advance(11 * time.Minute)
	waitTalkoot(t, "the delivery of the wake", func() bool {
		w.Timers().Poll()
		return len(wakeEnvelopes(t)) == 1 && wokenWith(fw, "Check the CI run.")
	})
	e := wakeEnvelopes(t)[0].Envelope
	if e.From != "jev" || len(e.To) != 1 || e.To[0] != "jev" || e.ID != ls[0].Ref {
		t.Fatalf("the wake is an envelope from the member to itself, with the wake's id: %+v", e)
	}
	// The woken turn is a turn of the member, so the turn cap bounds it.
	fw.end(t, "agent-1", 0, "The CI run passed.")
	waitTalkoot(t, "the count of the woken turn", func() bool { return memberView(t, w, "crew", "jev").Status.Turns == 2 })
}

// While wakes are off the bridge still lists the tool, because the bridge
// reads no configuration. The daemon refuses the call and names the key.
func TestAWorkersWakeIsRefusedWhileWakesAreOff(t *testing.T) {
	w, _, _ := workerWakeCrew(t, "", "", 0)
	r := bridgeCall(t, w, "agent-1", "talkoot_wake", workerWake)
	if !r.IsError || !strings.Contains(r.Text, "wakes are off") || !strings.Contains(r.Text, "talkoot_wakes") {
		t.Fatalf("reply = %+v, want a refusal that names the key", r)
	}
	if len(wakeLines(t)) != 0 || len(wakeTimers(w)) != 0 {
		t.Fatal("a refused wake must leave no line and no timer")
	}
}

// A worker stops when it is idle, so most wakes come due for a worker with
// no process. The firing is a delivery, and a delivery starts the worker
// again.
func TestAWakeStartsAWorkerThatStoppedForIdleness(t *testing.T) {
	w, fw, clock := workerWakeCrew(t, "", `{"per_member": 1}`, 30*time.Millisecond)
	if r := bridgeCall(t, w, "agent-1", "talkoot_wake", workerWake); r.IsError {
		t.Fatalf("reply = %+v", r)
	}
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the idle stop", func() bool { return slices.Contains(stoppedWorkers(fw), "agent-1") && jevIdle(t, w) })
	if _, _, _, resumed := fw.snapshot(); len(resumed) != 0 {
		t.Fatalf("the positive control: the worker started again before the wake: %v", resumed)
	}
	clock.advance(11 * time.Minute)
	waitTalkoot(t, "the revival by the wake", func() bool {
		w.Timers().Poll()
		_, _, _, resumed := fw.snapshot()
		return slices.Contains(resumed, "agent-1") && wokenWith(fw, "Check the CI run.")
	})
	if spawned, _, _, _ := fw.snapshot(); len(spawned) != 1 {
		t.Errorf("spawned %d workers, want the one that the wake revived", len(spawned))
	}
}

// The turn cap is what bounds a worker that reports no cost. A wake that
// comes due at the cap waits in the room, and the worker gets nothing.
func TestAWorkersWakeWaitsAtTheTurnCap(t *testing.T) {
	w, fw, clock := workerWakeCrew(t, "    turns_per_day: 1\n", `{"per_member": 1}`, 0)
	if r := bridgeCall(t, w, "agent-1", "talkoot_wake", workerWake); r.IsError {
		t.Fatalf("reply = %+v", r)
	}
	fw.end(t, "agent-1", 0, "The import is out.")
	waitTalkoot(t, "the end of jev's one turn", func() bool {
		m := memberView(t, w, "crew", "jev")
		return m.Status.Turns == 1 && !m.Status.Working
	})
	clock.advance(11 * time.Minute)
	waitTalkoot(t, "the wake's firing", func() bool {
		w.Timers().Poll()
		return len(wakeEnvelopes(t)) == 1 && len(wakeTimers(w)) == 0
	})
	settleReads(t, w)
	view, err := w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(view.Held, "jev") {
		t.Fatalf("the wake must wait for a member at its turn cap: held = %v", view.Held)
	}
	if wokenWith(fw, "Check the CI run.") {
		t.Fatal("a worker at its turn cap got a turn from a wake")
	}
	// The rail lists it as a wake that waits, so a person can cancel it.
	if o := view.Wakes.Open; len(o) != 1 || o[0].Member != "jev" || !o[0].Held {
		t.Fatalf("the list must hold the worker's wake as one that waits: %+v", o)
	}
}

// The seat can move between the lookup and the router. A wake checks the
// seat again inside the router call, and sets nothing.
func TestAReseatedWorkerWakeIsRefused(t *testing.T) {
	w, _, _ := workerWakeCrew(t, "", `{"per_member": 1}`, 0)
	run, member, ok := w.talkootWorker(t.Context(), "crew", "agent-1", nil)
	if !ok {
		t.Fatal("agent-1 holds no seat")
	}
	seat := workerSeat{ctx: t.Context(), w: w, run: run, member: member, agentID: "agent-1"}
	w.talkoot.mu.Lock()
	run.seats["jev"] = "agent-2"
	w.talkoot.mu.Unlock()
	if _, err := seat.Wake(10*time.Minute, "Check the CI run."); err != errWorkerUnseated {
		t.Fatalf("a wake after the seat moved: %v, want the unseated refusal", err)
	}
	if len(wakeLines(t)) != 0 || len(wakeTimers(w)) != 0 {
		t.Fatal("a refused wake must leave no line and no timer")
	}
}

// 🚨 The wake is no seat tool, so a roster's tools list narrows it. A native
// session loses the tool when its tools are built. A worker's call must meet
// the same list, or a person who took the wake from a member by its list
// would still get wakes from it (the security review of #1751).
func TestARostersToolsListNarrowsAWorkersWake(t *testing.T) {
	w, _, _ := workerWakeCrew(t, "    tools: [read, grep]\n", `{"per_member": 1}`, 0)
	r := bridgeCall(t, w, "agent-1", "talkoot_wake", workerWake)
	if !r.IsError || !strings.Contains(r.Text, "the roster's tools list for jev does not name talkoot_wake") {
		t.Fatalf("reply = %+v, want a refusal that names the roster's list", r)
	}
	if len(wakeLines(t)) != 0 || len(wakeTimers(w)) != 0 {
		t.Fatal("a refused wake must leave no line and no timer")
	}
	// A seat tool is not narrowed, by the same list.
	if r := bridgeCall(t, w, "agent-1", "talkoot_roster", `{}`); r.IsError {
		t.Fatalf("the positive control: a seat tool under the same list: %+v", r)
	}
}

// A list that names the wake keeps it.
func TestARostersToolsListThatNamesTheWakeKeepsIt(t *testing.T) {
	w, _, _ := workerWakeCrew(t, "    tools: [read, talkoot_wake]\n", `{"per_member": 1}`, 0)
	if r := bridgeCall(t, w, "agent-1", "talkoot_wake", workerWake); r.IsError {
		t.Fatalf("reply = %+v, want the wake set", r)
	}
	if len(wakeLines(t)) != 1 {
		t.Fatal("the wake must be in the room")
	}
}

// 🔑 The daemon is the one gate on a worker's call, and it holds the person's
// rules. A deny rule for talkoot_wake stops a worker member as it stops a
// native one. The docs tell a person to write that rule.
func TestAWorkersWakeMeetsThePersonsRules(t *testing.T) {
	w, _, _ := workerWakeCrew(t, "", `{"per_member": 1}`, 0)
	cfg := `{"talkoot_enabled": true, "external_workers_enabled": true, "talkoot_wakes": {"per_member": 1}, "permissions": [{"tool": "talkoot_wake", "decision": "deny", "reason": "no wakes for workers"}]}`
	if err := os.WriteFile(filepath.Join(os.Getenv("TERVA_HOME"), "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	r := bridgeCall(t, w, "agent-1", "talkoot_wake", workerWake)
	if !r.IsError || !strings.Contains(r.Text, "no wakes for workers") {
		t.Fatalf("reply = %+v, want the rule's refusal", r)
	}
	if len(wakeLines(t)) != 0 || len(wakeTimers(w)) != 0 {
		t.Fatal("a denied wake must leave no line and no timer")
	}
}
