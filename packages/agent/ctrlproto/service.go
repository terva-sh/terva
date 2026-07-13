package ctrlproto

import (
	"context"

	"terva.sh/terva/packages/core"
)

// WorkspaceService is everything a frontend needs to drive and manage one
// terva workspace: a single project/cwd hosting N sessions, each addressed by
// a session id. The WebSocket carrier (`terva web`) and the future in-process
// carrier (the TUI) are two serializations of this one interface.
//
// The methods are grouped to mirror the wire's method groups. A carrier need
// not expose every group — the mobile PWA drives only conversation + session —
// but the Go interface is one type; unimplemented control-group calls return a
// [CodeUnsupported] error.
//
// Session addressing: every call names its session. An empty session id means
// "the workspace's current/default session"; carriers may resolve it once at
// connect time. Concurrency: an implementation must be safe for calls from
// many carriers/goroutines at once (multi-device is a day-one requirement).
type WorkspaceService interface {
	// --- conversation group ---

	// Prompt starts a turn on sess with the given user text and optional
	// images. It returns once the turn is accepted (not when it finishes);
	// progress streams to every [Subscribe] channel for sess. Returns a
	// [CodeBusy] error if a turn is already running on sess.
	Prompt(ctx context.Context, sess, text string, images []Image) error

	// Queue enqueues text to be delivered at the next safe boundary of the
	// running turn on sess (the multi-device / interject story). If no turn is
	// running it behaves like a deferred Prompt at the implementation's
	// discretion.
	Queue(ctx context.Context, sess, text string) error

	// SetQueue replaces sess's entire pending queue — the primitive behind
	// editing or cancelling queued messages before they inject. An empty slice
	// clears the queue. Implementations broadcast the new state so every client
	// converges.
	SetQueue(ctx context.Context, sess string, texts []string) error

	// Cancel interrupts the active turn on sess, if any. A no-op when idle.
	Cancel(ctx context.Context, sess string) error

	// Compact summarizes and replaces sess's transcript (user-driven
	// compaction). Returns [CodeBusy] if a turn is running; a benign "nothing
	// to compact" is reported to clients as a notice, not an error. On success
	// clients receive a fresh snapshot with the compacted transcript.
	Compact(ctx context.Context, sess string) error

	// Clear wipes sess's transcript (unlike Compact, which summarizes it first),
	// starting the conversation over while keeping the same session. Returns
	// [CodeBusy] if a turn is running. On success clients receive a fresh
	// (empty) snapshot.
	Clear(ctx context.Context, sess string) error

	// Approve resolves a pending tool-approval round-trip previously surfaced
	// as an [EventPermissionRequest] event with the matching callID. The first
	// client to answer wins; late answers for an already-resolved call are
	// ignored.
	Approve(ctx context.Context, sess, callID string, d core.ConfirmDecision) error

	// Answer resolves a pending question previously surfaced as an
	// [EventAskRequest] event with the matching askID. First answer wins.
	Answer(ctx context.Context, sess, askID string, a core.UserAnswer) error

	// Subscribe returns a stream of events for sess: the conversation
	// [core.WireEvent] stream plus the control events (permission/ask
	// requests and their resolutions). Cancelling ctx unsubscribes and closes
	// the channel. Multiple subscribers to one session each receive every
	// event (fan-out).
	Subscribe(ctx context.Context, sess string) (<-chan Event, error)

	// --- session group ---

	// Sessions lists the workspace's sessions, newest activity first.
	Sessions(ctx context.Context) ([]SessionInfo, error)

	// CreateSession mints a new session and returns its descriptor.
	CreateSession(ctx context.Context, opts CreateOpts) (SessionInfo, error)

	// ResumeSession loads a persisted session's transcript so subsequent
	// Prompt/Subscribe calls operate on it, and returns its descriptor.
	ResumeSession(ctx context.Context, sess string) (SessionInfo, error)

	// RenameSession sets a session's display title (its "nickname").
	RenameSession(ctx context.Context, sess, title string) error

	// GenerateSessionTitle regenerates sess's title from its transcript with
	// a one-shot model call and persists it — the on-demand sibling of the
	// automatic auto_title pass (which it works independently of), and the
	// backfill for old untitled sessions. Works on live and cold sessions.
	// An explicit request overwrites whatever title exists, manual renames
	// included — the user just asked for it. BLOCKS on the model; keep it
	// off a UI goroutine. Returns the persisted title.
	GenerateSessionTitle(ctx context.Context, sess string) (string, error)

	// DeleteSession removes a session and its transcript.
	DeleteSession(ctx context.Context, sess string) error

	// Usage returns the cumulative token/cost totals for sess.
	Usage(ctx context.Context, sess string) (core.WireUsage, error)

	// UsageSnapshot returns the provider's subscription/credit picture for
	// sess: the plan and rate-limit windows, any credit balance, and when the
	// provider last reported them. Distinct from [Usage], which is this
	// session's own token and cost accounting.
	//
	// refresh=false reads the provider client's cached snapshot and is cheap
	// enough to call once per turn. refresh=true pulls from the provider's
	// usage endpoint for the providers that have one (OpenRouter, DeepSeek)
	// and BLOCKS on that fetch; callers must keep it off a UI goroutine.
	// [UsageInfo.Refreshable] reports which kind of provider this is, so a
	// client knows whether to show a loading state.
	//
	// A session with no agent, or a provider that reports nothing, yields a
	// zero UsageInfo with HasData false — not an error.
	UsageSnapshot(ctx context.Context, sess string, refresh bool) (UsageInfo, error)

	// ListResets returns sess's provider usage-reset credits (codex banked
	// resets), read-only. Supported is false when the provider offers none; a
	// session with no agent is likewise unsupported, not an error. BLOCKS on the
	// provider's endpoint — keep it off a UI goroutine.
	ListResets(ctx context.Context, sess string) (ResetsListResult, error)

	// ConsumeReset redeems the credit named by id on sess's provider. It is
	// IRREVERSIBLE and spends a scarce grant, so a client MUST confirm with the
	// user before calling. BLOCKS on the provider's endpoint. A provider with no
	// reset support, or an unknown/unavailable credit, is the implementation's
	// error to return.
	ConsumeReset(ctx context.Context, sess, id string) (ResetConsumeResult, error)

	// SideChatOpen freezes sess's system prompt and transcript and returns an id
	// naming that snapshot. The side chat is an ephemeral, tool-less completion
	// alongside a session — the /btw overlay — and it never appends to, reads
	// forward from, or otherwise disturbs the transcript it was opened against.
	//
	// The snapshot is frozen so a turn landing mid-dialogue (a queued prompt, a
	// paired chat DM, another device) cannot shift the ground under a
	// conversation already in progress. Implementations bound how many a session
	// may hold open; callers pair every open with a [SideChatClose].
	SideChatOpen(ctx context.Context, sess string) (string, error)

	// SideChatAsk runs one question against the snapshot named by id, preceded
	// by prior (the side chat's own completed exchanges, oldest first), and
	// returns the assistant's reply. It blocks on the model, honours ctx
	// cancellation, and records nothing anywhere: the reply comes back to the
	// caller and dies with the dialog.
	//
	// A stale or unknown id is a [CodeNotFound] error.
	SideChatAsk(ctx context.Context, sess, id string, prior []SideChatTurn, question string) (string, error)

	// SideChatClose releases the snapshot named by id. Closing an unknown id is
	// a no-op, not an error. Implementations also release every snapshot a
	// session holds when that session closes.
	SideChatClose(ctx context.Context, sess, id string) error

	// Context returns a size breakdown of everything riding sess's next
	// request — system prompt, tool defs, ext context, transcript — versus the
	// model's window, the /context view for debugging context bloat.
	Context(ctx context.Context, sess string) (ContextBreakdown, error)

	// Node resolves one context node by the opaque id from [Context]'s tree,
	// filling in its content/children for a lazy expand (message → content
	// blocks, tool defs → per-tool, system prompt / ext context → their text).
	// op selects the operation: empty (or "expand") expands; a node-named op is a
	// reveal (stage 3). Returns a CodeNotFound error for a stale or unknown id.
	Node(ctx context.Context, sess, id, op string) (ContextNode, error)

	// Surfaces lists the auxiliary panes available for sess (context, usage,
	// extension panels, …) for the client's surface switcher.
	Surfaces(ctx context.Context, sess string) ([]SurfaceMeta, error)

	// Surface returns one pane's content by id.
	Surface(ctx context.Context, sess, id string) (Surface, error)

	// SurfaceAction performs an action on a surface — e.g. forwarding a keypress
	// to an extension panel, or closing it. Args carries action-specific fields.
	SurfaceAction(ctx context.Context, sess, id, action string, args map[string]string) error

	// Catalog returns the effective web string catalog for lang (empty = the
	// daemon's active language), read fresh so an operator's overlay edit shows
	// on reconnect. Session-independent — the panel fetches it right after the
	// hello to localize its own strings.
	Catalog(ctx context.Context, lang string) (CatalogView, error)

	// ListFiles lists workspace files under the working directory for a
	// client's @-file picker (fswalk semantics: gitignore filtering, .git
	// pruning, entry/depth caps). Session-independent — files belong to the
	// workspace, not a session. The [FeatureFilesList] hello feature
	// advertises it, so a client on an older daemon degrades to its local
	// fallback instead of a method error.
	ListFiles(ctx context.Context, opts FilesListParams) (FilesListResult, error)

	// --- control group (mostly v2; implementations may return
	// CodeUnsupported for the parts they do not yet serve) ---

	// Models lists the models the workspace can switch to.
	Models(ctx context.Context) ([]ModelInfo, error)

	// SwitchModel changes the model backing sess, live, for the next turn.
	// providerName qualifies modelID — model ids are not globally unique
	// across providers (a subscription and an api-key route can serve the
	// same id), and an unqualified lookup may land on a provider the
	// workspace holds no credential for. Empty = resolve across all.
	SwitchModel(ctx context.Context, sess, providerName, modelID string) error

	// SetFavoriteModel pins or unpins a model in the user's favorites (the ★
	// favorites view), persisted to config.
	SetFavoriteModel(ctx context.Context, provider, model string, on bool) error

	// Trust grants Workspace Trust to the workspace's cwd, persisting the
	// verdict (parent = trust descendant directories too) and bringing project
	// content (extensions, lore, project permission rules) live for every open
	// session. Project skills/context baked into the system prompt take effect
	// on the next session. Idempotent.
	Trust(ctx context.Context, parent bool) error

	// Untrust removes the workspace's cwd from the trust store and tears project
	// content back down for every open session. The symmetric inverse of Trust.
	Untrust(ctx context.Context) error

	// Restart re-execs the daemon into the currently-installed binary (Tier-1
	// self-restart). Returns CodeUnsupported when the capability is not enabled.
	// On success the connection drops as the process image is replaced.
	Restart(ctx context.Context) error
}

