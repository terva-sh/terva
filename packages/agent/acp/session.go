//go:build terva_acp

package acp

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
)

// session is one ACP session bound 1:1 to a core.Agent (§3). One terva acp
// process holds a map[sessionId]*session and can serve several concurrent
// editor sessions; each keeps its own agent, cwd, turn mutex, and the
// per-turn edit snapshots the diff translation needs.
type session struct {
	id    string
	cwd   string
	agent *core.Agent
	srv   *agentServer

	// gate is the session's ConfirmGate (nil for a pure-yolo session with no
	// gate). session/set_mode switches its approval mode at runtime via
	// gate.SetMode (copy-on-write, race-safe) — no agent rebuild needed,
	// which keeps the ACP confirmer wired as the inner Confirmer (§4b).
	gate *core.ConfirmGate

	// sandbox is the session's filesystem/shell confinement, shared by
	// pointer with every tool in the agent's registry. The native /jail and
	// /unjail commands Lock/Unlock it to confine tools to (or release them
	// from) the session cwd, mirroring the TUI. nil when the build has no
	// sandbox; the commands degrade to a note then.
	sandbox *tools.Sandbox

	// skills is a snapshot func that re-discovers the visible SKILL.md skills
	// for this session, mirroring the TUI's SkillSnapshot. The native /skills
	// command lists them. nil when skills are disabled or unavailable.
	skills func() []*skills.Skill

	// extCommands snapshots the extension-registered slash commands for this
	// session so availableCommands() can advertise them alongside the built-in
	// curated set; invokeExtCommand fires one. Both come from the host
	// (SessionAgent), which owns the extensions.Manager the acp package can't
	// import. nil when the session has no extension manager — the command
	// surface is then built-in only.
	extCommands      func() []ExtCommandInfo
	invokeExtCommand func(ctx context.Context, name, args string) (ExtCommandResult, error)

	// extContext snapshots what this session's extensions are injecting into
	// the model (static guidance + live cards) so the native /context command
	// can show it; reloadExtensions tears down + respawns the extensions and
	// returns the reload stats so /reload-ext can report them. Both come from
	// the host (SessionAgent), which owns the extensions.Manager the acp package
	// can't import. nil when the session has no extension manager — /context and
	// /reload-ext degrade to a graceful note then.
	extContext       func() []ContextItem
	reloadExtensions func(ctx context.Context) ReloadStats

	// trustWorkspace / untrustWorkspace persist the cwd's Workspace Trust
	// verdict and re-apply it to the live session so the native /trust and
	// /untrust commands work from inside an editor (the only in-editor way to
	// trust a workspace over ACP). trustWorkspace records the cwd (parent marks
	// "trust descendants too") and reloads the extension manager so project
	// extensions go live this session; untrustWorkspace drops the cwd and reloads
	// to remove them. Both come from the host (SessionAgent), which owns the
	// trust store + extensions.Manager the acp package can't import. nil when the
	// host didn't wire them — /trust and /untrust degrade to a graceful note.
	trustWorkspace   func(parent bool) error
	untrustWorkspace func() error

	// recordSwap tells the host a model switch landed, so whatever the host
	// re-resolves from tracks it (SessionAgent.RecordModelSwap). Called beside
	// ModelSwitch.Apply — the two are halves of one event in different scopes,
	// and this package is the only thing holding both. nil when the host
	// re-resolves nothing.
	recordSwap func(provider, model string, rebuiltClient bool)

	// modelMu guards the session's current provider/model, the seed for the
	// model config option's currentValue and the source for a model switch's
	// "from" side. session/set_config_option updates these after a successful
	// SetClientAndModel/SetModel.
	modelMu  sync.Mutex
	provider string
	model    string

	// mode is the session's current approval mode id (a SessionMode.ID),
	// guarded by modelMu. Seeded from the gate's mode and updated on
	// session/set_mode so current_mode_update and a later session result
	// report the live value.
	mode string

	// durable is the on-disk terva session this ACP session writes through
	// (§3): the factory created/reopened it and wired the agent's
	// OnMessageAppended / OnUsage / OnTranscriptCompacted to it, so the real
	// session IS the transcript. Its Path is this session's id, the durable
	// identity a later session/load reopens. Held here so disconnect teardown
	// can Close() it (flush + drop the empty-stub file).
	durable *core.Session

	// cleanup stops this session's per-session subprocesses: the MCP servers
	// the factory started from the session/new|load mcpServers payload, AND
	// the extension subprocesses the factory discovered/loaded for this
	// session. The factory returned this single func (MCP stop + extension
	// stop); the acp package calls it on disconnect (closeSessions) and when
	// an id is rebound, so neither set of subprocesses leaks. Never nil — the
	// factory returns a no-op when there is nothing to stop.
	cleanup func()

	// turnMu serialises one session/prompt at a time for this session,
	// mirroring rpcServer.turnMu. A second prompt blocks until the first
	// finishes (the editor model is one turn per session).
	turnMu sync.Mutex

	// activeCancel cancels the in-flight turn (Phase 2 session/cancel seam).
	cancelMu     sync.Mutex
	activeCancel context.CancelFunc

	// perm carries the live permission state for this session: the
	// in-flight turn's context (so a permission round-trip unblocks on
	// session/cancel), the tool call currently awaiting a verdict, and
	// the session-scoped allow_always/reject_always cache so a remembered
	// decision never re-prompts the editor (§8).
	permMu      sync.Mutex
	permTurnCtx context.Context
	permDecided map[string]bool // toolName -> allow (allow_always / reject_always)

	// editSnapshots holds the pre-edit file contents captured when an
	// `edit`/`write` tool_call is announced, keyed by toolCallId, so the
	// tool_result can emit a whole-file {oldText,newText} diff (§7). The
	// pre/post snapshot approach sidesteps terva's custom Details["diff"].
	snapMu        sync.Mutex
	editSnapshots map[string]editSnapshot
}

