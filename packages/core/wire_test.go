package core

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// TestEventToWireGolden pins the canonical wire schema. These strings
// are the cross-surface contract (--json, RPC, SDK, swarm logs) that
// docs/rpc.md documents — change them only deliberately, with a
// protocol_version bump for anything backwards-incompatible.
func TestEventToWireGolden(t *testing.T) {
	when := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	msg := provider.Message{
		Role: provider.RoleAssistant,
		Time: when,
		Content: []provider.Content{
			provider.TextBlock{Text: "hi"},
			provider.ImageBlock{MimeType: "image/png", Data: []byte{1, 2, 3}},
		},
	}

	cases := []struct {
		name string
		ev   AgentEvent
		want string
	}{
		{"turn_start", EvTurnStart{Step: 3}, `{"type":"turn_start","step":3}`},
		{"text_delta", EvTextDelta{Delta: "he"}, `{"type":"text_delta","delta":"he"}`},
		{"assistant_message", EvAssistantMessage{Message: msg},
			`{"type":"assistant_message","message":{"role":"assistant","content":[{"type":"text","text":"hi"},{"type":"image","mime_type":"image/png","bytes":3}],"time":"2026-06-10T12:00:00Z"}}`},
		{"tool_use_start", EvToolUseStart{ID: "t1", Name: "read"}, `{"type":"tool_use_start","id":"t1","name":"read"}`},
		{"tool_use_args", EvToolUseArgs{ID: "t1", Delta: `{"pa`}, `{"type":"tool_use_args","delta":"{\"pa","id":"t1"}`},
		{"tool_use_end", EvToolUseEnd{ID: "t1"}, `{"type":"tool_use_end","id":"t1"}`},
		{"tool_call", EvToolCall{ID: "t1", Name: "read", Args: json.RawMessage(`{"path":"a"}`)},
			`{"type":"tool_call","id":"t1","name":"read","args":{"path":"a"}}`},
		{"tool_result", EvToolResult{ID: "t1", Result: ToolResult{IsError: true, Content: []provider.Content{provider.TextBlock{Text: "boom"}}}},
			`{"type":"tool_result","id":"t1","is_error":true,"content":[{"type":"text","text":"boom"}]}`},
		{"usage", EvUsage{
			Usage:      provider.Usage{InputTokens: 10, OutputTokens: 2, CostUSD: 0.01},
			Cumulative: provider.Usage{InputTokens: 100, OutputTokens: 20, CostUSD: 0.1},
		}, `{"type":"usage","usage":{"input":10,"output":2,"cache_read":0,"cache_write":0,"cost_usd":0.01},"cumulative":{"input":100,"output":20,"cache_read":0,"cache_write":0,"cost_usd":0.1}}`},
		{"turn_end clean", EvTurnEnd{Stop: provider.StopEnd}, `{"type":"turn_end","stop":"end"}`},
		{"turn_end error", EvTurnEnd{Stop: provider.StopError, Err: errors.New("boom")},
			`{"type":"turn_end","stop":"error","error":"boom"}`},
	}
	for _, c := range cases {
		b, err := json.Marshal(EventToWire(c.ev))
		if err != nil {
			t.Fatalf("%s: marshal: %v", c.name, err)
		}
		if string(b) != c.want {
			t.Errorf("%s:\ngot:  %s\nwant: %s", c.name, b, c.want)
		}
	}
}

// TestContentToWireReasoning: reasoning blocks must survive the
// canonical wire so providers that replay encrypted chain-of-thought
// (OpenAI Codex with thinking) keep it across get_messages / SDK
// round-trips instead of silently dropping it.
func TestContentToWireReasoning(t *testing.T) {
	blocks := ContentToWire([]provider.Content{
		provider.ReasoningBlock{ID: "rs_1", Summary: "weighing options", Encrypted: "OPAQUE"},
	})
	if len(blocks) != 1 {
		t.Fatalf("want 1 wire block, got %d", len(blocks))
	}
	b := blocks[0]
	if b.Type != "reasoning" || b.ReasoningID != "rs_1" || b.Summary != "weighing options" || b.Encrypted != "OPAQUE" {
		t.Fatalf("reasoning block did not round-trip to wire: %+v", b)
	}
	got, _ := json.Marshal(b)
	want := `{"type":"reasoning","reasoning_id":"rs_1","summary":"weighing options","encrypted_content":"OPAQUE"}`
	if string(got) != want {
		t.Errorf("reasoning wire JSON:\ngot:  %s\nwant: %s", got, want)
	}
}

// TestWireEventMapMatchesStruct: the Map() view (used by emitters
// that flatten into their own envelopes) must carry exactly the
// struct's JSON fields.
func TestWireEventMapMatchesStruct(t *testing.T) {
	ev := EventToWire(EvToolCall{ID: "t1", Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)})
	structJSON, _ := json.Marshal(ev)
	mapJSON, _ := json.Marshal(ev.Map())

	var a, b map[string]any
	_ = json.Unmarshal(structJSON, &a)
	_ = json.Unmarshal(mapJSON, &b)
	if len(a) != len(b) {
		t.Fatalf("field sets differ: struct %v vs map %v", a, b)
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			t.Errorf("map view missing %q", k)
		}
	}
}

// TestMessageToWireSynthetic: a host-injected (synthetic-meta) user message maps
// to WireMessage.Synthetic so display surfaces can de-emphasize it; a plain
// message does not.
func TestMessageToWireSynthetic(t *testing.T) {
	nudge := provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "finish the open items"}},
		Meta:    map[string]string{MetaSynthetic: "true"},
	}
	if !MessageToWire(nudge).Synthetic {
		t.Error("synthetic meta should set WireMessage.Synthetic")
	}
	plain := provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}}
	if MessageToWire(plain).Synthetic {
		t.Error("a plain user message must not be marked synthetic")
	}
}
