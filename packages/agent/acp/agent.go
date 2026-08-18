//go:build terva_acp

package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// AgentFactory builds a fresh core.Agent for a new ACP session. The agent
// host (runACPMode, in the parent package) implements this by resolving the
// CLI args + wiring the per-turn hook ladder (confirm gate, extensions, MCP)
// exactly as runRPCMode does. Tests implement it with a fake provider.Client.
// Returning an error fails session/new.
//
// Both factory methods also OWN durable persistence (§3): they create or
// reopen the on-disk core.Session and wire OnMessageAppended / OnUsage /
// OnTranscriptCompacted so the real terva session IS the transcript. The
// returned *core.Session's Path is the durable identity the acp package uses
// as the ACP sessionId, so a later session/load can reopen exactly this
// transcript. The acp package never touches TervaHome / the session root
// itself — that lives with the host, behind this interface.
type AgentFactory interface {
	// NewSessionAgent builds the agent + a fresh durable session rooted at
	// cwd, with persistence hooks already wired. mcpServers carries the raw
	// session/new mcpServers array (Phase 4a): the factory parses the stdio
	// entries, starts the MCP servers, and merges their tools into the
	// agent's registry BEFORE building the agent, so they ride the same
	// confirm-gate ladder and plan-mode filtering as every other tool.
	//
	// It returns a SessionAgent bundling the agent, the durable session, the
	// MCP cleanup func (never nil), the session's ConfirmGate (nil for a
	// pure-yolo session with no gate — §4b mode switching is a no-op then),
	// and the session's resolved provider/model (the model-selector seed).
	//
	// confirmer is the ACP confirmer the acp package owns; the factory
	// wires it as the inner Confirmer of the session's ConfirmGate (via
	// buildBeforeToolExecute) so a policy "ask" drives
	// session/request_permission (§8). The gate hands each call's id to
	// ConfirmWithCall directly, so the permission request correlates to the
	// right toolCallId with no factory-side hook (§13). The confirmer is
	// bound to the session by the acp package after construction.
	NewSessionAgent(ctx context.Context, cwd string, mcpServers json.RawMessage, confirmer core.Confirmer) (SessionAgent, error)

	// LoadSessionAgent reopens the durable session at sessionPath (the ACP
	// sessionId), builds an agent for it with persistence hooks wired, and
	// returns the same SessionAgent bundle plus the restored transcript
	// (already repaired by OpenSession). The acp package rehydrates model
	// context via SetMessages and replays the history to the editor BEFORE
	// returning the load response (§13). mcpServers/confirmer are wired as
	// in NewSessionAgent.
	LoadSessionAgent(ctx context.Context, sessionPath, cwd string, mcpServers json.RawMessage, confirmer core.Confirmer) (SessionAgent, []provider.Message, error)

	// ListSessions returns the durable sessions known to the host, newest
	// first, optionally filtered to cwd (empty cwd means all working
	// directories the host can enumerate). Backs session/list (§10).
	ListSessions(cwd string) []SessionInfo

	// ModelOptions returns the models terva can switch to under ACP: the
	// catalog filtered to AUTHENTICATED providers only (the user is logged
	// into them), resolving plan §14's "model menu scope" — the editor must
	// not offer a model whose provider has no credential. The acp package
	// renders these as the `model` config option's selectable values.
	ModelOptions() []ModelOption

	// SwitchModel resolves a model switch for a live session: given the
	// session's current provider+model and the target model id (the config
	// option value the editor sent), it applies the same-provider /
	// cross-endpoint rules (mirroring interactive_model.go and rpc.go's
	// cross-endpoint rejection) and returns the ModelSwitch the acp package
	// applies to the agent (SetModel in place when the client is reusable,
	// SetClientAndModel otherwise). An unknown model or an unauthenticated
	// provider is an error (mapped to -32602 invalid_params); a same-provider
	// model that routes to a different backend is also an error (the acp
	// session has no rebuild-without-respawn path, same as rpc).
	SwitchModel(currentProvider, currentModel, targetModelID string) (ModelSwitch, error)
}

