package core

// Turn policy (TUI plan Phase 2c): the decisions users think of as
// part of terva's core behavior — "is this failure worth retrying on
// another model?", "did the request outgrow the provider's byte
// limit?", "is the context full enough to condense?" — used to live
// only in the interactive mode, so print/json/rpc silently lacked
// them. They are core policy now: every run mode consumes the same
// classifiers, and PromptWithPolicy gives one-shot surfaces the
// compact-and-retry behavior interactive users already had.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// ClassifyRecoverable inspects an agent error and decides whether
// it's a recoverable provider failure that a host should offer a
// model switch / retry for, plus a short human-readable reason.
// Returns (false, "") for errors that switching models won't fix
// (bad request, context length, transcript serialization issues) so
// they surface as-is.
func ClassifyRecoverable(err error) (bool, string) {
	if err == nil {
		return false, ""
	}
	msg := err.Error()

	// Don't trigger on payload-too-large or context-length overflow;
	// those paths have their own compact-and-retry handling.
	if IsPayloadTooLargeError(err) || IsContextLengthError(err) {
		return false, ""
	}

	// Typed classification first: in-tree providers return
	// *provider.ProviderError, so status codes are data, not prose.
	var pe *provider.ProviderError
	if errors.As(err, &pe) {
		switch {
		case pe.Status == 401:
			return true, i18n.T("authentication failed: %s", shortErrorText(msg))
		case pe.Status == 403:
			return true, i18n.T("permission denied: %s", shortErrorText(msg))
		case pe.Status == 429:
			return true, i18n.T("rate limited: %s", shortErrorText(msg))
		case pe.Status >= 500:
			return true, i18n.T("provider unavailable: %s", shortErrorText(msg))
		case pe.Status == 0 && pe.Transient:
			// Stream death / transient in-stream errors.
			return true, i18n.T("network failure: %s", shortErrorText(msg))
		case pe.Status == 0:
			// In-stream API errors without a status: fall through to
			// the prose heuristics below — the provider's own error
			// vocabulary ("invalid_authentication", …) still carries
			// the class.
		default:
			// 4xx other than auth/rate (400 validation, 404, …):
			// switching models won't fix it; surface as-is.
			return false, ""
		}
	} else if provider.IsTransportError(err) {
		return true, i18n.T("network failure: %s", shortErrorText(msg))
	}

	// Prose fallback for untyped errors (custom SDK clients, auth
	// wrappers) and in-stream API errors without a status code.
	low := strings.ToLower(msg)
	if strings.Contains(low, "timeout") ||
		strings.Contains(low, "deadline exceeded") ||
		strings.Contains(low, "connection refused") ||
		strings.Contains(low, "connection reset") ||
		strings.Contains(low, "no such host") ||
		strings.Contains(low, "tls handshake") ||
		strings.Contains(low, "eof") {
		return true, i18n.T("network failure: %s", shortErrorText(msg))
	}
	switch {
	case containsAnyText(low, "http 401", " 401:", "invalid_authentication", "token expired", "api key appears to be invalid"):
		return true, i18n.T("authentication failed: %s", shortErrorText(msg))
	case containsAnyText(low, "http 403", " 403:", "permission denied", "forbidden"):
		return true, i18n.T("permission denied: %s", shortErrorText(msg))
	case containsAnyText(low, "http 429", " 429:", "rate limit", "rate_limit", "too many requests", "quota"):
		return true, i18n.T("rate limited: %s", shortErrorText(msg))
	// "overloaded" carries no status code and reaches this prose fallback intact:
	// it is how BOTH Anthropic ("overloaded_error") and codex ("Our servers are
	// currently overloaded. Please try again later.") phrase capacity pressure,
	// and neither wording matches a needle above. The typed path catches it when
	// the error is still a *provider.ProviderError — but a host that rebuilds the
	// error from wire text (the TUI carrier does exactly that, errors.New(ev.Error))
	// has no type left, so without this the most common recoverable provider
	// failure there is skipped the model-switch rescue and shown as a dead end.
	case containsAnyText(low, "http 500", "http 502", "http 503", "http 504", " 500:", " 502:", " 503:", " 504:", "upstream connect error", "service unavailable", "internal server error", "bad gateway", "gateway timeout", "overloaded"):
		return true, i18n.T("provider unavailable: %s", shortErrorText(msg))
	}

	// Anything else (400 bad request, validation errors, etc.) is
	// usually not fixed by switching models; let it surface as-is.
	return false, ""
}

// IsPayloadTooLargeError matches HTTP 413 responses surfaced by the
// provider clients — typed status first, with a prose fallback for
// untyped errors and for providers that phrase oversize rejections
// without the status code.
func IsPayloadTooLargeError(err error) bool {
	if err == nil {
		return false
	}
	var pe *provider.ProviderError
	if errors.As(err, &pe) && pe.Status != 0 {
		return pe.Status == 413
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "http 413") || strings.Contains(msg, " 413") || strings.HasPrefix(msg, "413 ") || strings.Contains(msg, "payload too large") || strings.Contains(msg, "request entity too large")
}

