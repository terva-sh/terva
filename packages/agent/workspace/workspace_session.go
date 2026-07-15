package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/agent/raati"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/agent/tools/tasks/tasktool"
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
	id          string
	ws          *Workspace
	agent       *core.Agent
	sess        *core.Session
	gate        *core.ConfirmGate    // nil in pure-yolo (no confirmation needed)
	extMgr      *extensions.Manager  // this session's extension subprocesses
	stopExt     func()               // tears extMgr down on close
	skillTool   *skills.Tool         // available skills, for /skill autocomplete (may be nil)
	tasks       *tasktool.Controller // the built-in task board (nil when the session has no base workspace tools)
	loreEntries []lore.Entry         // discovered lore, for the lore inspector pane (nil when lore off)
	// actorCast + warmActors back the --play director's actor_spawn tool: the
	// closed declared cast and the live-actor cache that survives registry
	// rebuilds (so re-injecting actor_spawn on reload keeps the warm scene).
	// Built once in buildSession when castSkinActive; nil otherwise.
	actorCast    map[string]tools.CastMember
	warmActors   *tools.WarmActors
	args         build.Args  // this session's resolved args (for tool rebuilds on reload/MCP toggle)
	cwd          string      // resolved workspace dir (for the extensions inventory scan)
	trusted      atomic.Bool // workspace trust (project extensions/skills gating); mutated live by Trust/Untrust
	hub          *wsHub
	subscription bool // credential is an OAuth ("sub") token, not a paid api key

	mu       sync.Mutex
	provider string
	model    string
	title    string
	// titleGenerated is title's provenance: true when machine titling wrote
	// it (settleTitle / generate_title / the post-compaction refresh), false
	// for a user rename. Automatic re-titling keys on it — a manual name is
	// never clobbered. Seeded from the session file's rename rows.
	titleGenerated bool
	persona        string
	turnCtx        context.Context
	turnCancel     context.CancelFunc // non-nil while a turn runs
	curCallID      string             // the tool call currently at the gate (recordCall)
	pendPerm       map[string]chan core.ConfirmDecision
	pendAsk        map[string]chan core.UserAnswer
	permReq        map[string]ctrlproto.PermissionRequest // details for the snapshot
	askReq         map[string]ctrlproto.AskRequest        // details for the snapshot
	askSeq         uint64

	paneMu    sync.Mutex           // guards extPanels (touched from ext driver goroutines)
	extPanels map[string]*webPanel // surface id → open extension panel

	// swarmWatch tracks the auto-swarm sub-agents this session's agent spawned,
	// so the coordinator receives an [auto-swarm update] recap when they all
	// finish (the daemon twin of the legacy TUI swarmWatch). Guarded by
	// swarmWatchMu — touched from swarm runner goroutines.
	swarmWatchMu sync.Mutex
	swarmWatch   []*swarmWatchEntry
	// swarmGuardNudged makes the "don't finalize while sub-agents run" guard
	// fire ONCE per batch (a single hold, then the coordinator idles and the
	// queued recap re-engages it) rather than spinning while they run.
	swarmGuardNudged bool

	// sidechat holds this session's open /btw snapshots — frozen system + msgs
	// + client, keyed by an id minted at open. Guarded by its own mutex, and
	// dropped wholesale at close(). See workspace_sidechat.go.
	sidechatMu  sync.Mutex
	sidechatSeq uint64
	sidechats   map[string]*sideChatSnapshot
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
		id:       id,
		ws:       w,
		sess:     sess,
		hub:      newWSHub(),
		provider: sess.Meta.Provider,
		model:    sess.Meta.Model,
		// Title + provenance come from the file (OpenSession reflects the
		// last rename row into Meta.Title), so a session renamed while cold
		// materializes titled — and the automatic passes know whether the
		// name is theirs to replace.
		title:          sess.Meta.Title,
		titleGenerated: sess.TitleGenerated,
		pendPerm:       map[string]chan core.ConfirmDecision{},
		pendAsk:        map[string]chan core.UserAnswer{},
		permReq:        map[string]ctrlproto.PermissionRequest{},
		askReq:         map[string]ctrlproto.AskRequest{},
		extPanels:      map[string]*webPanel{},
	}

	pol, warns := build.BuildPermissionPolicy(args)
	for _, wn := range warns {
		s.diag(fmt.Sprintf("note: %s", wn))
	}

	r, err := build.Resolve(args, true)
	if err != nil {
		return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "resolve: %v", err)
	}

	var gate *core.ConfirmGate
	if pol != nil {
		gate = core.NewPolicyGate(pol, &webConfirmer{s: s})
		r.AdoptReadOnlySet(pol.ReadOnly)
	}
	r.SetAsker(&webAsker{s: s})
	r.SetEscalator(&sessionEscalator{s: s})

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

	// --play cast: build the declared actors + warm-actor cache once per
	// session (Resolve already emitted the cast addendum advertising the
	// actor_spawn tool, so it must exist to match). Refs are validated now, so
	// a typo'd persona / missing card fails the session build loudly rather
	// than opaquely mid-scene. injectExtraTools registers the tool from these.
	if build.CastSkinActive(args) {
		cast, cerr := build.BuildActorCast(build.MergedCastRefs(args, r.CWD, r.Trusted), r.CWD)
		if cerr != nil {
			stopExt()
			return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "actor cast: %v", cerr)
		}
		if len(cast) > 0 {
			s.actorCast = cast
			s.warmActors = tools.NewWarmActors(tools.DefaultWarmActorCap)
		}
	}

	// Web-only built-ins that live outside Resolve (swarm_spawn, terva_restart,
	// and the --play actor_spawn cast skin).
	w.injectExtraTools(s, &r, args)

	// Re-point this session's file tools at the workspace-shared sandbox so
	// /jail is one workspace-wide posture (and survives the rebuilds below),
	// discarding the throwaway per-session sandbox Resolve just built.
	r.UseSandbox(w.sandbox)

	ag := r.NewAgent()
	s.agent = ag
	s.gate = gate
	s.skillTool = r.SkillTool
	s.tasks = r.Tasks
	s.args = args
	s.cwd = r.CWD
	s.trusted.Store(r.Trusted)
	s.persona = r.Persona.Name
	// Discover the authored lore for the read-only inspector pane (respecting the
	// lore-enabled gate, mirroring build.go). Resolve keeps only the triggered
	// subset, so re-Discover here for the full set.
	if !args.NoLore {
		if lcfg, _ := config.LoadConfig(); lcfg.Lore == nil || *lcfg.Lore {
			s.loreEntries, _, _ = lore.Discover(config.TervaHome(), r.CWD, r.Trusted)
		}
	}

	// Rebuild the agent's model-facing tool set whenever extensions reload (a
	// live enable/disable) — and also, via rebuildTools, after a live MCP toggle.
	extMgr.SetOnReload(func() { s.rebuildTools("extension-reload") })
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
	ladder := build.BuildBeforeToolExecute(w.ctx, hookEng, gate, extMgr)
	ag.BeforeToolExecute = func(call provider.ToolCallBlock) (bool, string, json.RawMessage) {
		s.recordCall(call.ID)
		return ladder(call)
	}
	build.WireHostToolDispatcher(ag, extMgr, gate)
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
		build.WireExtEphemeral(ag, extMgr.EphemeralContext)
		build.WireTasksEphemeral(ag, r.Tasks)
	}
	// Don't let the coordinator declare "finished" while it has open work —
	// an extension's blocking context (protocol), OR sub-agents it spawned that
	// are still running. Registration order is priority: open work outranks the
	// swarm hold. The swarm hold fires once, then the coordinator idles and the
	// queued [auto-swarm update] recap re-engages it (no spin).
	ag.AddContinuationGate(build.OpenWorkGate(extMgr, r.Tasks))
	ag.AddContinuationGate(core.ContinuationGate{
		Cause: "swarm-hold",
		Fire: func(provider.StopReason) (string, bool) {
			if s.swarmGuardHold() {
				return swarmWaitGateMessage, true
			}
			return "", false
		},
	})

	// Event observers, in registration order — which IS the delivery order.
	// Broadcast to clients FIRST (so the UI streams promptly), then the
	// extension + hook observers: the ACP composition order, with the web
	// fan-out standing in for the ACP translator. The ordering used to live in
	// a comment above one closure; now it is the code.
	sessCWD := r.CWD
	wsObserve := build.WorkspaceChangeObserver(tools.NewWorkspaceDiffer(func() string { return sessCWD }), extMgr)
	ag.AddEventObserver(func(ev core.AgentEvent) {
		// Full form: image blocks keep their raw Data. In-process subscribers
		// (the TUI carrier) render real pixels at zero cost — the slices are
		// shared, never serialized — and serialized carriers strip at their
		// connection boundary unless the client negotiated "image-data"
		// (ctrlproto serve loop).
		s.broadcast(ctrlproto.ConversationEvent(core.EventToWireFull(ev)))
	})
	ag.AddEventObserver(wsObserve)
	ag.AddEventObserver(func(ev core.AgentEvent) { build.FanoutAgentEvent(extMgr, ev) })
	ag.AddEventObserver(func(ev core.AgentEvent) { build.ObserveAgentEventForHooks(hookEng, ev) })
	// The queue is mirrored, not tracked, so every mutation has to announce
	// itself. The host performs — and therefore announces — all of them but
	// one: the loop's own drain at a mid-turn safe boundary, which is what this
	// closes. Broadcast the queue as it stands rather than the drained list, so
	// a message queued in the same breath is not overwritten by a stale empty.
	ag.AddQueueDrainedObserver(func([]string) { s.broadcastQueue() })

	// Durable persistence: every message/usage/compaction flows to the session
	// JSONL as it happens (also adopts the session identity). Sets the On*
	// persistence hooks, not OnEvent, so it composes with the fan-out above.
	build.WireHeadlessSessionPersist(ag, sess)

	// Mirror each assistant message's visible text out to a bound chat bridge.
	// Registered after persistence, and additive: an observer cannot unwire the
	// durable transcript (that is what AddMessageObserver bought us). One chat
	// message per assistant message, matching the legacy TUI's cadence, so a
	// multi-step turn still narrates each model call.
	ag.AddMessageObserver(func(m provider.Message) {
		if m.Role != provider.RoleAssistant {
			return
		}
		b := w.chatMirror(s.id)
		if b == nil {
			return
		}
		if text := strings.TrimSpace(assistantVisibleText(m)); text != "" {
			go b.OnAssistantText(text)
		}
	})

	if len(msgs) > 0 {
		ag.SetMessages(msgs)
		if cum, last, e := core.SessionUsageDetail(sess.Path); e == nil {
			ag.SeedCost(cum)
			ag.SeedLastTurnUsage(last)
		}
	}

	// Announce the session to extensions AFTER it exists, so a session-keyed
	// extension (e.g. memory) learns the real id before the first tool call.
	build.EmitSessionStart(extMgr, sess)
	build.RebindTasks(r.Tasks, sess)
	return s, nil
}

