// Package extproto defines the JSON-over-stdin/stdout wire format
// spoken between zot and its extension subprocesses. Both the host
// (packages/agent/extensions) and the SDK (packages/agent/ext) marshal/
// unmarshal the same types, so changes here ripple through both.
//
// All frames are one JSON object terminated by a single LF. Object
// boundaries follow newline boundaries; no multi-line JSON.
//
// Direction conventions in this file:
//   - Type names ending in "FromExt" are sent by the extension to zot.
//   - Type names ending in "FromHost" are sent by zot to the extension.
//   - Names without a suffix are direction-neutral payloads or shared
//     value types.
//
// Every frame has a top-level Type discriminator. Optional ID is
// present on commands and on responses to commands so the sender can
// correlate; events and notifications never carry an ID.
package extproto

import "encoding/json"

// ProtocolVersion is the wire revision this host/SDK speaks.
//
//	1 — baseline: tool_result fanout, crash surfacing, min-protocol
//	    negotiation.
//	2 — session identity: session_id / session_path / session_title on
//	    the session_start event, re-fired on every session switch, and
//	    a host guarantee that session_start reaches a subscriber before
//	    that session's first tool invocation (ordered delivery). An
//	    extension that needs per-session state declares RequireProtocol(2).
const ProtocolVersion = 2

type Frame struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

type HelloFromExt struct {
	Type         string   `json:"type"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities,omitempty"`
	// MinProtocol is the lowest host ProtocolVersion this extension
	// can run against. Optional and additive: zero (the default, and
	// what every pre-negotiation extension sends) means "no minimum",
	// so old extensions and old hosts interoperate unchanged. When
	// set, a host whose ProtocolVersion is lower refuses to load the
	// extension with a clear message instead of letting it misbehave
	// silently against a wire it doesn't fully speak — the mechanism
	// that lets a terva-only extension fail cleanly on an upstream
	// or an older terva.
	MinProtocol int `json:"min_protocol,omitempty"`
}

type RegisterCommandFromExt struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type RegisterToolFromExt struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema"`
	// ReadOnly declares the tool has no side effects (the MCP
	// readOnlyHint analog). Hosts may use it to admit the tool in
	// read-only approval modes; it is additive and optional, so old
	// hosts and extensions interoperate unchanged. An extension that
	// lies here only cheats its own user's policy.
	ReadOnly bool `json:"read_only,omitempty"`
}

type ReadyFromExt struct {
	Type string `json:"type"`
}

type SubscribeFromExt struct {
	Type      string   `json:"type"`
	Events    []string `json:"events,omitempty"`
	Intercept []string `json:"intercept,omitempty"`
}

type EventInterceptResponseFromExt struct {
	Type         string          `json:"type"`
	ID           string          `json:"id"`
	Block        bool            `json:"block,omitempty"`
	Reason       string          `json:"reason,omitempty"`
	ModifiedArgs json.RawMessage `json:"modified_args,omitempty"`
	ReplaceText  string          `json:"replace_text,omitempty"`
}

type ToolResultFromExt struct {
	Type    string         `json:"type"`
	ID      string         `json:"id"`
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"is_error,omitempty"`
}

type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"`
}

type CommandResponseFromExt struct {
	Type      string     `json:"type"`
	ID        string     `json:"id"`
	Action    string     `json:"action"`
	Prompt    string     `json:"prompt,omitempty"`
	Insert    string     `json:"insert,omitempty"`
	Display   string     `json:"display,omitempty"`
	OpenPanel *PanelSpec `json:"open_panel,omitempty"`
	Error     string     `json:"error,omitempty"`
}

type PanelSpec struct {
	ID     string   `json:"id"`
	Title  string   `json:"title,omitempty"`
	Lines  []string `json:"lines,omitempty"`
	Footer string   `json:"footer,omitempty"`
}

// OpenPanelFromExt is a spontaneous one-way frame an extension can send at
// any time to open an interactive panel. Unlike the open_panel action inside
// CommandResponseFromExt, this form is uncoupled from any command invocation
// and may be sent from a tool handler goroutine or any background context.
type OpenPanelFromExt struct {
	Type  string    `json:"type"` // "open_panel"
	Panel PanelSpec `json:"panel"`
}

