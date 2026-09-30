package workspace

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/mcpbridge"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/permission"
)

// signalLines returns the crew's lines of type typ.
func signalLines(t *testing.T, typ string) []talkoot.Line {
	t.Helper()
	return roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == typ })
}

// drain waits until the run's queue has run everything queued before it.
func drain(t *testing.T, w *Workspace) {
	t.Helper()
	w.talkoot.mu.Lock()
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Unlock()
	done := make(chan struct{})
	run.later(func() { close(done) })
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the run's queue did not drain")
	}
}

// A native member's failed tool call writes a tool_error line with the tool's
// name. The line lands before the turn line of the turn it came from.
func TestANativeToolErrorIsARoomLine(t *testing.T) {
	_, _ = startInboxCrew(t, toolCallProvider("read", `{"path":"no-such-file.txt"}`), "auto-edit")
	waitTalkoot(t, "helm's turn line", func() bool {
		return slices.ContainsFunc(signalLines(t, talkoot.LineTurn), func(l talkoot.Line) bool { return l.Member == "helm" })
	})
	lines := roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Member == "helm" && (l.Type == talkoot.LineToolError || l.Type == talkoot.LineTurn)
	})
	if len(lines) < 2 || lines[0].Type != talkoot.LineToolError {
		t.Fatalf("helm's lines = %+v, want the tool error before the turn", lines)
	}
	if l := lines[0]; l.Tool != "read" || l.Reason == "" || l.Ref == "" {
		t.Fatalf("tool error line = %+v, want the read tool, its call, and its error", l)
	}
}

// A native member's provider retry writes a retry line, with its attempt and a
// redacted reason. A session with no seat, or one that lost it, writes none.
func TestANativeRetryIsARoomLine(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.PostTalkoot(ctx, ctrlproto.TalkootPostParams{ID: "crew", By: "sothr", To: []string{"helm"}, Body: "start"}); err != nil {
		t.Fatal(err)
	}
	var helm string
	waitTalkoot(t, "helm's session", func() bool { helm = memberView(t, w, "crew", "helm").Session; return helm != "" })
	var n nativeSignals
	n.observe(w, helm, core.EvRetry{Attempt: 2, Max: 6, Err: "overloaded\nplease wait"})
	n.observe(w, "no-such-session", core.EvRetry{Attempt: 1, Err: "a stranger"})
	got := signalLines(t, talkoot.LineRetry)
	if len(got) != 1 || got[0].Member != "helm" || got[0].Attempt != 2 || got[0].Reason != "overloaded please wait" {
		t.Fatalf("retry lines = %+v, want helm's one line on one line", got)
	}
	w.unseatTalkoot(helm)
	n.observe(w, helm, core.EvRetry{Attempt: 3, Err: "after the seat"})
	if got := signalLines(t, talkoot.LineRetry); len(got) != 1 {
		t.Fatalf("an unseated session wrote %+v", got[1:])
	}
}

// A question a person answers writes its card_open and card_close lines,
// with the ask id and the outcome.
func TestAQuestionCardIsARoomLine(t *testing.T) {
	w, room := startInboxCrew(t, toolCallProvider("ask_user_question", `{"question":"Which port?","options":["8080","9090"]}`), "auto-edit")
	card := *nextEvent(t, room, ctrlproto.EventTalkootInbox).Talkoot.Card
	if err := w.Answer(t.Context(), card.Session, card.ID, []core.UserAnswer{{Answer: "8080"}}); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the card_close line", func() bool { return len(signalLines(t, talkoot.LineCardClose)) == 1 })
	open := signalLines(t, talkoot.LineCardOpen)
	if len(open) != 1 || open[0].Member != "helm" || open[0].Card != talkoot.CardQuestion || open[0].Ref != card.ID {
		t.Fatalf("card_open lines = %+v", open)
	}
	if c := signalLines(t, talkoot.LineCardClose)[0]; c.Member != "helm" || c.Ref != card.ID || c.Outcome != talkoot.OutcomeAnswered {
		t.Fatalf("card_close line = %+v, want answered", c)
	}
}