// Image is one user-attached image for [WorkspaceService.Prompt]. Unlike the
// event wire (which carries image size only), inbound images carry raw bytes.
type Image struct {
	MimeType string `json:"mime_type"`
	Data     []byte `json:"data"`
}

// SessionInfo describes one session for a picker/switcher. It maps from core's
// session summary + metadata.
type SessionInfo struct {
	ID       string         `json:"id"`
	Title    string         `json:"title,omitempty"` // the display nickname
	Provider string         `json:"provider,omitempty"`
	Model    string         `json:"model,omitempty"`
	Persona  string         `json:"persona,omitempty"` // loaded persona/agent name
	Path     string         `json:"path,omitempty"`    // session transcript file path
	Created  string         `json:"created,omitempty"` // RFC 3339
	Updated  string         `json:"updated,omitempty"` // RFC 3339
	Messages int            `json:"messages"`
	Usage    core.WireUsage `json:"usage"`
	Current  bool           `json:"current,omitempty"` // the active session
	// Trusted reports whether the session's workspace (its cwd) has Workspace
	// Trust granted. Project-scoped content — extensions, lore, permission
	// rules — only loads/edits when trusted; a client reads this to show the
	// trust state and offer a grant control (control.trust / control.untrust).
	Trusted bool `json:"trusted,omitempty"`
	// ContextTokens is the last turn's input+cache tokens — the real live
	// context size (matches the TUI status bar's ctx gauge), 0 before the first
	// turn. ContextWindow is the model's window in tokens (0 = unknown). Together
	// they drive a live context meter without a modal round-trip.
	ContextTokens int `json:"context_tokens,omitempty"`
	ContextWindow int `json:"context_window,omitempty"`
	// Subscription reports that the session's credential is a subscription
	// (OAuth) login rather than a metered API key — a client tags its cost
	// display "(sub)". Resolved daemon-side, where the credential lives.
	Subscription bool `json:"subscription,omitempty"`
}