// IsContextLengthError matches provider rejections of a transcript that
// outgrew the model's context window. Providers phrase this as an HTTP
// 400 (no dedicated status like 413), so the message text is the
// discriminator for typed and untyped errors alike. The needle list is
// deliberately conservative — a false positive would trigger a
// compact-and-retry on an unrelated validation error.
func IsContextLengthError(err error) bool {
	if err == nil {
		return false
	}
	return containsAnyText(strings.ToLower(err.Error()),
		"context_length_exceeded",              // openai error code
		"maximum context length",               // openai chat-completions message
		"exceeds the context window",           // openai responses message
		"prompt is too long",                   // anthropic
		"input is too long for requested",      // anthropic on bedrock
		"input token count exceeds",            // gemini
		"exceeds the maximum number of tokens", // gemini/vertex variants
	)
}

// ExtractFailedProvider pulls the failing provider name from a typed
// provider error, falling back to parsing the conventional
// "provider: http NNN: …" prefix for untyped errors. Returns "" when
// nothing recognisable is found.
func ExtractFailedProvider(err error) string {
	if err == nil {
		return ""
	}
	var pe *provider.ProviderError
	if errors.As(err, &pe) && pe.Provider != "" {
		return pe.Provider
	}
	msg := err.Error()
	if i := strings.Index(msg, ":"); i > 0 {
		head := strings.TrimSpace(msg[:i])
		switch head {
		case "anthropic", "openai", "openai-codex", "kimi", "google", "ollama":
			return head
		}
	}
	return ""
}

// AutoCompactMode selects where automatic transcript compaction may
// run. Manual /compact is always available regardless of mode.
type AutoCompactMode string

const (
	// AutoCompactOff disables ALL automatic compaction, including the
	// oversize-error recovery retry: context-limit failures surface to
	// the user, who compacts by hand. The purist escape hatch.
	AutoCompactOff AutoCompactMode = "off"
	// AutoCompactTurns compacts at turn boundaries only (pre-turn /
	// post-turn checks + the oversize-error recovery) — the behavior
	// before mid-turn compaction existed.
	AutoCompactTurns AutoCompactMode = "turns"
	// AutoCompactSteps additionally compacts at the safe boundary
	// between a turn's tool-loop steps, so a marathon agentic turn
	// can't grow past the window with no check. The default.
	AutoCompactSteps AutoCompactMode = "steps"
)

// autoCompactMode resolves the agent's effective mode: the host's live
// policy hook when set and valid, AutoCompactSteps otherwise (an
// unknown value from a hand-edited config degrades to the default, not
// to silence).
func (a *Agent) autoCompactMode() AutoCompactMode {
	if a.AutoCompactPolicy != nil {
		switch m := a.AutoCompactPolicy(); m {
		case AutoCompactOff, AutoCompactTurns, AutoCompactSteps:
			return m
		}
	}
	return AutoCompactSteps
}

// AutoCompactThreshold is the context-window fraction at which a
// host should condense the transcript after a turn ends. 0.85 leaves
// enough headroom for one more user prompt + response before the
// hard limit.
const AutoCompactThreshold = 0.85

// AutoCompactKeepTail is the number of most-recent messages auto-compact
// preserves verbatim after the summary. It is also the floor below which
// there is nothing to summarize (see CanCompact): a transcript of this
// size or smaller is entirely keep-tail.
const AutoCompactKeepTail = 4

// ContextWarnFraction is the context-window fraction at which the model
// itself is warned about context pressure (an ephemeral note riding the
// request tail — see oneTurn). Earlier than AutoCompactThreshold on
// purpose: the band between the two is the model's window to wrap up or
// get economical before the harness force-compacts.
const ContextWarnFraction = 0.70

// ContextUsage reports the most recent request's context consumption
// (input + cache tokens) and the model's working window from the live
// catalog. The working window is EffectiveContextWindow — the desired
// window when the user (or catalog) set one below the model max, else the
// model max — so auto-compaction can be pinned below a pricing surcharge
// without lying about the model's true ceiling. window is 0 when the
// model is unknown; used is 0 before any request lands usage.
func (a *Agent) ContextUsage() (used, window int) {
	// a.Model under the lock: SetModel and SetClientAndModel write it there, and
	// this is called from the turn loop while a host may be swapping models on
	// another goroutine. The unlocked read this replaces was a real data race —
	// latent only because nothing had exercised the two concurrently. No caller
	// holds a.mu (runLoop, oneTurn after its snapshot, compactHeld, the policy
	// checks), so taking it here cannot re-enter.
	a.mu.Lock()
	model := a.Model
	a.mu.Unlock()

	if m, err := provider.FindModel("", model); err == nil {
		window = m.EffectiveContextWindow()
	}
	last := a.LastTurnUsage()
	used = last.InputTokens + last.CacheReadTokens + last.CacheWriteTokens
	return used, window
}

