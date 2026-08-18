//go:build terva_acp

package acp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// sinkSession returns a session whose emits land in buf, so translateEvent can
// be driven directly and the WIRE FORM inspected.
func sinkSession(t *testing.T) (*session, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	srv := &agentServer{conn: newConn(strings.NewReader(""), &buf, nil)}
	return newSession("s1", testsupport.TempDir(t), nil, nil, srv), &buf
}

// updates decodes every session/update notification written to buf.
func updates(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var msg struct {
			Method string `json:"method"`
			Params struct {
				Update map[string]any `json:"update"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("undecodable frame %q: %v", line, err)
		}
		if msg.Method == MethodSessionUpdate {
			out = append(out, msg.Params.Update)
		}
	}
	return out
}

// §12 deferred agent_thought_chunk with a stated REASON: "no live source today;
// needs a core.EvReasoningDelta event + provider plumbing". That event was
// later added for the TUI's reasoning display, and nothing revisited the
// deferral — so the reason expired while the deferral stayed, and an ACP editor
// showed a silent gap wherever a reasoning model was thinking.
//
// A deferral whose condition has expired is invisible unless something asks.
func TestReasoningDeltaBecomesAThoughtChunk(t *testing.T) {
	s, buf := sinkSession(t)
	s.translateEvent(core.EvReasoningDelta{Delta: "weighing two approaches"})

	ups := updates(t, buf)
	if len(ups) != 1 {
		t.Fatalf("got %d updates, want 1: %v", len(ups), ups)
	}
	if got := ups[0]["sessionUpdate"]; got != UpdateAgentThoughtChunk {
		t.Errorf("sessionUpdate = %v, want %q — reasoning must not arrive as an ordinary message chunk",
			got, UpdateAgentThoughtChunk)
	}
	content, _ := ups[0]["content"].(map[string]any)
	if content["text"] != "weighing two approaches" {
		t.Errorf("content = %v, want the reasoning text", ups[0]["content"])
	}
}

// The §6 mapping table's `usage_update {used, size, cost}` — a row that was
// never built, not a deferral: §12 lists what was deliberately left out and
// usage is not on it. UpdateUsage sat in schema.go with zero uses.
func TestUsageBecomesAUsageUpdate(t *testing.T) {
	s, buf := sinkSession(t)
	s.setModel("anthropic", "claude-sonnet-4-5")

	s.translateEvent(core.EvUsage{
		Usage: provider.Usage{InputTokens: 10, OutputTokens: 5},
		Cumulative: provider.Usage{
			InputTokens: 1000, CacheReadTokens: 4000, CacheWriteTokens: 500,
			OutputTokens: 700, CostUSD: 0.42,
		},
	})

	ups := updates(t, buf)
	if len(ups) != 1 {
		t.Fatalf("got %d updates, want 1: %v", len(ups), ups)
	}
	if got := ups[0]["sessionUpdate"]; got != UpdateUsage {
		t.Errorf("sessionUpdate = %v, want %q", got, UpdateUsage)
	}
	// used is the PROMPT, so cache reads and writes count: they occupy the
	// window exactly like fresh input, and the editor draws them against size.
	if got := ups[0]["used"]; got != float64(5500) {
		t.Errorf("used = %v, want 5500 (1000 input + 4000 cache read + 500 cache write) — "+
			"a count that omits cached tokens understates a warm session by most of its context", got)
	}
	if got := ups[0]["cost"]; got != 0.42 {
		t.Errorf("cost = %v, want the CUMULATIVE cost", got)
	}
	if got := ups[0]["size"]; got == nil {
		t.Error("size is absent for a known model; the editor cannot draw a gauge without it")
	}
}

// Cumulative, not per-request: the field is a session total, and sending the
// last request's usage would make the editor's stats jump backwards every turn.
func TestUsageUpdateReportsTheSessionTotalNotTheLastRequest(t *testing.T) {
	s, buf := sinkSession(t)
	s.translateEvent(core.EvUsage{
		Usage:      provider.Usage{InputTokens: 7, CostUSD: 0.01},
		Cumulative: provider.Usage{InputTokens: 900, CostUSD: 1.25},
	})

	ups := updates(t, buf)
	if len(ups) != 1 {
		t.Fatalf("got %d updates", len(ups))
	}
	if got := ups[0]["used"]; got == float64(7) {
		t.Error("used carries the LAST REQUEST's tokens; the editor's total would drop each turn")
	}
	if got := ups[0]["used"]; got != float64(900) {
		t.Errorf("used = %v, want the cumulative 900", got)
	}
	if got := ups[0]["cost"]; got != 1.25 {
		t.Errorf("cost = %v, want the cumulative 1.25", got)
	}
}

// An unknown model has no window. Omitting size is honest; a zero would draw a
// full gauge, and a guess would draw a wrong one.
func TestUsageUpdateOmitsSizeForAnUnknownModel(t *testing.T) {
	s, buf := sinkSession(t)
	s.setModel("nonesuch", "not-a-model")
	s.translateEvent(core.EvUsage{Cumulative: provider.Usage{InputTokens: 10}})

	ups := updates(t, buf)
	if len(ups) != 1 {
		t.Fatalf("got %d updates", len(ups))
	}
	if _, ok := ups[0]["size"]; ok {
		t.Errorf("size = %v for an unknown model; absent is the honest answer", ups[0]["size"])
	}
}
