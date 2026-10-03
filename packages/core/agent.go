package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"terva.sh/terva/packages/core/i18n"
	"terva.sh/terva/packages/core/transcriptcodec"
	"terva.sh/terva/packages/provider"
)

// ErrBusy is returned by Prompt, Continue, and Compact when the agent
// is already running a turn (or compacting). The agent is single-
// flight: only one of these may be in progress at a time, because they
// all mutate the shared transcript and a second concurrent run would
// interleave appends or let Compact wholesale-replace a.messages mid-
// append, corrupting the transcript.
var ErrBusy = errors.New("agent is busy")

// ErrUserInterrupted is the cancellation CAUSE a host sets when a person
// deliberately stopped the turn — Esc, /stop, a client's cancel verb — as
// opposed to the turn dying with the process. Hosts pass it to
// context.CancelCauseFunc; core reads it with context.Cause.
//
// It exists because a cancel is not one thing, and the difference decides
// whether a prompt is withdrawn (docs/proposals/withdraw-cancelled-prompt.md).
// A person who stops their own turn before the model answered is saying they did
// not mean to send it. A restart drain, a deadline, or a dying parent context is
// saying nothing at all on the user's behalf, and silently deleting what they
// typed would be terva discarding input nobody asked it to discard — worst of
// all on a provider failure, which is exactly when they want it back.
//
// Requiring the cause makes that an opt-in a host states rather than a default
// core infers: a host that never sets it never withdraws, which is right for
// --print, rpc and a swarm child, none of which have a composer to return
// anything to.
var ErrUserInterrupted = errors.New("turn interrupted by the user")

