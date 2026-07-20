package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/hooks"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/agent/worker"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/relaunch"
)

// Workspace is the in-process implementation of [ctrlproto.WorkspaceService]
// for a single project/cwd. It owns one [core.Agent] per session, fans each
// session's event stream out to N subscribers, and surfaces tool-approval and
// question round-trips as broadcast events resolved by the first client to
// answer (the multi-device story).
//
// It is the reusable in-process carrier of the control plane: `terva web` binds
// it to a WebSocket via [ctrlproto.ServeConn]; the TUI can later bind it
// directly, with no serialization on the hot path (the interface-first payoff).
//
// v1 scope: conversation + session groups and live model switching. Extension
// MANAGEMENT is deferred to a later stage — lore, hooks, the confirm gate, and
// tool approvals all work here without it (see buildSession). Agents are built
// lazily on first access and kept live for the workspace's lifetime.
type Workspace struct {
	args     build.Args
	version  string
	root     string // $TERVA_HOME
	cwd      string
	provider string // default provider for new sessions
	model    string // default model for new sessions
	hookEng  *hooks.Engine

	// sandbox is the workspace-shared filesystem sandbox (rooted at cwd).
	// Every session's tools are re-pointed at it (buildSession / rebuildTools
	// via Resolved.UseSandbox), so /jail is a workspace-scoped posture: locking
	// it confines every session's tools AND the TUI's local `!`-shell escape,
	// and the state survives agent rebuilds (ext reload, MCP toggle). Its
	// initial lock comes from Resolve (resolveJail: on for interactive).
	sandbox *tools.Sandbox

	ctx    context.Context
	cancel context.CancelFunc

	swarm *swarm.Swarm // workspace-global background agents (the tasks pane)

	// chat is the workspace's chat-bridge registry (the chat pane). Bridges are
	// bound to a session id and never follow a client's active pane. See
	// workspace_chat.go.
	chat wsChat

	// raati is the deliberation board (the raati pane): at most one live
	// deliberation, run over the workspace swarm. Zero value is an idle
	// board. See workspace_raati.go.
	raati raatiBoard

	// Workspace-global MCP servers: started once for the daemon (subprocesses are
	// expensive to respawn per session) and merged into every session's agent.
	// nil when --no-mcp or none configured. mcpStop tears them down in Close.
	mcpAdapter *build.MCPToolAdapter
	mcpStop    func()
	mcpMu      sync.Mutex // serializes live MCP toggles (StartOne/StopOne want one caller)

	// hub carries WORKSPACE-scoped events: the facts that are true of the daemon
	// rather than of any session (a workspace surface changing, the locale
	// changing, a restart notice — and, once the auth group lands, a login).
	//
	// Before it, BroadcastAll simulated a workspace hub by looping over the live
	// sessions' hubs and stamping a copy with each session's id. That could not
	// reach a client holding no subscriptions, nor survive the deletion of the
	// last live session — which is exactly when a workspace-scoped event matters
	// most. Subscribers reach this through Subscribe(ctx, ctrlproto.AddrWorkspace).
	//
	// Reached through events(), never directly: a Workspace is also built by
	// struct literal (tests, and any future composition root that skips New), and
	// a workspace-scoped broadcast must not depend on somebody having remembered
	// to construct a hub.
	hub     *wsHub
	hubOnce sync.Once

	// auth is the model-provider login machinery, live only when the composition
	// root called EnableAuth (`terva web --web-allow-login`). Zero value = the
	// auth group is not served, every auth verb answers CodeUnsupported, and the
	// Providers pane reports CanLogin false so a client renders no controls
	// rather than controls that fail.
	auth wsAuth

	mu       sync.Mutex
	sessions map[string]*wsSession

	// credErr is the boot-time credential-resolution failure (nil once a
	// credential resolves — RefreshDefaults clears it after an in-TUI login).
	// A credential-less Workspace constructs fine: only sessions hard-require
	// a credential (buildSession's own Resolve). Guarded by mu.
	credErr error

	// trusted is the launch cwd's Workspace Trust verdict, captured at
	// construction for hosts that need it before any session exists (the
	// TUI's credential-less login boot). Immutable after NewWorkspace —
	// live trust flips ride Trust/Untrust, which reload sessions directly.
	trusted bool

	// personaName labels the default persona for hosts that need it before
	// any session exists (same audience as trusted). Immutable.
	personaName string

	// diag receives host-side session-build diagnostics (permission-policy
	// warnings, extension-load results). It defaults to a line on os.Stderr,
	// which is right for the web/ACP daemons, but the in-process TUI carrier
	// overrides it via SetDiag — a stray stderr write would corrupt the
	// full-screen alternate-screen UI. Always non-nil after NewWorkspace.
	diag func(string)
}

var _ ctrlproto.WorkspaceService = (*Workspace)(nil)

// NewWorkspace builds a Workspace from resolved args and captures the default
// provider/model for new sessions. It does NOT require a credential: sessions
// hard-require one at build time (buildSession's own Resolve), so a
// credential-less Workspace can boot for a host with a login flow — the TUI
// opens /login and calls RefreshDefaults once a credential lands. Hosts that
// cannot log in (the web daemon) fail fast on CredentialErr instead.
func NewWorkspace(args build.Args, version string) (*Workspace, error) {
	r, err := build.Resolve(args, false)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &Workspace{
		args:     args,
		version:  version,
		root:     config.TervaHome(),
		cwd:      r.CWD,
		provider: r.Provider,
		model:    r.Model,
		hookEng:  build.BuildHookEngine(args, r.Trusted),
		ctx:      ctx,
		cancel:   cancel,
		sessions: map[string]*wsSession{},
		credErr:  r.CredentialErr,
		trusted:  r.Trusted,
		sandbox:  r.Sandbox, // shared across sessions; carries the initial jail lock
		diag:     func(m string) { fmt.Fprintln(os.Stderr, m) },
	}
	// Sweep leaked empty sessions at boot: a Stage chat opened for preview defers
	// its greeting (no message rows), so a daemon hard-killed before that draft's
	// Close can leave a meta-only file behind. PruneEmptySessions removes any file
	// with no message row before any resume, so drafts never accrue. (The TUI/CLI
	// path already prunes; the web daemon did not until here.)
	core.PruneEmptySessions(w.root, w.cwd)
	w.personaName = r.Persona.Name
	// Workspace-global swarm for the tasks pane. Construction does no I/O;
	// Reload pulls previously-spawned agents off disk (shown as detached, like
	// the TUI). Close drains running children gracefully (StopAllAndWait) so
	// their durable state survives for the next launch's Reload/Resume.
	swarmCfg := swarm.Config{Root: swarm.DefaultRoot(config.TervaHome()), RepoRoot: r.CWD}
	// --swarm-worktrees: lease each sub-agent its own git worktree from the
	// built-in worktree engine (carrier_swarm_worktree.go calls it directly —
	// no extension, no live-session requirement). Off => shared tree.
	if uc, _ := config.LoadConfig(); build.ResolveSwarmWorktrees(args.SwarmWorktrees, uc.SwarmWorktrees) {
		swarmCfg.AcquireWorktree = w.acquireSwarmWorktree
	}
	// Route each agent to its runner by the opaque Backend label the swarm
	// persists but never interprets: no label -> the native `terva --swarm-agent`
	// child; a label -> a foreign worker driven behind the same supervisor seams.
	swarmCfg.NewRunner = w.newRunner
	w.swarm = swarm.New(swarmCfg)
	_, _ = w.swarm.Reload()
	go w.pollTasks()

	// Start the configured MCP servers once for the whole daemon; their tools are
	// merged into each session's agent in buildSession. setupMCP also merges into
	// this (throwaway) r — harmless; the per-session merge is what counts.
	w.mcpAdapter, w.mcpStop = build.SetupMCP(w.ctx, args, &r)
	// When a workspace-global MCP server dies unexpectedly, the manager
	// withdraws its tools; refresh every live session so the model's tool set
	// drops the dead server promptly — the same rebuild a live /mcp toggle runs.
	if w.mcpAdapter != nil && w.mcpAdapter.Mgr != nil {
		w.mcpAdapter.Mgr.SetOnToolsChanged(func() { w.rebuildAllSessions("mcp-server-died") })
	}

	return w, nil
}