// CreateOpts parameterizes [WorkspaceService.CreateSession]. All fields are
// optional; empty values fall back to the workspace defaults. Template and
// persona are shaped now but honored as the control group lands.
type CreateOpts struct {
	Title string `json:"title,omitempty"`
	// Provider disambiguates Model: some model ids exist under several
	// providers (e.g. a subscription and an api-key route to the same model),
	// and an unqualified lookup may land on one the workspace holds no
	// credential for. Empty = resolve Model across all providers.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Persona  string `json:"persona,omitempty"`
	Template string `json:"template,omitempty"`
}

// ContextBreakdown is a byte-size accounting of what fills the model's context
// window on the next request. terva has no tokenizer, so sizes are bytes and a
// token estimate is ~bytes/4 — enough to finger a bloat source (usually one
// oversized tool result). Mirrors the TUI's /context Overview, and carries the
// TUI status bar's live usage picture (real last-turn context tokens,
// cumulative usage, subscription windows).
type ContextBreakdown struct {
	Provider         string `json:"provider,omitempty"` // active provider (header line, from the usage pane)
	Model            string `json:"model,omitempty"`    // active model
	Window           int    `json:"window"`             // model context window in tokens (0 = unknown)
	SystemBytes      int    `json:"system_bytes"`       // system prompt (includes folded-in ext guidance)
	ExtGuidanceBytes int    `json:"ext_guidance_bytes"` // the static ext guidance share of system_bytes
	ToolBytes        int    `json:"tool_bytes"`         // JSON of the ADVERTISED tool definitions — what the model actually receives; under lazy visibility this excludes inactive groups (their schemas are not in context)
	ToolCount        int    `json:"tool_count"`         // advertised tool count
	// Installed totals, set only when lazy visibility hides some tools: the full
	// registry's weight/count, so a client can show "16 of 62 tools · 12.4 KB
	// installed". Omitted when advertised == installed (nothing hidden). The
	// hidden schemas are NOT counted in ToolBytes/TotalBytes — only their names
	// ride the cache-free capability note — so the totals stay honest.
	ToolBytesInstalled int `json:"tool_bytes_installed,omitempty"`
	ToolCountInstalled int `json:"tool_count_installed,omitempty"`
	ExtBytes           int `json:"ext_bytes"` // ephemeral tail: extension/card context + the lazy-tool note
	// LazyNoteBytes is the inactive-tool-groups capability note's share of
	// ExtBytes — the names (not schemas) of hidden groups that ride the ephemeral
	// tail under lazy visibility. Zero/omitted when nothing is hidden. Surfaced so
	// /context shows what deferred discovery costs, the sub-share twin of
	// ExtGuidanceBytes.
	LazyNoteBytes   int              `json:"lazy_note_bytes,omitempty"`
	TranscriptBytes int              `json:"transcript_bytes"`
	TotalBytes      int              `json:"total_bytes"`
	Messages        []ContextMessage `json:"messages"`
	// ContextTokens is the real last-turn context size (input+cache tokens),
	// the accurate live gauge next to the byte estimate. Cumulative is the
	// session's token/cost totals. Subscription marks an OAuth ("sub")
	// credential. UsageWindows are the provider's plan/credit budgets.
	ContextTokens int               `json:"context_tokens,omitempty"`
	Cumulative    core.WireUsage    `json:"cumulative"`
	Subscription  bool              `json:"subscription,omitempty"`
	UsageWindows  []UsageWindowInfo `json:"usage_windows,omitempty"`

	// Tree is the hierarchical outline of the same context the scalar fields
	// above summarize: sections (system / tool defs / ext context / transcript)
	// → transcript turns → message stubs, with sizes, labels, and kinds but no
	// message bodies. It is a superset of Messages, kept additive: a client that
	// negotiated the context-tree feature renders this; older ones ignore it and
	// use the flat Messages list. Message bodies and deeper content are fetched
	// lazily via context.node (later stages). See docs/proposals/context-inspector.md.
	Tree *ContextNode `json:"tree,omitempty"`
	// Rev is the transcript epoch the tree was built at (the agent's
	// transcriptEpoch). A client can compare it before a context.node call to
	// tell whether its opaque node ids have gone stale (the transcript grew).
	Rev int `json:"rev,omitempty"`
}