// SessionAgent bundles everything the acp package needs from a freshly built
// (or reopened) terva agent: the agent itself, the durable on-disk session
// (its Path is the ACP sessionId), the MCP cleanup func, the ConfirmGate (so
// session/set_mode can switch approval modes at runtime), and the resolved
// provider/model (the model selector's current value).
type SessionAgent struct {
	Agent    *core.Agent
	Session  *core.Session
	Cleanup  func()            // stops per-session MCP + extension subprocesses; never nil
	Gate     *core.ConfirmGate // nil when the session has no gate (pure yolo)
	Provider string
	Model    string

	// ObserveEvent, when non-nil, is the extension-side event sink the host
	// (acp_mode.go) builds: it fans every core.AgentEvent out to the session's
	// extensions and feeds the two tool events into the hook engine's
	// post-tool-use correlator. It MUST NOT be assigned directly to
	// ag.OnEvent — the acp package owns that field for its session/update
	// translator — so bindSession COMPOSES it AFTER translateEvent (translate
	// first, then observe), keeping the session/update stream intact while
	// still giving extensions their event-intercept surface (the §OnEvent
	// composition requirement). nil leaves the agent with translation only,
	// exactly as before extensions were wired.
	ObserveEvent func(core.AgentEvent)

	// RecordModelSwap tells the host that this session's model has moved, so
	// whatever the host RE-RESOLVES from — its tool-set rebuild — tracks the
	// swap instead of restoring the model the session was built on.
	//
	// It exists because a swap has two halves in different scopes.
	// ModelSwitch.Apply moves the running agent and is built per SWITCH, by a
	// factory that has no session; this is built per SESSION, in the host's
	// composition root, where the rebuild args live. This package is the only
	// thing that holds both, so it calls them together — the two must not
	// separate, which is the whole reason the shared swap event exists.
	//
	// Without it a switch held only until the next rebuild (an extension
	// reload, a /trust flip): terva_status went back to naming the provider the
	// session had switched away from, and read's vision support was re-derived
	// from the launch model. rebuiltClient says the endpoint moved, so the
	// host's launch-time key/URL pins no longer describe this session.
	//
	// nil is allowed and means the host re-resolves nothing.
	RecordModelSwap func(provider, model string, rebuiltClient bool)

	// Sandbox is the session's filesystem/shell confinement, shared by
	// pointer across every tool in the agent's registry (the same value the
	// TUI's /jail and /unjail toggle). Carried so the native /jail and
	// /unjail commands can Lock/Unlock it headlessly. nil when the build has
	// no sandbox.
	Sandbox *tools.Sandbox

	// Skills is a snapshot func that re-discovers the visible (non-builtin)
	// SKILL.md skills for this session's working directory, mirroring the
	// TUI's SkillSnapshot. Carried so the native /skills command can list
	// them headlessly. nil when skills are disabled (--no-skill) or the host
	// can't enumerate them.
	Skills func() []*skills.Skill

	// ExtCommands snapshots the extension-registered slash commands for this
	// session, so the ACP run mode can advertise them in
	// available_commands_update alongside the built-in curated set. The host
	// (acp_mode.go) builds it from extensions.Manager.Commands(); the acp
	// package can't import extensions/extproto, so the host maps those into
	// the neutral ExtCommandInfo type here. nil when the session has no
	// extension manager (extensions disabled), leaving the catalog built-in
	// only.
	ExtCommands func() []ExtCommandInfo

	// InvokeExtCommand fires the named extension command (the bare command
	// name, no leading slash) with the trailing argument text and returns the
	// extension's response mapped into the neutral ExtCommandResult. The host
	// drives extensions.Manager.Invoke with a sane timeout and translates
	// extproto.CommandResponseFromExt -> ExtCommandResult. nil when the
	// session has no extension manager. handleSlashCommand only calls this for
	// a head that ExtCommands advertised, so a nil InvokeExtCommand with a
	// non-nil ExtCommands would be a host bug.
	InvokeExtCommand func(ctx context.Context, name, args string) (ExtCommandResult, error)

	// ExtContext snapshots what the session's extensions are contributing to
	// the model — static system guidance and live context cards — so the
	// native /context command can show it without the acp package importing
	// extensions. The host builds it from extensions.Manager.ContextSnapshot(),
	// mapping each extensions.ContextItem into the neutral ContextItem here. nil
	// when the session has no extension manager (extensions disabled); an
	// empty slice means a manager is wired but nothing is being injected.
	ExtContext func() []ContextItem

	// ReloadExtensions tears down and respawns the session's extensions (the
	// manager's Reload), returning the reload stats in the acp package's own
	// terms, so the native /reload-ext command can report them without the acp
	// package importing extensions. The host drives extensions.Manager.Reload
	// with a sane grace and maps the extensions.ReloadStats into the neutral
	// ReloadStats here. Because ExtCommands reads the live manager, the
	// command set reflects the reload automatically — /reload-ext re-advertises
	// it afterwards. nil when the session has no extension manager.
	ReloadExtensions func(ctx context.Context) ReloadStats

	// TrustWorkspace persists the session cwd's Workspace Trust verdict and makes
	// its project content go live for this session, so the native /trust command
	// gives an editor user the only in-editor way to trust a workspace over ACP.
	// The host (acp_mode.go) records the cwd via TrustPath (parent marks "trust
	// descendants too"), then flips the extensions.Manager to trusted and reloads
	// it so project extensions are discovered now — without the acp package
	// importing the trust store or the extensions package. Project skills/context
	// are baked into the session's system prompt at build time and can't be
	// re-injected mid-session, so they apply on a NEW session; the confirmation
	// the command emits says so. nil when the host didn't wire it — /trust then
	// degrades to a note.
	TrustWorkspace func(parent bool) error

	// UntrustWorkspace is the symmetric inverse of TrustWorkspace: the host drops
	// the session cwd from the trust store (UntrustPath), flips the
	// extensions.Manager back to untrusted, and reloads it so project extensions
	// are torn down, so the native /untrust command can revoke trust from inside
	// the editor. nil when the host didn't wire it — /untrust degrades to a note.
	UntrustWorkspace func() error
}