// newRunner is the swarm's Config.NewRunner: it produces the runner for one
// agent by the opaque Backend label. No label is the historical case and stays
// the native `terva --swarm-agent` child. A label routes to a foreign worker,
// composed and scrubbed and supervised by worker.Runner.
//
// Note this is UNCONDITIONAL on the external-workers config gate: the gate lives
// at the spawn tool (whether a NEW foreign spawn is allowed), not here. An agent
// that already carries a backend — revived from meta.json after a restart — must
// come back on its backend whatever the current gate says, or disabling the knob
// would strand a worker mid-task rather than merely stopping new ones.
func (w *Workspace) newRunner(a *swarm.Agent) swarm.Runner {
	if a.Backend == "" {
		return swarm.NewExecRunner(a)
	}
	b, err := worker.Lookup(a.Backend)
	if err != nil {
		// An unknown backend is the host's error to raise, and raising it as a
		// failed agent (rather than a fallback to native, which would run
		// plausible work under the wrong identity) is the whole point of Lookup
		// erroring instead of defaulting.
		return failedRunner{err}
	}
	resolved, err := w.resolveForWorker(a)
	if err != nil {
		return failedRunner{fmt.Errorf("worker %s: resolve briefing: %w", a.Backend, err)}
	}
	return worker.NewRunner(a, b, resolved, w.workerApprover(a))
}

// workerApprover is the orchestrator's approval seam for a worker: it routes the
// worker's tool-approval requests to the DISPATCHING session's human card (the
// same place that session's own tool approvals land). Nil when the dispatching
// session is gone — a worker revived after a restart, or a spawn with no session
// stamp — in which case the runner denies the worker's asks cleanly rather than
// hanging on a human who isn't there.
func (w *Workspace) workerApprover(a *swarm.Agent) core.Confirmer {
	s := w.existing(a.SessionID)
	if s == nil {
		return nil
	}
	return &workerConfirmer{s: s, ctx: w.ctx, agentID: a.ID}
}

// resolveForWorker assembles terva's own resolved state for a worker's briefing.
// It is the SAME build.Resolve the host uses for a session — the one-assembler
// rule — parameterised by the WORKER's identity: its persona (which may differ
// from the dispatcher's), its model, its provider.
//
// It resolves against the dispatcher's cwd, NOT the worker's lease: that keeps
// the trust verdict and project config correct (they were resolved for the repo
// the user trusted, not for a fresh worktree path the trust store has never
// seen), and the composer re-roots the discovery pointers into the lease itself
// (worker.rerootIntoWorkspace). Credentials are NOT required — a foreign worker
// authenticates itself, and terva deliberately hands it none — so a credential-
// less resolve is expected and its swallowed error is ignored by the composer.
func (w *Workspace) resolveForWorker(a *swarm.Agent) (build.Resolved, error) {
	next := w.args
	next.Persona = a.Persona
	next.Model = a.Model
	next.Provider = a.Provider
	return build.Resolve(next, false)
}

// failedRunner reports a fixed error the moment the swarm runs it. The
// Config.NewRunner seam returns a Runner and no error, so a backend that cannot
// be looked up or a briefing that cannot be resolved has nowhere to fail at
// construction; this carries the failure into Run, where the swarm turns it into
// a StatusFailed agent with the message on its tile — visible and diagnosable,
// which an error swallowed at spawn would not be.
type failedRunner struct{ err error }

func (r failedRunner) Run(context.Context, swarm.Sink) error { return r.err }

// allowWorkerBackend is the swarm_spawn `backend` gate, injected into the tool
// (the tools package cannot import the worker registry without cycling through
// build). It delegates to worker.AllowSpawn — the single gate shared with the
// board's tasks-surface spawn and the TUI's /swarm command, so the policy can't
// drift between initiators — which reads the external-workers knob LIVE and
// validates the name against the registered backends.
func allowWorkerBackend(name string) error {
	return worker.AllowSpawn(name)
}

// SetDiag redirects host-side session-build diagnostics away from os.Stderr —
// the in-process TUI carrier sets a sink that swallows or surfaces them, since a
// stray stderr write corrupts the full-screen UI. A nil fn restores silence.
func (w *Workspace) SetDiag(fn func(string)) {
	if fn == nil {
		fn = func(string) {}
	}
	w.mu.Lock()
	w.diag = fn
	w.mu.Unlock()
}

// CredentialErr reports the boot-time credential-resolution failure, nil once
// a credential resolves. The TUI carrier boots session-less on it and opens
// /login; the web daemon (no login flow) fails fast on it.
func (w *Workspace) CredentialErr() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.credErr
}

// Defaults returns the workspace's default provider and model for new
// sessions — resolvable even credential-less (they come from flags/config/
// catalog), so the TUI's login boot can label the status line before the
// first session exists.
func (w *Workspace) Defaults() (provider, model string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.provider, w.model
}

// Trusted reports the launch cwd's Workspace Trust verdict captured at
// construction (see the field note; live flips ride Trust/Untrust).
func (w *Workspace) Trusted() bool { return w.trusted }

// Sandbox returns the workspace-shared filesystem sandbox. The in-process TUI
// carrier passes it to InteractiveConfig so /jail and /unjail toggle the same
// object every session's tools (and the local `!`-shell escape) enforce, and
// the status bar reads its lock state. A remote carrier can't hand out the
// pointer — jail there becomes a control verb, deferred to that milestone.
func (w *Workspace) Sandbox() *tools.Sandbox { return w.sandbox }

// RefreshDefaults re-resolves the workspace's default provider/model and
// credential state from args + the config/auth stores. The TUI calls it
// after an in-TUI login stores a fresh credential — the login flow may also
// have promoted a new default provider/model — so sessions created from here
// on resolve against the new state.
func (w *Workspace) RefreshDefaults() error {
	r, err := build.Resolve(w.args, true)
	if err != nil {
		return err
	}
	w.mu.Lock()
	w.provider, w.model = r.Provider, r.Model
	w.credErr = r.CredentialErr
	w.mu.Unlock()
	return nil
}

// Close cancels in-flight turns, drains swarm sub-agents, closes open
// session files, and releases the hook engine. Idempotent.
func (w *Workspace) Close() error {
	// Drain swarm children BEFORE cancelling the workspace context: a
	// graceful stop lets each child abort its turn, write its
	// agent_stopped terminator, and flush its session — so the next
	// launch's Reload shows "shutdown (offline)", resumable, instead of
	// an event log cut off mid-sentence. (Children cannot survive the
	// host anyway: the runner owns their stdout pipes.)
	if w.swarm != nil {
		w.swarm.StopAllAndWait(0)
	}
	// Stop chat bridges before cancelling the workspace context, so a connector's
	// receive goroutine never outlives the workspace that owns it.
	w.chatStopAll()
	w.mu.Lock()
	for id, s := range w.sessions {
		s.close()
		delete(w.sessions, id)
	}
	w.mu.Unlock()
	w.cancel()
	if w.mcpStop != nil {
		w.mcpStop() // StopAll + closeLogs, once for the daemon
	}
	if w.hookEng != nil {
		_ = w.hookEng.Close()
	}
	return nil
}

// --- path / id helpers ---

func (w *Workspace) sessionsDir() string          { return core.SessionsDir(w.root, w.cwd) }
func (w *Workspace) sessionPath(id string) string { return filepath.Join(w.sessionsDir(), id+".jsonl") }

// validSessionID reports whether a client-supplied wire session id is a safe
// handle: a bare filename stem, never a path. The id becomes
// <sessionsDir>/<id>.jsonl and filepath.Join CLEANS but does not CONTAIN "..",
// so an id like "../../foo" (or an absolute path) would escape the sessions dir
// and let sessions.delete/rename/resume touch arbitrary .jsonl files. Reject
// anything with a separator, a "..", or that isn't its own basename.
func validSessionID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	// A reserved address (#workspace) is an address, not a session. Everything
	// below is a PATH-SAFETY check — no separators, no traversal, a clean base
	// name — and "#workspace" passes all of it, so without this line the
	// workspace would cheerfully materialize a session FILE by that name, and a
	// client could create a session that shadows an address. Nothing else in
	// this function would catch it.
	if ctrlproto.IsReservedAddr(id) {
		return false
	}
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return false
	}
	return filepath.Base(id) == id
}

// The wire session id is the session file's name stem (see sessionIDFromPath in
// sessionread.go). NewSession's Meta.ID is a separate UUID; the filename is the
// stable, path-addressable handle, so it is what crosses the wire.

func toCtrlUsage(u provider.Usage) core.WireUsage {
	return core.WireUsage{
		Input:      u.InputTokens,
		Output:     u.OutputTokens,
		CacheRead:  u.CacheReadTokens,
		CacheWrite: u.CacheWriteTokens,
		CostUSD:    u.CostUSD,
	}
}

func ctrlTimeString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// --- session resolution ---