// Agent is a stateful conversation bound to a provider client, a model,
// and a set of tools.
//
// Its settings are private. A host passes them to New as options, changes
// them at runtime through the setters, and reads them through the getters.
// The setters and getters take the agent's lock. An exported field could be
// read or written without it, which races a turn on another goroutine.
type Agent struct {
	client provider.Client
	model  string
	// providerID is the catalog provider the model resolves under ("cpa",
	// "anthropic"), set by WithProvider and SetClientAndModel. Empty means the
	// host never said, and lookups fall back to the first entry with the id.
	// See lookupModel.
	providerID string
	tools      Registry
	maxSteps   int
	reasoning  string
	// reasoningSet reports the global reasoning level was explicitly chosen by
	// the user (flag/config/settings, including "off"), so it wins over a
	// model's DefaultReasoning. False means unset — fall back to the per-model
	// default. Read at turn start under a.mu alongside reasoning.
	reasoningSet bool

	// reasoningSummary asks the provider for a human-readable summary of its
	// reasoning, persisted into the transcript alongside the opaque payload so
	// an unattended run can be reviewed for intent. "" (the default) is off and
	// leaves requests unchanged. Read at turn start under a.mu alongside
	// reasoning; only the codex client acts on it.
	reasoningSummary string

	// showReasoning asks for the same summary in order to DISPLAY it while the
	// turn runs (EvReasoningDelta), without writing it to the session record.
	//
	// It is separate from reasoningSummary because the two costs are different
	// and only one of them is permanent. Watching a model work is ephemeral —
	// the text is on screen and then it is gone. Persisting it makes the
	// session file quotable, which is why reasoningSummary is opt-in, and
	// forcing that trade on anyone who merely wants to see the work would be
	// the wrong bargain.
	//
	// 🔑 Most providers will not send a summary unless ASKED, so display turns
	// the request flag on by itself (see reasoningSummaryRequest). What keeps it
	// off disk is stripUnrecordedSummaries, which blanks the text after the turn
	// and leaves the block itself alone. Read at turn start under a.mu alongside
	// reasoning.
	//
	// 🪤 "Unless asked" is not universal, and reading it as universal is what
	// once leaked. Anthropic thinking, Gemini thought summaries and chat
	// `reasoning_content` all arrive unbidden, so the strip must key on the
	// RECORD setting alone and never on this one.
	showReasoning bool

	// temperature sets the sampling temperature on each request. Nil
	// leaves it unset so each provider applies its own default (terva's
	// per-provider serialization guards still apply when it is set).
	temperature *float32

	// maxTokens caps the model's output tokens per turn. Zero leaves
	// the field unset on the provider request, letting each provider
	// apply its own default (which can be conservative, e.g. Bedrock
	// defaults to 4096, truncating long writes/edits). Hosts populate
	// this from the resolved model's MaxOutput so large single-turn
	// responses aren't silently cut off with stopReason=length.
	maxTokens int

	// imageOutput, when non-nil, requests native (in-protocol) image output:
	// the model may draw images inline in its own turn via the provider's
	// built-in image tool (OpenAI Responses image_generation). Set from the
	// opt-in native_output config. oneTurn only forwards it when the live model
	// advertises provider.CapImageOutput, so it tracks a model swap without a
	// rebuild. nil (the default) leaves native image output off.
	imageOutput *provider.ImageOutputConfig

	// gate is asked before each tool runs. New sets it and nothing
	// replaces it: it was an assignable field (BeforeToolExecute), and a host
	// that forgot to assign it got an agent that ran every call unchecked with
	// no signal (decisions 0004, 0005, 0021 rule 3). Nil only on an Agent built
	// without New, and then every tool call is refused.
	//
	// The turn and message hooks below are set from a TurnFilter or
	// MessageFilter component at construction. They are extension intercepts,
	// not permission checks, and leaving one unset runs the documented default
	// rather than an unchecked tool. docs/architecture/02-core-agent.md gives
	// the full reasoning.
	gate Gate

	// beforeTurn, if set, is called before each turn's model call.
	// Returning (allowed=false, reason) aborts the turn; reason is
	// surfaced as an assistant-like status line. Used for rate-
	// limiting, business-hour gates, and deny-by-default setups.
	beforeTurn func(step int) (allowed bool, reason string)

	// beforeAssistantMessage, if set, is called after the model's
	// final assistant message is assembled but before it's appended
	// to the transcript. Returning (allowed=false) suppresses both
	// the transcript append and the UI event. A non-empty
	// replacement rewrites the visible text for the user while
	// leaving the model's original text in the transcript (so the
	// model can still see what it said in subsequent turns).
	beforeAssistantMessage func(text string) (allowed bool, reason, replacement string)

	// beforeUserMessage, if set, is consulted just before a genuine
	// user message — the initial prompt or one drained from the queue —
	// is appended to the transcript and sent to the model. Returning
	// (allowed=false, reason) rejects the prompt: it is neither recorded
	// nor sent, and the host surfaces reason via EvUserMessageRejected.
	// A non-empty replacement rewrites the prompt the model actually
	// sees (the rewrite IS what lands in the transcript — unlike
	// beforeAssistantMessage, where the original is kept). The synthetic
	// at-close gate nudge is never gated. Mirrors beforeAssistantMessage
	// and backs the extension user_message intercept.
	beforeUserMessage func(text string) (allowed bool, reason, replacement string)

	// maxRetries controls agent-level retries for transient provider
	// failures that arrive after the HTTP stream opens (for example
	// Anthropic overloaded_error). Zero disables this retry layer.
	// retryBaseDelay is doubled for each attempt; zero uses 2s.
	maxRetries     int
	retryBaseDelay time.Duration

	// Hook observers. Registered through AddEventObserver / AddMessageObserver /
	// addUsageObserver / addTranscriptCompactedObserver /
	// addImageExcludedObserver / AddContinuationGate, never assigned — see observers.go for why the
	// assignable fields these replaced were a hazard.
	obsMu                  sync.RWMutex
	eventObs               []func(AgentEvent)
	messageObs             []func(provider.Message)
	usageObs               []func(u, cumulative provider.Usage)
	delegatedUsageObs      []func(u, cumulative provider.Usage)
	sideChannelUsageObs    []func(source string, u, cumulative provider.Usage)
	transcriptCompactedObs []func(messages []provider.Message, res CompactResult)
	imageExcludedObs       []func(sha256Hex string)
	queueDrainedObs        []func(drained []string)
	retryObs               []func(RetryRecord)
	tailObs                []func(TailRecord)
	dispatchObs            []DispatchObserver
	// diagWriters hand a component's diagnostic row to each attached store
	// that keeps diagnostics (AppendDiagnostic). Guarded by obsMu.
	diagWriters []func(func(TranscriptDiagnostics) error)
	// recordWriters hand a component's required record to each attached
	// store (AppendRecord). Guarded by obsMu.
	recordWriters []func(func(TranscriptStore) error)
	// toolRefresh is the host's re-resolve callback, set by SetToolRefresher and
	// fired by RequestToolRefresh. It lives on the AGENT and not on a tool
	// instance, because a rebuild mints fresh tools: a field on the tool would be
	// nil for the rest of the session after the first rebuild, which is the bug
	// workspace_toolchannels.go was written about. One host owns the rebuild, so
	// this is a single callback and not an observer list. Guarded by obsMu.
	toolRefresh       func(reason string)
	continuationGates []ContinuationGate
	stepGates         []StepGate

	// assembler produces the Frame for every request: the Stable segments that
	// form the system prompt and the Volatile ones that ride the ephemeral tail.
	// Set once by New and never reassigned, so it is read without a.mu. A
	// host that changes its frame live does so inside its assembler. Nil is an
	// empty frame.
	assembler ContextAssembler

	// readOnly names side-effect-free tools for dispatch and compaction. It is
	// published only with the registry it classifies, by SetToolsWithReadOnly,
	// under a.mu; readers use ToolsWithReadOnlySnapshot or the calling turn's
	// context. It is not an exported field because the pair must not drift: a
	// classification set on its own would describe a registry it was never
	// published with (TKT-01M35WK1J).
	//
	// Nil means every tool is assumed to mutate, and that is the correct failure
	// direction: a nil set over-reports the ledger, which costs a few tokens. The
	// opposite — assuming an unknown tool was read-only — would silently omit a
	// side effect from the record and invite the resuming agent to run it twice.
	// Extensions and MCP servers register arbitrary tools, so "unknown" is the
	// common case, not the edge one.
	readOnly *ReadOnlySet

	// asker, if set, is the front end's question channel — the same seam the
	// ask_user_question tool uses, wired onto the agent so the LOOP can ask too.
	// The engine's one caller is the prefix-change guard, which offers a
	// compaction before a cache-invalidating change lands
	// (offerCompactOnPrefixChange). The stuck-loop detector (core/stall) reads
	// it too, to ask before an escalation.
	//
	// Nil is the normal state for a host with nobody to ask: one-shot runs, the
	// chat bridge, swarm children. Those skip the offer rather than blocking on a
	// question no one will answer — and rather than silently compacting on their
	// behalf, which is not what a guard is for. Assigned at build, before the
	// agent runs a turn.
	asker Asker

	// compactionPolicy decides automatic compaction: whether at each point,
	// with what keep-tail, and with which strategies (compaction_policy.go).
	// Nil is DefaultCompactionPolicy{}. Set it during construction.
	compactionPolicy CompactionPolicy

	// running is the single-flight guard. It is set on entry to
	// Prompt/Continue/Compact and cleared on exit; a second concurrent
	// call sees it set and returns ErrBusy instead of interleaving its
	// transcript mutations with the in-flight run. It is an atomic so
	// the check-and-set needs no separate lock and never blocks.
	running atomic.Bool

	// repinRequested asks the run loop to take a fresh pin after the tool batch
	// that is running. RequestRepin sets it, and the loop clears it when it
	// re-pins. See RequestRepin.
	repinRequested atomic.Bool

	// catalog is the model catalog (SetCatalog); nil reads the default.
	catalog atomic.Pointer[catalogBox]

	mu sync.Mutex

	// visibility chooses the tools a request advertises. A ToolVisibility
	// component sets it, and nil advertises the whole registry. Guarded by mu.
	visibility ToolVisibility
	// tailFP is the fingerprint (block IDs, never their text) of the ephemeral
	// tail last recorded, so a tail row is written when the composition CHANGES
	// and not once per request. Guarded by mu; see recordTail.
	tailFP string

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
	cost            costTracker

	// continuePrefill is set for the duration of one ContinueAssistant turn
	// (guarded by mu, cleared on return). It makes oneTurn (a) suppress the
	// ephemeral tail so the trailing assistant message is the LAST message in the
	// request — required for a provider assistant-prefill continuation — and (b)
	// MERGE the streamed continuation onto that trailing assistant message in
	// place instead of appending a new message and persisting it. The merged
	// message + its index are stashed in continueResult for the caller (the
	// workspace) to persist as an AmendReplace.
	continuePrefill bool
	continueResult  *continuedMessage

	// stageCue is set for the duration of ONE turn (guarded by mu, cleared on
	// return): a request-scoped instruction appended to the ephemeral tail, after
	// the cache breakpoint, so it steers a single generation and costs no cache.
	// Non-empty means "append this"; the empty string is the ordinary turn.
	//
	// Two callers set it, for two different reasons. Advance needs it as the exact
	// INVERSE of continuePrefill: it forces a NON-EMPTY ephemeral tail, so the
	// request always ends in a user block even when the transcript ends in
	// assistant messages. That matters because Stage's directed authorship appends
	// authored lines as assistant messages, so a scene can end with several of
	// them. Dispatching a plain turn there sends a request whose last message is an
	// assistant one — which on Anthropic is a PREFILL: the model would extend that
	// line mid-sentence and the result would be appended as a NEW message
	// (continuePrefill is false, so the in-place merge never fires), producing a
	// bubble that starts mid-thought. Silent corruption, not an error.
	//
	// ContinueWithCue sets it for a guided regenerate, where the transcript ends in
	// a user message already and the cue is purely steering — the user's "what
	// should be different" note, and optionally the withdrawn take it replaces.
	// The cue text belongs to the CALLER (the workspace composes Stage's), so this
	// stays a dumb request-scoped string rather than a menu of flags.
	stageCue string

	// translator is the one WithTranslator gave, or nil for the process-wide
	// one. It is set once, by New, and never written again, so it needs no
	// lock.
	translator i18n.Translator

	// sessionID / sessionPath identify the transcript file this
	// conversation persists to: sessionID is the file basename without
	// .jsonl (the id --resume accepts), sessionPath the absolute path.
	// The front end that owns the session records them via
	// AdoptSessionIdentity when it opens or swaps the session; empty
	// means live-only (no persistence), e.g. --no-session or bot-mode
	// group chats. Guarded by mu: a /sessions swap can land while a
	// turn's terva_status call reads them.
	sessionID      string
	persistenceErr error // guarded by mu; first durable observer failure
	sessionPath    string
	// cacheID keys provider prompt caching (Request.PromptCacheKey). It
	// prefers the session's meta UUID over the file basename: basenames
	// are only unique within one directory, and every swarm child's
	// transcript is literally named session.json — concurrent children
	// keyed by basename share one cache route and evict each other.
	// Falls back to the basename for legacy files with no meta id.
	//
	// NEVER empty. A live-only agent (--no-session, bot-mode group chats)
	// has no session id to key on, so it carries a synthetic one — see
	// newLiveCacheID. The two properties that matter are STABLE for the
	// conversation's life and UNIQUE across concurrent ones; reproducible
	// is not among them, which is why this is a random UUID and not a
	// digest of anything.
	cacheID string

	// lastSent is the prompt prefix of the most recent request actually
	// dispatched (recordDispatch, from oneTurn) — nil until the first one goes
	// out. It is the only surviving copy of what the provider has cached once a
	// host swaps System/Tools/Model out from under it, which is what makes
	// compacting on the outgoing model possible. See promptPrefix.
	lastSent *promptPrefix

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

// Translator returns the translator the agent renders its text through, or
// nil when it uses the process-wide one. A component renders its own text
// through i18n.In(a.Translator()).T(...), so it speaks the agent's language.
//
// Unstable: it returns an i18n.Translator, and packages/core/i18n carries no
// promise before 1.0.
func (a *Agent) Translator() i18n.Translator { return a.translator }

// newAgent builds the agent New configures. The gate is already checked.
func newAgent(client provider.Client, model string, assembler ContextAssembler, tools Registry, gate Gate) *Agent {
	a := &Agent{
		client:    client,
		model:     model,
		assembler: assembler,
		tools:     tools,
		gate:      gate,
		maxSteps:  0, // 0 = unlimited
		// Six retries with the doubling base below is 2+4+8+16+32+60 = ~2min of
		// patience, ending on the maxRetryDelay tail. Three (14s) was too short
		// for the provider overloads it mostly meets — see maxRetryDelay.
		maxRetries:      6,
		retryBaseDelay:  2 * time.Second,
		transcriptEpoch: agentEpochSeq.Add(1) << 32,
		// Keyed from birth: New runs before the session is known (see
		// build.BindSession), so an agent can dispatch while still live-only,
		// and an unkeyed request forfeits the reliable prefix matching
		// GPT-5.6+ only offers when prompt_cache_key is set.
		cacheID: newLiveCacheID(),
	}
	return a
}

// Gate returns the gate New was given. There is no setter: a gate that
// could be replaced after construction could be replaced with nothing.
func (a *Agent) Gate() Gate { return a.gate }

// newLiveCacheID mints the cache-routing key for a conversation with no
// session to borrow one from.
//
// Random, deliberately. The key is a routing bucket, so it needs to be stable
// across a conversation and distinct between concurrent ones — and nothing
// more. A deterministic key (a constant, or a digest of cwd/model/opening
// message) would buy reattachment across restarts, which a live-only agent
// cannot use because it persists nothing, and would pay for it in collisions:
// see TestPromptCacheKeyPrefersMetaUUID, where sharing one key across
// concurrent agents produced alternating ~10%/~99% hit rates. Bot-mode group
// chats are the main live-only caller and would collide hardest, being many
// concurrent conversations off one system prompt. Keeping it content-free
// also keeps user text out of a string sent to the provider.
func newLiveCacheID() string { return "live-" + uuid.NewString() }

// TranscriptIdentity names the transcript an agent's conversation persists to.
// The zero value is a live-only conversation.
type TranscriptIdentity struct {
	// ID is the short name a host resumes by (terva's is the file's basename).
	ID string
	// Path locates the transcript for tools that report or open it.
	Path string
	// CacheKey routes prompt caching. Empty falls back to ID. It should be
	// globally unique where ID is not: every swarm child persists to a file
	// named session.json, so terva passes the session's meta UUID.
	CacheKey string
}

// AdoptSessionIdentity records which transcript this agent's conversation
// persists to; terva_status surfaces it to the model. Call it when binding or
// swapping the agent's store. The zero TranscriptIdentity clears the identity:
// the conversation is live-only from here.
//
// Clearing does NOT clear the cache route, it re-mints it. Sending no
// prompt_cache_key gives up the reliable matching GPT-5.6+ gates behind it,
// and a fresh key is right rather than merely harmless: unbinding means this
// is a different conversation from here on, so it should route as one.
func (a *Agent) AdoptSessionIdentity(id TranscriptIdentity) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id == (TranscriptIdentity{}) {
		a.sessionID, a.sessionPath = "", ""
		a.cacheID = newLiveCacheID()
		return
	}
	a.sessionPath, a.sessionID = id.Path, id.ID
	if a.cacheID = id.CacheKey; a.cacheID == "" {
		a.cacheID = id.ID
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
		if !synthetic && a.beforeUserMessage != nil && text != "" {
			allowed, reason, replacement := a.beforeUserMessage(text)
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
	msgs, _ := a.MessagesWithEpoch()
	return msgs
}

// MessagesWithEpoch snapshots the transcript and its revision identity together.
// A host must use this pair when it exposes message indices for later edits.
func (a *Agent) MessagesWithEpoch() ([]provider.Message, uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]provider.Message, len(a.messages))
	copy(out, a.messages)
	return out, a.transcriptEpoch
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
// This method preserves classification. Use SetToolsWithReadOnly when a
// registry rebuild can change a tool's read-only declaration.
func (a *Agent) SetTools(reg Registry) (changed bool) {
	a.mu.Lock()
	changed = !registryEqual(a.tools, reg)
	a.tools = reg
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

// ToolsSnapshot returns the live tool registry under the lock SetTools writes
// with, so a reader on another goroutine (e.g. the web /context view, which can
// run while an extension/MCP toggle calls SetTools) never races the swap.
// SetTools always installs a fresh map rather than mutating in place, so the
// returned Registry is safe to range read-only.
func (a *Agent) ToolsSnapshot() Registry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tools
}

// Assembler returns the ContextAssembler the agent was built with, so a host
// that keeps state in its own assembler can find it again from the agent.
func (a *Agent) Assembler() ContextAssembler { return a.assembler }

// FramePreview is the frame the next request would carry, assembled with
// AssemblePeek: the same segments, none of the per-request side effects. A size
// or inspection view such as /context reads it. It calls the assembler outside
// the agent's lock, since an assembler may read the agent.
func (a *Agent) FramePreview() Frame { return a.assemble(AssemblePeek) }

// deliverTail tells an assembler that implements TailDeliveryObserver which
// blocks a request carried.
func (a *Agent) deliverTail(tail []TailBlock) {
	d, ok := a.assembler.(TailDeliveryObserver)
	if !ok {
		return
	}
	ids := make([]string, len(tail))
	for i, b := range tail {
		ids[i] = b.ID
	}
	d.TailDelivered(ids)
}

// assemble asks the host's assembler for a frame. A nil assembler is an empty
// frame.
func (a *Agent) assemble(mode AssembleMode) Frame {
	if a.assembler == nil {
		return Frame{}
	}
	return a.assembler.Assemble(mode)
}

// LookupTool returns the tool registered under name in the live
// registry. Race-free against SetTools, so it is safe to call from a
// goroutine other than the turn loop (e.g. an extension's host_tool_call
// dispatch).
func (a *Agent) LookupTool(name string) (Tool, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.tools[name]
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

// ReplaceMessage swaps the message at idx for msg — an in-place edit backing the
// Stage surface's edit interaction. Identity is (epoch, index): an edit changes a
// message's content without moving it, but memoized renders and read-dedup
// fingerprints key on that identity, so it must still bump the transcript epoch.
// Out-of-range idx is a no-op returning false.
//
// It deliberately does NOT run tool-pair repair. Repair runs at send time and
// load time — after any batch of edits — and running it here would drop messages
// and shift indices out from under a following edit, breaking the invariant that
// a reloaded transcript equals the live one. Persisting the edit (an amend row
// via session.Session.AppendAmend) is the caller's responsibility.
func (a *Agent) ReplaceMessage(idx int, msg provider.Message) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if idx < 0 || idx >= len(a.messages) {
		return false
	}
	a.messages[idx] = msg
	a.rev++
	a.transcriptEpoch++
	return true
}

// DeleteMessage removes the message at idx; later messages shift down. Same
// epoch and no-repair reasoning as ReplaceMessage. Out-of-range idx is a no-op
// returning false. The caller persists it as a delete amend.
func (a *Agent) DeleteMessage(idx int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if idx < 0 || idx >= len(a.messages) {
		return false
	}
	a.messages = append(a.messages[:idx], a.messages[idx+1:]...)
	a.rev++
	a.transcriptEpoch++
	return true
}

// TruncateTo cuts the transcript to its first idx messages — the primitive behind
// regenerate (truncate to before the last turn, then prompt anew). idx == len is
// an accepted no-op that still returns true; a negative or too-large idx returns
// false. Same epoch and no-repair reasoning as ReplaceMessage. The caller
// persists it as a truncate amend.
func (a *Agent) TruncateTo(idx int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if idx < 0 || idx > len(a.messages) {
		return false
	}
	a.messages = a.messages[:idx]
	a.rev++
	a.transcriptEpoch++
	return true
}

// SetModel swaps the active model under the lock that oneTurn snapshots
// request fields with, so a host can change models on another goroutine
// without racing a starting turn. It only mutates the model id and keeps the
// provider — the caller is responsible for ensuring the current Client can
// serve the new model (same provider AND same resolved endpoint). When the model
// routes to a different base URL or needs a different client, rebuild
// the agent (or use SetClientAndModel) instead; mutating the id alone
// would keep firing requests at the previous endpoint.
func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
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
	if m, err := a.lookupModel(a.providerID, a.model); err == nil && m.MaxOutput > 0 {
		a.maxTokens = m.MaxOutput
	}
}

