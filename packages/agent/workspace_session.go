package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/relaunch"
)

// webReloadGrace is the extension-reload grace the web path uses when a live
// trust toggle rebuilds the project extension set — matching the 2s the TUI/ACP
// paths use so a reload feels no slower than a cold boot.
const webReloadGrace = 2 * time.Second

// wsSession is one live session in a [Workspace]: a core.Agent, its durable
// session file, its confirm gate, and the pub/sub hub that fans its event
// stream out to every connected client. Pending tool-approval and question
// round-trips park here until a client answers (see workspace_confirm.go).
type wsSession struct {
	id           string
	ws           *Workspace
	agent        *core.Agent
	sess         *core.Session
	gate         *core.ConfirmGate   // nil in pure-yolo (no confirmation needed)
	extMgr       *extensions.Manager // this session's extension subprocesses
	stopExt      func()              // tears extMgr down on close
	skillTool    *skills.Tool        // available skills, for /skill autocomplete (may be nil)
	loreEntries  []lore.Entry        // discovered lore, for the lore inspector pane (nil when lore off)
	args         Args                // this session's resolved args (for tool rebuilds on reload/MCP toggle)
	cwd          string              // resolved workspace dir (for the extensions inventory scan)
	trusted      atomic.Bool         // workspace trust (project extensions/skills gating); mutated live by Trust/Untrust
	hub          *wsHub
	subscription bool // credential is an OAuth ("sub") token, not a paid api key

	mu         sync.Mutex
	provider   string
	model      string
	title      string
	persona    string
	turnCtx    context.Context
	turnCancel context.CancelFunc // non-nil while a turn runs
	curCallID  string             // the tool call currently at the gate (recordCall)
	pendPerm   map[string]chan core.ConfirmDecision
	pendAsk    map[string]chan core.UserAnswer
	permReq    map[string]ctrlproto.PermissionRequest // details for the snapshot
	askReq     map[string]ctrlproto.AskRequest        // details for the snapshot
	askSeq     uint64

	paneMu    sync.Mutex           // guards extPanels (touched from ext driver goroutines)
	extPanels map[string]*webPanel // surface id → open extension panel
}

