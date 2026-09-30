package workspace

import (
	"errors"
	"testing"
)

// slotCrew starts the crew with jev on driver, delivers A, which spawns the
// worker, and then B, which joins A's turn.
func slotCrew(t *testing.T, driver string) (*Workspace, *fakeWorkers) {
	t.Helper()
	w, fw := readsCrew(t, driver)
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "A.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "B.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the joined send", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == 1 })
	return w, fw
}

// On a backend that ends a turn for each text, a member keeps its working
// slot until the turn of the last text it was sent ends, and each turn is
// charged.
func TestATervaWorkerHoldsItsSlotUntilItsQueuedTurnsEnd(t *testing.T) {
	w, fw := slotCrew(t, "terva")
	fw.end(t, "agent-1", 0.1, "") // A
	if !jevWorking(t, w) {
		t.Fatal("the member freed its working slot while the worker still ran B")
	}
	fw.end(t, "agent-1", 0.25, "") // B
	if jevWorking(t, w) {
		t.Fatal("the member still works after the turn of its last text ended")
	}
	if got := memberView(t, w, "crew", "jev").Status.SpendUSD; got < 0.24 || got > 0.26 {
		t.Errorf("spend = %v, want the 0.25 both turns spent", got)
	}
}

// On a backend that may fold queued texts into one turn, the slot frees at
// the first turn end, as before, so a fold cannot hold it for good.
func TestAClaudeWorkerFreesItsSlotAtItsFirstTurnEnd(t *testing.T) {
	w, fw := slotCrew(t, "claude")
	fw.end(t, "agent-1", 0.1, "")
	if jevWorking(t, w) {
		t.Fatal("a claude member held its working slot past its first turn end")
	}
}

// A send that fails while a turn runs leaves that turn open, so the turn's
// own end frees the slot. It used to close the turn, and the end that
// followed then freed nothing.
func TestAFailedJoinedSendLeavesTheTurnOpen(t *testing.T) {
	for _, driver := range []string{"terva", "claude"} {
		t.Run(driver, func(t *testing.T) {
			w, fw := readsCrew(t, driver)
			ctx := t.Context()
			if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "A.", nil, ""); err != nil {
				t.Fatal(err)
			}
			waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
			fw.mu.Lock()
			fw.sendErr = errors.New("the worker's stdin closed")
			fw.mu.Unlock()
			_, _ = w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "B.", nil, "")
			fw.mu.Lock()
			fw.sendErr = nil
			fw.mu.Unlock()
			if !jevWorking(t, w) {
				t.Fatal("a failed joined send ended the turn the worker still runs")
			}
			fw.end(t, "agent-1", 0.1, "") // A
			if jevWorking(t, w) {
				t.Fatal("the running turn's end did not free the member's slot")
			}
		})
	}
}

// A joined send that fails after the turn it joined has ended leaves no turn
// to free the slot, so the failure frees it, and writes no turn of its own.
func TestAFailedSendAfterTheTurnItJoinedEndedFreesTheSlot(t *testing.T) {
	w, fw := readsCrew(t, "terva")
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "A.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	run := w.talkoot.runs["crew"]
	ev := fw.ev(t, "agent-1")
	fw.mu.Lock()
	fw.sendErr = errors.New("the worker's stdin closed")
	fw.onSend = func() {
		// A's turn ends while B's send runs. Its router call waits for this
		// delivery, and its own count lands first.
		go ev.turnEnd("agent-1", 0.1, "", 0)
		waitTalkoot(t, "A's turn end", func() bool {
			w.talkoot.mu.Lock()
			defer w.talkoot.mu.Unlock()
			return run.workerOwed["jev"] == 1
		})
	}
	fw.mu.Unlock()
	_, _ = w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "B.", nil, "")
	fw.mu.Lock()
	fw.sendErr, fw.onSend = nil, nil
	fw.mu.Unlock()
	waitTalkoot(t, "the slot to free", func() bool { return !jevWorking(t, w) })
	settleReads(t, w)
	if turns := memberView(t, w, "crew", "jev").Status.Turns; turns != 1 {
		t.Errorf("turns = %d, want the one turn the worker ran", turns)
	}
}

// A send that opens a turn and fails closes that turn, although an older
// queued turn is still owed. The router counts no turn for it, and the queued
// turn's end is charged as a queued turn.
func TestAFailedSendThatOpenedATurnClosesIt(t *testing.T) {
	w, fw := slotCrew(t, "claude")
	fw.end(t, "agent-1", 0.1, "") // A, which frees claude's slot
	if jevWorking(t, w) {
		t.Fatal("claude held its slot past A")
	}
	fw.mu.Lock()
	fw.sendErr = errors.New("the worker's stdin closed")
	fw.mu.Unlock()
	_, _ = w.talkootPost(t.Context(), "crew", "sothr", []string{"jev"}, "C.", nil, "")
	fw.mu.Lock()
	fw.sendErr = nil
	fw.mu.Unlock()
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Lock()
	open := run.workerTurn["jev"] != 0
	w.talkoot.mu.Unlock()
	if open {
		t.Fatal("the failed send left open the turn it opened")
	}
	fw.end(t, "agent-1", 0.25, "") // B
	if s := memberView(t, w, "crew", "jev").Status; s.Working || s.Turns != 2 {
		t.Errorf("working %v with %d turns, want the slot free and the two turns the worker ran", s.Working, s.Turns)
	}
}