// ContextMessage is one transcript entry's size in a [ContextBreakdown].
type ContextMessage struct {
	Index int    `json:"index"`
	Kind  string `json:"kind"` // role, or tool_result / role+tool / role+image / compaction
	Bytes int    `json:"bytes"`
}

// ContextNode is one node of the assembled-context tree (see [ContextBreakdown.Tree]).
// The Kind vocabulary is deliberately OPEN — a client renders an unknown kind by
// its Label and Bytes — so new inspectable facets need no wire change. ids are
// opaque and server-minted, valid within one tree snapshot; a client passes them
// back to context.node (later stages) and never parses them.
type ContextNode struct {
	ID         string            `json:"id"`
	Kind       string            `json:"kind"` // section | turn | message | block | event
	Label      string            `json:"label"`
	Bytes      int               `json:"bytes"`
	Tokens     int               `json:"tokens,omitempty"`     // when known; else the client estimates bytes/4
	Summary    string            `json:"summary,omitempty"`    // one-line preview for the collapsed row
	Content    string            `json:"content,omitempty"`    // full leaf body, populated on a context.node expand (stage 2+)
	Expandable bool              `json:"expandable,omitempty"` // has deeper content to fetch via context.node (stage 2+)
	Reveal     string            `json:"reveal,omitempty"`     // names a reveal op (e.g. "compaction") — stage 3
	Meta       map[string]string `json:"meta,omitempty"`       // kind-specific hints (role, index, count, …)
	Children   []ContextNode     `json:"children,omitempty"`
}

// UsageWindowInfo is one provider-reported usage budget (a rolling 5h window, a
// weekly cap, …), mirroring [provider.UsageWindow] for the wire.
type UsageWindowInfo struct {
	Label         string  `json:"label"`
	UsedPercent   float64 `json:"used_percent"`             // <0 means the provider reports the window but not a usable percent
	WindowMinutes int     `json:"window_minutes,omitempty"` // window length; 0 unknown
	ResetsAt      string  `json:"resets_at,omitempty"`      // RFC 3339; empty unknown
	Kind          string  `json:"kind,omitempty"`           // plan | credit | rate_limit
}

// CreditsInfo is a provider's credit balance, mirroring [provider.Credits] for
// the wire. Unlimited and HasCredits give Balance its meaning: some providers
// report what is left, some what has been spent, some neither.
type CreditsInfo struct {
	Unlimited  bool    `json:"unlimited,omitempty"`
	HasCredits bool    `json:"has_credits,omitempty"`
	Balance    float64 `json:"balance,omitempty"` // remaining, provider-defined units
	Used       float64 `json:"used,omitempty"`    // spent, provider-defined units
}

// UsageInfo is the provider's subscription/credit picture for one session — the
// payload of [WorkspaceService.UsageSnapshot], and the wire form of
// [provider.UsageSnapshot].
//
// Unlike [ContextBreakdown.UsageWindows], which carries only the plan and
// credit budgets a status bar should show, Windows here carries EVERY window
// the provider reports, rate-limit ones included. Filtering is the client's
// business: a status bar drops the ephemeral rate-limit windows because they
// would churn an always-visible meter, and a modal usage view shows them.
type UsageInfo struct {
	Provider string `json:"provider,omitempty"`
	// HasData is false when the session has no agent, or the provider reports
	// no usage at all. The zero UsageInfo is a valid "nothing to show".
	HasData     bool              `json:"has_data,omitempty"`
	Windows     []UsageWindowInfo `json:"windows,omitempty"`
	Credits     *CreditsInfo      `json:"credits,omitempty"`
	CapturedAt  string            `json:"captured_at,omitempty"` // RFC 3339; empty unknown
	Refreshable bool              `json:"refreshable,omitempty"` // provider fetches usage from an endpoint
}

