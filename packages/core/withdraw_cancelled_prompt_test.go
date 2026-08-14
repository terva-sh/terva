package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// A prompt cancelled before the model said ANYTHING goes back out of the
// transcript — typing `htop` into a session and catching it in time should not
// leave it in the permanent record. See
// docs/proposals/withdraw-cancelled-prompt.md.
//
// The whole feature turns on one question: did anything land after the prompt?
// So most of what is worth pinning here is the NEGATIVE side — every way a turn
// can record something must defeat the withdrawal, because a withdrawal that
// fires once too often deletes work the user wanted.

// --- fixtures ---------------------------------------------------------------

// silentUntilCancelledClient is a model that never answers: it opens the stream,
// says it started, and waits for the turn to be cancelled. Faithful to the case
// under test — the request is in flight and not one token has come back.
type silentUntilCancelledClient struct{ started chan struct{} }

func (c *silentUntilCancelledClient) Name() string { return "silent-fake" }

func (c *silentUntilCancelledClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 1)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		select {
		case c.started <- struct{}{}:
		default:
		}
		<-ctx.Done()
	}()
	return out, nil
}

// partialThenCancelledClient streams some text and is then cancelled mid-answer.
// oneTurn keeps a text-only partial on purpose ("a cut-off summary is still
// useful"), which is exactly the recorded work a withdrawal must not eat.
type partialThenCancelledClient struct{ cancel context.CancelFunc }

func (c *partialThenCancelledClient) Name() string { return "partial-fake" }

func (c *partialThenCancelledClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 3)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		out <- provider.EventTextDelta{Delta: "half an ans"}
		c.cancel()
		out <- provider.EventDone{Stop: provider.StopAborted, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "half an ans"}},
		}}
	}()
	return out, nil
}

// toolCallingClient answers with a tool call. The tool cancels the turn as it
// runs, so by the time the loop notices, an assistant message AND a tool result
// are both in the transcript.
type toolCallingClient struct{}

func (c *toolCallingClient) Name() string { return "tool-fake" }

func (c *toolCallingClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
			Role: provider.RoleAssistant,
			Content: []provider.Content{provider.ToolCallBlock{
				ID: "call-1", Name: "canceller", Arguments: json.RawMessage(`{}`),
			}},
		}}
	}()
	return out, nil
}

// cancellingTool stands in for any tool that completes and records a result
// while the user reaches for Esc.
type cancellingTool struct{ cancel context.CancelFunc }

func (t *cancellingTool) Name() string            { return "canceller" }
func (t *cancellingTool) Description() string     { return "cancels the turn, then succeeds" }
func (t *cancellingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *cancellingTool) Execute(context.Context, json.RawMessage, func(string)) (ToolResult, error) {
	t.cancel()
	return ToolResult{Content: []provider.Content{provider.TextBlock{Text: "done"}}}, nil
}

// emptyAnswerClient ends a turn cleanly having said nothing at all. oneTurn
// appends no message for empty content, so this is the one shape where a turn
// runs to completion and the transcript is untouched afterwards — the state the
// "was it cancelled" half of the rule exists for.
type emptyAnswerClient struct{}

func (c *emptyAnswerClient) Name() string { return "empty-fake" }

func (c *emptyAnswerClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{Role: provider.RoleAssistant}}
	}()
	return out, nil
}

// failingClient never opens a stream. The prompt is the one thing the user
// wants back after a provider failure, so it is the one thing that must not be
// quietly deleted.
type failingClient struct{}

func (c *failingClient) Name() string { return "failing-fake" }

func (c *failingClient) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	return nil, errors.New("upstream is down")
}

// answeringClient completes a turn normally.
type answeringClient struct{}

func (c *answeringClient) Name() string { return "answering-fake" }

func (c *answeringClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}

// eventRecorder collects events from the sink, which the agent may call from
// another goroutine.
type eventRecorder struct {
	mu     sync.Mutex
	events []AgentEvent
}

func (r *eventRecorder) sink(ev AgentEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *eventRecorder) withdrawn() (EvUserMessageWithdrawn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.events {
		if w, ok := ev.(EvUserMessageWithdrawn); ok {
			return w, true
		}
	}
	return EvUserMessageWithdrawn{}, false
}

// saw reports whether an event of that type was emitted. The vacuity checks
// below read the fixture's behaviour from the EVENT STREAM rather than from the
// final transcript, and the difference matters: a broken rule can delete work
// out of the transcript after the fact, which would make "did the fixture
// produce any work" and "did the rule eat it" indistinguishable at the end of
// the turn. The events record what happened either way.
func (r *eventRecorder) saw(typeName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.events {
		if ev.Type() == typeName {
			return true
		}
	}
	return false
}

