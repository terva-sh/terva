package workspace

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/imagegen"
	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/agent/permissions"
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
	gate        *core.ConfirmGate      // nil in pure-yolo (no confirmation needed)
	extMgr      *extensions.Manager    // this session's extension subprocesses
	stopExt     func()                 // tears extMgr down on close
	roSet       *core.ReadOnlySet      // the live policy's read-only set, so a rebuild's merges land where the gate reads them (nil in pure-yolo)
	skillTool   *skills.Tool           // available skills, for /skill autocomplete (may be nil)
	tasks       *tasktool.Controller   // the built-in task board (nil when the session has no base workspace tools)
	memory      *tools.MemoryTool      // durable memory, bound once at session build (nil when --no-memory)
	files       *tools.FileState       // what the model has seen of each path; survives tool rebuilds
	loreEntries []lore.Entry           // discovered lore, for the lore inspector pane (nil when lore off)
	note        *build.NoteRecord      // live author's-note record (nil for a coding session); note.set writes it, the per-turn tail reads it
	user        *build.NoteRecord      // live user-persona description record (nil for a coding session); user.bind writes it, the per-turn tail reads it
	worldLore   *build.WorldLoreRecord // live World-lore record (nil for a coding session); world.lore.* writes it, the per-turn tail scans it
	loreFired   *build.LoreFiredRecord // the last turn's lore activation trace (which entries fired, why, what the budget dropped); refreshed on reloadLore
	// extReady is closed once this session's extensions have finished starting
	// AND their tools have been merged into the agent. launchTurn waits on it,
	// so the first turn can never go out against a half-registered extension
	// set. Nil on bare test fixtures that skip buildSession.
	extReady chan struct{}
	// actorCast + warmActors back the --play director's actor_spawn tool: the
	// closed declared cast and the live-actor cache that survives registry
	// rebuilds (so re-injecting actor_spawn on reload keeps the warm scene).
	// Built once in buildSession when castSkinActive; nil otherwise.
	actorCast  map[string]tools.CastMember
	warmActors *tools.WarmActors
	// imageRegistry is the session's resolved image-generation backends, retained
	// so backgrounds.generate can paint a scene (the model-facing generate_image
	// tool holds the same registry). nil/empty when no backend is configured.
	imageRegistry *imagegen.Registry
	args          build.Args  // this session's resolved args (for tool rebuilds on reload/MCP toggle)
	cwd           string      // resolved workspace dir (for the extensions inventory scan)
	trusted       atomic.Bool // workspace trust (project extensions/skills gating); mutated live by Trust/Untrust
	hub           *wsHub
	subscription  bool // credential is an OAuth ("sub") token, not a paid api key

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
	turnCancel     context.CancelFunc                     // non-nil while a turn runs
	permPark       core.ParkTable[core.ConfirmDecision]   // parked webConfirmer/workerConfirmer waits
	askPark        core.ParkTable[[]core.UserAnswer]      // parked webAsker waits (one answer per question)
	permReq        map[string]ctrlproto.PermissionRequest // details for the snapshot
	askReq         map[string]ctrlproto.AskRequest        // details for the snapshot
	askSeq         uint64
	// tail is the current tail span's swipe state — the ONE switchable span.
	// Seeded from the session file at materialize (a session may load with
	// retract/select rows already written: pre-seeded greetings, or a retry from
	// before this daemon started) and moved by swipe. A new turn, a tail edit, a
	// delete, or a compaction commits or invalidates the alternatives, so those
	// paths clear it. len(takes) < 2 means there is nothing to swipe. Guarded by mu.
	tail tailVariants
	// msgVars is the message-scoped variant state (Option C), by effective index —
	// an older message edited into swipeable alternatives with its downstream
	// shared. Independent of tail and NOT cleared by a new turn. Seeded from the
	// file at materialize (counts only; a resumed position's takes hydrate lazily
	// on the first swipe) and grown by an older-message edit. Guarded by mu.
	msgVars map[int]*msgVarLive

	// reviseBase re-anchors this session's in-memory transcript indices to the
	// on-disk effective transcript. A resume trims the in-memory transcript to a
	// recent window (TrimMessagesForResume) while the file keeps the full history,
	// so in-memory index i is on-disk index i+reviseBase. Revise verbs mutate the
	// live (trimmed) agent by the in-memory index but must PERSIST amends against
	// the on-disk index — walkSession replays them over the full transcript on the
	// next reload — so every amend/fork index crosses diskIndex first. reviseHead
	// marks that the trim prepended the compaction summary as in-memory index 0
	// (which maps to on-disk 0, not +base, and is not itself revisable). Both are
	// zero/false for a session that was not trimmed, making diskIndex the identity
	// and the common case unchanged. Set in buildSession, then immutable.
	reviseBase int
	reviseHead bool

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
// and event fan-out. msgs is the resumed transcript (nil for a fresh session);
// reviseBase/reviseHead re-anchor its indices to the on-disk transcript when a
// resume trimmed it (both zero for fresh/untrimmed — see wsSession.reviseBase).
func (w *Workspace) buildSession(id string, sess *core.Session, msgs []provider.Message, reviseBase int, reviseHead bool) (*wsSession, error) {
	args := w.args
	if sess.Meta.Provider != "" {
		args.Provider = sess.Meta.Provider
	}
	if sess.Meta.Model != "" {
		args.Model = sess.Meta.Model
	}
	// The per-session creation spec — persona plus the immersive (Stage) fields —
	// is persisted in session meta (SetCreationSpec), so it overrides the
	// workspace launch defaults on every build, including a fresh materialize
	// after a daemon restart. Resolve turns these into the active persona,
	// experience mode, card identity, and cast.
	// replayedPersona is the persona this session persisted, remembered so the
	// resolve below can tell "the session named a persona that is gone" apart
	// from every other reason a build fails. Empty when the session names none.
	replayedPersona := ""
	if sess.Meta.Persona != "" {
		args.Persona = sess.Meta.Persona
		replayedPersona = sess.Meta.Persona
	}
	if sess.Meta.Experience != "" {
		args.Experience = sess.Meta.Experience
	}
	if sess.Meta.Card != "" {
		args.Card = sess.Meta.Card
	}
	if len(sess.Meta.Cast) > 0 {
		args.Cast = sess.Meta.Cast
	}
	if sess.Meta.Greeting != 0 {
		args.Greeting = sess.Meta.Greeting
	}
	// A bound user-persona NAME is the card {{user}} macro, so it threads into the
	// build like any other creation-spec field (Args.As feeds resolveCardUserName)
	// and bakes into the cached prefix — re-applied on every materialize, including
	// a restart. The DESCRIPTION rides the per-turn tail instead (seeded below).
	if sess.Meta.UserName != "" {
		args.As = sess.Meta.UserName
	}
	// Gender/pronouns ride the uncached per-turn tail (the user-persona frame), not
	// the {{user}} macro, so they thread through unconditionally — re-applied on
	// every materialize, including a restart.
	args.UserGender = sess.Meta.UserGender
	args.UserPronouns = sess.Meta.UserPronouns

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
		permReq:        map[string]ctrlproto.PermissionRequest{},
		askReq:         map[string]ctrlproto.AskRequest{},
		extPanels:      map[string]*webPanel{},
		reviseBase:     reviseBase,
		reviseHead:     reviseHead,
	}

	pol, warns := permissions.BuildPolicy(args.PermInputs())
	for _, wn := range warns {
		s.diag(fmt.Sprintf("note: %s", wn))
	}

	r, err := build.Resolve(args, true)
	if err != nil && replayedPersona != "" {
		// The session names a persona that no longer resolves — deleted from the
		// user library, or in an extension bundle that is not loaded. Replaying it
		// unconditionally made that a refusal to open the session AT ALL: the
		// transcript is intact and simply unreachable, over a name, with no way
		// back a user could guess (recreate a persona spelled exactly that).
		//
		// So fall back to the workspace default and say so. A session that opens
		// in a different voice is recoverable — recreate the persona, or rebind
		// it — while one that will not open is not, and the trigger is an ordinary
		// workflow: duplicate a built-in to edit it, play, delete the copy.
		//
		// Retried rather than pre-checked so the "does this persona resolve" rule
		// stays in exactly one place (build's own resolution, extension bundles
		// and namespacing included). If the persona was NOT the problem the retry
		// fails the same way, and the original error is what the caller sees.
		retry := args
		retry.Persona = w.args.Persona
		if rr, rerr := build.Resolve(retry, true); rerr == nil {
			args, r, err = retry, rr, nil
			s.diag(fmt.Sprintf("note: %s", i18n.T(
				"persona %q is no longer available, so this session opened with the default one instead; recreate it, or pick another, to change that",
				replayedPersona)))
		}
	}
	if err != nil {
		return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "resolve: %v", err)
	}

	var gate *core.ConfirmGate
	if pol != nil {
		gate = core.NewPolicyGate(pol, &webConfirmer{s: s})
		// The confirm dialog's "always this tool — save to config" answer
		// (ConfirmDecision.PersistTool) is honoured here. This is the ONLY
		// production gate that installs the persist callback, and it serves
		// both surfaces that reach a durable grant: the web app and the
		// TUI-over-ctrlproto client both approve through this session's gate,
		// so wiring it once makes the TUI dialog's promise true as well. The
		// steps mirror the /permissions pane's add_rule exactly — write a
		// USER-scope allow rule (the self-approval ban forbids only PROJECT
		// allow), re-apply live so every session honours it without a restart,
		// and refresh an open pane. addUserPermissionRule is idempotent, so a
		// repeated grant is a no-op.
		gate.SetPersist(func(tool, argsPattern string) {
			// argsPattern narrows the rule to the derived command scope
			// ("^git status(?:\s|$)"); "" is the blanket grant. The pattern
			// normally round-trips from our own deriver, but it crosses the
			// wire through the client, so verify it still compiles rather
			// than persisting a rule the compile layer would drop with a
			// warning on every startup.
			if argsPattern != "" {
				if _, err := regexp.Compile(argsPattern); err != nil {
					s.diag(fmt.Sprintf("note: ignoring always-allow rule for %q: bad args pattern %q: %v", tool, argsPattern, err))
					return
				}
			}
			if err := addUserPermissionRule(config.PermissionRuleConfig{
				Tool:     tool,
				Args:     argsPattern,
				Decision: string(core.RuleAllow),
			}); err != nil {
				s.diag(fmt.Sprintf("note: could not save always-allow rule for %q: %v", tool, err))
				return
			}
			s.ws.refreshAllPolicies()
			s.ws.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("permissions"))
		})
		// The deriver that turns a bash call into the scoped "always allow
		// <command>" options the dialog offers; bash-only, injected here so
		// core stays free of shell parsing (same layering as the policy's
		// DecomposeCommand).
		gate.SetScopeDeriver(permissions.DeriveGrantScopes)
		r.AdoptReadOnlySet(pol.ReadOnly)
		// Retain it: every later rebuild re-merges extension and MCP tools, and
		// MergeToolsForMode registers a read_only tool's name into whichever set
		// the Resolved carries. A fresh Resolve carries a fresh one, so without
		// this the gate would stop auto-allowing an extension's read-only tools
		// after any rebuild — and the startup merge IS a rebuild now.
		s.roSet = pol.ReadOnly
	}
	// See workspace_toolchannels.go: these live on the tool INSTANCE, so the
	// identical call has to run again on every rebuild.
	s.bindResolvedChannels(&r)

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
			// Overlay the persisted per-actor model pins (Phase 7) onto the built cast.
			applyCastModels(cast, sess.Meta.CastModels)
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
	// Retain the memory tool for the same reason as the task board: the pane and
	// the injected block read the stores it bound, and a rebuild must not swap
	// them out from under either.
	s.memory, _ = r.ToolRegistry["memory"].(*tools.MemoryTool)
	// Same retention rule as the task board and memory: a rebuild must not
	// forget which files the model has read.
	s.files = r.Files()
	s.args = args
	s.cwd = r.CWD
	// Retain the live author's-note record (immersive sessions only) and seed it
	// from the persisted meta, so note.set updates the value the per-turn tail
	// reads and the note survives a daemon restart. The tail closure holds the
	// same pointer (via r.NewAgent above), so a later Set is visible next turn.
	if nr := r.Note(); nr != nil {
		nr.Set(s.sess.Meta.Note)
		s.note = nr
	}
	// Retain the live user-persona description record the same way — seeded from
	// meta so user.bind updates the tail value and it survives a restart. (The
	// NAME half already threaded into args.As above, baked into the prefix.)
	if ur := r.User(); ur != nil {
		ur.Set(s.sess.Meta.UserDescription)
		s.user = ur
	}
	// Retain the live World-lore record the same way — seeded from meta so
	// world.lore.* edits update the entries the per-turn tail scans and the
	// World's lore survives a restart. Audience-filtered (L2) for who the tail
	// speaks for: the bound character in a chat, the director in play.
	if wr := r.WorldLore(); wr != nil {
		s.worldLore = wr
		wr.Set(build.WorldLoreEntries(worldLoreFor(s.sess.Meta.WorldLore, s.tailLoreAudience())))
	}
	// Retain the lore activation-trace record the per-turn tail writes, so the
	// lore surface and /context can show what fired last turn (reloadLore swaps
	// the provider and re-points this — see workspace_lore.go).
	s.loreFired = r.LoreFired()
	// Retain the image-backend registry so backgrounds.generate can paint a scene
	// off the same backends the generate_image tool uses.
	s.imageRegistry = r.ImageRegistry
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

	// Canonical tool-call ladder (hooks → gate → ext intercept). The gate
	// hands the call id straight to the confirmer (ConfirmWithCall), so no
	// "current call" session state exists to collide when a host_tool_call
	// approval parks concurrently with a model call's.
	hookEng := w.hookEng
	ag.BeforeToolExecute = build.BuildBeforeToolExecute(hookEng, gate, extMgr)
	s.bindAgentChannels(ag, gate)
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
	}
	// The live cards the model reads each turn. Outside the check above: the
	// task board is not an extension — its card follows r.Tasks, not the
	// manager — and it was nested there, which made a built-in board's
	// visibility depend on whether this session had extensions.
	//
	// The order lives in build.EphemeralTail, which reloadLore reproduces on
	// every lore edit, user-persona change and trust flip. It used to re-derive
	// the run's tail and install it bare, taking both cards away.
	build.WireEphemeralTail(ag, s.ephemeralTail())
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
	} else if sess.Meta.Experience != "" && len(r.CardGreetings) > 0 {
		// A brand-new immersive session: seed the card's openings — first_mes plus
		// every alternate_greeting — as message-0 swipe variants, the selected one
		// active, so the character greets the user AND the user can swipe between
		// openings. The greeting is DEFERRED (held in memory, not written), so a chat
		// the user only previews stays a meta-only draft the prune gates discard; the
		// first real turn flushes it (core.DeferGreetingVariants). The tail-span swipe
		// state is set in memory here — the on-disk rows seedTail reads don't exist
		// yet — and re-derived from meta on a reload before the first turn.
		s.seedDeferredGreeting(sess, r.CardGreetings)
	}

	// Seed the tail-span swipe state for an immersive session, so alternatives —
	// pre-seeded greeting variants (just written above) or a retry's takes written
	// before this daemon started — are switchable. Gated to immersive: it re-reads
	// the file, and swipe is a Stage interaction, so a coding-session build pays
	// nothing. seedTail is a no-op when there are fewer than two takes.
	if sess.Meta.Experience != "" {
		s.seedTail()
		s.seedMsgVars()
	}

	// First half of the shared session-binding event: identity and the task
	// board, at session-open. The announcement is the second half, below —
	// the daemon splits it for the same reason rpc does, because its extensions
	// start in the background. The ORDER the split preserves is the point: the
	// board is loaded before anything is told the session exists.
	build.BindSession(build.SessionBinding{Agent: ag, Tasks: r.Tasks, Session: sess})

	// Extensions start in the background (setupWebExtensions), so the session
	// materializes without waiting on subprocess handshakes. Everything that
	// needs a live extension is deferred to here: fold their tools into the
	// agent, then announce the session so a session-keyed extension (e.g.
	// memory) learns the real id. launchTurn blocks on extReady, so both land
	// before the model's first turn — the same guarantee the synchronous build
	// gave, just paid out of the user's typing time instead of their startup.
	s.extReady = make(chan struct{})
	go func() {
		defer close(s.extReady)
		extMgr.AwaitStarted(w.ctx)
		if w.ctx.Err() != nil {
			return // daemon shutting down; nothing left to rebuild for
		}
		// A session with no extensions loaded has nothing to merge, and the
		// rebuild is a full re-Resolve — skip it rather than burn it on a no-op.
		if extMgr.Count() > 0 {
			s.rebuildTools("extensions-ready")
		}
		build.BindSession(build.SessionBinding{Ext: extMgr, Session: sess})
	}()
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
	// swarm_spawn: the workspace host is its ONLY registrant — no other mode
	// offers it (docs/architecture/06-extensibility.md §1.5). Registered here to
	// close the prompt/tool gap: Resolve ships the auto-swarm system addendum,
	// so the tool must exist to match.
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
			// Gate + validate the `backend` param (dispatch to a foreign worker).
			// Reads the external-workers knob live and checks the worker registry;
			// nil-safe by absence would refuse every backend, so it is always
			// wired here — the gate is inside the closure, not in whether it exists.
			AllowBackend: allowWorkerBackend,
			// Track each spawn so the coordinator gets the [auto-swarm update]
			// recap when the batch finishes (the carrier twin of the legacy
			// OnSpawned wiring; without it a coordinator never learns they're done).
			OnSpawned: s.trackSwarmAgent,
		}
	}
	// terva_status reports spend by sub-agents that are still RUNNING, which the
	// session's delegated total cannot include (a child is booked only when its
	// recap flushes). Bound here rather than in Resolve because the swarm belongs
	// to the workspace, and bound on every rebuild for the same reason
	// swarm_spawn is: a rebuilt registry mints a fresh StatusTool, and a status
	// tool that silently lost its swarm would under-report during exactly the
	// window this exists for.
	//
	// Not gated on AutoSwarmEnabled: agents dispatched manually through /swarm
	// spend real money too, and a nil swarm reports nothing anyway.
	if w.swarm != nil {
		if st, ok := r.ToolRegistry["terva_status"].(*tools.StatusTool); ok {
			st.Swarm = w.swarm
		}
	}
	// The model's World hands (Worlds W4b): the play director records world
	// knowledge (world_note) and marks characters learning secrets
	// (world_reveal). Play-only — chat is pure conversation (no tools), and
	// these belong to the scene authority anyway. Bounded on purpose: append +
	// reveal; edit/delete/promotion stay user verbs.
	if s != nil && args.Experience == build.ExperiencePlay && !args.NoTools {
		r.ToolRegistry["world_note"] = &worldNoteTool{s: s}
		r.ToolRegistry["world_reveal"] = &worldRevealTool{s: s}
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
			// Worlds W6: each dispatch renders the audience-filtered lore block
			// for the named actor against the current situation + scene tail —
			// the same L2 seam voiceLine uses in chat, so what an actor "knows"
			// is governed by the one lorebook, not by which surface voices them.
			WorldLore: func(actor, situation string) string {
				return s.worldLoreBlock(situation, actor)
			},
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
	// share_file: hand a local file to whoever is holding this session — the web
	// panel renders it as an image, a player, or a download. Bound to THIS
	// session, like the chat tools below, so a share can only ever land in the
	// conversation that produced it.
	//
	// Base workspace sessions only, on the skin gate that keeps raati_convene out
	// of --chat/--play: those sessions have no filesystem tools to produce a file
	// with, and an immersive scene is not a place to hand someone a download.
	// Where they DO want to send the user a picture, that is chat_send_image
	// below, over the bridge they are actually being held on.
	if s != nil && build.HasBaseWorkspaceTools(args) {
		r.ToolRegistry["share_file"] = &tools.ShareFileTool{
			CWD: r.CWD, Sandbox: w.sandbox, Publisher: sharePublisher{w: w, sess: s.id},
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
		// terva_arm_restart: declare a planned SUPERVISOR restart (the agent runs
		// the supervisor command itself — e.g. a unit change terva_restart's
		// self-exec cannot apply) so the interrupted call reconciles as planned and
		// this exact session resumes. Same self-restart gate; also unclassified in
		// permissions so it prompts before recording intent. Bound to this session.
		r.ToolRegistry["terva_arm_restart"] = &tools.ArmRestartTool{Session: s.id, Home: w.root}
	}
}

// setupWebExtensions builds this session's extension manager and STARTS it in
// the background. Mirrors the ACP per-session setup (extensions only — no
// connectors, no MCP), returning a stop closure the session calls on close.
// The manager is always non-nil (a --no-ext session just has an empty tool
// set).
//
// Unlike the synchronous hosts it does NOT merge extension tools into r: the
// subprocess handshake and ready wait are up to ~6s of latency terva does not
// control, and paying them here means a client stares at nothing while an npx
// cold start finishes. buildSession folds the tools in as soon as the start
// completes, and launchTurn blocks on that signal, so the model still never
// sees a turn with a half-registered extension set.
func setupWebExtensions(ctx context.Context, args build.Args, r *build.Resolved, version string, s *wsSession) (*extensions.Manager, func()) {
	// webExtHooks routes extension panel/status frames into the session's surface
	// registry (broadcasting to clients); other hooks stay non-interactive no-ops.
	extMgr := build.NewExtensionManager(config.TervaHome(), r.CWD, version, r.Provider, r.Model, webExtHooks{s: s})
	extMgr.SetContextDisabled(r.DisableContextExtensions)
	extMgr.SetDisabledExtensions(r.DisableExtensions)
	extMgr.SetAllowedExtensions(args.WithExtensions)
	extMgr.SetConfigResolver(build.ResolveExtensionConfig)
	build.WireSessionReader(extMgr, config.TervaHome(), r.CWD)
	extMgr.SetProjectTrusted(r.Trusted)
	// Cancelling the start ctx on close aborts an in-flight spawn rather than
	// leaving Stop to race it (Driver.Load rolls a cancelled claim back).
	startCtx, cancelStart := context.WithCancel(ctx)
	extMgr.StartAsync(startCtx, args.Exts, !args.NoExt, webStartupReadyGrace, func(err error) {
		s.diag(fmt.Sprintf("extension load: %v", err))
	})
	return extMgr, func() {
		cancelStart()
		extMgr.Stop(2 * time.Second)
	}
}

// webStartupReadyGrace is how long a freshly spawned extension has to signal
// ready before the session proceeds without it. Same value the reload path and
// every other host use; it costs no startup latency here because the wait runs
// behind the session build (see setupWebExtensions).
const webStartupReadyGrace = 3 * time.Second

// prompt starts a turn. The turn's context is derived from the workspace (not a
// client connection), so a client disconnecting mid-turn does not abort the run
// other clients are watching. Returns ErrBusy if a turn is already running.
// A chat World with a roster routes through the meta-narrator first (Worlds
// W3, workspace_route.go); everything else — including the queue-restart path,
// which re-enters here — is today's turn.
func (s *wsSession) prompt(text string, images []ctrlproto.Image, extras core.UserMessageExtras) error {
	if s.shouldRoute(text, images) {
		return s.routedTurn(text)
	}
	return s.promptBlocks(text, toImageBlocks(images), extras)
}

// promptBlocks is prompt over already-decoded image blocks. The chat bridge
// delivers provider.ImageBlock directly (it never speaks ctrlproto), and both
// entries must claim the same turn slot, so they share one body.
func (s *wsSession) promptBlocks(text string, imgs []provider.ImageBlock, extras core.UserMessageExtras) error {
	turnCtx, err := s.beginTurn()
	if err != nil {
		return err
	}
	// A new user turn commits the tail span (its pre-seeded greeting or swipe
	// choice is now history); the freshly generated response has no alternatives
	// until it is retried, so drop the switchable set.
	s.clearTail()
	s.launchTurn(turnCtx, func(ctx context.Context) error {
		// sink is nil: the agent's OnEvent (set in buildSession) fans every event
		// out, so Continue() and any internal re-prompt stream too.
		return s.agent.PromptWithPolicyExtra(ctx, text, imgs, extras, nil)
	}, nil)
	return nil
}

// beginTurn claims the single turn slot and returns the turn context, or ErrBusy
// if a turn is already running. The context is derived from the workspace (not a
// client connection), so a client disconnecting mid-turn does not abort the run
// other clients are watching. The caller runs the turn with launchTurn.
// awaitExtensions blocks until this session's background extension start has
// finished and its tools have been merged into the agent, or ctx is done. A
// session built without the async start (a bare test fixture) returns at once.
//
// The agent pins its tool set once per turn, before the first model call, so
// waiting here — rather than at prompt time — is what makes the first turn see
// the same registry the synchronous build used to guarantee.
func (s *wsSession) awaitExtensions(ctx context.Context) {
	if s.extReady == nil {
		return
	}
	select {
	case <-s.extReady:
	case <-ctx.Done():
	}
}

func (s *wsSession) beginTurn() (context.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turnCancel != nil {
		return nil, ctrlproto.ErrBusy
	}
	turnCtx, cancel := context.WithCancel(s.ws.ctx)
	s.turnCtx, s.turnCancel = turnCtx, cancel
	return turnCtx, nil
}

// launchTurn runs the turn goroutine shared by prompt and retry: it invokes gen
// (a user prompt or a regenerate), fans out error/done, settles the turn, runs
// afterTurn once the transcript is sealed (retry re-derives its tail variants
// there), re-broadcasts the authoritative snapshot, and handles titling,
// post-turn auto-compact, and the queued-message restart. The caller must have
// claimed the slot with beginTurn.
//
// ErrBusy is excluded from both the error banner and the sidecar, alongside
// Canceled. It means a run was already in progress and this one was refused —
// a control-flow rejection, not a turn that died. The post-turn auto-compact
// races a client prompt by design (the call site says so: "benign for an
// opportunistic pass"), and the loser's ErrBusy was landing in the sidecar as
// if it were a provider failure. In a reviewed 25-hour session that was the
// ONLY sidecar row, and it was spurious — the compaction it lost to succeeded.
// This matters more now that session_inspect reads the sidecar and interleaves
// it with the transcript: a rejection that never started is not a turn death,
// and counting it as one misdirects the next review. The sidecar's job is to
// explain turns that died.
func (s *wsSession) launchTurn(turnCtx context.Context, gen func(context.Context) error, afterTurn func()) {
	go func() {
		// Extensions load in the background so the session materializes at
		// once; this is where that debt comes due. The turn slot is already
		// claimed, so a client shows a running turn rather than a stalled
		// request — and in practice the wait is zero, because the user spent
		// longer typing than the subprocesses spent handshaking.
		s.awaitExtensions(turnCtx)
		err := gen(turnCtx)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, core.ErrBusy) {
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
		if afterTurn != nil {
			afterTurn()
		}
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
		// A Stage session opens on the character's name (instant, free) and upgrades
		// to a scene summary once there is a scene. Guarded to fire once, so this is
		// a cheap no-op on every turn after it lands. Async: a title call must not
		// hold up the turn's teardown or the auto-compact below.
		go s.upgradeImmersiveTitle(s.ws.ctx)
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
			if perr := s.prompt(next, nil, core.UserMessageExtras{}); perr != nil {
				// Raced a client Prompt that claimed the slot between endTurn
				// and here: re-arm at the front so the new turn's first safe
				// boundary delivers it instead of losing it.
				s.agent.RequeueFront(next)
				s.broadcastQueue()
			}
		}
	}()
}

