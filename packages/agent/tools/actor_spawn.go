package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// CastMember is one dispatchable actor in a --play cast: a role-bound identity
// (a persona name OR a character-card path) the director can voice. Exactly one
// of Persona/Card is set. Built from the trusted --cast declaration, never from
// model input, so the model can only ever NAME a member — never point at a file.
type CastMember struct {
	Persona string
	Card    string // absolute path
	// Provider/Model pin this actor to a specific route (Phase 7); empty inherits
	// the host/tier route. An explicit pin MAY differ from the host — the author's
	// choice — unlike a `tier`, which never exceeds the host.
	Provider string
	Model    string
}

// ActorSpawnTool is the --play director's skin over the swarm dispatch engine:
// it voices a member of the declared cast. Unlike swarm_spawn (fire-and-forget
// coding sub-agents), actor_spawn is SYNCHRONOUS — it hands the actor the
// situation, waits for its line, and RETURNS the line so the director can weave
// it into the scene ("director & performers", Model Y; docs/proposals/agent-dispatch.md).
//
// Actors are WARM: once voiced, an actor is kept alive (Warm), so it remembers
// the scene across turns and only its first line pays spawn latency. Subsequent
// situations are delivered to the live actor with SendUserTurn.
// ActorEngine is the slice of the swarm supervisor the tool needs, extracted so
// the dispatch/reuse decision is unit-testable without a live subprocess.
// *swarm.Swarm satisfies it.
type ActorEngine interface {
	SpawnReq(ctx context.Context, req swarm.SpawnRequest) (*swarm.Agent, error)
	SendUserTurn(id, text string) error
	Stop(id string) error
	Remove(id string) error
}

type ActorSpawnTool struct {
	// Swarm is the dispatch engine. Nil means actors are unavailable.
	Swarm ActorEngine
	// Warm is the shared, bounded cache of live actors (survives registry
	// rebuilds). Nil means actors are unavailable.
	Warm *WarmActors
	// Cast is the closed, declared set of actors this session can voice, keyed
	// by the NAME the model dispatches. Empty disables the tool.
	Cast map[string]CastMember
	// HostProvider/HostModel/Tiers back the optional `tier` param exactly as in
	// swarm_spawn: an actor inherits the host route by default, or runs a cheaper
	// tier for the host provider (never stronger than the host).
	HostProvider string
	HostModel    string
	Tiers        SwarmTierMap
	// WorldLore renders the World-lore block the named actor is cleared to see
	// for this situation (Worlds W6): the L2 audience filter applied to the
	// session's lorebook, keyword-scanned against the situation and the recent
	// scene. Called on EVERY dispatch — spawn and warm reuse alike — so a
	// world_note or world_reveal mid-scene reaches the actor's next beat, the
	// same freshness the chat tail gives the bound character. Optional; nil or
	// an empty render injects nothing.
	WorldLore func(actor, situation string) string
	// hostMu guards SetHost's live mutation of HostProvider/HostModel on a
	// mid-session model swap; acquire() reads them through host(). Construction
	// sets the fields directly, before the tool is registered.
	hostMu sync.RWMutex
}

// SetHost updates the host provider/model an actor inherits for tier
// resolution. Safe to call while a turn runs: a mid-session model swap
// mutates this live, and acquire() reads through host() under the read
// lock. Implements HostRouted.
func (t *ActorSpawnTool) SetHost(provider, model string) {
	t.hostMu.Lock()
	defer t.hostMu.Unlock()
	t.HostProvider = provider
	t.HostModel = model
}

func (t *ActorSpawnTool) host() (provider, model string) {
	t.hostMu.RLock()
	defer t.hostMu.RUnlock()
	return t.HostProvider, t.HostModel
}

type actorSpawnArgs struct {
	Actor     string `json:"actor"`
	Situation string `json:"situation"`
	Tier      string `json:"tier,omitempty"`
}

