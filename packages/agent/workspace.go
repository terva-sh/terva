package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	args     Args
	version  string
	root     string // $TERVA_HOME
	cwd      string
	provider string // default provider for new sessions
	model    string // default model for new sessions
	hookEng  *hooks.Engine

	ctx    context.Context
	cancel context.CancelFunc

	swarm *swarm.Swarm // workspace-global background agents (the tasks pane)

	// Workspace-global MCP servers: started once for the daemon (subprocesses are
	// expensive to respawn per session) and merged into every session's agent.
	// nil when --no-mcp or none configured. mcpStop tears them down in Close.
	mcpAdapter *mcpToolAdapter
	mcpStop    func()
	mcpMu      sync.Mutex // serializes live MCP toggles (StartOne/StopOne want one caller)

	mu       sync.Mutex
	sessions map[string]*wsSession

	// diag receives host-side session-build diagnostics (permission-policy
	// warnings, extension-load results). It defaults to a line on os.Stderr,
	// which is right for the web/ACP daemons, but the in-process TUI carrier
	// overrides it via SetDiag — a stray stderr write would corrupt the
	// full-screen alternate-screen UI. Always non-nil after NewWorkspace.
	diag func(string)
}

var _ ctrlproto.WorkspaceService = (*Workspace)(nil)

