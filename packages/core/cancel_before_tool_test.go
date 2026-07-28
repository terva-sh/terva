package core

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/provider"
)

// Cancelling a turn has to mean the turn stopped, and until this it did not
// quite: the confirm ladder can block for MINUTES waiting on a human in a chat
// (`terva bot --approval ask`, 2m) or on an orchestrator over MCP
// (--approval-socket / --approval-http, 10m), and BOTH confirmers wait on the
// HOST's context rather than the turn's — so `/stop` never unparked them.
//
// A confirmer that eventually times out denies, which is why this was invisible.
// The path that bites is the one where somebody ANSWERS: the user typed /stop
// and was told "cancelled the current turn", the approver then tapped Approve,
// and the tool ran. Tools receive ctx but are not obliged to read it, and the
// filesystem ones — write, edit, glob, grep — never do, because a single write
// has nothing to interrupt. So the write landed after the cancel.
//
// The turn's own loop is the only place that can promise a cancel is a cancel.

// cancelProbeTool reports whether it was ever dispatched. Its Execute deliberately
// IGNORES ctx, mirroring write/edit: a fixture that checked ctx would pass this
// test on its own merits and prove nothing about the loop.
type cancelProbeTool struct{ ran atomic.Bool }

func (t *cancelProbeTool) Name() string            { return "recorder" }
func (t *cancelProbeTool) Description() string     { return "records that it ran" }
func (t *cancelProbeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *cancelProbeTool) Execute(context.Context, json.RawMessage, func(string)) (ToolResult, error) {
	t.ran.Store(true)
	return ToolResult{Content: []provider.Content{provider.TextBlock{Text: "ran"}}}, nil
}

func recorderCall() provider.ToolCallBlock {
	return provider.ToolCallBlock{ID: "call-1", Name: "recorder", Arguments: json.RawMessage(`{}`)}
}

// The case this closed: the gate returns ALLOW after the turn was cancelled —
// exactly what a human tapping Approve a moment after /stop produces.
func TestAnApprovalThatLandsAfterACancelDoesNotRunTheTool(t *testing.T) {
	tool := &cancelProbeTool{}
	ag := &Agent{}
	ctx, cancel := context.WithCancel(context.Background())

	// Stand in for a confirmer blocked on the daemon's context: it is still
	// waiting when the turn is cancelled, and then it says yes.
	ag.BeforeToolExecute = func(provider.ToolCallBlock) (bool, string, json.RawMessage) {
		cancel()
		return true, "", nil
	}

	res := ag.runOneTool(ctx, recorderCall(), Registry{"recorder": tool}, func(AgentEvent) {})

	if tool.ran.Load() {
		t.Error("the tool RAN for a turn the user had already cancelled — this is the write that lands after " +
			"terva has said \"cancelled the current turn\"")
	}
	if !res.IsError {
		t.Error("the skipped call came back as a success; the model would read it as having run")
	}
	if text := resultText(res); !strings.Contains(text, "approval") {
		t.Errorf("the result does not say the approval was too late (%q) — that sentence is the only place a "+
			"reader learns why an approved call did nothing", text)
	}
}

// The simpler direction: cancelled before the call was ever dispatched, so the
// approver is never even asked. A question posted to a chat for a turn that is
// already dead is its own small harm.
func TestACancelledTurnDispatchesNoFurtherTools(t *testing.T) {
	tool := &cancelProbeTool{}
	var asked atomic.Bool
	ag := &Agent{BeforeToolExecute: func(provider.ToolCallBlock) (bool, string, json.RawMessage) {
		asked.Store(true)
		return true, "", nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := ag.runOneTool(ctx, recorderCall(), Registry{"recorder": tool}, func(AgentEvent) {})

	if tool.ran.Load() {
		t.Error("a tool ran for an already-cancelled turn")
	}
	if asked.Load() {
		t.Error("the confirm ladder was consulted for an already-cancelled turn — that posts an approval question " +
			"to a chat about work that can no longer happen")
	}
	if !res.IsError {
		t.Error("the skipped call came back as a success")
	}
}

// The guard must not become "tools never run": the ordinary path is unchanged,
// and a test suite where every call is skipped would pass the two above for the
// worst possible reason.
func TestALiveTurnStillRunsItsTools(t *testing.T) {
	tool := &cancelProbeTool{}
	ag := &Agent{BeforeToolExecute: func(provider.ToolCallBlock) (bool, string, json.RawMessage) {
		return true, "", nil
	}}

	res := ag.runOneTool(context.Background(), recorderCall(), Registry{"recorder": tool}, func(AgentEvent) {})

	if !tool.ran.Load() {
		t.Fatal("a live turn did not run its tool; the cancel guard is refusing work it should allow")
	}
	if res.IsError {
		t.Errorf("a live call came back as an error: %q", resultText(res))
	}
}

// Every call in a batch is covered, not just the first: a turn cancelled while
// call 1 was parked must not run calls 2 and 3 either. executeTools is the loop
// a real turn goes through, so this is the shape the fix has to hold at.
func TestACancelMidBatchStopsTheRemainingCalls(t *testing.T) {
	tool := &cancelProbeTool{}
	var dispatched atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	ag := &Agent{BeforeToolExecute: func(provider.ToolCallBlock) (bool, string, json.RawMessage) {
		if dispatched.Add(1) == 1 {
			cancel() // the first call's approval outlived the turn
		}
		return true, "", nil
	}}

	msg := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
		provider.ToolCallBlock{ID: "a", Name: "recorder", Arguments: json.RawMessage(`{}`)},
		provider.ToolCallBlock{ID: "b", Name: "recorder", Arguments: json.RawMessage(`{}`)},
		provider.ToolCallBlock{ID: "c", Name: "recorder", Arguments: json.RawMessage(`{}`)},
	}}
	out, hadError := ag.executeTools(ctx, msg, Registry{"recorder": tool}, func(AgentEvent) {})

	if tool.ran.Load() {
		t.Error("a tool ran after the turn was cancelled mid-batch")
	}
	if !hadError {
		t.Error("executeTools reported no error for a batch it entirely skipped")
	}
	if n := dispatched.Load(); n != 1 {
		t.Errorf("the confirm ladder was consulted %d times; want 1 — calls after the cancel must not reach it", n)
	}
	// One tool-role result per call, still: some providers reject a transcript
	// with a tool_use that has no matching result, so skipping must not mean
	// answering nothing.
	if got := len(out.Content); got != 3 {
		t.Errorf("got %d results for 3 calls; a skipped call still owes the model a reply", got)
	}
}

func resultText(res ToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}
