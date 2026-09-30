package workspace

import (
	"slices"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/talkoot"
)

// readsCrew starts the crew talkoot with jev on driver, on fake workers.
func readsCrew(t *testing.T, driver string) (*Workspace, *fakeWorkers) {
	t.Helper()
	cwd := workerHome(t, true)
	w := openTalkootWorkspace(t, cwd)
	fw := newFakeWorkers()
	w.talkootWorkers = fw
	crew := strings.Replace(string(workerCrew(cwd, "")), "driver: claude", "driver: "+driver, 1)
	if _, err := w.talkootCreate(t.Context(), "crew", []byte(crew)); err != nil {
		t.Fatal(err)
	}
	return w, fw
}

// lastSent returns the text of the last send to the worker.
func lastSent(t *testing.T, fw *fakeWorkers) string {
	t.Helper()
	_, sent, _, _ := fw.snapshot()
	if len(sent) == 0 {
		t.Fatal("nothing was sent")
	}
	_, text, _ := strings.Cut(sent[len(sent)-1], "|")
	return text
}

// settleReads waits until every read the crew's run queued before it has
// applied, so a test can assert that a read did not happen.
func settleReads(t *testing.T, w *Workspace) {
	t.Helper()
	w.talkoot.mu.Lock()
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Unlock()
	done := make(chan struct{})
	run.later(func() { close(done) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run's read queue did not drain")
	}
}

// A worker on a backend that reports reads takes an envelope's chain when its
// turn reads the text, not when it accepts it. The spawn reads at once,
// because its text is the worker's first turn.
func TestATervaWorkerReadsWhenItsTurnDoes(t *testing.T) {
	w, fw := readsCrew(t, "terva")
	ctx := t.Context()
	first, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	if _, ok := readLine(t, "jev", first.ID); !ok {
		t.Fatal("the spawn's envelope was not read")
	}
	second, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Also this.", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the send", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == 1 })
	if _, ok := readLine(t, "jev", second.ID); ok {
		t.Fatal("the worker read an envelope its turn had not reached")
	}
	fw.ev(t, "agent-1").userTurn(lastSent(t, fw))
	waitTalkoot(t, "the read", func() bool { _, ok := readLine(t, "jev", second.ID); return ok })
}

// A claude worker reads when its turn does, as a terva one does. Texts that
// queued behind a turn fold into one echo, and each of them is read, in the
// order they were sent.
func TestAClaudeWorkerReadsEachTextOfAFoldedTurn(t *testing.T) {
	w, fw := readsCrew(t, "claude")
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	var posts []talkoot.Envelope
	for i, body := range []string{"Also this.", "And this."} {
		e, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, body, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		posts = append(posts, e)
		waitTalkoot(t, "the send", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == i+1 })
	}
	_, sent, _, _ := fw.snapshot()
	settleReads(t, w)
	for _, e := range posts {
		if _, ok := readLine(t, "jev", e.ID); ok {
			t.Fatal("a claude worker read an envelope its turn had not reached")
		}
	}
	// The echo claude writes for a folded turn: one replayed user message
	// with a text block for each text, through the worker's own hook.
	blocks := make([]any, 0, len(sent))
	for _, s := range sent {
		_, text, _ := strings.Cut(s, "|")
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	hooks, _ := swarmWorkers{}.hook(fw.ev(t, "agent-1"))
	hooks.OnEvent(swarm.NewEvent("user_message", map[string]any{
		"message": map[string]any{"role": "user", "content": blocks},
		"replay":  true,
	}))
	for _, e := range posts {
		waitTalkoot(t, "the read", func() bool { _, ok := readLine(t, "jev", e.ID); return ok })
	}
	var order []string
	for _, l := range roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineRead && l.Member == "jev" }) {
		order = append(order, l.Ref)
	}
	if want := []string{posts[0].ID, posts[1].ID}; !slices.Equal(order[len(order)-2:], want) {
		t.Errorf("read %v, want the folded texts read in the order sent, %v", order, want)
	}
}

// A text the worker never reads before its process exits stays unread: the
// member keeps its chain, and a later report of the text reads nothing.
func TestAnExitDropsTheReadsItOwed(t *testing.T) {
	w, fw := readsCrew(t, "terva")
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	second, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Also this.", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the send", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == 1 })
	text := lastSent(t, fw)
	ev := fw.ev(t, "agent-1")
	fw.exit(t, "agent-1")
	waitTalkoot(t, "the exit", func() bool {
		w.talkoot.mu.Lock()
		defer w.talkoot.mu.Unlock()
		return len(w.talkoot.runs["crew"].workerReads["jev"]) == 0
	})
	ev.userTurn(text)
	settleReads(t, w)
	if _, ok := readLine(t, "jev", second.ID); ok {
		t.Error("a text the exited process never read was read")
	}
}

