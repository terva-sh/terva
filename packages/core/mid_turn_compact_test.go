package core

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

// noopTool satisfies tool_use steps in scripted turns.
type noopTool struct{}

func (noopTool) Name() string            { return "noop" }
func (noopTool) Description() string     { return "does nothing" }
func (noopTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (noopTool) Execute(ctx context.Context, _ json.RawMessage, _ func(string)) (ToolResult, error) {
	return ToolResult{Content: []provider.Content{provider.TextBlock{Text: "ok"}}}, nil
}

// midTurnStep scripts one agent-loop step: the usage the provider
// reports for the request and whether the model answers with a tool
// call (continuing the loop) or final text (ending it).
type midTurnStep struct {
	usageInput int
	toolCall   bool
}

// midTurnFakeClient serves a scripted multi-step agentic turn.
// Compaction requests (their single message wraps the transcript in
// <conversation> tags) are answered with a canned summary and counted;
// every other call consumes the next scripted step.
type midTurnFakeClient struct {
	mu           sync.Mutex
	steps        []midTurnStep
	compactCalls int
	compactUsage provider.Usage // when non-zero, emitted on compaction responses
	reqs         []provider.Request
}

func (c *midTurnFakeClient) Name() string { return "mid-turn-fake" }

func (c *midTurnFakeClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	isCompact := len(req.Messages) == 1 && len(req.Messages[0].Content) == 1 &&
		func() bool {
			tb, ok := req.Messages[0].Content[0].(provider.TextBlock)
			return ok && strings.Contains(tb.Text, "<conversation>")
		}()
	var step midTurnStep
	if isCompact {
		c.compactCalls++
	} else {
		if len(c.steps) == 0 {
			c.mu.Unlock()
			return nil, &provider.ProviderError{Provider: "mid-turn-fake", Status: 500, Msg: "script exhausted"}
		}
		step = c.steps[0]
		c.steps = c.steps[1:]
	}
	c.mu.Unlock()

	out := make(chan provider.Event, 8)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "mid-turn-fake", Model: req.Model}
		if isCompact {
			if c.compactUsage != (provider.Usage{}) {
				out <- provider.EventUsage{Usage: c.compactUsage}
			}
			out <- provider.EventTextDelta{Delta: "summary of the work so far"}
			out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: "summary of the work so far"}},
			}}
			return
		}
		out <- provider.EventUsage{Usage: provider.Usage{InputTokens: step.usageInput}}
		if step.toolCall {
			out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{ID: "T1", Name: "noop", Arguments: json.RawMessage(`{}`)}},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "done"}},
		}}
	}()
	return out, nil
}

