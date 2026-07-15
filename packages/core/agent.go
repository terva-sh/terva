package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// ErrBusy is returned by Prompt, Continue, and Compact when the agent
// is already running a turn (or compacting). The agent is single-
// flight: only one of these may be in progress at a time, because they
// all mutate the shared transcript and a second concurrent run would
// interleave appends or let Compact wholesale-replace a.messages mid-
// append, corrupting the transcript.
var ErrBusy = errors.New("agent is busy")

// Agent is a stateful conversation bound to a provider client, a model,
// and a set of tools.
type Agent struct {
	Client    provider.Client
	Model     string
	System    string
	Tools     Registry
	MaxSteps  int
	Reasoning string

	// VisibleTool, when non-nil, reports whether a registered tool is
	// ADVERTISED to the model on a turn. It filters only the tool specs sent
	// in the request (SpecsVisible, in oneTurn); it never touches dispatch or
	// the permission gate, both of which resolve the full Tools registry — so a
	// tool hidden here stays callable and stays gated. Advertisement is not
	// authority (retro H2·b's visibility ≠ authority invariant). nil advertises
	// the whole registry: today's behavior. Like Tools/System, it is pinned per
	// turn (see runLoop), so a mid-turn change lands only on the next turn.
	//
	// The predicate must be pure in the name so the advertised set — hence the
	// cached prompt prefix — is stable when nothing changed; a change to the
	// visible set is a model-facing-surface change and (once a host mutates it)
	// must be treated like a Tools swap for cache-write accounting.
	VisibleTool func(name string) bool

	// Temperature sets the sampling temperature on each request. Nil
	// leaves it unset so each provider applies its own default (terva's
	// per-provider serialization guards still apply when it is set).
	Temperature *float32

	// MaxTokens caps the model's output tokens per turn. Zero leaves
	// the field unset on the provider request, letting each provider
	// apply its own default (which can be conservative, e.g. Bedrock
	// defaults to 4096, truncating long writes/edits). Hosts populate
	// this from the resolved model's MaxOutput so large single-turn
	// responses aren't silently cut off with stopReason=length.
	MaxTokens int

	// BeforeToolExecute, if set, is called immediately before each
	// tool runs. Returning (allowed=false, reason) short-circuits
	// the call with an error result containing reason. Optionally,
	// returning a non-nil modifiedArgs replaces the JSON args the
	// tool will see, which lets guards redact / augment / patch the
	// model's request without rewriting the transcript. Empty or
	// malformed modifiedArgs is ignored.
	BeforeToolExecute func(call provider.ToolCallBlock) (allowed bool, reason string, modifiedArgs json.RawMessage)

	// BeforeTurn, if set, is called before each turn's model call.
	// Returning (allowed=false, reason) aborts the turn; reason is
	// surfaced as an assistant-like status line. Used for rate-
	// limiting, business-hour gates, and deny-by-default setups.
	BeforeTurn func(step int) (allowed bool, reason string)

	// BeforeAssistantMessage, if set, is called after the model's
	// final assistant message is assembled but before it's appended
	// to the transcript. Returning (allowed=false) suppresses both
	// the transcript append and the UI event. A non-empty
	// replacement rewrites the visible text for the user while
	// leaving the model's original text in the transcript (so the
	// model can still see what it said in subsequent turns).
	BeforeAssistantMessage func(text string) (allowed bool, reason, replacement string)

	// BeforeUserMessage, if set, is consulted just before a genuine
	// user message — the initial prompt or one drained from the queue —
	// is appended to the transcript and sent to the model. Returning
	// (allowed=false, reason) rejects the prompt: it is neither recorded
	// nor sent, and the host surfaces reason via EvUserMessageRejected.
	// A non-empty replacement rewrites the prompt the model actually
	// sees (the rewrite IS what lands in the transcript — unlike
	// BeforeAssistantMessage, where the original is kept). The synthetic
	// at-close gate nudge is never gated. Mirrors BeforeAssistantMessage
	// and backs the extension user_message intercept.
	BeforeUserMessage func(text string) (allowed bool, reason, replacement string)

	// MaxRetries controls agent-level retries for transient provider
	// failures that arrive after the HTTP stream opens (for example
	// Anthropic overloaded_error). Zero disables this retry layer.
	// RetryBaseDelay is doubled for each attempt; zero uses 2s.
	MaxRetries     int
	RetryBaseDelay time.Duration

	// Hook observers. Registered through AddEventObserver / AddMessageObserver /
	// AddUsageObserver / AddTranscriptCompactedObserver /
	// AddImageExcludedObserver / AddEscalationObserver / AddStallObserver /
	// AddContinuationGate, never assigned — see observers.go for why the
	// assignable fields these replaced were a hazard.
	obsMu                  sync.RWMutex
	eventObs               []func(AgentEvent)
	messageObs             []func(provider.Message)
	usageObs               []func(u, cumulative provider.Usage)
	transcriptCompactedObs []func(messages []provider.Message, res CompactResult)
	imageExcludedObs       []func(sha256Hex string)
	queueDrainedObs        []func(drained []string)
	escalationObs          []func(EscalationRecord)
	stallObs               []func(StallRecord)
	continuationGates      []ContinuationGate

	// ContextProvider, if set, is called once per turn to obtain
	// host-assembled ephemeral context (already wrapped/bounded) to
	// inject into the model's context for that request only. The result
	// rides provider.Request.EphemeralContext — a trailing block after
	// the cache breakpoint, never written to the transcript. Hosts wire
	// this to the extension manager's live context cards. Called outside
	// the agent lock; keep it quick.
	ContextProvider func() string

	// ContextProviderPeek, if set, is a side-effect-free twin of
	// ContextProvider: it renders the same ephemeral block but performs
	// none of ContextProvider's per-turn side effects (e.g. recording which
	// lore fired). The core never calls it — it exists so the UI can SIZE
	// the ephemeral tail (e.g. /context) without corrupting that state.
	ContextProviderPeek func() string

	// ReadOnly names the side-effect-free tools, shared with the permission
	// policy (which uses it to auto-allow in plan mode). Compaction reads it to
	// decide which discarded tool calls belong in the executed-actions ledger.
	//
	// Nil means every tool is assumed to mutate, and that is the correct failure
	// direction: a nil set over-reports the ledger, which costs a few tokens. The
	// opposite — assuming an unknown tool was read-only — would silently omit a
	// side effect from the record and invite the resuming agent to run it twice.
	// Extensions and MCP servers register arbitrary tools, so "unknown" is the
	// common case, not the edge one.
	ReadOnly *ReadOnlySet

	// Asker, if set, is the front end's question channel — the same seam the
	// ask_user_question tool uses, wired onto the agent so the LOOP can ask too.
	// Today its one caller is the prefix-change guard, which offers a compaction
	// before a cache-invalidating change lands (offerCompactOnPrefixChange).
	//
	// Nil is the normal state for a host with nobody to ask: one-shot runs, the
	// chat bridge, swarm children. Those skip the offer rather than blocking on a
	// question no one will answer — and rather than silently compacting on their
	// behalf, which is not what a guard is for. Assigned at build, before the
	// agent runs a turn.
	Asker Asker

	// Escalator, if set, hands the live session to a stronger model when a tool
	// loop persists past the detector's nudge (rung 3 of the stuck-loop hatch —
	// stall.go, escalate.go). The seam mirrors Asker: an interface here, a
	// per-session implementation in the host, nil in modes with no swap target.
	// Nil, or a host with no configured target, makes escalation inert.
	Escalator Escalator

	// AutoCompactPolicy, if set, supplies the live auto-compaction mode
	// (the config `auto_compact` knob, read per check so a settings edit
	// applies without an agent rebuild). Nil — and any unknown value —
	// resolves to AutoCompactSteps; see autoCompactMode.
	AutoCompactPolicy func() AutoCompactMode

	// running is the single-flight guard. It is set on entry to
	// Prompt/Continue/Compact and cleared on exit; a second concurrent
	// call sees it set and returns ErrBusy instead of interleaving its
	// transcript mutations with the in-flight run. It is an atomic so
	// the check-and-set needs no separate lock and never blocks.
	running atomic.Bool

	mu sync.Mutex

	// Lazy tool visibility (retro H2·b), guarded by mu. When lazyTools is on,
	// only the core group plus activeGroups are ADVERTISED; the rest are hidden
	// (still callable + gated) and surfaced as a capability note so the model can
	// activate_tools them. Resolved into a turnTools snapshot at the per-turn pin
	// (runLoop), so a mid-turn ActivateGroup lands on the next turn — one
	// deliberate cache write, never mid-turn churn. Off = advertise all (default).
	lazyTools    bool
	activeGroups map[string]bool
	// activationContinuationOff disables the built-in activation gate
	// (docs/proposals/activation-continuation.md): a segment that activated a
	// group is auto-continued with the tools live. The zero value keeps it ON
	// — the agreed default — wherever lazy tools are enabled. Guarded by mu.
	activationContinuationOff bool

	messages []provider.Message
	// rev increments whenever the transcript slice is replaced or a
	// message is appended. The TUI uses it as a cheap redraw cache key
	// so editor-only typing doesn't copy/rebuild a long transcript on
	// every keypress.
	rev uint64

	// transcriptEpoch increments only when the transcript is wholesale
	// REPLACED or shrunk (SetMessages, Compact) — never on a plain
	// append. Tools that cache per-transcript state (read-dedup) key on
	// it: a compaction or /clear that may have dropped an earlier read
	// from the context window bumps the epoch, transparently invalidating
	// any "you already read this" fingerprint so the next read returns the
	// full content again. Seeded per agent from agentEpochSeq so epochs
	// NEVER collide across agents: several live agents can share one tool
	// registry (bot mode mints an agent per chat), and a numerically
	// equal epoch from a different agent would otherwise let one
	// conversation's read dedup against another's — telling a model its
	// context holds content it never saw. Per-agent bumps stay in the
	// low 32 bits; the base occupies the high 32.
	transcriptEpoch uint64
	cost            CostTracker

	// sessionID / sessionPath identify the transcript file this
	// conversation persists to: sessionID is the file basename without
	// .jsonl (the id --resume accepts), sessionPath the absolute path.
	// The front end that owns the session records them via
	// AdoptSessionIdentity when it opens or swaps the session; empty
	// means live-only (no persistence), e.g. --no-session or bot-mode
	// group chats. Guarded by mu: a /sessions swap can land while a
	// turn's terva_status call reads them.
	sessionID   string
	sessionPath string
	// cacheID keys provider prompt caching (Request.PromptCacheKey). It
	// prefers the session's meta UUID over the file basename: basenames
	// are only unique within one directory, and every swarm child's
	// transcript is literally named session.json — concurrent children
	// keyed by basename share one cache route and evict each other.
	// Falls back to the basename for legacy files with no meta id.
	cacheID string

	// lastSent is the prompt prefix of the most recent request actually
	// dispatched (recordDispatch, from oneTurn) — nil until the first one goes
	// out. It is the only surviving copy of what the provider has cached once a
	// host swaps System/Tools/Model out from under it, which is what makes
	// compacting on the outgoing model possible. See promptPrefix.
	lastSent *promptPrefix

	// cacheAwareCompaction summarizes against the warm prefix instead of a
	// bespoke one (engine feature cache_aware_compaction, default off). Guarded
	// by mu and read at compaction time, so a settings toggle applies without
	// rebuilding the agent. See SetCacheAwareCompaction.
	cacheAwareCompaction bool

	// prefixGuard offers a compaction before a cache-invalidating change lands
	// (engine feature prefix_change_guard). Guarded by mu; see
	// SetPrefixChangeGuard and offerCompactOnPrefixChange.
	prefixGuard bool

	// stallDetect arms the stuck-loop detector (engine feature
	// stuck_loop_detection). Guarded by mu; see SetStallDetection.
	stallDetect bool

	// stuckLoopEscalate arms rung 3 (engine feature stuck_loop_escalation); it
	// depends on stallDetect (the detector is the trigger) and is inert without a
	// bound Escalator + a configured target. escalateAuto swaps without asking
	// (config escalation.auto). Both guarded by mu; see escalate.go.
	stuckLoopEscalate bool
	escalateAuto      bool

	// stall is the detector's per-turn state. Touched only on the turn goroutine
	// (runLoop resets and observes; oneTurn reads the nudge), so it carries no
	// lock — unlike stallDetect, which a host may toggle from another goroutine.
	stall stallTracker

	// queued holds user messages submitted while the agent is busy.
	// The loop appends them as normal user messages at safe
	// boundaries: before the next model call after a tool batch, or
	// after a text-only assistant turn finishes. It never interrupts
	// a running tool or cancels an in-flight provider request.
	queued []string
}

