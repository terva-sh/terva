package extproto

import (
	"bytes"
	"encoding/json"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// goldenFrame is one entry in the corpus: a value, the exact bytes
// Encode must produce for it, and a name that identifies it both in test
// failures and in the published fixture.
type goldenFrame struct {
	name string
	v    any
	want string
}

// goldenFrames pins the exact bytes of the extension wire format.
//
// Extension wire compatibility is an explicit invariant of the
// zot → terva rename (docs/plans/rename-terva.md): third-party
// extensions are deployed independently and parse these field names —
// including zot_version, which stays even after the rename
// (terva_version is additive). A failure here means the wire changed;
// either revert or treat it as a protocol version event, never a rename
// sweep.
//
// It aims to cover every frame type in the protocol, not a sample: the
// corpus is the conformance oracle for SDKs written in other languages
// (terva-sdk-rust's terva-extproto), and a frame it omits is a frame
// they have to reverse-engineer out of extproto.go by hand. Prefer
// pinning the shape a real producer emits — including the awkward
// cases — over a tidy one: a nil Go slice marshals to null where an
// empty Rust Vec marshals to [], and that difference surfaces only here.
//
// It is package scope rather than a local because three other tests read
// it: the encoder-neutrality guard, the name-uniqueness guard, and the
// fixture that publishes this corpus.
var goldenFrames = []goldenFrame{
	// --- registration phase -------------------------------------------
	{
		// Sent BEFORE hello by a launcher that still has to build the
		// real process. Each one restarts the hello deadline.
		"bootstrap",
		BootstrapFromExt{Type: "bootstrap", Message: "compiling extension"},
		`{"type":"bootstrap","message":"compiling extension"}`,
	},
	{
		"bootstrap bare",
		BootstrapFromExt{Type: "bootstrap"},
		`{"type":"bootstrap"}`,
	},
	{
		"hello",
		HelloFromExt{Type: "hello", Name: "guard", Version: "1.0.0", Capabilities: []string{"commands"}},
		`{"type":"hello","name":"guard","version":"1.0.0","capabilities":["commands"]}`,
	},
	{
		// min_protocol is how an extension refuses to load against a host
		// older than the wire it speaks. Absent (0) means "no minimum".
		"hello with min_protocol",
		HelloFromExt{Type: "hello", Name: "vault", Version: "2.1.0", MinProtocol: 6},
		`{"type":"hello","name":"vault","version":"2.1.0","min_protocol":6}`,
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
		"register_tool authority",
		RegisterToolFromExt{Type: "register_tool", Name: "web_fetch", Schema: json.RawMessage(`{"type":"object"}`), Authority: "network-read"},
		`{"type":"register_tool","name":"web_fetch","schema":{"type":"object"},"authority":"network-read"}`,
	},
	{
		// essential pins THIS tool's visibility under lazy tool loading;
		// its siblings still lazy-load.
		"register_tool essential",
		RegisterToolFromExt{Type: "register_tool", Name: "search_index", Description: "search the index",
			Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true, Authority: "local-read", Essential: true},
		`{"type":"register_tool","name":"search_index","description":"search the index","schema":{"type":"object"},"read_only":true,"authority":"local-read","essential":true}`,
	},
	{
		"register_context",
		RegisterContextFromExt{Type: "register_context", Text: "keep one task active"},
		`{"type":"register_context","text":"keep one task active"}`,
	},
	{
		"subscribe",
		SubscribeFromExt{Type: "subscribe", Events: []string{"turn_end"}, Intercept: []string{"tool_call"}},
		`{"type":"subscribe","events":["turn_end"],"intercept":["tool_call"]}`,
	},
	{
		"ready",
		ReadyFromExt{Type: "ready"},
		`{"type":"ready"}`,
	},

	// --- session traffic ----------------------------------------------
	{
		"refresh_context",
		RefreshContextFromExt{Type: "refresh_context", Text: "project notes"},
		`{"type":"refresh_context","text":"project notes"}`,
	},
	{
		// A wholesale snapshot. Neither field carries omitempty, so both
		// travel on every frame — an SDK that omits them is not on wire.
		"set_withdrawn_tools",
		SetWithdrawnToolsFromExt{Type: "set_withdrawn_tools", Tools: []string{"apply_patch", "run"}},
		`{"type":"set_withdrawn_tools","tools":["apply_patch","run"],"all":false}`,
	},
	{
		// The restore path: the empty set, not a null. The producer builds
		// the slice with make(), so it is empty rather than nil.
		"set_withdrawn_tools restore",
		SetWithdrawnToolsFromExt{Type: "set_withdrawn_tools", Tools: []string{}},
		`{"type":"set_withdrawn_tools","tools":[],"all":false}`,
	},
	{
		"set_withdrawn_tools all",
		SetWithdrawnToolsFromExt{Type: "set_withdrawn_tools", Tools: []string{}, All: true},
		`{"type":"set_withdrawn_tools","tools":[],"all":true}`,
	},
	{
		"host_tool_call",
		HostToolCallFromExt{Type: "host_tool_call", ID: "c1", Name: "read", Args: json.RawMessage(`{"path":"x"}`), Silent: true},
		`{"type":"host_tool_call","id":"c1","name":"read","args":{"path":"x"},"silent":true}`,
	},
	{
		"list_sessions",
		ListSessionsFromExt{Type: "list_sessions", ID: "s1", ProjectID: "proj"},
		`{"type":"list_sessions","id":"s1","project_id":"proj"}`,
	},
	{
		"read_session",
		ReadSessionFromExt{Type: "read_session", ID: "s2", SessionID: "abc"},
		`{"type":"read_session","id":"s2","session_id":"abc"}`,
	},
	{
		"tool_result",
		ToolResultFromExt{Type: "tool_result", ID: "2", Content: []ContentBlock{{Type: "text", Text: "hit"}}},
		`{"type":"tool_result","id":"2","content":[{"type":"text","text":"hit"}]}`,
	},
	{
		"tool_result error",
		ToolResultFromExt{Type: "tool_result", ID: "7", Content: []ContentBlock{{Type: "text", Text: "no such index"}}, IsError: true},
		`{"type":"tool_result","id":"7","content":[{"type":"text","text":"no such index"}],"is_error":true}`,
	},
	{
		// An image block: base64 in data, never a path.
		"tool_result image",
		ToolResultFromExt{Type: "tool_result", ID: "8", Content: []ContentBlock{{Type: "image", MimeType: "image/png", Data: "QUJD"}}},
		`{"type":"tool_result","id":"8","content":[{"type":"image","mime_type":"image/png","data":"QUJD"}]}`,
	},
	{
		"command_response",
		CommandResponseFromExt{Type: "command_response", ID: "1", Action: "display", Display: "done"},
		`{"type":"command_response","id":"1","action":"display","display":"done"}`,
	},
	{
		"command_response prompt",
		CommandResponseFromExt{Type: "command_response", ID: "1", Action: "prompt", Prompt: "summarise the diff"},
		`{"type":"command_response","id":"1","action":"prompt","prompt":"summarise the diff"}`,
	},
	{
		"command_response open_panel",
		CommandResponseFromExt{Type: "command_response", ID: "1", Action: "open_panel",
			OpenPanel: &PanelSpec{ID: "todos-main", Title: "Todos", Lines: []string{"1. ship it"}, Footer: "q to close"}},
		`{"type":"command_response","id":"1","action":"open_panel","open_panel":{"id":"todos-main","title":"Todos","lines":["1. ship it"],"footer":"q to close"}}`,
	},
	{
		// An empty Action becomes "noop" at the SDK boundary, so the field
		// is never blank on the wire.
		"command_response noop",
		CommandResponseFromExt{Type: "command_response", ID: "1", Action: "noop"},
		`{"type":"command_response","id":"1","action":"noop"}`,
	},
	{
		"command_response error",
		CommandResponseFromExt{Type: "command_response", ID: "1", Action: "error", Error: "no such target"},
		`{"type":"command_response","id":"1","action":"error","error":"no such target"}`,
	},
	{
		"event_intercept_response",
		EventInterceptResponseFromExt{Type: "event_intercept_response", ID: "3", Block: true, Reason: "nope"},
		`{"type":"event_intercept_response","id":"3","block":true,"reason":"nope"}`,
	},
	{
		// The rewrite path: allow the call, but with different args.
		"event_intercept_response modified args",
		EventInterceptResponseFromExt{Type: "event_intercept_response", ID: "4", ModifiedArgs: json.RawMessage(`{"cmd":"ls -a"}`)},
		`{"type":"event_intercept_response","id":"4","modified_args":{"cmd":"ls -a"}}`,
	},
	{
		"event_intercept_response replace text",
		EventInterceptResponseFromExt{Type: "event_intercept_response", ID: "5", ReplaceText: "redacted"},
		`{"type":"event_intercept_response","id":"5","replace_text":"redacted"}`,
	},
	{
		"notify",
		NotifyFromExt{Type: "notify", Level: "warn", Message: "careful"},
		`{"type":"notify","level":"warn","message":"careful"}`,
	},
	{
		"clear_notes",
		ClearNotesFromExt{Type: "clear_notes"},
		`{"type":"clear_notes"}`,
	},
	{
		"submit",
		SubmitFromExt{Type: "submit", Text: "review the failing test"},
		`{"type":"submit","text":"review the failing test"}`,
	},
	{
		"submit_slash",
		SubmitSlashFromExt{Type: "submit_slash", Text: "/compact"},
		`{"type":"submit_slash","text":"/compact"}`,
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
		// An empty Text clears the segment, and text carries omitempty —
		// so the clear frame is the id alone.
		"status_segment clear",
		StatusSegmentFromExt{Type: "status_segment", ID: "tasks"},
		`{"type":"status_segment","id":"tasks"}`,
	},
	{
		"shutdown_ack",
		ShutdownAckFromExt{Type: "shutdown_ack"},
		`{"type":"shutdown_ack"}`,
	},

	// --- panels (extension → host) -------------------------------------
	{
		"open_panel",
		OpenPanelFromExt{Type: "open_panel",
			Panel: PanelSpec{ID: "todos-main", Title: "Todos", Lines: []string{"1. ship it"}, Footer: "q to close"}},
		`{"type":"open_panel","panel":{"id":"todos-main","title":"Todos","lines":["1. ship it"],"footer":"q to close"}}`,
	},
	{
		"panel_render",
		PanelRenderFromExt{Type: "panel_render", PanelID: "todos-main", Title: "Todos",
			Lines: []string{"1. ship it"}, Footer: "q to close"},
		`{"type":"panel_render","panel_id":"todos-main","title":"Todos","lines":["1. ship it"],"footer":"q to close"}`,
	},
	{
		// The whole widget vocabulary in one frame — heading, text, meter,
		// keyvalue, table, list, group, note, action, divider — because a
		// rich frontend renders these natively and nothing else pins their
		// shape. Lines ride alongside as the TUI's text fallback.
		"panel_render with widgets",
		PanelRenderFromExt{Type: "panel_render", PanelID: "todos-main", Title: "Todos",
			Lines:  []string{"1. ship it"},
			Footer: "q to close",
			Widgets: []Widget{
				{Type: "heading", Text: "Todos", Level: 1},
				{Type: "text", Text: "3 of 8 done", Tone: "muted"},
				{Type: "meter", Label: "done", Value: 3, Max: 8, Unit: "tasks"},
				{Type: "keyvalue", Rows: []WidgetKV{
					{Key: "branch", Value: "main", Mono: true},
					{Key: "open", Value: "5", Note: "since Monday"},
				}},
				{Type: "table", Columns: []string{"id", "state"}, Cells: [][]string{{"t1", "open"}, {"t2", "done"}}},
				{Type: "list", Items: []WidgetItem{
					{Text: "ship it", Note: "today", Tone: "warn", ActionID: "open-t1"},
					{Text: "write it up"},
				}},
				{Type: "group", Children: []Widget{{Type: "note", Text: "nested", Tone: "ok"}}},
				{Type: "divider"},
				{Type: "action", Text: "Refresh", ActionID: "refresh"},
			}},
		`{"type":"panel_render","panel_id":"todos-main","title":"Todos","lines":["1. ship it"],"footer":"q to close","widgets":[{"type":"heading","text":"Todos","level":1},{"type":"text","text":"3 of 8 done","tone":"muted"},{"type":"meter","label":"done","value":3,"max":8,"unit":"tasks"},{"type":"keyvalue","rows":[{"key":"branch","value":"main","mono":true},{"key":"open","value":"5","note":"since Monday"}]},{"type":"table","columns":["id","state"],"cells":[["t1","open"],["t2","done"]]},{"type":"list","items":[{"text":"ship it","note":"today","tone":"warn","action_id":"open-t1"},{"text":"write it up"}]},{"type":"group","children":[{"type":"note","text":"nested","tone":"ok"}]},{"type":"divider"},{"type":"action","text":"Refresh","action_id":"refresh"}]}`,
	},
	{
		"panel_close from ext",
		PanelCloseFromExt{Type: "panel_close", PanelID: "todos-main"},
		`{"type":"panel_close","panel_id":"todos-main"}`,
	},

	// --- secrets (protocol 6, extension → host) -------------------------
	//
	// The requests carry NO scope: the driver substitutes the calling
	// extension's manifest name, so there is nothing here to forge.
	{
		"secret_set",
		SecretSetFromExt{Type: "secret_set", ID: "q1", Key: "oauth_token", Value: "tok-123"},
		`{"type":"secret_set","id":"q1","key":"oauth_token","value":"tok-123"}`,
	},
	{
		// An empty value DELETES host-side; the field still travels, since
		// Value carries no omitempty.
		"secret_set empty value",
		SecretSetFromExt{Type: "secret_set", ID: "q2", Key: "oauth_token"},
		`{"type":"secret_set","id":"q2","key":"oauth_token","value":""}`,
	},
	{
		"secret_get",
		SecretGetFromExt{Type: "secret_get", ID: "q3", Key: "oauth_token"},
		`{"type":"secret_get","id":"q3","key":"oauth_token"}`,
	},
	{
		"secret_delete",
		SecretDeleteFromExt{Type: "secret_delete", ID: "q4", Key: "oauth_token"},
		`{"type":"secret_delete","id":"q4","key":"oauth_token"}`,
	},
	{
		"secret_list",
		SecretListFromExt{Type: "secret_list", ID: "q5"},
		`{"type":"secret_list","id":"q5"}`,
	},

	// --- connector role (protocol 5) — the envelope only. The chat
	// session inside it is connproto, pinned by connproto's own golden
	// frames; nothing here mirrors that vocabulary.
	{
		"register_connector",
		RegisterConnectorFromExt{Type: "register_connector"},
		`{"type":"register_connector"}`,
	},
	{
		"chat envelope",
		ChatFrame{Type: "chat", ID: "s1", Frame: json.RawMessage(`{"type":"connect"}`)},
		`{"type":"chat","id":"s1","frame":{"type":"connect"}}`,
	},
	{
		"chat_down clean",
		ChatDownFromExt{Type: "chat_down", ID: "s1"},
		`{"type":"chat_down","id":"s1"}`,
	},
	{
		"chat_down error",
		ChatDownFromExt{Type: "chat_down", ID: "s1", Error: "auth revoked"},
		`{"type":"chat_down","id":"s1","error":"auth revoked"}`,
	},

	// --- host → extension: handshake ------------------------------------
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
		// supported_events is a capability list finer-grained than the
		// protocol version; the live roster is KnownEvents, and this pins
		// the field's SHAPE rather than that roster's membership.
		//
		// config carries THIS extension's resolved values. It is a Go map,
		// and encoding/json emits map keys SORTED — note api_base precedes
		// verbose here however the value was built. A language whose map
		// preserves insertion order will not reproduce these bytes unless
		// it sorts too.
		"hello_ack with supported_events and config",
		HelloAckFromHost{Type: "hello_ack", ProtocolVersion: 6, ZotVersion: "1.2.3", TervaVersion: "1.2.3",
			Provider: "anthropic", Model: "m", CWD: "/work",
			ExtensionDir:    "/install/todos",
			DataDir:         "/home/u/.terva/ext-data/todos",
			SupportedEvents: []string{"session_start", "turn_end", "config_update"},
			Config: map[string]json.RawMessage{
				"verbose":  json.RawMessage(`true`),
				"api_base": json.RawMessage(`"https://api.example.com"`),
			}},
		`{"type":"hello_ack","protocol_version":6,"zot_version":"1.2.3","terva_version":"1.2.3","provider":"anthropic","model":"m","cwd":"/work","extension_dir":"/install/todos","data_dir":"/home/u/.terva/ext-data/todos","supported_events":["session_start","turn_end","config_update"],"config":{"api_base":"https://api.example.com","verbose":true}}`,
	},

	// --- host → extension: session traffic -------------------------------
	{
		"host_tool_result",
		HostToolResultFromHost{Type: "host_tool_result", ID: "c1", Content: []ContentBlock{{Type: "text", Text: "ok"}}},
		`{"type":"host_tool_result","id":"c1","content":[{"type":"text","text":"ok"}]}`,
	},
	{
		"host_tool_result error",
		HostToolResultFromHost{Type: "host_tool_result", ID: "c2", Content: []ContentBlock{{Type: "text", Text: "denied by policy"}}, IsError: true},
		`{"type":"host_tool_result","id":"c2","content":[{"type":"text","text":"denied by policy"}],"is_error":true}`,
	},
	{
		"session_list",
		SessionListFromHost{Type: "session_list", ID: "s1", Sessions: []SessionInfo{{SessionID: "abc", Title: "hi", Messages: 3, ModTime: 42}}},
		`{"type":"session_list","id":"s1","sessions":[{"session_id":"abc","title":"hi","messages":3,"mtime":42}]}`,
	},
	{
		"session_data",
		SessionDataFromHost{Type: "session_data", ID: "s2", Messages: []SessionMessage{{Role: "user", Text: "hi"}}},
		`{"type":"session_data","id":"s2","messages":[{"role":"user","text":"hi"}]}`,
	},
	{
		// The not-found answer carries a NIL slice, and messages has no
		// omitempty — so it marshals to null, not []. A reader that types
		// this as a non-optional array breaks here and nowhere else.
		"session_data not found",
		SessionDataFromHost{Type: "session_data", ID: "s2", NotFound: true},
		`{"type":"session_data","id":"s2","messages":null,"not_found":true}`,
	},
	{
		"command_invoked",
		CommandInvokedFromHost{Type: "command_invoked", ID: "1", Name: "deploy", Args: "prod"},
		`{"type":"command_invoked","id":"1","name":"deploy","args":"prod"}`,
	},
	{
		"tool_call",
		ToolCallFromHost{Type: "tool_call", ID: "2", Name: "lookup", Args: json.RawMessage(`{"q":"x"}`)},
		`{"type":"tool_call","id":"2","name":"lookup","args":{"q":"x"}}`,
	},

	// --- host → extension: lifecycle events -------------------------------
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
		// cwd/project_id refresh on every session_start (including after a
		// /cd), unlike hello_ack's cwd which is frozen at the handshake.
		"event session_start with workspace",
		EventFromHost{Type: "event", Event: "session_start", SessionID: "s-1", SessionPath: "/p.tervasession",
			SessionTitle: "My Session", CWD: "/work/terva", ProjectID: "terva-9f2c"},
		`{"type":"event","event":"session_start","session_id":"s-1","session_path":"/p.tervasession","session_title":"My Session","cwd":"/work/terva","project_id":"terva-9f2c"}`,
	},
	{
		// An empty session_id means there is no active session.
		"event session_start no session",
		EventFromHost{Type: "event", Event: "session_start"},
		`{"type":"event","event":"session_start"}`,
	},
	{
		"event turn_end",
		EventFromHost{Type: "event", Event: "turn_end", Step: 2, Stop: "end_turn"},
		`{"type":"event","event":"turn_end","step":2,"stop":"end_turn"}`,
	},
	{
		"event tool_result error",
		EventFromHost{Type: "event", Event: "tool_result", ToolID: "t1", ToolName: "bash", Text: "exit status 1", IsError: true},
		`{"type":"event","event":"tool_result","tool_id":"t1","tool_name":"bash","text":"exit status 1","is_error":true}`,
	},
	{
		"event workspace_changed",
		EventFromHost{Type: "event", Event: "workspace_changed", Files: []FileChange{
			{Path: "packages/agent/extproto/extproto.go", Change: "modified"},
			{Path: "docs/notes.md", Change: "added"},
		}},
		`{"type":"event","event":"workspace_changed","files":[{"path":"packages/agent/extproto/extproto.go","change":"modified"},{"path":"docs/notes.md","change":"added"}]}`,
	},
	{
		// Same sorted-map note as hello_ack's config.
		"event config_update",
		EventFromHost{Type: "event", Event: "config_update", Config: map[string]json.RawMessage{
			"verbose":  json.RawMessage(`false`),
			"api_base": json.RawMessage(`"https://api.example.com"`),
		}},
		`{"type":"event","event":"config_update","config":{"api_base":"https://api.example.com","verbose":false}}`,
	},
	{
		"event run_end",
		EventFromHost{Type: "event", Event: "run_end", Error: "context canceled"},
		`{"type":"event","event":"run_end","error":"context canceled"}`,
	},
	{
		"event_intercept tool_call",
		EventInterceptFromHost{Type: "event_intercept", ID: "i1", Event: "tool_call",
			ToolID: "t1", ToolName: "bash", ToolArgs: json.RawMessage(`{"cmd":"ls"}`)},
		`{"type":"event_intercept","id":"i1","event":"tool_call","tool_id":"t1","tool_name":"bash","tool_args":{"cmd":"ls"}}`,
	},
	{
		"event_intercept turn_start",
		EventInterceptFromHost{Type: "event_intercept", ID: "i2", Event: "turn_start", Step: 3},
		`{"type":"event_intercept","id":"i2","event":"turn_start","step":3}`,
	},
	{
		"event_intercept assistant_message",
		EventInterceptFromHost{Type: "event_intercept", ID: "i3", Event: "assistant_message", Text: "the draft reply"},
		`{"type":"event_intercept","id":"i3","event":"assistant_message","text":"the draft reply"}`,
	},

	// --- host → extension: secrets (protocol 6) ---------------------------
	{
		"secret_value found",
		SecretValueFromHost{Type: "secret_value", ID: "q3", Found: true, Value: "tok-123"},
		`{"type":"secret_value","id":"q3","found":true,"value":"tok-123"}`,
	},
	{
		// Absent is not an error: found=false, and value is omitted rather
		// than sent empty — which is how "present and empty" stays distinct.
		"secret_value absent",
		SecretValueFromHost{Type: "secret_value", ID: "q3"},
		`{"type":"secret_value","id":"q3","found":false}`,
	},
	{
		"secret_value error",
		SecretValueFromHost{Type: "secret_value", ID: "q3", Error: "this host does not broker extension secrets"},
		`{"type":"secret_value","id":"q3","found":false,"error":"this host does not broker extension secrets"}`,
	},
	{
		// The store returns key names SORTED, so that is what travels —
		// never values, and never insertion order.
		"secret_keys",
		SecretKeysFromHost{Type: "secret_keys", ID: "q5", Keys: []string{"cursor", "oauth_token"}},
		`{"type":"secret_keys","id":"q5","keys":["cursor","oauth_token"]}`,
	},
	{
		// An extension holding no secrets: the store returns a NIL slice
		// and keys has no omitempty, so the frame carries null — where an
		// SDK modelling keys as a plain list would emit []. This frame is
		// the reason to read the corpus instead of the struct.
		"secret_keys empty",
		SecretKeysFromHost{Type: "secret_keys", ID: "q5"},
		`{"type":"secret_keys","id":"q5","keys":null}`,
	},
	{
		"secret_ack",
		SecretAckFromHost{Type: "secret_ack", ID: "q1"},
		`{"type":"secret_ack","id":"q1"}`,
	},
	{
		"secret_ack error",
		SecretAckFromHost{Type: "secret_ack", ID: "q1", Error: "this host does not broker extension secrets"},
		`{"type":"secret_ack","id":"q1","error":"this host does not broker extension secrets"}`,
	},

	// --- host → extension: panels and lifecycle ---------------------------
	{
		"panel_key",
		PanelKeyFromHost{Type: "panel_key", PanelID: "todos-main", Key: "rune", Text: "y"},
		`{"type":"panel_key","panel_id":"todos-main","key":"rune","text":"y"}`,
	},
	{
		// SPEC-ONLY: no host emits this frame, and none ever has — there
		// is no producer in the tree, no Driver.SendPanelResize, and no
		// SDK handler. Panels re-render on the extension's own
		// panel_render cadence instead. It is pinned because the corpus
		// publishes the wire SPEC, so an SDK that chooses to decode it
		// defensively gets the exact bytes rather than a guess; that is
		// not a promise it will arrive. See docs/extensions.md.
		//
		// Wiring a producer is a behaviour change, not a gap-fill: it
		// would reverse a decision the docs state outright.
		"panel_resize",
		PanelResizeFromHost{Type: "panel_resize", PanelID: "todos-main", Width: 80, Height: 24},
		`{"type":"panel_resize","panel_id":"todos-main","width":80,"height":24}`,
	},
	{
		"panel_close from host",
		PanelCloseFromHost{Type: "panel_close", PanelID: "todos-main"},
		`{"type":"panel_close","panel_id":"todos-main"}`,
	},
	{
		"chat_open",
		ChatOpenFromHost{Type: "chat_open", ID: "s1"},
		`{"type":"chat_open","id":"s1"}`,
	},
	{
		"chat_close",
		ChatCloseFromHost{Type: "chat_close", ID: "s1"},
		`{"type":"chat_close","id":"s1"}`,
	},
	{
		"shutdown",
		ShutdownFromHost{Type: "shutdown"},
		`{"type":"shutdown"}`,
	},
}