// actorFramingNote is prepended to an actor's situation (its user turn) so a full
// card/persona doesn't over-produce: a dispatched actor should contribute only
// its character's voice, not narrate the wider scene the director owns.
// Prompt-only — it shapes the actor's reply, not its extraction.
const actorFramingNote = "[Director's note: you are voicing a character in a scene another narrator is running. Respond only as your character, in the moment — your dialogue and your immediate action, kept concise. Don't narrate the wider scene, describe the setting at length, or speak for anyone but yourself; the narrator owns the world and the framing.]"

func composeActorTask(situation string) string {
	return i18n.P("play.actor.framing", actorFramingNote) + "\n\n" + situation
}

// composeTask builds the actor's full user turn: the framing note, the
// situation, and — when the tool has a WorldLore source — the audience-
// filtered lore block for this actor (Worlds W6). The lore rides the
// situation, the uncached per-beat channel, never the child's cached charter,
// so a mid-scene world_note or world_reveal reaches the actor's next beat.
func (t *ActorSpawnTool) composeTask(name, situation string) string {
	task := composeActorTask(situation)
	if t.WorldLore != nil {
		if block := t.WorldLore(name, situation); block != "" {
			heading := strings.ReplaceAll(i18n.P("play.actor.worldlore",
				"[What {name} knows of the world — established facts; weave them in only where the moment calls for them:]"), "{name}", name)
			task += "\n\n" + heading + "\n" + block
		}
	}
	return task
}

func (t *ActorSpawnTool) Name() string { return "actor_spawn" }

func (t *ActorSpawnTool) Description() string {
	return "Voice a member of the cast: dispatch a named actor to respond, in character, to the current situation, and return their line so you can weave it into the scene. The actor is a separate performer that sees ONLY the situations you give it — describe what is happening and what it should react to. An actor REMEMBERS your earlier exchanges with it this scene and builds on them, so its voice and choices stay consistent from turn to turn (you may still restate context freely — memory is a continuity bonus, not something to rely on). You remain the narrator and the source of truth about the world."
}

func (t *ActorSpawnTool) Schema() json.RawMessage {
	enum, _ := json.Marshal(castNames(t.Cast))
	schema := fmt.Sprintf(`{
  "type": "object",
  "properties": {
    "actor": {
      "type": "string",
      "enum": %s,
      "description": "The cast member to voice — one of the declared actor names. A NAME only, never a path."
    },
    "situation": {
      "type": "string",
      "description": "What is happening and what the actor should respond to, in enough detail to react in character. The actor remembers earlier turns and stays consistent, but keep each situation self-contained enough to stand on its own."
    },
    "tier": {
      "type": "string",
      "enum": ["weak", "medium", "strong"],
      "description": "Optional model strength for the actor on its FIRST appearance (weak = cheap/fast). Resolved for the host provider, never stronger than the host. Omit to inherit the host model; ignored once the actor is live."
    }
  },
  "required": ["actor", "situation"]
}`, string(enum))
	return json.RawMessage(schema)
}

func (t *ActorSpawnTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	if t.Swarm == nil || t.Warm == nil || len(t.Cast) == 0 {
		return toolErr("actor_spawn: no cast is available in this session"), nil
	}
	var a actorSpawnArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	name := strings.TrimSpace(a.Actor)
	situation := strings.TrimSpace(a.Situation)
	if name == "" {
		return toolErr("actor_spawn: actor is required"), nil
	}
	if situation == "" {
		return toolErr("actor_spawn: situation is required"), nil
	}
	member, ok := t.Cast[name]
	if !ok {
		return toolErr(fmt.Sprintf("actor_spawn: %q is not in the cast (%s)", name, strings.Join(castNames(t.Cast), ", "))), nil
	}
	task := t.composeTask(name, situation)

	wa, turnDone, err := t.acquire(ctx, name, task, member, a.Tier, progress)
	if err != nil {
		return toolErr("actor_spawn: " + err.Error()), nil
	}
	if err := waitTurn(ctx, turnDone, wa.terminated, wa.agent); err != nil {
		t.dropActor(name)
		return toolErr("actor_spawn: " + err.Error()), nil
	}
	return t.reply(name, wa.agent)
}