// retry regenerates the last response, keeping the current one as a swipeable
// take (SillyTavern's regenerate). It retracts the tail span — persists a retract
// amend, sets the current response aside, and truncates the transcript back to
// the last user turn — then runs a fresh generation against that prefix. When the
// turn seals, the tail's variants (the kept response plus the new one) are
// re-derived from disk, so both are switchable via turn.swipe. Like prompt it
// returns once the turn is accepted; the regeneration streams to subscribers.
// p.Guidance turns it into a GUIDED regenerate: the same retract-and-rerun, with
// a one-turn cue carrying what the user wants different (and, unless
// p.IgnorePrior, the withdrawn take itself, since guidance is usually relative).
// The cue is request-scoped and never persisted — see retryCue.
//
// Epoch/busy guarding matches edit/delete/swipe.
func (s *wsSession) retry(p ctrlproto.TurnRetryParams) error {
	if err := s.reviseGuard(p.Epoch); err != nil {
		return err
	}
	msgs := s.agent.Messages()
	idx := lastResponseStart(msgs)
	// idx == 0 means the transcript opens with model output nobody has answered —
	// a card greeting, possibly with advances on top. Retracting from there would
	// take the greeting with it, and a greeting is card content with its own swipe,
	// not a take this verb owns. Left refused, as it always was.
	if idx <= 0 || idx >= len(msgs) {
		// A scene ending on something the AUTHOR wrote gets its own sentence:
		// "nothing to retry" reads as a malfunction when a reply is plainly on
		// screen — it just is not one the model produced.
		if idx >= len(msgs) && len(msgs) > 0 && isAuthoredLine(msgs[len(msgs)-1]) {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s",
				i18n.T("this scene ends on a line you wrote — there is no model take to regenerate. Use advance to continue the scene."))
		}
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("nothing to retry"))
	}
	// Compose the cue BEFORE the truncation below removes the span it quotes.
	cue := retryCue(p, msgs[idx:])
	turnCtx, err := s.beginTurn()
	if err != nil {
		return err
	}
	// Retract the current response: persist the amend (so a reload reconstructs
	// the kept take), set it aside, and truncate to the last user turn. clearTail
	// drops the stale in-memory span; seedTail rebuilds it from disk once the new
	// take is generated and persisted. The amend is written against the on-disk
	// index (diskIndex) so a reload retracts the right span; the live agent is
	// truncated by the in-memory index.
	if disk, ok := s.diskIndex(idx); ok {
		_ = s.sess.AppendAmend(core.AmendRetract, disk, nil, "retry")
	}
	s.agent.TruncateTo(idx)
	s.clearTail()
	s.launchTurn(turnCtx, func(ctx context.Context) error {
		// An empty cue is exactly Continue, so an unguided regenerate keeps its
		// old behaviour to the byte: an independent sample from the same prefix.
		return s.agent.ContinueWithCue(ctx, nil, cue)
	}, s.seedTail)
	return nil
}