// ResetInfo is one consumable usage-reset credit, mirroring [provider.UsageReset]
// for the wire. Times are RFC 3339 (empty when the provider didn't report one).
type ResetInfo struct {
	ID          string `json:"id"`
	Kind        string `json:"kind,omitempty"`  // provider reset-type tag (codex: "codex_rate_limits")
	Title       string `json:"title,omitempty"` // human label
	Description string `json:"description,omitempty"`
	Status      string `json:"status"` // available | pending | redeemed | expired
	GrantedAt   string `json:"granted_at,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	RedeemedAt  string `json:"redeemed_at,omitempty"`
}

// ResetsListResult is the payload of a [MethodResetsList] response. Supported
// is false when the current provider offers no resets at all — the client then
// hides the affordance rather than showing an empty list that reads as "you
// have none".
type ResetsListResult struct {
	Supported bool        `json:"supported"`
	Resets    []ResetInfo `json:"resets,omitempty"`
}

// ResetConsumeParams is the payload of [MethodResetsConsume]: the credit id to
// redeem. The host confirms with the user BEFORE issuing this — the call spends
// the credit.
type ResetConsumeParams struct {
	ID string `json:"id"`
}

// ResetConsumeResult is the payload of a [MethodResetsConsume] response: the
// credit in its redeemed state and how many usage windows it cleared.
type ResetConsumeResult struct {
	Reset        ResetInfo `json:"reset"`
	WindowsReset int       `json:"windows_reset,omitempty"`
}

// SurfaceMeta describes one available pane for the client's surface switcher.
// The set is dynamic (extension panels open/close), so a client re-lists on an
// [EventSurfacesChanged].
type SurfaceMeta struct {
	ID      string `json:"id"`              // "context", "usage", "status", "ext:<name>:<panel>"
	Title   string `json:"title"`           // switcher label
	Icon    string `json:"icon,omitempty"`  // glyph/emoji hint
	Kind    string `json:"kind"`            // context | usage | panel | widgets | settings | tasks | commands | extensions | permissions | lore | mcp | raati
	Scope   string `json:"scope,omitempty"` // session | workspace
	Live    bool   `json:"live,omitempty"`  // pushes EventSurfaceUpdated
	Actions bool   `json:"actions,omitempty"`
	Badge   string `json:"badge,omitempty"` // optional switcher badge
}

// Surface is one pane's content, discriminated by Kind. The core panes carry a
// typed payload (bespoke client renderers); extension panes carry either flat
// styled Panel lines or, in future, a generic Widgets tree.
type Surface struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Kind        string            `json:"kind"`
	Context     *ContextBreakdown `json:"context,omitempty"`     // kind=context
	Usage       *UsageView        `json:"usage,omitempty"`       // kind=usage
	Tasks       *TaskList         `json:"tasks,omitempty"`       // kind=tasks
	TaskBoard   *TaskBoardView    `json:"task_board,omitempty"`  // kind=taskboard
	Settings    *SettingsView     `json:"settings,omitempty"`    // kind=settings
	Panel       *PanelView        `json:"panel,omitempty"`       // kind=panel
	Widgets     []Widget          `json:"widgets,omitempty"`     // kind=widgets
	Commands    *CommandsView     `json:"commands,omitempty"`    // kind=commands
	Extensions  *ExtensionsView   `json:"extensions,omitempty"`  // kind=extensions
	Permissions *PermissionsView  `json:"permissions,omitempty"` // kind=permissions
	Lore        *LoreView         `json:"lore,omitempty"`        // kind=lore
	MCP         *MCPView          `json:"mcp,omitempty"`         // kind=mcp
	Raati       *RaatiView        `json:"raati,omitempty"`       // kind=raati
	Chat        *ChatView         `json:"chat,omitempty"`        // kind=chat
}

// ChatView is the chat-connector pane: every registered chat service and the
// live bridge. Workspace-scoped like the MCP pane — the bridge belongs to the
// workspace and is bound to one of its sessions.
type ChatView struct {
	Services []ChatServiceInfo `json:"services,omitempty"`
	Bridge   ChatBridgeState   `json:"bridge"`
	// DaemonPID is the `terva bot` daemon already polling this service, 0 when
	// none. Nonzero disables connect: two consumers race each update and one
	// always loses, so messages arrive half-delivered.
	DaemonPID int `json:"daemon_pid,omitempty"`
}

// ChatServiceInfo is one registered chat service, with the provenance a picker
// shows so an extension or dev connector is never mistaken for a compiled-in one.
type ChatServiceInfo struct {
	Name       string `json:"name"`
	Kind       string `json:"kind,omitempty"` // "" | "extension"
	Dev        bool   `json:"dev,omitempty"`
	Configured bool   `json:"configured"`
	Paired     bool   `json:"paired"` // a user already claimed the bot
}

// ChatBridgeState is the live bridge, or the zero value when idle. Session names
// the session the mirror is bound to: the bridge does NOT follow whichever
// session a client happens to be showing.
type ChatBridgeState struct {
	State     string `json:"state"` // idle | connecting | connected | error
	Connector string `json:"connector,omitempty"`
	Username  string `json:"username,omitempty"`
	PairedID  string `json:"paired_id,omitempty"` // "" until a user sends /start
	Session   string `json:"session,omitempty"`
	Error     string `json:"error,omitempty"` // set when State == "error"
}

// PermissionsView is the permissions inspector pane: the session's approval
// mode + compiled rules (read-only), and the live "always-allow" grants the
// operator can revoke. The mode's setter lives in the settings pane; here it is
// shown for context. Grants are session-scoped (the gate), so revoking one only
// affects this session.
type PermissionsView struct {
	Mode     string           `json:"mode"`
	Rules    []PermissionRule `json:"rules,omitempty"`
	AllowAll bool             `json:"allow_all,omitempty"` // "always allow everything" was chosen this session
	Grants   []string         `json:"grants,omitempty"`    // tools granted "always allow" this session
}

// PermissionRule is one compiled approval rule for the inspector: which tool
// (and optional argument pattern) resolves to what decision, and where it came
// from. The wire mirror of core.PermissionRule.
type PermissionRule struct {
	Tool      string `json:"tool"`
	Args      string `json:"args,omitempty"` // regexp source, when the rule is argument-scoped
	Decision  string `json:"decision"`       // allow | deny | ask
	Reason    string `json:"reason,omitempty"`
	Source    string `json:"source,omitempty"`    // user | project | extension | builtin
	Removable bool   `json:"removable,omitempty"` // a user rule the pane may delete
}

// MCPView is the MCP-management pane: the workspace's configured Model Context
// Protocol servers with their live status. Servers are workspace-global (shared
// across sessions); toggling one restarts/stops it and rebuilds every session's
// tools.
type MCPView struct {
	Servers []MCPServerInfo `json:"servers"`
}

// MCPServerInfo is one MCP server's config + live status rollup.
type MCPServerInfo struct {
	Name        string `json:"name"`
	Scope       string `json:"scope,omitempty"` // global | project
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`  // running | stopped | disabled | gated | failed
	Enabled     bool   `json:"enabled"` // config says it should run (toggle state)
	Tools       int    `json:"tools,omitempty"`
	Note        string `json:"note,omitempty"` // startup error, when failed
	// The per-scope toggle detail behind the Enabled/Status rollup (Status
	// collapses user- and project-disabled into one "disabled"), plus the
	// live connection ground truth a masked status can't carry.
	UserDisabled    bool `json:"user_disabled,omitempty"`
	ProjectDisabled bool `json:"project_disabled,omitempty"`
	Connected       bool `json:"connected,omitempty"`
}