// agentEpochSeq hands each new agent a distinct transcript-epoch base
// (see the transcriptEpoch field comment for why collisions matter).
var agentEpochSeq atomic.Uint64

// NewAgent returns an Agent with sensible defaults.
func NewAgent(client provider.Client, model, system string, tools Registry) *Agent {
	return &Agent{
		Client:          client,
		Model:           model,
		System:          system,
		Tools:           tools,
		MaxSteps:        0, // 0 = unlimited
		MaxRetries:      3,
		RetryBaseDelay:  2 * time.Second,
		transcriptEpoch: agentEpochSeq.Add(1) << 32,
	}
}

// AdoptSessionIdentity records which transcript file this agent's
// conversation persists to; terva_status surfaces it to the model.
// Call it when binding or swapping the agent's session. Nil clears
// both fields — the conversation is live-only from here.
func (a *Agent) AdoptSessionIdentity(s *Session) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s == nil {
		a.sessionID, a.sessionPath, a.cacheID = "", "", ""
		return
	}
	a.sessionPath = s.Path
	a.sessionID = strings.TrimSuffix(filepath.Base(s.Path), ".jsonl")
	// Prefer the meta UUID for cache routing — globally unique where the
	// basename is not (all swarm children persist to a session.json).
	if a.cacheID = s.ID; a.cacheID == "" {
		a.cacheID = a.sessionID
	}
}

// SessionIdentity returns the transcript file this agent persists to:
// the id (file basename, what --resume accepts) and the absolute path.
// Both empty when the conversation is live-only.
func (a *Agent) SessionIdentity() (id, path string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessionID, a.sessionPath
}

// QueueMessage queues text to be injected as a user message at the
// next safe boundary of the active agent loop. It is non-blocking in
// the sense that it never waits for model/tool work; it only takes
// the transcript mutex briefly. Empty/whitespace-only messages are
// ignored.
func (a *Agent) QueueMessage(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	a.mu.Lock()
	a.queued = append(a.queued, text)
	a.mu.Unlock()
	return true
}

// RequeueFront puts text at the FRONT of the queue. Hosts use it to
// re-arm a prompt that must run next — e.g. the message that
// triggered an auto-compaction is requeued so it fires as soon as the
// condensed transcript is ready, ahead of anything the user queued
// while waiting. Empty/whitespace-only messages are ignored.
func (a *Agent) RequeueFront(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	a.mu.Lock()
	a.queued = append([]string{text}, a.queued...)
	a.mu.Unlock()
	return true
}

// ShiftQueuedMessage removes and returns the OLDEST queued message.
// Hosts use it when no agent loop is running to consume the queue
// in submission order: pop the head to start a fresh turn, and let
// that turn's loop drain the rest at its safe boundaries.
func (a *Agent) ShiftQueuedMessage() (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.queued) == 0 {
		return "", false
	}
	text := a.queued[0]
	a.queued = a.queued[1:]
	return text, true
}

// PendingQueuedMessages returns a snapshot of user messages waiting
// to be injected. Used by hosts to render the visible "sliding in"
// chips without consuming them.
func (a *Agent) PendingQueuedMessages() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.queued))
	copy(out, a.queued)
	return out
}

// QueuedMessageCount returns the number of messages waiting to be
// injected at the next safe boundary.
func (a *Agent) QueuedMessageCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.queued)
}

// PopQueuedMessage removes and returns the most recently queued
// message. Hosts use this for the slide-back keybinding.
func (a *Agent) PopQueuedMessage() (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := len(a.queued)
	if n == 0 {
		return "", false
	}
	text := a.queued[n-1]
	a.queued = a.queued[:n-1]
	return text, true
}

// DrainQueuedMessages discards and returns every queued message.
// Hosts use this on explicit cancel/clear so stale follow-ups do
// not run after the user aborted the turn.
func (a *Agent) DrainQueuedMessages() []string {
	return a.drainQueuedMessages()
}

func (a *Agent) drainQueuedMessages() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.queued))
	copy(out, a.queued)
	a.queued = nil
	return out
}

// SetQueuedMessages atomically replaces the pending queue, preserving order and
// dropping empty/whitespace-only entries. Hosts use it to edit or cancel queued
// messages before they inject (the queue.set control-plane command).
func (a *Agent) SetQueuedMessages(texts []string) {
	var cleaned []string
	for _, t := range texts {
		if t = strings.TrimSpace(t); t != "" {
			cleaned = append(cleaned, t)
		}
	}
	a.mu.Lock()
	a.queued = cleaned
	a.mu.Unlock()
}

func (a *Agent) appendQueuedAsUser(texts []string, synthetic bool, sink func(AgentEvent)) {
	for _, text := range texts {
		// Genuine queued prompts pass through the same guard as the
		// initial prompt, so a user_message intercept can't be bypassed
		// by typing while a turn is mid-flight. A rejected one is skipped
		// (not appended, no EvUserMessage); the synthetic gate nudge is
		// never gated.
		if !synthetic && a.BeforeUserMessage != nil && text != "" {
			allowed, reason, replacement := a.BeforeUserMessage(text)
			if !allowed {
				if reason == "" {
					reason = "message blocked by extension guard"
				}
				if sink != nil {
					sink(EvUserMessageRejected{Text: text, Reason: reason})
				}
				continue
			}
			if replacement != "" && replacement != text {
				text = replacement
			}
		}
		msg := provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: text}},
			Time:    time.Now(),
		}
		// Mark host-injected nudges (an at-close continuation-gate re-prompt) so
		// display surfaces can distinguish them from the user's own words — both
		// live (the event carries the message) and durably (the snapshot rebuilds
		// from the transcript). See WireMessage.Synthetic.
		if synthetic {
			msg.Meta = map[string]string{MetaSynthetic: "true"}
		}
		a.mu.Lock()
		a.messages = append(a.messages, msg)
		a.rev++
		a.mu.Unlock()
		a.fireMessageAppended(msg)
		if sink != nil {
			sink(EvUserMessage{Message: msg, Synthetic: synthetic})
		}
	}
}

// Messages returns a copy of the current transcript.
func (a *Agent) Messages() []provider.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]provider.Message, len(a.messages))
	copy(out, a.messages)
	return out
}

// Revision returns a monotonically increasing transcript version.
// It is cheap to query and changes whenever Messages() would return
// different transcript content because of append/set operations.
func (a *Agent) Revision() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rev
}

// TranscriptEpoch returns a counter that changes only when the transcript
// is wholesale replaced or compacted — not on a plain append. Tools that
// cache per-transcript state (read-dedup) use it to know when their cache
// may reference content no longer in the context window.
func (a *Agent) TranscriptEpoch() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.transcriptEpoch
}

// SetTools swaps the tool registry. Used by /reload-ext to hand
// the agent a fresh registry after extension subprocesses have been
// respawned (and their freshly-registered tools merged in). Reports whether
// the model-facing surface actually changed (name, description, or schema of
// any tool) — the cached prompt prefix serializes exactly that surface, so
// callers use the verdict to notify about a cache-breaking rebuild without
// false alarms from identical re-installs.
func (a *Agent) SetTools(reg Registry) (changed bool) {
	a.mu.Lock()
	changed = !registryEqual(a.Tools, reg)
	a.Tools = reg
	a.mu.Unlock()
	return changed
}

