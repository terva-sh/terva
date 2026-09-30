package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/testsupport"
)

// fakeWorkers is a workerHost that records what the talkoot asks of it, and
// lets a test end a worker's turn.
type fakeWorkers struct {
	mu      sync.Mutex
	spawned []swarm.SpawnRequest
	ids     []string
	sent    []string // id|text
	stopped []string
	resumed []string
	adopted []string
	// resumedIn holds the dir of each revival, in order.
	resumedIn []string
	// stopKeepsLive, when set, leaves a stopped worker live, as a worker
	// that is slow to exit is.
	stopKeepsLive bool
	live          map[string]bool
	reqs          map[string]swarm.SpawnRequest
	events        map[string]workerEvents
	totals        map[string]float64
	// onSpawn and onResume, when set, run inside a spawn or a revival, as a
	// roster update could.
	onSpawn  func()
	onResume func()
	// onSend, when set, runs inside a send, as a revived worker's slow inbox
	// does.
	onSend func()
	// onStop, when set, runs inside a stop, as a worker slow to exit makes
	// it.
	onStop func()
	// sendErr fails every send, and stopFails fails that many stops.
	sendErr   error
	stopFails int
	// draining lists workers whose stopped process has not exited yet, and
	// exitChecks each exited call, in order.
	draining   map[string]bool
	exitChecks []string
	// onExited, when set, runs inside an exited wait.
	onExited func()
}

func newFakeWorkers() *fakeWorkers {
	return &fakeWorkers{live: map[string]bool{}, reqs: map[string]swarm.SpawnRequest{}, events: map[string]workerEvents{}, totals: map[string]float64{}}
}

func (f *fakeWorkers) spawn(_ context.Context, req swarm.SpawnRequest, ev workerEvents) (string, error) {
	f.mu.Lock()
	id := fmt.Sprintf("agent-%d", len(f.ids)+1)
	f.spawned = append(f.spawned, req)
	f.ids = append(f.ids, id)
	f.live[id] = true
	f.reqs[id] = req
	f.events[id] = ev
	hook := f.onSpawn
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return id, nil
}

func (f *fakeWorkers) state(id string) (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	live, ok := f.live[id]
	return ok, live
}

func (f *fakeWorkers) resume(_ context.Context, id, dir string, ev workerEvents) error {
	f.mu.Lock()
	f.resumed = append(f.resumed, id)
	f.resumedIn = append(f.resumedIn, dir)
	f.live[id] = true
	f.events[id] = ev
	f.totals[id] = 0
	hook := f.onResume
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func (f *fakeWorkers) adopt(id string, ev workerEvents) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adopted = append(f.adopted, id)
	f.events[id] = ev
	return f.totals[id], nil
}

func (f *fakeWorkers) agentSnapshot(id string) (swarm.AgentSnapshot, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.live[id]; !ok {
		return swarm.AgentSnapshot{}, false
	}
	return swarm.AgentSnapshot{ID: id, Status: swarm.StatusRunning, Activity: "working", Tail: "user: " + f.lastSentLocked(id), CostUSD: f.totals[id]}, true
}

// lastSentLocked is the last text sent to worker id. The caller holds f.mu.
func (f *fakeWorkers) lastSentLocked(id string) string {
	for i := len(f.sent) - 1; i >= 0; i-- {
		if who, text, _ := strings.Cut(f.sent[i], "|"); who == id {
			return text
		}
	}
	return ""
}

func (f *fakeWorkers) holds(id string) (swarm.SpawnRequest, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req, ok := f.reqs[id]
	return req, ok
}