// An approval a person refuses closes as denied, and its open line names the
// tool.
func TestADeniedApprovalIsARoomLine(t *testing.T) {
	w, room := startInboxCrew(t, toolCallProvider("bash", `{"command":"echo hi"}`), "ask")
	card := *nextEvent(t, room, ctrlproto.EventTalkootInbox).Talkoot.Card
	if err := w.Approve(t.Context(), card.Session, card.ID, permission.ConfirmDecision{Allow: false, Reason: "not now"}); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the card_close line", func() bool { return len(signalLines(t, talkoot.LineCardClose)) == 1 })
	if o := signalLines(t, talkoot.LineCardOpen); len(o) != 1 || o[0].Card != talkoot.CardPermission || o[0].Tool != "bash" || o[0].Ref != card.ID {
		t.Fatalf("card_open lines = %+v", o)
	}
	if c := signalLines(t, talkoot.LineCardClose)[0]; c.Outcome != talkoot.OutcomeDenied {
		t.Fatalf("card_close line = %+v, want denied", c)
	}
	// The gate returns the refusal as a failed result. The card_close line
	// records it, and no tool_error line counts it again.
	waitTalkoot(t, "helm's turn line", func() bool {
		return slices.ContainsFunc(signalLines(t, talkoot.LineTurn), func(l talkoot.Line) bool { return l.Member == "helm" })
	})
	if e := signalLines(t, talkoot.LineToolError); len(e) != 0 {
		t.Fatalf("a refusal wrote tool_error lines %+v", e)
	}
}

// A worker's question that nobody answers closes as expired.
func TestAnExpiredWorkerQuestionIsARoomLine(t *testing.T) {
	w, _ := seatedWorker(t)
	old := workerQuestionWait
	workerQuestionWait = 300 * time.Millisecond
	t.Cleanup(func() { workerQuestionWait = old })
	got := make(chan mcpbridge.TeamReply, 1)
	go func() {
		got <- w.workerTeam(crewWorker("agent-1"))(t.Context(), "ask_user_question", json.RawMessage(`{"question":"Anyone there?"}`))
	}()
	teamReply(t, got)
	waitTalkoot(t, "the card_close line", func() bool { return len(signalLines(t, talkoot.LineCardClose)) == 1 })
	if c := signalLines(t, talkoot.LineCardClose)[0]; c.Member != "jev" || c.Card != talkoot.CardQuestion || c.Outcome != talkoot.OutcomeExpired {
		t.Fatalf("card_close line = %+v, want jev's question expired", c)
	}
	if o := signalLines(t, talkoot.LineCardOpen); len(o) != 1 || o[0].Member != "jev" {
		t.Fatalf("card_open lines = %+v", o)
	}
}

// A worker member's tool error and retry write room lines. A process the
// member no longer runs writes none.
func TestAWorkerSignalIsARoomLine(t *testing.T) {
	w, fw := seatedWorker(t)
	ev := fw.ev(t, "agent-1")
	ev.signal(workerSignal{id: "c9", tool: "bash", why: "exit status 1"})
	ev.signal(workerSignal{attempt: 2, why: "rate_limit (HTTP 429)"})
	drain(t, w)
	if e := signalLines(t, talkoot.LineToolError); len(e) != 1 || e[0].Member != "jev" || e[0].Ref != "c9" || e[0].Tool != "bash" || e[0].Reason != "exit status 1" {
		t.Fatalf("tool error lines = %+v", e)
	}
	if r := signalLines(t, talkoot.LineRetry); len(r) != 1 || r[0].Member != "jev" || r[0].Attempt != 2 {
		t.Fatalf("retry lines = %+v", r)
	}
	w.talkoot.mu.Lock()
	run := w.talkoot.runs["crew"]
	w.talkoot.mu.Unlock()
	// A signal the member's process sent before a revival still lands,
	// though the queue writes it after.
	hold := make(chan struct{})
	run.later(func() { <-hold })
	ev.signal(workerSignal{id: "c10", tool: "edit", why: "before the revival"})
	w.nextWorkerRun(run, "jev")
	close(hold)
	drain(t, w)
	if e := signalLines(t, talkoot.LineToolError); len(e) != 2 || e[1].Ref != "c10" {
		t.Fatalf("a signal sent before the revival: tool error lines = %+v", e)
	}
	// A report from a process the member no longer runs writes nothing.
	ev.signal(workerSignal{id: "c11", tool: "read", why: "stale"})
	drain(t, w)
	if e := signalLines(t, talkoot.LineToolError); len(e) != 2 {
		t.Fatalf("a stale process wrote %+v", e[2:])
	}
}

