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
	"slices"
	"sync"

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
//	assistant_message: Text
type Event struct {
	Name string

	Step  int
	Stop  string
	Error string

	ToolID   string
	ToolName string
	ToolArgs json.RawMessage

	Text string

	// SessionID / SessionPath / SessionTitle identify the active
	// session on a session_start event (host protocol 2+). An empty
	// SessionID means no active session (e.g. --no-session, or the
	// session was closed). Use OnSession for the common re-keying case.
	SessionID    string
	SessionPath  string
	SessionTitle string
}

// Session identifies the active terva session. It is delivered to an
// OnSession handler whenever the active session changes (open, resume,
// fork, /new, or close). A zero ID means no active session.
type Session struct {
	ID    string
	Path  string
	Title string
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

	mu            sync.Mutex
	commands      map[string]CommandHandler
	descriptions  []descTuple // ordered so register frames arrive in registration order
	tools         map[string]ToolHandler
	toolDefs      []toolDef // ordered so register frames arrive in registration order
	eventHandlers map[string]EventHandler
	eventNames    []string // declared subscription order
	onSession     func(Session)

	interceptTool      InterceptHandler
	interceptToolRich  ToolCallHandler
	interceptOn        bool
	interceptTurn      TurnStartHandler
	interceptAssistant AssistantMessageHandler
	panelKeys          map[string]func(key, text string)
	panelCloses        map[string]func()

	// Caps reported in the hello frame.
	caps []string

	// minProtocol is the lowest host protocol version this extension
	// can run against, reported in the hello frame. 0 = no minimum.
	minProtocol int

	// hostInfo is filled in once HelloAck arrives.
	host HostInfo
}

type descTuple struct {
	name, desc string
}

type toolDef struct {
	name        string
	description string
	schema      json.RawMessage
}

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
	CWD             string
	ExtensionDir    string
	DataDir         string

	SessionID    string
	SessionPath  string
	SessionTitle string
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
		caps:          []string{"commands", "tools", "events", "panels"},
	}
}

// Host returns the HostInfo received during the hello handshake.
// Returns the zero value if Run hasn't started yet.
func (e *Extension) Host() HostInfo { return e.host }

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
func (e *Extension) Tool(name, description string, schema json.RawMessage, fn ToolHandler) {
	e.mu.Lock()
	e.tools[name] = fn
	e.toolDefs = append(e.toolDefs, toolDef{name: name, description: description, schema: schema})
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

// ensureSubscribed adds name to the event subscription list if it is
// not already present, so On and OnSession can both request
// session_start without subscribing twice. Caller holds e.mu.
func (e *Extension) ensureSubscribed(name string) {
	if !slices.Contains(e.eventNames, name) {
		e.eventNames = append(e.eventNames, name)
	}
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

// Notify pushes an info-level status note into terva's chat without
// requiring a slash command from the user.
func (e *Extension) Notify(level, message string) {
	_ = e.send(extproto.NotifyFromExt{
		Type:    "notify",
		Level:   level,
		Message: message,
	})
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

	scanner := bufio.NewScanner(e.in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
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
				e.host = HostInfo{
					ProtocolVersion: ack.ProtocolVersion,
					ZotVersion:      hostVersion, // rename:keep — public SDK API
					Provider:        ack.Provider,
					Model:           ack.Model,
					CWD:             ack.CWD,
					ExtensionDir:    ack.ExtensionDir,
					DataDir:         ack.DataDir,
				}
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
			}
			handler := e.eventHandlers[ef.Event]
			var onSession func(Session)
			if ef.Event == "session_start" {
				onSession = e.onSession
			}
			e.mu.Unlock()
			if onSession != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							e.Logf("OnSession handler panicked: %v", r)
						}
					}()
					onSession(Session{ID: ef.SessionID, Path: ef.SessionPath, Title: ef.SessionTitle})
				}()
			}
			if handler != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							e.Logf("event %s handler panicked: %v", ef.Event, r)
						}
					}()
					handler(Event{
						Name: ef.Event, Step: ef.Step, Stop: ef.Stop,
						Error: ef.Error, ToolID: ef.ToolID, ToolName: ef.ToolName,
						ToolArgs: ef.ToolArgs, Text: ef.Text,
						SessionID: ef.SessionID, SessionPath: ef.SessionPath, SessionTitle: ef.SessionTitle,
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
		case "shutdown":
			_ = e.send(extproto.ShutdownAckFromExt{Type: "shutdown_ack"})
			return nil
		default:
			e.Logf("unknown frame type %q", frame.Type)
		}
	}
	return scanner.Err()
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
// / assistant_message) on its own goroutine, catches panics, and
// always emits exactly one event_intercept_response. Called from the
// Run loop.
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
	}

	_ = e.send(resp)
}
