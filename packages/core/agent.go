package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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

	// OnEvent, if set, mirrors every AgentEvent the loop emits to
	// this callback in addition to the per-Prompt sink. Used by the
	// extension manager to fan events out to subscribed extensions
	// without each caller having to compose sinks manually.
	OnEvent func(AgentEvent)

	// ContextProvider, if set, is called once per turn to obtain
	// host-assembled ephemeral context (already wrapped/bounded) to
	// inject into the model's context for that request only. The result
	// rides provider.Request.EphemeralContext — a trailing block after
	// the cache breakpoint, never written to the transcript. Hosts wire
	// this to the extension manager's live context cards. Called outside
	// the agent lock; keep it quick.
	ContextProvider func() string

	// ContinueOnStop, if set, is consulted when a turn ends with a
	// natural stop (the model produced a final message, no tool calls).
	// Returning (true, nudge) appends nudge as a user message and runs
	// one more turn — the at-close gate hosts use to re-prompt the model
	// when tracked work is still open ("you indicated you're finishing
	// …"). The loop fires it at most ONCE per Prompt regardless of the
	// return value, so a host that always returns true can't loop.
	ContinueOnStop func(stop provider.StopReason) (cont bool, nudge string)

	// OnMessageAppended, if set, fires every time a message is
	// appended to the in-memory transcript by the agent loop — the
	// initial user prompt, each finalised assistant message, and
	// each tool-results message (plus the synthetic OpenAI image
	// mirror, if any). Hosts wire this to the on-disk session so
	// that turns are durable as soon as they happen, instead of
	// only being flushed on a clean exit.
	OnMessageAppended func(provider.Message)

	// OnUsage, if set, fires after every turn's usage row arrives,
	// carrying the cumulative usage for the session. Hosts wire
	// this to the on-disk session so the persisted total stays
	// current and a crash recovers the right cost figure.
	OnUsage func(cumulative provider.Usage)

	// OnTranscriptCompacted, if set, fires after Compact replaces the
	// in-memory transcript with the synthetic summary plus kept tail.
	// Hosts wire this to append an explicit compaction checkpoint to
	// the session log; per-message append hooks do not fire for this
	// wholesale transcript replacement.
	OnTranscriptCompacted func(messages []provider.Message)

	// OnImageExcluded, if set, fires when image-rejection recovery drops an
	// image (a provider 400'd on it) from the transcript, carrying the image's
	// sha256. Hosts wire this to append an exclude_image directive to the
	// session, so the fix persists: a resumed session re-applies it instead of
	// re-sending the bad image and re-failing. The recovery is paid once.
	OnImageExcluded func(sha256Hex string)

	// running is the single-flight guard. It is set on entry to
	// Prompt/Continue/Compact and cleared on exit; a second concurrent
	// call sees it set and returns ErrBusy instead of interleaving its
	// transcript mutations with the in-flight run. It is an atomic so
	// the check-and-set needs no separate lock and never blocks.
	running atomic.Bool

	mu       sync.Mutex
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
	// full content again.
	transcriptEpoch uint64
	cost            CostTracker

	// queued holds user messages submitted while the agent is busy.
	// The loop appends them as normal user messages at safe
	// boundaries: before the next model call after a tool batch, or
	// after a text-only assistant turn finishes. It never interrupts
	// a running tool or cancels an in-flight provider request.
	queued []string
}