// userTexts (before_user_message_test.go) pulls the user-role text out of a
// transcript; these tests read it straight off the agent.
func agentUserTexts(a *Agent) []string { return userTexts(a.Messages()) }

// interruptibleContext is how a HOST cancels a turn when a person asked it to:
// with ErrUserInterrupted as the cause. A plain context.WithCancel is the other
// kind of cancel — a drain, a deadline, a dying parent — and deliberately does
// not withdraw anything.
func interruptibleContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(context.Background())
	return ctx, func() { cancel(ErrUserInterrupted) }
}

// --- the case the feature exists for ----------------------------------------

func TestACancelledPromptIsWithdrawnFromTheTranscript(t *testing.T) {
	client := &silentUntilCancelledClient{started: make(chan struct{}, 1)}
	a := NewAgent(client, "fake-model", "system", Registry{})
	rec := &eventRecorder{}
	ctx, cancel := interruptibleContext()
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Prompt(ctx, "htop", nil, rec.sink)
	}()

	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the turn never reached the provider")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not return after the cancel")
	}

	if got := a.Messages(); len(got) != 0 {
		t.Errorf("the cancelled prompt is still in the transcript: %+v", got)
	}
	w, ok := rec.withdrawn()
	if !ok {
		t.Fatal("no withdrawal event — the host has nothing to put back in the composer")
	}
	if w.Text != "htop" {
		t.Errorf("withdrawal carried %q; the composer needs the prompt back verbatim", w.Text)
	}
	if w.Index != 0 {
		t.Errorf("withdrawal reported index %d; the prompt was the first message, and a host persisting the removal names this row", w.Index)
	}
}

// The shutdown case, and the reason ErrUserInterrupted exists. A restart drain,
// a deadline, or a dying parent cancels the turn without anybody having decided
// anything — deleting the user's prompt on the way past would be terva
// discarding input nobody asked it to discard.
//
// Workspace.Restart can also survive its own drain (relaunch.Trigger may fail
// after it), and a withdrawal there would leave the message gone from memory
// and still on disk.
func TestATurnCancelledWithoutAUserBehindItKeepsThePrompt(t *testing.T) {
	client := &silentUntilCancelledClient{started: make(chan struct{}, 1)}
	a := NewAgent(client, "fake-model", "system", Registry{})
	rec := &eventRecorder{}
	// A plain cancel: no cause, so nothing claims a person did this.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Prompt(ctx, "the prompt a restart interrupted", nil, rec.sink)
	}()
	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the turn never reached the provider")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not return after the cancel")
	}

	if ctx.Err() == nil {
		t.Fatal("the turn was not cancelled — this no longer distinguishes the two kinds of cancel")
	}
	if rec.saw("assistant_message") {
		t.Fatal("the fixture recorded an answer, so the rev half could be carrying this test on its own")
	}
	if got := agentUserTexts(a); len(got) != 1 || got[0] != "the prompt a restart interrupted" {
		t.Errorf("a cancel nobody asked for deleted the prompt; user messages: %q", got)
	}
	if _, ok := rec.withdrawn(); ok {
		t.Error("withdrawal requires ErrUserInterrupted — a drain must not speak for the user")
	}
}

// --- every way a turn records something must defeat it -----------------------

func TestACompletedTurnKeepsThePrompt(t *testing.T) {
	a := NewAgent(&answeringClient{}, "fake-model", "system", Registry{})
	rec := &eventRecorder{}

	if err := a.Prompt(context.Background(), "keep me", nil, rec.sink); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if got := agentUserTexts(a); len(got) != 1 || got[0] != "keep me" {
		t.Errorf("an answered prompt must stay in the transcript, got %q", got)
	}
	if _, ok := rec.withdrawn(); ok {
		t.Error("a turn that was never cancelled must not withdraw anything")
	}
}

// The partial is the sharp one: the turn WAS cancelled, so only the "did
// anything land" half of the rule stands between the user and losing an answer
// they can see on screen.
func TestAPartialAnswerKeepsThePrompt(t *testing.T) {
	ctx, cancel := interruptibleContext()
	defer cancel()
	a := NewAgent(&partialThenCancelledClient{cancel: cancel}, "fake-model", "system", Registry{})
	rec := &eventRecorder{}

	_ = a.Prompt(ctx, "summarize this", nil, rec.sink)

	// Both halves of the situation have to be real, or this test passes without
	// exercising the rule: no cancel and the withdrawal was never eligible; no
	// kept partial and there was nothing for rev to have moved for.
	if ctx.Err() == nil {
		t.Fatal("the turn was never cancelled — the withdrawal was not even eligible, so this proves nothing")
	}
	if !rec.saw("assistant_message") {
		t.Fatal("the fixture never produced a kept partial, so rev never moved for one")
	}

	if got := agentUserTexts(a); len(got) != 1 || got[0] != "summarize this" {
		t.Errorf("the prompt was withdrawn out from under a partial answer, leaving the reply orphaned; user messages: %q", got)
	}
	if _, ok := rec.withdrawn(); ok {
		t.Error("a cancel that kept streamed text must not withdraw the prompt that produced it")
	}
}

