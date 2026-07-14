package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

func step(id, name, args, out string) []provider.Message {
	return []provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.ToolCallBlock{ID: id, Name: name, Arguments: json.RawMessage(args)},
		}},
		{Role: provider.RoleTool, Content: []provider.Content{
			provider.ToolResultBlock{CallID: id, Content: []provider.Content{provider.TextBlock{Text: out}}},
		}},
	}
}

// The bug, and the reason keep-tail cannot be a message count alone.
//
// An agent runs a long loop, then reads two source files before editing them —
// the most ordinary agentic pattern there is. keepTail is 4 messages, which is
// exactly those two reads. Compaction dutifully preserves them, and reclaims
// nothing: measured at 0.1% on 83.5k tokens.
//
// That is unrecoverable, not merely wasteful. Auto-compact fires at 85%, reclaims
// nothing, the context keeps growing, a request is rejected as too large, the 413
// path compacts and retries once, that compaction also reclaims nothing, and the
// retry is rejected again. The session is dead without /clear.
func TestKeepTailBudgetTrimsAnOversizedTail(t *testing.T) {
	bigFile := strings.Repeat("func doSomething() { /* ... */ }\n", 5000) // ~40k tokens

	msgs := []provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "go"}}}}
	for i := 0; i < 38; i++ {
		msgs = append(msgs, step(fmt.Sprintf("c%d", i), "bash", `{"command":"go test ./..."}`, "ok")...)
	}
	msgs = append(msgs, step("r1", "read", `{"path":"agent.go"}`, bigFile)...)
	msgs = append(msgs, step("r2", "read", `{"path":"compact.go"}`, bigFile)...)

	window := 200_000
	budget := int(float64(window) * KeepTailMaxFraction) // 20k

	// Today's behavior, with no size cap: the two file reads survive verbatim.
	unbounded := tailWithinBudget(msgs, AutoCompactKeepTail, 0)
	if got := estimateTokens(unbounded); got < 50_000 {
		t.Fatalf("fixture is wrong: the uncapped tail is only %d tokens", got)
	}

	// With the cap: the oversized reads are dropped, so the compaction can
	// actually reclaim the window.
	bounded := tailWithinBudget(msgs, AutoCompactKeepTail, budget)
	kept := estimateTokens(bounded)
	if kept > budget {
		t.Errorf("the tail is %d tokens, over the %d budget — compaction cannot reclaim the window", kept, budget)
	}
	t.Logf("uncapped tail: %d tokens (%d msgs) -> capped: %d tokens (%d msgs)",
		estimateTokens(unbounded), len(unbounded), kept, len(bounded))

	// Whatever survives must be a VALID transcript: no tool_result whose
	// tool_use was left behind. A provider rejects that outright, which would
	// turn an ineffective compaction into a broken one.
	for _, m := range bounded {
		for _, c := range m.Content {
			tr, ok := c.(provider.ToolResultBlock)
			if !ok {
				continue
			}
			found := false
			for _, m2 := range bounded {
				for _, c2 := range m2.Content {
					if tc, ok := c2.(provider.ToolCallBlock); ok && tc.ID == tr.CallID {
						found = true
					}
				}
			}
			if !found {
				t.Errorf("orphaned tool_result %q survived the trim; the provider will reject this", tr.CallID)
			}
		}
	}
}

// The common case is untouched. Small tool results are nowhere near the budget,
// so the token cap never binds and the tail is exactly the last four messages —
// the behavior this has always had.
func TestKeepTailBudgetDoesNotDisturbTheCommonCase(t *testing.T) {
	msgs := []provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "go"}}}}
	for i := 0; i < 40; i++ {
		msgs = append(msgs, step(fmt.Sprintf("c%d", i), "bash", `{"command":"go test ./..."}`, "ok, 12 packages")...)
	}

	budget := int(float64(200_000) * KeepTailMaxFraction)
	bounded := tailWithinBudget(msgs, AutoCompactKeepTail, budget)
	unbounded := tailWithinBudget(msgs, AutoCompactKeepTail, 0)

	if len(bounded) != len(unbounded) || len(bounded) != AutoCompactKeepTail {
		t.Errorf("the size cap disturbed an ordinary tail: kept %d messages, want %d",
			len(bounded), AutoCompactKeepTail)
	}
}

// A model whose context window terva doesn't know reports window=0. The size cap
// then can't be computed, and must not silently become "keep nothing" — it falls
// back to the pure message count, which is exactly today's behavior.
func TestKeepTailBudgetOfZeroKeepsTheMessageCount(t *testing.T) {
	msgs := []provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "go"}}}}
	for i := 0; i < 5; i++ {
		msgs = append(msgs, step(fmt.Sprintf("c%d", i), "bash", `{"command":"x"}`, "ok")...)
	}
	if got := len(tailWithinBudget(msgs, AutoCompactKeepTail, 0)); got != AutoCompactKeepTail {
		t.Errorf("with no window known, kept %d messages; want the plain count %d", got, AutoCompactKeepTail)
	}
}