func (f *fakeWorkers) send(id, text string) error {
	f.mu.Lock()
	f.sent = append(f.sent, id+"|"+text)
	hook, err := f.onSend, f.sendErr
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

func (f *fakeWorkers) stop(id string) error {
	f.mu.Lock()
	hook := f.onStop
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopFails > 0 {
		f.stopFails--
		return errors.New("the worker did not stop")
	}
	f.stopped = append(f.stopped, id)
	if !f.stopKeepsLive {
		f.live[id] = false
	}
	return nil
}

func (f *fakeWorkers) exited(id string, _ time.Duration) bool {
	f.mu.Lock()
	f.exitChecks = append(f.exitChecks, id)
	hook := f.onExited
	f.onExited = nil
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.draining[id]
}

func (f *fakeWorkers) ev(t *testing.T, id string) workerEvents {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	ev, ok := f.events[id]
	if !ok {
		t.Fatalf("no events are installed for %s", id)
	}
	return ev
}

// end ends the worker's turn with a running total and a reply.
func (f *fakeWorkers) end(t *testing.T, id string, total float64, reply string) {
	t.Helper()
	f.mu.Lock()
	f.totals[id] = total
	f.mu.Unlock()
	// A typical turn echoed the one text that started it.
	f.ev(t, id).turnEnd(id, total, reply, 1)
}

// exit reports that the worker's process ended.
func (f *fakeWorkers) exit(t *testing.T, id string) {
	t.Helper()
	f.mu.Lock()
	f.live[id] = false
	total := f.totals[id]
	f.mu.Unlock()
	f.ev(t, id).exit(id, total)
}

func (f *fakeWorkers) snapshot() (spawned []swarm.SpawnRequest, sent, stopped, resumed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]swarm.SpawnRequest{}, f.spawned...), append([]string{}, f.sent...), append([]string{}, f.stopped...), append([]string{}, f.resumed...)
}

// workerHome is talkootHome with the external_workers gate set as given.
func workerHome(t *testing.T, external bool) string {
	t.Helper()
	cwd := talkootHome(t)
	cfg := fmt.Sprintf(`{"talkoot_enabled": true, "external_workers_enabled": %t}`, external)
	if err := os.WriteFile(filepath.Join(os.Getenv("TERVA_HOME"), "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return cwd
}

func workerCrew(home, jev string) []byte {
	return []byte("---\nname: crew\nhome: " + home + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n  - id: jev\n    role: specialist\n    driver: claude\n" + jev + "---\n")
}

func jevWorking(t *testing.T, w *Workspace) bool {
	t.Helper()
	return memberView(t, w, "crew", "jev").Status.Working
}

// A worker member's first delivery spawns its worker with the roster's
// posture and tools, and records the worker on the member's seat. A later
// delivery is the same worker's next turn. Each turn reports its own cost,
// and the worker's reply reaches the room as a note to the coordinator.
func TestAWorkerMemberSpawnsOnceAndTakesEachLaterTurn(t *testing.T) {
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", workerCrew(cwd, "    posture: ask\n    tools: [read, grep]\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Review the schema.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	spawned, _, _, _ := fw.snapshot()
	req := spawned[0]
	if req.Backend != "claude" || req.Approval != "ask" || strings.Join(req.Tools, ",") != "read,grep" || !req.SharedTree || !strings.Contains(req.Task, "Review the schema.") {
		t.Errorf("spawn request = %+v", req)
	}
	if req.Model != "" {
		t.Errorf("a worker with no model in the roster must take its own default, got %q", req.Model)
	}
	// The fake records the spawn before the binding writes the seat line.
	var seats []talkoot.Line
	waitTalkoot(t, "the seat line", func() bool {
		seats = roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineSeat && l.Member == "jev" })
		return len(seats) > 0
	})
	if len(seats) != 1 || seats[0].Ref != "agent-1" {
		t.Errorf("seat lines = %+v, want one naming agent-1", seats)
	}
	if !jevWorking(t, w) {
		t.Error("the member must work until its turn ends")
	}

	fw.end(t, "agent-1", 0.5, "The schema looks sound.")
	waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })
	notes := roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Envelope != nil && l.Envelope.From == "jev" && l.Envelope.Kind == talkoot.KindNote
	})
	if len(notes) != 1 || notes[0].Envelope.Body != "The schema looks sound." || notes[0].Envelope.To[0] != "helm" {
		t.Errorf("reply notes = %+v", notes)
	}

	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "And the indexes?", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the second turn", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == 1 })
	spawned, sent, _, _ := fw.snapshot()
	if len(spawned) != 1 || !strings.HasPrefix(sent[0], "agent-1|") || !strings.Contains(sent[0], "And the indexes?") {
		t.Errorf("the second delivery must reach the same worker: spawned %d, sent %v", len(spawned), sent)
	}
	fw.end(t, "agent-1", 0.8, "")
	waitTalkoot(t, "the second turn end", func() bool { return !jevWorking(t, w) })
	if got := memberView(t, w, "crew", "jev").Status.SpendUSD; got < 0.79 || got > 0.81 {
		t.Errorf("spend = %v, want 0.5 and then the 0.3 difference", got)
	}
}