// ContextItem is one entry in the inspector view of what the session's
// extensions are injecting into the model, in the acp package's own terms (the
// acp package can't import the extensions package). Kind is "static" (a
// system-guidance block from register_context) or "card" (a live context card);
// Source is the contributing extension's name; Label is the card's short header
// (empty for static); Text is the contributed content. The host populates these
// from extensions.Manager.ContextSnapshot().
type ContextItem struct {
	Source string
	Kind   string // "static" or "card"
	Label  string
	Text   string
}

// ReloadStats is the host's summary of an extension reload, in the acp
// package's own terms (mirroring extensions.ReloadStats, with the []error
// Errors flattened to a count the native command renders). The host populates
// it from extensions.Manager.Reload().
type ReloadStats struct {
	Stopped int // old extension processes torn down
	Loaded  int // new extension processes that reached spawn
	Ready   int // of those, how many signalled ready in time
	Errors  int // non-fatal per-extension errors during the reload
}

// ExtCommandInfo is one extension-registered slash command, in the acp
// package's own terms (the acp package can't import the extensions package).
// Name is the bare command name as the extension registered it (no leading
// slash); Description is the one-line summary; Hint, when non-empty, is the
// argument hint shown in the editor's command-input field. The host populates
// these from extensions.Manager.Commands().
type ExtCommandInfo struct {
	Name        string
	Description string
	Hint        string
}

// ExtCommandAction is the action an extension command's response asks the host
// to take, in the acp package's own terms (mirroring
// extproto.CommandResponseFromExt.Action). The host maps the extension's
// string action onto one of these; an empty/unknown action degrades to a noop.
type ExtCommandAction string

const (
	// ExtActionPrompt hands the agent a task: the host runs a real model turn
	// with the returned Prompt text via the normal turn path.
	ExtActionPrompt ExtCommandAction = "prompt"
	// ExtActionDisplay shows the returned Display text to the user (no model
	// turn).
	ExtActionDisplay ExtCommandAction = "display"
	// ExtActionInsert (TUI-only) would prepopulate the editor input; ACP has
	// no input surface, so the host degrades it to a display-style chunk so
	// nothing is silently lost. The text rides in Display.
	ExtActionInsert ExtCommandAction = "insert"
	// ExtActionOpenPanel (TUI-only) would open an interactive panel; ACP has
	// no panel surface, so the host degrades it to a display-style chunk. The
	// rendered panel text rides in Display.
	ExtActionOpenPanel ExtCommandAction = "open_panel"
	// ExtActionNoop does nothing (the command had a side effect of its own and
	// nothing to render).
	ExtActionNoop ExtCommandAction = "noop"
	// ExtActionError reports a command-level failure; the host emits Error as a
	// chunk and ends the turn.
	ExtActionError ExtCommandAction = "error"
)

// ExtCommandResult is the host's translation of an extension command's
// response into the acp package's terms. Action selects how the acp run mode
// applies it (see handleExtCommand); Prompt/Display/Error carry the action's
// payload. The host folds insert/open_panel into Display (those TUI-only
// actions have no ACP surface) and surfaces a transport error / the response's
// own Error field via Action=error.
type ExtCommandResult struct {
	Action  ExtCommandAction
	Prompt  string
	Display string
	Error   string
}

// ModelOption is one selectable model in the ACP `model` config option. ID is
// the wire value (the model id the editor echoes back on
// session/set_config_option); DisplayName is the human label; Provider is the
// owning provider id (for grouping / the cross-provider switch).
type ModelOption struct {
	ID          string
	Provider    string
	DisplayName string
}