type editSnapshot struct {
	path    string // absolute
	oldText string
	existed bool
}

func newSession(id, cwd string, agent *core.Agent, durable *core.Session, srv *agentServer) *session {
	return &session{
		id:            id,
		cwd:           cwd,
		agent:         agent,
		durable:       durable,
		srv:           srv,
		editSnapshots: make(map[string]editSnapshot),
		permDecided:   make(map[string]bool),
	}
}

// setModel records the session's current provider/model (the model selector's
// currentValue and the "from" side of the next switch).
func (s *session) setModel(prov, model string) {
	s.modelMu.Lock()
	s.provider = prov
	s.model = model
	s.modelMu.Unlock()
}

// currentModel returns the session's current provider/model.
func (s *session) currentModel() (prov, model string) {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	return s.provider, s.model
}

// setMode records the session's current approval-mode id.
func (s *session) setMode(mode string) {
	s.modelMu.Lock()
	s.mode = mode
	s.modelMu.Unlock()
}

// currentMode returns the session's current approval-mode id.
func (s *session) currentMode() string {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	return s.mode
}

// setCancel / takeCancel guard the active turn's cancel func (Phase 2 seam).
func (s *session) setCancel(c context.CancelFunc) {
	s.cancelMu.Lock()
	s.activeCancel = c
	s.cancelMu.Unlock()
}

func (s *session) takeCancel() context.CancelFunc {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	c := s.activeCancel
	s.activeCancel = nil
	return c
}

// beginTurnPermission records the in-flight turn's context as the one a
// permission round-trip blocks on, so cancelling the turn ctx (via
// takeCancel/session/cancel) also unblocks any pending
// session/request_permission with a cancelled verdict. Called at the top
// of a prompt turn; cleared by endTurnPermission when the turn finishes.
func (s *session) beginTurnPermission(turnCtx context.Context) {
	s.permMu.Lock()
	s.permTurnCtx = turnCtx
	s.permMu.Unlock()
}

func (s *session) endTurnPermission() {
	s.permMu.Lock()
	s.permTurnCtx = nil
	s.permMu.Unlock()
}

// turnContext returns the current turn ctx for a permission round-trip,
// nil-guarded to context.Background() so a confirmer invoked outside a
// tracked turn still has a usable (uncancellable) context. The call id no
// longer lives here: the gate hands it straight to ConfirmWithCall (§13).
func (s *session) turnContext() context.Context {
	s.permMu.Lock()
	defer s.permMu.Unlock()
	ctx := s.permTurnCtx
	if ctx == nil {
		ctx = context.Background()
	}
	return ctx
}

// rememberDecision caches an allow_always / reject_always verdict for the
// rest of the session so the same tool never re-prompts the editor (§8,
// §14: session-scoped, not written back to config).
func (s *session) rememberDecision(toolName string, allow bool) {
	s.permMu.Lock()
	s.permDecided[toolName] = allow
	s.permMu.Unlock()
}

// recallDecision returns a cached always-decision for a tool, if any.
func (s *session) recallDecision(toolName string) (allow bool, ok bool) {
	s.permMu.Lock()
	defer s.permMu.Unlock()
	allow, ok = s.permDecided[toolName]
	return allow, ok
}

// cachedDecisions returns the session-scoped allow_always / reject_always
// grants the confirmer has remembered, as sorted name lists. Used by the
// native /permissions summary so the editor can see what won't re-prompt.
func (s *session) cachedDecisions() (allowed, rejected []string) {
	s.permMu.Lock()
	defer s.permMu.Unlock()
	for tool, allow := range s.permDecided {
		if allow {
			allowed = append(allowed, tool)
		} else {
			rejected = append(rejected, tool)
		}
	}
	sort.Strings(allowed)
	sort.Strings(rejected)
	return allowed, rejected
}

// emit sends one session/update notification for this session. The update
// map already carries the `sessionUpdate` discriminator plus the variant
// fields (built in translate.go); we wrap it in the {sessionId, update}
// envelope verbatim. Ordering is guaranteed by conn.write's single-writer
// mutex, so streamed chunks can't reorder (§6 ordered emit).
func (s *session) emit(update map[string]any) {
	_ = s.srv.conn.notify(MethodSessionUpdate, map[string]any{
		"sessionId": s.id,
		"update":    update,
	})
}

// snapshotEdit records the pre-edit file contents for a tool call.
func (s *session) snapshotEdit(toolCallID, path string) {
	abs := s.resolvePath(path)
	if abs == "" {
		return
	}
	data, err := os.ReadFile(abs)
	snap := editSnapshot{path: abs, existed: err == nil}
	if err == nil {
		snap.oldText = string(data)
	}
	s.snapMu.Lock()
	s.editSnapshots[toolCallID] = snap
	s.snapMu.Unlock()
}

// takeEditSnapshot pops the snapshot recorded for a tool call.
func (s *session) takeEditSnapshot(toolCallID string) (editSnapshot, bool) {
	s.snapMu.Lock()
	defer s.snapMu.Unlock()
	snap, ok := s.editSnapshots[toolCallID]
	if ok {
		delete(s.editSnapshots, toolCallID)
	}
	return snap, ok
}

// resolvePath turns a tool-arg path into an absolute path under the session
// cwd. Empty if path is empty.
func (s *session) resolvePath(path string) string {
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(s.cwd, path))
}
