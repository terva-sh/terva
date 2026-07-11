package workspace

import (
	"context"
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
	"terva.sh/terva/packages/core"
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
	w.personaName = r.Persona.Name
	// Workspace-global swarm for the tasks pane. Construction does no I/O;
	// Reload pulls previously-spawned agents off disk (shown as detached, like
	// the TUI). Close drains running children gracefully (StopAllAndWait) so
	// their durable state survives for the next launch's Reload/Resume.
	swarmCfg := swarm.Config{Root: filepath.Join(config.TervaHome(), "swarm"), RepoRoot: r.CWD}
	// --swarm-worktrees: lease each sub-agent its own git worktree via the
	// terva-git-worktree extension. The hook resolves a live session's manager
	// lazily (the swarm is workspace-global, extensions are per-session), so it
	// works once a session with the extension exists. Off => shared tree.
	if uc, _ := config.LoadConfig(); build.ResolveSwarmWorktrees(args.SwarmWorktrees, uc.SwarmWorktrees) {
		swarmCfg.AcquireWorktree = w.acquireSwarmWorktree
	}
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
	defer w.mu.Unlock()
	return w.sessionLocked(id)
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
	// Resume with the same compact window the legacy TUI uses: the live agent
	// gets the recent tail (plus any compaction summary), not the whole
	// multi-thousand-message history — which would otherwise ride every
	// subsequent request. The session FILE stays intact, and the per-message
	// persistence hooks only append new rows, so nothing is lost on disk.
	msgs = build.TrimMessagesForResume(msgs, 100)
	s, err := w.buildSession(id, sess, msgs, "")
	if err != nil {
		return nil, err
	}
	w.sessions[id] = s
	return s, nil
}

func (w *Workspace) createLocked(opts ctrlproto.CreateOpts) (*wsSession, error) {
	prov, model := w.provider, w.model
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
			return nil, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "unknown persona %q: %v", opts.Persona, err)
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
	id := build.SessionIDFromPath(sess.Path)
	s, err := w.buildSession(id, sess, nil, opts.Persona)
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
	s, err := w.resolve(sess)
	if err != nil {
		return nil, err
	}
	return s.subscribe(ctx, false), nil
}

// SubscribeReliable is Subscribe with no-drop delivery, for the in-process
// carrier (the TUI). It is deliberately NOT part of ctrlproto.WorkspaceService:
// a networked carrier can never promise unbounded delivery, so reliability is an
// in-process-only affordance the TUI reaches for on the concrete *Workspace. The
// events are identical to Subscribe's — only the backpressure discipline differs
// (the consumer must keep draining; a stall applies contained same-session
// backpressure rather than silently dropping a text delta).
func (w *Workspace) SubscribeReliable(ctx context.Context, sess string) (<-chan ctrlproto.Event, error) {
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
		id := build.SessionIDFromPath(sm.Path)
		title := sm.Title
		if title == "" {
			title = titleFromFirstText(sm.FirstUserText)
		}
		info := ctrlproto.SessionInfo{
			ID:       id,
			Title:    title,
			Provider: sm.Provider,
			Model:    sm.Model,
			Path:     sm.Path,
			Created:  ctrlTimeString(sm.Started),
			Messages: sm.MessageCount,
			Usage:    core.WireUsage{CostUSD: sm.TotalCost},
			Current:  id == defID,
			Trusted:  wsTrusted,
		}
		if fi, e := os.Stat(sm.Path); e == nil {
			info.Updated = ctrlTimeString(fi.ModTime())
		}
		if s, ok := w.sessions[id]; ok { // live overrides for an open session
			info.Provider, info.Model = s.currentModel()
			info.Persona = s.personaName()
			info.Usage = toCtrlUsage(s.agent.Cost())
			info.Trusted = s.trusted.Load()
		}
		out = append(out, info)
	}
	return out, nil
}

func (w *Workspace) CreateSession(ctx context.Context, opts ctrlproto.CreateOpts) (ctrlproto.SessionInfo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, err := w.createLocked(opts)
	if err != nil {
		return ctrlproto.SessionInfo{}, err
	}
	return s.info(), nil
}

func (w *Workspace) ResumeSession(ctx context.Context, sess string) (ctrlproto.SessionInfo, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return ctrlproto.SessionInfo{}, err
	}
	return s.info(), nil
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
		s.setTitle(title)
		s.broadcast(ctrlproto.SessionUpdatedEvent(s.info()))
	}
	return nil
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
		return ctrlproto.ResetConsumeResult{}, ctrlproto.Errorf(ctrlproto.CodeUnsupported, "provider does not support usage resets")
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