// retryCue builds the one-turn steer for a guided regenerate, or "" for a plain
// one. span is the take being withdrawn (the transcript from the retract point
// on), captured before the caller truncates it away.
//
// The prior take rides along by DEFAULT because regenerate guidance is almost
// always relative — "shorter", "less purple", "have her refuse instead" — and a
// model that cannot see what it is being asked to change is guessing at the
// referent. IgnorePrior is the opt-out for when the last attempt went somewhere
// the user wants no trace of, where showing it would anchor the retry to exactly
// what they are trying to get away from.
func retryCue(p ctrlproto.TurnRetryParams, span []provider.Message) string {
	guidance := strings.TrimSpace(p.Guidance)
	if guidance == "" {
		return "" // a plain regenerate steers nothing and quotes nothing
	}
	cue := i18n.P("stage.retry.cue",
		"[Retry] Your previous reply has been withdrawn and you are writing it again. What the user wants different this time: %s",
		guidance)
	if p.IgnorePrior {
		return cue + "\n\n" + i18n.P("stage.retry.instruction",
			"Write the reply itself — do not mention this note, the withdrawn attempt, or the fact that you are retrying, and do not begin mid-sentence.")
	}
	if prior := priorTakeText(span); prior != "" {
		cue += "\n\n" + i18n.P("stage.retry.prior", "The withdrawn attempt was:\n\n%s", prior)
		return cue + "\n\n" + i18n.P("stage.retry.instruction_prior",
			"Write a fresh reply that follows the guidance. Do not reuse the withdrawn attempt's phrasing or structure, do not mention this note or the fact that you are retrying, and do not begin mid-sentence.")
	}
	return cue + "\n\n" + i18n.P("stage.retry.instruction",
		"Write the reply itself — do not mention this note, the withdrawn attempt, or the fact that you are retrying, and do not begin mid-sentence.")
}