// LoreView is the lore inspector pane: the session's authored keyword-triggered
// context entries (read-only — there is no in-process mutation API). It lists
// what lore exists and what triggers each, not what fired on a given turn.
type LoreView struct {
	Entries    []LoreEntry `json:"entries"`
	CanProject bool        `json:"can_project,omitempty"` // project-scope authoring available (trusted workspace)
}

// LoreEntry is one lore entry for the inspector.
type LoreEntry struct {
	Name     string   `json:"name"`
	Keys     []string `json:"keys,omitempty"`
	Constant bool     `json:"constant,omitempty"` // always active, no keyword needed
	Source   string   `json:"source,omitempty"`
	Content  string   `json:"content,omitempty"`  // full body (client truncates for display, uses full to edit)
	Editable bool     `json:"editable,omitempty"` // a web-managed entry (deletable/editable)
	Scope    string   `json:"scope,omitempty"`    // user | project (which tier the entry's file lives in)
}

// ExtensionsView is the extension-management pane: the session's installed +
// loaded extensions with their health, so an operator can see what's running,
// what's disabled or gated, and why one failed. Read-only in v1.
type ExtensionsView struct {
	Extensions []ExtensionInfo `json:"extensions"`
}

// ExtensionInfo is one extension's inventory + health rollup (mirrors the TUI's
// extensions dialog): manifest facts plus live ground truth from the driver.
type ExtensionInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Language    string `json:"language,omitempty"`
	Description string `json:"description,omitempty"`
	Scope       string `json:"scope,omitempty"` // global | project
	Status      string `json:"status"`          // running | stopped | disabled | gated
	Enabled     bool   `json:"enabled"`         // config says it should run (the toggle state)
	Tools       int    `json:"tools,omitempty"`
	Commands    int    `json:"commands,omitempty"`
	Note        string `json:"note,omitempty"` // e.g. crash reason (log tail) when off
	// The per-scope toggle detail behind the Enabled rollup, so a client can
	// offer the same global (manifest) vs project (config) toggles the TUI
	// does — Status alone can't say WHICH scope disabled an extension.
	GlobalEnabled      bool `json:"global_enabled,omitempty"`
	ProjectDisabled    bool `json:"project_disabled,omitempty"`
	UserConfigDisabled bool `json:"user_config_disabled,omitempty"`
	// Affordance flags for the config form and log viewer: the manifest
	// declares a config schema / a log file exists. The verbs themselves are
	// host-side (the in-process TUI reads logs and builds the form locally;
	// applying a saved config live is the extensions surface's "config"
	// action), but a client needs these to know when to offer the keys.
	HasConfig bool `json:"has_config,omitempty"`
	HasLog    bool `json:"has_log,omitempty"`
}