// TestGoldenFrames checks every corpus entry encodes to its pinned bytes.
func TestGoldenFrames(t *testing.T) {
	for _, tc := range goldenFrames {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Encode(tc.v)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if !strings.HasSuffix(string(got), "\n") {
				t.Fatalf("frame not LF-terminated: %q", got)
			}
			if s := strings.TrimSuffix(string(got), "\n"); s != tc.want {
				t.Errorf("frame bytes drifted\n got: %s\nwant: %s", s, tc.want)
			}
		})
	}
}

// TestGoldenNamesAreUnique keeps every entry addressable. Consumers key
// the published corpus by name — terva-sdk-rust classifies each line and
// asserts per case — so two entries sharing one name silently collapse
// into one there while both still pass here. Two frames genuinely DO
// share bytes (panel_close travels in both directions), which is exactly
// how the collision arises without anyone noticing.
func TestGoldenNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, tc := range goldenFrames {
		if seen[tc.name] {
			t.Errorf("duplicate golden frame name %q — names address entries in the "+
				"published fixture and must be unique", tc.name)
		}
		seen[tc.name] = true
	}
}

// Frame directions as published in the fixture. An extension only ever
// ENCODES ext_to_host frames, so an SDK asserts those byte-exact and the
// rest on decode; publishing the split saves every SDK from re-deriving
// a classification we already know.
const (
	dirExtToHost = "ext_to_host"
	dirHostToExt = "host_to_ext"
	dirBoth      = "both"
)