// Without the user-layer gate a worker member takes no turn, and its
// introduction is a plain card.
func TestAWorkerMemberNeedsTheExternalWorkersGate(t *testing.T) {
	cwd := workerHome(t, false)
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	if _, err := w.talkootCreate(t.Context(), "crew", workerCrew(cwd, "")); err != nil {
		t.Fatal(err)
	}
	m, _ := memberOf(*w.talkoot.runs["crew"].roster.Load(), "jev")
	if err := w.memberUnbound(m); err == nil || !strings.Contains(err.Error(), "external_workers") {
		t.Errorf("unbound = %v, want the gate named", err)
	}
	if err := w.deliverWorker(w.talkoot.runs["crew"], m, "x"); err == nil {
		t.Error("a delivery ran without the gate")
	}
	if s, _, _, _ := fw.snapshot(); len(s) != 0 {
		t.Errorf("spawned %d workers without the gate", len(s))
	}
}

// An update that leaves the worker's spawn inputs alone keeps its worker. One
// that changes its posture stops the old worker, and the next delivery
// spawns a new one.
func TestARosterUpdateKeepsOrRetiresTheWorker(t *testing.T) {
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", workerCrew(cwd, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })

	edit := func(jev string) {
		t.Helper()
		if _, err := w.talkootUpdate(ctx, "crew", "sothr", workerCrew(cwd, jev)); err != nil {
			t.Fatal(err)
		}
	}
	edit("    title: Reviewer\n")
	if _, _, stopped, _ := fw.snapshot(); len(stopped) != 0 {
		t.Errorf("a title change stopped the worker: %v", stopped)
	}
	edit("    title: Reviewer\n    posture: ask\n")
	if _, _, stopped, _ := fw.snapshot(); len(stopped) != 1 || stopped[0] != "agent-1" {
		t.Errorf("a posture change must stop the old worker, stopped %v", stopped)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Again.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the new spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 2 })
	if spawned, _, _, _ := fw.snapshot(); spawned[1].Approval != "ask" {
		t.Errorf("the new worker takes the new posture, got %q", spawned[1].Approval)
	}
}

// After a restart the member's seat names its worker. The worker's process is
// gone, so the next delivery revives it, and does not spawn a second one.
func TestAWorkerMemberIsRevivedAfterARestart(t *testing.T) {
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", workerCrew(cwd, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	fw.end(t, "agent-1", 0.2, "")
	waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// The daemon stopped, and the worker's process with it.
	fw.mu.Lock()
	fw.live["agent-1"] = false
	fw.mu.Unlock()

	w2 := openTalkootWorkspace(t, cwd)
	w2.talkootWorkers = fw
	w2.LoadTalkoots()
	if _, err := w2.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Continue.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the revival", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == 1 })
	spawned, sent, _, resumed := fw.snapshot()
	if len(spawned) != 1 || len(resumed) != 1 || resumed[0] != "agent-1" || !strings.HasPrefix(sent[0], "agent-1|") {
		t.Errorf("spawned %d, resumed %v, sent %v; want agent-1 revived and sent to", len(spawned), resumed, sent)
	}
	// A revived worker counts its cost from zero again.
	fw.end(t, "agent-1", 0.1, "")
	waitTalkoot(t, "the revived turn end", func() bool { return !jevWorking(t, w2) })
	if got := memberView(t, w2, "crew", "jev").Status.SpendUSD; got < 0.29 || got > 0.31 {
		t.Errorf("spend = %v, want 0.2 and then the revived worker's 0.1", got)
	}
}