// retryPriorLimit caps how much of a withdrawn take rides in a retry cue. A model
// take is bounded by its own max_tokens, so this never fires on the Stage prose
// it was written for; it is the guard for an agent span that spent a turn
// accumulating tool traffic, where quoting the whole thing back would cost more
// than the regeneration.
const retryPriorLimit = 8000

// priorTakeText renders the withdrawn span as the model should see it: its prose
// only, tool calls and reasoning left out (they are machinery, not the reply the
// user is asking to change), truncated at retryPriorLimit.
func priorTakeText(span []provider.Message) string {
	var parts []string
	for _, m := range span {
		if m.Role != provider.RoleAssistant {
			continue
		}
		if prose := messageProse(m); prose != "" {
			parts = append(parts, prose)
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if len(text) > retryPriorLimit {
		text = strings.TrimSpace(text[:retryPriorLimit]) + i18n.P("stage.retry.prior_truncated", "\n\n[…truncated]")
	}
	return text
}

// isAuthoredLine reports whether the author wrote this message rather than the
// model producing it: their own turn, or a line they directed into the scene
// (postDirected appends those as ASSISTANT messages tagged core.MetaDirected, and
// SD5's cold open arrives the same way).
//
// Both are boundaries for a regenerate, and for the same reason: a retract span
// may contain only model output. Role alone cannot tell them apart, which is what
// made a directed line look like a take.
func isAuthoredLine(m provider.Message) bool {
	return m.Role == provider.RoleUser || m.Meta[core.MetaSource] == core.MetaDirected
}

// lastResponseStart is the index where the last response span begins: just after
// the last thing the AUTHOR put in the scene. It is the retract point for a
// regenerate — everything from here to the end is model output, which is the only
// thing this verb may throw away.
//
// It used to anchor on the last USER message alone, and that had two costs. A
// scene ending in "[directed line, model reply]" put the authored line inside the
// retract span, so retry refused the whole thing rather than regenerating the
// reply — protecting the line by declining to work. And a session whose transcript
// holds no user message at all (SD5 opens a scene on a cold open, and ▶ Advance
// answers without you typing) had no anchor, so retry reported "nothing to retry"
// with a real model take on screen. Anchoring on authorship instead of on role
// fixes both: the authored line bounds the span rather than poisoning it.
//
// Returns 0 when the author has put nothing in the scene yet — a greeting, or a
// greeting plus advances. The caller refuses that: the greeting is card content
// with its own swipe, not a take to regenerate.
func lastResponseStart(msgs []provider.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if isAuthoredLine(msgs[i]) {
			return i + 1
		}
	}
	return 0
}

// reviseBaseFor computes the re-anchoring offset for a resumed transcript that a
// window trim shortened. full is the untrimmed effective transcript OpenSession
// reconstructed; trimmed is what TrimMessagesForResume kept in memory. base is the
// count of leading effective messages the trim dropped — add it to an in-memory
// index to get the on-disk one. head is true when the trim prepended the
// compaction summary as trimmed[0]: that row maps to on-disk index 0 (not base)
// and is not a revisable message. base 0 (an untrimmed session) makes both the
// zero value, so diskIndex is the identity and nothing downstream shifts.
func reviseBaseFor(full, trimmed []provider.Message) (base int, head bool) {
	base = len(full) - len(trimmed)
	if base <= 0 {
		return 0, false
	}
	head = len(trimmed) > 0 && trimmed[0].Meta["compaction"] == "true"
	return base, head
}

// diskIndex maps an in-memory transcript index to the on-disk effective-transcript
// index an amend must be persisted against. A resume trims the in-memory transcript
// to a recent window while the file keeps the full history, so a revise verb that
// mutates the live agent at in-memory index i must record its amend at i+reviseBase
// — otherwise walkSession replays it over the full transcript on the next reload and
// hits the wrong message (silent corruption of exactly the long sessions a trim
// applies to). ok is false for the resume-window compaction summary at index 0,
// which is not a revisable message. Identity when reviseBase is 0, so a session that
// was never trimmed is unaffected.
func (s *wsSession) diskIndex(i int) (int, bool) {
	if s.reviseHead && i == 0 {
		return 0, false
	}
	return i + s.reviseBase, true
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

// ephemeralTail names the live cards this session stacks on top of the model's
// per-turn tail. One accessor, so buildSession and reloadLore cannot disagree
// about what a re-derived tail has to carry — which is exactly how they came to
// disagree: the re-derivation installed the run's own tail bare.
//
// Both fields are written once during buildSession, before the session is
// published, so this reads them without s.mu like the other build-time state.
func (s *wsSession) ephemeralTail() build.EphemeralTail {
	return build.EphemeralTail{Ext: build.ExtEphemeral(s.extMgr), Tasks: s.tasks}
}

// argsSnapshot copies the session's resolved args under s.mu.
//
// s.args is NOT immutable after buildSession, and reading it as if it were is
// how a live change gets reverted by the next rebuild. Several live verbs write
// into it so that rebuildTools' fresh Resolve reproduces the session as it now
// stands rather than as it launched — the approval mode, the user's identity,
// the cast, the model. session_liveargs_test.go holds that list and requires a
// reason for each, because a field that a verb moves but a rebuild re-resolves
// from the launch value fails silently and only on the second event.
//
// So every Resolve-time reader must snapshot instead of touching s.args bare.
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
	// Merge into the LIVE policy's read-only set, not the throwaway one this
	// Resolve just minted: MergeToolsForMode registers each read_only tool
	// there, and the confirm gate reads the policy's copy. Without this an
	// extension's read-only tools would start prompting after any rebuild.
	if s.roSet != nil {
		rr.AdoptReadOnlySet(s.roSet)
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
	// Same treatment for the session's task board, and the same reason. The pane,
	// the status glance, and the model's per-turn task card all read the
	// controller bound at session build; adopting this resolve's fresh one would
	// point the task tools at a store none of them render — so the board would
	// exist, never change, and the model would lose sight of its own open work.
	rr.UseTasks(s.tasks)
	rr.UseMemory(s.memory)
	rr.UseFiles(s.files)
	// Re-bind the channels that live on a TOOL INSTANCE rather than on a
	// long-lived object (the confirmer, by contrast, lives on the gate and
	// survives a rebuild untouched). Resolve just minted fresh tools with nil
	// channels; without this they fail for the rest of the session with the
	// front end sitting right there. See workspace_toolchannels.go for the full
	// list and why it is a list.
	//
	// Not a corner case: a rebuild fires before the first turn whenever an
	// extension asserts its tool policy, and again on entering plan mode — the
	// one mode that deliberately keeps ask_user_question, precisely so the agent
	// can ask when requirements are unclear.
	s.bindResolvedChannels(&rr)
	toolsChanged := s.agent.SetTools(rr.ToolRegistry)
	// The other half of the same rule, and the one this rebuild used to skip:
	// code_execution's gated dispatcher binds onto the freshly-minted tool, so
	// without this the tool fails closed ("not wired to the approval gate") for
	// the rest of the session. After SetTools, because it reaches the instance
	// through the agent's registry.
	s.bindAgentChannels(s.agent, s.gate)
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
// asserts its tool/context policy once the session cwd is known (an
// extension withdrawing its tools where they can't work), which fires a
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
	case "tool-withdrawal", "extension-context", "extensions-ready":
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
	// Replace every client's transcript with the compacted one. Compaction
	// snapshots the amended transcript, so the tail's swipe alternatives are now
	// folded away — drop the switchable set (they remain on disk for audit).
	s.clearTail()
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
	s.clearTail()
	// A clear reuses the compaction row as a floor marker. No summarizer ran,
	// so it cost nothing — zero usage, not the previous compaction's.
	_ = s.sess.AppendCompaction(nil, core.CompactResult{})
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	s.broadcast(ctrlproto.NoticeEvent("info", "", i18n.T("Cleared the conversation.")))
	return nil
}

// reviseGuard is the shared precondition for an in-place transcript revision: no
// turn may be running (a concurrent append would race the index), and the
// client's epoch must match the live transcript's — a mismatch means the
// transcript shifted under the client (another edit, a turn, a compaction) so its
// index no longer means what it thought. The busy gate also makes the direct
// durable AppendAmend safe, exactly as clear/compact rely on it.
func (s *wsSession) reviseGuard(epoch uint64) error {
	s.mu.Lock()
	busy := s.turnCancel != nil
	s.mu.Unlock()
	if busy {
		return ctrlproto.ErrBusy
	}
	if s.agent.TranscriptEpoch() != epoch {
		return ctrlproto.Errorf(ctrlproto.CodeConflict, "%s", i18n.T("transcript changed since epoch %d; reload and retry", epoch))
	}
	return nil
}

// editMessage replaces the message at index with a text message of the same role
// (the Stage surface's edit), persists the change as a replace amend, and
// broadcasts a fresh snapshot so every client converges. The role and any
// structural blocks are preserved (see reviseMessageText); only the prose changes.
func (s *wsSession) editMessage(epoch uint64, index int, text string) error {
	if err := s.reviseGuard(epoch); err != nil {
		return err
	}
	msgs := s.agent.Messages()
	if index < 0 || index >= len(msgs) {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("message index %d out of range", index))
	}
	if _, ok := s.diskIndex(index); !ok {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("the resume-window summary at index %d cannot be edited", index))
	}
	edited := reviseMessageText(msgs[index], text)
	// An edit within the current response span (the tail) rides the tail suffix
	// machinery; an edit to an OLDER message becomes a message-scoped variant (the
	// original kept, the downstream shared). Both keep the original swipeable.
	if start := lastResponseStart(msgs); index >= start {
		return s.editTailAsVariant(msgs, start, index, edited)
	}
	return s.editMessageAsVariant(msgs, index, edited)
}

