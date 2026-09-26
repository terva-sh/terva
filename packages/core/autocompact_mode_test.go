package core

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// TestAutoCompactModeOff: `off` silences every automatic path — the
// pre-turn check, the mid-turn valve, and the
// oversize-error recovery retry. The purist escape hatch must mean it.
func TestAutoCompactModeOff(t *testing.T) {
	// Pre-turn + mid-turn: a saturated multi-step turn runs to completion
	// with zero compaction calls.
	client := &midTurnFakeClient{steps: []midTurnStep{
		{usageInput: 190_000, toolCall: true},
		{usageInput: 195_000, toolCall: false},
	}}
	a := newTestAgent(client, "claude-sonnet-4-5", "system", Registry{"noop": noopTool{}})
	a.compactionPolicy = DefaultCompactionPolicy{Mode: func() AutoCompactMode { return AutoCompactOff }}
	seedSmallTranscript(a, 8)
	a.SeedLastTurnUsage(provider.Usage{InputTokens: 190_000}) // pre-turn check would fire

	if a.Compaction(CompactBeforeTurn).Compact {
		t.Fatal("the before-turn decision must be no in off mode")
	}
	if err := a.PromptWithPolicy(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("PromptWithPolicy returned %v", err)
	}
	if client.compactCalls != 0 {
		t.Fatalf("compact calls = %d, want 0 in off mode", client.compactCalls)
	}

	// Error recovery: a context-length 400 surfaces instead of compact-retry.
	failing := &policyFakeClient{
		firstErr: &provider.ProviderError{Provider: "policy-fake", Status: 400, Msg: "maximum context length exceeded"},
	}
	b := newTestAgent(failing, "fake-model", "system", Registry{})
	b.compactionPolicy = DefaultCompactionPolicy{Mode: func() AutoCompactMode { return AutoCompactOff }}
	seedSmallTranscript(b, 8)
	err := b.PromptWithPolicy(context.Background(), "go", nil, nil)
	if err == nil || !isContextLengthError(err) {
		t.Fatalf("off mode must surface the context-length error, got %v", err)
	}
}

// TestAutoCompactModeTurns: `turns` keeps the boundary policies but
// disables the mid-turn valve — the pre-mid-turn behavior, selectable.
func TestAutoCompactModeTurns(t *testing.T) {
	// Mid-turn: saturated multi-step turn, no compaction.
	client := &midTurnFakeClient{steps: []midTurnStep{
		{usageInput: 190_000, toolCall: true},
		{usageInput: 195_000, toolCall: false},
	}}
	a := newTestAgent(client, "claude-sonnet-4-5", "system", Registry{"noop": noopTool{}})
	a.compactionPolicy = DefaultCompactionPolicy{Mode: func() AutoCompactMode { return AutoCompactTurns }}
	seedSmallTranscript(a, 8)

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	if client.compactCalls != 0 {
		t.Fatalf("mid-turn compact calls = %d, want 0 in turns mode", client.compactCalls)
	}

	// Pre-turn still fires: primed gauge + long transcript compacts before
	// the next prompt.
	client2 := &midTurnFakeClient{steps: []midTurnStep{{usageInput: 8_000, toolCall: false}}}
	b := newTestAgent(client2, "claude-sonnet-4-5", "system", Registry{})
	b.compactionPolicy = DefaultCompactionPolicy{Mode: func() AutoCompactMode { return AutoCompactTurns }}
	seedSmallTranscript(b, 8)
	b.SeedLastTurnUsage(provider.Usage{InputTokens: 190_000})
	if err := b.PromptWithPolicy(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("PromptWithPolicy returned %v", err)
	}
	if client2.compactCalls != 1 {
		t.Fatalf("pre-turn compact calls = %d, want 1 in turns mode", client2.compactCalls)
	}
}