// A worker that exits in a turn leaves the turn unreported, so its member
// stops working and pauses for a person. A later report from that worker
// finds no turn, and counts nothing.
func TestAWorkerThatExitsInATurnFreesItsMember(t *testing.T) {
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", workerCrew(cwd, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	fw.mu.Lock()
	fw.totals["agent-1"] = 0.15
	fw.mu.Unlock()
	fw.exit(t, "agent-1")
	waitTalkoot(t, "the member to stop working", func() bool { return !jevWorking(t, w) })
	v := memberView(t, w, "crew", "jev")
	if !strings.Contains(v.Status.Paused, "stopped before its turn ended") {
		t.Errorf("paused %q, want the member paused for a person", v.Status.Paused)
	}
	// The turn is charged what the worker spent before it stopped.
	if v.Status.SpendUSD < 0.14 || v.Status.SpendUSD > 0.16 {
		t.Errorf("spend = %v, want the 0.15 the worker reported", v.Status.SpendUSD)
	}
	turns := v.Status.Turns
	fw.end(t, "agent-1", 0.4, "late")
	if got := memberView(t, w, "crew", "jev").Status; got.Turns != turns || got.SpendUSD != v.Status.SpendUSD {
		t.Errorf("a stale report counted: turns %d -> %d, spend %v -> %v", turns, got.Turns, v.Status.SpendUSD, got.SpendUSD)
	}
}

// A roster update that commits while a worker starts, and changes what it
// starts with, retires the new worker at once: the update could not see it.
func TestAWorkerStartedAcrossAnUpdateIsRetired(t *testing.T) {
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", workerCrew(cwd, "")); err != nil {
		t.Fatal(err)
	}
	run := w.talkoot.runs["crew"]
	fw.onSpawn = func() {
		// An update that moves the member from plan to ask commits.
		next, err := talkoot.Parse(workerCrew(cwd, "    posture: ask\n"), "crew/"+talkoot.FileName)
		if err != nil {
			t.Error(err)
			return
		}
		next.ID = "crew"
		run.roster.Store(&next)
	}
	m, _ := memberOf(*run.roster.Load(), "jev")
	err := w.deliverWorker(run, m, "Start.")
	if err == nil || !strings.Contains(err.Error(), "roster changed") {
		t.Fatalf("delivery = %v, want the retirement named", err)
	}
	if _, _, stopped, _ := fw.snapshot(); len(stopped) != 1 || stopped[0] != "agent-1" {
		t.Errorf("stopped %v, want the worker that started under the old roster", stopped)
	}
	w.talkoot.mu.Lock()
	seat := run.seats["jev"]
	w.talkoot.mu.Unlock()
	if seat != "" {
		t.Errorf("the retired worker holds the seat: %q", seat)
	}
}

// A binding works from the roster as it is now, not from a member the router
// handed over before an update committed.
func TestAWorkerSpawnsWithTheCurrentRoster(t *testing.T) {
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	if _, err := w.talkootCreate(t.Context(), "crew", workerCrew(cwd, "    posture: ask\n")); err != nil {
		t.Fatal(err)
	}
	run := w.talkoot.runs["crew"]
	stale, _ := memberOf(*run.roster.Load(), "jev")
	stale.Posture = "auto-edit"
	if err := w.deliverWorker(run, stale, "Start."); err != nil {
		t.Fatal(err)
	}
	if spawned, _, _, _ := fw.snapshot(); len(spawned) != 1 || spawned[0].Approval != "ask" {
		t.Errorf("spawned %+v, want the roster's posture", spawned)
	}
}

