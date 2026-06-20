// Package ext is the Go SDK for writing terva extensions.
//
// An extension is a subprocess that talks to terva over its stdin/stdout
// in newline-delimited JSON. This package wraps the wire format so
// extension authors can write straightforward Go without reimplementing
// the protocol.
//
// Minimal example (registers /hello and replies with a static prompt):
//
//	package main
//
//	import "terva.sh/terva/packages/agent/ext"
//
//	func main() {
//	    ext := ext.New("hello", "1.0.0")
//	    ext.Command("hello", "say hi", func(args string) ext.Response {
//	        return ext.Prompt("Greet me in three different languages.")
//	    })
//	    ext.Run()
//	}
//
// Build it, drop the binary + an extension.json next to it under
// `$TERVA_HOME/extensions/hello/`, and terva picks it up on next launch.
//
// The same wire format also has reference clients in TypeScript and
// Python under examples/extensions/. Use whichever language fits.
package ext

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"terva.sh/terva/packages/agent/extproto"
)

func base64Encode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// CommandHandler is invoked when the user runs the extension's
// registered slash command. args is everything the user typed after
// the command name (already trimmed). Return a Response describing
// what terva should do next.
type CommandHandler func(args string) Response

// ToolHandler is invoked when the LLM calls a tool the extension
// registered. args is the raw JSON object the model produced; the
// handler is responsible for parsing/validating it. Return a
// ToolResult describing what terva should send back to the model.
type ToolHandler func(args json.RawMessage) ToolResult

// EventHandler is called for each lifecycle event the extension
// subscribed to via Subscribe. The handler is invoked synchronously
// on the read goroutine; keep it quick or hand off to your own
// worker.
type EventHandler func(ev Event)

// Event is a lifecycle notification from terva. The fields populated
// depend on Name (the host's event_name string):
//
//	session_start    : SessionID, SessionPath, SessionTitle (protocol 2+)
//	turn_start       : Step
//	turn_end         : Stop, optional Error
//	tool_call        : ToolID, ToolName, ToolArgs
//	user_message     : Text
//	assistant_message: Text
//	workspace_changed: Files
type Event struct {
	Name string

	Step  int
	Stop  string
	Error string

	ToolID   string
	ToolName string
	ToolArgs json.RawMessage

	Text string

	// Files carries the per-turn workspace diff on a workspace_changed
	// event. Empty on every other event. Use OnWorkspaceChanged for the
	// typed convenience path.
	Files []FileChange

	// SessionID / SessionPath / SessionTitle identify the active
	// session on a session_start event (host protocol 2+). An empty
	// SessionID means no active session (e.g. --no-session, or the
	// session was closed). Use OnSession for the common re-keying case.
	SessionID    string
	SessionPath  string
	SessionTitle string
	// CWD / ProjectID ride a session_start event (host protocol 2+) and
	// refresh on a /cd. See Session / OnSession for the common case.
	CWD       string
	ProjectID string
}

// Session identifies the active terva session. It is delivered to an
// OnSession handler whenever the active session changes (open, resume,
// fork, /new, or close). A zero ID means no active session.
type Session struct {
	ID    string
	Path  string
	Title string
	// CWD is the session's working directory and ProjectID its stable
	// project key (core.ProjectKey). Both refresh on a /cd. Empty when
	// the host predates protocol 2 or there is no active session.
	CWD       string
	ProjectID string
}

// InterceptHandler decides whether a tool call may proceed. Return
// (allow=true) to permit, (allow=false, reason) to refuse. The
// reason is shown to the model as the tool's error text.
//
// This is the original 2-value handler. For richer control (arg
// rewrites, turn_start / assistant_message interception, etc.)
// register via InterceptToolCallX, InterceptTurnStart, or
// InterceptAssistantMessage.
type InterceptHandler func(toolName string, args json.RawMessage) (allow bool, reason string)

// ToolCallDecision is the richer reply an InterceptToolCallHandler
// can return. Zero value means "allow, pass through".
type ToolCallDecision struct {
	// Block refuses the call. The model sees Reason as the tool's
	// error text and typically proposes a different action.
	Block bool
	// Reason is the refusal text shown to the model on Block, or a
	// logged note when passing through (optional).
	Reason string
	// ModifiedArgs, when non-nil and Block is false, replaces the
	// JSON args the tool actually runs with. Lets guards redact /
	// augment / patch the model's request without rewriting the
	// transcript. Must encode to a JSON object.
	ModifiedArgs json.RawMessage
}

// ToolCallHandler is the richer form of an interceptor. Use this
// when you want to rewrite args instead of only blocking.
type ToolCallHandler func(toolName string, args json.RawMessage) ToolCallDecision

// TurnStartDecision controls whether the next model call runs. Zero
// value means "allow".
type TurnStartDecision struct {
	Block  bool
	Reason string
}

// TurnStartHandler is called before every turn's model call. Return
// Block=true with Reason to abort the turn (shown to the user as a
// status line). Useful for rate-limiting and business-hour gates.
type TurnStartHandler func(step int) TurnStartDecision

// AssistantMessageDecision controls the final assistant-text
// rendering. Zero value means "allow, show as-is".
type AssistantMessageDecision struct {
	// Block suppresses the message from the user entirely. The
	// transcript still records the model's original output so the
	// model sees what it said on the next turn.
	Block bool
	// Reason is logged when blocking (optional).
	Reason string
	// ReplaceText, when non-empty and Block is false, is what the
	// user sees. The model's original text still lives in the
	// transcript.
	ReplaceText string
}

// AssistantMessageHandler is called after the model's final text is
// assembled but before it's shown. Use it to scrub secrets, expand
// templates, enforce tone, or suppress responses entirely.
type AssistantMessageHandler func(text string) AssistantMessageDecision

// UserMessage carries the text of a genuine user prompt (the initial
// submit or one drained from the queue). Delivered to OnUserMessage
// handlers. The host's at-close gate nudge is NOT delivered here.
type UserMessage struct {
	Text string
}

// UserMessageDecision controls whether a user prompt reaches the model.
// Zero value means "allow, pass through unchanged".
type UserMessageDecision struct {
	// Block rejects the prompt: it is neither recorded in the transcript
	// nor sent to the model. The host surfaces Reason to the user.
	Block bool
	// Reason is shown to the user when blocking (optional).
	Reason string
	// ReplaceText, when non-empty and Block is false, rewrites the prompt
	// the model actually receives — the rewrite IS what lands in the
	// transcript. Use to redact secrets or inject standing context.
	ReplaceText string
}

// UserMessageHandler is called just before a user prompt is appended to
// the transcript and sent to the model. Return Block=true to reject it,
// or ReplaceText to rewrite it. Useful for guardrails, redaction, and
// prompt augmentation. Runs on both the initial prompt and queued
// prompts, so a guard can't be bypassed by typing while the agent works.
type UserMessageHandler func(text string) UserMessageDecision

// ToolResult is the extension's reply to a tool invocation. Build
// one with TextResult, ImageResult, or directly when you need to
// combine multiple blocks.
type ToolResult struct {
	Content []ToolContent
	IsError bool
}

// ToolContent is one block of tool output. Either Text is set, or
// MimeType+Data (base64-encoded). Use the Text/Image helpers below.
type ToolContent struct {
	Type     string // "text" | "image"
	Text     string
	MimeType string
	Data     string // base64
}