// resolve returns the live wsSession for id, materializing it from disk when
// needed. An empty id resolves to the default session (latest on disk, or a
// fresh one). Used by Prompt/Subscribe/Resume.
func (w *Workspace) resolve(id string) (*wsSession, error) {
	w.mu.Lock()
	had := len(w.sessions)
	s, err := w.sessionLocked(id)
	// The live set grew: a cold session just materialized (or an empty id
	// created a fresh one). A board keys "which sessions can I subscribe to?"
	// off Live, so tell it to re-list — broadcast after the unlock.
	grew := err == nil && len(w.sessions) > had
	w.mu.Unlock()
	if grew {
		w.BroadcastAll(ctrlproto.SessionsChangedEvent())
	}
	return s, err
}

// existing returns an already-materialized session for id, or nil.
func (w *Workspace) existing(id string) *wsSession {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sessions[id]
}

// live returns the running session the id names, resolving the empty id
// to the workspace default exactly like resolve — but never building a
// session. Used by Cancel/Approve/Answer/Queue/SetQueue, which act on
// transient in-memory state (a parked confirmation, the queue, an
// in-flight turn) that a freshly built session cannot have, so they stay
// no-ops on a session that isn't live. Before this helper those methods
// looked ids up verbatim, so a client that used "" consistently — valid
// everywhere else on the wire — had its approvals silently dropped.
func (w *Workspace) live(sess string) *wsSession {
	if sess == "" {
		if p := core.LatestSession(w.root, w.cwd); p != "" {
			sess = build.SessionIDFromPath(p)
		}
	}
	return w.existing(sess)
}

func (w *Workspace) sessionLocked(id string) (*wsSession, error) {
	if id == "" {
		if p := core.LatestSession(w.root, w.cwd); p != "" {
			id = build.SessionIDFromPath(p)
		} else {
			return w.createLocked(ctrlproto.CreateOpts{})
		}
	}
	if !validSessionID(id) {
		return nil, ctrlproto.ErrNoSession
	}
	if s, ok := w.sessions[id]; ok {
		return s, nil
	}
	path := w.sessionPath(id)
	if _, err := os.Stat(path); err != nil {
		return nil, ctrlproto.ErrNoSession
	}
	sess, msgs, err := core.OpenSession(path)
	if err != nil {
		return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "open session: %v", err)
	}
	// Load-cost telemetry: a session that carries revision (edits/variants) logs
	// how long its transcript took to reconstruct and how much amend machinery it
	// replayed, so accumulating variants becoming a load-time tax shows up in the
	// diagnostics before it is felt (stage-inline-editing.md §9). Silent for plain
	// sessions (no amends).
	if st := sess.LoadStats; st.Amends > 0 {
		w.diag(fmt.Sprintf("session %s reconstructed: %d msgs, %d amends, %d tail takes in %s",
			id, st.Messages, st.Amends, st.TailTakes, st.Elapsed.Round(time.Microsecond)))
	}
	// The daemon is picking this session back up to keep talking in it, so the
	// rows it is about to write belong to THIS build, not the one that created
	// the file. Best-effort: a session that resumes but cannot record its
	// version is still a session worth resuming.
	_ = sess.StampVersion(w.version)
	// Resume with the same compact window the legacy TUI uses: the live agent
	// gets the recent tail (plus any compaction summary), not the whole
	// multi-thousand-message history — which would otherwise ride every
	// subsequent request. The session FILE stays intact, and the per-message
	// persistence hooks only append new rows, so nothing is lost on disk.
	full := msgs
	msgs = build.TrimMessagesForResume(full, 100)
	// The trim shortens the in-memory transcript while the file keeps the full
	// history, so record how far the two index spaces diverge — revise verbs anchor
	// their persisted amends to the on-disk space through it (see wsSession.diskIndex).
	base, head := reviseBaseFor(full, msgs)
	s, err := w.buildSession(id, sess, msgs, base, head)
	if err != nil {
		return nil, err
	}
	w.sessions[id] = s
	return s, nil
}

// sceneSeed carries a parent scene's LIVE state into the session createLocked
// is building (SD5's next-scene flow). A saved WorldDoc is the wrong source
// for this: an unsaved chat World has none at all, and a saved one is only as
// fresh as the last worlds.save — the session that just played is the truth.
// nil for every ordinary create.
type sceneSeed struct {
	lore         []core.WorldLoreEntry
	coordination string
	world        string
	castModels   map[string]core.CastRoute
	note         string
	// The parent's bound user persona — who the player is in the story. It
	// outranks the workspace default a fresh immersive session would take:
	// scene two of the same story is the same person.
	userName, userDescription, userGender, userPronouns string
	// opening is the cold-open beat. It is appended BEFORE materialize, which
	// is what makes it stand in place of the card greeting — buildSession only
	// seeds greetings into an empty transcript.
	opening      string
	openingActor string
	// parent is the scene this one continues. A next scene is a SUCCESSOR, not a
	// branch — it shares no transcript prefix, so ForkPoint stays empty — but
	// without this the two sessions had nothing recording that one came from the
	// other, and a World (which does not order its scenes) could not supply it.
	parent string
}

func (w *Workspace) createLocked(opts ctrlproto.CreateOpts) (*wsSession, error) {
	return w.createSeededLocked(opts, nil)
}

func (w *Workspace) createSeededLocked(opts ctrlproto.CreateOpts, seed *sceneSeed) (*wsSession, error) {
	// New sessions start on the configured default (models.set_default), read
	// live so a default set earlier this session takes effect at once — NOT on
	// whatever model another session was last switched to (switchModel no longer
	// moves the workspace default). Fall back to the boot-resolved default
	// (w.provider/w.model, which honors a launch --model and the catalog
	// fallback) when config names no default. An explicit opts.Model wins below.
	prov, model := w.provider, w.model
	if dp, dm, _ := w.defaultModel(); dp != "" && dm != "" {
		prov, model = dp, dm
	}
	if opts.Model != "" {
		// Honor an explicit provider: model ids can exist under several
		// providers (subscription vs api-key), and the unqualified lookup may
		// pick one the workspace holds no credential for — the created
		// session would then fail its deferred Resolve.
		if m, e := provider.FindModel(opts.Provider, opts.Model); e == nil {
			prov, model = m.Provider, m.ID
		}
	}
	// Validate the requested persona up front so an unknown name is a clean
	// CodeBadRequest rather than a CodeInternal from the deferred Resolve.
	// Template is accepted on the wire but not yet applied (reserved).
	if opts.Persona != "" {
		if _, err := build.ResolvePersona(opts.Persona); err != nil {
			return nil, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("unknown persona %q: %v", opts.Persona, err))
		}
	}
	if opts.Experience != "" && opts.Experience != build.ExperienceChat && opts.Experience != build.ExperiencePlay {
		return nil, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("unknown experience %q (want %q or %q)", opts.Experience, build.ExperienceChat, build.ExperiencePlay))
	}
	// Creating inside a saved World (W5): the World's roster/pins/lore/
	// coordination seed the session's working copy — a COPY, never a live link
	// (worlds.save writes back explicitly). Resolved before the session file
	// exists so an unknown World is a clean refusal.
	var worldDoc *build.WorldDoc
	if opts.World != "" {
		doc, err := build.NewWorldStore().Get(opts.World)
		if err != nil {
			return nil, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("unknown world %q", opts.World))
		}
		worldDoc = &doc
		if opts.Experience == "" {
			opts.Experience = build.ExperienceChat // a World session is immersive
		}
		if len(opts.Cast) == 0 {
			opts.Cast = doc.Characters
		}
	}
	sess, err := core.NewSession(w.root, w.cwd, prov, model, w.version)
	if err != nil {
		return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "create session: %v", err)
	}
	if opts.Title != "" {
		_ = core.RenameSession(sess.Path, opts.Title)
		sess.Meta.Title = opts.Title
	}
	// Persist the creation spec (persona + immersive fields) so a daemon restart
	// re-materializes this session as created rather than as the workspace default.
	if err := sess.SetCreationSpec(opts.Persona, opts.Experience, opts.Card, opts.Cast, opts.Greeting); err != nil {
		return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "persist session spec: %v", err)
	}
	// A default saved user persona pre-fills a fresh immersive session's identity,
	// so a new chat opens as "you" rather than the literal "User" (rough-edge #5).
	// Stamped into meta before buildSession, so its name threads into the {{user}}
	// macro of the (deferred) greeting and it shows in the steering panel.
	if opts.Experience != "" {
		if seed != nil {
			if err := sess.SetUserPersona(seed.userName, seed.userDescription, seed.userGender, seed.userPronouns); err != nil {
				return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "carry user persona: %v", err)
			}
		} else if def, ok := w.userPersonaStore().Default(); ok {
			if err := sess.SetUserPersona(def.Name, def.Description, def.Gender, def.Pronouns); err != nil {
				return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "apply default user persona: %v", err)
			}
		}
	}
	if opts.Background != "" {
		if err := sess.SetBackground(opts.Background); err != nil {
			return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "persist session background: %v", err)
		}
	}
	// The rest of the World's state — pins, lore, coordination, membership —
	// stamps before materialize, which seeds every live record from meta.
	if worldDoc != nil {
		if err := sess.SetCast(opts.Cast, worldDoc.CharacterModels); err != nil {
			return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "seed world cast: %v", err)
		}
		if err := sess.SetWorldLore(worldDoc.Lore); err != nil {
			return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "seed world lore: %v", err)
		}
		if worldDoc.Coordination != "" {
			if err := sess.SetCoordination(worldDoc.Coordination); err != nil {
				return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "seed world coordination: %v", err)
			}
		}
		if err := sess.SetWorld(worldDoc.ID); err != nil {
			return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "stamp world membership: %v", err)
		}
	}
	// A scene sequel (SD5) stamps the parent's live World state over anything a
	// saved WorldDoc seeded above, then opens on its cold-open beat.
	var msgs []provider.Message
	if seed != nil {
		if err := sess.SetCast(opts.Cast, seed.castModels); err != nil {
			return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "carry cast: %v", err)
		}
		if err := sess.SetWorldLore(seed.lore); err != nil {
			return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "carry world lore: %v", err)
		}
		if seed.coordination != "" {
			if err := sess.SetCoordination(seed.coordination); err != nil {
				return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "carry coordination: %v", err)
			}
		}
		if seed.world != "" {
			if err := sess.SetWorld(seed.world); err != nil {
				return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "carry world membership: %v", err)
			}
		}
		if seed.note != "" {
			if err := sess.SetNote(seed.note); err != nil {
				return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "carry author's note: %v", err)
			}
		}
		if seed.parent != "" {
			if err := sess.SetParent(seed.parent); err != nil {
				return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "record scene lineage: %v", err)
			}
		}
		if body := strings.TrimSpace(seed.opening); body != "" {
			meta := map[string]string{core.MetaSource: core.MetaDirected}
			if a := strings.TrimSpace(seed.openingActor); a != "" {
				meta[core.MetaActor] = a
			}
			m := provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: body}},
				Time:    time.Now(),
				Meta:    meta,
			}
			if err := sess.AppendMessage(m); err != nil {
				return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "seed cold open: %v", err)
			}
			msgs = append(msgs, m)
		}
	}
	id := build.SessionIDFromPath(sess.Path)
	s, err := w.buildSession(id, sess, msgs, 0, false)
	if err != nil {
		return nil, err
	}
	w.sessions[id] = s
	return s, nil
}