// CommandsView is the extension-command pane: every slash command an extension
// registered, surfaced to the browser as clickable buttons. It is the web's
// answer to the TUI's slash commands — the web has no command line, so a command
// is a button, not a "/name" you type (the TUI keeps the slash prompt). The
// client groups the entries by Ext; running one is
// surface.action {action:"run", args:{name}}, whose result (open a panel, submit
// a prompt, show a note) the daemon applies.
type CommandsView struct {
	Commands []CommandEntry `json:"commands"`
}

// CommandEntry is one runnable extension command.
type CommandEntry struct {
	Ext         string `json:"ext"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SettingsView is the settings pane: a flat list of adjustable settings. Each
// item carries its current value + how to render/change it; the client changes
// one with surface.action {action:"set", args:{key,value}}.
type SettingsView struct {
	Items []SettingItem `json:"items"`
}

// SettingItem is one adjustable setting.
type SettingItem struct {
	Key         string          `json:"key"`
	Label       string          `json:"label"`
	Type        string          `json:"type"`  // "enum" | "bool"
	Value       string          `json:"value"` // current: enum value, or "true"/"false"
	Options     []SettingOption `json:"options,omitempty"`
	Description string          `json:"description,omitempty"`
	Note        string          `json:"note,omitempty"` // e.g. "per-session, not saved"
}

// SettingOption is one choice of an enum SettingItem.
type SettingOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// TaskList is the background-agent (swarm) dashboard pane.
type TaskList struct {
	Tasks []TaskInfo `json:"tasks"`
}

// TaskInfo is one background agent, mapped from swarm.AgentSnapshot.
type TaskInfo struct {
	ID       string `json:"id"`
	Task     string `json:"task"`
	Status   string `json:"status"` // pending|running|done|failed|killed|detached
	Activity string `json:"activity,omitempty"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	Persona  string `json:"persona,omitempty"`
	Dir      string `json:"dir,omitempty"`      // agent working directory
	Started  string `json:"started,omitempty"`  // RFC 3339
	Finished string `json:"finished,omitempty"` // RFC 3339
	Err      string `json:"error,omitempty"`
	Tail     string `json:"tail,omitempty"` // last few transcript lines
	// Lines is the full in-memory transcript (capped daemon-side at 2000
	// lines per agent) — what a transcript viewer scrolls. The list pane
	// only needs Tail; a serialized carrier pays for Lines only when the
	// swarm state actually changed (fetches ride surface_updated).
	Lines []string `json:"lines,omitempty"`
}

// TaskBoardView is the per-session task-board pane (the built-in task_* tools'
// list) — distinct from TaskList, which is the workspace swarm dashboard. It
// carries the raw task fields so a frontend reconstructs them and renders via
// the shared helpers in packages/agent/tools/tasks (one renderer, no drift).
type TaskBoardView struct {
	Tasks []TaskBoardItem `json:"tasks"`
}

// TaskBoardItem is one task, mapped from tasks.Task. Timestamps are omitted:
// the board pane and status glance need only status + labels.
type TaskBoardItem struct {
	ID         string `json:"id"`
	Status     string `json:"status"` // pending|active|blocked|done|cancelled
	Title      string `json:"title"`
	ActiveForm string `json:"active_form,omitempty"`
	Evidence   string `json:"evidence,omitempty"`
	Note       string `json:"note,omitempty"`
}

// RaatiView is the deliberation board pane: the workspace's current (or
// last) RAATI deliberation — three unit blocks plus the tallied verdict
// (docs/proposals/raati-deliberation.md). An idle board (no units, no
// decision) renders the convene form.
type RaatiView struct {
	Running   bool   `json:"running"`
	Question  string `json:"question,omitempty"`
	Class     string `json:"class,omitempty"`      // advisory | gate | veto
	Round     int    `json:"round,omitempty"`      // 0 none, 1 blind, 2 cross-examination, 3 convergence
	SeatOrder string `json:"seat_order,omitempty"` // fixed | convene | turn — how the pool was dealt
	// Phase marks pre-deliberation work: "briefing" while the clerk
	// summarizes the attached conversation, before any unit is seated.
	Phase string `json:"phase,omitempty"`
	// Archived + When mark a persisted past record being viewed rather
	// than a live run; History is the archive list (newest first).
	Archived bool               `json:"archived,omitempty"`
	When     string             `json:"when,omitempty"` // RFC 3339
	History  []RaatiHistoryItem `json:"history,omitempty"`
	Binding  string             `json:"binding,omitempty"`
	Units    []RaatiUnit        `json:"units,omitempty"`
	Decision string             `json:"decision,omitempty"` // approved | rejected | escalated
	Degraded bool               `json:"degraded,omitempty"`
	Tally    *RaatiTally        `json:"tally,omitempty"`
	Minority []RaatiVoice       `json:"minority,omitempty"`
	// Inquiries is the panel's between-round Q&A docket (the inquiry
	// protocol): what each unit asked, what came back, and from where —
	// "unanswered" entries mark decisions made with open questions.
	Inquiries []RaatiInquiry `json:"inquiries,omitempty"`
	// Profiles lists the user's configured convening profiles for the
	// form's picker: names and what-each-is-for only — a profile's
	// contents (seats, defaults) stay server-side, applied when the
	// convene action names one.
	Profiles []RaatiProfileInfo `json:"profiles,omitempty"`
	Err      string             `json:"error,omitempty"` // operational failure of the run
}

