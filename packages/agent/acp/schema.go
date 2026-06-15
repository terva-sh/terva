//go:build terva_acp

// Package acp implements terva's Agent Client Protocol (ACP) run mode —
// an editor (the *client*, e.g. Zed) spawns terva and drives it over
// JSON-RPC 2.0 on stdio. terva runs the model in-process and narrates a
// live transcript back as session/update notifications.
//
// This file holds only the v1 wire types terva actually emits or accepts
// (hand-rolled — no external SDK). camelCase json tags match the spec
// (schema/v1/schema.json). Decoders tolerate a stray `_meta` on any object
// and unknown sessionUpdate variants, per the §13 conformance rules.
package acp

import "encoding/json"

// ProtocolVersion is the single ACP protocol version terva targets.
const ProtocolVersion = 1

// ---- method names (client -> agent) ----

const (
	MethodInitialize          = "initialize"
	MethodAuthenticate        = "authenticate"
	MethodSessionNew          = "session/new"
	MethodSessionPromptName   = "session/prompt"
	MethodSessionLoad         = "session/load"
	MethodSessionList         = "session/list"
	MethodSessionCancel       = "session/cancel"
	MethodSessionSetMode      = "session/set_mode"
	MethodSessionSetConfigOpt = "session/set_config_option"
)

// ---- method names (agent -> client) ----

const (
	MethodSessionUpdate            = "session/update"
	MethodSessionRequestPermission = "session/request_permission"
)

// ---- sessionUpdate discriminator values (the §6 enum) ----
//
// The discriminator field on a SessionUpdate is `sessionUpdate` (NOT
// `type`; `type` is the ContentBlock discriminator). Unknown values
// must deserialize as no-ops.
const (
	UpdateAgentMessageChunk = "agent_message_chunk"
	UpdateAgentThoughtChunk = "agent_thought_chunk"
	UpdateUserMessageChunk  = "user_message_chunk"
	UpdateToolCall          = "tool_call"
	UpdateToolCallUpdate    = "tool_call_update"
	UpdatePlan              = "plan"
	UpdatePlanUpdate        = "plan_update"
	UpdatePlanRemoved       = "plan_removed"
	UpdateAvailableCommands = "available_commands_update"
	UpdateCurrentMode       = "current_mode_update"
	UpdateConfigOption      = "config_option_update"
	UpdateSessionInfo       = "session_info_update"
	UpdateUsage             = "usage_update"
)

// ---- content-block type discriminators ----

const (
	ContentText         = "text"
	ContentImage        = "image"
	ContentAudio        = "audio"
	ContentResource     = "resource"
	ContentResourceLink = "resource_link"
)

// ---- tool-call kinds / statuses ----

const (
	ToolKindRead       = "read"
	ToolKindEdit       = "edit"
	ToolKindDelete     = "delete"
	ToolKindMove       = "move"
	ToolKindSearch     = "search"
	ToolKindExecute    = "execute"
	ToolKindThink      = "think"
	ToolKindFetch      = "fetch"
	ToolKindSwitchMode = "switch_mode"
	ToolKindOther      = "other"
)

const (
	ToolStatusPending    = "pending"
	ToolStatusInProgress = "in_progress"
	ToolStatusCompleted  = "completed"
	ToolStatusFailed     = "failed"
)

// ---- stop reasons ----

const (
	StopEndTurn         = "end_turn"
	StopMaxTokens       = "max_tokens"
	StopMaxTurnRequests = "max_turn_requests"
	StopRefusal         = "refusal"
	StopCancelled       = "cancelled"
)

// ---- initialize ----

// Implementation is the {name, version, title?} block describing a peer.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Title   string `json:"title,omitempty"`
}

// InitializeParams are the client's initialize request params.
type InitializeParams struct {
	ProtocolVersion int             `json:"protocolVersion"`
	ClientInfo      *Implementation `json:"clientInfo,omitempty"`
	// ClientCapabilities is accepted but not consumed this pass (we never
	// delegate fs/* or terminal/* to the client — §12).
	ClientCapabilities json.RawMessage `json:"clientCapabilities,omitempty"`
	Meta               json.RawMessage `json:"_meta,omitempty"`
}