// --- conversation group (WorkspaceService) ---

// Prompt is a CLIENT-facing entry (a TUI, a browser, a remote carrier). Its
// prompts mirror out to a bound chat bridge tagged "you: ", so the phone thread
// stays a complete record of the conversation. The bridge's own inbound messages
// submit through wsSession.prompt instead, which is how a chat-originated prompt
// avoids echoing itself back — the origin is the entry point, not a flag.
func (w *Workspace) Prompt(ctx context.Context, sess, text string, images []ctrlproto.Image) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	if err := s.prompt(text, images); err != nil {
		return err
	}
	w.mirrorUserTyped(s.id, text)
	return nil
}

func (w *Workspace) Queue(ctx context.Context, sess, text string) error {
	if s := w.live(sess); s != nil {
		s.queue(text)
		w.mirrorUserTyped(s.id, text)
	}
	return nil
}

// mirrorUserTyped echoes a client-originated prompt into the bound chat, on a
// goroutine so a network write never delays the local turn.
func (w *Workspace) mirrorUserTyped(sessID, text string) {
	if b := w.chatMirror(sessID); b != nil {
		go b.OnUserTyped(text)
	}
}

func (w *Workspace) SetQueue(ctx context.Context, sess string, texts []string) error {
	if s := w.live(sess); s != nil {
		s.setQueue(texts)
	}
	return nil
}

func (w *Workspace) Cancel(ctx context.Context, sess string) error {
	if s := w.live(sess); s != nil {
		s.cancelTurn()
	}
	return nil
}

func (w *Workspace) Compact(ctx context.Context, sess string) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	return s.compact(ctx)
}

func (w *Workspace) Clear(ctx context.Context, sess string) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	return s.clear()
}

func (w *Workspace) EditMessage(ctx context.Context, sess string, epoch uint64, index int, text string) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	return s.editMessage(epoch, index, text)
}

func (w *Workspace) DeleteMessage(ctx context.Context, sess string, epoch uint64, index int) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	return s.deleteMessage(epoch, index)
}

func (w *Workspace) SwipeTurn(ctx context.Context, sess string, epoch uint64, variant int) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	return s.swipe(epoch, variant)
}

func (w *Workspace) SwipeMessage(ctx context.Context, sess string, epoch uint64, index, variant int) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	return s.swipeMessage(epoch, index, variant)
}

var _ ctrlproto.VariantsController = (*Workspace)(nil)

func (w *Workspace) PruneVariants(ctx context.Context, sess string, epoch uint64, index int) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	return s.pruneVariants(epoch, index)
}

func (w *Workspace) DropVariant(ctx context.Context, sess string, epoch uint64, index, variant int) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	return s.dropVariant(epoch, index, variant)
}

func (w *Workspace) RetryTurn(ctx context.Context, sess string, p ctrlproto.TurnRetryParams) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	return s.retry(p)
}

func (w *Workspace) Approve(ctx context.Context, sess, callID string, d core.ConfirmDecision) error {
	if s := w.live(sess); s != nil {
		s.approve(callID, d)
	}
	return nil
}

func (w *Workspace) Answer(ctx context.Context, sess, askID string, a core.UserAnswer) error {
	if s := w.live(sess); s != nil {
		s.answer(askID, a)
	}
	return nil
}

func (w *Workspace) Subscribe(ctx context.Context, sess string) (<-chan ctrlproto.Event, error) {
	if sess == ctrlproto.AddrWorkspace {
		return w.subscribeWorkspace(ctx, false), nil
	}
	s, err := w.resolve(sess)
	if err != nil {
		return nil, err
	}
	return s.subscribe(ctx, false), nil
}

// events returns the workspace's event hub, building it on first use.
func (w *Workspace) events() *wsHub {
	w.hubOnce.Do(func() { w.hub = newWSHub() })
	return w.hub
}

// subscribeWorkspace streams the workspace's own events. There is no snapshot:
// a workspace event is a notification that something changed, and the client
// answers it by re-reading whatever it names (sessions.list, surface.get,
// auth.providers). Carrying the new state in the event would invent a second
// source of truth for state a verb already returns.
func (w *Workspace) subscribeWorkspace(ctx context.Context, reliable bool) <-chan ctrlproto.Event {
	hub := w.events()
	ch := hub.add(nil, reliable)
	go func() {
		<-ctx.Done()
		hub.remove(ch)
	}()
	return ch
}

// SubscribeReliable is Subscribe with no-drop delivery, for the in-process
// carrier (the TUI). It is deliberately NOT part of ctrlproto.WorkspaceService:
// a networked carrier can never promise unbounded delivery, so reliability is an
// in-process-only affordance the TUI reaches for on the concrete *Workspace. The
// events are identical to Subscribe's — only the backpressure discipline differs
// (the consumer must keep draining; a stall applies contained same-session
// backpressure rather than silently dropping a text delta).
func (w *Workspace) SubscribeReliable(ctx context.Context, sess string) (<-chan ctrlproto.Event, error) {
	if sess == ctrlproto.AddrWorkspace {
		return w.subscribeWorkspace(ctx, true), nil
	}
	s, err := w.resolve(sess)
	if err != nil {
		return nil, err
	}
	return s.subscribe(ctx, true), nil
}

// AgentFor is gone (plan 4.1). It handed the in-process TUI carrier the live
// *core.Agent — a transitional crutch for rendering and management dialogs the
// wire did not yet cover. Every reader migrated to a snapshot, a surface, or a
// verb; /btw, the last, drives the sidechat surface (workspace_sidechat.go).
// The daemon owns the agent and never lends it out. A feature that needs
// daemon-side state adds a ctrlproto verb or surface — see the policy note in
// modes/carrier.go.

// --- session group (WorkspaceService) ---