func TestALandedToolResultKeepsThePrompt(t *testing.T) {
	ctx, cancel := interruptibleContext()
	defer cancel()
	tool := &cancellingTool{cancel: cancel}
	a := NewAgent(&toolCallingClient{}, "fake-model", "system", Registry{"canceller": tool})
	rec := &eventRecorder{}

	_ = a.Prompt(ctx, "run the thing", nil, rec.sink)

	// Same two-sided vacuity check as the partial case.
	if ctx.Err() == nil {
		t.Fatal("the turn was never cancelled — the withdrawal was not even eligible, so this proves nothing")
	}
	if !rec.saw("tool_result") {
		t.Fatal("the fixture's tool never produced a result, so rev never moved for one")
	}

	if got := agentUserTexts(a); len(got) != 1 || got[0] != "run the thing" {
		t.Errorf("a prompt whose tool actually RAN was withdrawn; user messages: %q", got)
	}
	if _, ok := rec.withdrawn(); ok {
		t.Error("work that reached the transcript must defeat the withdrawal")
	}
}

// The rev half of the rule cannot carry these two on its own: both end with the
// transcript untouched after the prompt, so ONLY "was this cancelled" stands
// between the user and a deleted message. A mutation that drops the ctx.Err()
// check passes every other test in this file.

func TestATurnThatEndsWithoutAnsweringKeepsThePrompt(t *testing.T) {
	a := NewAgent(&emptyAnswerClient{}, "fake-model", "system", Registry{})
	rec := &eventRecorder{}

	if err := a.Prompt(context.Background(), "say nothing", nil, rec.sink); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if rec.saw("assistant_message") {
		t.Fatal("the fixture recorded an answer, so rev moved and this no longer isolates the cancellation check")
	}
	if got := agentUserTexts(a); len(got) != 1 || got[0] != "say nothing" {
		t.Errorf("a turn that merely said nothing withdrew the prompt; user messages: %q", got)
	}
	if _, ok := rec.withdrawn(); ok {
		t.Error("withdrawal is for a CANCELLED turn — an empty answer is a completed one")
	}
}

// The rescue path depends on this. A provider failure is exactly when the user
// wants their prompt back to retry, and it is also a turn with nothing recorded
// after it.
func TestAFailedTurnKeepsThePrompt(t *testing.T) {
	a := NewAgent(&failingClient{}, "fake-model", "system", Registry{})
	a.MaxRetries = 0
	rec := &eventRecorder{}

	if err := a.Prompt(context.Background(), "expensive question", nil, rec.sink); err == nil {
		t.Fatal("the fixture was supposed to fail; without a failure this proves nothing")
	}

	if got := agentUserTexts(a); len(got) != 1 || got[0] != "expensive question" {
		t.Errorf("a provider failure deleted the prompt the user now wants to retry; user messages: %q", got)
	}
	if _, ok := rec.withdrawn(); ok {
		t.Error("a failed turn must keep the prompt — the rescue picker re-runs it")
	}
}

// --- the rule itself ---------------------------------------------------------