// buildSession constructs the live agent for a session, wiring the confirm gate
// (with a broadcasting web Confirmer), the Asker, this session's extension
// manager + the full tool-call ladder, durable persistence, lore (via NewAgent),
// and event fan-out. msgs is the resumed transcript (nil for a fresh session).
func (w *Workspace) buildSession(id string, sess *core.Session, msgs []provider.Message, persona string) (*wsSession, error) {
	args := w.args
	if sess.Meta.Provider != "" {
		args.Provider = sess.Meta.Provider
	}
	if sess.Meta.Model != "" {
		args.Model = sess.Meta.Model
	}
	// A per-session persona (from CreateOpts.Persona) overrides the workspace
	// default; Resolve turns the name into the active persona/charter. Empty
	// keeps whatever the workspace launched with. Not persisted to session meta
	// yet, so a fresh materialize after a daemon restart falls back to the
	// default — durable per-session personas are a follow-up (needs core meta).
	if persona != "" {
		args.Persona = persona
	}

	s := &wsSession{
		id:        id,
		ws:        w,
		sess:      sess,
		hub:       newWSHub(),
		provider:  sess.Meta.Provider,
		model:     sess.Meta.Model,
		title:     sess.Meta.Title,
		pendPerm:  map[string]chan core.ConfirmDecision{},
		pendAsk:   map[string]chan core.UserAnswer{},
		permReq:   map[string]ctrlproto.PermissionRequest{},
		askReq:    map[string]ctrlproto.AskRequest{},
		extPanels: map[string]*webPanel{},
	}

	pol, warns := buildPermissionPolicy(args)
	for _, wn := range warns {
		fmt.Fprintf(os.Stderr, "note: %s\n", wn)
	}

	r, err := Resolve(args, true)
	if err != nil {
		return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "resolve: %v", err)
	}

	var gate *core.ConfirmGate
	if pol != nil {
		gate = core.NewPolicyGate(pol, &webConfirmer{s: s})
		r.AdoptReadOnlySet(pol.ReadOnly)
	}
	r.SetAsker(&webAsker{s: s})

	// Per-session extensions, like ACP: each session owns its manager (own
	// announced session, own host-tool dispatcher, own context), so concurrent
	// sessions never bleed extension state — which a single shared manager
	// would, since it tracks one announced session (SwapAnnouncedSession).
	// Merges ext tools into r BEFORE NewAgent so the agent's registry has them.
	extMgr, stopExt := setupWebExtensions(w.ctx, args, &r, w.version, s)
	s.extMgr = extMgr
	s.stopExt = stopExt

	// Merge the workspace-global MCP tools (after extensions, so an extension
	// tool wins a name collision — matches the CLI order).
	if w.mcpAdapter != nil {
		r.MergeExtensionTools(w.mcpAdapter)
	}

	// Web-only built-ins that live outside Resolve (swarm_spawn, terva_restart).
	w.injectExtraTools(&r, args)

	ag := r.NewAgent()
	s.agent = ag
	s.gate = gate
	s.skillTool = r.SkillTool
	s.args = args
	s.cwd = r.CWD
	s.trusted.Store(r.Trusted)
	s.persona = r.persona.Name
	// Discover the authored lore for the read-only inspector pane (respecting the
	// lore-enabled gate, mirroring build.go). Resolve keeps only the triggered
	// subset, so re-Discover here for the full set.
	if !args.NoLore {
		if lcfg, _ := LoadConfig(); lcfg.Lore == nil || *lcfg.Lore {
			s.loreEntries, _, _ = lore.Discover(TervaHome(), r.CWD, r.Trusted)
		}
	}

	// Rebuild the agent's model-facing tool set whenever extensions reload (a
	// live enable/disable) — and also, via rebuildTools, after a live MCP toggle.
	extMgr.SetOnReload(s.rebuildTools)
	s.subscription = r.AuthMethod == "oauth"
	if s.provider == "" {
		s.provider = r.Provider
	}
	if s.model == "" {
		s.model = r.Model
	}

	// Canonical tool-call ladder (hooks → gate → ext intercept). recordCall
	// stays OUTSIDE the ladder so the web Confirmer learns the call id before
	// gate.Check runs (the ACP §13 correlation seam). Tools execute one at a
	// time, so exactly one call is current when Confirm fires.
	hookEng := w.hookEng
	ladder := buildBeforeToolExecute(w.ctx, hookEng, gate, extMgr)
	ag.BeforeToolExecute = func(call provider.ToolCallBlock) (bool, string, json.RawMessage) {
		s.recordCall(call.ID)
		return ladder(call)
	}
	wireHostToolDispatcher(ag, extMgr, gate)
	if extMgr != nil {
		ag.BeforeTurn = func(step int) (bool, string) {
			res := extMgr.InterceptTurnStart(w.ctx, step)
			return !res.Block, res.Reason
		}
		ag.BeforeAssistantMessage = func(text string) (bool, string, string) {
			res := extMgr.InterceptAssistantMessage(w.ctx, text)
			if res.Block {
				return false, res.Reason, ""
			}
			return true, "", res.ReplaceText
		}
		ag.BeforeUserMessage = func(text string) (bool, string, string) {
			res := extMgr.InterceptUserMessage(w.ctx, text)
			if res.Block {
				return false, res.Reason, ""
			}
			return true, "", res.ReplaceText
		}
		wireExtEphemeral(ag, extMgr.EphemeralContext)
		ag.ContinueOnStop = continueOnOpenWork(extMgr)
	}

	// OnEvent: broadcast to clients FIRST (so the UI streams promptly), then the
	// extension + hook observers — the ACP composition order, with the web
	// fan-out standing in for the ACP translator.
	sessCWD := r.CWD
	wsObserve := workspaceChangeObserver(tools.NewWorkspaceDiffer(func() string { return sessCWD }), extMgr)
	ag.OnEvent = func(ev core.AgentEvent) {
		s.broadcast(ctrlproto.ConversationEvent(core.EventToWire(ev)))
		wsObserve(ev)
		fanoutAgentEvent(extMgr, ev)
		observeAgentEventForHooks(hookEng, ev)
	}

	// Durable persistence: every message/usage/compaction flows to the session
	// JSONL as it happens (also adopts the session identity). Sets the On*
	// persistence hooks, not OnEvent, so it composes with the fan-out above.
	wireHeadlessSessionPersist(ag, sess)

	if len(msgs) > 0 {
		ag.SetMessages(msgs)
		if cum, last, e := core.SessionUsageDetail(sess.Path); e == nil {
			ag.SeedCost(cum)
			ag.SeedLastTurnUsage(last)
		}
	}

	// Announce the session to extensions AFTER it exists, so a session-keyed
	// extension (e.g. memory) learns the real id before the first tool call.
	emitSessionStart(extMgr, sess)
	return s, nil
}