// acquire returns a live actor for name — REUSING a warm one (installing the turn
// watcher and delivering the situation with SendUserTurn) or spawning a fresh one
// — along with the turn-watcher channel. It does NOT wait for the turn, so the
// spawn-vs-reuse decision is unit-testable. Reused vs fresh is surfaced via
// progress. On the reuse path the watcher is installed BEFORE SendUserTurn so
// a fast turn can't complete before we're listening. The spawn path can't
// install-before-trigger (SpawnReq itself starts the child's initial turn);
// its window is covered by child startup + a model round-trip (seconds vs
// microseconds), and a child that dies before task_end is caught by the
// terminated channel in waitTurn.
func (t *ActorSpawnTool) acquire(ctx context.Context, name, task string, member CastMember, tier string, progress func(string)) (*warmActor, <-chan string, error) {
	if wa := t.Warm.get(name); wa != nil {
		turnDone := installTurnWatcher(wa.agent)
		if err := t.Swarm.SendUserTurn(wa.agent.ID, task); err == nil {
			emitProgress(progress, name+" responds (remembers the scene)…")
			return wa, turnDone, nil
		}
		t.dropActor(name) // stale (child gone) — fall through and re-spawn
	}

	hostProvider, hostModel := t.host()
	route, errMsg := resolveSpawnRoute(swarmSpawnArgs{Tier: tier}, hostProvider, hostModel, t.Tiers)
	if errMsg != "" {
		return nil, nil, errors.New(errMsg)
	}
	spawnProvider, spawnModel := route.Provider, route.Model
	if strings.TrimSpace(member.Model) != "" {
		// An explicit per-actor pin (Phase 7) overrides the host/tier route — the
		// author chose this model for this actor, so it may differ from the host.
		spawnProvider, spawnModel = member.Provider, member.Model
	}
	req := swarm.SpawnRequest{
		Task:       task,
		Experience: "chat", // a tool-less voice; the director owns the world
		Persona:    member.Persona,
		Card:       member.Card,
		Model:      spawnModel,
		Provider:   spawnProvider,
	}
	// Same session stamp swarm_spawn applies: the actor belongs to the
	// director's conversation, so its meta.json should say so.
	if ag := core.AgentFromContext(ctx); ag != nil {
		if sid, _ := ag.SessionIdentity(); sid != "" {
			req.SessionID = sid
		}
	}
	agent, err := t.Swarm.SpawnReq(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	wa := newWarmActor(agent)
	turnDone := installTurnWatcher(agent)
	if _, evicted := t.Warm.put(name, wa); evicted != nil {
		t.stop(evicted) // over capacity: retire the least-recently-used actor
	}
	emitProgress(progress, name+" enters the scene…")
	return wa, turnDone, nil
}

func emitProgress(progress func(string), msg string) {
	if progress != nil {
		progress(msg)
	}
}

func (t *ActorSpawnTool) reply(name string, agent *swarm.Agent) (core.ToolResult, error) {
	line := actorReply(agent.SessionPath, agent.Transcript())
	if line == "" {
		return toolErr(fmt.Sprintf("actor_spawn: %s produced no line", name)), nil
	}
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: line}},
		Details: map[string]any{"actor": name},
	}, nil
}

// stop retires one actor: stop its process and delete its state.
func (t *ActorSpawnTool) stop(wa *warmActor) {
	if wa == nil {
		return
	}
	_ = t.Swarm.Stop(wa.agent.ID)
	_ = t.Swarm.Remove(wa.agent.ID)
}