// editMessageAsVariant records an edit to an older message (before the current
// response span) as a message-scoped variant: the transcript's message at index is
// replaced in place (downstream untouched), the prior version is retained as a
// swipeable take, and the in-memory msgVars is updated so the snapshot marks the
// position and a later swipe can switch it. The tail is left alone — an older edit
// does not touch the last response.
func (s *wsSession) editMessageAsVariant(msgs []provider.Message, index int, edited provider.Message) error {
	prior := msgs[index]
	if !s.agent.ReplaceMessage(index, edited) {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("message index %d out of range", index))
	}
	if disk, ok := s.diskIndex(index); ok {
		_ = s.sess.AppendReplaceVariant(disk, edited, "edit") // keep_prior: retain the original as a take
	}
	s.mu.Lock()
	if s.msgVars == nil {
		s.msgVars = map[int]*msgVarLive{}
	}
	switch mv := s.msgVars[index]; {
	case mv == nil:
		// Brand-new position: the original + this edit are the two takes, edit active.
		s.msgVars[index] = &msgVarLive{takes: []provider.Message{prior, edited}, count: 2, active: 1}
	case mv.takes == nil:
		// Resumed count, not yet hydrated: the file gains a take. Bump the count and
		// active; the full list stays lazy (a swipe hydrates it from disk).
		mv.count++
		mv.active = mv.count - 1
	default:
		mv.takes = append(mv.takes, edited)
		mv.count = len(mv.takes)
		mv.active = mv.count - 1
	}
	s.mu.Unlock()
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	return nil
}