// ModelSwitch is the host's resolution of a model change. When Reuse is true
// the current provider client can serve the new model (same provider, same
// endpoint), so the acp package calls SetModel(Model). Otherwise Client is the
// freshly built provider client for the target model and the acp package calls
// SetClientAndModel(Client, Model), preserving the transcript — and, since that
// keeps the tool registry, re-binds terva_status to AuthMethod/BaseURL so the
// status report doesn't keep naming the previous provider.
type ModelSwitch struct {
	Provider   string
	Model      string
	Client     provider.Client // non-nil only when Reuse is false
	Reuse      bool
	AuthMethod string // "apikey" | "oauth" | "" — the target's, for Reuse==false
	BaseURL    string // target endpoint, for Reuse==false

	// Apply moves ag onto this model. The HOST supplies it, closing over
	// build.ApplyModelSwap — the one event the daemon, bot mode and the resume
	// path also go through, so the four cannot drift on what a swap consists
	// of. This package stays out of the composition root, exactly as it does
	// for the agent, the extension manager and the trust applier it is handed.
	//
	// REQUIRED. A ModelSwitch that describes a target and cannot move anything
	// onto it is not a switch, and handleSetConfigOption reports a nil one as an
	// internal error rather than answering the editor with a success the session
	// did not have. A silently-unapplied swap is the precise shape of bug this
	// event exists to end.
	Apply func(ag *core.Agent)
}

// AgentInfo is the {name, version} terva reports in initialize.
type AgentInfo struct {
	Name    string
	Version string
}

// agentServer holds the ACP method handlers and the live session map. One
// per connection. It mirrors rpcServer: a single connection serving requests
// over the hand-rolled wire, here multiplexed across several sessions (§3).
type agentServer struct {
	conn    *conn
	factory AgentFactory
	info    AgentInfo

	// embeddedContext reports whether resource content blocks are folded
	// into prompts. Off this pass (advertised off — §5/§14).
	embeddedContext bool

	mu          sync.Mutex
	initialized bool
	// sessions is keyed by the ACP sessionId, which is the durable terva
	// session file path (§3). A session created via session/new and one
	// reopened via session/load with the same id share the one live entry,
	// so a load of an already-live session rebinds rather than duplicating.
	sessions map[string]*session
}

// Serve runs the ACP connection to completion: it reads JSON-RPC frames off
// r, dispatches them, and writes responses / session/update notifications to
// w. Returns when r closes. This is the entrypoint runACPMode calls with
// os.Stdin/os.Stdout, and the wire-harness test calls with an io.Pipe pair.
func Serve(ctx context.Context, r io.Reader, w io.Writer, factory AgentFactory, info AgentInfo) error {
	srv := &agentServer{
		factory:  factory,
		info:     info,
		sessions: make(map[string]*session),
	}
	srv.conn = newConn(r, w, srv.dispatch)
	// On disconnect, flush + close every durable session so the last turn is
	// persisted and a freshly-created-but-never-prompted session drops its
	// empty stub file (core.Session.Close handles that). Sessions append
	// per-message during turns, so this is a defensive final flush, not the
	// primary persistence path.
	defer srv.closeSessions()
	return srv.conn.run(ctx)
}

// closeSessions flushes and closes every live durable session and stops each
// session's subprocesses (MCP servers and extensions) via its cleanup func.
// Called once on disconnect. Safe to call with no sessions.
func (s *agentServer) closeSessions() {
	s.mu.Lock()
	live := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		live = append(live, sess)
	}
	s.sessions = make(map[string]*session)
	s.mu.Unlock()
	for _, sess := range live {
		if sess.cleanup != nil {
			sess.cleanup()
		}
		_ = sess.durable.Close()
	}
}

// dispatch is the wire handlerFunc: it runs the method handler, then returns
// any post-response action. session/new advertises its slash-command catalog
// via available_commands_update, which MUST follow the response — the editor
// drops a session/update for a sessionId it learns only from that very
// session/new response. session/load is exempt (the client already knows the
// id), so it advertises inline.
func (s *agentServer) dispatch(ctx context.Context, method string, params json.RawMessage) (any, func(), error) {
	result, err := s.handle(ctx, method, params)
	if err != nil {
		return nil, nil, err
	}
	if method == MethodSessionNew {
		if nr, ok := result.(NewSessionResult); ok {
			id := nr.SessionID
			return result, func() {
				s.mu.Lock()
				sess := s.sessions[id]
				s.mu.Unlock()
				if sess != nil {
					s.emitAvailableCommands(sess)
				}
			}, nil
		}
	}
	return result, nil, nil
}

// handle is the JSON-RPC method dispatcher. The initialize-first rule (§13):
// every method except initialize is rejected until initialize has returned.
func (s *agentServer) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method != MethodInitialize {
		s.mu.Lock()
		ok := s.initialized
		s.mu.Unlock()
		if !ok {
			return nil, &rpcError{Code: CodeInvalidRequest, Message: "initialize must be called first"}
		}
	}

	switch method {
	case MethodInitialize:
		return s.handleInitialize(params)
	case MethodAuthenticate:
		return s.handleAuthenticate(params)
	case MethodSessionNew:
		return s.handleSessionNew(ctx, params)
	case MethodSessionPromptName:
		return s.handleSessionPrompt(ctx, params)

	case MethodSessionCancel:
		return s.handleSessionCancel(params)

	case MethodSessionLoad:
		return s.handleSessionLoad(ctx, params)
	case MethodSessionList:
		return s.handleSessionList(params)

	// Phase 4b: model selection rides session/set_config_option and approval
	// mode rides session/set_mode. Both are advertised via the session
	// result's configOptions/modes, so honoring them here keeps capability
	// honesty (§4b).
	case MethodSessionSetConfigOpt:
		return s.handleSetConfigOption(params)
	case MethodSessionSetMode:
		return s.handleSetMode(params)

	default:
		return nil, errMethodNotFound(method)
	}
}