// A seat can outlive a retirement whose unbind line failed. The worker it
// names revives only with the limits the roster gives now, and otherwise the
// member gets a new worker.
func TestARevivalRefusesAWorkerWithOlderLimits(t *testing.T) {
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", workerCrew(cwd, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })
	// The worker's process is gone, and it holds a wider posture than the
	// roster now gives.
	fw.mu.Lock()
	fw.live["agent-1"] = false
	req := fw.reqs["agent-1"]
	req.Approval = "auto-edit"
	fw.reqs["agent-1"] = req
	fw.mu.Unlock()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Again.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "a new worker", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 2 })
	if _, _, _, resumed := fw.snapshot(); len(resumed) != 0 {
		t.Errorf("revived %v, a worker with older limits", resumed)
	}
}

// A late exit from a worker's earlier process does not close a turn of the
// process that revived it.
func TestALateExitDoesNotCloseARevivedTurn(t *testing.T) {
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", workerCrew(cwd, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	first := fw.ev(t, "agent-1")
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })
	fw.mu.Lock()
	fw.live["agent-1"] = false
	fw.mu.Unlock()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Again.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the revival", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == 1 })
	first.exit("agent-1", 0)
	if !jevWorking(t, w) {
		t.Error("the first process's exit closed the revived process's turn")
	}
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the revived turn end", func() bool { return !jevWorking(t, w) })
}

// A talkoot loaded again while the swarm kept its worker adopts the live
// worker, and the worker's reports then reach the new run.
func TestALiveWorkerIsAdoptedByANewRun(t *testing.T) {
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", workerCrew(cwd, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	fw.end(t, "agent-1", 0.4, "")
	waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w2 := openTalkootWorkspace(t, cwd)
	w2.talkootWorkers = fw
	w2.LoadTalkoots()
	if _, err := w2.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Continue.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the send", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == 1 })
	fw.mu.Lock()
	adopted := append([]string{}, fw.adopted...)
	fw.mu.Unlock()
	if len(adopted) != 1 || adopted[0] != "agent-1" {
		t.Errorf("adopted %v, want the live worker", adopted)
	}
	fw.end(t, "agent-1", 0.5, "")
	waitTalkoot(t, "the adopted turn end", func() bool { return !jevWorking(t, w2) })
	if got := memberView(t, w2, "crew", "jev").Status.SpendUSD; got < 0.49 || got > 0.51 {
		t.Errorf("spend = %v, want 0.4 and then the adopted worker's 0.1", got)
	}
}

// A live worker whose limits differ from the roster's takes no turn. It stops,
// and the member gets a new worker.
func TestALiveWorkerWithOlderLimitsIsReplaced(t *testing.T) {
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", workerCrew(cwd, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })
	fw.mu.Lock()
	req := fw.reqs["agent-1"]
	req.Approval = "auto-edit"
	fw.reqs["agent-1"] = req
	fw.mu.Unlock()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Again.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "a new worker", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 2 })
	_, sent, stopped, _ := fw.snapshot()
	if len(sent) != 0 || !slices.Contains(stopped, "agent-1") {
		t.Errorf("sent %v and stopped %v, want the older worker stopped and sent nothing", sent, stopped)
	}
}

// A worker that a binding revived, and that an update retired while it
// started, stops again rather than running with no seat.
func TestARevivedWorkerThatLostItsSeatStops(t *testing.T) {
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", workerCrew(cwd, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })
	run := w.talkoot.runs["crew"]
	fw.mu.Lock()
	fw.live["agent-1"] = false
	fw.onResume = func() {
		w.talkoot.mu.Lock()
		delete(run.seats, "jev")
		w.talkoot.mu.Unlock()
	}
	fw.mu.Unlock()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Again.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the stop", func() bool { _, _, stopped, _ := fw.snapshot(); return slices.Contains(stopped, "agent-1") })
	if _, sent, _, _ := fw.snapshot(); len(sent) != 0 {
		t.Errorf("sent %v to a worker with no seat", sent)
	}
}