func (w *Workspace) Sessions(ctx context.Context) ([]ctrlproto.SessionInfo, error) {
	summaries := core.DescribeSessions(w.root, w.cwd)
	defID := ""
	if p := core.LatestSession(w.root, w.cwd); p != "" {
		defID = build.SessionIDFromPath(p)
	}
	// Trust is workspace-global (keyed on w.cwd), so every session in this list
	// shares one verdict; a live session overrides with its own live flag below.
	wsTrusted := build.ResolveTrustState(w.args).IsTrusted()
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]ctrlproto.SessionInfo, 0, len(summaries))
	for _, sm := range summaries {
		// A draft: an immersive session whose greeting is still deferred, so it has
		// no durable message rows (MessageCount == 0) — a character the user opened to
		// preview but never sent into. Excluded so previewing doesn't clutter the
		// list; the first real turn flushes the greeting and it appears. This also
		// catches a live-but-unflushed draft (its file is meta-only). Coding sessions
		// (Experience == "") are never drafts and are untouched.
		if sm.Experience != "" && sm.MessageCount == 0 {
			continue
		}
		id := build.SessionIDFromPath(sm.Path)
		title := sm.Title
		if title == "" {
			title = titleFromFirstText(sm.FirstUserText)
		}
		info := ctrlproto.SessionInfo{
			ID:         id,
			Title:      title,
			Provider:   sm.Provider,
			Model:      sm.Model,
			Experience: sm.Experience,
			Background: sm.Background,
			Card:       sm.Card,
			World:      sm.World,
			Path:       sm.Path,
			Created:    ctrlTimeString(sm.Started),
			Messages:   sm.MessageCount,
			Usage:      core.WireUsage{CostUSD: sm.TotalCost},
			Current:    id == defID,
			Trusted:    wsTrusted,
		}
		if fi, e := os.Stat(sm.Path); e == nil {
			info.Updated = ctrlTimeString(fi.ModTime())
		}
		if s, ok := w.sessions[id]; ok { // live overrides for an open session
			info.Provider, info.Model = s.currentModel()
			info.Persona = s.personaName()
			info.Usage = toCtrlUsage(s.agent.Cost())
			info.Trusted = s.trusted.Load()
			info.Live = true
			info.Busy = s.busyNow()
		}
		out = append(out, info)
	}
	return out, nil
}

func (w *Workspace) CreateSession(ctx context.Context, opts ctrlproto.CreateOpts) (ctrlproto.SessionInfo, error) {
	w.mu.Lock()
	s, err := w.createLocked(opts)
	w.mu.Unlock()
	if err != nil {
		return ctrlproto.SessionInfo{}, err
	}
	// A new session joined the set; broadcast outside the lock so a board
	// re-lists and adds its tile (BroadcastAll takes only the hub mutex).
	w.BroadcastAll(ctrlproto.SessionsChangedEvent())
	return s.info(), nil
}

func (w *Workspace) ResumeSession(ctx context.Context, sess string) (ctrlproto.SessionInfo, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return ctrlproto.SessionInfo{}, err
	}
	return s.info(), nil
}

// ForkSession branches the parent session at fromIndex (see the interface doc):
// core.BranchSession copies the parent's resolved transcript through fromIndex
// into a new parent-linked file, which is then materialized and registered like
// a create. Broadcasts sessions_changed so a board adds the child's tile.
func (w *Workspace) ForkSession(ctx context.Context, sess string, fromIndex int) (ctrlproto.SessionInfo, error) {
	w.mu.Lock()
	s, err := w.forkLocked(sess, fromIndex)
	w.mu.Unlock()
	if err != nil {
		return ctrlproto.SessionInfo{}, err
	}
	w.BroadcastAll(ctrlproto.SessionsChangedEvent())
	return s.info(), nil
}

func (w *Workspace) forkLocked(sess string, fromIndex int) (*wsSession, error) {
	if fromIndex < 0 {
		return nil, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("fork index %d out of range", fromIndex))
	}
	if !validSessionID(sess) {
		return nil, ctrlproto.ErrNoSession
	}
	parentPath := w.sessionPath(sess)
	if _, err := os.Stat(parentPath); err != nil {
		return nil, ctrlproto.ErrNoSession
	}
	// A parent mid-turn is appending to its file, so its transcript — and the
	// client's index into it — is moving; refuse until it settles (the revise
	// guard's discipline, applied to the branch point).
	p := w.sessions[sess]
	if p != nil && p.busyNow() {
		return nil, ctrlproto.ErrBusy
	}
	// The client's fromIndex is into the live parent's (possibly resume-trimmed)
	// transcript, but BranchSession walks the parent FILE's full effective
	// transcript — so re-anchor the branch point to on-disk space (identity for an
	// untrimmed parent). Without this, a long resumed parent would branch at the
	// wrong message. A cold parent (p nil) was never trimmed in this daemon, so its
	// indices already match the file.
	branchAt := fromIndex
	if p != nil {
		disk, ok := p.diskIndex(fromIndex)
		if !ok {
			return nil, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("cannot fork at the resume-window summary (index %d)", fromIndex))
		}
		branchAt = disk
	}
	// BranchSession keeps the parent's first N messages; fromIndex is inclusive.
	newPath, err := core.BranchSession(parentPath, w.root, w.cwd, w.version, branchAt+1)
	if err != nil {
		return nil, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "fork: %v", err)
	}
	id := build.SessionIDFromPath(newPath)
	childSess, msgs, err := core.OpenSession(newPath)
	if err != nil {
		return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "open fork: %v", err)
	}
	child, err := w.buildSession(id, childSess, msgs, 0, false)
	if err != nil {
		return nil, err
	}
	w.sessions[id] = child
	return child, nil
}

func (w *Workspace) RenameSession(ctx context.Context, sess, title string) error {
	if !validSessionID(sess) {
		return ctrlproto.ErrNoSession
	}
	path := w.sessionPath(sess)
	if _, err := os.Stat(path); err != nil {
		return ctrlproto.ErrNoSession
	}
	if err := core.RenameSession(path, title); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "rename: %v", err)
	}
	if s := w.existing(sess); s != nil {
		s.setTitle(title, false) // a user rename: never replaceable by automatic titling
		s.broadcast(ctrlproto.SessionUpdatedEvent(s.info()))
	}
	// A rename changes the set's shape (its labels), so tell boards to re-list —
	// SessionUpdatedEvent above only reaches THAT session's subscribers, not a
	// board watching the workspace.
	w.BroadcastAll(ctrlproto.SessionsChangedEvent())
	return nil
}

// GenerateSessionTitle implements sessions.generate_title: one bounded model
// call over the transcript's title seed, persisted and (for a live session)
// broadcast via applyTitle. It deliberately skips the auto_title gate — that
// toggle governs spending tokens UNASKED; this is the user asking — and
// overwrites whatever title exists, manual renames included. Cold sessions
// are read from disk without materializing a wsSession; their titles land as
// a rename row and clients converge on their next list.
func (w *Workspace) GenerateSessionTitle(ctx context.Context, sess string) (string, error) {
	if !validSessionID(sess) {
		return "", ctrlproto.ErrNoSession
	}
	live := w.existing(sess)
	var msgs []provider.Message
	var prov, model string
	if live != nil {
		msgs = live.agent.Messages()
		prov, model = live.currentModel()
	} else {
		file, m, err := core.OpenSession(w.sessionPath(sess))
		if err != nil {
			return "", ctrlproto.ErrNoSession
		}
		file.Close() // read-only use; a reopened session is never pruned by Close
		msgs = m
		prov, model = file.Meta.Provider, file.Meta.Model
	}
	seed := core.BuildTitleSeed(msgs, core.TitleSeedBudget)
	if seed == "" {
		return "", ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("session has no conversation to title"))
	}
	ok, cl, m := w.titleClient(prov, model)
	if !ok {
		return "", ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", i18n.T("no usable credential for a title model"))
	}
	// A live session books the spend; a cold one was read without
	// materializing an agent, so there is no meter for it (nil no-op).
	title := generateTitle(ctx, cl, m, seed, live)
	if title == "" {
		return "", ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", i18n.T("the model returned no title"))
	}
	if live != nil {
		live.applyTitle(title)
		return title, nil
	}
	if err := core.RenameSessionGenerated(w.sessionPath(sess), title); err != nil {
		return "", ctrlproto.Errorf(ctrlproto.CodeInternal, "persist title: %v", err)
	}
	return title, nil
}