// InitializeResult is terva's initialize response.
type InitializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AuthMethods       []AuthMethod      `json:"authMethods"`
	AgentInfo         *Implementation   `json:"agentInfo,omitempty"`
}

// AgentCapabilities advertises ONLY what this pass implements. loadSession
// and sessionCapabilities.list stay false/{} until Phase 3 (capability
// honesty is binding — §13).
type AgentCapabilities struct {
	LoadSession        bool               `json:"loadSession"`
	PromptCapabilities PromptCapabilities `json:"promptCapabilities"`
	McpCapabilities    McpCapabilities    `json:"mcpCapabilities"`
	// SessionCapabilities is an object {} (NOT a bool). Left empty until
	// session/list lands.
	SessionCapabilities map[string]any `json:"sessionCapabilities"`
}

type PromptCapabilities struct {
	Image           bool `json:"image"`
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
}

// McpCapabilities advertises which MCP transports the agent accepts.
// terva MCP is stdio-only, so http/sse/acp are all false. The acp flag
// is carried per the plan (§13) even though the core schema only lists
// http/sse — clients tolerate the extra false flag.
type McpCapabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
	ACP  bool `json:"acp"`
}

// AuthMethod is an {id, name, description?} entry in the authMethods
// registry. terva's terminal-auth method is advertised but `authenticate`
// is a no-op this pass.
type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ---- session/new ----

// NewSessionParams are the session/new request params. mcpServers carries the
// ACP McpServer union array (Phase 4a wires its stdio entries — see mcp.go's
// ParseMCPServers). additionalDirectories is accepted and currently ignored.
type NewSessionParams struct {
	CWD                   string          `json:"cwd"`
	MCPServers            json.RawMessage `json:"mcpServers,omitempty"`
	AdditionalDirectories []string        `json:"additionalDirectories,omitempty"`
	Meta                  json.RawMessage `json:"_meta,omitempty"`
}

// NewSessionResult is the session/new response. Phase 4b advertises the
// model `configOptions` (authenticated-provider models) and the approval-mode
// `modes` (SessionModeState). Both stay omitempty so a result with neither
// still marshals to just `{sessionId}` (capability honesty).
type NewSessionResult struct {
	SessionID     string                `json:"sessionId"`
	Modes         *SessionModeState     `json:"modes,omitempty"`
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
}

// ---- session/prompt ----

// PromptParams are the session/prompt request params.
type PromptParams struct {
	SessionID string          `json:"sessionId"`
	Prompt    []ContentBlock  `json:"prompt"`
	Meta      json.RawMessage `json:"_meta,omitempty"`
}

// PromptResult resolves session/prompt with the turn's stop reason.
type PromptResult struct {
	StopReason string `json:"stopReason"`
}

// ---- session/cancel (notification, Phase 2) ----

type CancelParams struct {
	SessionID string `json:"sessionId"`
}

// ---- session/load (Phase 3) ----

// LoadSessionParams are the session/load request params. mcpServers is
// REQUIRED on the wire and wired the same as session/new (Phase 4a — stdio
// entries spawn per-session MCP servers). additionalDirectories is accepted
// and currently ignored (terva reads local FS under cwd; honoring extra roots
// is a later item).
type LoadSessionParams struct {
	SessionID             string          `json:"sessionId"`
	CWD                   string          `json:"cwd"`
	MCPServers            json.RawMessage `json:"mcpServers,omitempty"`
	AdditionalDirectories []string        `json:"additionalDirectories,omitempty"`
	Meta                  json.RawMessage `json:"_meta,omitempty"`
}

// LoadSessionResult is the session/load response. Phase 4b advertises the
// same model `configOptions` + approval-mode `modes` as session/new, so a
// resumed session restores its model/mode menus. Both stay omitempty.
type LoadSessionResult struct {
	Modes         *SessionModeState     `json:"modes,omitempty"`
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
}

// ---- session/list (Phase 3) ----

// ListSessionsParams are the session/list request params. cwd filters to a
// working directory (nullable on the wire); cursor is the opaque pagination
// token (unused — terva returns the full set in one page).
type ListSessionsParams struct {
	CWD    string          `json:"cwd,omitempty"`
	Cursor string          `json:"cursor,omitempty"`
	Meta   json.RawMessage `json:"_meta,omitempty"`
}