// NewAgent returns an Agent with sensible defaults.
func NewAgent(client provider.Client, model, system string, tools Registry) *Agent {
	return &Agent{
		Client:         client,
		Model:          model,
		System:         system,
		Tools:          tools,
		MaxSteps:       0, // 0 = unlimited
		MaxRetries:     3,
		RetryBaseDelay: 2 * time.Second,
	}
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
// respawned (and their freshly-registered tools merged in).
func (a *Agent) SetTools(reg Registry) {
	a.mu.Lock()
	a.Tools = reg
	a.mu.Unlock()
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

// fireMessageAppended invokes OnMessageAppended without holding the
// agent mutex, so the host's persistence callback can take its own
// locks without deadlocking the agent loop. Tolerates a nil hook so
// non-persisting callers (tests, RPC mode) don't have to set it.
func (a *Agent) fireMessageAppended(m provider.Message) {
	if a.OnMessageAppended != nil {
		a.OnMessageAppended(m)
	}
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
// Prompt. Compaction runs OUTSIDE the Prompt loop — callers invoke Compact
// on their own — so its EvCompactStart/EvCompactEnd would otherwise never
// reach OnEvent (the per-call sink that carries them is the host's own UI
// sink, not the wrapped one). Compaction triggers call this so extensions
// see compact_start / transcript_compacted. Nil-safe.
func (a *Agent) EmitLifecycle(ev AgentEvent) {
	if a.OnEvent != nil {
		a.OnEvent(ev)
	}
}

// wrapSink composes the per-call sink with a.OnEvent (if set) so the
// extension manager (or any other observer) sees every AgentEvent
// without having to thread itself through every Prompt callsite.
func (a *Agent) wrapSink(sink func(AgentEvent)) func(AgentEvent) {
	if a.OnEvent == nil {
		return sink
	}
	obs := a.OnEvent
	return func(ev AgentEvent) {
		obs(ev)
		sink(ev)
	}
}

func (a *Agent) runLoop(ctx context.Context, sink func(AgentEvent)) error {
	// gateFired caps the at-close gate (ContinueOnStop) to one re-prompt
	// per Prompt, so a host that always says "continue" can't loop the
	// model forever.
	gateFired := false
	for step := 1; a.MaxSteps <= 0 || step <= a.MaxSteps; step++ {
		// Messages queued while the agent was busy are delivered
		// before the next model call. This is the safe boundary:
		// any previous tool batch has already completed and its
		// results have been appended, but no new provider request has
		// started yet.
		if pending := a.drainQueuedMessages(); len(pending) > 0 {
			a.appendQueuedAsUser(pending, false, sink)
		}

		sink(EvTurnStart{Step: step})
		if a.BeforeTurn != nil {
			if allowed, reason := a.BeforeTurn(step); !allowed {
				if reason == "" {
					reason = "turn blocked by extension guard"
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
			stop, assistantMsg, commit, err = a.oneTurn(ctx, sink)
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
					if a.OnImageExcluded != nil {
						a.OnImageExcluded(sha)
					}
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
			toolMsg, hadError := a.executeTools(ctx, assistantMsg, sink)
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
			// Use the unwrapping helper: openai-codex is wrapped in
			// RefreshingClient and openai-responses in renamedClient, so a
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
			_ = hadError
			continue
		}

		// If the assistant stopped without tool calls but a message was
		// queued while it was speaking, loop once more so that message
		// is appended and answered instead of waiting until a later
		// top-level prompt.
		if ctx.Err() == nil && a.QueuedMessageCount() > 0 {
			continue
		}

		// At-close gate: when the model finishes naturally but the host
		// still has open work (a blocking context card), re-prompt it
		// once with the host's nudge, appended as a user turn so the
		// model can respond. Capped to one re-prompt per Prompt.
		if ctx.Err() == nil && !gateFired && stop == provider.StopEnd && a.ContinueOnStop != nil {
			if cont, nudge := a.ContinueOnStop(stop); cont && nudge != "" {
				gateFired = true
				a.appendQueuedAsUser([]string{nudge}, true, sink)
				continue
			}
		}

		// Terminal stop (end, length, error, aborted).
		sink(EvDone{})
		return nil
	}
	if a.MaxSteps > 0 {
		sink(EvDone{})
		return fmt.Errorf("max steps (%d) exceeded", a.MaxSteps)
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
func (a *Agent) oneTurn(ctx context.Context, sink func(AgentEvent)) (provider.StopReason, provider.Message, func(), error) {
	// Snapshot the mutable request fields once under the lock. Hosts
	// assign Model/System/Tools/Reasoning/MaxTokens at runtime (model
	// swap, /reload-ext) on another goroutine; reading them piecemeal
	// here would race those writes. Take a consistent picture for the
	// whole turn and a copy of the transcript while we hold the lock.
	a.mu.Lock()
	model := a.Model
	system := a.System
	tools := a.Tools
	reasoning := a.Reasoning
	maxTokens := a.MaxTokens
	temperature := a.Temperature
	client := a.Client
	contextProvider := a.ContextProvider
	msgs := make([]provider.Message, len(a.messages))
	copy(msgs, a.messages)
	a.mu.Unlock()

	// Pull host ephemeral context (e.g. an extension's live task card)
	// outside the lock; it rides the request only, never the transcript.
	var ephemeral string
	if contextProvider != nil {
		ephemeral = contextProvider()
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
		Messages:         repairToolUseResultPairs(msgs),
		Tools:            tools.Specs(),
		Reasoning:        reasoning,
		MaxTokens:        maxTokens,
		Temperature:      temperature,
		EphemeralContext: ephemeral,
	}
	stream, err := client.Stream(ctx, req)
	if err != nil {
		return provider.StopError, provider.Message{}, nil, err
	}

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
			if a.OnUsage != nil {
				a.OnUsage(cum)
			}
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
func (a *Agent) executeTools(ctx context.Context, msg provider.Message, sink func(AgentEvent)) (provider.Message, bool) {
	var results []provider.Content
	hadError := false

	for _, c := range msg.Content {
		tc, ok := c.(provider.ToolCallBlock)
		if !ok {
			continue
		}
		res := a.runOneTool(ctx, tc, sink)
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

func (a *Agent) runOneTool(ctx context.Context, tc provider.ToolCallBlock, sink func(AgentEvent)) ToolResult {
	tool, err := a.Tools.Get(tc.Name)
	if err != nil {
		return ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: err.Error()}},
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