// ContextFraction reports the share of the model's context window
// the last turn consumed (input + cache tokens over the catalog's
// window). Returns 0 when the window is unknown or no turn has
// landed usage yet.
func (a *Agent) ContextFraction() float64 {
	used, window := a.ContextUsage()
	if used <= 0 || window <= 0 {
		return 0
	}
	return float64(used) / float64(window)
}

// ShouldAutoCompact reports whether the transcript has grown past
// threshold (use AutoCompactThreshold for the standard policy). Always
// false in AutoCompactOff mode, so every host's opportunistic check
// (pre-turn, post-turn, idle) inherits the knob without changes.
func (a *Agent) ShouldAutoCompact(threshold float64) bool {
	if threshold <= 0 || a.autoCompactMode() == AutoCompactOff {
		return false
	}
	return a.ContextFraction() >= threshold
}

// CanCompact reports whether Compact(keepTail) would actually have
// something to summarize — i.e. the transcript is longer than keepTail.
// Auto-compact gates on this so it never fires a no-op compaction on a
// transcript that is already entirely keep-tail (which is exactly the
// state right after a successful compaction). Without the guard, the
// post-compaction context fraction can still read high enough to
// re-trigger, producing a spurious "nothing to compact" failure.
func (a *Agent) CanCompact(keepTail int) bool {
	if keepTail < 0 {
		keepTail = 0
	}
	a.mu.Lock()
	n := len(a.messages)
	a.mu.Unlock()
	return n > keepTail
}

// PromptWithPolicy runs Prompt with the standard turn policy that
// interactive users already get, for one-shot hosts (print/json):
//
//   - pre-turn: when a resumed transcript is already past the
//     auto-compact threshold, condense before sending so the request
//     doesn't bounce off the context limit;
//   - on HTTP 413 (request bytes too large) or a context-length
//     rejection (providers phrase that as a 400): condense and retry
//     the prompt once.
//
// Compaction progress is reported through sink as EvCompactStart /
// EvCompactEnd so streaming consumers can surface it.
func (a *Agent) PromptWithPolicy(ctx context.Context, prompt string, images []provider.ImageBlock, sink func(AgentEvent)) error {
	return a.PromptWithPolicyExtra(ctx, prompt, images, UserMessageExtras{}, sink)
}

// PromptWithPolicyExtra is PromptWithPolicy carrying a host-assembled preamble
// and message metadata (see [UserMessageExtras]). PromptWithPolicy is this with
// a zero value — one implementation, so the compaction and 413-retry policy
// cannot drift between the two entry points.
func (a *Agent) PromptWithPolicyExtra(ctx context.Context, prompt string, images []provider.ImageBlock, extras UserMessageExtras, sink func(AgentEvent)) error {
	// A nil sink is legal — the Workspace/web carrier passes nil and relies on
	// the agent's OnEvent fan-out (via EmitLifecycle below). Prompt normalizes
	// its own nil sink, but the compact closure calls sink directly, so without
	// this a firing auto-compact (or 413 retry) would panic the turn goroutine.
	if sink == nil {
		sink = func(AgentEvent) {}
	}
	compact := func(reason string) error {
		start := EvCompactStart{Reason: reason}
		sink(start)
		a.EmitLifecycle(start) // reach extensions: sink here is the host UI sink, not the OnEvent fanout
		res, err := a.Compact(ctx, AutoCompactKeepTail, func(string) {})
		if errors.Is(err, ErrNothingToCompact) {
			// Nothing left to summarize — not a failure. Report a clean
			// compact_end so consumers don't surface a phantom error.
			err = nil
		}
		ev := EvCompactEnd{Usage: res.Usage}
		if err != nil {
			ev.Err = err.Error()
		}
		sink(ev)
		a.EmitLifecycle(ev)
		return err
	}
	// The prefix-change guard, before the auto-compact check because a
	// compaction here makes that one moot, and before Prompt appends the user's
	// message so the summarizer sees the conversation as it actually stood.
	a.offerCompactOnPrefixChange(ctx, compact)

	if a.ShouldAutoCompact(AutoCompactThreshold) && a.CanCompact(AutoCompactKeepTail) {
		// A failed compaction does NOT fail the turn — the same reasoning
		// offerCompactOnPrefixChange spells out below, which this site did not
		// inherit. The user asked for a message to be sent, not for it to be
		// dropped because a precaution failed.
		//
		// And it IS only a precaution here. AutoCompactThreshold is 0.85, so the
		// request the abort refused to send would almost always have fit. Worse,
		// this is the one compaction site that runs BEFORE PromptExtra appends the
		// user's message, so aborting was also the one that lost it: not in the
		// transcript, not in the queue, and — because the carrier rebuilds turn
		// errors from wire text, where the typed provider error is gone — not
		// reliably in the rescue dialog either.
		//
		// If the transcript genuinely no longer fits, the oversize retry below is
		// the designed catch, and the provider's own "prompt is too long" is a
		// better error than "auto-compact before prompt: overloaded" ever was.
		if cerr := compact("context near limit"); cerr != nil && ctx.Err() != nil {
			// Cancelled mid-compaction: the user asked to stop, so don't dispatch
			// the turn they just interrupted. A provider failure is non-fatal; a
			// cancellation is not.
			return cerr
		}
	}
	// Both attempts carry the extras: a retry that dropped the preamble would
	// re-send the turn without the attachment manifest, and the model would be
	// asked about files it had just been told about and now could not see.
	err := a.PromptExtra(ctx, prompt, images, extras, sink)
	if err != nil && ctx.Err() == nil && (IsPayloadTooLargeError(err) || IsContextLengthError(err)) &&
		a.autoCompactMode() != AutoCompactOff {
		if cerr := compact("request too large; retrying"); cerr != nil {
			return err
		}
		return a.PromptExtra(ctx, prompt, images, extras, sink)
	}
	return err
}