// editTailAsVariant records an edit to the current response span (the tail, from
// lastResponseStart to the end) as a swipeable alternative: the current span
// becomes a kept take and the edited span becomes the new active take, reusing the
// retract machinery retry already uses — so the original stays reachable with the
// swipe arrows and the existing tail counter lights up. Because the span from the
// last user turn is the final assistant message(s), never the tool_use/tool_result
// pairs before it, an edit here leaves the actor_spawn machinery and its 🎭
// attribution untouched. Amend-persist errors are best-effort, matching retry.
func (s *wsSession) editTailAsVariant(msgs []provider.Message, start, index int, edited provider.Message) error {
	newSpan := append([]provider.Message(nil), msgs[start:]...)
	newSpan[index-start] = edited

	if disk, ok := s.diskIndex(start); ok {
		_ = s.sess.AppendAmend(core.AmendRetract, disk, nil, "edit") // set the current span aside as a take
	}
	for _, m := range newSpan {
		_ = s.sess.AppendMessage(m) // the edited span becomes the new active take
	}
	s.agent.SetMessages(append(append([]provider.Message(nil), msgs[:start]...), newSpan...))
	s.seedTail() // reconstruct [current, edited] (edited active) from disk
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	return nil
}

// reviseMessageText rebuilds a message with its prose replaced by text, PRESERVING
// the structural blocks that carry meaning beyond prose: tool calls and their
// results, and any inline images, kept in their original order. This is what keeps
// an edit to a cast turn's narration from stripping the actor_spawn machinery the
// 🎭 attribution rides on (Details.actor lives on the tool_result, a block this
// edit never touches). Reasoning blocks are dropped — they described the original
// text and no longer correspond to the edit. If the message had no prose block, the
// new text leads; otherwise it replaces the first text block and later text blocks
// are folded into that one.
func reviseMessageText(orig provider.Message, text string) provider.Message {
	var rebuilt []provider.Content
	placed := false
	for _, c := range orig.Content {
		switch c.(type) {
		case provider.TextBlock:
			if !placed {
				rebuilt = append(rebuilt, provider.TextBlock{Text: text})
				placed = true
			}
			// drop later text blocks — consolidated into the first
		case provider.ReasoningBlock:
			// drop — stale relative to the edited prose
		default:
			rebuilt = append(rebuilt, c) // keep tool_call / tool_result / image, in order
		}
	}
	if !placed {
		rebuilt = append([]provider.Content{provider.TextBlock{Text: text}}, rebuilt...)
	}
	return provider.Message{Role: orig.Role, Content: rebuilt}
}

// deleteMessage removes the message at index, persists a delete amend, and
// broadcasts a fresh snapshot.
func (s *wsSession) deleteMessage(epoch uint64, index int) error {
	if err := s.reviseGuard(epoch); err != nil {
		return err
	}
	disk, ok := s.diskIndex(index)
	if !ok {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("the resume-window summary at index %d cannot be deleted", index))
	}
	if !s.agent.DeleteMessage(index) {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("message index %d out of range", index))
	}
	_ = s.sess.AppendAmend(core.AmendDelete, disk, nil, "delete")
	s.clearTail()
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	return nil
}

// tailVariants holds the current tail span's swipe state in memory — the span's
// start index in the effective transcript, its alternative takes (creation
// order), and which take is currently live. The takes are materialized here for
// the tail span only, mirroring the session format's derived-variant model (older
// spans keep their variants on disk but drop out of memory). len(takes) < 2 means
// nothing to swipe.
type tailVariants struct {
	start  int
	takes  [][]provider.Message
	active int
}

// msgVarLive is one position's message-scoped variant state in memory. takes is
// populated for a position edited in this daemon; for a position seeded from the
// file on resume it is nil (only count/active are known) until a swipe hydrates it
// via core.SessionMsgVariant. count is authoritative (== len(takes) once hydrated).
type msgVarLive struct {
	takes  []provider.Message
	count  int
	active int
}

// swipe switches the tail span's active take to variant — the Stage surface's
// swipe-back. It rebuilds the transcript as the unchanged prefix plus the chosen
// take, persists the choice as a select amend (reconstructed on reload, no bytes
// duplicated), and broadcasts a fresh snapshot. Epoch/busy guarding matches
// edit/delete; a swipe to the current variant is an idempotent no-op.
func (s *wsSession) swipe(epoch uint64, variant int) error {
	if err := s.reviseGuard(epoch); err != nil {
		return err
	}
	s.mu.Lock()
	t := s.tail
	s.mu.Unlock()
	if len(t.takes) < 2 {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("no alternatives to swipe"))
	}
	if variant < 0 || variant >= len(t.takes) {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("variant %d out of range", variant))
	}
	if variant == t.active {
		return nil // already live — nothing to write
	}
	msgs := s.agent.Messages()
	if t.start > len(msgs) {
		// The transcript shrank under the tracked span (should not happen — the
		// mutators that could clear it, but be safe rather than index-panic).
		return ctrlproto.Errorf(ctrlproto.CodeConflict, "%s", i18n.T("tail changed; reload and retry"))
	}
	newMsgs := append(append([]provider.Message(nil), msgs[:t.start]...), t.takes[variant]...)
	s.agent.SetMessages(newMsgs)
	if s.sess.HasPendingGreeting() {
		// A pre-first-turn greeting swipe stays a draft: update the deferred active
		// in memory, no disk write, so previewing openings does not promote the
		// session. The flush at the first turn persists this chosen active.
		s.sess.SetPendingGreetingActive(variant)
	} else if disk, ok := s.diskIndex(t.start); ok {
		_ = s.sess.AppendSelect(disk, variant, "swipe")
	}
	s.mu.Lock()
	s.tail.active = variant
	s.mu.Unlock()
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	return nil
}

// swipeMessage switches a message-scoped variant's active take at index — the swipe
// for an edited older message. It swaps that one message in place (the downstream
// is untouched), persists an mselect amend, and lazily hydrates the take list from
// disk the first time a resumed position is swiped (seedMsgVars kept only counts).
func (s *wsSession) swipeMessage(epoch uint64, index, variant int) error {
	if err := s.reviseGuard(epoch); err != nil {
		return err
	}
	s.mu.Lock()
	mv := s.msgVars[index]
	s.mu.Unlock()
	if mv == nil || mv.count < 2 {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("no alternatives to swipe at %d", index))
	}
	if variant < 0 || variant >= mv.count {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("variant %d out of range", variant))
	}
	if variant == mv.active {
		return nil // already live — nothing to write
	}
	disk, ok := s.diskIndex(index)
	if !ok {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("the resume-window summary at index %d has no variants", index))
	}
	// Lazily hydrate a resumed position's full take list on the first swipe. The
	// take list lives on disk at the on-disk index, so hydrate + persist use it.
	takes := mv.takes
	if takes == nil {
		hydrated, ok, err := core.SessionMsgVariant(s.sess.Path, disk)
		if err != nil || !ok || len(hydrated.Takes) != mv.count {
			return ctrlproto.Errorf(ctrlproto.CodeConflict, "%s", i18n.T("could not load variants at %d; reload and retry", index))
		}
		takes = hydrated.Takes
	}
	if !s.agent.ReplaceMessage(index, takes[variant]) {
		return ctrlproto.Errorf(ctrlproto.CodeConflict, "%s", i18n.T("message %d changed; reload and retry", index))
	}
	_ = s.sess.AppendMsgSelect(disk, variant, "swipe")
	s.mu.Lock()
	mv.takes = takes // cache the now-hydrated list
	mv.active = variant
	s.mu.Unlock()
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	return nil
}

// pruneVariants collapses the message-scoped variant at index to its active take
// and closes the position (prune-to-latest). The active take is already live in the
// transcript, so this only seals the position on disk and drops the in-memory
// marker — no transcript change, no epoch bump; the fresh snapshot removes the
// swipe control.
func (s *wsSession) pruneVariants(epoch uint64, index int) error {
	if err := s.reviseGuard(epoch); err != nil {
		return err
	}
	s.mu.Lock()
	mv := s.msgVars[index]
	s.mu.Unlock()
	if mv == nil || mv.count < 2 {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("no variants to prune at %d", index))
	}
	disk, ok := s.diskIndex(index)
	if !ok {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("the resume-window summary at index %d has no variants", index))
	}
	_ = s.sess.AppendSeal(disk, mv.active, "prune")
	s.mu.Lock()
	delete(s.msgVars, index)
	s.mu.Unlock()
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	return nil
}

