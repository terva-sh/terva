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

	// Don't trigger on payload-too-large; that path has its own
	// compact-and-retry handling.
	if IsPayloadTooLargeError(err) {
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
	case containsAnyText(low, "http 500", "http 502", "http 503", "http 504", " 500:", " 502:", " 503:", " 504:", "upstream connect error", "service unavailable", "internal server error", "bad gateway", "gateway timeout"):
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

// ContextFraction reports the share of the model's context window
// the last turn consumed (input + cache tokens over the catalog's
// window). Returns 0 when the window is unknown or no turn has
// landed usage yet.
func (a *Agent) ContextFraction() float64 {
	m, err := provider.FindModel("", a.Model)
	if err != nil || m.ContextWindow <= 0 {
		return 0
	}
	last := a.LastTurnUsage()
	used := last.InputTokens + last.CacheReadTokens + last.CacheWriteTokens
	if used <= 0 {
		return 0
	}
	return float64(used) / float64(m.ContextWindow)
}

// ShouldAutoCompact reports whether the transcript has grown past
// threshold (use AutoCompactThreshold for the standard policy).
func (a *Agent) ShouldAutoCompact(threshold float64) bool {
	if threshold <= 0 {
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
//   - on HTTP 413 (request bytes too large): condense and retry the
//     prompt once.
//
// Compaction progress is reported through sink as EvCompactStart /
// EvCompactEnd so streaming consumers can surface it.
func (a *Agent) PromptWithPolicy(ctx context.Context, prompt string, images []provider.ImageBlock, sink func(AgentEvent)) error {
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
		_, err := a.Compact(ctx, AutoCompactKeepTail, func(string) {})
		if errors.Is(err, ErrNothingToCompact) {
			// Nothing left to summarize — not a failure. Report a clean
			// compact_end so consumers don't surface a phantom error.
			err = nil
		}
		ev := EvCompactEnd{}
		if err != nil {
			ev.Err = err.Error()
		}
		sink(ev)
		a.EmitLifecycle(ev)
		return err
	}
	if a.ShouldAutoCompact(AutoCompactThreshold) && a.CanCompact(AutoCompactKeepTail) {
		if err := compact("context near limit"); err != nil {
			return fmt.Errorf("auto-compact before prompt: %w", err)
		}
	}
	err := a.Prompt(ctx, prompt, images, sink)
	if err != nil && ctx.Err() == nil && IsPayloadTooLargeError(err) {
		if cerr := compact("request too large; retrying"); cerr != nil {
			return err
		}
		return a.Prompt(ctx, prompt, images, sink)
	}
	return err
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