func (w *Workspace) DeleteSession(ctx context.Context, sess string) error {
	if !validSessionID(sess) {
		return ctrlproto.ErrNoSession
	}
	w.mu.Lock()
	s, existed := w.sessions[sess]
	if existed {
		s.close() // closing an empty fresh session may itself prune the file
		delete(w.sessions, sess)
	}
	w.mu.Unlock()
	err := os.Remove(w.sessionPath(sess))
	if err != nil && !os.IsNotExist(err) {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "delete: %v", err)
	}
	// Deleting a session deletes its failure record too: the error sidecar
	// is that session's data, and leaving it would orphan a file no scan
	// lists (sidecars are filtered from session listings). Best-effort —
	// most sessions never had one.
	if sc := core.ErrorLogPathFor(w.sessionPath(sess)); sc != "" {
		_ = os.Remove(sc)
	}
	// A missing file is only "not found" when we never knew the session; if it
	// was live, close() legitimately pruned an empty transcript.
	if os.IsNotExist(err) && !existed {
		return ctrlproto.ErrNoSession
	}
	// A session left the set; boards re-list to prune its tile.
	w.BroadcastAll(ctrlproto.SessionsChangedEvent())
	return nil
}

var _ ctrlproto.DraftController = (*Workspace)(nil)

// DiscardDraft removes an UNPROMOTED draft — a Stage session whose greeting is
// still deferred (opened for preview, never sent into) — reclaiming its live
// session and any extension subprocesses the moment the user navigates away,
// rather than leaving it live until shutdown. A guarded no-op on anything else
// (a promoted chat, a coding session), keyed on the pending-greeting signal, so
// the front end can call it freely on back-out without risking real work. It
// mirrors DeleteSession's close+remove+broadcast.
func (w *Workspace) DiscardDraft(ctx context.Context, sess string) error {
	if !validSessionID(sess) {
		return nil
	}
	w.mu.Lock()
	s, existed := w.sessions[sess]
	if !existed || !s.sess.HasPendingGreeting() {
		w.mu.Unlock()
		return nil // not a live unpromoted draft — keep it
	}
	s.close() // an empty fresh session's close prunes its meta-only file
	delete(w.sessions, sess)
	w.mu.Unlock()
	_ = os.Remove(w.sessionPath(sess)) // best-effort straggler cleanup
	if sc := core.ErrorLogPathFor(w.sessionPath(sess)); sc != "" {
		_ = os.Remove(sc)
	}
	w.BroadcastAll(ctrlproto.SessionsChangedEvent())
	return nil
}

func (w *Workspace) Usage(ctx context.Context, sess string) (core.WireUsage, error) {
	if s := w.existing(sess); s != nil {
		return toCtrlUsage(s.agent.Cost()), nil
	}
	if !validSessionID(sess) {
		return core.WireUsage{}, ctrlproto.ErrNoSession
	}
	path := w.sessionPath(sess)
	if _, err := os.Stat(path); err != nil {
		return core.WireUsage{}, ctrlproto.ErrNoSession
	}
	cum, _, err := core.SessionUsageDetail(path)
	if err != nil {
		return core.WireUsage{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "usage: %v", err)
	}
	return toCtrlUsage(cum), nil
}

// UsageSnapshot reports the provider's subscription/credit picture. It reads
// the LIVE session only — the snapshot hangs off the provider client, so a
// session that has not been materialized has nothing to report, and asking is
// not worth building an agent for. That is HasData=false, not an error.
//
// refresh=true blocks on the provider's usage endpoint (ClientRefreshUsage);
// the caller is responsible for keeping that off a UI goroutine, and the
// verb's context bounds it.
func (w *Workspace) UsageSnapshot(ctx context.Context, sess string, refresh bool) (ctrlproto.UsageInfo, error) {
	s := w.existing(sess)
	if s == nil || s.agent == nil {
		return ctrlproto.UsageInfo{}, nil
	}
	ag := s.agent
	snap, ok := ag.Usage()
	if refresh {
		snap, ok = ag.RefreshUsage(ctx)
	}
	return usageInfo(snap, ok, ag.UsageRefreshable()), nil
}

// ListResets reports the provider's usage-reset credits for the LIVE session.
// Like UsageSnapshot, an unmaterialized session (or a provider with no reset
// support) is Supported=false, not an error. It blocks on the provider's
// endpoint; the verb's context bounds it.
func (w *Workspace) ListResets(ctx context.Context, sess string) (ctrlproto.ResetsListResult, error) {
	s := w.existing(sess)
	if s == nil || s.agent == nil || !s.agent.SupportsResets() {
		return ctrlproto.ResetsListResult{}, nil
	}
	resets, err := s.agent.ListResets(ctx)
	if err != nil {
		return ctrlproto.ResetsListResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "list resets: %v", err)
	}
	return ctrlproto.ResetsListResult{Supported: true, Resets: resetInfos(resets)}, nil
}

// ConsumeReset redeems a reset credit on the LIVE session's provider. The
// caller (TUI/panel) has already confirmed with the user; this method performs
// the irreversible spend. An unmaterialized session or unsupported provider is
// a clean CodeUnsupported rather than a silent success.
func (w *Workspace) ConsumeReset(ctx context.Context, sess, id string) (ctrlproto.ResetConsumeResult, error) {
	s := w.existing(sess)
	if s == nil || s.agent == nil || !s.agent.SupportsResets() {
		return ctrlproto.ResetConsumeResult{}, ctrlproto.Errorf(ctrlproto.CodeUnsupported, "%s", i18n.T("provider does not support usage resets"))
	}
	res, err := s.agent.ConsumeReset(ctx, id)
	if err != nil {
		return ctrlproto.ResetConsumeResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "consume reset: %v", err)
	}
	// A redemption cleared the provider's windows, so the cached usage snapshot
	// is now stale; the next usage.snapshot refresh (or turn) repopulates it.
	// Broadcast a session_updated so open clients re-pull promptly.
	s.broadcast(ctrlproto.SessionUpdatedEvent(s.info()))
	return ctrlproto.ResetConsumeResult{Reset: resetInfo(res.Reset), WindowsReset: res.WindowsReset}, nil
}

// --- control group (WorkspaceService) ---

// restartDrainTimeout bounds how long Restart waits for cancelled turns to
// unwind and persist before it replaces the process image. A var so a test can
// shorten it. It only ever fully elapses if a turn ignores cancellation; the
// common case (idle, or a turn that stops promptly) returns well under it.
var restartDrainTimeout = 3 * time.Second

// Restart re-execs the daemon into the currently-installed binary (Tier-1
// self-restart). It first cancels any in-flight turn and waits, bounded, for it
// to unwind and persist — the graceful contract documented in docs/tui.md and
// docs/controllers.md — then relaunch runs the pre-exec notice hook and replaces
// the process image after a short flush delay. Gated by relaunch.Enabled() (set
// from --allow-restart; web mode adds an insecure-listener refusal in
// runWebMode), so it reports CodeUnsupported when off.
func (w *Workspace) Restart(ctx context.Context) error {
	// Ask BEFORE destroying anything. The drain below cancels every in-flight
	// turn, and relaunch can still refuse afterwards — an unsupported platform,
	// a `go run`/debug binary, a failed executable capture, a restart already
	// pending. A refusal that lands after the drain has thrown the user's work
	// away for a restart that never happens, so the refusal has to come first.
	if err := relaunch.CanTrigger(); err != nil {
		if errors.Is(err, relaunch.ErrDisabled) {
			return ctrlproto.Errorf(ctrlproto.CodeUnsupported, "%s", i18n.T("self-restart is not enabled (start with --allow-restart)"))
		}
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "restart: %v", err)
	}
	// The image is about to be replaced, killing every in-flight turn regardless.
	// Cancel them first and give them a bounded window to stop their tools and
	// let endTurn persist, so a restart during streaming or a mutating tool call
	// finalizes cleanly rather than being killed mid-step. Session history
	// persists incrementally, so already-recorded events are safe regardless.
	w.cancelAndDrainTurns(ctx, restartDrainTimeout)
	if err := relaunch.Trigger("control-plane request"); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "restart: %v", err)
	}
	return nil
}