// SetReasoning swaps the reasoning/thinking level under the same lock oneTurn
// snapshots request fields with, so a host can change it on another goroutine
// without racing a starting turn (Reasoning is read at turn start). Empty
// disables thinking. The caller normalizes the level.
func (a *Agent) SetReasoning(level string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reasoning = level
	// A runtime set is an explicit user choice (the settings "reasoning"
	// control), so it wins over any per-model DefaultReasoning from here on —
	// including when level is "" (the user chose off).
	a.reasoningSet = true
}

// ClearReasoning drops an explicit level so the per-model DefaultReasoning
// applies again. It is the inverse of SetReasoning and exists because "" is not
// its own inverse: SetReasoning("") means the user chose OFF, and the two must
// stay distinguishable.
//
// A session clearing its per-session override needs this. Without it, "clear"
// could only mean "set to whatever the global happens to be right now", which
// would freeze the session at that value instead of letting it follow the
// global — and would override the model's own default forever after.
//
// This is the live twin of the build path, where the set-signal is the RAW
// level being non-empty (see build.Resolve): clearing here is the same state a
// rebuild reaches when args.Reasoning and cfg.Reasoning are both empty.
func (a *Agent) ClearReasoning() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reasoning = ""
	a.reasoningSet = false
}

// SetReasoningSummary switches reasoning-summary persistence live ("" = off).
// Read at turn start under the same lock, so the next turn picks it up with no
// rebuild — and, unlike Reasoning, there is no per-model default to override.
func (a *Agent) SetReasoningSummary(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reasoningSummary = mode
}

// SetShowReasoning switches live reasoning display on or off for the next turn.
func (a *Agent) SetShowReasoning(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.showReasoning = on
}

// SetAsker sets the channel the engine asks the user through, for a host that
// learns it only after construction, such as rpc once its client says it can
// answer questions. A host that knows it at construction passes WithAsker.
// Nil means nobody to ask.
func (a *Agent) SetAsker(ask Asker) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asker = ask
}

// reasoningSummaryRequest is the value that rides the provider request: the
// persistence setting when one is chosen, otherwise "auto" when the only reason
// to ask is live display.
//
// 🪤 persist WINS when both are set. The two settings do not conflict about
// whether to ask, only about how much detail, and honouring the persisted
// choice keeps the session record exactly what the operator asked for rather
// than what a display toggle happened to imply.
func reasoningSummaryRequest(persist string, show bool) string {
	if persist != "" {
		return persist
	}
	if show {
		return "auto"
	}
	return ""
}

// stripUnrecordedSummaries blanks ReasoningBlock summaries on a message about
// to be persisted, for every turn where recording is off.
//
// 🪤 It was once named for the display-only case and gated on ShowReasoning,
// because a Codex summary exists only when terva asks for one: with recording
// and display both off there was, for that provider, nothing on hand to strip.
// That premise is Codex's alone. Gemini thought summaries and chat
// `reasoning_content` (DeepSeek, Kimi) arrive whether or not anyone asked,
// exactly as Anthropic thinking does — and Anthropic was the only one the
// shape-keyed drop below caught. So the DISPLAY toggle silently decided a
// question the RECORD setting had already answered, and with both switched off
// that readable text reached the session file.
//
// 🪤 It clears the TEXT and keeps the BLOCK. A ReasoningBlock also carries the
// provider's opaque ID and Encrypted payload, which have to round-trip on the
// next request exactly like a gemini thought signature — dropping the block to
// hide the text would strand the reasoning item and break the following turn.
// The result is byte-identical to what a build with display switched off
// records today: a reasoning block with an empty summary.
// 🪤 Blanking is only meaningful where the text and the replay payload are
// separable. An Anthropic thinking block is sealed by a signature over its own
// text, so a blanked one is not a quieter block, it is an unreplayable one.
// Those are removed outright by dropUnrecordableThinking, which runs on every
// path this does — so this function never has to reason about them.
func stripUnrecordedSummaries(m provider.Message) provider.Message {
	keepReply := chatReasoningIsTheReply(m)
	out := make([]provider.Content, 0, len(m.Content))
	for _, c := range m.Content {
		if r, ok := c.(provider.ReasoningBlock); ok {
			if keepReply && r.Shape == provider.ReasoningShapeOpenAIChat {
				out = append(out, r)
				continue
			}
			r.Summary = ""
			out = append(out, r)
			continue
		}
		out = append(out, c)
	}
	m.Content = out
	return m
}

// chatReasoningIsTheReply reports whether chat-shaped reasoning is ALL this
// message has — in which case it is the answer, not deliberation, and blanking
// it would delete the turn rather than quiet it.
//
// 🪤 This is the one exemption to "recording off means no readable text", and
// it is not privacy losing to convenience. A chat backend whose thinking
// channel never closes classifies every token after the opener as reasoning:
// the whole REPLY arrives in reasoning_content with content empty. openai.go
// promotes that back to visible text on replay precisely because dropping it
// "left a hole in the history exactly where the answer had been" — the model
// then apologises for a lapse it has no record of. With the summary blanked
// there is nothing to promote, and the serializer skips a message carrying
// neither text nor tool calls, so the turn vanishes from the conversation.
//
// Scoped to the chat shape on purpose. A Codex or Gemini block keeps its own
// replay payload (ID / Encrypted) and loses nothing readable it needs, so
// neither earns the exemption.
func chatReasoningIsTheReply(m provider.Message) bool {
	chatReasoning := false
	for _, c := range m.Content {
		switch v := c.(type) {
		case provider.ReasoningBlock:
			if v.Shape == provider.ReasoningShapeOpenAIChat && v.Summary != "" {
				chatReasoning = true
			}
		case provider.TextBlock:
			if strings.TrimSpace(v.Text) != "" {
				return false
			}
		default:
			// A tool call, an image, anything else: the turn has substance of
			// its own and the reasoning beside it is deliberation.
			return false
		}
	}
	return chatReasoning
}

// dropUnrecordableThinking removes reasoning whose readable text cannot be
// separated from its replay payload, for turns where recording is off.
//
// Only Anthropic thinking qualifies. Its signature seals the text, so the block
// is all-or-nothing: keeping it means keeping the model's unabridged
// chain-of-thought in the session file, which is the exact trade "Record
// thinking" is default-off to avoid. Dropping it returns this provider to where
// it was before terva captured thinking at all.
//
// 🪤 The cost is real and worth stating: the block is what Anthropic wants
// replayed alongside a tool call, so a recording-off session hands none back —
// unchanged from every terva release to date, but no longer for lack of having
// it. Turning "Record thinking" on is what makes replay possible.
//
// 🔑 ThinkingOpaque is deliberately NOT dropped, and the shape test rather than
// a Summary == "" test is what makes that possible. Adaptive-thinking models
// sign reasoning whose text Anthropic withholds, so there is no
// chain-of-thought in the block to decline to record — keeping it costs the
// user nothing and preserves replay on the default setting. Redacted blocks
// survive for the same reason. Dropping either would be privacy theatre paid
// for in fidelity.
func dropUnrecordableThinking(m provider.Message) provider.Message {
	out := make([]provider.Content, 0, len(m.Content))
	for _, c := range m.Content {
		if r, ok := c.(provider.ReasoningBlock); ok && r.Shape == provider.ReasoningShapeAnthropicThinking {
			continue
		}
		out = append(out, c)
	}
	m.Content = out
	return m
}