// The signals come out of both worker shapes: a terva worker's tool_result
// and retry events, and a claude worker's tool results inside its messages.
// A terva result that also rides a message counts once.
func TestWorkerCallsReadsTheSignals(t *testing.T) {
	var c workerCalls
	see := func(e swarm.Event) []workerSignal { return c.signals(e) }

	// terva
	see(swarm.Event{Type: "tool_call", Data: map[string]any{"id": "t1", "name": "edit"}})
	got := see(swarm.Event{Type: "tool_result", Data: map[string]any{"id": "t1", "is_error": true,
		"content": []any{map[string]any{"type": "text", "text": "old_string not found"}}}})
	if len(got) != 1 || got[0].id != "t1" || got[0].tool != "edit" || got[0].why != "old_string not found" {
		t.Fatalf("terva tool error = %+v", got)
	}
	if got := see(swarm.Event{Type: "user_message", Data: map[string]any{"message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "call_id": "t1", "is_error": true},
	}}}}); len(got) != 0 {
		t.Fatalf("a terva message block counted again: %+v", got)
	}
	if got := see(swarm.Event{Type: "tool_result", Data: map[string]any{"id": "t2"}}); len(got) != 0 {
		t.Fatalf("a result that did not fail = %+v", got)
	}
	got = see(swarm.Event{Type: "retry", Data: map[string]any{"retry": map[string]any{"attempt": float64(3), "error": "overloaded"}}})
	if len(got) != 1 || got[0].attempt != 3 || got[0].why != "overloaded" {
		t.Fatalf("terva retry = %+v", got)
	}
	// A claude worker's retry, as its translator builds it from api_retry:
	// the attempt an int rather than a decoded float.
	got = see(swarm.Event{Type: "retry", Data: map[string]any{"retry": map[string]any{"attempt": 1, "max": 10, "delay_ms": int64(593), "error": "overloaded (HTTP 529)"}}})
	if len(got) != 1 || got[0].attempt != 1 || got[0].why != "overloaded (HTTP 529)" {
		t.Fatalf("claude retry = %+v", got)
	}

	// claude
	see(swarm.Event{Type: "assistant_message", Data: map[string]any{"message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "c1", "name": "Bash"},
		map[string]any{"type": "tool_use", "id": "c2", "name": "Read"},
	}}}})
	got = see(swarm.Event{Type: "user_message", Data: map[string]any{"message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "c1", "is_error": true, "content": "exit 2"},
		map[string]any{"type": "tool_result", "tool_use_id": "c2", "content": "ok"},
	}}}})
	if len(got) != 1 || got[0].tool != "Bash" || got[0].why != "exit 2" {
		t.Fatalf("claude tool error = %+v", got)
	}
	got = see(swarm.Event{Type: "retry", Data: map[string]any{"retry": map[string]any{"attempt": 1, "error": "rate_limit (HTTP 429)"}}})
	if len(got) != 1 || got[0].attempt != 1 {
		t.Fatalf("claude retry = %+v", got)
	}
	if len(c.names) != 0 {
		t.Errorf("names left after every call ended: %v", c.names)
	}
	// A call whose result never comes ends with its turn.
	see(swarm.Event{Type: "tool_call", Data: map[string]any{"id": "t3", "name": "bash"}})
	see(swarm.Event{Type: "task_end", Data: map[string]any{"error": "the turn was interrupted"}})
	if len(c.names) != 0 {
		t.Errorf("names left after the turn ended: %v", c.names)
	}
}