// Text returns a text content block.
func Text(s string) ToolContent { return ToolContent{Type: "text", Text: s} }

// Image returns an image content block. data must already be
// base64-encoded; use ImageBytes to encode raw bytes.
func Image(mimeType, base64Data string) ToolContent {
	return ToolContent{Type: "image", MimeType: mimeType, Data: base64Data}
}

// ImageBytes returns an image content block from raw bytes,
// encoding them to base64 for the wire.
func ImageBytes(mimeType string, data []byte) ToolContent {
	return ToolContent{Type: "image", MimeType: mimeType, Data: base64Encode(data)}
}

// TextResult returns a tool result with one text block, success.
func TextResult(s string) ToolResult { return ToolResult{Content: []ToolContent{Text(s)}} }

// TextErrorResult returns a tool result with one text block, marked
// as an error to the model.
func TextErrorResult(s string) ToolResult {
	return ToolResult{Content: []ToolContent{Text(s)}, IsError: true}
}

// Response tells terva how to react to a command invocation. Construct
// one with Prompt(), Insert(), Display(), or Noop().
type Panel struct {
	ID     string
	Title  string
	Lines  []string
	Footer string
}

type Response struct {
	Action    string // "prompt", "insert", "display", "open_panel", "noop"
	Prompt    string
	Insert    string
	Display   string
	OpenPanel *Panel
	Error     string
}

// Prompt returns a Response that submits text as a fresh user message
// to the agent (running it through the model loop as if the user had
// typed and pressed enter).
func Prompt(text string) Response { return Response{Action: "prompt", Prompt: text} }

// Insert returns a Response that drops text into the editor at the
// cursor without submitting.
func Insert(text string) Response { return Response{Action: "insert", Insert: text} }

// Display returns a Response that adds a one-shot styled note to the
// chat without invoking the model. Useful for showing a result without
// burning tokens.
func Display(text string) Response { return Response{Action: "display", Display: text} }

// OpenPanel returns a Response that opens an interactive extension-owned
// panel inside terva.
func OpenPanel(id, title string, lines []string, footer string) Response {
	return Response{Action: "open_panel", OpenPanel: &Panel{ID: id, Title: title, Lines: lines, Footer: footer}}
}

// Noop returns a Response that signals "I handled it, no UI change".
// Use after pushing your own state or notifications.
func Noop() Response { return Response{Action: "noop"} }

// Errorf returns a Response that surfaces the error in the chat as a
// red status line.
func Errorf(format string, args ...any) Response {
	return Response{Action: "noop", Error: fmt.Sprintf(format, args...)}
}

// Extension is one terva extension. Construct with New, register
// commands, then call Run.
type Extension struct {
	name    string
	version string

	in      io.Reader
	out     io.Writer
	stderr  io.Writer
	writeMu sync.Mutex

	mu                 sync.Mutex
	commands           map[string]CommandHandler
	descriptions       []descTuple // ordered so register frames arrive in registration order
	tools              map[string]ToolHandler
	toolDefs           []toolDef // ordered so register frames arrive in registration order
	eventHandlers      map[string]EventHandler
	eventNames         []string // declared subscription order
	onSession          func(Session)
	onSessionEnd       func(Session)
	onCompaction       func(Compaction)
	onCompactStart     func(CompactStart)
	onUserMessage      func(UserMessage)
	onWorkspaceChanged func(WorkspaceChange)
	onRunEnd           func(RunEnd)

	interceptTool      InterceptHandler
	interceptToolRich  ToolCallHandler
	interceptOn        bool
	interceptTurn      TurnStartHandler
	interceptAssistant AssistantMessageHandler
	interceptUser      UserMessageHandler
	panelKeys          map[string]func(key, text string)
	panelCloses        map[string]func()

	// Caps reported in the hello frame.
	caps []string

	// minProtocol is the lowest host protocol version this extension
	// can run against, reported in the hello frame. 0 = no minimum.
	minProtocol int

	// contextContribution is the static guidance sent via
	// ContributeContext, flushed as a register_context frame in Run.
	contextContribution string

	// hostInfo is filled in once HelloAck arrives.
	host HostInfo

	// pending correlates an outbound ext→host request (host_tool_call,
	// list_sessions, read_session — protocol 3) with its reply. The Run
	// loop routes a reply frame to the waiting channel by id. seq mints
	// the ids. These are the ONLY request/response calls the ext makes;
	// every other inbound frame is host-initiated.
	pendingMu sync.Mutex
	pending   map[string]chan json.RawMessage
	seq       atomic.Uint64
}

type descTuple struct {
	name, desc string
}

type toolDef struct {
	name        string
	description string
	schema      json.RawMessage
	readOnly    bool
	authority   string
}

// ToolOption configures a tool at registration time. Pass options as the
// trailing args to Tool.
type ToolOption func(*toolDef)

// ReadOnly marks a tool as having no side effects (the MCP readOnlyHint
// analog). A host that understands the hint may admit the tool in
// read-only / workspace approval modes without prompting — use it for
// tools that only inspect (a lister, a search, a status query) so they
// don't ask on every call. Declaring it is a promise about behavior;
// an extension that lies here only loosens its own user's policy.
func ReadOnly() ToolOption { return func(t *toolDef) { t.readOnly = true } }

// Authority effect-classes a tool can declare via WithAuthority. These
// mirror core.Authority on the wire (kept as strings so this SDK stays
// standalone, the same hand-mirroring extproto uses). Authority is the
// richer successor to ReadOnly: a network-read tool reads nothing on the
// local machine yet must not be auto-allowed as read-only.
const (
	AuthorityLocalRead = "local-read"
	// AuthorityLocalData: reads AND writes only the extension's own
	// host-managed data dir ($TERVA_HOME/ext-data/<name>) — never the
	// user's workspace, network, or anything external. Auto-allowable like
	// local-read, so a memory/notes/state tool needs no approval prompt.
	AuthorityLocalData       = "local-data"
	AuthorityWorkspaceMutate = "workspace-mutation"
	AuthorityProcessExec     = "process-execution"
	AuthorityNetworkRead     = "network-read"
	AuthorityExternalMutate  = "external-mutation"
)

// WithAuthority declares the tool's effect class (use the Authority*
// constants). It supersedes ReadOnly for host classification: local-read
// and local-data are auto-allowable, while a tool declared network-read
// is gated like a side-effecting tool rather than admitted as read-only.
// When both are set, authority wins.
func WithAuthority(class string) ToolOption { return func(t *toolDef) { t.authority = class } }

// HostInfo is what the host (terva) tells us in HelloAck. Useful for
// extensions that want to behave differently per provider.
//
// SessionID / SessionPath / SessionTitle are NOT part of the handshake
// (the session opens after extensions spawn). They start empty and are
// kept current as session_start events arrive — updated before the
// matching OnSession handler runs, so reading Host().SessionID inside a
// tool or panel handler always reflects the active session.
type HostInfo struct {
	ProtocolVersion int
	ZotVersion      string // rename:keep — public SDK API
	Provider        string
	Model           string
	// CWD is the working directory. It is seeded at the handshake and
	// then refreshed on every session_start that carries one, so it
	// follows a /cd instead of staying on the launch cwd.
	CWD          string
	ExtensionDir string
	DataDir      string

	// SupportedEvents lists the lifecycle events this host advertised it
	// can emit (from the hello_ack). Check one with Emits. Empty on an
	// older host that doesn't advertise — treat that as "unknown".
	SupportedEvents []string

	SessionID    string
	SessionPath  string
	SessionTitle string
	// ProjectID is the host's stable, collision-proof key for CWD
	// (core.ProjectKey). Use it to scope per-project state. Refreshes
	// alongside CWD on session_start.
	ProjectID string
}

