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
		{"tool_result edit stats", EvToolResult{ID: "t2", Result: ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: "diff"}}, LinesAdded: 4, LinesRemoved: 2,
		}}, `{"type":"tool_result","id":"t2","content":[{"type":"text","text":"diff"}],"lines_added":4,"lines_removed":2}`},
		{"usage", EvUsage{
			Usage:      provider.Usage{InputTokens: 10, OutputTokens: 2, CostUSD: 0.01},
			Cumulative: provider.Usage{InputTokens: 100, OutputTokens: 20, CostUSD: 0.1},
		}, `{"type":"usage","usage":{"input":10,"output":2,"cache_read":0,"cache_write":0,"cost_usd":0.01},"cumulative":{"input":100,"output":20,"cache_read":0,"cache_write":0,"cost_usd":0.1}}`},
		{"user_message_rejected", EvUserMessageRejected{Text: "do the bad thing", Reason: "blocked by extension guard"},
			`{"type":"user_message_rejected","text":"blocked by extension guard"}`},
		{"turn_end clean", EvTurnEnd{Stop: provider.StopEnd}, `{"type":"turn_end","stop":"end"}`},
		{"turn_end error", EvTurnEnd{Stop: provider.StopError, Err: errors.New("boom")},
			`{"type":"turn_end","stop":"error","error":"boom"}`},
		{"stall", EvStall{StallRecord: StallRecord{Axis: "churn", Tool: "task_update", Detail: "activate_next must name a different task"}},
			`{"type":"stall","stall":{"axis":"churn","tool":"task_update","detail":"activate_next must name a different task"}}`},
		{"escalation", EvEscalation{EscalationRecord: EscalationRecord{
			Reason: "stuck on task_update", Tool: "task_update", FromModel: "gemma-4-26b",
			ToProvider: "openai-codex", ToModel: "gpt-5.6-sol", Auto: true, Disposition: EscalationSwitched,
		}}, `{"type":"escalation","escalation":{"reason":"stuck on task_update","tool":"task_update","from_model":"gemma-4-26b","to_provider":"openai-codex","to_model":"gpt-5.6-sol","auto":true,"disposition":"switched"}}`},
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

// TestMessageWireRoundTrip: MessageFromWire must invert MessageToWire for
// everything a transcript renderer needs — a control-plane client rebuilds
// its whole transcript through this pair (ctrlproto snapshots + message
// events). Images are the one deliberate loss: data-less, mime kept.
func TestMessageWireRoundTrip(t *testing.T) {
	when := time.Date(2026, 7, 4, 9, 30, 0, 0, time.UTC)
	orig := provider.Message{
		Role: provider.RoleAssistant,
		Time: when,
		Meta: map[string]string{MetaSynthetic: "true"},
		Content: []provider.Content{
			provider.TextBlock{Text: "plan"},
			provider.ToolCallBlock{ID: "t1", Name: "edit", Arguments: []byte(`{"path":"a.go"}`)},
			provider.ReasoningBlock{ID: "rs_1", Summary: "weighing", Encrypted: "OPAQUE"},
			provider.ImageBlock{MimeType: "image/png", Data: []byte{1, 2, 3}},
		},
	}
	got := MessageFromWire(MessageToWire(orig))
	if got.Role != orig.Role || !got.Time.Equal(when) || got.Meta[MetaSynthetic] != "true" {
		t.Fatalf("envelope did not round-trip: %+v", got)
	}
	if len(got.Content) != 4 {
		t.Fatalf("blocks = %d, want 4", len(got.Content))
	}
	if tb := got.Content[0].(provider.TextBlock); tb.Text != "plan" {
		t.Errorf("text = %+v", tb)
	}
	if tc := got.Content[1].(provider.ToolCallBlock); tc.ID != "t1" || tc.Name != "edit" || string(tc.Arguments) != `{"path":"a.go"}` {
		t.Errorf("tool_call = %+v", tc)
	}
	if rb := got.Content[2].(provider.ReasoningBlock); rb.ID != "rs_1" || rb.Summary != "weighing" || rb.Encrypted != "OPAQUE" {
		t.Errorf("reasoning = %+v", rb)
	}
	if ib := got.Content[3].(provider.ImageBlock); ib.MimeType != "image/png" || len(ib.Data) != 0 {
		t.Errorf("image = mime %q data %d bytes, want data-less with mime kept", ib.MimeType, len(ib.Data))
	}

	// Nested tool results (the per-step RoleTool message) round-trip too.
	tools := provider.Message{Role: provider.RoleTool, Content: []provider.Content{
		provider.ToolResultBlock{CallID: "t1", IsError: true, Content: []provider.Content{provider.TextBlock{Text: "boom"}}},
	}}
	back := MessageFromWire(MessageToWire(tools))
	tr := back.Content[0].(provider.ToolResultBlock)
	if back.Role != provider.RoleTool || tr.CallID != "t1" || !tr.IsError ||
		tr.Content[0].(provider.TextBlock).Text != "boom" {
		t.Fatalf("tool-result message did not round-trip: %+v", back)
	}
	// A plain message must not grow synthetic meta.
	if m := MessageFromWire(MessageToWire(provider.Message{Role: provider.RoleUser})); m.Meta != nil {
		t.Fatalf("plain message grew meta: %+v", m.Meta)
	}
}