// handleInitialize negotiates the protocol version and advertises ONLY the
// capabilities this pass backs (§9, §13). Version negotiation: echo the
// client's version if we support it, else return the highest we support —
// which is 1 either way this pass.
func (s *agentServer) handleInitialize(params json.RawMessage) (any, error) {
	var p InitializeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, errInvalidParams(err.Error())
		}
	}

	negotiated := ProtocolVersion
	if p.ProtocolVersion > 0 && p.ProtocolVersion <= ProtocolVersion {
		negotiated = p.ProtocolVersion
	}

	s.mu.Lock()
	s.initialized = true
	s.mu.Unlock()

	return InitializeResult{
		ProtocolVersion: negotiated,
		AgentCapabilities: AgentCapabilities{
			// Phase 3: session/load is backed (OpenSession + replay +
			// SetMessages), so we now honestly advertise it (§13).
			LoadSession: true,
			PromptCapabilities: PromptCapabilities{
				Image:           true,
				Audio:           false,
				EmbeddedContext: s.embeddedContext,
			},
			McpCapabilities: McpCapabilities{
				HTTP: false, SSE: false, ACP: false, // terva MCP is stdio-only
			},
			// Phase 3: session/list is backed, so advertise it. The
			// SessionListCapabilities value is the empty object {} (presence
			// = "supported"), which is exactly how the schema gates it.
			SessionCapabilities: map[string]any{"list": map[string]any{}},
		},
		AuthMethods: []AuthMethod{{
			ID:          "terminal-auth",
			Name:        "Terminal login",
			Description: "Run terva interactively and use /login to authenticate, then reconnect.",
		}},
		AgentInfo: &Implementation{Name: s.info.Name, Version: s.info.Version},
	}, nil
}

// handleAuthenticate is a no-op this pass — terminal-auth happens out of
// band (§8). The seam exists so a future mid-turn auth failure can drive the
// client here via a -32000 auth_required error.
func (s *agentServer) handleAuthenticate(_ json.RawMessage) (any, error) {
	return map[string]any{}, nil
}

// handleSessionNew builds a fresh core.Agent for the session, wires the
// translator as the persistent OnEvent sink, and registers it in the session
// map (§3). The agent's per-turn hook ladder was installed by the factory.
func (s *agentServer) handleSessionNew(ctx context.Context, params json.RawMessage) (any, error) {
	var p NewSessionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, errInvalidParams(err.Error())
	}
	if p.CWD == "" {
		return nil, errInvalidParams("cwd is required")
	}

	// The confirmer is created before the agent so the factory can wire it
	// as the inner Confirmer of the session's ConfirmGate (§8). It is bound
	// to the session after construction (the session does not exist yet).
	confirmer := newConfirmer()

	sa, err := s.factory.NewSessionAgent(ctx, p.CWD, p.MCPServers, confirmer)
	if err != nil {
		return nil, errInternal("build agent: " + err.Error())
	}

	// The ACP sessionId IS the durable terva session file path (§3): it
	// round-trips back on session/load to reopen exactly this transcript,
	// and session/list reports the same id. The path is opaque to the editor.
	id := sa.Session.Path

	sess := s.bindSession(id, p.CWD, sa, confirmer)

	// The slash-command catalog (available_commands_update) is advertised by
	// dispatch AFTER this response is written — the editor drops a
	// session/update for the sessionId it learns only from this response.

	// Advertise the model selector (authenticated-provider models) and the
	// approval-mode menu on the session result (§4b). A client that supports
	// them renders the menus immediately; one that doesn't ignores the extra
	// fields (capability honesty — these are optional schema fields).
	return NewSessionResult{
		SessionID:     id,
		ConfigOptions: s.sessionConfigOptions(sess),
		Modes:         sessionModeState(sess),
	}, nil
}

