package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core/permission"
)

// seatedWorker creates the crew talkoot and binds jev's worker as agent-1.
func seatedWorker(t *testing.T) (*Workspace, *fakeWorkers) {
	t.Helper()
	return seatedWorkerWith(t, "")
}

// seatedWorkerWith is seatedWorker with more of jev's roster fields, as YAML.
func seatedWorkerWith(t *testing.T, jev string) (*Workspace, *fakeWorkers) {
	t.Helper()
	return seatedWorkerConfig(t, jev, "")
}

// seatedWorkerConfig is seatedWorkerWith under a user config of cfg, which the
// workspace reads as it opens. An empty cfg keeps workerHome's.
func seatedWorkerConfig(t *testing.T, jev, cfg string) (*Workspace, *fakeWorkers) {
	t.Helper()
	cwd := workerHome(t, true)
	if cfg != "" {
		if err := os.WriteFile(filepath.Join(os.Getenv("TERVA_HOME"), "config.json"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", workerCrew(cwd, jev)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the seat", func() bool {
		w.talkoot.mu.Lock()
		defer w.talkoot.mu.Unlock()
		return w.talkoot.runs["crew"].seats["jev"] == "agent-1"
	})
	return w, fw
}

func crewWorker(id string) *swarm.Agent {
	return &swarm.Agent{ID: id, SessionID: ctrlproto.TalkootAddr("crew")}
}

// permissionCards returns the talkoot's open permission cards.
func permissionCards(t *testing.T, w *Workspace) []ctrlproto.TalkootCard {
	t.Helper()
	cards, err := w.talkootInbox(t.Context(), "crew")
	if err != nil {
		t.Fatal(err)
	}
	var out []ctrlproto.TalkootCard
	for _, c := range cards {
		if c.Kind == ctrlproto.TalkootCardPermission {
			out = append(out, c)
		}
	}
	return out
}

// A worker member spawns with the talkoot's address as its session, so its
// asks have an approver.
func TestAWorkerMemberSpawnsUnderTheTalkoot(t *testing.T) {
	_, fw := seatedWorker(t)
	spawned, _, _, _ := fw.snapshot()
	if got := spawned[0].SessionID; got != ctrlproto.TalkootAddr("crew") {
		t.Errorf("session = %q, want the talkoot's address", got)
	}
}

// A worker member's ask opens a card in the talkoot's inbox under the member,
// and an approval on the talkoot's address answers it.
func TestAWorkerAskReachesTheTalkootInbox(t *testing.T) {
	w, _ := seatedWorker(t)
	c := w.workerApprover(crewWorker("agent-1"))
	if c == nil {
		t.Fatal("a talkoot worker has no approver")
	}
	got := make(chan permission.ConfirmDecision, 1)
	go func() { got <- c.Confirm(t.Context(), "bash", "rm -rf build") }()
	var card ctrlproto.TalkootCard
	waitTalkoot(t, "the card", func() bool {
		cards := permissionCards(t, w)
		if len(cards) == 1 {
			card = cards[0]
		}
		return len(cards) == 1
	})
	if card.Session != ctrlproto.TalkootAddr("crew") || card.Member != "jev" || card.Permission == nil ||
		card.Permission.Tool != "bash" || card.Permission.Agent != "agent-1" {
		t.Fatalf("card = %+v", card)
	}
	if err := w.Approve(t.Context(), card.Session, card.ID, permission.ConfirmDecision{Allow: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-got:
		if !d.Allow {
			t.Errorf("decision = %+v, want the approval", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the approval did not reach the worker")
	}
	if cards := permissionCards(t, w); len(cards) != 0 {
		t.Errorf("an answered card stays open: %+v", cards)
	}
}

// A worker with no seat in the talkoot has nobody to ask, so its ask is
// refused at once and opens no card.
func TestAnUnseatedWorkerAskIsRefused(t *testing.T) {
	w, _ := seatedWorker(t)
	// A refusal is at once. The deadline only ends a wrong wait.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	d := w.workerApprover(crewWorker("agent-9")).Confirm(ctx, "bash", "ls")
	if d.Allow || ctx.Err() != nil {
		t.Errorf("decision = %+v after %v, want a refusal at once", d, ctx.Err())
	}
	if cards := permissionCards(t, w); len(cards) != 0 {
		t.Errorf("an unseated worker opened cards: %+v", cards)
	}
	other := &swarm.Agent{ID: "agent-1", SessionID: ctrlproto.TalkootAddr("gone")}
	if d := w.workerApprover(other).Confirm(ctx, "bash", "ls"); d.Allow || ctx.Err() != nil {
		t.Error("a worker of a talkoot not running here was allowed")
	}
}

// A new worker can ask before its binding records the seat. The ask waits
// for the binding rather than finding no seat.
func TestAnAskDuringTheBindingWaitsForTheSeat(t *testing.T) {
	w, _ := seatedWorker(t)
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Lock()
	delete(run.seats, "jev")
	w.talkoot.mu.Unlock()
	run.workerMu.Lock()
	got := make(chan permission.ConfirmDecision, 1)
	go func() { got <- w.workerApprover(crewWorker("agent-1")).Confirm(t.Context(), "bash", "ls") }()
	select {
	case d := <-got:
		run.workerMu.Unlock()
		t.Fatalf("the ask did not wait for the binding: %+v", d)
	case <-time.After(100 * time.Millisecond):
	}
	w.talkoot.mu.Lock()
	run.seats["jev"] = "agent-1"
	w.talkoot.mu.Unlock()
	run.workerMu.Unlock()
	waitTalkoot(t, "the card", func() bool { return len(permissionCards(t, w)) == 1 })
	card := permissionCards(t, w)[0]
	if err := w.Approve(t.Context(), card.Session, card.ID, permission.ConfirmDecision{Allow: false, Reason: "no"}); err != nil {
		t.Fatal(err)
	}
	if d := <-got; d.Allow {
		t.Errorf("decision = %+v, want the refusal", d)
	}
}

// A worker that stops while its ask waits takes a refusal, and its card
// closes.
func TestAStoppedWorkerAskCloses(t *testing.T) {
	w, _ := seatedWorker(t)
	ctx, cancel := context.WithCancel(t.Context())
	got := make(chan permission.ConfirmDecision, 1)
	go func() { got <- w.workerApprover(crewWorker("agent-1")).Confirm(ctx, "bash", "ls") }()
	waitTalkoot(t, "the card", func() bool { return len(permissionCards(t, w)) == 1 })
	cancel()
	if d := <-got; d.Allow {
		t.Errorf("decision = %+v, want a refusal", d)
	}
	if cards := permissionCards(t, w); len(cards) != 0 {
		t.Errorf("a stopped worker's card stays open: %+v", cards)
	}
}

// A worker from before the talkoot was its session cannot ask anyone. The
// member gets a new worker rather than reviving it.
func TestAWorkerWithNoTalkootSessionIsReplaced(t *testing.T) {
	w, fw := seatedWorker(t)
	fw.end(t, "agent-1", 0, "")
	waitTalkoot(t, "the turn end", func() bool { return !jevWorking(t, w) })
	fw.mu.Lock()
	fw.live["agent-1"] = false
	req := fw.reqs["agent-1"]
	req.SessionID = ""
	fw.reqs["agent-1"] = req
	fw.mu.Unlock()
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "Again.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "a new worker", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 2 })
	if _, _, _, resumed := fw.snapshot(); len(resumed) != 0 {
		t.Errorf("revived %v, a worker with no approver", resumed)
	}
}

// The carrier is not a session. Nothing that reads the session map finds it.
func TestTheCarrierIsNoSession(t *testing.T) {
	w, _ := seatedWorker(t)
	if w.existing(ctrlproto.TalkootAddr("crew")) != nil {
		t.Error("the carrier is in the session map")
	}
	if err := w.Approve(t.Context(), ctrlproto.TalkootAddr("nowhere"), "worker-x-1", permission.ConfirmDecision{Allow: true}); err != nil {
		t.Errorf("an approval for a talkoot not running here = %v", err)
	}
}

// A worker that stops while its ask waits for a binding takes a refusal at
// once, and does not wait for the binding to end.
func TestAnAskWaitingForABindingEndsWithTheWorker(t *testing.T) {
	w, _ := seatedWorker(t)
	run := w.talkoot.runs["crew"]
	run.workerMu.Lock()
	defer run.workerMu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	got := make(chan permission.ConfirmDecision, 1)
	go func() { got <- w.workerApprover(crewWorker("agent-1")).Confirm(ctx, "bash", "ls") }()
	cancel()
	select {
	case d := <-got:
		if d.Allow {
			t.Errorf("decision = %+v, want a refusal", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the ask waited for the binding after its worker stopped")
	}
}