// dropVariant removes one take from the message-scoped variant at index, keeping
// the rest swipeable; closes the position at one take. It hydrates the take list if
// needed (a resumed position) to compute the new active take, mirrors the fold's
// active-adjustment, persists a drop-take amend, and swaps the live message.
func (s *wsSession) dropVariant(epoch uint64, index, variant int) error {
	if err := s.reviseGuard(epoch); err != nil {
		return err
	}
	s.mu.Lock()
	mv := s.msgVars[index]
	s.mu.Unlock()
	if mv == nil || mv.count < 2 {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("no variants to drop at %d", index))
	}
	if variant < 0 || variant >= mv.count {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("variant %d out of range", variant))
	}
	disk, ok := s.diskIndex(index)
	if !ok {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("the resume-window summary at index %d has no variants", index))
	}
	takes := mv.takes
	if takes == nil {
		hydrated, ok, err := core.SessionMsgVariant(s.sess.Path, disk)
		if err != nil || !ok || len(hydrated.Takes) != mv.count {
			return ctrlproto.Errorf(ctrlproto.CodeConflict, "%s", i18n.T("could not load variants at %d; reload and retry", index))
		}
		takes = hydrated.Takes
	}
	active := mv.active
	newTakes := append(append([]provider.Message(nil), takes[:variant]...), takes[variant+1:]...)
	switch {
	case variant < active:
		active--
	case variant == active && active >= len(newTakes):
		active = len(newTakes) - 1
	}
	_ = s.sess.AppendDropTake(disk, variant, "drop")
	if len(newTakes) <= 1 {
		s.mu.Lock()
		delete(s.msgVars, index)
		s.mu.Unlock()
		s.agent.ReplaceMessage(index, newTakes[0])
	} else {
		s.mu.Lock()
		mv.takes, mv.count, mv.active = newTakes, len(newTakes), active
		s.mu.Unlock()
		s.agent.ReplaceMessage(index, newTakes[active])
	}
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	return nil
}

// seedDeferredGreeting seeds a card's openings as a DEFERRED greeting (in memory,
// not on disk — see core.DeferGreetingVariants) and sets the tail-span swipe
// state directly, so the character greets the user and the openings are swipeable
// while the session stays a meta-only draft until the first real turn. Shared by
// buildSession and the pre-first-turn user-persona rename (UserBind), which
// re-derives the greeting text with the new {{user}} before calling this.
func (s *wsSession) seedDeferredGreeting(sess *core.Session, greetings []string) {
	greetingMsgs := make([]provider.Message, 0, len(greetings))
	// Stamped like every other message, and like the CLI's own greeting seed
	// (seedCardGreeting). Without it a deferred greeting persisted with a zero
	// Time — "0001-01-01T00:00:00Z" as the opening beat of every Stage chat.
	// Invisible in Stage, which draws no per-message times, but it is the first
	// row in the file: anything that reads the session as a timeline (the player,
	// a replay, a time-sorted export) starts two thousand years before the scene.
	// Construction time, not flush time: the greeting enters the conversation when
	// the chat opens and the user reads it, which is what the deferral is hiding.
	now := time.Now()
	for _, g := range greetings {
		greetingMsgs = append(greetingMsgs, provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: g}},
			Time:    now,
			Meta:    map[string]string{"source": "card:greeting"},
		})
	}
	activeMsg, _ := sess.DeferGreetingVariants(greetingMsgs, sess.Meta.Greeting)
	s.agent.SetMessages([]provider.Message{activeMsg})
	// Set the tail-span swipe state directly — one single-message take per opening,
	// the selected one active — mirroring exactly what seedTail would read off disk
	// for a persisted greeting, so snapshot() renders the swipe arrows identically.
	if len(greetingMsgs) > 1 {
		takes := make([][]provider.Message, len(greetingMsgs))
		for i := range greetingMsgs {
			takes[i] = []provider.Message{greetingMsgs[i]}
		}
		active := sess.Meta.Greeting
		if active < 0 || active >= len(takes) {
			active = 0
		}
		s.mu.Lock()
		s.tail = tailVariants{start: 0, takes: takes, active: active}
		s.mu.Unlock()
	}
}

// seedTail captures the session file's tail-span swipe state into memory at
// materialize, so swipes written before this daemon started (a pre-restart
// retry, or pre-seeded greetings) are switchable again. It is defensive about
// OpenSession's post-walk tool-pair repair: it adopts the takes only when the
// active one lines up with the live transcript's tail by length, so a repair that
// shifted indices yields no-swipe rather than a wrong swipe.
func (s *wsSession) seedTail() {
	start, takes, active, err := core.SessionTail(s.sess.Path)
	if err != nil || start < 0 || len(takes) < 2 || active < 0 || active >= len(takes) {
		return
	}
	// SessionTail reports on-disk indices; the live agent may be a resume-trimmed
	// window of the transcript, so re-anchor to in-memory space. A span that begins
	// before the window (start < 0 after the shift) is not swipeable here.
	start -= s.reviseBase
	if start < 0 {
		return
	}
	msgs := s.agent.Messages()
	if start > len(msgs) || len(takes[active]) != len(msgs)-start {
		return // repair moved the transcript out from under the takes
	}
	s.mu.Lock()
	s.tail = tailVariants{start: start, takes: takes, active: active}
	s.mu.Unlock()
}

// seedMsgVars captures the session file's message-scoped variant positions into
// memory at materialize — counts and active only, the full take lists staying lazy
// (a swipe hydrates one from disk via core.SessionMsgVariant) — so a session that
// reloads with keep_prior edits already written draws its per-position swipe
// markers. The tail span is left to seedTail. A no-op when there are none.
func (s *wsSession) seedMsgVars() {
	vars, err := core.SessionVariants(s.sess.Path)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range vars {
		if v.Span {
			continue // the tail suffix span is seedTail's job
		}
		// SessionVariants reports on-disk indices; re-anchor to the live agent's
		// (possibly resume-trimmed) space. A position before the window is dropped —
		// its message is not in the in-memory transcript to swipe.
		idx := v.Index - s.reviseBase
		if idx < 0 {
			continue
		}
		if s.msgVars == nil {
			s.msgVars = map[int]*msgVarLive{}
		}
		s.msgVars[idx] = &msgVarLive{count: v.Count, active: v.Active} // takes nil = hydrate on swipe
	}
}