// cancelAndDrainTurns cancels every live session's in-flight turn and waits,
// bounded, for them all to go idle. Returns when all cancelled turns have
// reached endTurn (turnCancel cleared), the budget elapses, or ctx is done — a
// turn wedged past the budget is abandoned rather than blocking the restart
// forever; already-persisted history is still safe. No-op when nothing is
// running.
func (w *Workspace) cancelAndDrainTurns(ctx context.Context, timeout time.Duration) {
	w.mu.Lock()
	all := make([]*wsSession, 0, len(w.sessions))
	for _, s := range w.sessions {
		all = append(all, s)
	}
	w.mu.Unlock()

	var pending []*wsSession
	for _, s := range all {
		if s.busy() {
			s.cancelTurn()
			pending = append(pending, s)
		}
	}
	if len(pending) == 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for {
		allIdle := true
		for _, s := range pending {
			if s.busy() {
				allIdle = false
				break
			}
		}
		if allIdle || !time.Now().Before(deadline) || ctx.Err() != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (w *Workspace) Models(ctx context.Context, sess string) ([]ctrlproto.ModelInfo, error) {
	// Current reflects the FRAMED session's own model, so the picker shows the
	// model of the session the client is viewing rather than a workspace-global
	// last-switched value. existing() has no side effects — unlike resolve, which
	// would materialize a cold session or create one for an empty id — so a
	// read-only model list never mutates the live set. With no live session in
	// frame (startup, pre-first-session, a session-less caller) we fall back to
	// the workspace default, which is the right "what a new session starts on".
	var curProv, curModel string
	if s := w.existing(sess); s != nil {
		curProv, curModel = s.currentModel()
	} else {
		w.mu.Lock()
		curProv, curModel = w.provider, w.model
		w.mu.Unlock()
	}
	authed := build.LoggedInProviderSet()
	// How each provider authenticates, so the picker can tell a subscription-backed
	// row from a metered one when both offer the same model id. Cheap beside the
	// membership sweep above: an unexpired OAuth token short-circuits its refresh.
	authMethod := build.LoggedInProviderAuth()
	favs := favoriteModelSet()
	defProv, defModel, defScope := w.defaultModel()
	var out []ctrlproto.ModelInfo
	for _, m := range provider.Active() {
		if !authed[m.Provider] {
			continue
		}
		info := ctrlproto.ModelInfo{
			ID:            m.ID,
			Provider:      m.Provider,
			ContextWindow: m.ContextWindow,
			MaxOutput:     m.MaxOutput,
			Reasoning:     m.Reasoning,
			Current:       m.ID == curModel && m.Provider == curProv,
			Favorite:      favs[favModelKey(m.Provider, m.ID)],
			Auth:          authMethod[m.Provider],
		}
		if m.ID == defModel && m.Provider == defProv {
			info.Default, info.DefaultScope = true, defScope
		}
		out = append(out, info)
	}
	return out, nil
}

// favModelKey is the "provider/id" key format the config's FavoriteModels list
// uses (shared with the TUI's ★ favorites view).
func favModelKey(provider, model string) string { return provider + "/" + model }

func favoriteModelSet() map[string]bool {
	cfg, _ := config.LoadConfig()
	out := make(map[string]bool, len(cfg.FavoriteModels))
	for _, k := range cfg.FavoriteModels {
		out[k] = true
	}
	return out
}

// SetFavoriteModel pins/unpins a model in the user's favorites, persisted to
// config — the same list the TUI's ★ favorites view reads.
func (w *Workspace) SetFavoriteModel(ctx context.Context, provider, model string, on bool) error {
	if err := config.MutateConfig(func(c *config.Config) {
		c.FavoriteModels = config.ToggleStringMember(c.FavoriteModels, favModelKey(provider, model), on)
	}); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
	}
	return nil
}

// SetDefaultModel persists provider+model as the default for NEW sessions. It
// deliberately leaves every live session alone — SwitchModel is the live one,
// and trying a model is not the same as adopting it.
//
// This is the wire half of the TUI model picker's ctrl+d, and it is the reason
// that logic lives here rather than in the TUI's config closure: an attach-mode
// TUI is a ctrlproto client, so before this existed its ctrl+d had nowhere to
// land and did nothing.
func (w *Workspace) SetDefaultModel(ctx context.Context, provider, model string, scope ctrlproto.DefaultScope) error {
	if provider == "" || model == "" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("provider and model are both required"))
	}
	switch scope {
	case ctrlproto.ScopeGlobal:
		if err := config.MutateConfig(func(c *config.Config) {
			c.Provider, c.Model = provider, model
		}); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save config: %v", err)
		}
	case ctrlproto.ScopeProject:
		if err := config.SetProjectModel(w.CWD(), provider, model); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save project config: %v", err)
		}
	default:
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("unknown model-default scope %q", scope))
	}
	return nil
}

// defaultModel reports which model new sessions start on, and which scope said
// so. A project default shadows the global one — the same precedence config
// resolution applies — and it only counts while the workspace is trusted, so an
// untrusted project default is not advertised as in force when it is not.
func (w *Workspace) defaultModel() (provider, model string, scope ctrlproto.DefaultScope) {
	if w.Trusted() {
		// LoadProjectConfig returns (nil, nil) when the workspace has no project
		// config at all — the common case, and not an error.
		if pc, err := config.LoadProjectConfig(w.CWD()); err == nil && pc != nil && pc.Provider != "" && pc.Model != "" {
			return pc.Provider, pc.Model, ctrlproto.ScopeProject
		}
	}
	cfg, _ := config.LoadConfig()
	if cfg.Provider == "" || cfg.Model == "" {
		return "", "", ""
	}
	return cfg.Provider, cfg.Model, ctrlproto.ScopeGlobal
}

// Trust grants Workspace Trust to the workspace's cwd (parent = trust
// descendant directories too), persists the verdict, and brings project content
// live for every open session (extensions reload, tool sets rebuild, lore
// re-discovers). Project skills/context baked into the system prompt take effect
// on the next session. Idempotent — re-trusting just refreshes.
func (w *Workspace) Trust(ctx context.Context, parent bool) error {
	if err := config.TrustPath(w.cwd, parent); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "trust: %v", err)
	}
	w.applyTrust(ctx, true)
	return nil
}

// Untrust removes the workspace's cwd from the trust store and tears project
// content back down for every open session — the symmetric inverse of Trust.
func (w *Workspace) Untrust(ctx context.Context) error {
	if err := config.UntrustPath(w.cwd); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "untrust: %v", err)
	}
	w.applyTrust(ctx, false)
	return nil
}

// applyTrust brings every open session in line with a new trust verdict. Trust
// is workspace-global (keyed on w.cwd), so one verdict fans out to all sessions.
func (w *Workspace) applyTrust(ctx context.Context, trusted bool) {
	w.mu.Lock()
	sess := make([]*wsSession, 0, len(w.sessions))
	for _, s := range w.sessions {
		sess = append(sess, s)
	}
	w.mu.Unlock()
	for _, s := range sess {
		s.setTrusted(ctx, trusted)
	}
}

func (w *Workspace) SwitchModel(ctx context.Context, sess, providerName, modelID string) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	return w.switchModel(s, providerName, modelID, false)
}

// overrideClient builds a provider.Client + resolved model id for an optional
// per-generation model override (Phase 7 — per-generation model routing). An
// empty modelID resolves `base` as-is — the caller's default (workspace args for
// the card doctor, the session's args for suggest). A non-empty modelID names a
// specific model to run this ONE generation on, provider-qualified (same
// rationale as CreateOpts.Provider — a bare id resolves against base's provider
// first to avoid a silent provider hop), rebuilt as a fresh client that drops any
// launch-time key/URL pinning (mirroring switchModel's rebuild path). It does NOT
// touch the session — the override is ephemeral, scoped to the one call. An
// unknown model or one with no credential is a clean CodeBadRequest up front, not
// a deferred failure mid-stream.
func (w *Workspace) overrideClient(base build.Args, providerName, modelID string) (provider.Client, string, error) {
	next := base
	if strings.TrimSpace(modelID) != "" {
		target, err := provider.FindModel(providerName, modelID)
		if providerName == "" && strings.TrimSpace(base.Provider) != "" {
			if m, e := provider.FindModel(base.Provider, modelID); e == nil {
				target, err = m, nil
			}
		}
		if err != nil {
			return nil, "", ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("unknown model %q", modelID))
		}
		next.Provider = target.Provider
		next.Model = target.ID
		next.APIKey = ""
		next.BaseURL = ""
	}
	r, err := build.Resolve(next, true)
	if err != nil || !r.HasCredential() {
		if strings.TrimSpace(modelID) != "" {
			return nil, "", ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("no usable credential for model %q", modelID))
		}
		return nil, "", ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", i18n.T("no usable credential"))
	}
	return r.NewClient(), r.Model, nil
}

// RefreshSessionCredential rebuilds a live session's provider client from a
// freshly-resolved credential, keeping its provider/model/transcript — the
// carrier twin of the legacy login rebuild (BuildAgent + SetAgent). Without
// it a re-login (expired token) leaves the session's agent holding the dead
// client until a cross-provider /model swap happens to rebuild it.
func (w *Workspace) RefreshSessionCredential(ctx context.Context, sess string) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	prov, model := s.currentModel()
	return w.switchModel(s, prov, model, true)
}

