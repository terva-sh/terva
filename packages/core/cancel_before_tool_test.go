package core

import (
	"context"
	"encoding/json"
	"fmt"
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
	ag.gate = GateFunc(func(context.Context, provider.ToolCallBlock, Tool) (bool, string, json.RawMessage) {
		cancel()
		return true, "", nil
	})

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
	ag := &Agent{gate: GateFunc(func(context.Context, provider.ToolCallBlock, Tool) (bool, string, json.RawMessage) {
		asked.Store(true)
		return true, "", nil
	})}
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
	ag := &Agent{gate: GateFunc(func(context.Context, provider.ToolCallBlock, Tool) (bool, string, json.RawMessage) {
		return true, "", nil
	})}

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
	ag := &Agent{gate: GateFunc(func(context.Context, provider.ToolCallBlock, Tool) (bool, string, json.RawMessage) {
		if dispatched.Add(1) == 1 {
			cancel() // the first call's approval outlived the turn
		}
		return true, "", nil
	})}

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

// cancelMidRunTool stands in for a call that is under way when the turn is
// cancelled: an MCP request already sent, a write partway through. It cancels
// the turn itself, from inside Execute, and returns the error a tool that
// reads its ctx returns.
type cancelMidRunTool struct {
	name   string
	cancel context.CancelFunc
}

func (t *cancelMidRunTool) Name() string            { return t.name }
func (t *cancelMidRunTool) Description() string     { return "cancelled while it runs" }
func (t *cancelMidRunTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *cancelMidRunTool) Execute(ctx context.Context, _ json.RawMessage, _ func(string)) (ToolResult, error) {
	t.cancel()
	<-ctx.Done()
	return ToolResult{}, ctx.Err()
}

// A call that a cancel stopped while it ran is not a call that never started.
// Its effect is unknown, so a call that can change state says so and asks the
// model to look before it repeats the call. A read-only call changed nothing
// either way, so it keeps the plain "aborted". Run through the loop, because
// the read-only set has to be the one the turn pinned, and only the loop pins.
func TestACallInterruptedWhileItRunsSaysItsEffectIsUnknown(t *testing.T) {
	for _, c := range []struct {
		name     string
		readOnly bool
	}{{"changes state", false}, {"read-only", true}} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tool := &cancelMidRunTool{name: "remote", cancel: cancel}
			client := &scriptedClient{name: "scripted", script: func(int, provider.Request) ([]provider.Event, error) {
				call := provider.ToolCallBlock{ID: "c1", Name: "remote", Arguments: json.RawMessage(`{}`)}
				return []provider.Event{provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
					Role: provider.RoleAssistant, Content: []provider.Content{call},
				}}}, nil
			}}
			ag := newTestAgent(client, "m", "", Registry{"remote": tool})
			ro := NewReadOnlySet()
			if c.readOnly {
				ro.Add("remote")
			}
			ag.SetToolsWithReadOnly(Registry{"remote": tool}, ro)

			_ = ag.Prompt(ctx, "call it", nil, nil) // the turn ends on the cancel

			msgs := ag.Messages()
			last := msgs[len(msgs)-1]
			if last.Role != provider.RoleTool || len(last.Content) != 1 {
				t.Fatalf("want one tool result at the end, got %+v", last)
			}
			tr := last.Content[0].(provider.ToolResultBlock)
			text := blockText(tr)
			if !tr.IsError {
				t.Errorf("an interrupted call came back as a success: %q", text)
			}
			if !strings.Contains(text, context.Canceled.Error()) {
				t.Errorf("the result dropped the error text: %q", text)
			}
			if c.readOnly {
				if !strings.HasPrefix(text, "aborted: ") {
					t.Errorf("a read-only call lost its plain aborted wording: %q", text)
				}
				return
			}
			for _, want := range []string{"interrupted", "while it ran", "effect is unknown", "Check the current state before you repeat the call"} {
				if !strings.Contains(text, want) {
					t.Errorf("a call that can change state lacks %q: %q", want, text)
				}
			}
			if strings.HasPrefix(text, "aborted") {
				t.Errorf("a call that ran still reads as aborted: %q", text)
			}
		})
	}
}

// The tool's own timeout, with the turn still live, is the MCP case: the
// request went out and the server can have done the work. It reads the same
// as a cancel. A ctx with no pinned generation counts the tool as one that can
// change state, which is the safe direction.
func TestAToolTimeoutReadsAsAnUnknownEffect(t *testing.T) {
	tool := &timeoutTool{}
	ag := &Agent{gate: AllowAll}

	res := ag.runOneTool(context.Background(), provider.ToolCallBlock{ID: "c1", Name: "slow", Arguments: json.RawMessage(`{}`)},
		Registry{"slow": tool}, func(AgentEvent) {})

	text := resultText(res)
	if !res.IsError || !strings.Contains(text, "effect is unknown") || !strings.Contains(text, context.DeadlineExceeded.Error()) {
		t.Errorf("a timed-out call does not say its effect is unknown, with the error kept: %q", text)
	}
}

type timeoutTool struct{}

func (timeoutTool) Name() string            { return "slow" }
func (timeoutTool) Description() string     { return "times out" }
func (timeoutTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (timeoutTool) Execute(context.Context, json.RawMessage, func(string)) (ToolResult, error) {
	return ToolResult{}, fmt.Errorf("mcp call: %w", context.DeadlineExceeded)
}

// The calls that never started keep their wording: nothing ran, so "aborted"
// is the truth for them, and a check-the-state warning there would send the
// model looking for effects that cannot exist.
func TestACallCancelledBeforeItStartsStillReadsAsAborted(t *testing.T) {
	ag := &Agent{gate: AllowAll}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := ag.runOneTool(ctx, recorderCall(), Registry{"recorder": &cancelProbeTool{}}, func(AgentEvent) {})

	if text := resultText(res); !strings.HasPrefix(text, "aborted: ") || strings.Contains(text, "effect is unknown") {
		t.Errorf("a call that never started lost its aborted wording: %q", text)
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