// bidirectionalFrames names the wire types BOTH sides send, which
// therefore carry neither naming suffix. Keyed by Go type name.
var bidirectionalFrames = map[string]string{
	"ChatFrame": dirBoth,
}

// frameDirection derives a frame's direction from its Go type name
// rather than from a hand-kept table, so a frame added tomorrow is
// classified without anyone remembering to classify it. Every wire type
// in this package carries a FromExt / FromHost suffix; one that carries
// neither and is not a known bidirectional type fails here rather than
// being published with a guess.
func frameDirection(t *testing.T, v any) string {
	t.Helper()
	name := reflect.TypeOf(v).Name()
	if dir, ok := bidirectionalFrames[name]; ok {
		return dir
	}
	switch {
	case strings.HasSuffix(name, "FromExt"):
		return dirExtToHost
	case strings.HasSuffix(name, "FromHost"):
		return dirHostToExt
	}
	t.Fatalf("cannot classify frame type %s: wire types carry a FromExt or FromHost "+
		"suffix, and a type carrying neither must be listed in bidirectionalFrames. "+
		"The published fixture reports a direction per frame and must not guess one.", name)
	return ""
}

// TestFrameDirectionHasTeeth proves the classifier discriminates rather
// than defaulting. A deriver that returned one answer for everything
// would pass the fixture test silently while publishing a corpus in
// which half the directions are wrong.
func TestFrameDirectionHasTeeth(t *testing.T) {
	for _, tc := range []struct {
		v    any
		want string
	}{
		{HelloFromExt{}, dirExtToHost},
		{SecretSetFromExt{}, dirExtToHost},
		{HelloAckFromHost{}, dirHostToExt},
		{PanelCloseFromHost{}, dirHostToExt},
		{ChatFrame{}, dirBoth},
	} {
		if got := frameDirection(t, tc.v); got != tc.want {
			t.Errorf("%T: direction %q, want %q", tc.v, got, tc.want)
		}
	}
}