// A directed line (Phase 6) surfaces typed Directed/Actor attribution on the
// wire and round-trips back into its source/actor meta. A narrator beat is
// directed with no actor; a plain assistant message stays undirected.
func TestMessageWireDirected(t *testing.T) {
	actor := provider.Message{
		Role:    provider.RoleAssistant,
		Meta:    map[string]string{MetaSource: MetaDirected, MetaActor: "Kael"},
		Content: []provider.Content{provider.TextBlock{Text: "You're late."}},
	}
	w := MessageToWire(actor)
	if !w.Directed || w.Actor != "Kael" {
		t.Fatalf("actor line: directed=%v actor=%q, want true/Kael", w.Directed, w.Actor)
	}
	back := MessageFromWire(w)
	if back.Meta[MetaSource] != MetaDirected || back.Meta[MetaActor] != "Kael" {
		t.Errorf("actor meta did not round-trip: %+v", back.Meta)
	}

	beat := MessageToWire(provider.Message{
		Role: provider.RoleAssistant,
		Meta: map[string]string{MetaSource: MetaDirected},
	})
	if !beat.Directed || beat.Actor != "" {
		t.Errorf("narrator beat: directed=%v actor=%q, want true/empty", beat.Directed, beat.Actor)
	}
	if b := MessageFromWire(beat); b.Meta[MetaSource] != MetaDirected || b.Meta[MetaActor] != "" {
		t.Errorf("narrator meta did not round-trip: %+v", b.Meta)
	}

	if plain := MessageToWire(provider.Message{Role: provider.RoleAssistant}); plain.Directed || plain.Actor != "" {
		t.Errorf("plain assistant message read as directed: %+v", plain)
	}
}

// TestEventToWireFullImageData: the Full conversions carry image payloads
// (Data alongside the usual size metadata) where the lean ones stay
// size-only — the split the control plane relies on: broadcast full, strip
// at serialized carrier boundaries unless "image-data" was negotiated.
func TestEventToWireFullImageData(t *testing.T) {
	msg := provider.Message{Role: provider.RoleUser, Content: []provider.Content{
		provider.TextBlock{Text: "look"},
		provider.ImageBlock{MimeType: "image/png", Data: []byte{1, 2, 3}},
	}}
	full := EventToWireFull(EvUserMessage{Message: msg})
	if b := full.Message.Content[1]; string(b.Data) != "\x01\x02\x03" || b.Bytes != 3 || b.MimeType != "image/png" {
		t.Fatalf("full form = %+v, want data plus size metadata", b)
	}
	lean := EventToWire(EvUserMessage{Message: msg})
	if b := lean.Message.Content[1]; b.Data != nil || b.Bytes != 3 {
		t.Fatalf("lean form = %+v, want size-only", b)
	}

	// Tool results carry their screenshots the same way (nested content).
	res := EvToolResult{ID: "t1", Result: ToolResult{Content: []provider.Content{
		provider.ImageBlock{MimeType: "image/png", Data: []byte{9}},
	}}}
	if b := EventToWireFull(res).Result[0]; len(b.Data) != 1 {
		t.Fatalf("full tool result = %+v, want data", b)
	}
	if b := EventToWire(res).Result[0]; b.Data != nil {
		t.Fatalf("lean tool result = %+v, want size-only", b)
	}

	// The full form round-trips to real pixels client-side.
	back := MessageFromWire(MessageToWireFull(msg))
	ib := back.Content[1].(provider.ImageBlock)
	if string(ib.Data) != "\x01\x02\x03" || ib.MimeType != "image/png" {
		t.Fatalf("round-trip lost pixels: %+v", ib)
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