// offerCompactOnPrefixChange asks whether to condense the transcript before a
// pending prompt-prefix invalidation lands, and condenses it if the answer is
// yes. It is the reason the rest of this file's cache machinery exists.
//
// The situation it guards: while the agent sat idle, something rewrote the
// prompt prefix the provider had cached — a /model switch, an extension reload
// regenerating the tool set and the system prompt that embeds it. Nothing is
// broken and nothing warns. The next message simply costs, silently, a
// full-price re-read of a 60-100k transcript that was being served from cache
// for a tenth of that a minute ago. The user finds out on the invoice.
//
// Three properties, all inherited from comparing prefixes rather than counting
// events (see pendingPrefixChange): the offer is made ONCE no matter how many
// things changed, it quotes the cost of the invalidation actually about to be
// paid, and a change that gets reverted before the next turn withdraws its own
// offer. And it needs no "already asked" flag: accept or decline, the turn
// dispatches, lastSent becomes the new prefix, and there is nothing left to ask
// about. The invalidation is a one-time toll, so an offer to avoid it can only
// ever be made once.
//
// It requires cache_aware_compaction, and that is the economics rather than an
// implementation convenience. With the bespoke summarizer, compacting costs a
// full-price read of the whole transcript — which is the same full-price read
// that eating the invalidation costs. There would be no saving to offer, and a
// dialog that says "compact to pay less" would be lying. The warm summarizer
// reads that same transcript at cache rates; only then is the offer true.
//
// A failed compaction does NOT fail the turn. The user asked to save money, not
// to have their message dropped: the compact closure already reports the error
// through EvCompactEnd, and the turn proceeds — expensively, which is exactly
// the outcome they were trying to avoid, but not destructively.
func (a *Agent) offerCompactOnPrefixChange(ctx context.Context, compact func(reason string) error) {
	if !a.prefixGuardOn() || !a.cacheAwareCompactionOn() {
		return
	}
	asker := a.Asker
	if asker == nil {
		return
	}
	reason, tokens, ok := a.pendingPrefixChange()
	if !ok || !a.CanCompact(AutoCompactKeepTail) {
		return
	}

	compactNow := i18n.T("Compact first")
	ans, err := AskOne(ctx, asker, UserQuestion{
		Question: i18n.T(
			"Since your last message %s, so the provider's cached prompt no longer matches: the next request re-reads %s tokens at full price instead of serving them from cache. Compact the conversation first? The summary is written against the prompt that is still cached, so it costs a fraction of the re-read.",
			reason, fmtTokenCount(tokens)),
		Options: []string{compactNow, i18n.T("Send as-is")},
	})
	if err != nil || ans.Declined || ans.Answer != compactNow {
		return
	}
	_ = compact(i18n.T("prompt cache invalidated: %s", reason))
}

// fmtTokenCount renders a token count compactly (850, 12.3k, 1.2M) for
// the context-pressure note, mirroring terva_status's formatting.
func fmtTokenCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// shortErrorText trims a long http-payload error to something
// readable in a status line without dropping the most useful prefix.
func shortErrorText(msg string) string {
	msg = strings.TrimSpace(msg)
	const max = 140
	if len(msg) <= max {
		return msg
	}
	return msg[:max] + "..."
}

func containsAnyText(haystack string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}