// injectExtraTools adds the web-session built-ins that live outside Resolve —
// the auto-swarm spawn tool and the self-restart tool — into r's registry.
// Called at session build and again on every extension reload, so a rebuilt tool
// set keeps them (Resolve doesn't know about them).
func (w *Workspace) injectExtraTools(r *Resolved, args Args) {
	if r.ToolRegistry == nil {
		return
	}
	// swarm_spawn: mirrors the CLI, and closes the prompt/tool gap (Resolve ships
	// the auto-swarm system addendum, so the tool must exist to match).
	if hasBaseWorkspaceTools(args) && AutoSwarmEnabled() {
		cfg, _ := LoadConfig()
		r.ToolRegistry["swarm_spawn"] = &tools.SwarmSpawnTool{
			Swarm:           w.swarm,
			Enabled:         AutoSwarmEnabled,
			HostProvider:    r.Provider,
			HostModel:       r.Model,
			Tiers:           swarmTierMap(cfg.SwarmTiers),
			PersonaResolver: resolveDispatchPersona,
			Personas:        dispatchablePersonaNames(),
		}
	}
	// terva_restart: only when the operator enabled self-restart. Left
	// unclassified in permissions.go so it always prompts before re-execing.
	if relaunch.Enabled() {
		r.ToolRegistry["terva_restart"] = &tools.RestartTool{}
	}
}

// setupWebExtensions builds this session's extension manager and merges its
// tools into r before the agent is constructed. Mirrors the ACP per-session
// setup (extensions only — no connectors, no MCP), returning a stop closure the
// session calls on close. The manager is always non-nil (a --no-ext session
// just has an empty tool set).
func setupWebExtensions(ctx context.Context, args Args, r *Resolved, version string, s *wsSession) (*extensions.Manager, func()) {
	// webExtHooks routes extension panel/status frames into the session's surface
	// registry (broadcasting to clients); other hooks stay non-interactive no-ops.
	extMgr := extensions.New(TervaHome(), r.CWD, version, r.Provider, r.Model, webExtHooks{s: s})
	extMgr.SetContextDisabled(r.DisableContextExtensions)
	extMgr.SetDisabledExtensions(r.DisableExtensions)
	extMgr.SetAllowedExtensions(args.WithExtensions)
	extMgr.SetConfigResolver(resolveExtensionConfig)
	wireSessionReader(extMgr, TervaHome(), r.CWD)
	extMgr.SetProjectTrusted(r.Trusted)
	for _, e := range extMgr.LoadExplicit(ctx, args.Exts) {
		fmt.Fprintln(os.Stderr, "extension load:", e)
	}
	if !args.NoExt {
		for _, e := range extMgr.Discover(ctx) {
			fmt.Fprintln(os.Stderr, "extension load:", e)
		}
	}
	extMgr.WaitForReady(3 * time.Second)
	r.MergeExtensionTools(&extToolAdapter{mgr: extMgr})
	return extMgr, func() { extMgr.Stop(2 * time.Second) }
}

// prompt starts a turn. The turn's context is derived from the workspace (not a
// client connection), so a client disconnecting mid-turn does not abort the run
// other clients are watching. Returns ErrBusy if a turn is already running.
func (s *wsSession) prompt(text string, images []ctrlproto.Image) error {
	s.mu.Lock()
	if s.turnCancel != nil {
		s.mu.Unlock()
		return ctrlproto.ErrBusy
	}
	turnCtx, cancel := context.WithCancel(s.ws.ctx)
	s.turnCtx, s.turnCancel = turnCtx, cancel
	s.mu.Unlock()

	imgs := toImageBlocks(images)
	go func() {
		defer func() {
			s.mu.Lock()
			s.turnCtx, s.turnCancel = nil, nil
			s.mu.Unlock()
		}()
		// sink is nil: the agent's OnEvent (set in buildSession) fans every event
		// out, so Continue() and any internal re-prompt stream too.
		if err := s.agent.PromptWithPolicy(turnCtx, text, imgs, nil); err != nil && !errors.Is(err, context.Canceled) {
			s.broadcast(ctrlproto.ConversationEvent(core.WireEvent{Type: "error", Error: err.Error()}))
		}
		// After the turn, settle an untitled session's title and push it live, so
		// the header/list stop reading "new chat" without a page refresh. Uses the
		// workspace context (not the turn's) so it survives turn teardown.
		s.settleTitle(s.ws.ctx)
	}()
	return nil
}