// bindSession registers a live session under id, binds the confirmer, and
// wires the OnEvent translator. The SessionAgent bundle carries the MCP
// cleanup func (never nil), the ConfirmGate (for session/set_mode), and the
// resolved provider/model/mode (the menus' current values). Shared by
// session/new and session/load so the post-construction wiring stays
// identical. Returns the live *session.
func (s *agentServer) bindSession(id, cwd string, sa SessionAgent, confirmer *acpConfirmer) *session {
	ag, durable := sa.Agent, sa.Session
	sess := newSession(id, cwd, ag, durable, s)
	sess.cleanup = sa.Cleanup
	sess.gate = sa.Gate
	sess.sandbox = sa.Sandbox
	sess.skills = sa.Skills
	sess.extCommands = sa.ExtCommands
	sess.invokeExtCommand = sa.InvokeExtCommand
	sess.extContext = sa.ExtContext
	sess.reloadExtensions = sa.ReloadExtensions
	sess.trustWorkspace = sa.TrustWorkspace
	sess.untrustWorkspace = sa.UntrustWorkspace
	sess.recordSwap = sa.RecordModelSwap
	sess.setModel(sa.Provider, sa.Model)
	// Seed the session's mode from the gate (ApprovalYolo when there is no
	// gate), so the mode menu's currentModeId matches what the gate enforces.
	sess.setMode(string(sa.Gate.Mode()))

	s.mu.Lock()
	prev := s.sessions[id]
	s.mu.Unlock()

	// Replacing a live entry under the same id (e.g. a session/load of an
	// already-open session): close the superseded durable session so its file
	// handle isn't leaked, and stop its MCP subprocesses so they don't leak.
	// The new binding owns the id going forward.
	//
	// 🪤 The teardown must not race the OLD binding's in-flight turn, and until
	// this interlock existed it did. handleSessionPrompt resolves its session
	// from the map and holds only that session's turnMu; this took only s.mu.
	// So a session/load arriving mid-turn — dispatched on its own goroutine,
	// see conn.run — killed the MCP and extension subprocesses the running
	// tools were using and closed the durable file its message observer was
	// still appending to. build.WireHeadlessSessionPersist swallows that append
	// error, so every message of the turn vanished from the transcript with
	// nothing logged. session/cancel then resolved to the NEW binding, whose
	// activeCancel is nil, leaving the orphaned turn uncancellable and still
	// emitting session/update for the same id.
	//
	// Retire the old binding first so no turn can START on it, then cancel the
	// one already running and wait for it to flush, and only then tear down.
	if prev != nil && prev != sess {
		prev.markSuperseded()
		if !prev.cancelAndAwaitTurn(rebindGrace) {
			// Proceed anyway: the id must end up bound, and the editor is
			// waiting. Say so — a dropped transcript tail is exactly the
			// silence this interlock exists to end.
			//
			// A plain literal, not i18n.T, matching every other user-facing
			// string in this package. This one CANNOT be translated even if it
			// were marked: terva-i18n-lint parses the tree without terva_acp,
			// so a T() here never reaches the catalogue and would read as
			// coverage the string does not have.
			sess.emit(map[string]any{
				"sessionUpdate": UpdateAgentMessageChunk,
				"content": textContentBlock(fmt.Sprintf(
					"(the previous turn on this session did not stop within %s of being reloaded; "+
						"the tail of its transcript may be missing)", rebindGrace)),
			})
		}
	}

	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()

	if prev != nil && prev != sess {
		if prev.cleanup != nil {
			prev.cleanup()
		}
		if prev.durable != nil && prev.durable != durable {
			_ = prev.durable.Close()
		}
	}

	// Bind the confirmer to its session so session/request_permission knows
	// the sessionId, the live turn ctx (for cancellation), and the current
	// toolCallId (for correlation).
	confirmer.bind(sess)

	// The translator is an event observer, so every event of every turn in this
	// session narrates back as session/update — the persistent fan-out pattern
	// (rpc.go registers the same way). The per-turn sink passed to
	// PromptWithPolicy is composed with these by the core (wrapSink).
	//
	// Registration order is delivery order: translate into a session/update
	// FIRST, so an extension's fan-out can never preempt or corrupt the
	// narration, and a panic in observe (defensively shouldn't happen) still
	// left the editor with the translated event. AddEventObserver ignores a nil
	// observer, so the host having wired none needs no branch — this used to be
	// a hand-rolled compose-or-overwrite, the exact hazard registration removes.
	ag.AddEventObserver(sess.translateEvent)
	ag.AddEventObserver(sa.ObserveEvent)

	return sess
}