// SetClientAndModel atomically swaps the provider client, the catalog
// provider the model resolves under, and the model, for hosts that re-resolve
// a fresh client (a different endpoint, a different provider, rotated
// credentials) while keeping the same transcript. All three move together
// under the lock so a turn can never observe the new client paired with the
// old model or vice versa.
//
// providerID is required in the signature because a swap is exactly where it
// goes stale: a swap from anthropic to an anthropic-compatible endpoint that
// lists the same model id would otherwise keep reading the anthropic entry's
// window, and auto-compaction would ignore the endpoint's desired window.
// Empty means unscoped, as WithProvider describes.
func (a *Agent) SetClientAndModel(client provider.Client, providerID, model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.client = client
	a.providerID = providerID
	a.model = model
	a.refreshMaxTokensLocked()
}

// Provider returns the catalog provider the model resolves under, as
// WithProvider or SetClientAndModel set it. Empty when no host said.
func (a *Agent) Provider() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.providerID
}

// Client returns the provider client the next turn sends with. It reads under
// the lock SetClientAndModel writes with, so a side request on another
// goroutine never races a swap. A host that shows subscription usage or
// redeems a reset passes it to provider.ClientUsage and its siblings, which
// see through wrapper layers.
func (a *Agent) Client() provider.Client {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.client
}

// Model returns the model id the next turn requests, under the lock SetModel
// and SetClientAndModel write with.
func (a *Agent) Model() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

// Reasoning returns the reasoning level and whether one was chosen. With set
// false the model's own default applies, whatever level says. With set true
// an empty level means the user chose off.
func (a *Agent) Reasoning() (level string, set bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reasoning, a.reasoningSet
}

// ReasoningSummary returns the reasoning-summary mode, "" for off.
func (a *Agent) ReasoningSummary() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reasoningSummary
}

// Asker returns the channel the engine asks the user through, or nil when
// there is nobody to ask. A component that asks on the engine's behalf, such
// as the stuck-loop detector, reads it here.
func (a *Agent) Asker() Asker {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.asker
}

// Cost returns the cumulative usage. The cost tracker carries its own
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

// RecentUsage returns the tail of per-response usage records, oldest first —
// the cache strip's data. Empty until this agent has run a request, including
// on a resumed session: the tracker keeps no durable record of the tail to
// rehydrate it from.
func (a *Agent) RecentUsage() []provider.Usage {
	return a.cost.RecentUsage()
}

// RecordSideChannelUsage books a request the agent did not itself run: the
// daemon's one-off completions (the World router's pick, the line it voices,
// suggest, side chat). They spend real money on the session's credentials, but
// they never pass through Run, so nothing folded their usage in and nothing
// wrote them a session row — a session's recorded cost was the cost of its
// TURNS, silently understating what it actually spent.
//
// Kept out of the per-turn snapshot, deliberately: this is the same treatment
// compaction's summarization request gets (cost.AddTotalOnly). The per-turn
// snapshot is the CONTEXT gauge, and a side-channel request's prompt is not
// this session's context — letting one overwrite the snapshot would leave
// every threshold check reading a size the transcript never had.
//
// source names the surface that spent it ("next_step", "side_chat", ...) and
// travels to the row on disk. It is what lets a reader tell an idle
// suggestion's request from a turn of the session: the two were byte-identical
// on disk, so the only way to find a side-channel call in a session file was
// the forensic shape it left, a usage row with no message after it. That is
// how TKT-01M213C1 was found, and it is not a way to measure what such a call
// costs. Firing the side-channel observers is what persists the row, so the
// session file gains one line per call, marked.
//
// There is no fallback to the plain usage observers. It existed for a host that
// registered a plain usage observer and no marked one; both are now registered
// together, by AttachTranscriptStore, so that host can no longer exist.
func (a *Agent) RecordSideChannelUsage(source string, u provider.Usage) {
	if u == (provider.Usage{}) {
		return
	}
	a.cost.AddSideChannel(u)
	cum := a.cost.CumulativeTotal()
	a.fireSideChannelUsage(source, u, cum)
}

// SideChannelCost returns the part of the cumulative total that the host's
// one-off completions spent on this session's credentials. Always <= Cost().
func (a *Agent) SideChannelCost() provider.Usage {
	return a.cost.SideChannelTotal()
}

// RecordDelegatedUsage books what a sub-agent spent on this session's behalf.
//
// The measured gap this closes: one workflow run spent $24.4936 while its
// launching session's record ended at $5.3602 — 16% of what the task actually
// cost. Nothing was missing from disk; every child writes its own cumulative,
// and the workflow runner already summed them for a line on stderr. The number
// was computed and then discarded at the process boundary.
//
// Booked as delegated rather than as the session's own, so the two stay
// distinguishable. Callers must pass a
// CUMULATIVE-TO-DELTA value: a child reports its running total, so a caller
// watching one must book the increment, not the total, or a chatty child is
// counted once per event it emits.
func (a *Agent) RecordDelegatedUsage(u provider.Usage) {
	if u == (provider.Usage{}) {
		return
	}
	a.cost.AddDelegated(u)
	// The DELEGATED observer, not the usage one. AddDelegated already keeps this
	// out of the last-turn snapshot; firing the plain usage hook put it back on
	// disk as an ordinary row, where a child's transcript-sized cold prompt is
	// indistinguishable from this session's cache collapsing.
	a.fireDelegatedUsage(u, a.cost.CumulativeTotal())
}