// rebuildTools re-resolves and swaps the agent's model-facing tool set: a fresh
// base registry + live extension tools + the workspace MCP tools + web built-ins.
// Fired on an extension reload (SetOnReload) and after a live MCP toggle — a
// fresh Resolve carries neither ext nor MCP tools, so both are re-merged or a
// reload would silently drop them from the model.
func (s *wsSession) rebuildTools() {
	rr, err := Resolve(s.args, true)
	if err != nil {
		return
	}
	rr.MergeExtensionTools(&extToolAdapter{mgr: s.extMgr})
	if s.ws.mcpAdapter != nil {
		rr.MergeExtensionTools(s.ws.mcpAdapter)
	}
	s.ws.injectExtraTools(&rr, s.args)
	s.agent.SetTools(rr.ToolRegistry)
}

// compact runs user-driven compaction: summarize + replace the transcript, then
// push a fresh snapshot so every client re-renders the compacted history. A
// running turn blocks it (ErrBusy); an already-minimal transcript is reported as
// a benign notice rather than an error.
func (s *wsSession) compact(ctx context.Context) error {
	s.mu.Lock()
	busy := s.turnCancel != nil
	s.mu.Unlock()
	if busy {
		return ctrlproto.ErrBusy
	}
	// Non-nil sink: Compact streams summary deltas and calls it unconditionally.
	if _, err := s.agent.Compact(ctx, core.AutoCompactKeepTail, func(string) {}); err != nil {
		if errors.Is(err, core.ErrNothingToCompact) {
			s.broadcast(ctrlproto.NoticeEvent("info", "", i18n.T("Nothing to compact — the transcript is already minimal.")))
			return nil
		}
		if errors.Is(err, core.ErrBusy) {
			return ctrlproto.ErrBusy
		}
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "compact: %v", err)
	}
	// Replace every client's transcript with the compacted one.
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	s.broadcast(ctrlproto.NoticeEvent("info", "", i18n.T("Compacted the conversation.")))
	return nil
}

// clear wipes the transcript — unlike compact, it keeps no summary. It empties
// the live agent AND writes an empty compaction checkpoint so a resume from disk
// also starts fresh (the old rows stay in the JSONL for audit, but loaders honor
// the latest checkpoint). A running turn blocks it (ErrBusy). On success every
// client receives a fresh, empty snapshot. The busy gate means no turn is
// appending concurrently, so the direct durable write is safe (mirrors compact).
func (s *wsSession) clear() error {
	s.mu.Lock()
	busy := s.turnCancel != nil
	s.mu.Unlock()
	if busy {
		return ctrlproto.ErrBusy
	}
	s.agent.SetMessages(nil)
	_ = s.sess.AppendCompaction(nil)
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	s.broadcast(ctrlproto.NoticeEvent("info", "", i18n.T("Cleared the conversation.")))
	return nil
}

// setTrusted brings this session's project content in line with a new Workspace
// Trust verdict, mirroring the interactive /trust and ACP paths: flip the
// extension manager and reload it (so project extensions appear or tear down —
// the reload fires SetOnReload → rebuildTools, refreshing the model tool set
// once the new set is ready), then re-discover lore (project lore becomes
// visible/editable) and push a fresh session_updated so clients re-read Trusted
// and the trust-gated panes. Project skills/context are baked into the system
// prompt and only change on the next session — deliberate, matching /trust.
func (s *wsSession) setTrusted(ctx context.Context, trusted bool) {
	s.trusted.Store(trusted)
	if s.extMgr != nil {
		s.extMgr.SetProjectTrusted(trusted)
		s.extMgr.Reload(ctx, webReloadGrace) // fires rebuildTools via SetOnReload
	} else {
		s.rebuildTools()
	}
	s.reloadLore()
	s.broadcast(ctrlproto.SessionUpdatedEvent(s.info()))
}