// ListSessionsResult is the session/list response. sessions is REQUIRED;
// nextCursor is omitted (single page, no pagination this pass).
type ListSessionsResult struct {
	Sessions   []SessionInfo `json:"sessions"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

// SessionInfo is one entry in a session/list response. sessionId+cwd are
// required; additionalDirectories/title/updatedAt are optional. updatedAt is
// an ISO 8601 (RFC 3339) timestamp.
type SessionInfo struct {
	SessionID             string   `json:"sessionId"`
	CWD                   string   `json:"cwd"`
	AdditionalDirectories []string `json:"additionalDirectories,omitempty"`
	Title                 string   `json:"title,omitempty"`
	UpdatedAt             string   `json:"updatedAt,omitempty"`
}

// ---- session/request_permission (agent -> client, Phase 2) ----

// PermissionOptionKind values (§8). The four kinds map back to
// allow/deny (+ remember-for-session for the *_always variants).
const (
	PermAllowOnce    = "allow_once"
	PermAllowAlways  = "allow_always"
	PermRejectOnce   = "reject_once"
	PermRejectAlways = "reject_always"
)

// requestPermission outcome discriminator values (§8). The outcome object
// nests its own `outcome` field (RequestPermissionResponse.outcome.outcome).
const (
	PermOutcomeSelected  = "selected"
	PermOutcomeCancelled = "cancelled"
)

// PermissionOption is one choice the editor renders for a permission
// prompt. `name` is REQUIRED (the human label) — the schema mandates it
// even though some clients tolerate its absence.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// PermissionToolCall is the slim toolCall reference carried on a
// request_permission (only toolCallId is load-bearing for correlation;
// the editor already has the full tool_call from the pending update).
type PermissionToolCall struct {
	ToolCallID string `json:"toolCallId"`
}

// RequestPermissionParams is the session/request_permission request body.
type RequestPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  PermissionToolCall `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// RequestPermissionResult is the client's response. The outcome is nested:
// {outcome:{outcome:"selected", optionId:"..."}} or {outcome:{outcome:"cancelled"}}.
type RequestPermissionResult struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// PermissionOutcome is the inner outcome object. OptionID is set only when
// Outcome == "selected".
type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// ---- content blocks (§5) ----

// ContentBlock is the discriminated (`type`) prompt-input / update-output
// block. Only the fields terva reads or writes are modeled; `annotations`
// and `_meta` are carried opaquely so a round-trip never drops them.
type ContentBlock struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// image / audio
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	URI      string `json:"uri,omitempty"`

	// resource (embedded context)
	Resource *EmbeddedResource `json:"resource,omitempty"`

	// resource_link
	Name string `json:"name,omitempty"`

	Annotations json.RawMessage `json:"annotations,omitempty"`
	Meta        json.RawMessage `json:"_meta,omitempty"`
}

// EmbeddedResource is the inner object of a `resource` content block.
type EmbeddedResource struct {
	URI      string `json:"uri,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// ---- session/update notification ----

// SessionNotification is the params object of a session/update.
type SessionNotification struct {
	SessionID string        `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
}

// SessionUpdate is one update variant. The `sessionUpdate` discriminator
// plus the variant's flattened fields ride a single object. We build these
// as maps in translate.go to flatten cleanly; this typed form is the read
// shape (used by tests / future inbound handling).
type SessionUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	// agent_message_chunk / user_message_chunk
	Content *ContentBlock `json:"content,omitempty"`
	// tool_call / tool_call_update
	ToolCallID  string             `json:"toolCallId,omitempty"`
	Title       string             `json:"title,omitempty"`
	Kind        string             `json:"kind,omitempty"`
	Status      string             `json:"status,omitempty"`
	Locations   []ToolCallLocation `json:"locations,omitempty"`
	ToolContent []ToolCallContent  `json:"-"`
	RawInput    json.RawMessage    `json:"rawInput,omitempty"`
	RawOutput   json.RawMessage    `json:"rawOutput,omitempty"`
}

// ToolCallLocation is a {path, line?} entry for follow-along UIs.
type ToolCallLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

