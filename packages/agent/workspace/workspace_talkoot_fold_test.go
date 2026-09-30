package workspace

import (
	"encoding/json"
	"slices"
	"testing"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/worker"
)

// Turn ends report in the order the event path saw them, each with its own
// count and error, whichever callback runs first.
func TestTurnTextsReportsEachTurnWhole(t *testing.T) {
	var c turnTexts
	user := swarm.NewEvent("user_message", nil)
	c.see(user, 2)
	c.see(swarm.NewEvent("assistant_message", nil), 0)
	c.see(swarm.NewEvent("task_end", map[string]any{"error": "the first failed"}), 0)
	c.see(swarm.NewEvent("turn_end", nil), 0) // a core turn end, which ends no task turn
	c.see(swarm.NewEvent("task_end", nil), 0)
	type got struct {
		texts  int
		errMsg string
	}
	var reports []got
	rec := func(texts int, errMsg string) { reports = append(reports, got{texts, errMsg}) }
	// The second turn's callback runs first, with its own empty error.
	c.report("", rec)
	c.report("the first failed", rec)
	// A callback with nothing queued still reports, with its own error.
	c.report("unseen", rec)
	want := []got{{2, "the first failed"}, {0, ""}, {0, "unseen"}}
	if !slices.Equal(reports, want) {
		t.Errorf("reports %v, want %v", reports, want)
	}
}

// A folded echo, as claude writes it, counts one text for each text it
// carries, through the translator and the hook's own reading.
func TestAClaudeFoldedEchoCountsEachText(t *testing.T) {
	b, err := worker.Lookup(worker.BackendClaude)
	if err != nil {
		t.Fatal(err)
	}
	var c turnTexts
	for _, line := range []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[talkoot crew] B"},{"type":"text","text":"[talkoot crew] C"}]},"isReplay":true}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}`,
		`{"type":"result","subtype":"success","num_turns":1,"total_cost_usd":0.01}`,
	} {
		for _, e := range b.Translate([]byte(line)) {
			ev := swarm.NewEvent(e.Type, e.Data)
			c.see(ev, len(workerUserTexts(ev)))
		}
	}
	var texts int
	c.report("", func(n int, _ string) { texts = n })
	if texts != 2 {
		t.Errorf("the folded turn counted %d texts, want 2", texts)
	}
	// A folding worker pays one owed text for each echoed block, so each
	// text the runner writes, the opening included, must be one block.
	frame, err := b.Steer("[talkoot crew] D")
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Message struct {
			Content []map[string]any `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(frame, &sent); err != nil {
		t.Fatal(err)
	}
	if n := len(sent.Message.Content); n != 1 || sent.Message.Content[0]["type"] != "text" {
		t.Errorf("a claude send carries %d blocks %v, want one text block", n, sent.Message.Content)
	}
}

// A claude worker folds texts that queued behind a turn into one turn. The
// turn pays for each text it took, so the worker goes idle and stops after
// its last turn, and not never.
func TestAFoldedClaudeTurnPaysForEachText(t *testing.T) {
	w, fw, _ := idleCrew(t, "")
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "A.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	for i, body := range []string{"B.", "C."} {
		if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, body, nil, ""); err != nil {
			t.Fatal(err)
		}
		waitTalkoot(t, "the send", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == i+1 })
	}
	ev := fw.ev(t, "agent-1")
	ev.turnEnd("agent-1", 0.1, "", 1) // A
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Lock()
	owed := run.workerOwed["jev"]
	w.talkoot.mu.Unlock()
	if owed != 2 {
		t.Fatalf("after A the worker owes %d turns, want 2 for B and C", owed)
	}
	ev.turnEnd("agent-1", 0.3, "", 2) // B and C, folded
	waitTalkoot(t, "the idle stop", func() bool { return slices.Contains(stoppedWorkers(fw), "agent-1") })
}

// A terva worker ends a turn for each text, so a turn that echoed two texts,
// such as a prompt and a nudge inside its turn, still pays for one.
func TestATervaTurnPaysForOneText(t *testing.T) {
	w, fw := readsCrew(t, "terva")
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "A.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "B.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the send", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == 1 })
	fw.ev(t, "agent-1").turnEnd("agent-1", 0.1, "", 2) // A
	if !jevWorking(t, w) {
		t.Fatal("a terva turn that echoed two texts paid for B as well")
	}
}

// A turn on a folding backend that echoed no text pays for none, and is
// still charged.
func TestAFoldedTurnWithNoEchoPaysForNone(t *testing.T) {
	w, fw, _ := idleCrew(t, "")
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "A.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "B.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the send", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == 1 })
	ev := fw.ev(t, "agent-1")
	ev.turnEnd("agent-1", 0.1, "", 1) // A
	ev.turnEnd("agent-1", 0.2, "", 0) // a turn that took no text
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Lock()
	owed := run.workerOwed["jev"]
	w.talkoot.mu.Unlock()
	if owed != 1 {
		t.Fatalf("the worker owes %d turns, want 1 for B", owed)
	}
	if got := memberView(t, w, "crew", "jev").Status.SpendUSD; got < 0.19 || got > 0.21 {
		t.Errorf("spend = %v, want the 0.2 both turns spent", got)
	}
}