// declaredFrameTypes returns every wire frame type declared in
// extproto.go, read from the SOURCE because Go has no runtime registry
// of a package's types — reflection can describe a value you already
// have, never enumerate the ones you forgot.
//
// The filter is the package's own naming convention: a frame type
// carries a FromExt or FromHost suffix. Nested payload types
// (ContentBlock, PanelSpec, Widget, SessionInfo, …) carry neither and
// are excluded by the same rule, since they are pinned through the
// frames that hold them rather than on their own.
func declaredFrameTypes(t *testing.T) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "extproto.go", nil, 0)
	if err != nil {
		t.Fatalf("parse extproto.go: %v", err)
	}
	out := map[string]bool{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if _, ok := ts.Type.(*ast.StructType); !ok {
				continue
			}
			if strings.HasSuffix(ts.Name.Name, "FromExt") || strings.HasSuffix(ts.Name.Name, "FromHost") {
				out[ts.Name.Name] = true
			}
		}
	}
	// Types both sides send carry neither suffix, so the convention
	// cannot find them; bidirectionalFrames is where they are declared
	// and it enrolls them here too.
	for name := range bidirectionalFrames {
		out[name] = true
	}
	// A parse that quietly finds nothing would make this test vacuous.
	if len(out) < 30 {
		t.Fatalf("found only %d frame types in extproto.go; the parse is not seeing them", len(out))
	}
	return out
}