// withdrawLastUserMessage is guarded directly as well as through Prompt: these
// are the states a live transcript reaches that no fake provider reproduces
// cheaply (a compaction, a SetMessages), and the trap below is invisible from
// the outside.
func TestWithdrawLastUserMessageRequiresAnUntouchedTranscript(t *testing.T) {
	newAgentWithPrompt := func() (*Agent, uint64) {
		a := NewAgent(&answeringClient{}, "fake-model", "system", Registry{})
		a.mu.Lock()
		a.messages = append(a.messages, provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: "htop"}},
		})
		a.rev++
		rev := a.rev
		a.mu.Unlock()
		return a, rev
	}

	t.Run("untouched", func(t *testing.T) {
		a, rev := newAgentWithPrompt()
		if _, ok := a.withdrawLastUserMessage(rev); !ok {
			t.Fatal("an untouched transcript must withdraw")
		}
		if got := len(a.Messages()); got != 0 {
			t.Errorf("transcript still holds %d message(s)", got)
		}
	})

	t.Run("something was appended after", func(t *testing.T) {
		a, rev := newAgentWithPrompt()
		a.mu.Lock()
		a.messages = append(a.messages, provider.Message{Role: provider.RoleAssistant})
		a.rev++
		a.mu.Unlock()

		if _, ok := a.withdrawLastUserMessage(rev); ok {
			t.Fatal("withdrew despite a later append — and would have taken the assistant message, not the prompt")
		}
		if got := len(a.Messages()); got != 2 {
			t.Errorf("transcript should be untouched at 2 messages, got %d", got)
		}
	})

	// The trap a length comparison falls into, and the reason the rule is
	// written against rev. A compaction replaces the transcript wholesale and
	// can leave it at exactly the length it had — so "same length" is not
	// "untouched", and a withdrawal keyed on length would delete the tail of
	// somebody's compaction summary.
	t.Run("length returned but the transcript changed", func(t *testing.T) {
		a, rev := newAgentWithPrompt()
		a.SetMessages([]provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: "a summary that replaced everything"}},
		}})

		if got := len(a.Messages()); got != 1 {
			t.Fatalf("fixture wants a length-preserving replacement, got %d messages", got)
		}
		if _, ok := a.withdrawLastUserMessage(rev); ok {
			t.Fatal("withdrew after a wholesale replacement that happened to preserve the length — this is the exact bug a len() compare ships")
		}
	})

	t.Run("empty transcript", func(t *testing.T) {
		a := NewAgent(&answeringClient{}, "fake-model", "system", Registry{})
		a.mu.Lock()
		rev := a.rev
		a.mu.Unlock()
		if _, ok := a.withdrawLastUserMessage(rev); ok {
			t.Fatal("withdrew from an empty transcript")
		}
	})
}

// --- what comes back --------------------------------------------------------

// A BeforeUserMessage guard that rewrites a prompt did so deliberately; handing
// the pre-guard text back to the composer would quietly undo the rewrite.
func TestWithdrawalReturnsTheRecordedTextNotTheTyped(t *testing.T) {
	client := &silentUntilCancelledClient{started: make(chan struct{}, 1)}
	a := NewAgent(client, "fake-model", "system", Registry{})
	a.BeforeUserMessage = func(string) (bool, string, string) { return true, "", "redacted by the guard" }
	rec := &eventRecorder{}
	ctx, cancel := interruptibleContext()
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Prompt(ctx, "my password is hunter2", nil, rec.sink)
	}()
	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the turn never reached the provider")
	}
	cancel()
	<-done

	w, ok := rec.withdrawn()
	if !ok {
		t.Fatal("no withdrawal event")
	}
	if w.Text != "redacted by the guard" {
		t.Errorf("withdrawal carried %q — a guard's rewrite must survive the round trip", w.Text)
	}
}

// The preamble is the HOST's words, not the user's. Nothing should offer to put
// it in a composer.
func TestWithdrawalDoesNotReturnTheHostPreamble(t *testing.T) {
	client := &silentUntilCancelledClient{started: make(chan struct{}, 1)}
	a := NewAgent(client, "fake-model", "system", Registry{})
	rec := &eventRecorder{}
	ctx, cancel := interruptibleContext()
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.PromptExtra(ctx, "htop", nil, UserMessageExtras{
			Preamble: "[attachments expired] the files you sent are gone",
		}, rec.sink)
	}()
	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the turn never reached the provider")
	}
	cancel()
	<-done

	w, ok := rec.withdrawn()
	if !ok {
		t.Fatal("no withdrawal event")
	}
	if strings.Contains(w.Text, "attachments expired") {
		t.Errorf("the host preamble came back as if the user had typed it: %q", w.Text)
	}
	if w.Text != "htop" {
		t.Errorf("withdrawal carried %q, want just the user's own text", w.Text)
	}
}

// Without a wire mapping the withdrawal is in-process only, and every remote
// surface (web, --json, attach) sees the message vanish with no explanation —
// the failure EvUserMessageRejected's own comment records having shipped once.
func TestWithdrawalCrossesTheWire(t *testing.T) {
	w := EventToWire(EvUserMessageWithdrawn{Text: "htop"})

	if w.Type != "user_message_withdrawn" {
		t.Errorf("wire type = %q", w.Type)
	}
	if w.Text != "htop" {
		t.Errorf("wire text = %q; the withdrawn prompt must cross in full", w.Text)
	}
	if w.Rejected != "" {
		t.Errorf("withdrawal set the rejected field (%q) — a cancel is not a refusal", w.Rejected)
	}
}
