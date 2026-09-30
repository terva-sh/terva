package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/talkoot"
)

// idleCrew starts the crew talkoot on fake workers, with every idle stop
// shortened to a few milliseconds.
func idleCrew(t *testing.T, jev string) (*Workspace, *fakeWorkers, string) {
	t.Helper()
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	w.talkootIdleClock = func(time.Duration) time.Duration { return 30 * time.Millisecond }
	if _, err := w.talkootCreate(t.Context(), "crew", workerCrew(cwd, jev)); err != nil {
		t.Fatal(err)
	}
	return w, fw, cwd
}

func jevIdle(t *testing.T, w *Workspace) bool {
	t.Helper()
	return memberView(t, w, "crew", "jev").Status.Idle
}

// A worker that ends its turn stops after its idle stop. The member keeps its
// seat and shows that it stopped, and its next envelope revives the same
// worker.
func TestAnIdleWorkerStopsAndRevives(t *testing.T) {
	w, fw, _ := idleCrew(t, "")
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the idle stop", func() bool { _, _, stopped, _ := fw.snapshot(); return slices.Contains(stopped, "agent-1") })
	waitTalkoot(t, "the idle status", func() bool { return jevIdle(t, w) })
	if got := memberView(t, w, "crew", "jev").Session; got != "agent-1" {
		t.Errorf("seat = %q, want the stopped worker kept", got)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Again.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the revival", func() bool { _, sent, _, r := fw.snapshot(); return len(r) == 1 && len(sent) == 1 })
	if s, _, _, _ := fw.snapshot(); len(s) != 1 {
		t.Errorf("spawned %d workers, want the one revived", len(s))
	}
	if jevIdle(t, w) {
		t.Error("a revived worker still shows as stopped")
	}
}

// A worker with a turn open is not idle, whatever its timer says. A delivery
// ends the idle wait.
func TestAWorkerInATurnDoesNotStop(t *testing.T) {
	w, fw, _ := idleCrew(t, "")
	w.talkootIdleClock = func(time.Duration) time.Duration { return 150 * time.Millisecond }
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Again.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the send", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == 1 })
	time.Sleep(400 * time.Millisecond)
	if _, _, stopped, _ := fw.snapshot(); len(stopped) != 0 {
		t.Errorf("stopped %v with a turn open", stopped)
	}
}

// A timer that fired before a delivery, and then waited for the binding's
// lock, finds its generation old and the turn open, and stops nothing.
func TestAFiredTimerAfterADeliveryStopsNothing(t *testing.T) {
	w, fw, _ := idleCrew(t, "")
	w.talkootIdleClock = func(time.Duration) time.Duration { return time.Hour }
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Lock()
	armed, token := run.idleGen["jev"], run.workerRun["jev"]
	w.talkoot.mu.Unlock()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Again.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the send", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == 1 })
	w.talkoot.mu.Lock()
	now := run.idleGen["jev"]
	w.talkoot.mu.Unlock()
	w.idleStop(run, "jev", "agent-1", token, armed, time.Time{})
	if _, _, stopped, _ := fw.snapshot(); len(stopped) != 0 {
		t.Fatalf("a timer from before the delivery stopped %v", stopped)
	}
	// The current generation too: the open turn alone keeps the worker.
	w.idleStop(run, "jev", "agent-1", token, now, time.Time{})
	if _, _, stopped, _ := fw.snapshot(); len(stopped) != 0 {
		t.Fatalf("stopped %v with a turn open", stopped)
	}
	// Once the second turn ends, the first timer is still early: the member's
	// idle wait starts again at every turn end.
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the second turn end", func() bool { return !jevWorking(t, w) })
	w.idleStop(run, "jev", "agent-1", token, armed, time.Time{})
	if _, _, stopped, _ := fw.snapshot(); len(stopped) != 0 {
		t.Errorf("a timer from before the last turn stopped %v", stopped)
	}
}

// idle_stop off keeps the worker's process up.
func TestIdleStopOffKeepsTheWorker(t *testing.T) {
	w, fw, _ := idleCrew(t, "    idle_stop: off\n")
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	fw.end(t, "agent-1", 0, "")
	time.Sleep(200 * time.Millisecond)
	if _, _, stopped, _ := fw.snapshot(); len(stopped) != 0 || jevIdle(t, w) {
		t.Errorf("stopped %v with idle_stop off", stopped)
	}
}

// A revived worker's slow inbox holds no lock that the talkoot's other
// workers report through.
func TestASlowSendHoldsNoLock(t *testing.T) {
	w, fw, _ := idleCrew(t, "    idle_stop: off\n")
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	fw.mu.Lock()
	fw.onSend = func() {
		entered <- struct{}{}
		<-release
	}
	fw.mu.Unlock()
	// The post delivers on its own goroutine, and the send blocks there.
	posted := make(chan error, 1)
	defer func() {
		close(release)
		if err := <-posted; err != nil {
			t.Error(err)
		}
	}()
	go func() {
		_, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Again.", nil, "")
		posted <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the send never started")
	}
	run := w.talkoot.runs["crew"]
	if !run.workerMu.TryLock() {
		t.Fatal("a send in flight holds run.workerMu")
	}
	run.workerMu.Unlock()
}

// The retention sweep keeps a worker a talkoot's room seats now, and lets go
// of one the room unseated or never named.
func TestTheSweepKeepsASeatedWorker(t *testing.T) {
	cwd := workerHome(t, true)
	_ = openTalkootWorkspace(t, cwd)
	dir := filepath.Join(talkoot.Dir(), "crew")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	room := talkoot.OpenRoom(dir)
	for _, l := range []talkoot.Line{
		{Type: talkoot.LineSeat, At: time.Now(), Member: "jev", Ref: "agent-1"},
		{Type: talkoot.LineSeat, At: time.Now(), Member: "tess", Ref: "agent-2"},
		{Type: talkoot.LineSeat, At: time.Now(), Member: "tess"},
	} {
		if err := room.Append(l); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[string]bool{"agent-1": true, "agent-2": false, "agent-3": false} {
		a := &swarm.Agent{ID: id, SessionID: ctrlproto.TalkootAddr("crew")}
		if got := retainTalkootWorker(a); got != want {
			t.Errorf("retain %s = %v, want %v", id, got, want)
		}
	}
	if retainTalkootWorker(&swarm.Agent{ID: "agent-1", SessionID: "20260101-x"}) {
		t.Error("the sweep kept a session's worker")
	}
	if retainTalkootWorker(&swarm.Agent{ID: "agent-1", SessionID: ctrlproto.TalkootAddr("gone")}) {
		t.Error("the sweep kept a worker of a talkoot that does not exist")
	}
}

// A run's start gives back the worktree of a member the room seated that the
// roster no longer puts in a worktree: a release the last process did not
// finish.
func TestARunRetriesAnUnfinishedRelease(t *testing.T) {
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw, fl := newFakeWorkers(), &fakeLeases{}
	w.talkootWorkers, w.talkootLeases = fw, fl
	if _, err := w.talkootCreate(t.Context(), "crew", workerCrew(cwd, "")); err != nil {
		t.Fatal(err)
	}
	run := w.talkoot.runs["crew"]
	if err := run.room.Append(talkoot.Line{Type: talkoot.LineSeat, At: time.Now(), Member: "jev", Ref: "agent-7"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w2 := openTalkootWorkspace(t, cwd)
	w2.talkootWorkers, w2.talkootLeases = fw, fl
	w2.LoadTalkoots()
	waitTalkoot(t, "the release", func() bool { _, r := fl.snapshot(); return slices.Contains(r, "jev") })
	if _, r := fl.snapshot(); slices.Contains(r, "helm") {
		t.Errorf("released %v, a member the room never seated", r)
	}
}

// endedCrew is idleCrew with jev's first turn ended, and every idle stop set
// to d.
func endedCrew(t *testing.T, jev string, d time.Duration) (*Workspace, *fakeWorkers, string) {
	t.Helper()
	w, fw, cwd := idleCrew(t, jev)
	w.talkootIdleClock = func(time.Duration) time.Duration { return d }
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })
	return w, fw, cwd
}

func stoppedWorkers(fw *fakeWorkers) []string {
	_, _, stopped, _ := fw.snapshot()
	return stopped
}

// Two deliveries to one member reach its worker in the order they bound,
// though neither send holds run.workerMu.
func TestDeliveriesToAMemberKeepTheirOrder(t *testing.T) {
	w, fw, _ := endedCrew(t, "    idle_stop: off\n", time.Hour)
	ctx := t.Context()
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	fw.mu.Lock()
	first := true
	fw.onSend = func() {
		entered <- struct{}{}
		fw.mu.Lock()
		hold := first
		first = false
		fw.mu.Unlock()
		if hold {
			<-release
		}
	}
	fw.mu.Unlock()
	posted := make(chan error, 2)
	go func() {
		_, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "One.", nil, "")
		posted <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first send never started")
	}
	go func() {
		_, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Two.", nil, "")
		posted <- err
	}()
	time.Sleep(200 * time.Millisecond)
	_, sent, _, _ := fw.snapshot()
	close(release)
	if len(sent) != 1 {
		t.Fatalf("sent %v while the first send was in flight, want only the first", sent)
	}
	for range 2 {
		if err := <-posted; err != nil {
			t.Fatal(err)
		}
	}
	if _, sent, _, _ := fw.snapshot(); len(sent) != 2 || !strings.Contains(sent[0], "One.") {
		t.Errorf("sent %v, want One. first", sent)
	}
}

// A delivery that fails disarms the idle timer, and arms it again for the
// worker it left live and idle.
func TestAFailedDeliveryArmsTheIdleStopAgain(t *testing.T) {
	w, fw, _ := endedCrew(t, "", 150*time.Millisecond)
	fw.mu.Lock()
	fw.sendErr = errors.New("the inbox is closed")
	fw.mu.Unlock()
	_, _ = w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Again.", nil, "")
	waitTalkoot(t, "the idle stop", func() bool { return slices.Contains(stoppedWorkers(fw), "agent-1") })
}

// An idle stop that fails tries again.
func TestAFailedIdleStopTriesAgain(t *testing.T) {
	w, fw, _ := idleCrew(t, "")
	fw.mu.Lock()
	fw.stopFails = 1
	fw.mu.Unlock()
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the second idle stop", func() bool { return slices.Contains(stoppedWorkers(fw), "agent-1") })
}

// An update that turns idle_stop off keeps a worker whose timer is armed.
func TestTurningIdleStopOffKeepsAWaitingWorker(t *testing.T) {
	w, fw, cwd := endedCrew(t, "", 150*time.Millisecond)
	if _, err := w.talkootUpdate(t.Context(), "crew", "sothr", workerCrew(cwd, "    idle_stop: off\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if stopped := stoppedWorkers(fw); len(stopped) != 0 {
		t.Errorf("stopped %v after idle_stop turned off", stopped)
	}
}

// A run that closes stops its idle timers.
func TestAClosedRunStopsNoWorker(t *testing.T) {
	w, fw, _ := endedCrew(t, "", 150*time.Millisecond)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	before := stoppedWorkers(fw)
	time.Sleep(400 * time.Millisecond)
	if after := stoppedWorkers(fw); len(after) != len(before) {
		t.Errorf("stopped %v after the run closed", after[len(before):])
	}
}

// A worker still live when its talkoot loads again gets no idle timer, even
// once a delivery adopts it: it could still be in a turn of the run before,
// which no count here holds.
func TestACarriedWorkerIsNotIdleStopped(t *testing.T) {
	w, fw, cwd := endedCrew(t, "", time.Hour)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w2 := openTalkootWorkspace(t, cwd)
	w2.talkootWorkers = fw
	w2.talkootIdleClock = func(time.Duration) time.Duration { return 30 * time.Millisecond }
	w2.LoadTalkoots()
	// An update visits every waiting worker, and arms none of these.
	if _, err := w2.talkootUpdate(t.Context(), "crew", "sothr", workerCrew(cwd, "    idle_stop: 5m\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := w2.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Again.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the adoption", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == 1 })
	// The turn of the run before ends first, then the adopted one.
	fw.end(t, "agent-1", 0, "")
	fw.end(t, "agent-1", 0, "")
	time.Sleep(200 * time.Millisecond)
	if stopped := stoppedWorkers(fw); len(stopped) != 0 {
		t.Fatalf("stopped the adopted worker %v", stopped)
	}
}

// An update that turns idle_stop on arms the timer of a worker already
// waiting.
func TestTurningIdleStopOnArmsAWaitingWorker(t *testing.T) {
	w, fw, cwd := endedCrew(t, "    idle_stop: off\n", 30*time.Millisecond)
	if _, err := w.talkootUpdate(t.Context(), "crew", "sothr", workerCrew(cwd, "    idle_stop: 5m\n")); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the idle stop", func() bool { return slices.Contains(stoppedWorkers(fw), "agent-1") })
}

// A slow idle stop holds no lock that the talkoot's other workers report
// through.
func TestASlowIdleStopHoldsNoLock(t *testing.T) {
	w, fw, _ := endedCrew(t, "", time.Hour)
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Lock()
	a := run.idleArm["jev"]
	w.talkoot.mu.Unlock()
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	fw.mu.Lock()
	fw.onStop = func() {
		entered <- struct{}{}
		<-release
	}
	fw.mu.Unlock()
	done := make(chan struct{})
	go func() {
		w.idleStop(run, "jev", a.id, a.token, a.gen, time.Time{})
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the stop never started")
	}
	locked := run.workerMu.TryLock()
	if locked {
		run.workerMu.Unlock()
	}
	close(release)
	<-done
	if !locked {
		t.Fatal("a stop in flight holds run.workerMu")
	}
}

// A member that turns native shows no idle stop of the worker it had.
func TestANativeMemberShowsNoIdleStop(t *testing.T) {
	w, fw, cwd := endedCrew(t, "", 30*time.Millisecond)
	waitTalkoot(t, "the idle status", func() bool { return jevIdle(t, w) })
	_ = fw
	next := strings.Replace(string(workerCrew(cwd, "")), "    driver: claude\n", "", 1)
	if _, err := w.talkootUpdate(t.Context(), "crew", "sothr", []byte(next)); err != nil {
		t.Fatal(err)
	}
	if jevIdle(t, w) {
		t.Error("a native member still shows its old worker stopped while idle")
	}
}

// A run's start waits on no member that holds no worktree.
func TestARunReleasesNoWorktreeAMemberNeverHeld(t *testing.T) {
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw, fl := newFakeWorkers(), &fakeLeases{none: []string{"jev"}}
	w.talkootWorkers, w.talkootLeases = fw, fl
	if _, err := w.talkootCreate(t.Context(), "crew", workerCrew(cwd, "")); err != nil {
		t.Fatal(err)
	}
	if err := w.talkoot.runs["crew"].room.Append(talkoot.Line{Type: talkoot.LineSeat, At: time.Now(), Member: "jev", Ref: "agent-7"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w2 := openTalkootWorkspace(t, cwd)
	w2.talkootWorkers, w2.talkootLeases = fw, fl
	w2.LoadTalkoots()
	time.Sleep(200 * time.Millisecond)
	w2.talkoot.mu.Lock()
	holders := w2.talkoot.runs["crew"].leaseHolders["jev"]
	w2.talkoot.mu.Unlock()
	if _, r := fl.snapshot(); len(r) != 0 || len(holders) != 0 {
		t.Errorf("released %v and waits on %v for a member with no worktree", r, holders)
	}
}

// An arm that a delivery replaced is not restored by a reschedule that read
// it before the delivery.
func TestARescheduleRestoresNoReplacedArm(t *testing.T) {
	w, fw, _ := endedCrew(t, "", time.Hour)
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Lock()
	old := run.idleArm["jev"]
	w.talkoot.mu.Unlock()
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Again.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the send", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == 1 })
	w.armIdleStopAt(run, "jev", old.id, old.token, old.at, old.gen)
	w.talkoot.mu.Lock()
	_, armed := run.idleArm["jev"]
	w.talkoot.mu.Unlock()
	if armed {
		t.Error("a reschedule restored the arm a delivery replaced")
	}
}

// A delivery that arrives while an idle stop is under way waits for the stop,
// then revives the worker, so the stop never cuts the turn it opens.
func TestADeliveryDuringAnIdleStopRevives(t *testing.T) {
	w, fw, _ := endedCrew(t, "", time.Hour)
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Lock()
	a := run.idleArm["jev"]
	w.talkoot.mu.Unlock()
	posted := make(chan error, 1)
	fw.mu.Lock()
	fw.onStop = func() {
		fw.mu.Lock()
		fw.onStop = nil
		fw.mu.Unlock()
		go func() {
			_, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Again.", nil, "")
			posted <- err
		}()
		time.Sleep(150 * time.Millisecond)
	}
	fw.mu.Unlock()
	w.idleStop(run, "jev", a.id, a.token, a.gen, time.Time{})
	if err := <-posted; err != nil {
		t.Fatal(err)
	}
	if _, _, _, resumed := fw.snapshot(); len(resumed) != 1 {
		t.Errorf("revived %v, want the delivery to wait for the stop and revive the worker", resumed)
	}
}

// A stop reads idle_stop again once nothing can change it, so a roster that
// turned it off while the stop waited keeps the worker.
func TestAStopRereadsIdleStopUnderTheLock(t *testing.T) {
	w, fw, cwd := endedCrew(t, "", time.Hour)
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Lock()
	a := run.idleArm["jev"]
	w.talkoot.mu.Unlock()
	send := w.memberSendMu(run, "jev")
	send.Lock()
	done := make(chan struct{})
	go func() {
		w.idleStop(run, "jev", a.id, a.token, a.gen, time.Time{})
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	off, err := talkoot.Parse(workerCrew(cwd, "    idle_stop: off\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	run.roster.Store(&off)
	send.Unlock()
	<-done
	if stopped := stoppedWorkers(fw); len(stopped) != 0 {
		t.Errorf("stopped %v after idle_stop turned off", stopped)
	}
}

// A message sent while a turn is open runs as a turn of its own after it. The
// first turn end leaves the worker busy, and the last one leaves it idle.
func TestAJoinedMessageKeepsTheWorkerUntilItsTurnEnds(t *testing.T) {
	w, fw, _ := idleCrew(t, "")
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Also this.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the joined send", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == 1 })
	fw.end(t, "agent-1", 0.1, "")
	time.Sleep(200 * time.Millisecond)
	if stopped := stoppedWorkers(fw); len(stopped) != 0 {
		t.Fatalf("stopped %v while it ran the joined message", stopped)
	}
	fw.end(t, "agent-1", 0.25, "")
	waitTalkoot(t, "the idle stop", func() bool { return slices.Contains(stoppedWorkers(fw), "agent-1") })
	// The joined message's turn is charged before the stop, because a
	// revival counts the cost from zero again.
	if got := memberView(t, w, "crew", "jev").Status.SpendUSD; got < 0.24 || got > 0.26 {
		t.Errorf("spend = %v, want the 0.25 both turns spent", got)
	}
}

// A stop that fails arms no retry over an arm made while it ran.
func TestAFailedStopReplacesNoNewerArm(t *testing.T) {
	w, fw, _ := endedCrew(t, "", time.Hour)
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Lock()
	a := run.idleArm["jev"]
	w.talkoot.mu.Unlock()
	fw.mu.Lock()
	fw.stopFails = 1
	fw.onStop = func() {
		// What a newer arm does to the generation.
		w.talkoot.mu.Lock()
		disarmIdleStopLocked(run, "jev")
		w.talkoot.mu.Unlock()
	}
	fw.mu.Unlock()
	w.idleStop(run, "jev", a.id, a.token, a.gen, time.Time{})
	w.talkoot.mu.Lock()
	_, armed := run.idleArm["jev"]
	w.talkoot.mu.Unlock()
	if armed {
		t.Error("a failed stop armed a retry over a newer arm")
	}
}

// A message sent while a message that joined a turn still runs is owed a
// turn too, so the worker is idle only after all three turns end.
func TestAMessageBehindAJoinedOneIsOwedATurn(t *testing.T) {
	w, fw, _ := idleCrew(t, "")
	ctx := t.Context()
	post := func(body string, sends int) {
		t.Helper()
		if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, body, nil, ""); err != nil {
			t.Fatal(err)
		}
		waitTalkoot(t, "the send", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == sends })
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "A.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	post("B.", 1)
	fw.end(t, "agent-1", 0, "") // A
	post("C.", 2)
	fw.end(t, "agent-1", 0, "") // B
	time.Sleep(200 * time.Millisecond)
	if stopped := stoppedWorkers(fw); len(stopped) != 0 {
		t.Fatalf("stopped %v while it ran C", stopped)
	}
	fw.end(t, "agent-1", 0, "") // C
	waitTalkoot(t, "the idle stop", func() bool { return slices.Contains(stoppedWorkers(fw), "agent-1") })
}

// A worker that exits in a queued turn charges that turn what it spent, and
// its member pauses for a person.
func TestAnExitInAQueuedTurnIsChargedAndPauses(t *testing.T) {
	w, fw, _ := idleCrew(t, "")
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "A.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "B.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the joined send", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == 1 })
	fw.end(t, "agent-1", 0.1, "")
	fw.mu.Lock()
	fw.totals["agent-1"] = 0.3
	fw.mu.Unlock()
	fw.exit(t, "agent-1")
	waitTalkoot(t, "the pause", func() bool {
		return strings.Contains(memberView(t, w, "crew", "jev").Status.Paused, "stopped before its turn ended")
	})
	if got := memberView(t, w, "crew", "jev").Status.SpendUSD; got < 0.29 || got > 0.31 {
		t.Errorf("spend = %v, want the 0.3 the worker reported", got)
	}
}

// An arm checks that the worker is idle under the lock a delivery opens its
// turn under, so a turn end that a delivery overtook arms nothing.
func TestAnArmWithATurnOpenArmsNothing(t *testing.T) {
	w, fw, _ := endedCrew(t, "", time.Hour)
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Lock()
	a := run.idleArm["jev"]
	w.talkoot.mu.Unlock()
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Again.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the send", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == 1 })
	w.armIdleStop(run, "jev", a.id, a.token)
	w.talkoot.mu.Lock()
	_, armed := run.idleArm["jev"]
	w.talkoot.mu.Unlock()
	if armed {
		t.Error("a turn end armed the idle stop of a worker with a turn open")
	}
}

// A delivery right after an idle stop revives the worker only once the old
// process has exited, so two processes never share its inbox.
func TestARevivalWaitsForTheStoppedProcess(t *testing.T) {
	w, fw, _ := endedCrew(t, "", 30*time.Millisecond)
	waitTalkoot(t, "the idle stop", func() bool { return slices.Contains(stoppedWorkers(fw), "agent-1") })
	fw.mu.Lock()
	fw.draining = map[string]bool{"agent-1": true}
	fw.mu.Unlock()
	_, _ = w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Again.", nil, "")
	if _, _, _, resumed := fw.snapshot(); len(resumed) != 0 {
		t.Fatalf("revived %v while the old process still ran", resumed)
	}
	fw.mu.Lock()
	fw.draining = nil
	fw.mu.Unlock()
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Once more.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the revival", func() bool { _, _, _, r := fw.snapshot(); return len(r) == 1 })
}

// A revival that waits for the old process holds no lock that the talkoot's
// other workers report through.
func TestARevivalWaitHoldsNoLock(t *testing.T) {
	w, fw, _ := endedCrew(t, "", 30*time.Millisecond)
	waitTalkoot(t, "the idle stop", func() bool { return slices.Contains(stoppedWorkers(fw), "agent-1") })
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	fw.mu.Lock()
	fw.onExited = func() {
		entered <- struct{}{}
		<-release
	}
	fw.mu.Unlock()
	posted := make(chan error, 1)
	go func() {
		_, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Again.", nil, "")
		posted <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the revival never waited")
	}
	run := w.talkoot.runs["crew"]
	locked := run.workerMu.TryLock()
	if locked {
		run.workerMu.Unlock()
	}
	close(release)
	if err := <-posted; err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("a revival waiting for the old process holds run.workerMu")
	}
}