// TestAutoCompactModeUnknownFallsBackToSteps: a hand-edited config with
// a typo must degrade to the default policy, not to silence.
func TestAutoCompactModeUnknownFallsBackToSteps(t *testing.T) {
	full := CompactionState{Point: CompactMidTurn, Fraction: 0.9, Messages: 10}
	if !(DefaultCompactionPolicy{Mode: func() AutoCompactMode { return "stepz" }}).Decide(full).Compact {
		t.Fatal("an unknown mode did not resolve to steps")
	}
	if !(DefaultCompactionPolicy{}).Decide(full).Compact {
		t.Fatal("no mode did not resolve to steps")
	}
}

// TestMidTurnCompactUsesMidTaskAddendum: the mid-turn summarization
// request must carry the mid-task instruction (the already-executed
// ledger), and the host-driven Compact must NOT — its idle-time format
// stays unchanged.
func TestMidTurnCompactUsesMidTaskAddendum(t *testing.T) {
	client := &midTurnFakeClient{steps: []midTurnStep{
		{usageInput: 190_000, toolCall: true},
		{usageInput: 8_000, toolCall: false},
	}}
	a := newTestAgent(client, "claude-sonnet-4-5", "system", Registry{"noop": noopTool{}})
	seedSmallTranscript(a, 4)

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}
	if client.compactCalls != 1 {
		t.Fatalf("compact calls = %d, want 1", client.compactCalls)
	}
	compactReq := findCompactRequest(t, client)
	if !strings.Contains(compactReq, "Actions Already Executed") {
		t.Fatal("mid-turn summarization request missing the mid-task addendum")
	}

	// Host-driven (idle) Compact: no addendum.
	client2 := &midTurnFakeClient{}
	b := newTestAgent(client2, "claude-sonnet-4-5", "system", Registry{})
	seedSmallTranscript(b, 8)
	if _, err := b.Compact(context.Background(), AutoCompactKeepTail, nil); err != nil {
		t.Fatalf("Compact returned %v", err)
	}
	if req := findCompactRequest(t, client2); strings.Contains(req, "Actions Already Executed") {
		t.Fatal("idle compaction must not carry the mid-task addendum")
	}
}

// findCompactRequest returns the text of the (single) summarization
// request the fake client saw.
func findCompactRequest(t *testing.T, c *midTurnFakeClient) string {
	t.Helper()
	for _, req := range c.reqs {
		if len(req.Messages) == 1 && len(req.Messages[0].Content) == 1 {
			if tb, ok := req.Messages[0].Content[0].(provider.TextBlock); ok && strings.Contains(tb.Text, "<conversation>") {
				return tb.Text
			}
		}
	}
	t.Fatal("no summarization request captured")
	return ""
}

// TestCompactUsageCountsTowardTotalOnly: the summarization request's
// usage is real spend and must land in the session total — but never in
// the last-turn snapshot, which is the context gauge Compact just
// re-baselined.
func TestCompactUsageCountsTowardTotalOnly(t *testing.T) {
	client := &midTurnFakeClient{compactUsage: provider.Usage{InputTokens: 170_000, OutputTokens: 900, CostUSD: 1.10}}
	a := newTestAgent(client, "claude-sonnet-4-5", "system", Registry{})
	seedSmallTranscript(a, 8)
	before := a.Cost()

	if _, err := a.Compact(context.Background(), AutoCompactKeepTail, nil); err != nil {
		t.Fatalf("Compact returned %v", err)
	}
	after := a.Cost()
	if got := after.InputTokens - before.InputTokens; got != 170_000 {
		t.Fatalf("total input delta = %d, want 170000 (summarization spend uncounted)", got)
	}
	if after.CostUSD-before.CostUSD < 1.0 {
		t.Fatalf("total cost delta = %v, want ~1.10", after.CostUSD-before.CostUSD)
	}
	// The gauge holds the small post-compact estimate, not the 170k call.
	last := a.LastTurnUsage()
	if last.InputTokens >= 170_000 {
		t.Fatalf("last-turn snapshot = %d input tokens; the summarization call clobbered the re-baselined gauge", last.InputTokens)
	}
}