// clearTail drops the tail-span swipe state — a new turn, a tail edit, a delete, or
// a compaction commits or invalidates the alternatives.
func (s *wsSession) clearTail() {
	s.mu.Lock()
	s.tail = tailVariants{}
	s.mu.Unlock()
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
	// Before ApplyTrust: reloadLore reads this atomic for the verdict it
	// discovers against, and s.info() reports it to every client.
	s.trusted.Store(trusted)
	build.ApplyTrust(ctx, trusted, build.TrustSurfaces{
		Args:  s.argsSnapshot(),
		Ext:   s.extMgr,
		Grace: webReloadGrace,
		// Only reached when there is no manager to fire SetOnReload for us —
		// bare fixtures, never a buildSession session.
		Rebuild: func() { s.rebuildTools("trust") },
		Lore:    s.reloadLore,
		After:   func() { s.broadcast(ctrlproto.SessionUpdatedEvent(s.info())) },
		// Hooks: workspace-scoped, moved by applyTrust before this loop runs.
	})
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

	// Immersive (Stage) sessions title by the character name — not the player's
	// first line (which reads as a stammered fragment) and not an LLM summary
	// (which spends unasked tokens and reads as "User greets the elf innkeeper…").
	// The design's "default title = character name"; resolve it from the bound
	// card and fall back to the first line only when there is no card name to use.
	if s.sess != nil && s.sess.Meta.Experience != "" {
		title := fallback
		if name := s.ws.cardName(s.sess.Meta.Card); name != "" {
			title = name
		}
		// Provisional, not final. The character name is instant and free, which is
		// what a chat needs the moment it opens — but every chat with the same
		// character then carries the same title, and the Library draws the character
		// name beside it anyway, so the row reads "Kobeni / Kobeni" and three chats
		// with her are indistinguishable. upgradeImmersiveTitle replaces it once the
		// scene has enough in it to summarize. applyTitle marks it machine-generated,
		// which is what makes it replaceable (and a manual rename un-replaceable).
		s.applyTitle(title)
		return
	}

	s.applyTitle(fallback)

	ok, cl, model := s.ws.titleGen(s)
	if !ok || cl == nil {
		return
	}
	// The cascading seed (compaction anchor + recent exchanges) degenerates
	// to the first user message here — settleTitle only ever runs on a
	// still-untitled session, i.e. right after the first exchange.
	gen := generateTitle(ctx, cl, model, core.BuildTitleSeed(s.agent.Messages(), core.TitleSeedBudget), s)
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

// immersiveTitleUpgradeTurns is how many of the player's own messages a scene
// needs before its title is worth generating. Two is too few — the opening
// exchange is greetings and setup, and a title drawn from it says "User greets
// the shopkeeper". By the third the scene has a subject.
const immersiveTitleUpgradeTurns = 3

// upgradeImmersiveTitle replaces a Stage session's provisional character-name
// title with a generated one, once the scene has enough in it to summarize.
//
// settleTitle gives an immersive session the bound character's name: instant,
// free, and right for the moment the chat opens. It is also the same title every
// other chat with that character gets, and the Library already draws the
// character name beside it — so the row reads "Kobeni / Kobeni" and three chats
// with her are indistinguishable. Dogfooding bore that out: the user hit ✨
// regenerate twice and settled on a scene summary.
//
// Deferred rather than generated up front for the reason settleTitle avoided it
// originally — tokens spent unasked. Waiting until the third player turn means a
// character opened for a look, or a chat abandoned after one line, never spends
// anything; only a scene someone is actually playing does, once.
//
// Same guards as the post-compaction refresh: the auto_title gate, and a title
// that is still machine-owned. A manual rename is never touched, including one
// that lands while the model is thinking.
func (s *wsSession) upgradeImmersiveTitle(ctx context.Context) {
	if s.sess == nil || s.sess.Meta.Experience == "" {
		return
	}
	prev, due := s.titleUpgradeDue()
	if !due {
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
	gen := generateTitle(ctx, cl, model, seed, s)
	if gen == "" || gen == prev {
		return
	}
	// Re-check under the lock: a manual rename may have landed while the model was
	// thinking, and it outranks us.
	s.mu.Lock()
	stillOurs := s.title == prev && s.titleGenerated
	s.mu.Unlock()
	if stillOurs {
		s.applyTitle(gen)
	}
}

// titleUpgradeDue reports whether this session is still wearing its provisional
// character-name title and the scene has grown enough to replace it, returning
// that title so the caller can re-check it after the model call.
//
// Split out from upgradeImmersiveTitle so the decision is testable without a
// model: everything that decides whether tokens get spent lives here.
func (s *wsSession) titleUpgradeDue() (string, bool) {
	s.mu.Lock()
	prev, generated := s.title, s.titleGenerated
	s.mu.Unlock()
	if prev == "" || !generated {
		return prev, false // never titled, or a manual rename — not ours to replace
	}
	// Only the provisional character name is upgradeable. Any other machine title
	// is a summary this pass already produced.
	if prev != s.ws.cardName(s.sess.Meta.Card) {
		return prev, false
	}
	return prev, playerTurns(s.agent.Messages()) >= immersiveTitleUpgradeTurns
}

// playerTurns counts the human's own messages — synthetic (harness-injected)
// ones do not mean the player said anything.
func playerTurns(msgs []provider.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == provider.RoleUser && m.Meta[core.MetaSynthetic] != "true" {
			n++
		}
	}
	return n
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
	gen := generateTitle(ctx, cl, model, seed, s)
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
//
// Compaction summaries are skipped. They are synthetic messages carrying the
// user role, so after a compaction the transcript's first user message is the
// summary — and titling an untitled session then (a resume, a restart, any turn
// that finds s.title empty) produced a title made of the summary's own
// scaffolding: "## Context Summary (compacted)  ## Goal The user is building…",
// truncated at 60 characters. Observed verbatim in a reviewed session.
//
// core.BuildTitleSeed, which feeds the model-generated phase, already handles
// this — it anchors on the compaction deliberately and strips the header. This
// is the instant phase-1 fallback, which did not, and which is what actually
// names the session when the model call is off or fails.
//
// Falls back to the first summary's own text when there is nothing else, with
// the header stripped, so a fully-compacted transcript still yields a title
// about the work rather than about the document.
func firstUserText(msgs []provider.Message) string {
	var summary string
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
		t := strings.TrimSpace(sb.String())
		if t == "" {
			continue
		}
		if m.Meta[core.MetaCompaction] == "true" {
			if summary == "" {
				summary = core.StripCompactionHeader(t)
			}
			continue
		}
		return t
	}
	return summary
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
	if err := s.prompt(text, nil, core.UserMessageExtras{}); err != nil {
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
	var tail *ctrlproto.TailInfo
	var marks []ctrlproto.VariantMark
	if len(s.tail.takes) >= 2 {
		tail = &ctrlproto.TailInfo{
			SpanStart: s.tail.start,
			Variants:  len(s.tail.takes),
			Active:    s.tail.active,
		}
		marks = append(marks, ctrlproto.VariantMark{Index: s.tail.start, Variants: len(s.tail.takes), Active: s.tail.active, Span: true})
	}
	for idx, mv := range s.msgVars {
		if mv.count >= 2 {
			marks = append(marks, ctrlproto.VariantMark{Index: idx, Variants: mv.count, Active: mv.active})
		}
	}
	s.mu.Unlock()
	sort.Slice(marks, func(i, j int) bool { return marks[i].Index < marks[j].Index })
	return ctrlproto.Snapshot{
		Session:  s.info(),
		Messages: wm,
		// The transcript this window was cut from. The hub always broadcasts the WHOLE
		// thing (free in-process — the slices are shared); the serialization edge cuts
		// it down per client contract, and stamps Base. Total and Epoch are true of the
		// transcript either way, which is what lets a windowed client place what it got.
		Epoch:        s.agent.TranscriptEpoch(),
		Total:        len(wm),
		Busy:         busy,
		Permissions:  perms,
		Asks:         asks,
		Queued:       s.agent.PendingQueuedMessages(),
		Skills:       s.skillList(),
		Tail:         tail,
		VariantMarks: marks,
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
	busy := s.turnCancel != nil
	s.mu.Unlock()
	info := ctrlproto.SessionInfo{
		ID:              s.id,
		Title:           title,
		Provider:        prov,
		Model:           model,
		Persona:         persona,
		Experience:      s.sess.Meta.Experience,
		Background:      s.sess.Meta.Background,
		Note:            s.sess.Meta.Note,
		UserName:        s.sess.Meta.UserName,
		UserDescription: s.sess.Meta.UserDescription,
		UserGender:      s.sess.Meta.UserGender,
		UserPronouns:    s.sess.Meta.UserPronouns,
		Card:            s.sess.Meta.Card,
		Cast:            s.sess.Meta.Cast,
		CastModels:      castRoutesToView(s.sess.Meta.CastModels),
		WorldLore:       worldLoreToView(s.sess.Meta.WorldLore),
		ScenePinStale:   scenePinStaleFor(s.sess.Meta.WorldLore, s.messageCount()),
		Coordination:    s.sess.Meta.Coordination,
		World:           s.sess.Meta.World,
		Path:            s.sess.Path,
		Created:         ctrlTimeString(s.sess.Meta.Started),
		Trusted:         s.trusted.Load(),
		Subscription:    sub,
		// info() only ever describes a materialized session, so Live is always
		// true here; the cold rows are built in Workspace.Sessions without it.
		Live: true,
		Busy: busy,
	}
	if s.agent != nil {
		info.Messages = len(s.agent.Messages())
		info.SupportsContinue = s.agent.ContinuesAssistantPrefill()
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

// busyNow reports whether a turn is in flight (a running turn holds
// turnCancel). Used by Workspace.Sessions to set SessionInfo.Busy on the live
// rows without building a full snapshot.
func (s *wsSession) busyNow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnCancel != nil
}

// setModel records this session's live model identity — on the session AND in
// the args every later Resolve reads.
//
// s.args is not a launch snapshot. rebuildTools re-resolves from it on every
// extension reload, MCP toggle, approval switch and trust flip, and a fresh
// Resolve re-mints the tools that CARRY the model's identity: terva_status
// (provider, auth method, base URL), the host-routed dispatch tools
// swarm_spawn / actor_spawn / raati_convene, which injectExtraTools stamps with
// the resolve's provider+model, and read, whose SupportsVision is the resolved
// model's image capability. Leaving Provider/Model at their launch values
// meant the next rebuild silently put back two of the three steps
// build.ApplyModelSwap had just performed: sub-agents spawned afterwards ran on
// the pre-swap model, and terva_status reported the pre-swap endpoint, for the
// rest of the session. buildSession seeds these same two fields from session
// meta on every build — this is the live half of that, and the reason a daemon
// restart already got it right while the running session did not.
//
// rebuiltClient says the swap replaced the provider client, i.e. the endpoint
// moved. The launch-time key/URL overrides then pin an endpoint this session
// has left, so they go — the same clearing switchModel does to build the
// replacement client, and acpFactory.SwitchModel to resolve the target's creds.
func (s *wsSession) setModel(prov, model string, rebuiltClient bool) {
	s.mu.Lock()
	s.provider, s.model = prov, model
	s.args.Provider, s.args.Model = prov, model
	if rebuiltClient {
		s.args.APIKey, s.args.BaseURL = "", ""
	}
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