// handleSessionLoad reopens a durable terva session (the ACP sessionId is its
// file path), rehydrates model context via SetMessages, and — per the §13
// conformance MUST — replays the full prior transcript as session/update
// notifications (user_message_chunk / agent_message_chunk) BEFORE this load
// response resolves. Cost is seeded from the session file so the editor's
// usage view doesn't reset to zero on resume. Result is `{}` (no required
// fields; modes/configOptions are Phase 4).
//
// Loading a session that is already live (same id) rebinds it to a freshly
// reopened agent/session — the editor reconnecting to a session it already
// holds is tolerated rather than an error.
func (s *agentServer) handleSessionLoad(ctx context.Context, params json.RawMessage) (any, error) {
	var p LoadSessionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, errInvalidParams(err.Error())
	}
	if p.SessionID == "" {
		return nil, errInvalidParams("sessionId is required")
	}
	cwd := p.CWD

	confirmer := newConfirmer()

	sa, msgs, err := s.factory.LoadSessionAgent(ctx, p.SessionID, cwd, p.MCPServers, confirmer)
	if err != nil {
		// A missing / unreadable session file is the resource-not-found case;
		// anything else is an internal failure building the agent.
		if os.IsNotExist(err) {
			return nil, &rpcError{Code: CodeResourceNotFound, Message: "unknown session: " + p.SessionID}
		}
		return nil, errInternal("load session: " + err.Error())
	}
	ag, durable := sa.Agent, sa.Session
	if cwd == "" {
		cwd = durable.Meta.CWD
	}

	// Rehydrate model context: the reopened transcript becomes the agent's
	// in-memory history, so the next prompt continues with full context (not
	// just a repainted UI).
	ag.SetMessages(msgs)

	// Seed cost from the on-disk usage rows so the editor's usage view and
	// our own cumulative meter resume at the right figure rather than zero.
	if cum, _, resume, uerr := core.SessionUsageDetail(p.SessionID); uerr == nil {
		ag.SeedCost(cum)
		ag.SeedLastTurnUsage(resume)
	}

	sess := s.bindSession(p.SessionID, cwd, sa, confirmer)

	// §13 MUST: replay the full history as session/update notifications
	// BEFORE returning the load response. emit() goes straight through the
	// single-writer wire, so every chunk is flushed to the client before this
	// handler returns its result frame (serve() writes the response only
	// after handle() returns). Ordering across chunks is guaranteed by the
	// write mutex.
	s.replayTranscript(sess, msgs)

	// Surface any load warnings as an agent_message_chunk so the editor sees
	// what was skipped/guessed (corrupt rows, newer format). Never silently
	// dropped (§10).
	for _, w := range durable.LoadWarnings {
		sess.emit(map[string]any{
			"sessionUpdate": UpdateAgentMessageChunk,
			"content":       textContentBlock("(session load warning: " + w + ")"),
		})
	}

	// Re-advertise the slash-command catalog on resume (Phase 4c) so the
	// editor's command palette is repopulated for the reopened session, just
	// as session/new populates it for a fresh one.
	s.emitAvailableCommands(sess)

	// Restore the model/mode menus on resume so the editor's selectors reflect
	// the session's persisted provider/model and approval mode (§4b).
	return LoadSessionResult{
		ConfigOptions: s.sessionConfigOptions(sess),
		Modes:         sessionModeState(sess),
	}, nil
}

// replayTranscript emits the restored transcript as session/update chunks so
// the editor repaints the conversation on load (§10/§13). User messages map
// to user_message_chunk, assistant text to agent_message_chunk; tool
// calls/results from history are not re-narrated as live tool_calls (they
// already completed — repainting the textual conversation is what the editor
// needs, and replaying tool_call/permission machinery for past turns would be
// misleading). Image/non-text blocks in user turns are skipped.
func (s *agentServer) replayTranscript(sess *session, msgs []provider.Message) {
	for _, m := range msgs {
		var variant string
		switch m.Role {
		case provider.RoleUser:
			variant = UpdateUserMessageChunk
		case provider.RoleAssistant:
			variant = UpdateAgentMessageChunk
		default:
			// Tool-result messages and any other role carry no
			// conversational text to repaint.
			continue
		}
		text := messageText(m)
		if text == "" {
			continue
		}
		sess.emit(map[string]any{
			"sessionUpdate": variant,
			"content":       textContentBlock(text),
		})
	}
}

// handleSessionList returns the durable sessions the host knows about,
// optionally filtered to cwd (§10). Gated by sessionCapabilities.list, which
// initialize now advertises. nextCursor is omitted — terva returns the full
// set in one page.
func (s *agentServer) handleSessionList(params json.RawMessage) (any, error) {
	var p ListSessionsParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, errInvalidParams(err.Error())
		}
	}
	sessions := s.factory.ListSessions(p.CWD)
	if sessions == nil {
		// The schema requires `sessions` to be present; emit [] not null.
		sessions = []SessionInfo{}
	}
	return ListSessionsResult{Sessions: sessions}, nil
}