// registryEqual compares the model-facing surface of two registries: the same
// tool names each with equal description and schema bytes. Execute behavior is
// deliberately out of scope — the model (and the prompt cache) only sees the
// serialized name/description/schema triple.
func registryEqual(a, b Registry) bool {
	if len(a) != len(b) {
		return false
	}
	for name, at := range a {
		bt, ok := b[name]
		if !ok {
			return false
		}
		if at.Description() != bt.Description() || !bytes.Equal(at.Schema(), bt.Schema()) {
			return false
		}
	}
	return true
}

// EnableLazyTools turns on lazy tool visibility (retro H2·b): only the core
// group plus the given always-active groups are advertised; every other group
// starts hidden (still callable, still gated) and is offered as a capability
// note the model can act on with ActivateGroup. Idempotent setup; call once.
func (a *Agent) EnableLazyTools(active ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lazyTools = true
	a.activeGroups = make(map[string]bool, len(active))
	for _, g := range active {
		if g != "" && g != CoreToolGroup {
			a.activeGroups[g] = true
		}
	}
}

// SetActivationContinuation toggles activation continuation — the built-in
// at-close gate that resumes a Prompt when the ended segment activated a tool
// group, re-pinning so the continuation runs with the tools live
// (docs/proposals/activation-continuation.md). On by default wherever lazy
// tools are enabled; the engine-feature surface (stage 3) drives this setter.
func (a *Agent) SetActivationContinuation(on bool) {
	a.mu.Lock()
	a.activationContinuationOff = !on
	a.mu.Unlock()
}

// ActivationContinuationEnabled reports whether the activation gate is live
// for this agent: lazy tools on and the feature not switched off.
// activate_tools reads it to tell the model whether it will be continued
// automatically after finishing its reply.
func (a *Agent) ActivationContinuationEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lazyTools && !a.activationContinuationOff
}