// A user_message event's text blocks reach the worker's userTurn, and no
// other event's do.
func TestTheWorkerHookReportsUserTurns(t *testing.T) {
	var got []string
	hooks, _ := swarmWorkers{}.hook(workerEvents{userTurn: func(text string) { got = append(got, text) }})
	user := func(blocks ...map[string]any) swarm.Event {
		content := make([]any, len(blocks))
		for i, b := range blocks {
			content[i] = b
		}
		return swarm.NewEvent("user_message", map[string]any{"message": map[string]any{"role": "user", "content": content}})
	}
	hooks.OnEvent(user(map[string]any{"type": "text", "text": "[talkoot crew] hello"}))
	hooks.OnEvent(user(map[string]any{"type": "tool_result", "tool_use_id": "t1"}))
	marker := user(map[string]any{"type": "text", "text": "[Request interrupted by user]"})
	marker.Data["replay"] = false
	hooks.OnEvent(marker)
	folded := user(map[string]any{"type": "text", "text": "[talkoot crew] B"}, map[string]any{"type": "text", "text": "[talkoot crew] C"})
	folded.Data["replay"] = true
	hooks.OnEvent(folded)
	hooks.OnEvent(swarm.NewEvent("assistant_message", map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "reply"}}}}))
	if want := []string{"[talkoot crew] hello", "[talkoot crew] B", "[talkoot crew] C"}; !slices.Equal(got, want) {
		t.Errorf("user turns %q, want %q", got, want)
	}
}

// A late event from a process the member replaced reads nothing of the new
// process's deliveries, and the new process's own event reads it.
func TestAnOldProcessReadsNothingOfTheNew(t *testing.T) {
	w, fw := readsCrew(t, "terva")
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	old := fw.ev(t, "agent-1")
	fw.exit(t, "agent-1")
	waitTalkoot(t, "the member to stop working", func() bool { return !jevWorking(t, w) })
	// The exit pauses the member for a person, who resumes it.
	if err := w.talkootResume(ctx, "crew", "sothr", "jev", ""); err != nil {
		t.Fatal(err)
	}
	next, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Again.", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the revival", func() bool { _, sent, _, r := fw.snapshot(); return len(r) == 1 && len(sent) == 1 })
	text := lastSent(t, fw)
	old.userTurn(text)
	settleReads(t, w)
	if _, ok := readLine(t, "jev", next.ID); ok {
		t.Fatal("the replaced process read the new process's delivery")
	}
	fw.ev(t, "agent-1").userTurn(text)
	waitTalkoot(t, "the read", func() bool { _, ok := readLine(t, "jev", next.ID); return ok })
}

// A read of an older delivery that lands after a newer read moves the member
// nowhere.
func TestAnOlderReadAfterANewerOneReadsNothing(t *testing.T) {
	w, fw := readsCrew(t, "terva")
	ctx := t.Context()
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Start.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the spawn", func() bool { s, _, _, _ := fw.snapshot(); return len(s) == 1 })
	second, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Also this.", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the send", func() bool { _, sent, _, _ := fw.snapshot(); return len(sent) == 1 })
	// What a newer delivery's read leaves behind.
	w.talkoot.mu.Lock()
	w.talkoot.runs["crew"].lastRead["jev"] = 1 << 40
	w.talkoot.mu.Unlock()
	fw.ev(t, "agent-1").userTurn(lastSent(t, fw))
	settleReads(t, w)
	if _, ok := readLine(t, "jev", second.ID); ok {
		t.Error("an older delivery was read after a newer one")
	}
}

// A failed send forgets its own read, not another delivery's with the same
// text, as a replayed envelope has.
func TestAFailedSendDropsItsOwnRead(t *testing.T) {
	w, _ := readsCrew(t, "terva")
	w.talkoot.mu.Lock()
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Unlock()
	first := w.addWorkerRead(run, "jev", "same text", talkoot.Receipt{})
	second := w.addWorkerRead(run, "jev", "same text", talkoot.Receipt{})
	w.dropWorkerRead(run, "jev", second)
	w.talkoot.mu.Lock()
	left := run.workerReads["jev"]
	w.talkoot.mu.Unlock()
	if len(left) != 1 || left[0].seq != first {
		t.Errorf("left %+v, want only the first delivery's read", left)
	}
}

// A new process clears the reads its predecessor never made.
func TestANewProcessClearsTheOldReads(t *testing.T) {
	w, _ := readsCrew(t, "terva")
	w.talkoot.mu.Lock()
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Unlock()
	w.addWorkerRead(run, "jev", "text", talkoot.Receipt{})
	w.nextWorkerRun(run, "jev")
	w.talkoot.mu.Lock()
	left := len(run.workerReads["jev"])
	w.talkoot.mu.Unlock()
	if left != 0 {
		t.Errorf("%d reads of the old process are still pending", left)
	}
}