// TestCorpusCoversEveryFrameType is why the corpus can be trusted as a
// complete reference rather than a sample.
//
// The corpus fell behind the protocol once already, and quietly: the
// secret verbs (protocol 6) shipped with no golden entry, so
// terva-sdk-rust had to source-read extproto.go to implement them and
// left a comment saying as much. Nothing failed — a table that names its
// subjects cannot notice a subject that was never added.
//
// So the subjects are discovered, not listed: a new FromExt/FromHost
// struct fails here on the commit that adds it, naming itself.
func TestCorpusCoversEveryFrameType(t *testing.T) {
	covered := map[string]bool{}
	for _, tc := range goldenFrames {
		covered[reflect.TypeOf(tc.v).Name()] = true
	}
	var missing []string
	for name := range declaredFrameTypes(t) {
		if !covered[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("frame types with no entry in goldenFrames — add one, so SDKs in other "+
			"languages read the bytes here instead of reverse-engineering them out of "+
			"extproto.go:\n  %s", strings.Join(missing, "\n  "))
	}
	// The reverse direction: an entry for a type that no longer exists
	// would publish a frame the protocol has retired.
	declared := declaredFrameTypes(t)
	var stale []string
	for name := range covered {
		if !declared[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("goldenFrames pins types that extproto.go no longer declares: %v", stale)
	}
}

// divergentEscapes returns the byte sequences encoding/json emits for
// the characters other JSON encoders leave alone — derived FROM the
// encoder rather than written out, so the guard below cannot drift from
// what Encode actually does, and says so out loud if Go ever stops.
//
// Note what to scan for. A frame whose content holds one of these
// characters never shows the character in its golden bytes: Encode has
// already turned it into the six-byte escape. Scanning the corpus for a
// literal < would therefore match nothing, ever — a guard that cannot
// fail is not a guard.
func divergentEscapes(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, c := range []string{"<", ">", "&"} {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal %q: %v", c, err)
		}
		esc := strings.Trim(string(b), `"`)
		if esc == c {
			t.Fatalf("encoding/json no longer escapes %q — this guard is obsolete "+
				"and the cross-language hazard it protects against is gone", c)
		}
		out = append(out, esc)
	}
	return out
}

// escapeDivergent reports where s carries an escape from that set, or -1.
func escapeDivergent(s string, escapes []string) int {
	for _, esc := range escapes {
		if i := strings.Index(s, esc); i >= 0 {
			return i
		}
	}
	return -1
}

// TestGoldenCorpusIsEncoderNeutral keeps the corpus usable as a
// CROSS-LANGUAGE oracle.
//
// This is NOT a statement about what may travel the wire. Encode uses
// encoding/json, which escapes < > & (U+003C, U+003E, U+0026); serde and
// friends emit them literally. Both are valid JSON for the same value
// and every reader on both sides accepts either, so real frames carry
// these characters constantly — a tool schema, a panel line, an
// assistant message under intercept — and nothing cares.
//
// It matters in exactly one place: here. SDKs in other languages
// byte-compare their encoder against this corpus, so a frame carrying
// one of these characters would encode differently under their encoder
// than ours and fail their suite over a frame nobody considered wrong.
//
// The scan walks the corpus rather than naming frames, so a case added
// tomorrow is enrolled without anyone remembering to enrol it.
func TestGoldenCorpusIsEncoderNeutral(t *testing.T) {
	escapes := divergentEscapes(t)
	for _, tc := range goldenFrames {
		if i := escapeDivergent(tc.want, escapes); i >= 0 {
			t.Errorf("golden frame %q carries %s at byte %d: encoding/json escapes that "+
				"character, other encoders do not, and this corpus is a byte-exact oracle "+
				"for SDKs written in other languages. Pick different sample content, or "+
				"retire the byte-exact cross-language contract first.",
				tc.name, tc.want[i:i+6], i)
		}
	}
}

// TestEncoderNeutralityGuardHasTeeth proves the scan can fail. A guard
// over a corpus that happens to be clean is indistinguishable from no
// guard at all, so pin both halves: it must catch a hazardous frame AND
// must still pass a benign one.
func TestEncoderNeutralityGuardHasTeeth(t *testing.T) {
	escapes := divergentEscapes(t)

	// Build the hazard the way it really arises — encode a value whose
	// content holds the character — rather than hand-writing the escape.
	for _, text := range []string{"a <b> tag", "you & me", "2 > 1"} {
		frame, err := Encode(NotifyFromExt{Type: "notify", Level: "info", Message: text})
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		got := strings.TrimSuffix(string(frame), "\n")
		if escapeDivergent(got, escapes) < 0 {
			t.Errorf("guard missed a divergent frame: %s", got)
		}
	}
	for _, ok := range []string{
		`{"type":"shutdown"}`,
		`{"type":"status_segment","id":"tasks","text":"▸ patch (1/4)"}`,
	} {
		if i := escapeDivergent(ok, escapes); i >= 0 {
			t.Errorf("guard rejected a benign frame at byte %d: %s", i, ok)
		}
	}
}

// goldenFixturePath is the corpus published for SDKs written in other
// languages: one JSON object per line, {"name":…,"dir":…,"frame":…},
// where frame is the exact bytes Encode produces, carried as a JSON
// STRING. A nested object would be re-encoded by whoever read it —
// which is the very thing this file exists to make unnecessary.
//
// It exists because the corpus above is Go source. terva-sdk-rust's
// terva-extproto hand-copied these literals into its own suite and had
// to source-read extproto.go for the frames the Go table never covered;
// a hand-copy drifts silently, and a fetch cannot.
//
// dir is extproto's addition over connproto's two-field envelope,
// because this protocol's two directions are not symmetric in use: an
// extension only ever ENCODES ext_to_host frames, so an SDK asserts
// those byte-exact and the rest on decode. Publishing the split keeps
// every SDK from re-deriving a classification we already know.
const goldenFixturePath = "testdata/golden.jsonl"

var updateGolden = flag.Bool("update-golden", false,
	"rewrite "+goldenFixturePath+" from the in-source corpus")

type goldenFixtureEntry struct {
	Name  string `json:"name"`
	Dir   string `json:"dir"`
	Frame string `json:"frame"`
}

func renderGoldenFixture(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// The envelope has to be portable for the same reason its contents
	// do — with HTML escaping left on, a reader in another language would
	// not get the frame back byte-for-byte.
	enc.SetEscapeHTML(false)
	for _, tc := range goldenFrames {
		entry := goldenFixtureEntry{Name: tc.name, Dir: frameDirection(t, tc.v), Frame: tc.want}
		if err := enc.Encode(entry); err != nil {
			t.Fatalf("render %q: %v", tc.name, err)
		}
	}
	return buf.Bytes()
}

// TestGoldenFixtureMatchesCorpus keeps the published file and the
// in-source corpus in lockstep, so the file cannot quietly rot into a
// stale copy of the thing it publishes — the failure mode it was added
// to remove. Regenerate with:
//
//	go test ./packages/agent/extproto -run TestGoldenFixture -update-golden
func TestGoldenFixtureMatchesCorpus(t *testing.T) {
	want := renderGoldenFixture(t)
	if *updateGolden {
		if err := os.WriteFile(goldenFixturePath, want, 0o644); err != nil {
			t.Fatalf("write %s: %v", goldenFixturePath, err)
		}
		t.Logf("rewrote %s (%d frames)", goldenFixturePath, len(goldenFrames))
		return
	}
	got, err := os.ReadFile(goldenFixturePath)
	if err != nil {
		t.Fatalf("read %s: %v (regenerate with -update-golden)", goldenFixturePath, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s no longer matches the in-source corpus — regenerate with:\n"+
			"\tgo test ./packages/agent/extproto -run TestGoldenFixture -update-golden",
			goldenFixturePath)
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

	// A null array must not break a reader: the host emits one whenever
	// the producing slice is nil (an extension holding no secrets, a
	// not-found session read).
	var keys SecretKeysFromHost
	line = `{"type":"secret_keys","id":"q5","keys":null}`
	if err := json.Unmarshal([]byte(line), &keys); err != nil {
		t.Fatalf("decode secret_keys with null keys: %v", err)
	}
	if keys.ID != "q5" || len(keys.Keys) != 0 {
		t.Errorf("secret_keys fields: %+v", keys)
	}
}