// Emits reports whether the host advertised (in the hello_ack) that it can
// emit the named lifecycle event — e.g. Host().Emits("transcript_compacted").
// Use it to adapt or warn. An older host that doesn't advertise returns
// false, so an extension that simply wants the event should subscribe
// optimistically (OnCompaction etc.) and degrade, not gate on this.
func (h HostInfo) Emits(event string) bool {
	return slices.Contains(h.SupportedEvents, event)
}

// New constructs an Extension with the given identifier. name should
// match the name field in extension.json.
func New(name, version string) *Extension {
	return &Extension{
		name:          name,
		version:       version,
		in:            os.Stdin,
		out:           os.Stdout,
		stderr:        os.Stderr,
		commands:      map[string]CommandHandler{},
		tools:         map[string]ToolHandler{},
		eventHandlers: map[string]EventHandler{},
		panelKeys:     map[string]func(key, text string){},
		panelCloses:   map[string]func(){},
		pending:       map[string]chan json.RawMessage{},
		caps:          []string{"commands", "tools", "events", "panels"},
	}
}

// Host returns the HostInfo received during the hello handshake.
// Returns the zero value if Run hasn't started yet.
func (e *Extension) Host() HostInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.host
}

// ProjectDataDir returns the per-project writable directory for this
// extension — DataDir/projects/<ProjectID> — creating it on call. Use
// it for state that belongs to one project/working-tree (a task board,
// a worktree registry). ProjectID refreshes on a /cd, so call this off a
// fresh Host() (e.g. inside an OnSession handler) rather than caching the
// result. Before the first session_start, or under --no-session, the
// ProjectID is empty and state lands in a shared "_noproject" bucket.
func (h HostInfo) ProjectDataDir() (string, error) {
	if h.DataDir == "" {
		return "", fmt.Errorf("ext: no data dir (host predates this field)")
	}
	proj := h.ProjectID
	if proj == "" {
		proj = "_noproject"
	}
	dir := filepath.Join(h.DataDir, "projects", proj)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// DataFS returns a read-through view that layers the writable DataDir
// (upper) over the read-only ExtensionDir (lower). Reads prefer DataDir
// and fall through to the install dir; writes always land in DataDir
// (copy-on-write). This gives two things at once: ship default
// assets/config in the install dir and let the user override them in
// DataDir, and — because the install dir was the *old* DataDir — keep
// data written before the data dir moved readable until it's rewritten.
func (h HostInfo) DataFS() DataFS { return DataFS{upper: h.DataDir, lower: h.ExtensionDir} }

// DataFS is a two-layer file view: a writable upper directory over a
// read-only lower one. Construct it via HostInfo.DataFS. Names are
// relative paths within the layers; absolute paths and paths escaping
// the layer (via "..") are rejected.
type DataFS struct {
	upper string // DataDir — writable
	lower string // ExtensionDir — read-only, never written
}

func cleanRel(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("ext: empty path")
	}
	escape := func() (string, error) {
		return "", fmt.Errorf("ext: path %q escapes the data layer", name)
	}
	// Reject absolute, volume-qualified, and rooted paths up front, on
	// every OS. filepath.IsAbs is not enough: on Windows a leading-slash
	// path like "/etc/passwd" is NOT absolute (no drive), so it would slip
	// through and be joined onto the layer root.
	if filepath.IsAbs(name) || filepath.VolumeName(name) != "" ||
		strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return escape()
	}
	c := filepath.Clean(name)
	if filepath.IsAbs(c) || c == "." || c == ".." ||
		strings.HasPrefix(c, ".."+string(filepath.Separator)) ||
		strings.HasPrefix(c, "/") || strings.HasPrefix(c, `\`) {
		return escape()
	}
	return c, nil
}

// ReadFile returns name from the writable layer if present, otherwise
// from the read-only install layer. A genuine error on the upper layer
// (e.g. permissions) surfaces rather than silently falling through.
func (f DataFS) ReadFile(name string) ([]byte, error) {
	rel, err := cleanRel(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(f.upper, rel))
	if err == nil {
		return b, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	if f.lower == "" {
		return nil, err
	}
	return os.ReadFile(filepath.Join(f.lower, rel))
}

// WriteFile writes name to the writable layer (copy-on-write), creating
// parent directories. It never touches the read-only install layer.
func (f DataFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	rel, err := cleanRel(name)
	if err != nil {
		return err
	}
	if f.upper == "" {
		return fmt.Errorf("ext: no writable data dir")
	}
	p := filepath.Join(f.upper, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, perm)
}

// Exists reports whether name resolves in either layer.
func (f DataFS) Exists(name string) bool {
	rel, err := cleanRel(name)
	if err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(f.upper, rel)); err == nil {
		return true
	}
	if f.lower == "" {
		return false
	}
	_, err = os.Stat(filepath.Join(f.lower, rel))
	return err == nil
}

// Path returns the writable path where a write to name would land,
// without creating anything. Useful for handing a path to a child
// process or another tool.
func (f DataFS) Path(name string) (string, error) {
	rel, err := cleanRel(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(f.upper, rel), nil
}

// RequireProtocol declares the minimum host protocol version this
// extension needs. A host that speaks a lower version refuses to load
// the extension (with a clear message) instead of running it against
// a wire it only half-understands. Call before Run. Use this when the
// extension depends on a wire feature added in a specific protocol
// version — e.g. tool_result fanout (protocol 1+) — so it fails
// cleanly on an older host rather than silently missing events.
func (e *Extension) RequireProtocol(version int) { e.minProtocol = version }

// Logf writes a line to the extension's stderr, which terva captures to
// $TERVA_HOME/logs/ext-<name>.log. Use this for debug output: anything
// you print to stdout would corrupt the JSON wire protocol.
func (e *Extension) Logf(format string, args ...any) {
	fmt.Fprintf(e.stderr, "["+e.name+"] "+format+"\n", args...)
}

// OnPanelKey registers callbacks for panel key + close events.
func (e *Extension) OnPanelKey(panelID string, onKey func(key, text string), onClose func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if onKey != nil {
		e.panelKeys[panelID] = onKey
	}
	if onClose != nil {
		e.panelCloses[panelID] = onClose
	}
}

// OpenPanel opens an interactive panel spontaneously from extension code
// without requiring a slash command. Safe to call from a tool handler goroutine
// or any background context. The panel receives panel_key events via OnPanelKey
// and can be dismissed with ClosePanel, exactly as with command-response panels.
func (e *Extension) OpenPanel(id, title string, lines []string, footer string) {
	_ = e.send(extproto.OpenPanelFromExt{
		Type:  "open_panel",
		Panel: extproto.PanelSpec{ID: id, Title: title, Lines: lines, Footer: footer},
	})
}

// RenderPanel pushes a fresh frame for panelID.
func (e *Extension) RenderPanel(panelID, title string, lines []string, footer string) {
	_ = e.send(extproto.PanelRenderFromExt{Type: "panel_render", PanelID: panelID, Title: title, Lines: lines, Footer: footer})
}

// ClosePanel tells terva to close panelID.
func (e *Extension) ClosePanel(panelID string) {
	_ = e.send(extproto.PanelCloseFromExt{Type: "panel_close", PanelID: panelID})
}

// Command registers a slash-command handler. Call this BEFORE Run().
// Once Run is going, when the user runs /name in terva, fn is invoked
// with the remaining args.
//
// Naming conflicts with built-in commands (e.g. /help) are silently
// shadowed by the built-in; check the user's "ext logs" if a command
// you registered isn't taking effect.
func (e *Extension) Command(name, description string, fn CommandHandler) {
	e.mu.Lock()
	e.commands[name] = fn
	e.descriptions = append(e.descriptions, descTuple{name: name, desc: description})
	e.mu.Unlock()
}

// Tool registers an LLM-callable tool. schema is a JSON Schema
// object describing the tool's args (the same shape Anthropic /
// OpenAI accept). Call this BEFORE Run(); terva folds extension tools
// into the agent's registry once the extension's ready frame fires.
//
// Naming conflicts with built-in tools (read, write, edit, bash,
// skill) are silently shadowed by the built-in.
//
// Pass ReadOnly() to declare the tool side-effect free so the host can
// admit it without a permission prompt in read-only / workspace modes.
func (e *Extension) Tool(name, description string, schema json.RawMessage, fn ToolHandler, opts ...ToolOption) {
	td := toolDef{name: name, description: description, schema: schema}
	for _, opt := range opts {
		opt(&td)
	}
	e.mu.Lock()
	e.tools[name] = fn
	e.toolDefs = append(e.toolDefs, td)
	e.mu.Unlock()
}

// On subscribes to a lifecycle event. fn is called for each
// notification; the same name can only have one handler (later
// registrations replace earlier ones). Recognised names:
// session_start, turn_start, turn_end, tool_call,
// assistant_message.
func (e *Extension) On(name string, fn EventHandler) {
	e.mu.Lock()
	e.ensureSubscribed(name)
	e.eventHandlers[name] = fn
	e.mu.Unlock()
}

// OnSession registers a callback for changes to the active session. It
// fires once after the session opens and again on every switch
// (/sessions resume, fork, /new) and on close (with a zero-value
// Session, i.e. empty ID). It is the convenient path for re-keying
// per-session state. Sugar over On("session_start", …): Host() is
// already updated when the callback runs, so an extension can also read
// Host().SessionID directly from a tool or panel handler. Requires host
// protocol 2+ (declare RequireProtocol(2)); call before Run.
func (e *Extension) OnSession(fn func(Session)) {
	e.mu.Lock()
	e.onSession = fn
	e.ensureSubscribed("session_start")
	e.mu.Unlock()
}

// OnSessionEnd registers a callback fired when a session is ending — the
// bookend to OnSession. It fires for the OUTGOING session just before a
// switch or close announces the next one, and once more for the active
// session at host shutdown. The Session carries the ending session's
// identity. Use it to flush a memory store or index the just-finished
// session. Best-effort: a hard kill (SIGKILL) skips it, so persist
// incrementally and treat this as a flush point, not a guarantee. Sugar
// over On("session_end", …).
func (e *Extension) OnSessionEnd(fn func(Session)) {
	e.mu.Lock()
	e.onSessionEnd = fn
	e.ensureSubscribed("session_end")
	e.mu.Unlock()
}

// ensureSubscribed adds name to the event subscription list if it is
// not already present, so On and OnSession can both request
// session_start without subscribing twice. Caller holds e.mu.
func (e *Extension) ensureSubscribed(name string) {
	if !slices.Contains(e.eventNames, name) {
		e.eventNames = append(e.eventNames, name)
	}
}

// Compaction carries the host's post-compaction signal to an OnCompaction
// handler. It is currently empty — the event itself is the value
// ("re-snapshot now") — but kept a struct so metadata (reason,
// token/message counts) can be added later without breaking the handler
// signature.
type Compaction struct{}

// CompactStart carries the host's pre-compaction signal to an
// OnCompactStart handler. Reason is short human-readable prose (e.g.
// "context near limit"). Fires BEFORE the transcript is squashed — the
// window to harvest detail that the post-compaction summary drops.
type CompactStart struct {
	Reason string
}

// RunEnd signals the agent finished a whole prompt (all steps and the
// at-close gate) — the per-prompt bookend to a user message, distinct
// from the per-step turn_end. Empty for now (the event is the value);
// kept a struct so fields can be added without breaking the handler.
type RunEnd struct{}

// FileChange is one entry in a workspace diff: a workspace-relative,
// slash-separated path and what happened to it during the turn.
type FileChange struct {
	Path   string
	Change string // "added" | "modified" | "deleted"
}

// WorkspaceChange carries the net set of file changes a turn made to the
// workspace, delivered to an OnWorkspaceChanged handler. Files is sorted
// by path and never empty (a turn that changed nothing fires no event).
type WorkspaceChange struct {
	Files []FileChange
}

// OnCompaction registers a handler that fires after the host compacts the
// transcript, before the next model turn. Use it to re-snapshot
// refreshable context (RefreshContext): e.g. a memory extension whose
// frozen session-start snapshot no longer reflects mid-session writes that
// compaction summarized out of the transcript.
//
// Purely additive and opt-in — a host that doesn't emit the event (an
// older terva) simply never fires this, and the extension degrades to its
// session-boundary refresh. No RequireProtocol is needed: the host records
// the subscription either way and only emits when it can.
func (e *Extension) OnCompaction(fn func(Compaction)) {
	e.mu.Lock()
	e.onCompaction = fn
	e.ensureSubscribed("transcript_compacted")
	e.mu.Unlock()
}

// OnCompactStart registers a handler that fires when the host is ABOUT to
// compact the transcript — the pre-event paired with OnCompaction (post).
// Because compaction runs a slow LLM summarization, the handler has time
// to read the full session (read_session) and harvest detail before it's
// summarized away. Purely additive and opt-in.
func (e *Extension) OnCompactStart(fn func(CompactStart)) {
	e.mu.Lock()
	e.onCompactStart = fn
	e.ensureSubscribed("compact_start")
	e.mu.Unlock()
}

// OnRunEnd registers a handler that fires once when the agent finishes a
// whole prompt — the per-prompt bookend to OnUserMessage, distinct from
// the per-step turn lifecycle. Use it to act when the agent goes idle:
// summarize the exchange, run a post-turn check, or flush state.
func (e *Extension) OnRunEnd(fn func(RunEnd)) {
	e.mu.Lock()
	e.onRunEnd = fn
	e.ensureSubscribed("run_end")
	e.mu.Unlock()
}

// OnUserMessage registers a handler fired for every genuine user prompt
// (the initial submit and queued follow-ups) — the symmetric counterpart
// to observing assistant output. Use it to harvest intent for a memory
// store or feed a session index. The host's synthetic at-close gate
// nudge is not delivered. To rewrite or reject a prompt instead of just
// observing it, use InterceptUserMessage.
func (e *Extension) OnUserMessage(fn func(UserMessage)) {
	e.mu.Lock()
	e.onUserMessage = fn
	e.ensureSubscribed("user_message")
	e.mu.Unlock()
}

// OnWorkspaceChanged registers a handler fired once at the end of each
// agent run with the net set of files added/modified/deleted in the
// workspace during that run (honoring .gitignore, ignoring .git). A run
// that changed nothing fires no event. Use it to keep a code index fresh
// or note edits in a memory store. The host derives this by diffing the
// workspace at run boundaries, so it catches bash side effects and
// external edits too — not just the agent's own write/edit tools.
func (e *Extension) OnWorkspaceChanged(fn func(WorkspaceChange)) {
	e.mu.Lock()
	e.onWorkspaceChanged = fn
	e.ensureSubscribed("workspace_changed")
	e.mu.Unlock()
}

// InterceptToolCall registers a guard that runs immediately before
// each tool call. Returning (false, reason) refuses the call; the
// model sees reason as the tool error text. Multiple extensions can
// install interceptors; if any one refuses, the call is blocked.
//
// Use this to build permission gates: refuse `bash` calls containing
// `rm -rf`, ask the user for confirmation on dangerous patterns,
// audit-log every call, etc.
//
// For the richer form (arg rewrites, structured decisions) use
// InterceptToolCallX.
func (e *Extension) InterceptToolCall(fn InterceptHandler) {
	e.mu.Lock()
	e.interceptTool = fn
	e.interceptOn = true
	e.mu.Unlock()
}

// InterceptToolCallX is the richer variant. Return a ToolCallDecision
// to block, allow, or rewrite args mid-flight. If both
// InterceptToolCall and InterceptToolCallX are set, the X form wins.
func (e *Extension) InterceptToolCallX(fn ToolCallHandler) {
	e.mu.Lock()
	e.interceptToolRich = fn
	e.interceptOn = true
	e.mu.Unlock()
}

// InterceptTurnStart registers a guard that runs before every turn's
// model call. Return Block=true with Reason to abort the turn.
// Useful for deny-by-default gates and usage quotas.
func (e *Extension) InterceptTurnStart(fn TurnStartHandler) {
	e.mu.Lock()
	e.interceptTurn = fn
	e.mu.Unlock()
}

// InterceptAssistantMessage registers a guard that runs after the
// model's final text is assembled but before it's shown. Return
// Block=true to suppress entirely, or ReplaceText to rewrite what
// the user sees. The model's original output stays in the transcript.
func (e *Extension) InterceptAssistantMessage(fn AssistantMessageHandler) {
	e.mu.Lock()
	e.interceptAssistant = fn
	e.mu.Unlock()
}

// InterceptUserMessage registers a guard that runs just before a user
// prompt is appended to the transcript and sent to the model. Return
// Block=true to reject it (the host surfaces Reason; no turn runs), or
// ReplaceText to rewrite what the model sees. Runs on both the initial
// prompt and queued follow-ups. Use for input guardrails, secret
// redaction, or prompt augmentation.
func (e *Extension) InterceptUserMessage(fn UserMessageHandler) {
	e.mu.Lock()
	e.interceptUser = fn
	e.mu.Unlock()
}

// ContributeContext declares static guidance the host folds into the
// cached system-prompt addendum — standing policy the model should see
// every turn that never changes (e.g. "keep exactly one task active;
// finish and verify before declaring done"). The host wraps, bounds,
// and attributes it. Call BEFORE Run; it is flushed during the register
// handshake. Pair it with SetContextCard for the dynamic state. Requires
// host protocol 2+; gate with RequireProtocol(2).
func (e *Extension) ContributeContext(text string) {
	e.mu.Lock()
	e.contextContribution = text
	e.mu.Unlock()
}

// RefreshContext replaces this extension's static system-prompt block at
// any time after the register handshake — register_context you can send
// mid-session. Unlike ContributeContext (set once, flushed in Run), call
// this whenever the block should change: typically from an OnSession
// handler to swap in the active project's standing context (a memory
// store's notes, say). The host rebuilds the cached system prompt, and
// the block stays a *snapshot* — it changes only when you call this, not
// per turn, so the prompt cache survives mid-session writes. An empty
// string clears the block. Requires host protocol 3; gate with
// RequireProtocol(3).
func (e *Extension) RefreshContext(text string) {
	e.mu.Lock()
	e.contextContribution = text
	e.mu.Unlock()
	_ = e.send(extproto.RefreshContextFromExt{Type: "refresh_context", Text: text})
}

// --- ext→host requests (protocol 3) ----------------------------------
//
// host_tool_call, list_sessions, and read_session are the only calls the
// extension *initiates* that expect a reply. nextID mints a correlation
// id; request registers a reply slot, sends the frame, and blocks until
// the matching reply lands (routed by the Run loop), the timeout fires,
// or the wire is sent but never answered. Tool/command handlers already
// run on their own goroutines, so blocking here blocks that handler, not
// the read loop.

const defaultRequestTimeout = 30 * time.Second

func (e *Extension) nextID() string { return "req-" + strconv.FormatUint(e.seq.Add(1), 10) }

// request sends frame (whose id field must equal id) and waits for the
// reply line the Run loop delivers for that id.
func (e *Extension) request(id string, frame any, timeout time.Duration) (json.RawMessage, error) {
	ch := make(chan json.RawMessage, 1)
	e.pendingMu.Lock()
	e.pending[id] = ch
	e.pendingMu.Unlock()
	cancel := func() {
		e.pendingMu.Lock()
		delete(e.pending, id)
		e.pendingMu.Unlock()
	}
	if err := e.send(frame); err != nil {
		cancel()
		return nil, err
	}
	select {
	case line := <-ch:
		return line, nil
	case <-time.After(timeout):
		cancel()
		return nil, fmt.Errorf("timed out waiting for host reply")
	}
}

// deliverReply routes a reply frame to the waiting request by id. Called
// from the Run loop for host_tool_result / session_list / session_data.
func (e *Extension) deliverReply(id string, line json.RawMessage) {
	e.pendingMu.Lock()
	ch := e.pending[id]
	delete(e.pending, id)
	e.pendingMu.Unlock()
	if ch != nil {
		ch <- line // buffered (cap 1); the sole waiter is registered
	}
}

// HostCallOption tunes a HostToolCall.
type HostCallOption func(*hostCallConfig)

type hostCallConfig struct {
	silent  bool
	timeout time.Duration
}

// Silent controls whether the host surfaces the host tool call in the
// UI/transcript. The default is true (the call is hidden — the point of
// running tools from a script). Pass Silent(false) to make it visible.
func Silent(on bool) HostCallOption { return func(c *hostCallConfig) { c.silent = on } }

// WithCallTimeout overrides the default 30s wait for a host tool call (a
// host tool can block on an approval prompt or a long command).
func WithCallTimeout(d time.Duration) HostCallOption {
	return func(c *hostCallConfig) { c.timeout = d }
}

// HostToolCall runs one of the HOST's own tools (read, grep, glob, bash,
// an MCP tool, …) and returns its result. The host runs it under the
// SAME permission gate a model call uses — the extension gains reach, not
// authority — and refuses extension-owned tools, so a call cannot recurse
// back into an extension. This is how a code-execution extension runs
// host tools from a sandboxed script with the intermediate calls kept out
// of the model's context (Silent, the default). Requires host protocol 3;
// gate with RequireProtocol(3).
func (e *Extension) HostToolCall(name string, args json.RawMessage, opts ...HostCallOption) (ToolResult, error) {
	cfg := hostCallConfig{silent: true, timeout: defaultRequestTimeout}
	for _, o := range opts {
		o(&cfg)
	}
	id := e.nextID()
	line, err := e.request(id, extproto.HostToolCallFromExt{
		Type:   "host_tool_call",
		ID:     id,
		Name:   name,
		Args:   args,
		Silent: cfg.silent,
	}, cfg.timeout)
	if err != nil {
		return ToolResult{}, err
	}
	var res extproto.HostToolResultFromHost
	if err := json.Unmarshal(line, &res); err != nil {
		return ToolResult{}, err
	}
	return toolResultFromBlocks(res.Content, res.IsError), nil
}

// SessionInfo describes one past session (ListSessions). ModTime is unix
// seconds.
type SessionInfo struct {
	ID       string
	Title    string
	Preview  string
	Messages int
	ModTime  int64
}

// SessionMessage is one transcript entry flattened to role + text.
type SessionMessage struct {
	Role string
	Text string
}

// ListSessions returns the active project's past sessions (newest-first
// as the host stores them) so an extension can index them — e.g. a
// session-search store. Read-only and project-scoped. Requires host
// protocol 3; gate with RequireProtocol(3).
func (e *Extension) ListSessions() ([]SessionInfo, error) {
	id := e.nextID()
	line, err := e.request(id, extproto.ListSessionsFromExt{
		Type:      "list_sessions",
		ID:        id,
		ProjectID: e.Host().ProjectID,
	}, defaultRequestTimeout)
	if err != nil {
		return nil, err
	}
	var resp extproto.SessionListFromHost
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	out := make([]SessionInfo, len(resp.Sessions))
	for i, s := range resp.Sessions {
		out[i] = SessionInfo{ID: s.SessionID, Title: s.Title, Preview: s.Preview, Messages: s.Messages, ModTime: s.ModTime}
	}
	return out, nil
}

// ReadSession returns one session's transcript, flattened to role+text
// (the shape a text index wants, not the full tool-call structure).
// Returns an error when the id is unknown or outside the active project.
// Requires host protocol 3; gate with RequireProtocol(3).
func (e *Extension) ReadSession(sessionID string) ([]SessionMessage, error) {
	id := e.nextID()
	line, err := e.request(id, extproto.ReadSessionFromExt{
		Type:      "read_session",
		ID:        id,
		SessionID: sessionID,
	}, defaultRequestTimeout)
	if err != nil {
		return nil, err
	}
	var resp extproto.SessionDataFromHost
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	if resp.NotFound {
		return nil, fmt.Errorf("session %q not found (unknown id or outside the active project)", sessionID)
	}
	out := make([]SessionMessage, len(resp.Messages))
	for i, m := range resp.Messages {
		out[i] = SessionMessage{Role: m.Role, Text: m.Text}
	}
	return out, nil
}

// toolResultFromBlocks converts wire content blocks into the SDK's
// ToolResult (the inverse of respondTool's mapping).
func toolResultFromBlocks(blocks []extproto.ContentBlock, isError bool) ToolResult {
	content := make([]ToolContent, 0, len(blocks))
	for _, b := range blocks {
		content = append(content, ToolContent{Type: b.Type, Text: b.Text, MimeType: b.MimeType, Data: b.Data})
	}
	return ToolResult{Content: content, IsError: isError}
}

// Card is a dynamic context card. ID identifies it (re-pushing the same
// ID replaces it). Label is an optional short header; Priority orders
// multiple cards (lower first); Blocking marks open work so the host
// appends a "review before declaring done" recommendation to the card's
// injection.
type Card struct {
	ID       string
	Label    string
	Text     string
	Priority int
	Blocking bool
}

// PushContextCard sets (or replaces, by ID) a dynamic context card the
// host injects into the model's context each turn — at the cache-free
// tail, host-wrapped and attributed to this extension. Use it for live
// state and call it whenever that state changes (the host owns cadence
// and placement). Safe to call from any goroutine, including event
// handlers.
func (e *Extension) PushContextCard(c Card) {
	_ = e.send(extproto.ContextCardFromExt{
		Type: "context_card", ID: c.ID, Label: c.Label, Text: c.Text,
		Priority: c.Priority, Blocking: c.Blocking,
	})
}

// SetContextCard is sugar for a plain (non-blocking, default-priority)
// card. For priority or the blocking nudge, use PushContextCard.
func (e *Extension) SetContextCard(id, label, text string) {
	e.PushContextCard(Card{ID: id, Label: label, Text: text})
}

// ClearContextCard removes a card previously set with SetContextCard.
func (e *Extension) ClearContextCard(id string) {
	_ = e.send(extproto.ContextCardClearFromExt{Type: "context_card_clear", ID: id})
}

// SetStatus sets (or replaces, by id) a short status-bar segment the
// host renders in the TUI status line. Not model-facing. An empty text
// clears the segment.
func (e *Extension) SetStatus(id, text string) {
	_ = e.send(extproto.StatusSegmentFromExt{Type: "status_segment", ID: id, Text: text})
}

// Notify pushes an info-level status note into terva's chat without
// requiring a slash command from the user.
func (e *Extension) Notify(level, message string) {
	_ = e.send(extproto.NotifyFromExt{
		Type:    "notify",
		Level:   level,
		Message: message,
	})
}

// SubmitSlash submits a slash command to the host's TUI as if the user had
// typed it — typically from a panel-key handler (e.g. Enter on a selected
// row to run "/cd <path>"). text must start with '/'. Safe to call from any
// goroutine. No-op on an empty or non-slash string. The host may ignore it
// in non-interactive modes.
func (e *Extension) SubmitSlash(text string) {
	if text == "" || text[0] != '/' {
		e.Logf("SubmitSlash ignored (must start with '/'): %q", text)
		return
	}
	_ = e.send(extproto.SubmitSlashFromExt{Type: "submit_slash", Text: text})
}

// Run starts the protocol loop. Blocks until stdin closes (terva has
// shut us down). Returns the first fatal error, or nil on clean exit.
func (e *Extension) Run() error {
	// Send hello, then re-announce all commands (covers Command calls
	// made before Run, and is also fine for those made after via
	// Command()'s direct send).
	if err := e.send(extproto.HelloFromExt{
		Type:         "hello",
		Name:         e.name,
		Version:      e.version,
		Capabilities: e.caps,
		MinProtocol:  e.minProtocol,
	}); err != nil {
		return err
	}
	e.mu.Lock()
	descs := append([]descTuple(nil), e.descriptions...)
	toolDefs := append([]toolDef(nil), e.toolDefs...)
	eventNames := append([]string(nil), e.eventNames...)
	interceptTool := e.interceptOn
	interceptTurn := e.interceptTurn != nil
	interceptAsst := e.interceptAssistant != nil
	interceptUser := e.interceptUser != nil
	contextContribution := e.contextContribution
	e.mu.Unlock()
	for _, d := range descs {
		_ = e.send(extproto.RegisterCommandFromExt{
			Type:        "register_command",
			Name:        d.name,
			Description: d.desc,
		})
	}
	for _, td := range toolDefs {
		_ = e.send(extproto.RegisterToolFromExt{
			Type:        "register_tool",
			Name:        td.name,
			Description: td.description,
			Schema:      td.schema,
			ReadOnly:    td.readOnly,
			Authority:   td.authority,
		})
	}
	if contextContribution != "" {
		_ = e.send(extproto.RegisterContextFromExt{
			Type: "register_context",
			Text: contextContribution,
		})
	}
	var intercepts []string
	if interceptTool {
		intercepts = append(intercepts, "tool_call")
	}
	if interceptTurn {
		intercepts = append(intercepts, "turn_start")
	}
	if interceptAsst {
		intercepts = append(intercepts, "assistant_message")
	}
	if interceptUser {
		intercepts = append(intercepts, "user_message")
	}
	if len(eventNames) > 0 || len(intercepts) > 0 {
		_ = e.send(extproto.SubscribeFromExt{
			Type:      "subscribe",
			Events:    eventNames,
			Intercept: intercepts,
		})
	}
	// Sentinel: tells the host that all initial registrations have
	// been flushed and the agent registry can be built. Never block
	// on this; if the host doesn't act on it, registrations still
	// land in time for the typical use case.
	_ = e.send(extproto.ReadyFromExt{Type: "ready"})

	// Read frames with extproto.ReadFrame rather than bufio.Scanner: a
	// single oversized inbound frame (e.g. a huge tool-call argument the
	// host forwards) is logged and skipped instead of killing Run() and
	// taking every tool and panel down with it.
	reader := bufio.NewReaderSize(e.in, 64*1024)
	for {
		line, tooLong, rerr := extproto.ReadFrame(reader)
		if tooLong {
			e.Logf("dropped oversized inbound frame (>%d bytes); skipping", extproto.MaxFrameBytes)
			continue
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil
			}
			return rerr
		}
		var frame extproto.Frame
		if err := json.Unmarshal(line, &frame); err != nil {
			e.Logf("malformed frame from host: %v", err)
			continue
		}
		switch frame.Type {
		case "hello_ack":
			var ack extproto.HelloAckFromHost
			if err := json.Unmarshal(line, &ack); err == nil {
				// Renamed hosts send terva_version (and keep
				// terva_version); either spelling fills the same field.
				hostVersion := ack.TervaVersion
				if hostVersion == "" {
					hostVersion = ack.ZotVersion // rename:keep — frozen wire field
				}
				// Under e.mu: Host() (and session_start) read/write e.host
				// from other goroutines, and "ready" is sent before this
				// ack is processed, so a handler can race the write.
				e.mu.Lock()
				e.host = HostInfo{
					ProtocolVersion: ack.ProtocolVersion,
					ZotVersion:      hostVersion, // rename:keep — public SDK API
					Provider:        ack.Provider,
					Model:           ack.Model,
					CWD:             ack.CWD,
					ExtensionDir:    ack.ExtensionDir,
					DataDir:         ack.DataDir,
					SupportedEvents: ack.SupportedEvents,
				}
				e.mu.Unlock()
			}
		case "command_invoked":
			var ci extproto.CommandInvokedFromHost
			if err := json.Unmarshal(line, &ci); err != nil {
				continue
			}
			e.mu.Lock()
			fn := e.commands[ci.Name]
			e.mu.Unlock()
			if fn == nil {
				e.respond(ci.ID, Errorf("no handler for /%s", ci.Name))
				continue
			}
			// Run the handler on its own goroutine so a slow handler
			// doesn't block subsequent commands. Order isn't promised.
			go func(id string, fn CommandHandler, args string) {
				defer func() {
					if r := recover(); r != nil {
						e.respond(id, Errorf("panic: %v", r))
					}
				}()
				resp := fn(args)
				e.respond(id, resp)
			}(ci.ID, fn, ci.Args)
		case "tool_call":
			var tc extproto.ToolCallFromHost
			if err := json.Unmarshal(line, &tc); err != nil {
				continue
			}
			e.mu.Lock()
			fn := e.tools[tc.Name]
			e.mu.Unlock()
			if fn == nil {
				e.respondTool(tc.ID, TextErrorResult(fmt.Sprintf("no handler for tool %q", tc.Name)))
				continue
			}
			go func(id string, fn ToolHandler, args json.RawMessage) {
				defer func() {
					if r := recover(); r != nil {
						e.respondTool(id, TextErrorResult(fmt.Sprintf("panic: %v", r)))
					}
				}()
				res := fn(args)
				e.respondTool(id, res)
			}(tc.ID, fn, tc.Args)
		case "event":
			var ef extproto.EventFromHost
			if err := json.Unmarshal(line, &ef); err != nil {
				continue
			}
			e.mu.Lock()
			// Keep Host() current before any handler runs, so a tool or
			// panel handler reading Host().SessionID sees the active
			// session and OnSession observes the same value.
			if ef.Event == "session_start" {
				e.host.SessionID = ef.SessionID
				e.host.SessionPath = ef.SessionPath
				e.host.SessionTitle = ef.SessionTitle
				// Refresh cwd/project only when the event carries one (a
				// session close / no-session start leaves them empty but
				// doesn't actually move the cwd, so keep the last value).
				if ef.CWD != "" {
					e.host.CWD = ef.CWD
					e.host.ProjectID = ef.ProjectID
				}
			}
			handler := e.eventHandlers[ef.Event]
			var onSession func(Session)
			var onSessionEnd func(Session)
			var onCompaction func(Compaction)
			var onCompactStart func(CompactStart)
			var onUserMessage func(UserMessage)
			var onWorkspaceChanged func(WorkspaceChange)
			var onRunEnd func(RunEnd)
			switch ef.Event {
			case "session_start":
				onSession = e.onSession
			case "session_end":
				onSessionEnd = e.onSessionEnd
			case "transcript_compacted":
				onCompaction = e.onCompaction
			case "compact_start":
				onCompactStart = e.onCompactStart
			case "user_message":
				onUserMessage = e.onUserMessage
			case "workspace_changed":
				onWorkspaceChanged = e.onWorkspaceChanged
			case "run_end":
				onRunEnd = e.onRunEnd
			}
			e.mu.Unlock()
			if onSession != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							e.Logf("OnSession handler panicked: %v", r)
						}
					}()
					onSession(Session{ID: ef.SessionID, Path: ef.SessionPath, Title: ef.SessionTitle, CWD: ef.CWD, ProjectID: ef.ProjectID})
				}()
			}
			if onSessionEnd != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							e.Logf("OnSessionEnd handler panicked: %v", r)
						}
					}()
					onSessionEnd(Session{ID: ef.SessionID, Path: ef.SessionPath, Title: ef.SessionTitle, CWD: ef.CWD, ProjectID: ef.ProjectID})
				}()
			}
			if onCompaction != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							e.Logf("OnCompaction handler panicked: %v", r)
						}
					}()
					onCompaction(Compaction{})
				}()
			}
			if onCompactStart != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							e.Logf("OnCompactStart handler panicked: %v", r)
						}
					}()
					onCompactStart(CompactStart{Reason: ef.Text})
				}()
			}
			if onRunEnd != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							e.Logf("OnRunEnd handler panicked: %v", r)
						}
					}()
					onRunEnd(RunEnd{})
				}()
			}
			if onUserMessage != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							e.Logf("OnUserMessage handler panicked: %v", r)
						}
					}()
					onUserMessage(UserMessage{Text: ef.Text})
				}()
			}
			if onWorkspaceChanged != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							e.Logf("OnWorkspaceChanged handler panicked: %v", r)
						}
					}()
					files := make([]FileChange, len(ef.Files))
					for i, fc := range ef.Files {
						files[i] = FileChange{Path: fc.Path, Change: fc.Change}
					}
					onWorkspaceChanged(WorkspaceChange{Files: files})
				}()
			}
			if handler != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							e.Logf("event %s handler panicked: %v", ef.Event, r)
						}
					}()
					evFiles := make([]FileChange, len(ef.Files))
					for i, fc := range ef.Files {
						evFiles[i] = FileChange{Path: fc.Path, Change: fc.Change}
					}
					handler(Event{
						Name: ef.Event, Step: ef.Step, Stop: ef.Stop,
						Error: ef.Error, ToolID: ef.ToolID, ToolName: ef.ToolName,
						ToolArgs: ef.ToolArgs, Text: ef.Text,
						SessionID: ef.SessionID, SessionPath: ef.SessionPath, SessionTitle: ef.SessionTitle,
						CWD: ef.CWD, ProjectID: ef.ProjectID,
						Files: evFiles,
					})
				}()
			}
		case "event_intercept":
			var ei extproto.EventInterceptFromHost
			if err := json.Unmarshal(line, &ei); err != nil {
				continue
			}
			go e.dispatchIntercept(ei)
		case "panel_key":
			var pk extproto.PanelKeyFromHost
			if err := json.Unmarshal(line, &pk); err != nil {
				continue
			}
			e.mu.Lock()
			h := e.panelKeys[pk.PanelID]
			e.mu.Unlock()
			if h != nil {
				go h(pk.Key, pk.Text)
			}
		case "panel_close":
			var pc extproto.PanelCloseFromHost
			if err := json.Unmarshal(line, &pc); err != nil {
				continue
			}
			e.mu.Lock()
			h := e.panelCloses[pc.PanelID]
			e.mu.Unlock()
			if h != nil {
				go h()
			}
		case "host_tool_result", "session_list", "session_data":
			// Replies to an ext-initiated request (protocol 3). Route to
			// the waiting HostToolCall / ListSessions / ReadSession by id.
			// ReadFrame returns a fresh copy, so the line is safe to hand off.
			var f extproto.Frame
			if err := json.Unmarshal(line, &f); err == nil && f.ID != "" {
				e.deliverReply(f.ID, append(json.RawMessage(nil), line...))
			}
		case "shutdown":
			_ = e.send(extproto.ShutdownAckFromExt{Type: "shutdown_ack"})
			return nil
		default:
			e.Logf("unknown frame type %q", frame.Type)
		}
	}
}

// respond serialises a CommandResponseFromExt for the given id.
func (e *Extension) respond(id string, r Response) {
	if r.Action == "" {
		r.Action = "noop"
	}
	var panel *extproto.PanelSpec
	if r.OpenPanel != nil {
		panel = &extproto.PanelSpec{ID: r.OpenPanel.ID, Title: r.OpenPanel.Title, Lines: r.OpenPanel.Lines, Footer: r.OpenPanel.Footer}
	}
	_ = e.send(extproto.CommandResponseFromExt{
		Type:      "command_response",
		ID:        id,
		Action:    r.Action,
		Prompt:    r.Prompt,
		Insert:    r.Insert,
		Display:   r.Display,
		OpenPanel: panel,
		Error:     r.Error,
	})
}

// respondTool serialises a ToolResultFromExt for the given id.
func (e *Extension) respondTool(id string, r ToolResult) {
	blocks := make([]extproto.ContentBlock, 0, len(r.Content))
	for _, c := range r.Content {
		blocks = append(blocks, extproto.ContentBlock{
			Type:     c.Type,
			Text:     c.Text,
			MimeType: c.MimeType,
			Data:     c.Data,
		})
	}
	_ = e.send(extproto.ToolResultFromExt{
		Type:    "tool_result",
		ID:      id,
		Content: blocks,
		IsError: r.IsError,
	})
}

// send marshals v + LF and writes it under a mutex (so concurrent
// goroutines don't interleave bytes on stdout).
func (e *Extension) send(v any) error {
	b, err := extproto.Encode(v)
	if err != nil {
		return err
	}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	_, err = e.out.Write(b)
	return err
}

// dispatchIntercept runs the per-event handler (tool_call / turn_start
// / user_message / assistant_message) on its own goroutine, catches
// panics, and always emits exactly one event_intercept_response. Called
// from the Run loop.
func (e *Extension) dispatchIntercept(ei extproto.EventInterceptFromHost) {
	defer func() {
		if r := recover(); r != nil {
			_ = e.send(extproto.EventInterceptResponseFromExt{
				Type:   "event_intercept_response",
				ID:     ei.ID,
				Block:  true,
				Reason: fmt.Sprintf("intercept panic: %v", r),
			})
		}
	}()

	resp := extproto.EventInterceptResponseFromExt{
		Type: "event_intercept_response",
		ID:   ei.ID,
	}

	switch ei.Event {
	case "tool_call":
		e.mu.Lock()
		rich := e.interceptToolRich
		plain := e.interceptTool
		e.mu.Unlock()
		if rich != nil {
			d := rich(ei.ToolName, ei.ToolArgs)
			resp.Block = d.Block
			resp.Reason = d.Reason
			if !d.Block && len(d.ModifiedArgs) > 0 && json.Valid(d.ModifiedArgs) {
				resp.ModifiedArgs = d.ModifiedArgs
			}
		} else if plain != nil {
			allow, reason := plain(ei.ToolName, ei.ToolArgs)
			resp.Block = !allow
			resp.Reason = reason
		}
	case "turn_start":
		e.mu.Lock()
		fn := e.interceptTurn
		e.mu.Unlock()
		if fn != nil {
			d := fn(ei.Step)
			resp.Block = d.Block
			resp.Reason = d.Reason
		}
	case "assistant_message":
		e.mu.Lock()
		fn := e.interceptAssistant
		e.mu.Unlock()
		if fn != nil {
			d := fn(ei.Text)
			resp.Block = d.Block
			resp.Reason = d.Reason
			if !d.Block && d.ReplaceText != "" && d.ReplaceText != ei.Text {
				resp.ReplaceText = d.ReplaceText
			}
		}
	case "user_message":
		e.mu.Lock()
		fn := e.interceptUser
		e.mu.Unlock()
		if fn != nil {
			d := fn(ei.Text)
			resp.Block = d.Block
			resp.Reason = d.Reason
			if !d.Block && d.ReplaceText != "" && d.ReplaceText != ei.Text {
				resp.ReplaceText = d.ReplaceText
			}
		}
	}

	_ = e.send(resp)
}