// ToolCallContent is a tool-call content item (discriminator `type`):
// content | diff | terminal. terva emits `content` (text) and `diff`.
type ToolCallContent struct {
	Type string `json:"type"`
	// type=content
	Content *ContentBlock `json:"content,omitempty"`
	// type=diff
	Path    string `json:"path,omitempty"`
	OldText string `json:"oldText,omitempty"`
	NewText string `json:"newText,omitempty"`
}

// ---- available commands (Phase 4c — slash-command catalog) ----
//
// The agent advertises the slash commands it can execute as an
// `available_commands_update` session/update, carrying an array of
// AvailableCommand. The editor renders them in its command palette and
// sends a selection as a session/prompt whose text is the leading
// `/command` (which the prompt handler intercepts — §10).

// AvailableCommand is one entry in an available_commands_update
// (schema: AvailableCommand). name + description are required; input is
// the optional argument spec carrying a display hint.
type AvailableCommand struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Input       *AvailableCommandInput `json:"input,omitempty"`
}

// AvailableCommandInput is the unstructured-input spec for a command
// (schema: AvailableCommandInput / UnstructuredCommandInput). The whole
// text typed after the command name is the input; `hint` is shown in the
// input field before anything is typed. hint is required when input is
// present, so we only attach an input block for commands that take an
// argument.
type AvailableCommandInput struct {
	Hint string `json:"hint"`
}

// ---- session config options (Phase 4b — model selection) ----
//
// ACP exposes a model not as a `session/set_model` (there is none — §4/§13)
// but as a `select` config option. The session result carries a
// `configOptions` array; the editor renders a menu and round-trips the
// selection via session/set_config_option {sessionId, configId, value};
// the agent acknowledges with a config_option_update notification carrying
// the refreshed option list.

// SessionConfigSelectType is the const discriminator of a select option.
const SessionConfigSelectType = "select"

// Config option categories (UX hints — §schema). terva uses "model".
const (
	ConfigCategoryMode  = "mode"
	ConfigCategoryModel = "model"
)

// ConfigIDModel is the stable configId of terva's model selector.
const ConfigIDModel = "model"

// SessionConfigOption is one `select` configuration option (schema:
// SessionConfigOption, type="select"). id/name/type/currentValue/options are
// required; description/category are optional UX hints.
type SessionConfigOption struct {
	ID           string                      `json:"id"`
	Name         string                      `json:"name"`
	Type         string                      `json:"type"`
	Description  string                      `json:"description,omitempty"`
	Category     string                      `json:"category,omitempty"`
	CurrentValue string                      `json:"currentValue"`
	Options      []SessionConfigSelectOption `json:"options"`
}

// SessionConfigSelectOption is one selectable value of a select option
// (schema: SessionConfigSelectOption). value/name required; description
// optional.
type SessionConfigSelectOption struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SetSessionConfigOptionRequest are the session/set_config_option request
// params: {sessionId, configId, value} (schema-verified, §13).
type SetSessionConfigOptionRequest struct {
	SessionID string          `json:"sessionId"`
	ConfigID  string          `json:"configId"`
	Value     string          `json:"value"`
	Meta      json.RawMessage `json:"_meta,omitempty"`
}

// SetSessionConfigOptionResult is the session/set_config_option response. The
// schema requires no fields; we echo the refreshed option list so a client
// that ignores the config_option_update notification still sees the new state.
type SetSessionConfigOptionResult struct {
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
}

// ---- session modes (Phase 4b — approval modes) ----
//
// terva's approval-mode spectrum (plan/ask/auto-edit/workspace/yolo) maps onto
// ACP session modes. The session result carries a SessionModeState
// {currentModeId, availableModes}; the editor round-trips a switch via
// session/set_mode {sessionId, modeId}; the agent acknowledges with a
// current_mode_update notification.

// SessionModeState is the {availableModes, currentModeId} block on a session
// result (schema: SessionModeState). Both fields required when present.
type SessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
}

// SessionMode is one available approval mode (schema: SessionMode). id/name
// required; description optional.
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SetSessionModeRequest are the session/set_mode request params:
// {sessionId, modeId} (schema-verified, §13).
type SetSessionModeRequest struct {
	SessionID string          `json:"sessionId"`
	ModeID    string          `json:"modeId"`
	Meta      json.RawMessage `json:"_meta,omitempty"`
}

// SetSessionModeResult is the session/set_mode response (no required fields).
type SetSessionModeResult struct{}