// Restart re-execs the daemon into the currently-installed binary (Tier-1
// self-restart). It acks immediately; relaunch runs the pre-exec notice hook
// and then replaces the process image after a short flush delay. Gated by
// relaunch.Enabled() (set from --web-allow-restart, after the insecure-listener
// refusal in runWebMode), so it reports CodeUnsupported when off.
func (w *Workspace) Restart(ctx context.Context) error {
	if !relaunch.Enabled() {
		return ctrlproto.Errorf(ctrlproto.CodeUnsupported, "self-restart is not enabled (start with --web-allow-restart)")
	}
	if err := relaunch.Trigger("control-plane request"); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "restart: %v", err)
	}
	return nil
}

func (w *Workspace) Models(ctx context.Context) ([]ctrlproto.ModelInfo, error) {
	w.mu.Lock()
	curProv, curModel := w.provider, w.model
	w.mu.Unlock()
	authed := webLoggedInProviders()
	favs := favoriteModelSet()
	var out []ctrlproto.ModelInfo
	for _, m := range provider.Active() {
		if !authed[m.Provider] {
			continue
		}
		out = append(out, ctrlproto.ModelInfo{
			ID:            m.ID,
			Provider:      m.Provider,
			ContextWindow: m.ContextWindow,
			MaxOutput:     m.MaxOutput,
			Reasoning:     m.Reasoning,
			Current:       m.ID == curModel && m.Provider == curProv,
			Favorite:      favs[favModelKey(m.Provider, m.ID)],
		})
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

// webLoggedInProviders returns the providers the user can actually reach: those
// with a resolvable credential, plus ollama (no auth) and openai-compatible
// (once a base URL is configured). Mirrors acpLoggedInProviders so the web model
// menu — like ACP's — is scoped to authenticated providers instead of the whole
// catalog. SwitchModel still guards the switch, so a stale entry fails cleanly.
func webLoggedInProviders() map[string]bool {
	out := map[string]bool{}
	for _, p := range build.KnownProviders {
		if _, _, err := build.ResolveCredential(p, ""); err == nil {
			out[p] = true
		}
	}
	out["ollama"] = true
	if !out["openai-compatible"] {
		if bu, _, _ := config.AuthStoreFor().Extras("openai-compatible"); bu != "" {
			out["openai-compatible"] = true
		}
	}
	return out
}

func (w *Workspace) SwitchModel(ctx context.Context, sess, providerName, modelID string) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	return w.switchModel(s, providerName, modelID, false)
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
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "unknown model %q", modelID)
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
			return ctrlproto.Errorf(ctrlproto.CodeUnauthorized, "no credential for provider %q", r.Provider)
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
	w.mu.Lock()
	w.provider, w.model = prov, model
	w.mu.Unlock()
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
// if so, returns a fresh client + model to generate with. It resolves its own
// client rather than borrowing the session's — so it never races a concurrent
// model switch touching the live agent. AutoTitleModel overrides the model;
// empty uses the session's current one. Any resolution failure disables it for
// this call (the first-line fallback title already stands).
func (w *Workspace) titleGen(s *wsSession) (bool, provider.Client, string) {
	cfg, _ := config.LoadConfig()
	if cfg.AutoTitle == nil || !*cfg.AutoTitle {
		return false, nil, ""
	}
	prov, model := s.currentModel()
	if cfg.AutoTitleModel != "" {
		if t, err := provider.FindModel("", cfg.AutoTitleModel); err == nil {
			prov, model = t.Provider, t.ID
		}
	}
	next := w.args
	next.Provider = prov
	next.Model = model
	next.APIKey = ""
	next.BaseURL = ""
	r, err := build.Resolve(next, true)
	if err != nil || !r.HasCredential() {
		return false, nil, ""
	}
	return true, r.NewClient(), r.Model
}

const titleSystem = "You write concise, specific titles for chat sessions. Reply with only the title — at most six words, no surrounding quotes, no trailing punctuation."

// generateTitle asks the model for a short title seeded by the first message.
// Best-effort: any error yields "" and the caller keeps the fallback title.
func generateTitle(ctx context.Context, cl provider.Client, model, seed string) string {
	const maxSeed = 2000
	if len(seed) > maxSeed {
		seed = seed[:maxSeed]
	}
	req := provider.Request{
		Model:     model,
		System:    titleSystem,
		MaxTokens: 24,
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: "Title this chat, which opens with:\n\n" + seed}},
			Time:    time.Now(),
		}},
	}
	stream, err := cl.Stream(ctx, req)
	if err != nil {
		return ""
	}
	var sb strings.Builder
	for ev := range stream {
		switch e := ev.(type) {
		case provider.EventTextDelta:
			sb.WriteString(e.Delta)
		case provider.EventDone:
			if e.Err != nil {
				return ""
			}
		}
	}
	return cleanTitle(sb.String())
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