// switchReusesClient reports whether a model switch may swap the id on the live
// client instead of rebuilding it.
//
// Reuse is only sound when the resolved endpoint does not change. A per-model
// models.json baseUrl can route two models of the SAME provider to different
// backends (one on a gateway, another local), and a provider client captures its
// base URL immutably at construction — so swapping the id in place would keep
// firing requests at, and reporting, the previous model's endpoint. That is the
// wrong-backend bug; a differing BaseURL must fall through to a full rebuild.
//
// forceRebuild (a rescue retry or a re-login) always rebuilds, so a stale auth
// header or base URL can never carry over. An unresolvable current model
// (curErr) also rebuilds: we cannot prove the endpoint is unchanged.
func switchReusesClient(curProv string, cur provider.Model, curErr error, target provider.Model, forceRebuild bool) bool {
	return !forceRebuild && curErr == nil && curProv == target.Provider && cur.BaseURL == target.BaseURL
}

// switchModel mirrors acpFactory.SwitchModel: same provider+endpoint swaps the
// id in place; anything else builds a fresh client (dropping launch-time key/URL
// overrides that pin the old endpoint) and hot-swaps client+model, keeping the
// transcript. The new default becomes the workspace default for future sessions.
// providerName qualifies the id (same rationale as CreateOpts.Provider).
// forceRebuild skips the same-endpoint id-swap shortcut so a re-login can
// replace the client even when nothing else changed.
//
// A bare id (empty providerName) resolves against the CURRENT provider first:
// several ids exist under both an api-key provider and a subscription one
// (openai's gpt-5.5 precedes openai-codex's in the catalog), and a global
// first-match would silently hop providers — onto one the user may hold no
// credential for — when they meant "same backend, different model".
func (w *Workspace) switchModel(s *wsSession, providerName, modelID string, forceRebuild bool) error {
	curProv, curModel := s.currentModel()
	target, err := provider.FindModel(providerName, modelID)
	if providerName == "" && curProv != "" {
		if m, err2 := provider.FindModel(curProv, modelID); err2 == nil {
			target, err = m, nil
		}
	}
	if err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("unknown model %q", modelID))
	}
	cur, curErr := provider.FindModel(curProv, curModel)
	if switchReusesClient(curProv, cur, curErr, target, forceRebuild) {
		s.agent.SetModel(target.ID)
		s.setModel(target.Provider, target.ID)
	} else {
		next := w.args
		next.Provider = target.Provider
		next.Model = target.ID
		next.APIKey = ""
		next.BaseURL = ""
		r, rerr := build.Resolve(next, true)
		if rerr != nil {
			return ctrlproto.Errorf(ctrlproto.CodeUnauthorized, "%v", rerr)
		}
		if !r.HasCredential() {
			return ctrlproto.Errorf(ctrlproto.CodeUnauthorized, "%s", i18n.T("no credential for provider %q", r.Provider))
		}
		nc := r.NewClient()
		// Carry the passively-observed usage snapshot across the rebuild
		// (same-provider swaps only; the seeder rejects foreign snapshots).
		// Without this a re-login or endpoint change blanks the status-bar
		// meters until the next turn's headers arrive.
		if snap, ok := s.agent.Usage(); ok {
			provider.SeedClientUsage(nc, snap)
		}
		s.agent.SetClientAndModel(nc, r.Model)
		s.setModel(r.Provider, r.Model)
		// SetClientAndModel keeps the agent's registry, so terva_status
		// still carries the OLD provider identity: re-bind it to the target
		// provider/auth/endpoint. Without this the tool reports the prior
		// provider and — because FindModel(oldProvider, newModel) misses —
		// loses the context-window size after the swap.
		if st, ok := s.agent.LookupTool("terva_status"); ok {
			if stt, ok := st.(*tools.StatusTool); ok {
				stt.SetProvider(r.Provider, r.AuthMethod, r.BaseURL)
			}
		}
	}
	prov, model := s.currentModel()
	// A mid-session model swap must refresh every host-routed dispatch tool
	// (swarm_spawn, ...) so a sub-agent spawned afterward inherits the
	// CURRENT provider/model and resolves tiers against it, not the stale
	// pre-swap route. Generic over HostRouted so this covers both the
	// same-endpoint id-swap and the rebuild path, and any future such tool.
	for _, tl := range s.agent.ToolsSnapshot() {
		if hr, ok := tl.(tools.HostRouted); ok {
			hr.SetHost(prov, model)
		}
	}
	_ = s.sess.UpdateModel(prov, model)
	// A per-session switch changes ONLY this session; it must not move the
	// workspace default new sessions inherit — that is models.set_default's job
	// (SetDefaultModel writes config). Before Stage 2 this wrote w.provider/
	// w.model here, so trying a model in one chat silently became the default
	// every later new chat opened on. See createLocked, which now seeds from the
	// configured default rather than this workspace-global pair.
	s.broadcast(ctrlproto.SessionUpdatedEvent(s.info()))
	return nil
}

// titleFromFirstText derives a fallback nickname from a session's first user
// message when it has no explicit title.
func titleFromFirstText(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	const max = 60
	if len(s) > max {
		s = strings.TrimSpace(s[:max]) + "…"
	}
	return s
}

// titleGen reports whether LLM auto-titling is enabled (config AutoTitle) and,
// if so, returns a fresh client + model to generate with. Any resolution
// failure disables it for this call (the first-line fallback title already
// stands). The on-demand verb (GenerateSessionTitle) skips this gate — the
// toggle governs spending tokens unasked, not an explicit request.
func (w *Workspace) titleGen(s *wsSession) (bool, provider.Client, string) {
	cfg, _ := config.LoadConfig()
	if cfg.AutoTitle == nil || !*cfg.AutoTitle {
		return false, nil, ""
	}
	prov, model := s.currentModel()
	return w.titleClient(prov, model)
}

// titleClient resolves a fresh client + model to generate a title against the
// given session provider/model. It resolves its own client rather than
// borrowing the session's — so it never races a concurrent model switch
// touching the live agent. AutoTitleModel overrides the model. The launch
// flags' APIKey/BaseURL apply only when the title provider IS the launch
// provider — they'd misdirect any other provider (and stripping them
// unconditionally used to break titling for custom-endpoint sessions).
func (w *Workspace) titleClient(prov, model string) (bool, provider.Client, string) {
	cfg, _ := config.LoadConfig()
	if cfg.AutoTitleModel != "" {
		if t, err := provider.FindModel("", cfg.AutoTitleModel); err == nil {
			prov, model = t.Provider, t.ID
		}
	}
	next := w.args
	next.Provider = prov
	next.Model = model
	if prov != w.args.Provider {
		next.APIKey = ""
		next.BaseURL = ""
	}
	r, err := build.Resolve(next, true)
	if err != nil || !r.HasCredential() {
		return false, nil, ""
	}
	return true, r.NewClient(), r.Model
}

const titleSystem = "You write concise, specific titles for chat sessions. Reply with only the title — at most six words, no surrounding quotes, no trailing punctuation."

// generateTitle asks the model for a short title from a pre-budgeted,
// self-labeled seed (core.BuildTitleSeed — the old head-only byte slice here
// could split a UTF-8 sequence; the builder clips rune-safely). Best-effort:
// any error yields "" and the caller keeps whatever title stands.
//
// The spend is booked against s. A title call is a side-channel completion
// like any other, and this drain predated streamText's usage contract, so it
// dropped EventUsage on the floor. Passing the session here rather than
// returning the usage makes booking structural: a caller with no session
// meter — GenerateSessionTitle's cold path — passes nil (safe no-op), which
// is the deliberate, visible form of not booking.
func generateTitle(ctx context.Context, cl provider.Client, model, seed string, s *wsSession) string {
	req := provider.Request{
		Model:     model,
		System:    i18n.P("title.system", titleSystem),
		MaxTokens: 24,
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: i18n.P("title.instruction", "Title this chat.") + "\n\n" + seed}},
			Time:    time.Now(),
		}},
	}
	out, usage, err := streamText(ctx, cl, req)
	s.recordSideChannelUsage(usage)
	if err != nil {
		return ""
	}
	return cleanTitle(out)
}

// cleanTitle normalizes a model-produced title: one line, no wrapping quotes,
// no trailing punctuation, length-capped like the fallback.
func cleanTitle(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	s = strings.Trim(s, "\"'`")
	s = strings.TrimRight(s, ".!,;: ")
	const max = 60
	if len(s) > max {
		s = strings.TrimSpace(s[:max]) + "…"
	}
	return strings.TrimSpace(s)
}

// CWD is the workspace's working directory. Exposed as a method rather than a
// field so the composition root can read it without the struct's internals
// becoming part of the package's API — the same shape as Trusted.
func (w *Workspace) CWD() string { return w.cwd }

// PersonaName is the persona this workspace booted with ("" when none).
func (w *Workspace) PersonaName() string { return w.personaName }