// dropActor removes a named actor from the cache and retires it.
func (t *ActorSpawnTool) dropActor(name string) { t.stop(t.Warm.drop(name)) }

// actorHandle is the slice of *swarm.Agent the turn coordination needs, extracted
// so it is unit-testable without a live subprocess.
type actorHandle interface {
	SetOnTurnEnd(func(step int, errMsg string))
	Err() error
}

// installTurnWatcher registers a one-shot listener for the actor's next
// task-level turn end and returns the channel it delivers on (buffered, so a turn
// that finishes before waitTurn is called still delivers). Install it BEFORE
// triggering the turn where possible (reuse/SendUserTurn) — the buffer covers
// a turn ending before waitTurn runs, NOT a task_end observed while no
// callback is installed (the runner drops those; see acquire for why the
// spawn path tolerates this).
func installTurnWatcher(a actorHandle) <-chan string {
	turnDone := make(chan string, 1) // the turn's error message ("" = ok)
	a.SetOnTurnEnd(func(step int, errMsg string) {
		select {
		case turnDone <- errMsg:
		default:
		}
	})
	return turnDone
}

// waitTurn blocks until the actor's turn ends (turnDone), the actor terminates
// early (terminated — a crash, no line), or ctx is cancelled. Director-pull
// coordination for Model Y.
func waitTurn(ctx context.Context, turnDone <-chan string, terminated <-chan struct{}, a actorHandle) error {
	select {
	case errMsg := <-turnDone:
		if errMsg != "" {
			return fmt.Errorf("the actor could not respond: %s", errMsg)
		}
		return nil
	case <-terminated:
		if err := a.Err(); err != nil {
			return fmt.Errorf("the actor exited before responding: %w", err)
		}
		return fmt.Errorf("the actor exited before responding")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// actorReply extracts the actor's spoken line. The session file is the clean,
// role-tagged source of truth — the dashboard transcript mixes in the actor's
// echoed user turn (see swarm/applyEventToSink) — so read the last assistant
// message from it. Fall back to the transcript (dropping the echoed user turn +
// meta lines) if the session can't be read yet (a write/flush race).
func actorReply(sessionPath string, transcript []string) string {
	if sessionPath != "" {
		if _, msgs, err := core.OpenSession(sessionPath); err == nil {
			if line := lastAssistantText(msgs); line != "" {
				return line
			}
		}
	}
	var out []string
	for _, l := range transcript {
		if strings.HasPrefix(l, "user: ") || strings.HasPrefix(l, "error: ") || strings.HasPrefix(l, "stderr: ") {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// lastAssistantText returns the text of the actor's newest reply: the
// assistant message(s) AFTER the last user turn (the situation just
// delivered). The scan stops at that user turn — walking past it would
// silently hand back a prior turn's line (or the seeded greeting) whenever
// the newest reply is empty or whitespace-only, defeating the caller's
// "produced no line" guard.
func lastAssistantText(msgs []provider.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == provider.RoleUser {
			return ""
		}
		if msgs[i].Role != provider.RoleAssistant {
			continue
		}
		var b strings.Builder
		for _, c := range msgs[i].Content {
			if tb, ok := c.(provider.TextBlock); ok {
				b.WriteString(tb.Text)
			}
		}
		if s := strings.TrimSpace(b.String()); s != "" {
			return s
		}
	}
	return ""
}

func castNames(cast map[string]CastMember) []string {
	names := make([]string, 0, len(cast))
	for n := range cast {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ---- warm-actor cache ----

// warmActor is one live actor kept for reuse across turns.
type warmActor struct {
	agent      *swarm.Agent
	terminated chan struct{} // closed when the agent reaches a terminal state
}

func newWarmActor(agent *swarm.Agent) *warmActor {
	wa := &warmActor{agent: agent, terminated: make(chan struct{})}
	go func() { agent.Wait(); close(wa.terminated) }()
	return wa
}

// DefaultWarmActorCap bounds the live-actor cache: R3 in
// docs/proposals/agent-dispatch.md fixes the policy at a small cap (~3–5);
// hitting it routinely is the signal that motivates the sister multiplexer
// (Decision 3), not a bigger number here.
const DefaultWarmActorCap = 5

// WarmActors is a bounded, LRU-evicting cache of live actors keyed by cast name.
// It is a pure data structure: the ActorSpawnTool owns all swarm interaction
// (spawn/send/stop) and retires the actors handed back by put/drop/Shutdown. A
// single instance is shared across registry rebuilds so warmth survives a
// mid-scene tool re-injection.
type WarmActors struct {
	mu    sync.Mutex
	max   int
	live  map[string]*warmActor
	order []string // LRU; order[0] = least-recently used
}

func NewWarmActors(max int) *WarmActors {
	if max < 1 {
		max = 1
	}
	return &WarmActors{max: max, live: map[string]*warmActor{}}
}

// get returns the live actor for name and marks it most-recently-used, or nil.
func (w *WarmActors) get(name string) *warmActor {
	w.mu.Lock()
	defer w.mu.Unlock()
	wa := w.live[name]
	if wa != nil {
		w.touch(name)
	}
	return wa
}

// put registers wa under name and returns the actor the caller must retire:
// the LRU actor evicted to stay within max, or a live actor the same name
// displaced (empty name / nil actor when neither). The same-key case is
// unreachable via acquire — which get()s or drop()s first — but a silent
// clobber would leak a subprocess and its terminated-watcher goroutine, so
// the displaced actor is handed back for retirement instead.
func (w *WarmActors) put(name string, wa *warmActor) (string, *warmActor) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if prev, ok := w.live[name]; ok {
		w.live[name] = wa
		w.touch(name)
		return name, prev
	}
	w.order = append(w.order, name)
	w.live[name] = wa
	w.touch(name)
	if len(w.live) <= w.max {
		return "", nil
	}
	evName := w.order[0]
	w.order = w.order[1:]
	ev := w.live[evName]
	delete(w.live, evName)
	return evName, ev
}

// drop removes and returns the named actor (nil if absent).
func (w *WarmActors) drop(name string) *warmActor {
	w.mu.Lock()
	defer w.mu.Unlock()
	wa := w.live[name]
	if wa == nil {
		return nil
	}
	delete(w.live, name)
	w.removeOrder(name)
	return wa
}

// Retire removes one named actor from the cache and, if it had warmed up, calls
// stop(agentID) so the caller tears down its swarm process — the single-actor
// twin of Shutdown, for when a cast member is dropped mid-scene. A no-op if the
// actor was never spawned.
func (w *WarmActors) Retire(name string, stop func(agentID string)) {
	wa := w.drop(name)
	if wa != nil && stop != nil {
		stop(wa.agent.ID)
	}
}

// Shutdown retires every live actor (scene teardown): it clears the cache and
// calls stop(agentID) for each, so the caller owns the swarm interaction and
// this package need not expose the agent handle.
func (w *WarmActors) Shutdown(stop func(agentID string)) {
	w.mu.Lock()
	actors := make([]*warmActor, 0, len(w.live))
	for _, wa := range w.live {
		actors = append(actors, wa)
	}
	w.live = map[string]*warmActor{}
	w.order = nil
	w.mu.Unlock()
	for _, wa := range actors {
		if stop != nil {
			stop(wa.agent.ID)
		}
	}
}

// touch moves name to the most-recently-used end. Caller holds mu.
func (w *WarmActors) touch(name string) {
	w.removeOrder(name)
	w.order = append(w.order, name)
}

// removeOrder drops name from the LRU order. Caller holds mu.
func (w *WarmActors) removeOrder(name string) {
	for i, n := range w.order {
		if n == name {
			w.order = append(w.order[:i], w.order[i+1:]...)
			return
		}
	}
}