// injectExtraTools adds the web-session built-ins that live outside Resolve —
// the auto-swarm spawn tool, the self-restart tool, and the --play actor_spawn
// cast skin — into r's registry. Called at session build and again on every
// extension reload, so a rebuilt tool set keeps them (Resolve doesn't know
// about them). The cast (s.actorCast / s.warmActors) is built once in
// buildSession; re-injecting here just re-points a rebuilt registry at it.
func (w *Workspace) injectExtraTools(s *wsSession, r *build.Resolved, args build.Args) {
	if r.ToolRegistry == nil {
		return
	}
	// swarm_spawn: mirrors the CLI, and closes the prompt/tool gap (Resolve ships
	// the auto-swarm system addendum, so the tool must exist to match).
	if build.HasBaseWorkspaceTools(args) && config.AutoSwarmEnabled() {
		cfg, _ := config.LoadConfig()
		r.ToolRegistry["swarm_spawn"] = &tools.SwarmSpawnTool{
			Swarm:           w.swarm,
			Enabled:         config.AutoSwarmEnabled,
			HostProvider:    r.Provider,
			HostModel:       r.Model,
			Tiers:           build.SwarmTierMap(cfg.SwarmTiers),
			PersonaResolver: build.ResolveDispatchPersona,
			Personas:        build.DispatchablePersonaNames(),
			Trusted:         w.Trusted, // live: a Trust/Untrust flip applies to the next spawn
			// Track each spawn so the coordinator gets the [auto-swarm update]
			// recap when the batch finishes (the carrier twin of the legacy
			// OnSpawned wiring; without it a coordinator never learns they're done).
			OnSpawned: s.trackSwarmAgent,
		}
	}
	// actor_spawn: the --play director's cast-dispatch skin. Only when the cast
	// was built (castSkinActive with a non-empty cast). Shares the session's
	// warm-actor cache across rebuilds so the live scene survives a reload.
	if s != nil && len(s.actorCast) > 0 {
		cfg, _ := config.LoadConfig()
		r.ToolRegistry["actor_spawn"] = &tools.ActorSpawnTool{
			Swarm:        w.swarm,
			Warm:         s.warmActors,
			Cast:         s.actorCast,
			HostProvider: r.Provider,
			HostModel:    r.Model,
			Tiers:        build.SwarmTierMap(cfg.SwarmTiers),
		}
	}
	// raati_convene: convene a deliberation panel on a decisive question
	// (docs/proposals/raati-deliberation.md). Opt-in via the user config's
	// raati.convene_tool — a convening spends real sub-agent turns — and
	// left unclassified in permissions so it always hits the approval gate.
	// Base workspace sessions only (the skin-gate lesson: no deliberation
	// tool inside --chat/--play). The Board hook mirrors the run onto the
	// live raati pane, so an agent's deliberation is watchable.
	if build.HasBaseWorkspaceTools(args) {
		if cfg, _ := config.LoadConfig(); cfg.Raati.ConveneTool {
			r.ToolRegistry["raati_convene"] = &tools.RaatiConveneTool{
				Engine:       raati.SwarmEngine{Swarm: w.swarm},
				Enabled:      raatiConveneEnabled,
				HostProvider: r.Provider,
				HostModel:    r.Model,
				Tiers:        build.SwarmTierMap(cfg.SwarmTiers),
				Level2:       build.RaatiLevel2Bindings(cfg),
				SeatOrder:    raati.SeatOrder(cfg.Raati.SeatOrder),
				SeatMap:      cfg.Raati.SeatMap,
				Profiles:     build.RaatiProfiles(cfg),
				Board:        raatiBoardHook{w},
				Persist:      build.WriteRaatiRecord,
				Answer:       w.raatiClerkAnswer,
			}
		}
	}
	// chat_send_image / chat_send_file: only while a bridge is connected AND
	// bound to THIS session, so a second session never sees another's chat tools.
	// A declarative input, exactly like AutoSwarmEnabled above — connect and
	// disconnect change the input and re-run this derivation (rebuildTools),
	// they never patch a live registry. The TUI used to patch it, and its
	// snapshot-and-merge dance existed only to survive an extension reload that
	// rebuilt the registry underneath it. A re-derivation cannot desynchronize.
	if s != nil {
		if b, caps, ok := w.chatBound(s.id); ok {
			sender := chatSender{bridge: b}
			if caps.SendsImages {
				r.ToolRegistry["chat_send_image"] = &tools.ChatSendImageTool{
					CWD: r.CWD, Sandbox: w.sandbox, Sender: sender,
				}
			}
			if caps.SendsFiles {
				r.ToolRegistry["chat_send_file"] = &tools.ChatSendFileTool{
					CWD: r.CWD, Sandbox: w.sandbox, Sender: sender,
				}
			}
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
func setupWebExtensions(ctx context.Context, args build.Args, r *build.Resolved, version string, s *wsSession) (*extensions.Manager, func()) {
	// webExtHooks routes extension panel/status frames into the session's surface
	// registry (broadcasting to clients); other hooks stay non-interactive no-ops.
	extMgr := extensions.New(config.TervaHome(), r.CWD, version, r.Provider, r.Model, webExtHooks{s: s})
	extMgr.SetContextDisabled(r.DisableContextExtensions)
	extMgr.SetDisabledExtensions(r.DisableExtensions)
	extMgr.SetAllowedExtensions(args.WithExtensions)
	extMgr.SetConfigResolver(build.ResolveExtensionConfig)
	build.WireSessionReader(extMgr, config.TervaHome(), r.CWD)
	extMgr.SetProjectTrusted(r.Trusted)
	for _, e := range extMgr.LoadExplicit(ctx, args.Exts) {
		s.diag(fmt.Sprintf("extension load: %v", e))
	}
	if !args.NoExt {
		for _, e := range extMgr.Discover(ctx) {
			s.diag(fmt.Sprintf("extension load: %v", e))
		}
	}
	extMgr.WaitForReady(3 * time.Second)
	r.MergeExtensionTools(&build.ExtToolAdapter{Mgr: extMgr})
	return extMgr, func() { extMgr.Stop(2 * time.Second) }
}

// prompt starts a turn. The turn's context is derived from the workspace (not a
// client connection), so a client disconnecting mid-turn does not abort the run
// other clients are watching. Returns ErrBusy if a turn is already running.
func (s *wsSession) prompt(text string, images []ctrlproto.Image) error {
	return s.promptBlocks(text, toImageBlocks(images))
}

// promptBlocks is prompt over already-decoded image blocks. The chat bridge
// delivers provider.ImageBlock directly (it never speaks ctrlproto), and both
// entries must claim the same turn slot, so they share one body.
func (s *wsSession) promptBlocks(text string, imgs []provider.ImageBlock) error {
	s.mu.Lock()
	if s.turnCancel != nil {
		s.mu.Unlock()
		return ctrlproto.ErrBusy
	}
	turnCtx, cancel := context.WithCancel(s.ws.ctx)
	s.turnCtx, s.turnCancel = turnCtx, cancel
	s.mu.Unlock()

	go func() {
		// sink is nil: the agent's OnEvent (set in buildSession) fans every event
		// out, so Continue() and any internal re-prompt stream too.
		err := s.agent.PromptWithPolicy(turnCtx, text, imgs, nil)
		if err != nil && !errors.Is(err, context.Canceled) {
			s.broadcast(ctrlproto.ConversationEvent(core.WireEvent{Type: "error", Error: err.Error()}))
			// The banner is transient; persist the failure to the session's
			// error sidecar (alongside the transcript, not in it) so a red X is
			// recoverable after the fact. Best-effort — never compound the turn
			// failure with a logging failure.
			_ = s.sess.LogError(err.Error())
		}
		if err != nil {
			// The agent's own "done" does not fire on error returns (a
			// non-retryable provider failure, a cancel mid-stream or during a
			// retry sleep) — without a definitive completion event a stream
			// consumer's busy state would stick forever. Emitted after the
			// error event, so consumers see error → done. Clients must treat
			// "done" idempotently: the cancel-during-tools path emits the
			// agent's own done AND returns an error, producing two.
			s.broadcast(ctrlproto.ConversationEvent(core.WireEvent{Type: "done"}))
		}
		next, restart := s.endTurn(turnCtx, err)
		// Snapshot-on-done: the transcript now contains the sealed final step,
		// so re-broadcast the authoritative snapshot the way compact/clear do.
		// This converges any subscriber that attached mid-step — its initial
		// snapshot predates the step's seal (agent.Messages() lags the
		// per-tool result events), and the hub cannot replay events from
		// before it existed. Cost: one full-transcript frame per turn end to
		// every subscriber (image bytes ride by reference in-process).
		s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
		// The task board only mutates via task_* tool calls within a turn, so a
		// single surface_updated at turn end is the reasonable refresh cadence: it
		// keeps an open /tasks panel and the status-bar glance current without a
		// per-mutation poller. Web ignores the id (not offered as a tab).
		if s.hasTaskBoard() {
			s.broadcast(ctrlproto.SurfaceUpdatedEvent("taskboard"))
		}
		// After the turn, settle an untitled session's title and push it live, so
		// the header/list stop reading "new chat" without a page refresh. Uses the
		// workspace context (not the turn's) so it survives turn teardown.
		s.settleTitle(s.ws.ctx)
		// Post-turn auto-compact, mirroring the legacy TUI engine and the
		// rpc/chat hosts: condense while idle so the NEXT prompt doesn't pay
		// the summarization latency in PromptWithPolicy's pre-turn check. A
		// queued restart skips it — that turn's pre-turn policy covers it —
		// and a raced client prompt makes compact() return ErrBusy, which is
		// benign for an opportunistic pass.
		if err == nil && !restart && s.agent.ShouldAutoCompact(core.AutoCompactThreshold) && s.agent.CanCompact(core.AutoCompactKeepTail) {
			s.broadcast(ctrlproto.NoticeEvent("info", "", i18n.T("Context is nearly full — compacting the conversation.")))
			_ = s.compact(s.ws.ctx)
		}
		if restart {
			if perr := s.prompt(next, nil); perr != nil {
				// Raced a client Prompt that claimed the slot between endTurn
				// and here: re-arm at the front so the new turn's first safe
				// boundary delivers it instead of losing it.
				s.agent.RequeueFront(next)
				s.broadcastQueue()
			}
		}
	}()
	return nil
}

// endTurn atomically closes out a finished turn: it releases the busy slot and
// settles the pending queue under the same s.mu hold, mirroring the TUI turn
// engine's release semantics. A failed turn (error or cancel) drops the queue —
// stale follow-ups must not fire after an interrupt — while a clean turn shifts
// the queue's head for the caller to restart with. The agent drains its queue
// at safe boundaries within a turn, but a message queued after the last
// boundary check would otherwise strand until the next user prompt (a gap the
// web client had too). Holding s.mu across the release AND the shift means a
// concurrent queue() lands either before the shift (delivered by the restart)
// or after the release (sees idle and starts its own turn) — never in between.
func (s *wsSession) endTurn(turnCtx context.Context, err error) (next string, restart bool) {
	failed := err != nil || turnCtx.Err() != nil
	s.mu.Lock()
	s.turnCtx, s.turnCancel = nil, nil
	var dropped []string
	if failed {
		dropped = s.agent.DrainQueuedMessages()
	} else {
		next, restart = s.agent.ShiftQueuedMessage()
	}
	s.mu.Unlock()
	if restart || len(dropped) > 0 {
		s.broadcastQueue()
	}
	return next, restart
}

// argsSnapshot copies the session's resolved args under s.mu. Args is
// immutable after buildSession EXCEPT Approval (a live settings switch), so
// every Resolve-time reader must snapshot instead of touching s.args bare.
func (s *wsSession) argsSnapshot() build.Args {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.args
}

// rebuildTools re-resolves and swaps the agent's model-facing tool set: a fresh
// base registry + live extension tools + the workspace MCP tools + web built-ins.
// Fired on an extension reload (SetOnReload), after a live MCP toggle, and on a
// live approval-mode switch — a fresh Resolve carries neither ext nor MCP
// tools, so both are re-merged or a reload would silently drop them from the
// model; the approval mode rides args.Approval into buildToolRegistry and the
// merges, so plan mode withholds mutating tools from the rebuilt view.
//
// reason labels the trigger for the prompt_rebuilt notice (one of the values
// documented on ctrlproto.NoticePromptRebuilt); the notice is broadcast only
// when the pinned prefix actually changed — an identical re-install (the
// common case for e.g. a trust flip that added nothing) stays silent, since
// a byte-identical prefix breaks no cache.
func (s *wsSession) rebuildTools(reason string) {
	if s.agent == nil {
		return
	}
	args := s.argsSnapshot()
	rr, err := build.Resolve(args, true)
	if err != nil {
		return
	}
	// extMgr is always non-nil for a buildSession session; the guard keeps
	// the settings verbs (approval / auto-swarm) safe on bare fixtures, same
	// stance as the nil-tolerant apply helpers.
	if s.extMgr != nil {
		rr.MergeExtensionTools(&build.ExtToolAdapter{Mgr: s.extMgr})
	}
	if s.ws.mcpAdapter != nil {
		rr.MergeExtensionTools(s.ws.mcpAdapter)
	}
	s.ws.injectExtraTools(s, &rr, args)
	// Keep the workspace-shared sandbox (and its live /jail state) across the
	// rebuild — Resolve just minted a fresh unlocked one for the new tools.
	rr.UseSandbox(s.ws.sandbox)
	// Re-bind the ask channel. Unlike the confirmer — which lives on the
	// long-lived gate and so survives a rebuild untouched — the asker lives on
	// the tool instance, and Resolve just minted a fresh ask_user_question with
	// a nil Asker. Without this the rebuilt tool reports "no interactive
	// channel" for the rest of the session, with the front end sitting right
	// there, question dialog wired and waiting.
	//
	// Not a corner case: a rebuild fires before the first turn whenever an
	// extension asserts its tool policy, and again on entering plan mode — the
	// one mode that deliberately keeps ask_user_question, precisely so the agent
	// can ask when requirements are unclear.
	rr.SetAsker(&webAsker{s: s})
	rr.SetEscalator(&sessionEscalator{s: s})
	toolsChanged := s.agent.SetTools(rr.ToolRegistry)
	// The system prompt carries view state too — the prompt's tool list, the
	// auto-swarm nudge, an extension's static context — so install the
	// freshly-resolved render alongside the tools (same fidelity as
	// buildSession: both run the identical Resolve+merge pipeline). Pinned
	// per-turn like the tools, so it lands on the next turn.
	systemChanged := s.agent.SetSystem(rr.SystemPrompt)
	if toolsChanged || systemChanged {
		s.notifyPromptRebuilt(toolsChanged, systemChanged, reason)
	}
}

// notifyPromptRebuilt broadcasts the kinded notice that this session's pinned
// prompt prefix changed: the provider's prompt cache is invalidated, so the
// next turn re-reads the transcript uncached. Purely informational — the
// rebuild has already been applied (and lands at the next turn anyway, per
// the run loop's per-turn pin), so nothing blocks; a kind-aware client can
// filter or badge it, a plain one shows the text.
//
// One rebuild is expected at startup and carries no signal: an extension
// asserts its tool/context policy once the session cwd is known (e.g.
// terva-git-worktree withdrawing its tools outside a repo), which fires a
// "tool-withdrawal" / "extension-context" rebuild before the first turn. With
// no turn yet there is no prompt cache to invalidate, so a banner there just
// confuses the user about a non-event — it goes to the host diagnostic log
// instead. Everything else still surfaces: a user-initiated change (approval
// mode, trust, auto-swarm, …) notifies even pre-turn because the user acted
// and wants the confirmation, and any mid-session rebuild (a turn has run, so
// a cache really is being thrown away) notifies with the token count.
func (s *wsSession) notifyPromptRebuilt(toolsChanged, systemChanged bool, reason string) {
	scope := "both"
	switch {
	case toolsChanged && !systemChanged:
		scope = "tools"
	case systemChanged && !toolsChanged:
		scope = "system"
	}
	u := s.agent.LastTurnUsage()
	tokens := u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
	if tokens == 0 && isAutomaticRebuild(reason) {
		s.diag(fmt.Sprintf("prompt rebuilt (%s, scope=%s) before first turn — no cache to invalidate", reason, scope))
		return
	}
	data := map[string]string{"scope": scope, "reason": reason}
	text := i18n.T("prompt rebuilt (%s) — the next turn starts uncached", reason)
	if tokens > 0 {
		data["context_tokens"] = strconv.Itoa(tokens)
		text = i18n.T("prompt rebuilt (%s) — the next turn re-reads ~%d context tokens uncached", reason, tokens)
	}
	s.broadcast(ctrlproto.KindedNoticeEvent("info", ctrlproto.NoticePromptRebuilt, text, data))
}

// isAutomaticRebuild reports whether a prompt-rebuild reason is an extension
// asserting its own policy (not something the user just did). These are the
// only rebuilds worth suppressing pre-first-turn, when there is no cache to
// invalidate — a user-initiated rebuild always warrants its confirmation.
func isAutomaticRebuild(reason string) bool {
	switch reason {
	case "tool-withdrawal", "extension-context":
		return true
	default:
		return false
	}
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
	// A compacted session has outgrown its early title; refresh a
	// machine-generated one from the new summary anchor (never a manual
	// rename; gated on auto_title inside). Uses the workspace context so it
	// survives the caller — same reasoning as settleTitle at turn end.
	go s.retitleAfterCompaction(s.ws.ctx)
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
	// A clear reuses the compaction row as a floor marker. No summarizer ran,
	// so it cost nothing — zero usage, not the previous compaction's.
	_ = s.sess.AppendCompaction(nil, core.CompactResult{})
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
		s.rebuildTools("trust")
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
	// The cascading seed (compaction anchor + recent exchanges) degenerates
	// to the first user message here — settleTitle only ever runs on a
	// still-untitled session, i.e. right after the first exchange.
	gen := generateTitle(ctx, cl, model, core.BuildTitleSeed(s.agent.Messages(), core.TitleSeedBudget))
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

// applyTitle persists a machine-generated title to the session file (with
// the provenance marker automatic re-titling keys on), records it in memory,
// and broadcasts the change. Best-effort persistence: an update still
// reaches clients even if the append fails. Manual renames don't come here —
// they go through RenameSession (the verb), which writes a user row.
func (s *wsSession) applyTitle(title string) {
	if title == "" {
		return
	}
	if s.sess != nil {
		_ = core.RenameSessionGenerated(s.sess.Path, title)
	}
	s.setTitle(title, true)
	s.broadcast(ctrlproto.SessionUpdatedEvent(s.info()))
}

// retitleAfterCompaction refreshes a machine-generated title right after a
// compaction — the moment a session has provably outgrown the title its
// opening earned, and exactly when BuildTitleSeed gains a fresh anchor. Same
// gate as the automatic pass (auto_title governs tokens spent unasked) and
// the same client resolution; a manual rename is never touched, including
// one that lands while the model is thinking (re-checked before apply).
// Runs off the compact path (async) so neither the compact verb nor the
// turn-end auto-compact waits on a second model call.
func (s *wsSession) retitleAfterCompaction(ctx context.Context) {
	s.mu.Lock()
	prev, replaceable := s.title, s.titleGenerated || s.title == ""
	s.mu.Unlock()
	if !replaceable {
		return
	}
	ok, cl, model := s.ws.titleGen(s)
	if !ok || cl == nil {
		return
	}
	seed := core.BuildTitleSeed(s.agent.Messages(), core.TitleSeedBudget)
	if seed == "" {
		return
	}
	gen := generateTitle(ctx, cl, model, seed)
	if gen == "" || gen == prev {
		return
	}
	s.mu.Lock()
	raced := s.title != prev
	s.mu.Unlock()
	if raced {
		return
	}
	s.applyTitle(gen)
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
// multi-device interject story), broadcasting the new queue so every client's
// queued view converges. When the session is idle, a queue is a deferred
// Prompt (the interface contract's discretion) and starts a turn immediately —
// previously it stranded until the next user prompt. The enqueue happens under
// s.mu so it can't slip between a finishing turn's last boundary check and its
// endTurn queue shift (which holds the same lock).
func (s *wsSession) queue(text string) {
	s.mu.Lock()
	if s.turnCancel != nil {
		queued := s.agent.QueueMessage(text)
		s.mu.Unlock()
		if queued {
			s.broadcastQueue()
		}
		return
	}
	s.mu.Unlock()
	if err := s.prompt(text, nil); err != nil {
		// Raced a concurrent Prompt that claimed the slot first: queue onto the
		// turn that beat us (its boundaries / endTurn shift deliver it).
		if s.agent.QueueMessage(text) {
			s.broadcastQueue()
		}
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

// busy reports whether a turn is currently running (its cancel is armed). Used
// by the restart drain to wait for a cancelled turn to reach endTurn, which
// clears turnCancel.
func (s *wsSession) busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnCancel != nil
}

// recordCall notes the tool call about to be gated, so the web Confirmer can
// key its broadcast permission request to it.
func (s *wsSession) recordCall(id string) {
	s.mu.Lock()
	s.curCallID = id
	s.mu.Unlock()
}

func (s *wsSession) broadcast(ev ctrlproto.Event) { s.hub.broadcast(ev) }

// diag emits a host-side session-build diagnostic through the workspace's sink
// (os.Stderr by default; redirected by the in-process TUI carrier so it can't
// corrupt the full-screen UI). Nil-safe for sessions built outside a Workspace
// (tests). Callers hold w.mu (buildSession runs locked), so the field read is
// synchronized with SetDiag's write under the same lock.
func (s *wsSession) diag(msg string) {
	if s.ws != nil && s.ws.diag != nil {
		s.ws.diag(msg)
	}
}

// subscribe registers a new client. It receives a snapshot of the current
// transcript first (atomically, before any live event), then the live stream.
// Cancelling ctx unsubscribes and closes the channel. reliable selects the
// no-drop delivery discipline (see wsHub) — the in-process TUI carrier must
// never miss a text delta; networked carriers stay lossy.
func (s *wsSession) subscribe(ctx context.Context, reliable bool) <-chan ctrlproto.Event {
	ch := s.hub.add(func() ctrlproto.Event { return ctrlproto.SnapshotEvent(s.snapshot()) }, reliable)
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
		// Full form (image Data included) — same contract as the event
		// broadcast above; serialized carriers strip per negotiation.
		wm[i] = core.MessageToWireFull(m)
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
		Session:  s.info(),
		Messages: wm,
		// The transcript this window was cut from. The hub always broadcasts the WHOLE
		// thing (free in-process — the slices are shared); the serialization edge cuts
		// it down per client contract, and stamps Base. Total and Epoch are true of the
		// transcript either way, which is what lets a windowed client place what it got.
		Epoch:       s.agent.TranscriptEpoch(),
		Total:       len(wm),
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
	prov, model, title, persona, sub := s.provider, s.model, s.title, s.persona, s.subscription
	s.mu.Unlock()
	info := ctrlproto.SessionInfo{
		ID:           s.id,
		Title:        title,
		Provider:     prov,
		Model:        model,
		Persona:      persona,
		Path:         s.sess.Path,
		Created:      ctrlTimeString(s.sess.Meta.Started),
		Trusted:      s.trusted.Load(),
		Subscription: sub,
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

// setTitle records a title and its provenance (generated: machine titling —
// replaceable by automatic passes; not: a user rename — never clobbered).
func (s *wsSession) setTitle(t string, generated bool) {
	s.mu.Lock()
	s.title = t
	s.titleGenerated = generated
	s.mu.Unlock()
}

// close cancels any turn, tears down this session's extension subprocesses,
// closes all subscribers, and closes the session file.
func (s *wsSession) close() {
	// A mirror without a session has nowhere to deliver.
	if s.ws != nil {
		s.ws.chatStopForSession(s.id)
	}
	// Drop any open /btw snapshots: they pin a frozen transcript and a client,
	// and there is nothing to open them against once the session is gone.
	s.sidechatMu.Lock()
	s.sidechats = nil
	s.sidechatMu.Unlock()
	s.mu.Lock()
	c := s.turnCancel
	s.mu.Unlock()
	if c != nil {
		c()
	}
	if s.stopExt != nil {
		s.stopExt()
	}
	// Retire the --play warm-actor scene: stop + delete each live actor's swarm
	// agent so no dispatched child outlives the session (Release, never leak).
	if s.warmActors != nil {
		s.warmActors.Shutdown(func(id string) {
			_ = s.ws.swarm.Stop(id)
			_ = s.ws.swarm.Remove(id)
		})
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

// wsHub fans one session's events out to N subscribers. A subscriber is either
// lossy (a networked carrier: drops under backpressure to keep the turn moving —
// it can re-subscribe to resync from a fresh snapshot) or reliable (the
// in-process TUI carrier: never drops, because a lost text delta corrupts the
// rendered transcript). A reliable send blocks until the consumer drains — the
// same backpressure the TUI's old synchronous sink already applied.
//
// Locking discipline: h.mu guards ONLY the subscriber set and is never held
// across a send, so a slow/stuck reliable consumer cannot wedge add/remove/
// closeAll or other broadcasts (it used to: broadcast once held h.mu across
// the blocking send). h.sendMu serializes whole broadcasts, so every reliable
// subscriber observes the same total event order. Send/close races are
// settled per subscriber: sends hold the sub's own lock and bail on a dead
// sub; remove/closeAll close done first (unblocking any in-flight reliable
// send) and only then close the channel, under that same lock.
type wsHub struct {
	mu     sync.Mutex // subscriber set only — never held across a send
	sendMu sync.Mutex // serializes broadcasts: one total order for reliable subs
	subs   map[chan ctrlproto.Event]*wsSub
}

// wsSub is one subscription: the delivery channel plus the coordination
// that lets a removal interrupt an in-flight blocking send safely.
type wsSub struct {
	ch       chan ctrlproto.Event
	done     chan struct{} // closed on remove: unblocks a parked reliable send
	reliable bool

	mu   sync.Mutex // pairs sends with close: no send on a closed channel
	dead bool
}

// send delivers ev under the sub's own lock. A reliable send parks until
// the consumer drains OR the sub is removed (done); a lossy send drops on
// a full buffer.
func (s *wsSub) send(ev ctrlproto.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return
	}
	if s.reliable {
		select {
		case s.ch <- ev:
		case <-s.done:
			// Removed mid-send: the consumer is gone; drop the event.
		}
		return
	}
	select {
	case s.ch <- ev:
	default: // slow lossy subscriber: drop rather than stall the turn
	}
}

// close tears the sub down: done first, so an in-flight send parked on a
// full reliable channel exits, THEN the channel close under the send lock
// — the ordering that makes send-on-closed impossible. Called exactly
// once per sub (the hub's delete-under-mu gates it).
func (s *wsSub) close() {
	close(s.done)
	s.mu.Lock()
	s.dead = true
	close(s.ch)
	s.mu.Unlock()
}

// hubBuffer is the per-subscriber channel depth. A lossy subscriber that falls
// this far behind drops events (see broadcast); a reliable one blocks the
// broadcaster instead of losing an event.
const hubBuffer = 256

func newWSHub() *wsHub { return &wsHub{subs: map[chan ctrlproto.Event]*wsSub{}} }

// add creates a subscriber channel. If first is non-nil its result is enqueued
// before the channel joins the broadcast set, so a snapshot is guaranteed to
// precede any live event without a race. reliable picks the no-drop discipline.
func (h *wsHub) add(first func() ctrlproto.Event, reliable bool) chan ctrlproto.Event {
	s := &wsSub{
		ch:       make(chan ctrlproto.Event, hubBuffer),
		done:     make(chan struct{}),
		reliable: reliable,
	}
	h.mu.Lock()
	if first != nil {
		s.ch <- first()
	}
	h.subs[s.ch] = s
	h.mu.Unlock()
	return s.ch
}

func (h *wsHub) remove(ch chan ctrlproto.Event) {
	h.mu.Lock()
	s, ok := h.subs[ch]
	if ok {
		delete(h.subs, ch)
	}
	h.mu.Unlock()
	if ok {
		s.close()
	}
}

func (h *wsHub) broadcast(ev ctrlproto.Event) {
	h.sendMu.Lock()
	defer h.sendMu.Unlock()
	h.mu.Lock()
	list := make([]*wsSub, 0, len(h.subs))
	for _, s := range h.subs {
		list = append(list, s)
	}
	h.mu.Unlock()
	for _, s := range list {
		s.send(ev)
	}
}

func (h *wsHub) closeAll() {
	h.mu.Lock()
	list := make([]*wsSub, 0, len(h.subs))
	for ch, s := range h.subs {
		list = append(list, s)
		delete(h.subs, ch)
	}
	h.mu.Unlock()
	for _, s := range list {
		s.close()
	}
}