// settleTitle gives an untitled session a name once its first exchange exists,
// broadcasting the change so every client updates live. Phase 1 sets an instant
// title from the first message; phase 2 optionally refines it with a one-shot
// model call (config AutoTitle). A title already present — user rename or a
// prior turn — short-circuits the whole thing.
func (s *wsSession) settleTitle(ctx context.Context) {
	s.mu.Lock()
	titled := s.title != ""
	s.mu.Unlock()
	if titled {
		return
	}
	first := firstUserText(s.agent.Messages())
	if first == "" {
		return
	}
	fallback := titleFromFirstText(first)
	s.applyTitle(fallback)

	ok, cl, model := s.ws.titleGen(s)
	if !ok || cl == nil {
		return
	}
	gen := generateTitle(ctx, cl, model, first)
	if gen == "" {
		return
	}
	// Only upgrade to the generated title if a manual rename didn't land while
	// the model was thinking.
	s.mu.Lock()
	stillOurs := s.title == fallback
	s.mu.Unlock()
	if stillOurs {
		s.applyTitle(gen)
	}
}

// applyTitle persists a title to the session file, records it in memory, and
// broadcasts the change. Best-effort persistence: an update still reaches
// clients even if the append fails.
func (s *wsSession) applyTitle(title string) {
	if title == "" {
		return
	}
	if s.sess != nil {
		_ = core.RenameSession(s.sess.Path, title)
	}
	s.setTitle(title)
	s.broadcast(ctrlproto.SessionUpdatedEvent(s.info()))
}

// firstUserText returns the text of the first user message in a transcript, the
// seed for a session title.
func firstUserText(msgs []provider.Message) string {
	for _, m := range msgs {
		if m.Role != provider.RoleUser {
			continue
		}
		var sb strings.Builder
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok {
				sb.WriteString(tb.Text)
			}
		}
		if t := strings.TrimSpace(sb.String()); t != "" {
			return t
		}
	}
	return ""
}

// queue injects text at the next safe boundary of the running turn (the
// multi-device interject story). If idle, it is delivered on the next turn.
// Broadcasts the new queue so every client's queued view converges.
func (s *wsSession) queue(text string) {
	if s.agent.QueueMessage(text) {
		s.broadcastQueue()
	}
}

// setQueue replaces the pending queue wholesale (edit/cancel) and broadcasts it.
func (s *wsSession) setQueue(texts []string) {
	s.agent.SetQueuedMessages(texts)
	s.broadcastQueue()
}

func (s *wsSession) broadcastQueue() {
	s.broadcast(ctrlproto.QueueUpdatedEvent(s.agent.PendingQueuedMessages()))
}

// cancelTurn interrupts the active turn, if any.
func (s *wsSession) cancelTurn() {
	s.mu.Lock()
	c := s.turnCancel
	s.mu.Unlock()
	if c != nil {
		c()
	}
}

// recordCall notes the tool call about to be gated, so the web Confirmer can
// key its broadcast permission request to it.
func (s *wsSession) recordCall(id string) {
	s.mu.Lock()
	s.curCallID = id
	s.mu.Unlock()
}

func (s *wsSession) broadcast(ev ctrlproto.Event) { s.hub.broadcast(ev) }

// subscribe registers a new client. It receives a snapshot of the current
// transcript first (atomically, before any live event), then the live stream.
// Cancelling ctx unsubscribes and closes the channel.
func (s *wsSession) subscribe(ctx context.Context) <-chan ctrlproto.Event {
	ch := s.hub.add(func() ctrlproto.Event { return ctrlproto.SnapshotEvent(s.snapshot()) })
	go func() {
		<-ctx.Done()
		s.hub.remove(ch)
	}()
	return ch
}