type PanelRenderFromExt struct {
	Type    string   `json:"type"`
	PanelID string   `json:"panel_id"`
	Title   string   `json:"title,omitempty"`
	Lines   []string `json:"lines,omitempty"`
	Footer  string   `json:"footer,omitempty"`
}

type PanelCloseFromExt struct {
	Type    string `json:"type"`
	PanelID string `json:"panel_id"`
}

type NotifyFromExt struct {
	Type    string `json:"type"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// ClearNotesFromExt is a spontaneous frame an extension can send to
// retract every note it previously pushed via notify/display, so
// transient status lines (e.g. an approval prompt) do not stack up
// forever in the bottom-sticky notes block.
type ClearNotesFromExt struct {
	Type string `json:"type"`
}

// SubmitSlashFromExt is a spontaneous frame an extension can send at
// any time (typically from a panel_key handler) to invoke a slash
// command in the host's TUI as if the user had typed it. Text must
// start with '/'. Reserved for internal / opt-in extensions today;
// the wire format is stable but not yet exposed in the public docs.
type SubmitSlashFromExt struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ShutdownAckFromExt struct {
	Type string `json:"type"`
}

// HelloAckFromHost carries the host's identity. ZotVersion and
// TervaVersion are the SAME value under both naming eras: existing
// extensions parse zot_version, so it is kept indefinitely —
// extension wire compatibility is an explicit invariant of the
// rename (docs/plans/rename-terva.md); the golden tests in
// extproto_test.go enforce it.
type HelloAckFromHost struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
	ZotVersion      string `json:"zot_version"`
	TervaVersion    string `json:"terva_version,omitempty"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	CWD             string `json:"cwd"`
	ExtensionDir    string `json:"extension_dir,omitempty"`
	DataDir         string `json:"data_dir,omitempty"`
}

type CommandInvokedFromHost struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args,omitempty"`
}

type ToolCallFromHost struct {
	Type string          `json:"type"`
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type EventFromHost struct {
	Type     string          `json:"type"`
	Event    string          `json:"event"`
	Step     int             `json:"step,omitempty"`
	Stop     string          `json:"stop,omitempty"`
	Error    string          `json:"error,omitempty"`
	ToolID   string          `json:"tool_id,omitempty"`
	ToolName string          `json:"tool_name,omitempty"`
	ToolArgs json.RawMessage `json:"tool_args,omitempty"`
	Text     string          `json:"text,omitempty"`
	// IsError is set on a "tool_result" event when the tool returned
	// an error result. The result's text rides in Text. Additive:
	// pre-fanout hosts never emit tool_result events at all, so older
	// subscribers simply never see this field.
	IsError bool `json:"is_error,omitempty"`
	// SessionID / SessionPath / SessionTitle identify the active
	// session on a "session_start" event (ProtocolVersion 2+). The
	// host fires session_start AFTER the session opens and re-fires it
	// on every switch (/sessions resume, fork, /new). An empty
	// SessionID means there is no active session (e.g. --no-session, or
	// a session was just closed). SessionTitle is presentation-only.
	// Additive/omitempty — pre-v2 subscribers ignore the fields, and a
	// session_start that carries no session info (older emitters)
	// simply leaves them empty.
	SessionID    string `json:"session_id,omitempty"`
	SessionPath  string `json:"session_path,omitempty"`
	SessionTitle string `json:"session_title,omitempty"`
}

type EventInterceptFromHost struct {
	Type     string          `json:"type"`
	ID       string          `json:"id"`
	Event    string          `json:"event"`
	ToolID   string          `json:"tool_id,omitempty"`
	ToolName string          `json:"tool_name,omitempty"`
	ToolArgs json.RawMessage `json:"tool_args,omitempty"`
	Step     int             `json:"step,omitempty"`
	Text     string          `json:"text,omitempty"`
}

type PanelKeyFromHost struct {
	Type    string `json:"type"`
	PanelID string `json:"panel_id"`
	Key     string `json:"key"`
	Text    string `json:"text,omitempty"`
}

type PanelResizeFromHost struct {
	Type    string `json:"type"`
	PanelID string `json:"panel_id"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

type PanelCloseFromHost struct {
	Type    string `json:"type"`
	PanelID string `json:"panel_id"`
}

type ShutdownFromHost struct {
	Type string `json:"type"`
}

func Encode(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