// newlyActiveSincePin returns the sorted names of tools whose capability
// groups are active now but were not at the pin — what an activation
// continuation announces as newly live, and the dirty test for re-pinning at
// a segment boundary. Empty when nothing new is active. Activation is
// monotonic, so a non-empty result can never turn empty within one boundary.
func (a *Agent) newlyActiveSincePin(pinned map[string]bool) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.lazyTools {
		return nil
	}
	var names []string
	for name, t := range a.Tools {
		g := ToolGroup(t)
		if g == CoreToolGroup || pinned[g] || !a.activeGroups[g] {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ActivateGroup marks a capability group advertised from the next turn. Returns
// false if it was already active (or is the always-on core group). Visibility
// only — it never grants authority, and it takes effect at the next turn's pin
// (one deliberate cache write), never mid-turn.
func (a *Agent) ActivateGroup(group string) bool {
	if group == "" || group == CoreToolGroup {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeGroups == nil {
		a.activeGroups = map[string]bool{}
	}
	if a.activeGroups[group] {
		return false
	}
	a.activeGroups[group] = true
	return true
}

// ToolsInGroup returns the names of registered tools in a capability group,
// sorted. Empty means no such group is installed (an activate_tools guard).
func (a *Agent) ToolsInGroup(group string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var names []string
	for name, t := range a.Tools {
		if ToolGroup(t) == group {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// ToolSpecsInGroup returns the provider specs (name, description, schema) of the
// registered tools in a capability group, name-sorted for a stable render. It
// is the schema-bearing twin of ToolsInGroup: activate_tools echoes these into
// its result so the model can compose its next-turn call immediately, since the
// activated group's schemas only reach the advertised Tools array on the next
// turn (the per-turn pin). Reuses SpecsVisible so the sort matches the
// advertised order. Empty means no such group.
func (a *Agent) ToolSpecsInGroup(group string) []provider.Tool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Tools.SpecsVisible(func(name string) bool {
		t, ok := a.Tools[name]
		return ok && ToolGroup(t) == group
	})
}

// ActivateGroupsForTools activates the capability groups of the named tools —
// skill-driven activation (retro H2·b step 5): a skill declaring the tools it
// needs (its allowed-tools) can SURFACE their groups on load. Visibility only,
// so it never grants authority: dispatch and the permission gate keep resolving
// the full registry, so a revealed tool is still gated when actually called.
// A no-op unless lazy mode is on; names absent from this registry (e.g. an
// untrusted workspace never loaded that extension) and core-group names are
// skipped. Returns the groups newly activated (for a load notice), sorted.
func (a *Agent) ActivateGroupsForTools(names []string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.lazyTools {
		return nil
	}
	if a.activeGroups == nil {
		a.activeGroups = map[string]bool{}
	}
	var activated []string
	for _, n := range names {
		t, ok := a.Tools[n]
		if !ok {
			continue
		}
		g := ToolGroup(t)
		if g == CoreToolGroup || a.activeGroups[g] {
			continue
		}
		a.activeGroups[g] = true
		activated = append(activated, g)
	}
	sort.Strings(activated)
	return activated
}

// AdvertisedTools reports the current tool-advertisement decision as a predicate
// over tool names: whether a registered tool would be sent to the model this
// turn (core + active groups under lazy mode, or the VisibleTool override).
// filtered is false in the default all-advertised case — then visible admits
// every tool. It is the read-only twin of the per-turn pin (turnToolsLocked) for
// inspection surfaces like /context, which use it to separate the live
// advertised weight from installed-but-inactive schemas. The returned predicate
// is a pure function of the name and safe to call after the lock is released.
func (a *Agent) AdvertisedTools() (visible func(name string) bool, filtered bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	tt := a.turnToolsLocked(a.Tools)
	if tt.visible == nil {
		return func(string) bool { return true }, false
	}
	return tt.visible, true
}

// CapabilityNote returns the inactive-tool-groups note that rides this turn's
// ephemeral tail under lazy visibility — the names (not schemas) of the hidden
// groups the model can activate_tools. Empty when lazy mode is off or nothing is
// hidden. It is the exact text oneTurn appends to EphemeralContext, so /context
// can account for the few bytes deferred discovery actually costs (the schemas
// are gone from the window, but their names are not free).
func (a *Agent) CapabilityNote() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.turnToolsLocked(a.Tools).capabilityNote
}

// turnTools is the per-turn tool-advertisement decision, pinned as a unit so the
// advertised specs and the capability note the model reads can never drift.
type turnTools struct {
	visible        func(name string) bool // nil = advertise every registered tool
	capabilityNote string                 // inactive-group summary for the ephemeral tail (lazy mode)
	groups         map[string]bool        // active-group snapshot at the pin (lazy mode; nil otherwise); never mutated
}

// turnToolsLocked resolves this turn's advertisement; a.mu must be held. An
// embedder's VisibleTool wins (the raw visibility seam); otherwise lazy mode
// advertises core + the active groups and notes the inactive ones; otherwise
// nil (advertise all — today's default).
func (a *Agent) turnToolsLocked(reg Registry) turnTools {
	if a.VisibleTool != nil {
		return turnTools{visible: a.VisibleTool}
	}
	if !a.lazyTools {
		return turnTools{}
	}
	active := make(map[string]bool, len(a.activeGroups))
	for g := range a.activeGroups {
		active[g] = true
	}
	return turnTools{
		visible:        lazyVisible(reg, active),
		capabilityNote: inactiveGroupNote(reg, active),
		groups:         active,
	}
}

// lazyVisible advertises a tool iff its group is core or currently active. A
// name absent from reg is advertised (never hidden by a stale predicate).
func lazyVisible(reg Registry, active map[string]bool) func(name string) bool {
	return func(name string) bool {
		t, ok := reg[name]
		if !ok {
			return true
		}
		g := ToolGroup(t)
		return g == CoreToolGroup || active[g]
	}
}

// inactiveGroupNote summarizes the groups hidden this turn — the model reads it
// from the ephemeral tail and can bring one in with activate_tools. Empty when
// nothing is hidden. It lists tool names (not schemas) so discovery costs a few
// bytes, not the whole schema (retro H2·b: the cache-cheap capability line).
func inactiveGroupNote(reg Registry, active map[string]bool) string {
	byGroup := map[string][]string{}
	for name, t := range reg {
		g := ToolGroup(t)
		if g == CoreToolGroup || active[g] {
			continue
		}
		byGroup[g] = append(byGroup[g], name)
	}
	if len(byGroup) == 0 {
		return ""
	}
	groups := make([]string, 0, len(byGroup))
	for g := range byGroup {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	var b strings.Builder
	// Model-facing prompt injection (rides the ephemeral tail like the
	// context-pressure note), so it is translatable via the prompts catalog.
	b.WriteString(i18n.P("tools.lazy.inactive_groups",
		"[inactive tool groups] These capabilities are installed but their tool schemas are not loaded. Call activate_tools with a group name to load them; activation is visibility only — each tool still requires its normal permission when used:"))
	for _, g := range groups {
		names := byGroup[g]
		sort.Strings(names)
		fmt.Fprintf(&b, "\n  - %s: %s", g, strings.Join(names, ", "))
	}
	return b.String()
}

// SetSystem swaps the system prompt under the agent's lock — the live twin of
// SetTools for view rebuilds (an approval-mode or auto-swarm toggle, a
// reloaded extension's context). The run loop pins both at turn start, so a
// mid-turn swap affects the next turn only (same contract as SetTools).
// Reports whether the prompt actually changed, for the same cache-breaking
// notification purpose as SetTools.
func (a *Agent) SetSystem(system string) (changed bool) {
	a.mu.Lock()
	changed = a.System != system
	a.System = system
	a.mu.Unlock()
	return changed
}

// SetContextProvider swaps the per-turn context provider live (the turn loop
// reads it under a.mu). Used to re-wire lore after an edit so the next turn sees
// the change without a new session.
func (a *Agent) SetContextProvider(fn func() string) {
	a.mu.Lock()
	a.ContextProvider = fn
	a.mu.Unlock()
}

// SetContextProviderPeek swaps the side-effect-free context preview (the twin of
// ContextProvider used by the /context size view) live, so a host reloading lore
// keeps the preview in sync with the provider — otherwise /context would size
// stale lore after an edit.
func (a *Agent) SetContextProviderPeek(fn func() string) {
	a.mu.Lock()
	a.ContextProviderPeek = fn
	a.mu.Unlock()
}

// ToolsSnapshot returns the live tool registry under the lock SetTools writes
// with, so a reader on another goroutine (e.g. the web /context view, which can
// run while an extension/MCP toggle calls SetTools) never races the swap.
// SetTools always installs a fresh map rather than mutating in place, so the
// returned Registry is safe to range read-only.
func (a *Agent) ToolsSnapshot() Registry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Tools
}

// ContextPreview renders the per-turn ephemeral context for a size/inspection
// view WITHOUT side effects. It snapshots the provider function under the lock
// SetContextProvider writes with (avoiding a race with a live lore/trust reload),
// then calls it OUTSIDE the lock since the closure may touch the agent. Prefers
// the side-effect-free Peek twin; empty when neither is set.
func (a *Agent) ContextPreview() string {
	a.mu.Lock()
	fn := a.ContextProviderPeek
	if fn == nil {
		fn = a.ContextProvider
	}
	a.mu.Unlock()
	if fn == nil {
		return ""
	}
	return fn()
}

// LookupTool returns the tool registered under name in the live
// registry. Race-free against SetTools, so it is safe to call from a
// goroutine other than the turn loop (e.g. an extension's host_tool_call
// dispatch).
func (a *Agent) LookupTool(name string) (Tool, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.Tools[name]
	return t, ok
}

// SetMessages replaces the transcript (used when resuming a session).
func (a *Agent) SetMessages(msgs []provider.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = append(a.messages[:0], msgs...)
	a.rev++
	a.transcriptEpoch++
}

// SetModel swaps the active model under the lock that oneTurn snapshots
// request fields with, so a host can change models on another goroutine
// without racing a starting turn. It only mutates the model id — the
// caller is responsible for ensuring the current Client can serve the
// new model (same provider AND same resolved endpoint). When the model
// routes to a different base URL or needs a different client, rebuild
// the agent (or use SetClientAndModel) instead; mutating the id alone
// would keep firing requests at the previous endpoint.
func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Model = model
	a.refreshMaxTokensLocked()
}

// refreshMaxTokensLocked re-derives the per-turn output budget from the
// current model's advertised cap after a model swap. MaxTokens is seeded
// once at build time from the launch model's MaxOutput and would otherwise
// stay pinned to it: a swap to a lower-cap model leaves a stale, too-large
// budget (the provider request builders clamp it at send time, but the
// agent's own field — read by cost/context accounting and terva_status —
// stays wrong). Best-effort: only a successful, non-zero lookup updates the
// field, so a swap to a model missing from the catalog leaves the previous
// working budget untouched rather than zeroing it. Caller holds a.mu.
func (a *Agent) refreshMaxTokensLocked() {
	if m, err := provider.FindModel("", a.Model); err == nil && m.MaxOutput > 0 {
		a.MaxTokens = m.MaxOutput
	}
}

// SetReasoning swaps the reasoning/thinking level under the same lock oneTurn
// snapshots request fields with, so a host can change it on another goroutine
// without racing a starting turn (Reasoning is read at turn start). Empty
// disables thinking. The caller normalizes the level.
func (a *Agent) SetReasoning(level string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Reasoning = level
}

// SetClientAndModel atomically swaps both the provider client and the
// model, for hosts that re-resolve a fresh client (a different endpoint,
// rotated credentials) while keeping the same transcript. Both fields
// move together under the lock so a turn can never observe the new
// client paired with the old model or vice versa.
func (a *Agent) SetClientAndModel(client provider.Client, model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Client = client
	a.Model = model
	a.refreshMaxTokensLocked()
}

// Usage returns the current provider client's subscription usage
// snapshot (5h/weekly windows, credits), with ok=false when the
// provider reports none. It probes the live a.Client through any
// wrapper layers, so a SetClientAndModel swap to a different provider
// transparently surfaces the new provider's reporter (or none).
func (a *Agent) Usage() (provider.UsageSnapshot, bool) {
	a.mu.Lock()
	c := a.Client
	a.mu.Unlock()
	return provider.ClientUsage(c)
}

// RefreshUsage pulls a fresh usage snapshot, fetching from the provider's
// usage/balance endpoint when it has one (OpenRouter, DeepSeek) and otherwise
// returning the passively-observed snapshot (codex). It BLOCKS on the fetch,
// so callers must run it off the UI goroutine.
func (a *Agent) RefreshUsage(ctx context.Context) (provider.UsageSnapshot, bool) {
	a.mu.Lock()
	c := a.Client
	a.mu.Unlock()
	return provider.ClientRefreshUsage(ctx, c)
}

// UsageRefreshable reports whether the current provider fetches its usage from
// an endpoint — i.e. /usage should refresh in the background and show a
// loading state rather than rendering instantly from headers.
func (a *Agent) UsageRefreshable() bool {
	a.mu.Lock()
	c := a.Client
	a.mu.Unlock()
	return provider.ClientNeedsUsageFetch(c)
}

// SupportsResets reports whether the current provider exposes consumable usage
// resets (codex banked resets) — the gate for offering a /resets affordance.
// It probes the live a.Client through wrapper layers, so a SetClientAndModel
// swap surfaces the new provider's capability.
func (a *Agent) SupportsResets() bool {
	a.mu.Lock()
	c := a.Client
	a.mu.Unlock()
	return provider.ClientSupportsResets(c)
}

// ListResets returns the provider's usage-reset credits (available and spent),
// or nil when the provider offers none. It BLOCKS on the provider's endpoint,
// so callers must run it off the UI goroutine.
func (a *Agent) ListResets(ctx context.Context) ([]provider.UsageReset, error) {
	a.mu.Lock()
	c := a.Client
	a.mu.Unlock()
	return provider.ClientListResets(ctx, c)
}

// ConsumeReset redeems one reset credit by id. It is IRREVERSIBLE and spends a
// scarce, provider-granted credit, so callers MUST gate it behind explicit user
// confirmation. It BLOCKS on the provider's endpoint.
func (a *Agent) ConsumeReset(ctx context.Context, id string) (provider.UsageResetResult, error) {
	a.mu.Lock()
	c := a.Client
	a.mu.Unlock()
	return provider.ClientConsumeReset(ctx, c, id)
}

// Cost returns the cumulative usage. The CostTracker carries its own
// lock so this is safe to call concurrently with a running turn, which
// folds usage in from the stream goroutine.
func (a *Agent) Cost() provider.Usage {
	return a.cost.CumulativeTotal()
}

// SeedCost sets the cumulative usage as a baseline before the first
// turn runs. Used when transferring state from another agent (model
// or provider switch) so the running cost meter doesn't reset to 0.
func (a *Agent) SeedCost(u provider.Usage) {
	a.cost.SetTotal(u)
}

// LastTurnUsage returns the per-turn usage of the most recent
// completed turn. Drives the "context used" gauge in the status bar
// without waiting for the next turn to land.
func (a *Agent) LastTurnUsage() provider.Usage {
	return a.cost.LastTurnUsage()
}

// SeedLastTurnUsage primes the per-turn snapshot. Used on resume so
// the gauge reflects the prompt size of the last turn in the session
// file instead of starting at zero.
func (a *Agent) SeedLastTurnUsage(u provider.Usage) {
	a.cost.SetLastTurn(u)
}

// acquire claims the single-flight guard. It returns a release func and
// true on success, or nil and false if a run is already in progress.
// Callers must defer release() once they hold the guard.
func (a *Agent) acquire() (release func(), ok bool) {
	if !a.running.CompareAndSwap(false, true) {
		return nil, false
	}
	return func() { a.running.Store(false) }, true
}

// Prompt sends a user message and runs the agent loop until the model
// stops or an error occurs. Events are delivered via sink in order.
// sink must not block the caller for long; buffer as needed. Prompt is
// single-flight: it returns ErrBusy if another Prompt/Continue/Compact
// is already in progress.
func (a *Agent) Prompt(ctx context.Context, text string, images []provider.ImageBlock, sink func(AgentEvent)) error {
	release, ok := a.acquire()
	if !ok {
		return ErrBusy
	}
	defer release()
	if sink == nil {
		sink = func(AgentEvent) {}
	}
	sink = a.wrapSink(sink)
	// Consult the user-message guard before anything is recorded: a
	// rejection must leave no trace in the transcript and start no turn.
	// Skipped for an empty prompt (image-only submit — nothing to judge).
	if a.BeforeUserMessage != nil && text != "" {
		allowed, reason, replacement := a.BeforeUserMessage(text)
		if !allowed {
			if reason == "" {
				reason = "message blocked by extension guard"
			}
			sink(EvUserMessageRejected{Text: text, Reason: reason})
			sink(EvDone{})
			return nil
		}
		if replacement != "" && replacement != text {
			text = replacement
		}
	}
	content := []provider.Content{}
	if text != "" {
		content = append(content, provider.TextBlock{Text: text})
	}
	for _, img := range images {
		content = append(content, img)
	}
	user := provider.Message{Role: provider.RoleUser, Content: content, Time: time.Now()}

	a.mu.Lock()
	a.messages = append(a.messages, user)
	a.rev++
	a.mu.Unlock()
	a.fireMessageAppended(user)
	sink(EvUserMessage{Message: user})

	return a.runLoop(ctx, sink)
}

// Continue runs the agent loop against the existing transcript. Used
// after appending tool results manually or to retry. Like Prompt it is
// single-flight and returns ErrBusy if a run is already in progress.
func (a *Agent) Continue(ctx context.Context, sink func(AgentEvent)) error {
	release, ok := a.acquire()
	if !ok {
		return ErrBusy
	}
	defer release()
	if sink == nil {
		sink = func(AgentEvent) {}
	}
	sink = a.wrapSink(sink)
	return a.runLoop(ctx, sink)
}

// EmitLifecycle delivers a host-lifecycle event to the OnEvent observer
// (the extension fanout / hook engine) directly, independent of an active
// Prompt. Host-driven compaction runs OUTSIDE the Prompt loop — callers
// invoke Compact on their own — so its EvCompactStart/EvCompactEnd would
// otherwise never reach OnEvent (the per-call sink that carries them is the
// host's own UI sink, not the wrapped one). Compaction triggers call this so
// extensions see compact_start / transcript_compacted. Nil-safe. (The
// mid-turn auto-compact inside runLoop doesn't need it: its sink is already
// the wrapped one.)
func (a *Agent) EmitLifecycle(ev AgentEvent) {
	for _, fn := range a.eventObservers() {
		fn(ev)
	}
}

// wrapSink composes the per-call sink with the registered event observers so
// the extension manager (or any other observer) sees every AgentEvent without
// having to thread itself through every Prompt callsite. The observer set is
// snapshotted once per Prompt rather than per event: the returned closure is on
// the token-delta hot path and must not take a lock.
func (a *Agent) wrapSink(sink func(AgentEvent)) func(AgentEvent) {
	obs := a.eventObservers()
	if len(obs) == 0 {
		return sink
	}
	return func(ev AgentEvent) {
		for _, fn := range obs {
			fn(ev)
		}
		sink(ev)
	}
}

// turnPin is the cached prompt prefix — the system prompt, the tool registry,
// and the per-turn tool visibility — snapshotted once and threaded through
// every step it covers. One pin spans one SEGMENT of the loop: the steps
// between StopEnd boundaries (queued input, an at-close gate). A boundary
// deliberately reuses the pin unchanged unless the ended segment activated a
// tool group under activation continuation, in which case it refreshes
// (repinForContinuation) so the continuation runs with the tools live —
// docs/proposals/activation-continuation.md.
type turnPin struct {
	system string
	tools  Registry
	turn   turnTools
}

// pinTurn snapshots the cached prompt prefix for a segment. A host may swap
// System/Tools on another goroutine mid-turn — an extension's refresh_context
// / set_withdrawn_tools, or /reload-ext — and activate_tools may extend the
// active group set. Re-reading any of that between the model→tool→model steps
// of one segment would evict the prompt cache and change the tools the model
// is mid-way through using — very disruptive. By snapshotting once, a
// mid-segment change updates the agent fields but cannot affect in-flight
// steps; the next pin — a dirty segment boundary, or the next Prompt — picks
// it up. (The cache-free ephemeral tail and the growing transcript still
// update per step — only the cached prefix is frozen.)
func (a *Agent) pinTurn() turnPin {
	a.mu.Lock()
	defer a.mu.Unlock()
	return turnPin{system: a.System, tools: a.Tools, turn: a.turnToolsLocked(a.Tools)}
}

// fireContinuationGate consults the at-close gates in registration order and
// returns the first willing gate's nudge and cause, consuming one unit of that
// gate's per-Prompt budget. fires is indexed alongside gates; declines cost
// nothing.
func fireContinuationGate(gates []ContinuationGate, fires []int, stop provider.StopReason) (nudge, cause string, ok bool) {
	for i, g := range gates {
		budget := g.Cap
		if budget <= 0 {
			budget = 1
		}
		if fires[i] >= budget {
			continue
		}
		if nudge, ok := g.Fire(stop); ok && nudge != "" {
			fires[i]++
			return nudge, g.Cause, true
		}
	}
	return "", "", false
}

// activationContinuationCap bounds the built-in activation gate's fires per
// Prompt. Activation is monotonic (a group cannot be newly activated twice),
// so continuation chains are structurally bounded by the group count and this
// cap should never bind — defense in depth, deliberately a constant rather
// than configuration (the proposal's Decisions).
const activationContinuationCap = 3

// activationGate builds the built-in activation continuation gate for one
// runLoop. It is appended AFTER every host gate — registration order is
// priority, and correctness gates (open work, the swarm hold) outrank the
// convenience continuation. It fires when the ended segment newly activated a
// tool group, so a model that deliberately finished its reply after
// activate_tools is re-prompted with those tools actually live. pin is the
// loop's live pin variable: the gate diffs against whatever pin is current at
// that boundary, and the boundary then refreshes it (repinForContinuation).
func (a *Agent) activationGate(pin *turnPin) ContinuationGate {
	return ContinuationGate{
		Cause: "activation",
		Cap:   activationContinuationCap,
		Fire: func(provider.StopReason) (string, bool) {
			newly := a.newlyActiveSincePin(pin.turn.groups)
			if len(newly) == 0 {
				return "", false
			}
			return "[activation continuation] Now live: " + strings.Join(newly, ", ") + ". Continue where you left off.", true
		},
	}
}

// repinForContinuation refreshes the pin at a segment boundary when the ended
// segment activated a tool group and activation continuation is on; otherwise
// it returns the pin unchanged — the deliberate reuse the stage-0 contract
// pinned. A refresh is one tools-array cache write, the same write the next
// Prompt would have paid. It shares newlyActiveSincePin with the activation
// gate so a fired gate's "now live" promise and the re-pin can never disagree.
func (a *Agent) repinForContinuation(pin turnPin) turnPin {
	if !a.ActivationContinuationEnabled() {
		return pin
	}
	if len(a.newlyActiveSincePin(pin.turn.groups)) == 0 {
		return pin
	}
	return a.pinTurn()
}

func (a *Agent) runLoop(ctx context.Context, sink func(AgentEvent)) error {
	// One pin per segment; the whole Prompt is a single segment until a
	// boundary refreshes it (repinForContinuation) — see pinTurn.
	pin := a.pinTurn()

	// Stuck-loop detection spans this turn's tool-use steps and no further: a
	// repeat across turns is usually the user asking again, not the model stuck.
	a.stall.reset()

	// The at-close continuation gates, snapshotted per Prompt like the
	// observers, with per-gate fire counts enforcing each gate's Cap
	// (default 1) — so a gate that always says "continue" can't loop the
	// model forever. The built-in activation gate runs last: host
	// correctness gates outrank the convenience continuation.
	gates := a.continuationGateSnapshot()
	if a.ActivationContinuationEnabled() {
		gates = append(gates, a.activationGate(&pin))
	}
	gateFires := make([]int, len(gates))

	// Mid-turn auto-compact hysteresis: after a compaction fires, the
	// valve stays disarmed until the measured fraction actually drops
	// below the threshold again (one completed request refreshes it).
	// Without the re-arm rule, a tail too big to condense away — say one
	// enormous tool result inside the keep-tail — would re-trigger a
	// futile summarization on every subsequent step.
	compactArmed := true

	for step := 1; a.MaxSteps <= 0 || step <= a.MaxSteps; step++ {
		// Messages queued while the agent was busy are delivered
		// before the next model call. This is the safe boundary:
		// any previous tool batch has already completed and its
		// results have been appended, but no new provider request has
		// started yet.
		if pending := a.drainQueuedMessages(); len(pending) > 0 {
			a.appendQueuedAsUser(pending, false, sink)
			// The queue just shrank, and no host asked it to — this is the
			// only mutation the host does not perform itself, so it is the
			// only one it cannot announce without being told. Left unsaid, a
			// client that mirrors the queue keeps rendering messages that are
			// already in the transcript above.
			a.fireQueueDrained(pending)
		}

		// Mid-turn auto-compact, at the same safe boundary. A long
		// agentic turn (one prompt, many tool steps) can grow the
		// transcript past the context window with no turn boundary in
		// between — the pre-turn check in PromptWithPolicy never gets
		// another look. Each step's usage refreshes ContextFraction, so
		// condense here the moment it crosses the threshold. Step 1 is
		// exempt: its fraction reading predates this turn (the pre-turn
		// policy owns that boundary), and right after a pre-turn compact
		// the reading is an estimate that must not double-fire. Gated on
		// the `steps` mode — `turns` restores the boundary-only policy
		// and `off` disables auto-compaction entirely.
		if step > 1 && a.autoCompactMode() == AutoCompactSteps {
			if a.ContextFraction() < AutoCompactThreshold {
				compactArmed = true
			} else if compactArmed && a.CanCompact(AutoCompactKeepTail) {
				compactArmed = false
				sink(EvCompactStart{Reason: "context near limit (mid-turn)"})
				cres, cerr := a.compactMidTurn(ctx, AutoCompactKeepTail)
				if errors.Is(cerr, ErrNothingToCompact) {
					cerr = nil
				}
				end := EvCompactEnd{Usage: cres.Usage}
				if cerr != nil {
					end.Err = cerr.Error()
				}
				sink(end)
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// A failed compaction is best-effort: the next request may
				// still fit, and if it doesn't, the provider's context-length
				// error surfaces through the normal error path.
			}
		}

		sink(EvTurnStart{Step: step})
		if a.BeforeTurn != nil {
			if allowed, reason := a.BeforeTurn(step); !allowed {
				if reason == "" {
					reason = i18n.T("turn blocked by extension guard")
				}
				sink(EvTurnEnd{Stop: provider.StopError, Err: fmt.Errorf("%s", reason)})
				sink(EvDone{})
				return nil
			}
		}

		var (
			stop         provider.StopReason
			assistantMsg provider.Message
			commit       func()
			err          error
		)
		imageRounds := 0
		for attempt := 0; ; attempt++ {
			stop, assistantMsg, commit, err = a.oneTurn(ctx, pin.system, pin.tools, pin.turn, sink)
			sink(EvTurnEnd{Stop: stop, Err: err})
			if err == nil {
				break
			}
			// Image-rejection recovery: the provider refused an image in the
			// transcript (a 400 about invalid/unreadable image data — e.g. a
			// degenerate 1x1 or corrupt screenshot). Replace the *most recent*
			// image with a short text note and retry, peeling images off
			// newest-first across rounds until the turn succeeds. This is
			// surgical and cache-friendly: only the offending image (plus any
			// newer than it) is dropped, never one before it, so the cached
			// prefix up to the culprit survives — everything after it is dead
			// cache once the culprit is replaced anyway. Bounded by the image
			// count and a hard cap so a non-image 400 can't loop. Runs before
			// the transient-retry check because a 400 is otherwise terminal.
			if isImageRejectionError(err) && imageRounds < maxImageRecoveryRounds {
				if sha, ok := a.neutralizeLastTranscriptImage(); ok {
					imageRounds++
					a.dropLastAssistantMessage()
					// Persist the drop so a resumed session re-applies it instead
					// of re-sending the bad image (fired outside the agent lock).
					a.fireImageExcluded(sha)
					attempt-- // recovery rounds don't consume the transient-retry budget
					continue
				}
			}
			if !a.canRetryError(err, attempt) {
				break
			}
			// This attempt is being retried: drop its (possibly partial)
			// assistant message from memory and do NOT commit it, so the
			// durable session never records the abandoned attempt.
			a.dropLastAssistantMessage()
			if sleepErr := sleepRetry(ctx, a.retryDelay(attempt, err)); sleepErr != nil {
				return sleepErr
			}
		}
		// The turn is final (success or non-retryable error). Persist and
		// emit the kept assistant message exactly once, before propagating
		// any error, so a final-but-errored turn still records what landed.
		if commit != nil {
			commit()
		}
		if err != nil {
			return err
		}

		if stop == provider.StopToolUse {
			// Execute each tool call, append a single tool-results message, continue.
			toolMsg, hadError := a.executeTools(ctx, assistantMsg, pin.tools, sink)
			a.mu.Lock()
			a.messages = append(a.messages, toolMsg)
			a.rev++
			// Some provider wire formats can't carry images inside a tool
			// result: OpenAI chat-completions only accepts text in a `tool`
			// message, and the OpenAI Responses route's function_call_output
			// is a bare string. Those clients (every openai-wire provider —
			// openai, openai-compatible, ollama, groq, xai, kimi, azure, … —
			// plus openai-codex) declare ClientCapabilities.MirrorsToolImages,
			// so when a tool result contains images we mirror them into a
			// synthetic user message immediately after the tool result, where
			// they DO serialize correctly and reach vision models. Providers
			// that carry tool-result images natively (Anthropic, Gemini)
			// declare nothing and are left untouched.
			var imageMirror provider.Message
			// Use the unwrapping helper: openai-responses is wrapped in
			// a renamedClient (openai-responses), so a
			// direct type assertion on a.Client would miss the capability.
			// The mirror additionally requires the model to accept image
			// input at all — mirroring screenshots to a vision-less model
			// wastes tokens at best and 400s at worst. Unknown models keep
			// the capability's default (true), preserving old behavior.
			mirrorImages := provider.ClientMirrorsToolImages(a.Client)
			if mirrorImages {
				if m, err := provider.FindModel("", a.Model); err == nil && !m.Has(provider.CapImageInput) {
					mirrorImages = false
				}
			}
			if mirrorImages {
				if mirror := mirrorToolImagesAsUser(toolMsg); len(mirror.Content) > 0 {
					a.messages = append(a.messages, mirror)
					a.rev++
					imageMirror = mirror
				}
			}
			a.mu.Unlock()
			a.fireMessageAppended(toolMsg)
			if len(imageMirror.Content) > 0 {
				a.fireMessageAppended(imageMirror)
			}
			// If context was cancelled during tool execution, bail out.
			if err := ctx.Err(); err != nil {
				sink(EvDone{})
				return err
			}
			// Feed the just-completed step to the stuck-loop detector; a trip
			// stages a one-turn nudge that the next oneTurn rides on the
			// ephemeral tail (never the transcript). If the loop has persisted
			// past the nudge, rung 3 may offer to escalate to a stronger model
			// (inert unless a host bound an Escalator). A user-chosen "stop"
			// ends the turn cleanly; an escalation continues the loop on the new
			// model with a handoff marker staged.
			if a.stallDetectionOn() {
				for _, ev := range a.stall.observe(assistantMsg, toolMsg) {
					rec := StallRecord{Axis: ev.axis, Tool: ev.tool, Detail: ev.detail}
					a.fireStall(rec)                // durable: the session row
					sink(EvStall{StallRecord: rec}) // live: UI + extension observers
				}
				if a.maybeEscalate(ctx, sink) {
					sink(EvDone{})
					return nil
				}
			}
			_ = hadError
			continue
		}

		// If the assistant stopped without tool calls but a message was
		// queued while it was speaking, loop once more so that message
		// is appended and answered instead of waiting until a later
		// top-level prompt. This is a segment boundary: real input
		// outranks any gate (no synthetic nudge is injected), and the pin
		// refreshes only when the ended segment activated a tool group —
		// otherwise it is deliberately reused.
		if ctx.Err() == nil && a.QueuedMessageCount() > 0 {
			pin = a.repinForContinuation(pin)
			continue
		}

		// At-close gates: when the model finishes naturally but a gate
		// still has work for it (a blocking context card, running
		// sub-agents, a freshly activated tool group), re-prompt with
		// that gate's nudge, appended as a user turn so the model can
		// respond. Registration order is priority order — the first gate
		// that fires wins the boundary, the rest wait for the next
		// natural stop — and each gate is capped per Prompt (Cap,
		// default 1). Also a segment boundary: the pin refreshes only
		// when the ended segment activated a tool group, so an activation
		// gate's continuation (and any other gate's, incidentally) runs
		// with the new tools live.
		if ctx.Err() == nil && stop == provider.StopEnd {
			if nudge, cause, ok := fireContinuationGate(gates, gateFires, stop); ok {
				sink(EvContinuation{Cause: cause})
				a.appendQueuedAsUser([]string{nudge}, true, sink)
				pin = a.repinForContinuation(pin)
				continue
			}
		}

		// Terminal stop (end, length, error, aborted).
		sink(EvDone{})
		return nil
	}
	if a.MaxSteps > 0 {
		sink(EvDone{})
		return i18n.Errorf("max steps (%d) exceeded", a.MaxSteps)
	}
	return nil
}

// canRetryError decides whether a failed turn attempt is retried.
// Classification is typed: in-tree clients return
// *provider.ProviderError whose Transient field encodes the wire
// protocol's own retry vocabulary (set where that knowledge lives),
// and bare transport failures classify by error type via
// provider.IsTransportError. The old substring-needle list is gone —
// it retried "prompt is too long: 208500 tokens" because "500"
// matched. Untyped errors from custom SDK clients no longer retry;
// returning *provider.ProviderError is the documented opt-in.
func (a *Agent) canRetryError(err error, attempt int) bool {
	if err == nil || a.MaxRetries <= 0 || attempt >= a.MaxRetries {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var pe *provider.ProviderError
	if errors.As(err, &pe) {
		// Quota/billing exhaustion can arrive as a 429 that is
		// technically transient but never recovers within a retry
		// window; don't burn attempts on it.
		if isNonRetryableProviderLimit(strings.ToLower(pe.Msg)) {
			return false
		}
		return pe.Transient
	}
	return provider.IsTransportError(err)
}

func isNonRetryableProviderLimit(msg string) bool {
	needles := []string{
		"usage limit", "monthly usage limit", "freeusagelimit", "gousagelimit",
		"available balance", "insufficient_quota", "out of budget", "quota exceeded", "billing",
	}
	for _, needle := range needles {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// retryDelay returns the wait before retry attempt n. A server-stated
// Retry-After wins over the default exponential backoff, capped so a
// hostile or misconfigured header can't stall the turn for minutes.
func (a *Agent) retryDelay(attempt int, err error) time.Duration {
	var pe *provider.ProviderError
	if errors.As(err, &pe) && pe.RetryAfter > 0 {
		const maxRetryAfter = 30 * time.Second
		if pe.RetryAfter > maxRetryAfter {
			return maxRetryAfter
		}
		return pe.RetryAfter
	}
	base := a.RetryBaseDelay
	if base <= 0 {
		base = 2 * time.Second
	}
	return base * time.Duration(1<<attempt)
}

// imageRejectedNote replaces an image the provider refused to accept. It tells
// the model (and, on resume, the reader) why the picture is gone, in place of
// the bytes that broke the turn.
const imageRejectedNote = "[image omitted: the model's provider rejected it as an invalid or unreadable image]"

// isImageRejectionError reports whether a failed turn was the provider refusing
// an image we sent — either bad image data, or a content schema that doesn't
// accept images at all — rather than a transient fault. Matched on the message
// text so it works across providers and whether or not the error is a typed
// ProviderError: OpenAI's "does not represent a valid image", or DeepSeek's
// "unknown variant `image_url`, expected `text`" (a multimodal-less API). Only
// consulted on a non-nil error, so a positive phrase in a success path can't
// false-trigger. The catalog's CapImageInput should stop these from being sent
// in the first place; this is the safety net for a model mis-marked as vision.
func isImageRejectionError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "image") {
		return false
	}
	for _, p := range []string{
		"valid image", // "does not represent a valid image"
		"invalid image",
		"image data",        // "the image data you provided"
		"process the image", // "unable to process the image"
		"process image",
		"image you provided",
		"unsupported image",
		"corrupt image",
		"decode the image",
		"image_url", // DeepSeek "unknown variant `image_url`, expected `text`"
	} {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// maxImageRecoveryRounds caps how many images a single turn will peel off
// chasing an image-rejection 400, so a misclassified non-image error (or a
// pathological transcript) can't turn into an unbounded run of round-trips.
// Real transcripts carry far fewer live images than this.
const maxImageRecoveryRounds = 16

// imageSHA256 is the content address of an image's raw bytes — the key an
// exclude_image directive matches on, so one directive drops every copy of the
// image (tool result + codex mirror) regardless of position.
func imageSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// neutralizeLastTranscriptImage replaces the single most-recent ImageBlock in
// the transcript — scanning from the end, including an image nested in a tool
// result — with a short text note, returning its content sha256 and whether one
// was found. The retry loop calls it repeatedly to peel images off newest-first
// until a turn that 400'd on an image succeeds, so only the offending image
// (and any newer than it) is dropped and the cached prefix before it is
// preserved. Bumps rev + transcriptEpoch when it changes anything; the returned
// hash lets the caller persist the drop as a session directive.
func (a *Agent) neutralizeLastTranscriptImage() (sha string, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for mi := len(a.messages) - 1; mi >= 0; mi-- {
		content := a.messages[mi].Content
		for ci := len(content) - 1; ci >= 0; ci-- {
			switch v := content[ci].(type) {
			case provider.ImageBlock:
				h := imageSHA256(v.Data)
				content[ci] = provider.TextBlock{Text: imageRejectedNote}
				a.rev++
				a.transcriptEpoch++
				return h, true
			case provider.ToolResultBlock:
				for ii := len(v.Content) - 1; ii >= 0; ii-- {
					if ib, isImg := v.Content[ii].(provider.ImageBlock); isImg {
						h := imageSHA256(ib.Data)
						v.Content[ii] = provider.TextBlock{Text: imageRejectedNote}
						content[ci] = v
						a.rev++
						a.transcriptEpoch++
						return h, true
					}
				}
			}
		}
	}
	return "", false
}

func sleepRetry(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (a *Agent) dropLastAssistantMessage() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n := len(a.messages); n > 0 && a.messages[n-1].Role == provider.RoleAssistant {
		a.messages = a.messages[:n-1]
		a.rev++
	}
}

// oneTurn calls the LLM once, forwards events, and returns the stop
// reason, the assembled assistant message (already appended to the
// in-memory transcript when kept), and a commit closure. The commit
// closure persists the assistant message (OnMessageAppended) and emits
// its visible events; it is nil when no message was kept. The caller
// must invoke commit only once the turn is final — never before a
// retry — so an abandoned partial attempt is not persisted durably.
func (a *Agent) oneTurn(ctx context.Context, system string, tools Registry, tt turnTools, sink func(AgentEvent)) (provider.StopReason, provider.Message, func(), error) {
	// system and tools are PINNED by runLoop for the whole user turn (see
	// the snapshot there) so a mid-turn host swap can't evict the prompt
	// cache between steps. The remaining request fields are read per step
	// under the lock: hosts assign Model/Reasoning/MaxTokens at runtime
	// (model swap) on another goroutine, and reading them piecemeal here
	// would race those writes. Take a consistent picture of those plus a
	// copy of the transcript while we hold the lock.
	a.mu.Lock()
	model := a.Model
	reasoning := a.Reasoning
	maxTokens := a.MaxTokens
	temperature := a.Temperature
	client := a.Client
	contextProvider := a.ContextProvider
	cacheKey := a.cacheID
	msgs := make([]provider.Message, len(a.messages))
	copy(msgs, a.messages)
	a.mu.Unlock()

	// Pull host ephemeral context (e.g. an extension's live task card)
	// outside the lock; it rides the request only, never the transcript.
	var ephemeral string
	if contextProvider != nil {
		ephemeral = contextProvider()
	}

	// Lazy tool visibility (retro H2·b): surface the groups hidden this turn so
	// the model can discover and activate_tools them. Rides the cache-free
	// ephemeral tail (pinned with the visibility that produced it, so the note
	// and the advertised specs never disagree) rather than the cached system
	// prefix, so bringing a group in is a tools-array cache write only — the
	// note itself never costs cache.
	if tt.capabilityNote != "" {
		if ephemeral != "" {
			ephemeral += "\n\n"
		}
		ephemeral += tt.capabilityNote
	}

	// Context-pressure note: past ContextWarnFraction the model is told
	// how full its window is instead of relying on it to poll
	// terva_status (models don't re-poll). Rides the cache-free
	// ephemeral tail so it refreshes every step and never lands in the
	// transcript.
	if used, window := a.ContextUsage(); window > 0 && used > 0 {
		if f := float64(used) / float64(window); f >= ContextWarnFraction {
			// The closing sentence must match the actual compaction policy:
			// with auto_compact "off" there is no 85% valve — telling the
			// model one exists invites it to defer summarization to a
			// harness intervention that will never come.
			var note string
			if a.autoCompactMode() == AutoCompactOff {
				note = i18n.P("context.pressure.no_autocompact",
					"[context pressure] Your context window is %d%% full (%s of %s tokens). Be economical: prefer targeted reads over whole-file dumps, and summarize or persist important findings now. Automatic compaction is disabled for this session: past the limit, requests fail until the transcript is compacted — wrap up, or suggest the user run /compact.",
					int(f*100), fmtTokenCount(used), fmtTokenCount(window))
			} else {
				note = i18n.P("context.pressure",
					"[context pressure] Your context window is %d%% full (%s of %s tokens). Be economical: prefer targeted reads over whole-file dumps, and summarize or persist important findings now. Past %d%% the transcript is auto-compacted.",
					int(f*100), fmtTokenCount(used), fmtTokenCount(window), int(AutoCompactThreshold*100))
			}
			// Delegation guidance deliberately does NOT ride this note: by
			// 70% it's too late to restructure the work. The context-shield
			// nudge lives in the always-on swarm system addendum instead
			// (AutoSwarmSystemAddendum), where it shapes the plan from
			// turn one.
			if ephemeral != "" {
				ephemeral += "\n\n"
			}
			ephemeral += note
		}
	}

	// Stuck-loop nudge: when the detector tripped on the previous step, ride its
	// one-turn note on the ephemeral tail. Peeked (not consumed) here because
	// oneTurn is re-entered per retry attempt — the note must survive a failed
	// attempt and clear only once a request actually reaches the provider
	// (clearNudge, after recordDispatch below).
	if a.stallDetectionOn() {
		if note := a.stall.nudge(); note != "" {
			if ephemeral != "" {
				ephemeral += "\n\n"
			}
			ephemeral += note
		}
	}

	req := provider.Request{
		Model:  model,
		System: system,
		// Repair any dangling tool_use blocks before sending. A turn
		// aborted mid-flight (cancel, connection drop, ECONNREFUSED to a
		// dev server, etc.) can leave an assistant tool_use with no
		// matching tool_result in the live transcript. The load-time
		// repair in OpenSession only runs on restart, so without this the
		// next in-process request is rejected by providers like Anthropic
		// with "tool_use ids were found without tool_result blocks". The
		// repair is pure and a no-op on already-valid transcripts.
		Messages: repairToolUseResultPairs(msgs),
		// SpecsVisible advertises only the tools the pinned visibility predicate
		// admits (nil = all, today's behavior). Dispatch and the permission gate
		// still resolve the full `tools` registry (runOneTool below), so hiding a
		// tool here never affects callability or authority (retro H2·b).
		Tools:            tools.SpecsVisible(tt.visible),
		Reasoning:        reasoning,
		MaxTokens:        maxTokens,
		Temperature:      temperature,
		EphemeralContext: ephemeral,
		// The session id doubles as the provider cache-routing key so
		// concurrent conversations on one account (coordinator + swarm
		// children) stop evicting each other's cached prefixes. Empty
		// (live-only agents) sends nothing — today's behavior.
		PromptCacheKey: cacheKey,
	}
	stream, err := client.Stream(ctx, req)
	if err != nil {
		// Nothing reached the provider, so nothing was cached — leave the
		// retained prefix pointing at the last request that actually landed.
		return provider.StopError, provider.Message{}, nil, err
	}
	// This prefix is now warm at the provider. Retain it: a host swap (an
	// extension reload, a /model switch) can overwrite the agent's copy at any
	// moment, and this then becomes the only record of what is actually cached.
	a.recordDispatch(client, req)

	// The request landed, so any stuck-loop nudge it carried has been delivered:
	// drop it so it rides exactly one dispatch, not every subsequent step.
	a.stall.clearNudge()

	sink(EvAssistantStart{})

	var (
		stop     provider.StopReason
		finalErr error
		finalMsg provider.Message
	)

	for ev := range stream {
		switch e := ev.(type) {
		case provider.EventStart:
			// nothing
		case provider.EventTextDelta:
			sink(EvTextDelta{Delta: e.Delta})
		case provider.EventToolStart:
			sink(EvToolUseStart{ID: e.ID, Name: e.Name})
		case provider.EventToolArgs:
			sink(EvToolUseArgs{ID: e.ID, Delta: e.Delta})
		case provider.EventToolEnd:
			sink(EvToolUseEnd{ID: e.ID})
		case provider.EventUsage:
			cum := a.cost.Add(e.Usage)
			sink(EvUsage{Usage: e.Usage, Cumulative: cum})
			a.fireUsage(e.Usage, cum)
		case provider.EventDone:
			stop = e.Stop
			finalErr = e.Err
			finalMsg = e.Message
		}
	}

	// Append assistant message to transcript. Aborted turns (Esc / Ctrl+C)
	// produce partial content. When the partial message is text only we
	// keep whatever was streamed up to the cancel so the user does not
	// lose visible work (a cut-off summary is still useful). If the
	// partial message already contained tool-call blocks we drop the
	// whole thing, because an unmatched tool_use would fail the next
	// turn with a tool_result mismatch error.
	keep := len(finalMsg.Content) > 0
	if stop == provider.StopAborted && keep {
		hasToolCall := false
		for _, c := range finalMsg.Content {
			if _, ok := c.(provider.ToolCallBlock); ok {
				hasToolCall = true
				break
			}
		}
		if hasToolCall {
			keep = false
		}
	}
	if keep {
		emit := finalMsg
		suppress := false

		// BeforeAssistantMessage hook: extensions can suppress or
		// rewrite the visible text. The transcript keeps the
		// model's original output so the model still sees what it
		// said on subsequent turns.
		if a.BeforeAssistantMessage != nil {
			orig := extractText(finalMsg)
			if orig != "" {
				allowed, _, replacement := a.BeforeAssistantMessage(orig)
				if !allowed {
					suppress = true
				} else if replacement != "" && replacement != orig {
					emit = replaceText(finalMsg, replacement)
				}
			}
		}

		// Append to the in-memory transcript now so a same-process
		// retry can drop it via dropLastAssistantMessage and so the
		// next request's tool_use repair sees consistent state. The
		// durable persistence (OnMessageAppended) and the visible
		// events are deferred to the returned commit closure: when the
		// turn ends in a retryable error, runLoop drops the partial and
		// never calls commit, so the JSONL is never tainted with the
		// abandoned attempt. On a final turn runLoop calls commit once.
		a.mu.Lock()
		a.messages = append(a.messages, finalMsg)
		a.rev++
		a.mu.Unlock()

		commit := func() {
			a.fireMessageAppended(finalMsg)
			if !suppress {
				sink(EvAssistantMessage{Message: emit})
			}
			// Surface tool calls as EvToolCall events so UIs can render
			// them in order before the tool results arrive.
			for _, c := range finalMsg.Content {
				if tc, ok := c.(provider.ToolCallBlock); ok {
					sink(EvToolCall{ID: tc.ID, Name: tc.Name, Args: tc.Arguments})
				}
			}
		}
		return stop, finalMsg, commit, finalErr
	}

	return stop, finalMsg, nil, finalErr
}

// executeTools runs every tool call in the assistant message and returns
// a single tool-role message carrying all results.
func (a *Agent) executeTools(ctx context.Context, msg provider.Message, tools Registry, sink func(AgentEvent)) (provider.Message, bool) {
	var results []provider.Content
	hadError := false

	for _, c := range msg.Content {
		tc, ok := c.(provider.ToolCallBlock)
		if !ok {
			continue
		}
		res := a.runOneTool(ctx, tc, tools, sink)
		if res.IsError {
			hadError = true
		}
		results = append(results, provider.ToolResultBlock{
			CallID:  tc.ID,
			Content: res.Content,
			IsError: res.IsError,
		})
		sink(EvToolResult{ID: tc.ID, Result: res})
	}

	return provider.Message{
		Role:    provider.RoleTool,
		Content: results,
		Time:    time.Now(),
	}, hadError
}

// agentCtxKey carries the executing *Agent through the context passed
// to tool Execute calls. Several live agents can share one tool
// registry (bot mode mints an agent per chat; the map stays shared so
// live extension re-registration reaches every agent), so agent-aware
// tools (terva_status, read's re-read dedup) must identify the CALLING
// agent per dispatch — a field bound at construction time is clobbered
// by the next agent built from the same registry.
type agentCtxKey struct{}

// ContextWithAgent returns ctx tagged with the executing agent. The
// agent loop applies it on every tool dispatch; exported for tests and
// custom dispatchers that invoke tools directly.
func ContextWithAgent(ctx context.Context, a *Agent) context.Context {
	return context.WithValue(ctx, agentCtxKey{}, a)
}

// AgentFromContext returns the agent executing the current tool call,
// or nil when ctx did not come from an agent dispatch.
func AgentFromContext(ctx context.Context) *Agent {
	a, _ := ctx.Value(agentCtxKey{}).(*Agent)
	return a
}

func (a *Agent) runOneTool(ctx context.Context, tc provider.ToolCallBlock, tools Registry, sink func(AgentEvent)) ToolResult {
	ctx = ContextWithAgent(ctx, a)
	// Dispatch against the registry PINNED for this turn (passed down from
	// runLoop), not a live read of a.Tools: the turn runs on its own
	// goroutine while the host may swap the registry from another (model
	// swap, /reload-ext, an extension's set_withdrawn_tools). Using the
	// pinned set both avoids that data race and keeps dispatch consistent
	// with the tool specs the model was actually offered this turn. An
	// absent tool yields the same "unknown tool" result the model already
	// knows how to handle.
	tool, ok := tools[tc.Name]
	if !ok {
		return ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf("unknown tool %q", tc.Name)}},
			IsError: true,
		}
	}

	args := tc.Arguments

	// Intercept hook: an extension or other guard can refuse the
	// call before any side effect happens, OR rewrite the args
	// seen by the tool. The model sees the reason as the tool
	// error, learns from it, and (typically) proposes a different
	// action; rewrites are invisible to the model (they apply only
	// to the execution).
	if a.BeforeToolExecute != nil {
		allowed, reason, modified := a.BeforeToolExecute(tc)
		if !allowed {
			if reason == "" {
				reason = "tool call refused by extension guard"
			}
			return ToolResult{
				Content: []provider.Content{provider.TextBlock{Text: reason}},
				IsError: true,
			}
		}
		if len(modified) > 0 && json.Valid(modified) {
			args = modified
		}
	}

	if len(args) == 0 {
		args = json.RawMessage("{}")
	}

	// Recover panics so a buggy tool does not crash the agent.
	var res ToolResult
	func() {
		defer func() {
			if r := recover(); r != nil {
				res = ToolResult{
					Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf("panic: %v", r)}},
					IsError: true,
				}
			}
		}()
		out, err := tool.Execute(ctx, args, func(text string) {
			sink(EvToolProgress{ID: tc.ID, Text: text})
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				res = ToolResult{
					Content: []provider.Content{provider.TextBlock{Text: "aborted: " + err.Error()}},
					IsError: true,
				}
				return
			}
			res = ToolResult{
				Content: []provider.Content{provider.TextBlock{Text: err.Error()}},
				IsError: true,
			}
			return
		}
		res = out
	}()
	return res
}

// extractText concatenates all TextBlock content in a message. Used
// by BeforeAssistantMessage so guards see a single string instead of
// having to walk provider.Content themselves.
func mirrorToolImagesAsUser(msg provider.Message) provider.Message {
	var content []provider.Content
	hasImage := false
	for _, c := range msg.Content {
		tr, ok := c.(provider.ToolResultBlock)
		if !ok {
			continue
		}
		for _, inner := range tr.Content {
			switch v := inner.(type) {
			case provider.TextBlock:
				// Keep short textual context so the model understands why
				// the images appeared, but don't duplicate giant read
				// outputs verbatim.
				if len(v.Text) > 0 && len(v.Text) <= 500 {
					content = append(content, v)
				}
			case provider.ImageBlock:
				content = append(content, v)
				hasImage = true
			}
		}
	}
	// Only synthesize a mirror when the tool result actually carried an
	// image. The short text blocks above are context *for* the images;
	// without an image they are not "image content", and mirroring a
	// text-only result would feed the model a message wrongly prefixed
	// "Tool output included the following image content:" — visible on
	// codex, which round-trips that prefix back into the model's view.
	if !hasImage {
		return provider.Message{}
	}
	prefix := provider.TextBlock{Text: ToolImageMirrorPrefix}
	content = append([]provider.Content{prefix}, content...)
	// Mark the synthetic message structurally so consumers identify it
	// without string-matching the prefix (see IsToolImageMirror). It is
	// a provider-wire artifact — required in the model-facing history on
	// mirroring providers, but display/summarization should skip it.
	return provider.Message{
		Role:    provider.RoleUser,
		Content: content,
		Time:    time.Now(),
		Meta:    map[string]string{toolImageMirrorMeta: "true"},
	}
}

// ToolImageMirrorPrefix is the leading text block of a tool-image
// mirror message (see mirrorToolImagesAsUser). Exported as the single
// source of truth for the legacy-session fallback in IsToolImageMirror;
// new mirrors are identified by meta, not this string.
const ToolImageMirrorPrefix = "Tool output included the following image content:"

const toolImageMirrorMeta = "tool_image_mirror"

// IsToolImageMirror reports whether msg is a synthetic tool-image
// mirror (a provider-wire artifact, not something the user wrote).
// Checks the structural meta marker first; falls back to the prefix
// string so mirrors persisted before the marker existed are still
// recognized on resume.
func IsToolImageMirror(msg provider.Message) bool {
	if msg.Meta[toolImageMirrorMeta] == "true" {
		return true
	}
	if msg.Role != provider.RoleUser || len(msg.Content) == 0 {
		return false
	}
	tb, ok := msg.Content[0].(provider.TextBlock)
	return ok && strings.TrimSpace(tb.Text) == ToolImageMirrorPrefix
}

func extractText(msg provider.Message) string {
	var out string
	for _, c := range msg.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			if out != "" {
				out += "\n"
			}
			out += tb.Text
		}
	}
	return out
}

// replaceText returns a copy of msg with every TextBlock replaced by
// a single TextBlock containing replacement. Non-text content (tool
// calls, etc.) is preserved in order.
func replaceText(msg provider.Message, replacement string) provider.Message {
	out := provider.Message{Role: msg.Role}
	out.Content = make([]provider.Content, 0, len(msg.Content))
	replaced := false
	for _, c := range msg.Content {
		if _, ok := c.(provider.TextBlock); ok {
			if !replaced {
				out.Content = append(out.Content, provider.TextBlock{Text: replacement})
				replaced = true
			}
			continue
		}
		out.Content = append(out.Content, c)
	}
	if !replaced {
		out.Content = append(out.Content, provider.TextBlock{Text: replacement})
	}
	return out
}