// DelegatedCost returns the part of the cumulative total that sub-agents spent
// on this session's behalf. Always <= Cost().
func (a *Agent) DelegatedCost() provider.Usage {
	return a.cost.DelegatedTotal()
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

// UserMessageExtras is what a host attaches to a user turn beyond the words the
// user typed: a preamble the host assembled, and metadata to stamp on the
// stored message. The zero value is an ordinary prompt.
//
// Preamble becomes its OWN leading text block rather than being glued onto the
// front of Text. The model sees the same thing either way — providers
// concatenate a message's text blocks — but a client can then render the user's
// words alone and present the preamble however it likes, instead of showing a
// bubble whose first nine lines are machine prose. It also keeps the preamble
// out of anything that reads "the user's message" for another purpose; the
// session-title seed is the one that bit us.
type UserMessageExtras struct {
	Preamble string
	Meta     map[string]string
}

// withMeta returns m with key set, without writing into the caller's map. The
// extras a host hands Prompt are its own — one may well be a package-level table
// reused across turns — and stamping into it would edit every message that ever
// shared it.
func withMeta(m map[string]string, key, value string) map[string]string {
	out := make(map[string]string, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	out[key] = value
	return out
}

// Prompt starts a turn with the user's text and any inline images.
func (a *Agent) Prompt(ctx context.Context, text string, images []provider.ImageBlock, sink func(AgentEvent)) error {
	return a.promptExtra(ctx, text, images, UserMessageExtras{}, sink)
}

// promptExtra is Prompt with a host-assembled preamble and message metadata.
// See [UserMessageExtras]. Prompt is this with a zero value, so there is one
// implementation and no twin to drift.
func (a *Agent) promptExtra(ctx context.Context, text string, images []provider.ImageBlock, extras UserMessageExtras, sink func(AgentEvent)) error {
	release, ok := a.acquire()
	if !ok {
		return ErrBusy
	}
	defer release()
	if err := a.PersistenceError(); err != nil {
		return err
	}
	if sink == nil {
		sink = func(AgentEvent) {}
	}
	sink = a.wrapSink(sink)
	// Consult the user-message guard before anything is recorded: a
	// rejection must leave no trace in the transcript and start no turn.
	// Skipped for an empty prompt (image-only submit — nothing to judge).
	if a.beforeUserMessage != nil && text != "" {
		allowed, reason, replacement := a.beforeUserMessage(text)
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
	// The preamble leads, and stays a block of its own — see UserMessageExtras.
	// It is deliberately NOT subject to BeforeUserMessage above: that guard
	// judges what the USER said, and the preamble is the host's own words.
	if extras.Preamble != "" {
		content = append(content, provider.TextBlock{Text: extras.Preamble})
	}
	if text != "" {
		content = append(content, provider.TextBlock{Text: text})
	}
	for _, img := range images {
		content = append(content, img)
	}
	user := provider.Message{Role: provider.RoleUser, Content: content, Time: time.Now(), Meta: extras.Meta}
	// Record the preamble HERE, where it is prepended, rather than leaving each
	// host to remember to say so alongside whatever else it stamped. A host that
	// assembled a preamble and no metadata is not exotic — it is what "every
	// attachment expired" looks like — and readers that skip the block key off
	// this, so the fact and the block have to be produced together.
	if extras.Preamble != "" {
		user.Meta = withMeta(user.Meta, MetaPreamble, "true")
	}

	a.mu.Lock()
	a.messages = append(a.messages, user)
	a.rev++
	revAfterUser := a.rev
	a.mu.Unlock()
	a.fireMessageAppended(user)
	sink(EvUserMessage{Message: user})

	err := a.runLoop(ctx, sink)

	// A PERSON stopped the turn and nothing at all was recorded after the
	// prompt: take it back out and hand the text to the host, which decides
	// whether it has a composer to return it to. See
	// docs/proposals/withdraw-cancelled-prompt.md.
	//
	// The cause, not ctx.Err(), is the test — see ErrUserInterrupted. Every
	// cancel would be the wrong rule twice over: a restart drain would delete
	// what the user typed on its way past, and a host that survives its own
	// drain (Workspace.Restart does, when relaunch refuses after it) would be
	// left with the message gone from memory and still on disk.
	//
	// It is also why this cannot key off err. runLoop returns ctx.Err() from
	// most cancel paths but nil from the terminal-stop one, so a cancel landing
	// exactly on a turn boundary comes back clean — an error check would miss
	// precisely the fastest Esc.
	//
	// Index is reported rather than left to be inferred. It is len(messages)
	// after the removal by construction, but a host that re-derived "it was the
	// last one" would delete the wrong durable row the day this rule changes,
	// and silently.
	if errors.Is(context.Cause(ctx), ErrUserInterrupted) {
		if idx, ok := a.withdrawLastUserMessage(revAfterUser); ok {
			// The prompt goes back to the composer, so anything it consumed on
			// the way out has to go back too. The engine consumed nothing a host
			// owns; a host segment spent on this turn's requests (see
			// TailDeliveryObserver) is restored by its owner on this event.
			sink(EvUserMessageWithdrawn{Text: text, Images: images, Index: idx})
		}
	}
	return err
}

// withdrawLastUserMessage drops the trailing message if and only if rev is
// still what it was when that message was appended — proof that nothing has
// touched the transcript since. Reports whether it removed anything.
//
// rev is the right test and a length compare is not: len(a.messages) can return
// to its old value after a compaction or a SetMessages, so equal length does not
// mean untouched, while rev is monotonic over every transcript mutation and
// cannot coincide. It moves for an appended assistant message, a tool result, a
// queued prompt, a compaction — every way this could stop being the last message
// — and, deliberately, not for editor typing (it is also the TUI's redraw cache
// key).
//
// The check and the removal share ONE lock acquisition on purpose. Split into
// check-then-delete, a queued prompt could be appended between them and the
// delete would take the wrong message off the end.
func (a *Agent) withdrawLastUserMessage(revAfterAppend uint64) (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.rev != revAfterAppend || len(a.messages) == 0 {
		return 0, false
	}
	idx := len(a.messages) - 1
	a.messages = a.messages[:idx]
	// Same bookkeeping DeleteMessage does, and for the same reason: rev because
	// the transcript changed, transcriptEpoch because it SHRANK — a consumer
	// holding an index into it must know its indices moved.
	a.rev++
	a.transcriptEpoch++
	return idx, true
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

// ContinueWithCue runs one turn against the existing transcript with cue on the
// ephemeral tail — Continue plus a request-scoped steer. It is what a GUIDED
// regenerate runs: the workspace has already retracted the take being replaced
// and truncated back to the last user message, and cue carries the user's note
// about what should be different (and, unless they asked for a blind retry, the
// withdrawn take itself).
//
// The cue is deliberately not persisted anywhere. A regenerate's takes all share
// the transcript prefix they were generated from, so writing the guidance into
// that prefix would put it in front of takes that never saw it — swipe back one
// and the scene would claim an instruction that did not apply. Request-scoped
// keeps every take's history honest, and keeps the guidance off the cache.
//
// An empty cue is exactly Continue. Single-flight like Prompt: ErrBusy if a run
// is already in progress.
func (a *Agent) ContinueWithCue(ctx context.Context, sink func(AgentEvent), cue string) error {
	release, ok := a.acquire()
	if !ok {
		return ErrBusy
	}
	defer release()
	if sink == nil {
		sink = func(AgentEvent) {}
	}
	sink = a.wrapSink(sink)
	defer a.setStageCue(cue)()
	return a.runLoop(ctx, sink)
}

// setStageCue installs a request-scoped cue and returns the function that clears
// it, so a turn can arm and disarm it in one deferred line. Both cue setters run
// exactly one turn, and neither may leave the cue armed for the next one.
func (a *Agent) setStageCue(cue string) func() {
	a.mu.Lock()
	a.stageCue = cue
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		a.stageCue = ""
		a.mu.Unlock()
	}
}

// ErrNoAssistantToContinue is returned by ContinueAssistant when the transcript
// does not end in an assistant message (there is nothing to extend).
var ErrNoAssistantToContinue = errors.New("no trailing assistant message to continue")

// ErrContinueUnsupported is returned by ContinueAssistant when the active
// provider's wire format does not extend a trailing assistant message as a
// prefill (only Anthropic does today).
var ErrContinueUnsupported = errors.New("this provider does not support continuing an assistant message")

// continuedMessage is the stashed result of a ContinueAssistant turn: the
// trailing assistant message extended in place, and its transcript index, for
// the caller to persist as an AmendReplace.
type continuedMessage struct {
	index   int
	message provider.Message
}

// ContinueAssistant extends the trailing assistant message: it runs one turn
// with that message as a provider prefill and MERGES the streamed continuation
// onto it in place, rather than appending a new message (the inverse of
// dropLastAssistantMessage). The ephemeral tail is suppressed for the turn so the
// assistant message is genuinely the last message in the request — required for
// the prefill. Requires the active client to advertise ContinuesAssistantPrefill.
// The merged message is stashed for ConsumeContinueResult so the caller persists
// an AmendReplace; the agent appends nothing durable itself.
func (a *Agent) ContinueAssistant(ctx context.Context, sink func(AgentEvent)) error {
	release, ok := a.acquire()
	if !ok {
		return ErrBusy
	}
	defer release()
	a.mu.Lock()
	n := len(a.messages)
	trailingIsAssistant := n > 0 && a.messages[n-1].Role == provider.RoleAssistant
	client := a.client
	a.mu.Unlock()
	if !trailingIsAssistant {
		return ErrNoAssistantToContinue
	}
	if !provider.ClientContinuesAssistantPrefill(client) {
		return ErrContinueUnsupported
	}
	if sink == nil {
		sink = func(AgentEvent) {}
	}
	a.mu.Lock()
	a.continuePrefill = true
	a.continueResult = nil
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.continuePrefill = false
		a.mu.Unlock()
	}()
	sink = a.wrapSink(sink)
	return a.runLoop(ctx, sink)
}

// ConsumeContinueResult returns and clears the last ContinueAssistant turn's
// merged message and index, or ok=false if the turn produced no merge (e.g. it
// errored before any text landed). The caller persists an AmendReplace at index.
func (a *Agent) ConsumeContinueResult() (index int, merged provider.Message, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.continueResult == nil {
		return 0, provider.Message{}, false
	}
	r := a.continueResult
	a.continueResult = nil
	return r.index, r.message, true
}

// ContinuesAssistantPrefill reports whether the agent's active provider can
// extend a trailing assistant message (the turn.continue gate).
func (a *Agent) ContinuesAssistantPrefill() bool {
	a.mu.Lock()
	c := a.client
	a.mu.Unlock()
	return provider.ClientContinuesAssistantPrefill(c)
}

// mergeContinuation folds a prefill continuation onto the message it extends: the
// base's blocks, then the continuation's, joining a trailing text block of base
// with the leading text block of the continuation into one (the model continued
// mid-text). The base's trailing whitespace is trimmed at the seam so the stored
// text matches what the model actually continued from (the converter trims the
// prefill the same way).
func mergeContinuation(base, cont provider.Message) provider.Message {
	merged := base
	merged.Content = append([]provider.Content{}, base.Content...)
	tail := cont.Content
	if n := len(merged.Content); n > 0 && len(tail) > 0 {
		if bt, ok := merged.Content[n-1].(provider.TextBlock); ok {
			baseText := strings.TrimRight(bt.Text, " \t\n\r")
			if ct, ok := tail[0].(provider.TextBlock); ok {
				merged.Content[n-1] = provider.TextBlock{Text: baseText + ct.Text}
				tail = tail[1:]
			} else {
				merged.Content[n-1] = provider.TextBlock{Text: baseText}
			}
		}
	}
	merged.Content = append(merged.Content, tail...)
	return merged
}

// messageHasToolCall reports whether a message carries any tool-call block.
func messageHasToolCall(m provider.Message) bool {
	for _, c := range m.Content {
		if _, ok := c.(provider.ToolCallBlock); ok {
			return true
		}
	}
	return false
}

// emitLifecycle delivers a host-lifecycle event to the OnEvent observer
// (the extension fanout / hook engine) directly, independent of an active
// Prompt. Host-driven compaction runs OUTSIDE the Prompt loop — callers
// invoke Compact on their own — so its EvCompactStart/EvCompactEnd would
// otherwise never reach OnEvent (the per-call sink that carries them is the
// host's own UI sink, not the wrapped one). Compaction triggers call this so
// extensions see compact_start / transcript_compacted. Nil-safe. (The
// mid-turn auto-compact inside runLoop doesn't need it: its sink is already
// the wrapped one.)
func (a *Agent) emitLifecycle(ev AgentEvent) {
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
// deliberately reuses the pin unchanged unless the ended segment grew the
// advertisement and the visibility asked for a re-pin within the Prompt, in
// which case it refreshes (repinForContinuation) so the continuation runs
// with the tools live — docs/proposals/activation-continuation.md.
type turnPin struct {
	system   string
	tools    Registry
	visible  func(name string) bool // nil advertises every registered tool
	readOnly *ReadOnlySet
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
	// The Stable segments are read with a peek: the pin wants the prefix, and
	// the per-request side effects belong to the request (oneTurn), which asks
	// again. Outside a.mu, because the assembler may read the agent.
	system := a.assemble(AssemblePeek).SystemText()
	a.mu.Lock()
	defer a.mu.Unlock()
	return turnPin{system: system, tools: a.tools, visible: a.advertiseLocked(a.tools, true), readOnly: a.readOnly.Snapshot()}
}

// RequestRepin asks the run loop to take a fresh pin of the system prompt and
// the tool registry after the current tool batch, so the next model step of
// the same turn runs with what the host published since the turn began.
//
// The default is the opposite on purpose (see pinTurn): a mid-turn change
// waits for the next segment, because re-reading the prefix evicts the prompt
// cache. RequestRepin is for a host change the rest of the turn must not miss.
// The first caller moves a Talkoot member into or out of a worktree: the
// rebuilt tools carry the new working directory, and the old pinned instances
// would keep running in the old one.
//
// Call it after the host publishes the new tools. A request made outside a
// turn is dropped when the next turn starts, because that turn's first pin
// already reads the current registry. The re-pin never touches a call in
// flight. It takes effect at the next model step.
func (a *Agent) RequestRepin() {
	if a == nil {
		return
	}
	a.repinRequested.Store(true)
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

// repinForContinuation refreshes the pin at a segment boundary when the ended
// segment grew the advertisement and repin is on; otherwise it returns the pin
// unchanged — the deliberate reuse the stage-0 contract pinned. A refresh is
// one tools-array cache write, the same write the next Prompt would have paid.
// Growth is asked against the live registry, because the boundary pins it.
func (a *Agent) repinForContinuation(pin turnPin, vis ToolVisibility, repin bool) turnPin {
	if !repin || !vis.Grew(a.ToolsSnapshot()) {
		return pin
	}
	return a.pinTurn()
}

// repinActivatedVisibility is the immediate post-tool availability boundary:
// when the tool batch just executed grew the advertisement within the PINNED
// registry, refresh only the visibility half of the pin so the very next model
// step advertises the new tools, instead of making the model finish its reply
// and wait for a continuation. It preserves pin.system and pin.tools (never
// importing an unrelated concurrent frame change or SetTools). A no-op unless
// the advertisement grew, so an ordinary tool call never writes the cache. The
// caller gates this on the repin answer snapshotted at runLoop entry.
func (a *Agent) repinActivatedVisibility(pin turnPin, vis ToolVisibility) turnPin {
	if !vis.Grew(pin.tools) {
		return pin
	}
	a.mu.Lock()
	pin.visible = vis.Advertise(pin.tools, true)
	a.mu.Unlock()
	return pin
}

func (a *Agent) runLoop(ctx context.Context, sink func(AgentEvent)) (err error) {
	defer func() { err = a.withPersistenceError(err) }()
	if err := a.PersistenceError(); err != nil {
		return err
	}
	// The visibility says once per Prompt whether a grown advertisement
	// re-pins within it, so a live toggle takes effect on the NEXT Prompt and
	// never mixes the immediate-refresh and the next-Prompt semantics within one.
	vis := a.ToolVisibility()
	repin := vis != nil && vis.BeginPrompt()

	// A re-pin request left over from an earlier turn is already satisfied:
	// the pin below reads the current prefix. Clear it before pinning, so it
	// cannot force a second, unrequested pin after this turn's first tool
	// batch. A request that lands after the clear is honoured as usual.
	a.repinRequested.Store(false)

	// One pin per segment; the whole Prompt is a single segment until a
	// boundary refreshes it (repinForContinuation) — see pinTurn.
	pin := a.pinTurn()

	// The step gates, snapshotted per prompt like the observers, are told the
	// prompt's loop is starting. A stuck-loop detector resets here
	// (packages/core/stall).
	stepGates := a.stepGateSnapshot()
	beginStep(stepGates)

	// The at-close continuation gates, snapshotted per Prompt like the
	// observers, with per-gate fire counts enforcing each gate's Cap
	// (default 1) — so a gate that always says "continue" can't loop the
	// model forever. Fallback gates run after the others: host correctness
	// gates outrank a convenience continuation such as lazy tools' activation
	// gate.
	gates := a.continuationGateSnapshot()
	gateFires := make([]int, len(gates))

	// Mid-turn auto-compact hysteresis: after a compaction fires, the
	// valve stays disarmed until the measured fraction actually drops
	// below the threshold again (one completed request refreshes it).
	// Without the re-arm rule, a tail too big to condense away — say one
	// enormous tool result inside the keep-tail — would re-trigger a
	// futile summarization on every subsequent step.
	compactArmed := true

	for step := 1; a.maxSteps <= 0 || step <= a.maxSteps; step++ {
		if err := a.PersistenceError(); err != nil {
			return err
		}
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
		// the reading is an estimate that must not double-fire. Whether to
		// compact is the policy's (terva's `turns` mode says no here, and
		// `off` says no everywhere); the step-1 exemption and the arming
		// are mechanics, and stay here.
		if step > 1 {
			if d := a.decideCompaction(CompactMidTurn); !d.Compact {
				compactArmed = true
			} else if compactArmed && a.canCompact(d.KeepTail) {
				compactArmed = false
				sink(EvCompactStart{Reason: "context near limit (mid-turn)"})
				cres, cerr := a.compactMidTurn(ctx, d.KeepTail, d.Strategies)
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
		if a.beforeTurn != nil {
			if allowed, reason := a.beforeTurn(step); !allowed {
				if reason == "" {
					reason = i18n.In(a.translator).T("turn blocked by extension guard")
				}
				sink(EvTurnEnd{Stop: provider.StopError, Err: fmt.Errorf("%s", reason)})
				sink(EvDone{})
				return nil
			}
		}

		var (
			stop         provider.StopReason
			assistantMsg provider.Message
			commit       func(incomplete bool)
			err          error
		)
		imageRounds := 0
		var retriedFor time.Duration // backoff actually slept this step
		// A continue turn deliberately leaves its prefill target (the trailing
		// assistant message) LAST so oneTurn can extend it in place. The retry/
		// image-recovery drops below key on "last message is assistant" — which is
		// exactly that target — so on a transient error they would delete the very
		// message being continued, and the retry would then build from a transcript
		// no longer ending in an assistant, losing the prefill. Suppress the drops
		// for a continue turn: a transient error there carried no new content to
		// abandon (oneTurn kept nothing), and the target must survive to be retried.
		a.mu.Lock()
		continuePrefill := a.continuePrefill
		a.mu.Unlock()
		for attempt := 0; ; attempt++ {
			if err := a.PersistenceError(); err != nil {
				return err
			}
			stop, assistantMsg, commit, err = a.oneTurn(ctx, pin.system, pin.tools, pin.visible, sink)
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
					if !continuePrefill {
						a.dropLastAssistantMessage()
					}
					// Persist the drop so a resumed session re-applies it instead
					// of re-sending the bad image (fired outside the agent lock).
					a.fireImageExcluded(sha)
					attempt-- // recovery rounds don't consume the transient-retry budget
					continue
				}
			}
			if !a.canRetryError(err, attempt) {
				// Give up, but SAY that we tried. This message is read by three
				// surfaces — the red banner, the session error sidecar, and the
				// rescue dialog's reason — and all three previously showed the
				// provider's bare sentence, which reads as one immediate
				// failure. A sidecar row saying only "servers are overloaded"
				// is what made a working backoff look absent during forensics.
				//
				// Wrapped with %w so the typed classification underneath
				// (ProviderError, transport errors) keeps working; the suffix
				// deliberately carries no digit-colon pair that could trip the
				// prose heuristics in ClassifyRecoverable.
				if attempt > 0 {
					// retriedFor is what was actually SLEPT, accumulated as each
					// wait was taken — not the curve recomputed, which would
					// misreport every time a server's Retry-After overrode it.
					// It excludes the request time on top: a number that
					// undercounts honestly beats one that guesses.
					err = fmt.Errorf("%w (gave up after %d attempts over %s)",
						err, attempt+1, retriedFor.Round(time.Second))
				}
				break
			}
			// This attempt is being retried: drop its (possibly partial)
			// assistant message from memory and do NOT commit it, so the
			// durable session never records the abandoned attempt. Skipped for a
			// continue turn, whose trailing assistant is the prefill target, not an
			// abandoned attempt (see the continuePrefill note above the loop).
			if !continuePrefill {
				a.dropLastAssistantMessage()
			}
			// Announce the wait BEFORE taking it. A transient retry used to be
			// entirely silent, which made a working backoff look like no
			// backoff at all: the user sat through ~20s of nothing and then got
			// the provider's raw sentence, indistinguishable from a single
			// immediate failure. The sink is the same one the turn's text rides,
			// so every host that renders a turn can render this.
			delay := a.retryDelay(attempt, err)
			rec := RetryRecord{
				Phase:    RetryPhaseTurn,
				Provider: providerOf(err),
				Attempt:  attempt + 1, // 1-based: the attempt that just failed
				Max:      a.maxRetries,
				Delay:    delay,
				Err:      retryErrMsg(err),
			}
			sink(EvRetry{Provider: rec.Provider, Attempt: rec.Attempt,
				Max: rec.Max, Delay: rec.Delay, Err: rec.Err}) // live: the UI
			a.fireRetry(rec) // durable: the session row
			if sleepErr := sleepRetry(ctx, delay); sleepErr != nil {
				return sleepErr
			}
			retriedFor += delay
		}
		// The turn is final (success or non-retryable error). Persist and
		// emit the kept assistant message exactly once, before propagating
		// any error, so a final-but-errored turn still records what landed.
		//
		// commit is told whether the turn is ending badly. Only here is that
		// knowable: oneTurn cannot tell a retryable failure from a final one, and a
		// retryable one never gets this far (the partial is dropped and retried
		// above). So a true here means the reply is short and stays short, which is
		// what MetaIncomplete records.
		if commit != nil {
			commit(err != nil)
		}
		if perr := a.PersistenceError(); perr != nil {
			return errors.Join(err, perr)
		}
		if err != nil {
			return err
		}

		if stop == provider.StopToolUse {
			// Execute each tool call, append a single tool-results message, continue.
			toolCtx := context.WithValue(ctx, toolGenerationKey{}, toolGeneration{agent: a, tools: pin.tools, readOnly: pin.readOnly})
			// The tools see the prompt's step gates, the same set Begin and After
			// use, so a tool's inner-call reports reach exactly those gates.
			toolCtx = context.WithValue(toolCtx, stepGatesKey{}, stepGates)
			toolMsg, hadError := a.executeTools(toolCtx, assistantMsg, pin.tools, sink)
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
			// that carry tool-result images natively (Anthropic) declare
			// nothing and are left untouched.
			//
			// Gemini used to be named here as native, and it is not: its
			// functionResponse carries TEXT only, so it declares the
			// capability too. Measured 2026-08-14, a tool-returned image
			// reached the model only through this mirror.
			var imageMirror provider.Message
			// Use the unwrapping helper: openai-responses is wrapped in
			// a renamedClient (openai-responses), so a
			// direct type assertion on a.client would miss the capability.
			// The mirror additionally requires the model to accept image
			// input at all — mirroring screenshots to a vision-less model
			// wastes tokens at best and 400s at worst. Unknown models keep
			// the capability's default (true), preserving old behavior.
			mirrorImages := provider.ClientMirrorsToolImages(a.client)
			if mirrorImages {
				if m, err := a.lookupModel(a.providerID, a.model); err == nil && !m.Has(provider.CapImageInput) {
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
			if err := a.PersistenceError(); err != nil {
				return err
			}
			// If context was cancelled during tool execution, bail out.
			if err := ctx.Err(); err != nil {
				sink(EvDone{})
				return err
			}
			// The step gates see the batch that just ran, and one may end the
			// turn: the stuck-loop detector's escalation "stop" and its give-up
			// rung both do (packages/core/stall).
			if afterStep(ctx, stepGates, StepState{Assistant: assistantMsg, Results: toolMsg}, sink) {
				sink(EvDone{})
				return nil
			}
			_ = hadError
			// Immediate re-advertisement: if this tool batch grew the
			// advertisement (a lazy-tools activation, say), advertise the new
			// tools on the very NEXT model step rather than waiting for the model
			// to stop and a continuation gate to fire. Visibility-only against the
			// pinned registry (repinActivatedVisibility), and a no-op unless the
			// set grew, so it never churns the cache on an ordinary tool call.
			if a.repinRequested.CompareAndSwap(true, false) {
				// The host published a change this turn must not miss, such
				// as a new working directory (RequestRepin). A full pin, so
				// the next step dispatches to the new tool instances.
				pin = a.pinTurn()
			} else if repin {
				pin = a.repinActivatedVisibility(pin, vis)
			}
			continue
		}

		// If the assistant stopped without tool calls but a message was
		// queued while it was speaking, loop once more so that message
		// is appended and answered instead of waiting until a later
		// top-level prompt. This is a segment boundary: real input
		// outranks any gate (no synthetic nudge is injected), and the pin
		// refreshes only when the ended segment grew the advertisement —
		// otherwise it is deliberately reused.
		if ctx.Err() == nil && a.QueuedMessageCount() > 0 {
			pin = a.repinForContinuation(pin, vis, repin)
			continue
		}

		// At-close gates: when the model finishes naturally but a gate
		// still has work for it (a blocking context card, running
		// sub-agents, a freshly activated tool group), re-prompt with
		// that gate's nudge, appended as a user turn so the model can
		// respond. Registration order is priority order — the first gate
		// that fires wins the boundary, the rest wait for the next
		// natural stop — and each gate is capped per Prompt (Cap,
		// default 1), with Fallback gates last. Also a segment boundary:
		// the pin refreshes only when the ended segment grew the
		// advertisement, so an activation gate's continuation (and any
		// other gate's, incidentally) runs with the new tools live.
		if ctx.Err() == nil && stop == provider.StopEnd {
			if nudge, cause, ok := fireContinuationGate(gates, gateFires, stop); ok {
				sink(EvContinuation{Cause: cause})
				a.appendQueuedAsUser([]string{nudge}, true, sink)
				pin = a.repinForContinuation(pin, vis, repin)
				continue
			}
		}

		// Terminal stop (end, length, error, aborted).
		sink(EvDone{})
		return nil
	}
	if a.maxSteps > 0 {
		sink(EvDone{})
		return i18n.In(a.translator).Errorf("max steps (%d) exceeded", a.maxSteps)
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
	if err == nil || a.maxRetries <= 0 || attempt >= a.maxRetries {
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

// maxRetryDelay caps a single backoff wait, and with the default base and
// ceiling it is the LAST wait the agent takes before giving up: 2s, 4s, 8s,
// 16s, 32s, 60s.
//
// The old curve stopped at 8s — three retries, 14s of patience total. That is
// not enough for the failure it most often meets. "Our servers are currently
// overloaded, please try again later" is a load shedder, and a load shedder
// measured in seconds is asking to be waited out in tens of seconds; a session
// lost four turns to it inside half an hour, each after ~20s of trying. A
// minute at the tail costs one more paused turn when the provider is genuinely
// down, and saves the user retyping a prompt when it is merely busy.
//
// The tail is the expensive part on purpose: the early waits stay short so an
// ordinary transport blip still recovers in seconds.
const maxRetryDelay = 60 * time.Second

// retryDelay returns the wait before retry attempt n. A server-stated
// Retry-After wins over the default exponential backoff. Both are capped at
// maxRetryDelay, so a hostile or misconfigured header can't stall the turn for
// longer than terva would wait on its own judgement.
func (a *Agent) retryDelay(attempt int, err error) time.Duration {
	var pe *provider.ProviderError
	if errors.As(err, &pe) && pe.RetryAfter > 0 {
		return min(pe.RetryAfter, maxRetryDelay)
	}
	base := a.retryBaseDelay
	if base <= 0 {
		base = 2 * time.Second
	}
	// Shift-guard: attempt is bounded by MaxRetries, but a host is free to set
	// that to anything, and 1<<64 is not a long wait — it is zero.
	if attempt >= 32 {
		return maxRetryDelay
	}
	return min(base*time.Duration(1<<attempt), maxRetryDelay)
}

// providerOf and retryErrMsg pull the two things a retry notice needs out of
// whatever the client returned. A bare transport failure carries no provider
// name and no ProviderError wrapper, so both degrade to something renderable
// rather than to a panic or an empty line.
func providerOf(err error) string {
	var pe *provider.ProviderError
	if errors.As(err, &pe) {
		return pe.Provider
	}
	return ""
}

func retryErrMsg(err error) string {
	if err == nil {
		return ""
	}
	var pe *provider.ProviderError
	if errors.As(err, &pe) && pe.Msg != "" {
		// Msg alone, not Error(): the provider name and status are separate
		// fields on the event, and a renderer that wants "openai-codex: http
		// 503: …" can compose it. Repeating them inside the message is how a
		// status line ends up saying the provider's name twice.
		return pe.Msg
	}
	return err.Error()
}

// foreignCompactionNote stands in for a compaction blob the live provider
// cannot replay.
//
// The blob is opaque and provider-issued, so there is nothing to translate —
// the summarized turns cannot be recovered here at any cost. What CAN be fixed
// is the silence. Dropping it left the model reading a continuous conversation
// with half its history missing and no reason to doubt it; a note makes the gap
// something the model can see and say so about.
const foreignCompactionNote = "[a compaction summary from another provider stands here. It is an opaque " +
	"blob only its issuer can read, so the conversation it summarized is not available on this model. " +
	"Treat everything before this point as missing rather than as nothing having happened.]"

// replaceForeignCompactions swaps a compaction blob explicitly attributed to
// SOME OTHER provider for foreignCompactionNote.
//
// An UNATTRIBUTED blob is left exactly as it was, and that is a deliberate
// narrowing of provider.ForeignCompactions, which reports one as foreign to
// everyone. For DETECTION that paranoia is right — nothing should assume an
// unlabelled blob is replayable. For REPLACEMENT it is not: openai-codex is the
// only issuer in the tree, so an unattributed blob is in practice codex's own,
// from a session compacted before CompactionBlock.Provider existed. Converting
// those to a note would strip the summary out of every such session — silently
// destroying, in the name of preventing amnesia, exactly the context this is
// meant to protect. Two existing round-trip tests caught that, which is what
// they are for.
//
// So: act on the case that is knowable (an explicit mismatch), and leave the
// guess alone.
//
// Copy-on-write: msgs is a snapshot whose Content slices are the live
// transcript's, and the blob must survive there — switching BACK to the issuing
// provider has to replay it, and a session file that lost it could never be
// resumed on the model that made it.
func replaceForeignCompactions(msgs []provider.Message, providerName string) []provider.Message {
	var out []provider.Message
	for _, i := range provider.ForeignCompactions(msgs, providerName) {
		var content []provider.Content
		for ci, c := range msgs[i].Content {
			cb, ok := c.(provider.CompactionBlock)
			if !ok || cb.Provider == "" || cb.Provider == providerName {
				continue
			}
			if content == nil {
				content = make([]provider.Content, len(msgs[i].Content))
				copy(content, msgs[i].Content)
			}
			content[ci] = provider.TextBlock{Text: foreignCompactionNote}
		}
		if content == nil {
			continue
		}
		if out == nil {
			out = make([]provider.Message, len(msgs))
			copy(out, msgs)
		}
		out[i].Content = content
	}
	if out == nil {
		return msgs
	}
	return out
}

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
				h := transcriptcodec.ImageSHA256(v.Data)
				content[ci] = provider.TextBlock{Text: transcriptcodec.ImageRejectedNote}
				a.rev++
				a.transcriptEpoch++
				return h, true
			case provider.ToolResultBlock:
				for ii := len(v.Content) - 1; ii >= 0; ii-- {
					if ib, isImg := v.Content[ii].(provider.ImageBlock); isImg {
						h := transcriptcodec.ImageSHA256(ib.Data)
						v.Content[ii] = provider.TextBlock{Text: transcriptcodec.ImageRejectedNote}
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
//
// Its incomplete argument says the turn is ending on an error, which stamps
// [MetaIncomplete] on the message before it is persisted. Only the caller can
// supply it: oneTurn cannot tell a retryable failure from a final one, and this
// closure runs for the final one alone.
func (a *Agent) oneTurn(ctx context.Context, system string, tools Registry, visible func(name string) bool, sink func(AgentEvent)) (provider.StopReason, provider.Message, func(incomplete bool), error) {
	// system and tools are PINNED by runLoop for the whole user turn (see
	// the snapshot there) so a mid-turn host swap can't evict the prompt
	// cache between steps. The remaining request fields are read per step
	// under the lock: hosts assign Model/Reasoning/MaxTokens at runtime
	// (model swap) on another goroutine, and reading them piecemeal here
	// would race those writes. Take a consistent picture of those plus a
	// copy of the transcript while we hold the lock.
	a.mu.Lock()
	model := a.model
	providerID := a.providerID
	reasoning := a.reasoning
	reasoningSet := a.reasoningSet
	reasoningSummary := a.reasoningSummary
	showReasoning := a.showReasoning
	maxTokens := a.maxTokens
	temperature := a.temperature
	imageOutput := a.imageOutput
	client := a.client
	cacheKey := a.cacheID
	continuePrefill := a.continuePrefill
	stageCue := a.stageCue
	msgs := make([]provider.Message, len(a.messages))
	copy(msgs, a.messages)
	a.mu.Unlock()

	// Native image output is offered only when the live model advertises
	// CapImageOutput, so swapping to (or away from) an image-capable model
	// toggles it with no rebuild. Look it up under the agent's provider, then
	// the current client's — the capability is provider-specific (only the
	// Responses/codex path implements it), and an id can exist under more than
	// one provider. No bare-id fallback here: offering a tool the wire cannot
	// serve is worse than not offering it.
	if imageOutput != nil {
		m, err := provider.Model{}, errNoProvider
		if providerID != "" {
			m, err = a.Catalog().FindModel(providerID, model)
		}
		if err != nil {
			m, err = a.Catalog().FindModel(client.Name(), model)
		}
		if err != nil || !m.Has(provider.CapImageOutput) {
			imageOutput = nil
		}
	}

	// A compaction blob only the provider that issued it can read must not
	// reach a serializer that will drop it without a word.
	//
	// provider.ForeignCompactions was written for this and had no caller. Its
	// own doc explains why it belongs here — "Leaving this to each provider to
	// remember is how it stayed invisible; asking once, here, is what makes it
	// a decision" — and then nothing asked. Only openai-codex issues these
	// blocks and only its builder has an arm for them; the anthropic, gemini,
	// bedrock and openai content switches have no default arm, so the block
	// vanished. That is not degradation, it is amnesia: the blob is the ONLY
	// encoding of the assistant turns it replaced, so /model away from codex
	// after a server-side compaction sent the next model a conversation that
	// reads continuous and is missing half its history.
	msgs = replaceForeignCompactions(msgs, client.Name())

	// Ask the host for this request's frame outside the lock; its Volatile
	// segments ride the request only, never the transcript. The Stable ones are
	// ignored here: the prefix is the one pinned for the segment (system), so a
	// host changing it mid-turn cannot evict the cache under a running tool
	// loop. Asked even on a continue turn, whose tail is discarded, because the
	// assembler is the host's and may have side effects of its own.
	host := a.assemble(AssembleRequest).Volatile()

	// The ephemeral tail: host context (its Volatile segments carry the
	// component notes, such as the stuck-loop nudge), a Stage cue — everything
	// appended after the prompt-cache breakpoint. Composed as identified blocks
	// (see tail.go) rather than concatenated here, so recordTail below can say
	// WHICH of them the model was shown without re-deriving the assembly, and so
	// this and any other renderer of the tail cannot drift apart.
	tail := a.composeTail(host, stageCue, continuePrefill)
	ephemeral := tailText(tail)

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
		Messages: transcriptcodec.RepairToolUseResultPairs(msgs),
		// SpecsVisible advertises only the tools the pinned visibility predicate
		// admits (nil = all, today's behavior). Dispatch and the permission gate
		// still resolve the full `tools` registry (runOneTool below), so hiding a
		// tool here never affects callability or authority (retro H2·b).
		Tools:            tools.SpecsVisible(visible),
		Reasoning:        reasoning,
		ReasoningSet:     reasoningSet,
		ReasoningSummary: reasoningSummaryRequest(reasoningSummary, showReasoning),
		MaxTokens:        maxTokens,
		Temperature:      temperature,
		ImageOutput:      imageOutput,
		EphemeralContext: ephemeral,
		// The session id doubles as the provider cache-routing key so
		// concurrent conversations on one account (coordinator + swarm
		// children) stop evicting each other's cached prefixes. Empty
		// (live-only agents) sends nothing — today's behavior.
		PromptCacheKey: cacheKey,
	}
	if err := a.PersistenceError(); err != nil {
		return provider.StopError, provider.Message{}, nil, err
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
	a.recordDispatch(client, providerID, req)

	// Tell the dispatch observers what went on the wire. Placed beside
	// recordDispatch because both answer "what did we just put on the wire",
	// and both must see the request as sent rather than as intended. The same
	// snapshot hears this request's stream below.
	dispatchObs := a.dispatchObservers()
	for _, o := range dispatchObs {
		if o.Sent != nil {
			o.Sent(req)
		}
	}

	// Tell the host's assembler what this request carried, for a Volatile
	// segment that is shown once. From the tail as composed, so a continue turn,
	// which suppresses it, reports nothing.
	a.deliverTail(tail)

	// Record what the model was shown, if it differs from last time. The tail is
	// otherwise unauditable — composed per request and discarded — which left a
	// post-hoc review able to see a model's reaction to a prompt injection and
	// never the injection.
	a.recordTail(tail, continuePrefill)

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
		case provider.EventReasoningDelta:
			sink(EvReasoningDelta{Delta: e.Delta})
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
		// After the engine has acted on the event, so a row it writes for the
		// event lands first: a cache-cliff row follows the usage row it reads.
		for _, o := range dispatchObs {
			if o.Event != nil {
				o.Event(ev)
			}
		}
	}

	// Recording is off, so no readable reasoning may reach the record. Blank
	// what is separable from its replay payload, then drop what is not. Done
	// here, on the one message every path takes, rather than at each save site.
	//
	// 🪤 showReasoning is deliberately NOT part of this condition, and it once
	// was. The argument for gating on it was that a Codex summary exists only
	// because terva asked for one, so with recording and display both off there
	// is nothing on hand to strip. True for Codex, false for everyone else:
	// Gemini thought summaries and chat `reasoning_content` arrive unbidden and
	// were caught by neither branch, so "Record thinking: off" with display also
	// off wrote their text to the session file anyway. One setting answers this,
	// and it is the one whose name says so.
	if reasoningSummary == "" {
		finalMsg = stripUnrecordedSummaries(finalMsg)
		finalMsg = dropUnrecordableThinking(finalMsg)
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
		if a.beforeAssistantMessage != nil {
			orig := extractText(finalMsg)
			if orig != "" {
				allowed, _, replacement := a.beforeAssistantMessage(orig)
				if !allowed {
					suppress = true
				} else if replacement != "" && replacement != orig {
					emit = replaceText(finalMsg, replacement)
				}
			}
		}

		// A continue turn MERGES this message onto the trailing assistant message
		// in place instead of appending it, stashing the result for the caller to
		// persist as an AmendReplace (no durable append here — the model didn't
		// say a NEW message, it extended the last one). Only a pure-text
		// continuation merges: if the model produced tool calls it is a real new
		// step, so fall through to the normal append path. Inert for every
		// non-continue turn (continuePrefill is false).
		if continuePrefill && !messageHasToolCall(finalMsg) {
			a.mu.Lock()
			if mi := len(a.messages) - 1; mi >= 0 && a.messages[mi].Role == provider.RoleAssistant {
				merged := mergeContinuation(a.messages[mi], finalMsg)
				a.messages[mi] = merged
				a.rev++
				a.continueResult = &continuedMessage{index: mi, message: merged}
				a.mu.Unlock()
				commit := func(incomplete bool) {
					// No fireMessageAppended: the workspace persists an
					// AmendReplace from the stashed result. Emit the merged message
					// so a live subscriber redraws the extended bubble; the
					// post-turn snapshot is authoritative regardless.
					//
					// A continuation that died partway is itself incomplete, so the
					// mark goes on the stash as well as the live transcript. The
					// stash is what the workspace turns into an AmendReplace, and a
					// mark missing from it would vanish on the next reload.
					if incomplete {
						merged.Meta = withMeta(merged.Meta, MetaIncomplete, "true")
						a.mu.Lock()
						if mi < len(a.messages) {
							a.messages[mi] = merged
						}
						a.continueResult = &continuedMessage{index: mi, message: merged}
						a.rev++
						a.mu.Unlock()
					}
					if !suppress {
						sink(EvAssistantMessage{Message: merged})
					}
				}
				return stop, merged, commit, finalErr
			}
			a.mu.Unlock()
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

		commit := func(incomplete bool) {
			// Stamp BEFORE the durable append. fireMessageAppended is what writes the
			// row, so a mark added after it would need an amend to reach disk at all.
			// emit is a struct copy sharing finalMsg's Meta map and withMeta builds a
			// fresh one, so both need setting or the live event disagrees with the
			// transcript it is announcing.
			if incomplete {
				finalMsg.Meta = withMeta(finalMsg.Meta, MetaIncomplete, "true")
				emit.Meta = finalMsg.Meta
				a.mu.Lock()
				if mi := len(a.messages) - 1; mi >= 0 && a.messages[mi].Role == provider.RoleAssistant {
					a.messages[mi].Meta = finalMsg.Meta
				}
				a.rev++
				a.mu.Unlock()
			}
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
	var shared []SharedFile
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
		// Stamp the call id here rather than trusting the tool with it: a tool
		// cannot know its own call, so it also cannot claim another one's, and
		// the client's card-to-row mapping is the loop's fact, not the tool's
		// claim. Done before the sink so the live event carries it too.
		for i := range res.Shared {
			res.Shared[i].CallID = tc.ID
		}
		shared = append(shared, res.Shared...)
		results = append(results, provider.ToolResultBlock{
			CallID:  tc.ID,
			Content: res.Content,
			IsError: res.IsError,
		})
		sink(EvToolResult{ID: tc.ID, Result: res})
	}

	out := provider.Message{
		Role:    provider.RoleTool,
		Content: results,
		Time:    time.Now(),
	}
	// The shares ride the message's Meta, NOT its content: the model gets the
	// text line each tool returned and nothing retrievable, while the record
	// persists with the turn so a transcript reopened later still offers the
	// downloads. A record that will not marshal is dropped rather than fatal —
	// the turn's actual work is in Content, and losing a card must not lose it.
	if len(shared) > 0 {
		if raw, err := json.Marshal(shared); err == nil {
			out.Meta = map[string]string{MetaShared: string(raw)}
		}
	}
	return out, hadError
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

// abortedToolResult is the answer for a call that did not run because the turn
// it belonged to had ended. It is an error result rather than a silent skip:
// the model gets a tool-role reply for every call it made (some providers
// reject a transcript missing one), and the transcript records WHY, which is
// the only place a later reader can learn that the tool was skipped rather
// than that it ran and did nothing.
func abortedToolResult(why string) ToolResult {
	return ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: "aborted: " + why}},
		IsError: true,
	}
}

func (a *Agent) runOneTool(ctx context.Context, tc provider.ToolCallBlock, tools Registry, sink func(AgentEvent)) ToolResult {
	ctx = ContextWithAgent(ctx, a)
	// Name the call being executed, so a tool that calls back into the host can
	// attribute those inner calls to it (ReportInnerCall). The early returns below
	// (cancelled, unknown tool, unparseable args, refused) dispatch nothing, so
	// they make no inner calls and carry nothing to attribute.
	ctx = contextWithOuterCall(ctx, tc.ID)
	// A cancelled turn dispatches nothing further. Tools receive ctx, but a
	// tool is not obliged to read it — write, edit, glob and grep never do,
	// because for a filesystem call there is nothing to interrupt — so the
	// turn's own loop is the only place that can promise a cancel is a cancel.
	if ctx.Err() != nil {
		return abortedToolResult("the turn was cancelled before this tool call started")
	}
	// Dispatch against the registry PINNED for this turn (passed down from
	// runLoop), not a live read of a.tools: the turn runs on its own
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

	// Arguments that never parsed. The provider kept the model's original text
	// rather than discarding it, so the model can be told what is actually
	// wrong instead of being handed encoding/json's "invalid character '\t' in
	// string literal" — which names a character class, no location, and no
	// remedy. A model given that re-sends the identical bytes, because nothing
	// in it says what to change; that is what the stall detector kept catching.
	// Running the tool on the "{}" placeholder would be worse still: it would
	// fail on a missing required field and blame the wrong thing entirely.
	if tc.RawArguments != "" {
		return ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: unparseableArgsMessage(tc.Name, tc.RawArguments)}},
			IsError: true,
		}
	}

	args := tc.Arguments

	// The gate: it can refuse the call before any side effect happens,
	// OR rewrite the args seen by the tool. The model sees the reason as
	// the tool error, learns from it, and (typically) proposes a different
	// action; rewrites are invisible to the model (they apply only to the
	// execution). It receives the tool resolved above, from the registry
	// pinned for this turn, so it can describe the call without reaching
	// back into the agent.
	//
	// 🚨 An Agent built without New has no gate, and fails closed here.
	// Running its calls unchecked is exactly what the constructor argument
	// exists to prevent.
	if isNilGate(a.gate) {
		return ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: noGateReason}},
			IsError: true,
		}
	}
	{
		allowed, reason, modified := a.gate.CheckTool(ctx, tc, tool)
		if !allowed {
			if reason == "" {
				reason = "tool call refused by extension guard"
			}
			return ToolResult{
				Content: []provider.Content{provider.TextBlock{Text: reason}},
				IsError: true,
			}
		}
		// The ladder can block for MINUTES waiting on a human in a chat or an
		// orchestrator over MCP. Those confirmers now take this turn's context
		// and unpark when it is cancelled, but that makes the race narrower, not
		// absent: an approval delivered a moment BEFORE the cancel still returns
		// allow, and the hook and extension rungs answer on their own schedule
		// too. Without this check the tool then runs after the user was told
		// "cancelled the current turn" — the write landed anyway. An answer for
		// a turn that no longer exists is too late by definition, whatever it
		// says, and whatever unparked it.
		if ctx.Err() != nil {
			return abortedToolResult("the turn was cancelled while this tool call waited for approval, so the approval arrived too late to run it")
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
//
// Unstable: a transcript helper carries no promise before 1.0.
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

// extractText concatenates all TextBlock content in a message. Used
// by BeforeAssistantMessage so guards see a single string instead of
// having to walk provider.Content themselves.
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