func (s *wsSession) snapshot() ctrlproto.Snapshot {
	msgs := s.agent.Messages()
	wm := make([]core.WireMessage, len(msgs))
	for i, m := range msgs {
		wm[i] = core.MessageToWire(m)
	}
	s.mu.Lock()
	busy := s.turnCancel != nil
	var perms []ctrlproto.PermissionRequest
	for _, r := range s.permReq {
		perms = append(perms, r)
	}
	var asks []ctrlproto.AskRequest
	for _, r := range s.askReq {
		asks = append(asks, r)
	}
	s.mu.Unlock()
	return ctrlproto.Snapshot{
		Session:     s.info(),
		Messages:    wm,
		Busy:        busy,
		Permissions: perms,
		Asks:        asks,
		Queued:      s.agent.PendingQueuedMessages(),
		Skills:      s.skillList(),
	}
}

// skillList surfaces the session's available skills for /skill autocomplete.
func (s *wsSession) skillList() []ctrlproto.SkillInfo {
	if s.skillTool == nil {
		return nil
	}
	sk := s.skillTool.Skills()
	out := make([]ctrlproto.SkillInfo, 0, len(sk))
	for _, k := range sk {
		out = append(out, ctrlproto.SkillInfo{Name: k.Name, Description: k.Description})
	}
	return out
}

func (s *wsSession) info() ctrlproto.SessionInfo {
	s.mu.Lock()
	prov, model, title, persona := s.provider, s.model, s.title, s.persona
	s.mu.Unlock()
	info := ctrlproto.SessionInfo{
		ID:       s.id,
		Title:    title,
		Provider: prov,
		Model:    model,
		Persona:  persona,
		Path:     s.sess.Path,
		Created:  ctrlTimeString(s.sess.Meta.Started),
		Trusted:  s.trusted.Load(),
	}
	if s.agent != nil {
		info.Messages = len(s.agent.Messages())
		info.Usage = toCtrlUsage(s.agent.Cost())
		last := s.agent.LastTurnUsage()
		info.ContextTokens = last.InputTokens + last.CacheReadTokens + last.CacheWriteTokens
		if mdl, err := provider.FindModel(prov, model); err == nil {
			info.ContextWindow = mdl.ContextWindow
		}
	}
	return info
}

func (s *wsSession) currentModel() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.provider, s.model
}

func (s *wsSession) personaName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persona
}

func (s *wsSession) setModel(prov, model string) {
	s.mu.Lock()
	s.provider, s.model = prov, model
	s.mu.Unlock()
}

func (s *wsSession) setTitle(t string) {
	s.mu.Lock()
	s.title = t
	s.mu.Unlock()
}

// close cancels any turn, tears down this session's extension subprocesses,
// closes all subscribers, and closes the session file.
func (s *wsSession) close() {
	s.mu.Lock()
	c := s.turnCancel
	s.mu.Unlock()
	if c != nil {
		c()
	}
	if s.stopExt != nil {
		s.stopExt()
	}
	s.hub.closeAll()
	if s.sess != nil {
		_ = s.sess.Close()
	}
}

func toImageBlocks(imgs []ctrlproto.Image) []provider.ImageBlock {
	if len(imgs) == 0 {
		return nil
	}
	out := make([]provider.ImageBlock, 0, len(imgs))
	for _, im := range imgs {
		out = append(out, provider.ImageBlock{MimeType: im.MimeType, Data: im.Data})
	}
	return out
}

// wsHub fans one session's events out to N subscribers.
type wsHub struct {
	mu   sync.Mutex
	subs map[chan ctrlproto.Event]struct{}
}

// hubBuffer is the per-subscriber channel depth. A subscriber that falls this
// far behind drops events (see broadcast) and can re-subscribe to resync from a
// fresh snapshot rather than block the turn for everyone.
const hubBuffer = 256

func newWSHub() *wsHub { return &wsHub{subs: map[chan ctrlproto.Event]struct{}{}} }

// add creates a subscriber channel. If first is non-nil its result is enqueued
// before the channel joins the broadcast set, so a snapshot is guaranteed to
// precede any live event without a race.
func (h *wsHub) add(first func() ctrlproto.Event) chan ctrlproto.Event {
	ch := make(chan ctrlproto.Event, hubBuffer)
	h.mu.Lock()
	if first != nil {
		ch <- first()
	}
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *wsHub) remove(ch chan ctrlproto.Event) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *wsHub) broadcast(ev ctrlproto.Event) {
	h.mu.Lock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default: // slow subscriber: drop rather than stall the turn
		}
	}
	h.mu.Unlock()
}

func (h *wsHub) closeAll() {
	h.mu.Lock()
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}
