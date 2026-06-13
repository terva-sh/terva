package extproto

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestGoldenFrames pins the exact bytes of the extension wire format.
// Extension wire compatibility is an explicit invariant of the
// zot → terva rename (docs/plans/rename-terva.md): third-party
// extensions are deployed independently and parse these field names —
// including zot_version, which stays even after the rename
// (terva_version is additive). A failure here means the wire changed;
// either revert or treat it as a protocol version event, never a
// rename sweep.
func TestGoldenFrames(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		{
			"hello",
			HelloFromExt{Type: "hello", Name: "guard", Version: "1.0.0", Capabilities: []string{"commands"}},
			`{"type":"hello","name":"guard","version":"1.0.0","capabilities":["commands"]}`,
		},
		{
			"hello_ack",
			HelloAckFromHost{Type: "hello_ack", ProtocolVersion: 1, ZotVersion: "1.2.3",
				Provider: "anthropic", Model: "claude-sonnet-4-5", CWD: "/work", ExtensionDir: "/x", DataDir: "/x"},
			`{"type":"hello_ack","protocol_version":1,"zot_version":"1.2.3","provider":"anthropic","model":"claude-sonnet-4-5","cwd":"/work","extension_dir":"/x","data_dir":"/x"}`,
		},
		{
			// The rename bridge: terva_version rides alongside; the
			// zot_version key must never disappear or be renamed.
			"hello_ack both naming eras",
			HelloAckFromHost{Type: "hello_ack", ProtocolVersion: 1, ZotVersion: "1.2.3", TervaVersion: "1.2.3",
				Provider: "anthropic", Model: "m", CWD: "/work"},
			`{"type":"hello_ack","protocol_version":1,"zot_version":"1.2.3","terva_version":"1.2.3","provider":"anthropic","model":"m","cwd":"/work"}`,
		},
		{
			"register_command",
			RegisterCommandFromExt{Type: "register_command", Name: "deploy", Description: "ship it"},
			`{"type":"register_command","name":"deploy","description":"ship it"}`,
		},
		{
			"register_tool",
			RegisterToolFromExt{Type: "register_tool", Name: "lookup", Schema: json.RawMessage(`{"type":"object"}`)},
			`{"type":"register_tool","name":"lookup","schema":{"type":"object"}}`,
		},
		{
			"register_tool read-only",
			RegisterToolFromExt{Type: "register_tool", Name: "peek", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true},
			`{"type":"register_tool","name":"peek","schema":{"type":"object"},"read_only":true}`,
		},
		{
			"ready",
			ReadyFromExt{Type: "ready"},
			`{"type":"ready"}`,
		},
		{
			"subscribe",
			SubscribeFromExt{Type: "subscribe", Events: []string{"turn_end"}, Intercept: []string{"tool_call"}},
			`{"type":"subscribe","events":["turn_end"],"intercept":["tool_call"]}`,
		},
		{
			"command_invoked",
			CommandInvokedFromHost{Type: "command_invoked", ID: "1", Name: "deploy", Args: "prod"},
			`{"type":"command_invoked","id":"1","name":"deploy","args":"prod"}`,
		},
		{
			"command_response",
			CommandResponseFromExt{Type: "command_response", ID: "1", Action: "display", Display: "done"},
			`{"type":"command_response","id":"1","action":"display","display":"done"}`,
		},
		{
			"tool_call",
			ToolCallFromHost{Type: "tool_call", ID: "2", Name: "lookup", Args: json.RawMessage(`{"q":"x"}`)},
			`{"type":"tool_call","id":"2","name":"lookup","args":{"q":"x"}}`,
		},
		{
			"tool_result",
			ToolResultFromExt{Type: "tool_result", ID: "2", Content: []ContentBlock{{Type: "text", Text: "hit"}}},
			`{"type":"tool_result","id":"2","content":[{"type":"text","text":"hit"}]}`,
		},
		{
			"event",
			EventFromHost{Type: "event", Event: "tool_call", ToolID: "t1", ToolName: "bash", ToolArgs: json.RawMessage(`{}`)},
			`{"type":"event","event":"tool_call","tool_id":"t1","tool_name":"bash","tool_args":{}}`,
		},
		{
			// protocol 2: session identity rides on session_start.
			"event session_start",
			EventFromHost{Type: "event", Event: "session_start", SessionID: "s-1", SessionPath: "/p.tervasession", SessionTitle: "My Session"},
			`{"type":"event","event":"session_start","session_id":"s-1","session_path":"/p.tervasession","session_title":"My Session"}`,
		},
		{
			"event_intercept_response",
			EventInterceptResponseFromExt{Type: "event_intercept_response", ID: "3", Block: true, Reason: "nope"},
			`{"type":"event_intercept_response","id":"3","block":true,"reason":"nope"}`,
		},
		{
			"notify",
			NotifyFromExt{Type: "notify", Level: "warn", Message: "careful"},
			`{"type":"notify","level":"warn","message":"careful"}`,
		},
		{
			"register_context",
			RegisterContextFromExt{Type: "register_context", Text: "keep one task active"},
			`{"type":"register_context","text":"keep one task active"}`,
		},
		{
			"context_card",
			ContextCardFromExt{Type: "context_card", ID: "tasks", Label: "Tasks", Text: "active foo", Priority: 1},
			`{"type":"context_card","id":"tasks","label":"Tasks","text":"active foo","priority":1}`,
		},
		{
			"context_card minimal",
			ContextCardFromExt{Type: "context_card", ID: "tasks", Text: "active foo"},
			`{"type":"context_card","id":"tasks","text":"active foo"}`,
		},
		{
			"context_card blocking",
			ContextCardFromExt{Type: "context_card", ID: "tasks", Text: "active foo", Blocking: true},
			`{"type":"context_card","id":"tasks","text":"active foo","blocking":true}`,
		},
		{
			"context_card_clear",
			ContextCardClearFromExt{Type: "context_card_clear", ID: "tasks"},
			`{"type":"context_card_clear","id":"tasks"}`,
		},
		{
			"status_segment",
			StatusSegmentFromExt{Type: "status_segment", ID: "tasks", Text: "▸ patch (1/4)"},
			`{"type":"status_segment","id":"tasks","text":"▸ patch (1/4)"}`,
		},
		{
			"shutdown",
			ShutdownFromHost{Type: "shutdown"},
			`{"type":"shutdown"}`,
		},
		{
			"shutdown_ack",
			ShutdownAckFromExt{Type: "shutdown_ack"},
			`{"type":"shutdown_ack"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Encode(tc.v)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if s := strings.TrimSuffix(string(got), "\n"); s != tc.want {
				t.Errorf("frame bytes drifted\n got: %s\nwant: %s", s, tc.want)
			}
		})
	}
}

// TestGoldenDecode pins the read direction: frames written by an old
// extension must keep decoding, and unknown fields (a newer peer)
// must not break older readers.
func TestGoldenDecode(t *testing.T) {
	var ack HelloAckFromHost
	line := `{"type":"hello_ack","protocol_version":1,"zot_version":"0.9","provider":"openai","model":"m","cwd":"/w","future_field":42}`
	if err := json.Unmarshal([]byte(line), &ack); err != nil {
		t.Fatalf("decode hello_ack with unknown field: %v", err)
	}
	if ack.ZotVersion != "0.9" || ack.TervaVersion != "" || ack.Provider != "openai" {
		t.Errorf("hello_ack fields: %+v", ack)
	}

	var tr ToolResultFromExt
	line = `{"type":"tool_result","id":"7","content":[{"type":"image","mime_type":"image/png","data":"QUJD"}],"is_error":true}`
	if err := json.Unmarshal([]byte(line), &tr); err != nil {
		t.Fatalf("decode tool_result: %v", err)
	}
	if tr.ID != "7" || !tr.IsError || len(tr.Content) != 1 || tr.Content[0].MimeType != "image/png" {
		t.Errorf("tool_result fields: %+v", tr)
	}
}