// A delivery routed before the member became native spawns no worker.
func TestADeliveryToAMemberNowNativeSpawnsNothing(t *testing.T) {
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	roster := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n  - id: jev\n    role: specialist\n---\n"
	if _, err := w.talkootCreate(t.Context(), "crew", []byte(roster)); err != nil {
		t.Fatal(err)
	}
	run := w.talkoot.runs["crew"]
	stale, _ := memberOf(*run.roster.Load(), "jev")
	stale.Driver = "claude"
	if err := w.deliverWorker(run, stale, "Start."); err == nil {
		t.Error("a delivery to a member now native succeeded")
	}
	if s, _, _, _ := fw.snapshot(); len(s) != 0 {
		t.Errorf("spawned %d workers for a native member", len(s))
	}
}

// A worker spawned from a roster request, stopped, and reloaded by a real
// swarm still compares equal to the same request. The swarm trims each field
// at the spawn, so the request here carries a padded persona. An unchanged member
// resumes its conversation rather than getting a new worker.
func TestAReloadedWorkerHoldsItsSpawnRequest(t *testing.T) {
	root := testsupport.TempDir(t)
	cfg := swarm.Config{
		Root: root, RepoRoot: root,
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			return swarm.RunnerFunc(func(ctx context.Context, _ swarm.Sink) error { <-ctx.Done(); return ctx.Err() })
		},
	}
	m := talkoot.Member{ID: "jev", Driver: "claude", Posture: "ask", Persona: "vartija", Tools: []string{}}
	req, err := (&Workspace{}).workerRequest(&talkootRun{id: "crew"}, m, "Start.")
	if err != nil {
		t.Fatal(err)
	}
	req.Persona = " vartija "
	f := swarm.New(cfg)
	id, err := swarmWorkers{f: f}.spawn(context.Background(), req, workerEvents{turnEnd: func(string, float64, string, int) {}, exit: func(string, float64) {}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Stop(id); err != nil {
		t.Fatal(err)
	}
	f.Get(id).Wait()
	g := swarm.New(swarm.Config{Root: root, RepoRoot: root})
	if loaded, errs := g.Reload(); loaded != 1 || len(errs) > 0 {
		t.Fatalf("reload loaded=%d errs=%v", loaded, errs)
	}
	held, ok := swarmWorkers{f: g}.holds(id)
	if !ok || !sameRequest(held, req) {
		t.Errorf("held %+v, want it equal to %+v", held, req)
	}
}

// A worker the roster retires mid-turn ends the member's turn and charges
// what it spent. The member does not wait for a person.
func TestARetiredWorkerEndsItsTurnWithoutAPause(t *testing.T) {
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", workerCrew(cwd, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	waitTalkoot(t, "the seat", func() bool {
		w.talkoot.mu.Lock()
		defer w.talkoot.mu.Unlock()
		return w.talkoot.runs["crew"].seats["jev"] == "agent-1"
	})
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", workerCrew(cwd, "    posture: ask\n")); err != nil {
		t.Fatal(err)
	}
	fw.mu.Lock()
	fw.totals["agent-1"] = 0.2
	fw.mu.Unlock()
	fw.exit(t, "agent-1")
	waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })
	st := memberView(t, w, "crew", "jev").Status
	if st.Paused != "" || st.SpendUSD < 0.19 || st.SpendUSD > 0.21 {
		t.Errorf("status = %+v, want no pause and the 0.2 spent", st)
	}
}

// A failed turn tells the room it failed, not an earlier turn's answer.
func TestAFailedTurnReportsTheFailure(t *testing.T) {
	if got := turnReply("an earlier answer", "context window exceeded"); got != "The turn failed: context window exceeded" {
		t.Errorf("reply = %q", got)
	}
	if got := turnReply("the answer", ""); got != "the answer" {
		t.Errorf("reply = %q", got)
	}
}