// NewWorkspace builds a Workspace from resolved args. It requires a credential
// (a daemon cannot run turns without one) and captures the default
// provider/model for new sessions.
func NewWorkspace(args Args, version string) (*Workspace, error) {
	r, err := Resolve(args, true)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &Workspace{
		args:     args,
		version:  version,
		root:     TervaHome(),
		cwd:      r.CWD,
		provider: r.Provider,
		model:    r.Model,
		hookEng:  buildHookEngine(args, r.Trusted),
		ctx:      ctx,
		cancel:   cancel,
		sessions: map[string]*wsSession{},
		diag:     func(m string) { fmt.Fprintln(os.Stderr, m) },
	}
	// Workspace-global swarm for the tasks pane. Construction does no I/O;
	// Reload pulls previously-spawned agents off disk (shown as detached, like
	// the TUI). Daemons persist across restarts, so Close does not stop them.
	w.swarm = swarm.New(swarm.Config{Root: filepath.Join(TervaHome(), "swarm"), RepoRoot: r.CWD})
	_, _ = w.swarm.Reload()
	go w.pollTasks()

	// Start the configured MCP servers once for the whole daemon; their tools are
	// merged into each session's agent in buildSession. setupMCP also merges into
	// this (throwaway) r — harmless; the per-session merge is what counts.
	w.mcpAdapter, w.mcpStop = setupMCP(w.ctx, args, &r)

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

// Close cancels in-flight turns, closes open session files, and releases the
// hook engine. Idempotent.
func (w *Workspace) Close() error {
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

// existing returns an already-materialized session for id, or nil. Used by
// Cancel/Approve/Answer/Queue, which are no-ops on a session with no live
// agent (there can be no pending turn or request).
func (w *Workspace) existing(id string) *wsSession {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sessions[id]
}

func (w *Workspace) sessionLocked(id string) (*wsSession, error) {
	if id == "" {
		if p := core.LatestSession(w.root, w.cwd); p != "" {
			id = sessionIDFromPath(p)
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
	msgs = trimMessagesForResume(msgs, 100)
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
		if _, err := ResolvePersona(opts.Persona); err != nil {
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
	id := sessionIDFromPath(sess.Path)
	s, err := w.buildSession(id, sess, nil, opts.Persona)
	if err != nil {
		return nil, err
	}
	w.sessions[id] = s
	return s, nil
}

// --- conversation group (WorkspaceService) ---

func (w *Workspace) Prompt(ctx context.Context, sess, text string, images []ctrlproto.Image) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	return s.prompt(text, images)
}

func (w *Workspace) Queue(ctx context.Context, sess, text string) error {
	if s := w.existing(sess); s != nil {
		s.queue(text)
	}
	return nil
}

func (w *Workspace) SetQueue(ctx context.Context, sess string, texts []string) error {
	if s := w.existing(sess); s != nil {
		s.setQueue(texts)
	}
	return nil
}

func (w *Workspace) Cancel(ctx context.Context, sess string) error {
	if s := w.existing(sess); s != nil {
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
	if s := w.existing(sess); s != nil {
		s.approve(callID, d)
	}
	return nil
}

func (w *Workspace) Answer(ctx context.Context, sess, askID string, a core.UserAnswer) error {
	if s := w.existing(sess); s != nil {
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

// AgentFor returns the in-process *core.Agent backing a session — a
// TRANSITIONAL affordance for the in-process TUI carrier during the
// TUI-on-ctrlproto migration. The migrating TUI drives its hot path through the
// wire (Prompt + the Subscribe stream), but still reads the agent directly for
// rendering the finalized transcript and for management dialogs not yet moved
// onto surfaces. It is deliberately NOT on ctrlproto.WorkspaceService: it has no
// wire form and disappears as those readers migrate to snapshots/surfaces (a
// networked carrier never gets a *core.Agent). Materializes the session if
// needed and resolves an empty id to the default session.
func (w *Workspace) AgentFor(sess string) (*core.Agent, string, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return nil, "", err
	}
	return s.agent, s.id, nil
}

// --- session group (WorkspaceService) ---

func (w *Workspace) Sessions(ctx context.Context) ([]ctrlproto.SessionInfo, error) {
	summaries := core.DescribeSessions(w.root, w.cwd)
	defID := ""
	if p := core.LatestSession(w.root, w.cwd); p != "" {
		defID = sessionIDFromPath(p)
	}
	// Trust is workspace-global (keyed on w.cwd), so every session in this list
	// shares one verdict; a live session overrides with its own live flag below.
	wsTrusted := resolveTrustState(w.args).IsTrusted()
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]ctrlproto.SessionInfo, 0, len(summaries))
	for _, sm := range summaries {
		id := sessionIDFromPath(sm.Path)
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
	cfg, _ := LoadConfig()
	out := make(map[string]bool, len(cfg.FavoriteModels))
	for _, k := range cfg.FavoriteModels {
		out[k] = true
	}
	return out
}

// SetFavoriteModel pins/unpins a model in the user's favorites, persisted to
// config — the same list the TUI's ★ favorites view reads.
func (w *Workspace) SetFavoriteModel(ctx context.Context, provider, model string, on bool) error {
	if err := MutateConfig(func(c *Config) {
		c.FavoriteModels = toggleStringMember(c.FavoriteModels, favModelKey(provider, model), on)
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
	if err := TrustPath(w.cwd, parent); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "trust: %v", err)
	}
	w.applyTrust(ctx, true)
	return nil
}

// Untrust removes the workspace's cwd from the trust store and tears project
// content back down for every open session — the symmetric inverse of Trust.
func (w *Workspace) Untrust(ctx context.Context) error {
	if err := UntrustPath(w.cwd); err != nil {
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
	for _, p := range knownProviders {
		if _, _, err := ResolveCredential(p, ""); err == nil {
			out[p] = true
		}
	}
	out["ollama"] = true
	if !out["openai-compatible"] {
		if bu, _, _ := AuthStoreFor().Extras("openai-compatible"); bu != "" {
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
	return w.switchModel(s, providerName, modelID)
}

// switchModel mirrors acpFactory.SwitchModel: same provider+endpoint swaps the
// id in place; anything else builds a fresh client (dropping launch-time key/URL
// overrides that pin the old endpoint) and hot-swaps client+model, keeping the
// transcript. The new default becomes the workspace default for future sessions.
// providerName qualifies the id (same rationale as CreateOpts.Provider).
func (w *Workspace) switchModel(s *wsSession, providerName, modelID string) error {
	target, err := provider.FindModel(providerName, modelID)
	if err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "unknown model %q", modelID)
	}
	curProv, curModel := s.currentModel()
	cur, curErr := provider.FindModel(curProv, curModel)
	if curErr == nil && curProv == target.Provider && cur.BaseURL == target.BaseURL {
		s.agent.SetModel(target.ID)
		s.setModel(target.Provider, target.ID)
	} else {
		next := w.args
		next.Provider = target.Provider
		next.Model = target.ID
		next.APIKey = ""
		next.BaseURL = ""
		r, rerr := Resolve(next, true)
		if rerr != nil {
			return ctrlproto.Errorf(ctrlproto.CodeUnauthorized, "%v", rerr)
		}
		if !r.HasCredential() {
			return ctrlproto.Errorf(ctrlproto.CodeUnauthorized, "no credential for provider %q", r.Provider)
		}
		s.agent.SetClientAndModel(r.NewClient(), r.Model)
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
	cfg, _ := LoadConfig()
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
	r, err := Resolve(next, true)
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