// handleSessionPrompt runs a single turn (§4, §6) and resolves with the
// stopReason (§11). It holds the session turn mutex so a second concurrent
// prompt for the same session blocks until this one finishes.
func (s *agentServer) handleSessionPrompt(ctx context.Context, params json.RawMessage) (any, error) {
	var p PromptParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, errInvalidParams(err.Error())
	}

	s.mu.Lock()
	sess := s.sessions[p.SessionID]
	s.mu.Unlock()
	if sess == nil {
		return nil, &rpcError{Code: CodeResourceNotFound, Message: "unknown session: " + p.SessionID}
	}

	sess.turnMu.Lock()
	defer sess.turnMu.Unlock()

	// Re-checked AFTER the lock, not before: the wait to get here can be the
	// whole of a previous turn, and a session/load may have retired this
	// binding meanwhile. Starting now would run against subprocesses that are
	// being killed and write to a durable session that is being closed — the
	// rebind race from the other side. The caller is told rather than served a
	// turn whose output goes nowhere.
	if sess.isSuperseded() {
		return nil, &rpcError{
			Code:    CodeResourceNotFound,
			Message: "session was reloaded while this prompt was queued; send it again: " + p.SessionID,
		}
	}

	turnCtx, cancel := context.WithCancel(ctx)
	sess.setCancel(cancel)
	// Register the turn ctx as the one a session/request_permission
	// round-trip blocks on, so session/cancel unblocks any outstanding
	// permission with a cancelled verdict (§8 cancellation contract).
	sess.beginTurnPermission(turnCtx)
	defer func() {
		sess.setCancel(nil)
		sess.endTurnPermission()
		cancel()
	}()

	text, images := promptToProvider(p.Prompt, s.embeddedContext)

	// Phase 4c: a leading slash command is intercepted and executed natively
	// (the headless-safe subset — /clear, /compact) or, for an
	// extension-registered command, invoked through the host and its response
	// action applied — instead of being sent to the model. An
	// advertised-but-unhandled or unknown command degrades to a brief note.
	// Only when the prompt is NOT a slash command does it run as an ordinary
	// model turn below. Slash commands carry no images, so we intercept on the
	// text and ignore any image blocks.
	if handled, stopReason := s.handleSlashCommand(turnCtx, sess, text); handled {
		return PromptResult{StopReason: stopReason}, nil
	}

	return PromptResult{StopReason: s.runModelTurn(turnCtx, sess, text, images)}, nil
}

// runModelTurn runs one real model turn for sess with the given prompt text +
// images against the live turn context, returning the resolved ACP stopReason
// (§11). It is the shared "do work" path: handleSessionPrompt calls it for an
// ordinary prompt, and an extension command whose response action is `prompt`
// calls it with the extension-supplied task text — so an extension command can
// hand the agent work that streams agent_message_chunks and resolves with the
// turn's own stopReason, exactly like a typed prompt. The caller owns the turn
// mutex + the turn-permission registration; this only runs the turn body.
func (s *agentServer) runModelTurn(turnCtx context.Context, sess *session, text string, images []provider.ImageBlock) string {
	// Track the last turn's stop reason so EvDone resolves with the right
	// ACP stopReason (§11). The per-call sink is composed with OnEvent by
	// the core (wrapSink), so emits still go through the translator; here we
	// only observe the terminal-state signals.
	var lastStop provider.StopReason = provider.StopEnd
	sink := func(ev core.AgentEvent) {
		if te, ok := ev.(core.EvTurnEnd); ok {
			lastStop = te.Stop
		}
	}

	err := sess.agent.PromptWithPolicy(turnCtx, text, images, sink)
	cancelled := turnCtx.Err() != nil
	if err != nil && !errors.Is(err, context.Canceled) {
		// Surface a non-cancellation error as an agent_message_chunk so the
		// editor sees something, then still resolve the turn.
		sess.emit(map[string]any{
			"sessionUpdate": UpdateAgentMessageChunk,
			"content":       textContentBlock("Error: " + err.Error()),
		})
	}

	return stopReasonFor(cancelled, lastStop)
}

// handleSessionCancel is the session/cancel notification handler (§8). It
// cancels the in-flight turn's context, which:
//
//   - aborts the running provider request / tool (the turn ctx threads into
//     PromptWithPolicy), and
//   - unblocks any outstanding session/request_permission, because the
//     confirmer blocks on that same turn ctx in conn.request — so the
//     pending permission resolves as a refusal-with-cancel from our side
//     (we stop waiting) and the tool never runs.
//
// The prompt handler then observes turnCtx.Err() != nil and resolves
// session/prompt with stopReason "cancelled". Late tool/permission responses
// that arrive after cancel are tolerated (conn.request already returned and
// deleted its pending entry; deliver becomes a no-op).
//
// session/cancel is a notification (no id), so we never write a response.
// An unknown sessionId or a missing in-flight turn is a tolerated no-op.
func (s *agentServer) handleSessionCancel(params json.RawMessage) (any, error) {
	var p CancelParams
	if err := json.Unmarshal(params, &p); err != nil {
		// A malformed cancel is ignored rather than erroring — it's a
		// notification, and racing teardown shouldn't crash the loop.
		return nil, nil
	}

	s.mu.Lock()
	sess := s.sessions[p.SessionID]
	s.mu.Unlock()
	if sess == nil {
		return nil, nil
	}

	if cancel := sess.takeCancel(); cancel != nil {
		cancel()
	}
	return nil, nil
}