func smallMsg(role provider.Role, text string) provider.Message {
	return provider.Message{Role: role, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

func seedSmallTranscript(a *Agent, n int) {
	seed := make([]provider.Message, 0, n)
	for i := range n {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		seed = append(seed, smallMsg(role, "filler"))
	}
	a.SetMessages(seed)
}

// TestMidTurnAutoCompact reproduces the marathon-turn failure: one
// prompt drives a multi-step tool loop whose usage crosses the
// auto-compact threshold BETWEEN steps. No turn boundary ever arrives,
// so only the mid-turn valve at the step boundary can condense the
// transcript before the next request bounces off the context window.
func TestMidTurnAutoCompact(t *testing.T) {
	client := &midTurnFakeClient{steps: []midTurnStep{
		{usageInput: 190_000, toolCall: true}, // step 1: 95% of the 200k window
		{usageInput: 8_000, toolCall: false},  // step 2: post-compact, small again
	}}
	a := NewAgent(client, "claude-sonnet-4-5", "system", Registry{"noop": noopTool{}})
	seedSmallTranscript(a, 4) // beyond keep-tail so CanCompact is true

	var events []string
	err := a.Prompt(context.Background(), "go", nil, func(ev AgentEvent) {
		events = append(events, ev.Type())
	})
	if err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	if client.compactCalls != 1 {
		t.Fatalf("compact calls = %d, want 1", client.compactCalls)
	}
	joined := strings.Join(events, ",")
	if !strings.Contains(joined, "compact_start,compact_end,turn_start") {
		t.Fatalf("mid-turn compact should fire at the step boundary (before turn_start): %v", events)
	}
	msgs := a.Messages()
	if len(msgs) == 0 || msgs[0].Meta["compaction"] != "true" {
		t.Fatalf("transcript head is not a compaction summary; head=%+v", msgs[0])
	}
	// The tool_use/tool_result pair from step 1 sits inside the kept tail
	// and must survive compaction intact — a severed pair would be
	// rejected by the provider on the next request.
	var hasToolUse, hasToolResult bool
	for _, m := range msgs {
		for _, c := range m.Content {
			switch c.(type) {
			case provider.ToolCallBlock:
				hasToolUse = true
			case provider.ToolResultBlock:
				hasToolResult = true
			}
		}
	}
	if !hasToolUse || !hasToolResult {
		t.Fatalf("tool_use/tool_result pair damaged by mid-turn compact (use=%v result=%v)", hasToolUse, hasToolResult)
	}
	// The final step's usage re-baselined the gauge far below threshold.
	if f := a.ContextFraction(); f >= AutoCompactThreshold {
		t.Fatalf("post-turn ContextFraction = %v, want < threshold", f)
	}
}

// TestMidTurnAutoCompactHysteresis: when the keep-tail itself is too big
// for compaction to get the transcript under the threshold (one enormous
// tool payload, say), the valve must not re-fire a futile summarization
// at every subsequent step boundary — it stays disarmed until the
// measured fraction actually drops below the threshold.
func TestMidTurnAutoCompactHysteresis(t *testing.T) {
	client := &midTurnFakeClient{steps: []midTurnStep{
		{usageInput: 120_000, toolCall: true}, // 94% of gpt-4o's 128k window
		{usageInput: 120_000, toolCall: true}, // still saturated after compaction
		{usageInput: 8_000, toolCall: false},
	}}
	a := NewAgent(client, "gpt-4o", "system", Registry{"noop": noopTool{}})
	// A transcript whose newest message is so large that even the
	// post-compaction estimate (summary + kept tail) stays over the
	// threshold: 0.85 * 128k tokens * 4 chars/token ≈ 435k chars.
	seed := []provider.Message{
		smallMsg(provider.RoleUser, "q"),
		smallMsg(provider.RoleAssistant, "a"),
		smallMsg(provider.RoleUser, "q2"),
		smallMsg(provider.RoleAssistant, strings.Repeat("x", 500_000)),
	}
	a.SetMessages(seed)

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	if client.compactCalls != 1 {
		t.Fatalf("compact calls = %d, want exactly 1 (disarmed valve must not re-fire)", client.compactCalls)
	}
}

// TestContextPressureNoteInEphemeralContext: past ContextWarnFraction
// the request's ephemeral tail carries a context-pressure note so the
// model learns how full its window is without polling terva_status.
func TestContextPressureNoteInEphemeralContext(t *testing.T) {
	client := &midTurnFakeClient{steps: []midTurnStep{{usageInput: 150_000, toolCall: false}}}
	a := NewAgent(client, "claude-sonnet-4-5", "system", Registry{})
	a.SeedLastTurnUsage(provider.Usage{InputTokens: 150_000}) // 75% of 200k

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	if len(client.reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(client.reqs))
	}
	eph := client.reqs[0].EphemeralContext
	if !strings.Contains(eph, "[context pressure]") || !strings.Contains(eph, "% full") {
		t.Fatalf("ephemeral context missing pressure note: %q", eph)
	}

	// Below the warn threshold: no note.
	client2 := &midTurnFakeClient{steps: []midTurnStep{{usageInput: 10_000, toolCall: false}}}
	b := NewAgent(client2, "claude-sonnet-4-5", "system", Registry{})
	b.SeedLastTurnUsage(provider.Usage{InputTokens: 50_000}) // 25%
	if err := b.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	if eph := client2.reqs[0].EphemeralContext; strings.Contains(eph, "[context pressure]") {
		t.Fatalf("pressure note should not appear below the warn threshold: %q", eph)
	}
}

// TestContextPressureNoteRespectsAutoCompactOff: with auto_compact "off"
// the note must not promise the 85% auto-compaction valve — it tells the
// model compaction is disabled and suggests wrapping up / manual /compact.
func TestContextPressureNoteRespectsAutoCompactOff(t *testing.T) {
	client := &midTurnFakeClient{steps: []midTurnStep{{usageInput: 150_000, toolCall: false}}}
	a := NewAgent(client, "claude-sonnet-4-5", "system", Registry{})
	a.AutoCompactPolicy = func() AutoCompactMode { return AutoCompactOff }
	a.SeedLastTurnUsage(provider.Usage{InputTokens: 150_000}) // 75% of 200k

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	if len(client.reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(client.reqs))
	}
	eph := client.reqs[0].EphemeralContext
	if !strings.Contains(eph, "[context pressure]") {
		t.Fatalf("ephemeral context missing pressure note: %q", eph)
	}
	if strings.Contains(eph, "auto-compacted") {
		t.Errorf("off-mode note still promises auto-compaction: %q", eph)
	}
	if !strings.Contains(eph, "automatic compaction off") || !strings.Contains(eph, "/compact") {
		t.Errorf("off-mode note should say compaction is off and point at manual /compact: %q", eph)
	}
}

// TestCompactRebaselinesContextGauge: a successful compaction must reset
// the stale pre-compaction usage snapshot, otherwise every fraction
// check until the next completed request re-reads ~full and re-fires.
func TestCompactRebaselinesContextGauge(t *testing.T) {
	client := &midTurnFakeClient{}
	a := NewAgent(client, "claude-sonnet-4-5", "system", Registry{})
	seedSmallTranscript(a, 8)
	a.SeedLastTurnUsage(provider.Usage{InputTokens: 190_000}) // 95%

	if _, err := a.Compact(context.Background(), AutoCompactKeepTail, nil); err != nil {
		t.Fatalf("Compact returned %v", err)
	}
	if f := a.ContextFraction(); f >= AutoCompactThreshold {
		t.Fatalf("ContextFraction after compaction = %v; want re-baselined below threshold", f)
	}
}