// RaatiInquiry is one entry of the panel's between-round Q&A docket.
type RaatiInquiry struct {
	Unit     string `json:"unit"`
	Question string `json:"question"`
	Answer   string `json:"answer,omitempty"`
	Source   string `json:"source,omitempty"` // record | convener | unanswered
	Round    int    `json:"round,omitempty"`
}

// RaatiProfileInfo names one configured convening profile for pickers.
type RaatiProfileInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// RaatiUnit is one panelist block on the board.
type RaatiUnit struct {
	Name       string  `json:"name"`
	Accent     string  `json:"accent,omitempty"`  // #RRGGBB from the persona charter
	Binding    string  `json:"binding,omitempty"` // this seat's "provider/model"
	Status     string  `json:"status"`            // deliberating | voted | absent
	Verdict    string  `json:"verdict,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Rationale  string  `json:"rationale,omitempty"`
	Why        string  `json:"why,omitempty"`   // absence cause
	Blind      string  `json:"blind,omitempty"` // round-1 verdict, so revisions are visible
}

// RaatiTally is the head count behind the verdict.
type RaatiTally struct {
	Approve int `json:"approve"`
	Reject  int `json:"reject"`
	Abstain int `json:"abstain"`
	Absent  int `json:"absent"`
}

// RaatiVoice is one minority-report entry — the dissent is the product.
type RaatiVoice struct {
	Unit      string `json:"unit"`
	Rationale string `json:"rationale,omitempty"`
}

// RaatiHistoryItem is one past deliberation in the board's archive
// list, loadable via the "show" action.
type RaatiHistoryItem struct {
	ID       string      `json:"id"`
	When     string      `json:"when,omitempty"` // RFC 3339
	Question string      `json:"question"`
	Class    string      `json:"class,omitempty"`
	Decision string      `json:"decision"`
	Degraded bool        `json:"degraded,omitempty"`
	Tally    *RaatiTally `json:"tally,omitempty"`
	Minority []string    `json:"minority,omitempty"` // dissenting unit names
}

// UsageView is the usage/budget pane: the TUI status bar's live usage picture as
// a standalone surface (context tokens, cumulative usage, subscription windows).
type UsageView struct {
	Provider      string            `json:"provider,omitempty"`
	Model         string            `json:"model,omitempty"`
	ContextTokens int               `json:"context_tokens,omitempty"`
	Window        int               `json:"window,omitempty"`
	Cumulative    core.WireUsage    `json:"cumulative"`
	Subscription  bool              `json:"subscription,omitempty"`
	Windows       []UsageWindowInfo `json:"windows,omitempty"`
}

// CatalogView is the effective web string catalog for one language, in the
// runtime i18n JSON shape: English-as-key singular translations plus plural
// entries keyed by the "one|other" English composite (CLDR category → form).
// The client resolves its own strings against it, falling back to the key.
type CatalogView struct {
	Lang     string                       `json:"lang"`
	Singular map[string]string            `json:"singular,omitempty"`
	Plural   map[string]map[string]string `json:"plural,omitempty"`
}

// PanelView is an extension-owned panel (or aggregated status): styled text
// lines with an optional footer, the existing extproto.PanelSpec shape.
type PanelView struct {
	Ext    string   `json:"ext,omitempty"`
	Lines  []string `json:"lines"`
	Footer string   `json:"footer,omitempty"`
}

// Widget is one node of a generic pane tree (the extensible content model). Its
// vocabulary is defined for the wire but not yet rendered — extension panes
// currently use PanelView. Type discriminates; the other fields are per-type.
type Widget struct {
	Type     string     `json:"type"` // heading|text|meter|keyvalue|table|list|group|note|action|divider
	Text     string     `json:"text,omitempty"`
	Tone     string     `json:"tone,omitempty"` // default|muted|danger|ok|warn
	Level    int        `json:"level,omitempty"`
	Label    string     `json:"label,omitempty"`
	Value    float64    `json:"value,omitempty"`
	Max      float64    `json:"max,omitempty"`
	Unit     string     `json:"unit,omitempty"`
	Rows     []KV       `json:"rows,omitempty"`
	Columns  []string   `json:"columns,omitempty"`
	Cells    [][]string `json:"cells,omitempty"`
	Items    []ListItem `json:"items,omitempty"`
	Children []Widget   `json:"children,omitempty"`
	ActionID string     `json:"action_id,omitempty"`
}

// KV is one row of a keyvalue widget.
type KV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Note  string `json:"note,omitempty"`
	Mono  bool   `json:"mono,omitempty"`
}

// ListItem is one entry of a list widget.
type ListItem struct {
	Text     string `json:"text"`
	Note     string `json:"note,omitempty"`
	Tone     string `json:"tone,omitempty"`
	ActionID string `json:"action_id,omitempty"`
}

// ModelInfo describes one switchable model. It mirrors the SDK's ModelInfo.
type ModelInfo struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	ContextWindow int    `json:"context_window,omitempty"`
	MaxOutput     int    `json:"max_output,omitempty"`
	Reasoning     bool   `json:"reasoning,omitempty"`
	Current       bool   `json:"current,omitempty"`
	Favorite      bool   `json:"favorite,omitempty"` // pinned by the user (the ★ favorites view)
}
