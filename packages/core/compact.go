package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// ErrNothingToCompact is returned by Compact when there is nothing
// summarizable: the transcript is empty, or keepTail already covers the
// whole thing. Callers that compact opportunistically (auto-compact
// before a turn, the 413 retry) treat it as a benign no-op rather than
// a failure — there is simply nothing to do.
var ErrNothingToCompact = errors.New("nothing to compact: keep-tail covers the whole transcript")

// CompactResult reports what one compaction did and what it cost.
//
// Usage is the summarization request's own spend. It is real money and it
// belongs in the cumulative total — but it is emphatically NOT a
// context-window sample: the summarizer reads the whole transcript, so its
// input count is transcript-sized by construction. Letting it seed the
// context gauge re-arms every threshold check at stale-high on a transcript
// that was just condensed. That is why CostTracker.AddTotalOnly exists, why
// SetLastTurn re-baselines below, and why the durable record rides a
// "compaction" row rather than a "usage" row (SessionUsageDetail derives the
// gauge from usage rows alone). Three guards, one invariant: compaction spend
// is cost, never context.
type CompactResult struct {
	// Summary is the condensed transcript the synthetic message carries.
	Summary string
	// TokensBefore is the rough size of what was summarized away (the same
	// 1 tok ≈ 4 chars heuristic the compaction Meta records).
	TokensBefore int
	// Usage is the summarization call's own token spend.
	Usage provider.Usage
	// Strategy names the summarizer that actually produced Summary, and
	// FallbackReason says why the cache-aware one was abandoned when it was.
	//
	// These exist so the cache_aware_compaction A/B can be run at all. Usage
	// alone cannot tell the two arms apart after the fact, and the failure this
	// feature is most exposed to is SILENT: a warm compaction whose prefix match
	// missed looks exactly like one that hit — same summary, same transcript, no
	// error — and differs only in that the tokens were billed at full price
	// instead of a tenth. Recording the arm is what turns that into a checkable
	// claim (strategy "warm" + CacheReadTokens ≈ 0 = the cache missed) rather
	// than a comfortable assumption. See docs/plans/cache-aware-compaction-ab.md.
	Strategy       CompactStrategy
	FallbackReason string
	// Truncated reports the summarizer hit compactMaxTokens and the checkpoint
	// stops mid-thought.
	//
	// It is recorded rather than repaired because the alternative — re-running
	// the summarization at a larger cap — pays the transcript-sized read a second
	// time, and the compaction that truncates is by definition the one whose
	// transcript is largest. The cost of saying so is a sentence; the cost of
	// silence is a checkpoint that reads as complete, which is exactly the claim
	// the executed-actions ledger refuses to make about its own overflow.
	Truncated bool
}

// CompactStrategy names which summarizer produced a compaction.
type CompactStrategy string

const (
	// CompactCold is the bespoke summarizer: its own system prompt, no tools,
	// the transcript flattened into one block. Matches no cached prefix, so it
	// re-reads everything it summarizes at full price.
	CompactCold CompactStrategy = "cold"
	// CompactWarm is the cache-aware summarizer: the conversation's own prefix,
	// so the transcript is served from cache.
	CompactWarm CompactStrategy = "warm"
	// CompactWarmFellBack is a warm attempt that produced nothing usable and was
	// finished by the cold one. Both were billed; the fallback RATE is a
	// first-class result of the A/B, not an error count — it directly discounts
	// the saving the warm arm appears to deliver.
	CompactWarmFellBack CompactStrategy = "warm_fallback_cold"
)

// Compact summarizes the agent's transcript via the LLM and replaces
// it with a single synthetic user message carrying the summary. A
// small tail of recent messages is optionally preserved for continuity.
//
// keepTail is the number of most-recent messages to keep verbatim after
// the summary. 0 means summarize everything; a typical useful value is
// 4-8 (last couple of exchanges).
//
// The method blocks until the summary request completes. Emitted
// events via sink are limited to text deltas from the summary call so
// the UI can show progress.
func (a *Agent) Compact(ctx context.Context, keepTail int, sink func(delta string)) (CompactResult, error) {
	// Single-flight: Compact wholesale-replaces a.messages, so it must
	// not run concurrently with a Prompt/Continue turn appending to the
	// transcript (or another Compact). Return ErrBusy instead.
	release, ok := a.acquire()
	if !ok {
		return CompactResult{}, ErrBusy
	}
	defer release()
	return a.compactHeld(ctx, keepTail, sink, false)
}

// compactMidTurn condenses the transcript from INSIDE an active tool
// loop (runLoop's step boundary): the single-flight guard is already
// held, and the summarization prompt gains the mid-task addendum — the
// resuming agent needs a precise ledger of already-executed actions so
// it never repeats a side effect, which the idle-time format doesn't
// demand.
func (a *Agent) compactMidTurn(ctx context.Context, keepTail int) (CompactResult, error) {
	return a.compactHeld(ctx, keepTail, nil, true)
}

// compactHeld is Compact's body for callers that already hold the
// single-flight guard — the mid-turn auto-compact runs inside runLoop,
// where Prompt/Continue own the slot, so re-acquiring would deadlock
// into ErrBusy. midTurn selects the mid-task summarization addendum.
func (a *Agent) compactHeld(ctx context.Context, keepTail int, sink func(delta string), midTurn bool) (res CompactResult, err error) {
	// Summarize against the prefix the provider still has WARM — the last one
	// dispatched — not whatever the agent happens to hold now. When a host
	// swaps the model (or an extension reload rewrites the tools) the agent's
	// fields have already moved on, and the compaction that swap triggered
	// would otherwise be sent, cold and transcript-sized, to a model that has
	// never seen this conversation. See compactionPrefix.
	prefix, warm := a.compactionPrefix()

	a.mu.Lock()
	msgs := append([]provider.Message(nil), a.messages...)
	a.mu.Unlock()

	if len(msgs) == 0 {
		return CompactResult{}, ErrNothingToCompact
	}
	if keepTail < 0 {
		keepTail = 0
	}
	if keepTail > len(msgs) {
		keepTail = len(msgs)
	}
	summarizable := msgs[:len(msgs)-keepTail]
	if len(summarizable) == 0 {
		return CompactResult{}, ErrNothingToCompact
	}

	// Serialize the summarizable transcript to text. The cold path sends this as
	// its prompt; both paths size tokens_before from it, so the estimate stays
	// comparable across an A/B of the two.
	transcript := serializeTranscript(summarizable)

	var (
		summary        string
		usage          provider.Usage
		strategy       = CompactCold
		fallbackReason string
		truncated      bool
	)

	// One transient-retry allowance for the WHOLE compaction, shared by both
	// summarizers. See drainSummaryRetrying for why it is shared rather than
	// per-strategy: a warm attempt that already burned the budget waiting out an
	// overloaded provider must not hand the cold fallback a fresh one to burn
	// against the same provider, at full price.
	retries := 0

	// The cache-aware path: summarize against the prefix the provider already
	// holds. Only worth attempting when a prefix was actually dispatched —
	// otherwise there is nothing warm to be aware of.
	if warm && a.cacheAwareCompactionOn() {
		// Watch whether the warm attempt puts text in front of the user before
		// it fails. It usually won't — a model that answers with a tool_use, or
		// a request the provider rejects outright, produces no text at all — but
		// a stream that dies halfway through a summary has already been rendered
		// (ACP forwards these deltas to the editor live), and the bespoke summary
		// that follows would read as a continuation of it.
		streamed := false
		warmSink := sink
		if sink != nil {
			warmSink = func(delta string) {
				if delta != "" {
					streamed = true
				}
				sink(delta)
			}
		}

		// Anything short of usable text — AFTER the transient ladder above has run
		// its course — falls through to the bespoke path below, and both failure
		// modes are ordinary rather than exotic. The model can answer a
		// summarization ask with a tool_use — its tools are still advertised,
		// because withdrawing them would have invalidated the very cache we came
		// for. And the request can simply not fit: the warm path
		// re-sends the whole transcript PLUS the live system and tools, making it
		// slightly larger than the flattened cold prompt — so a compaction
		// triggered by a context-overflow 413 is precisely the one most likely to
		// overflow again. The fallback is not a safety net bolted onto the design;
		// it is the second half of it.
		s, u, stop, werr := a.drainSummaryRetrying(ctx, prefix.client, warmCompactRequest(prefix, msgs, keepTail, midTurn), warmSink, &retries)
		usage = usage.Add(u)
		switch {
		case werr == nil && s != "":
			summary, strategy = s, CompactWarm
			// A summary that ran into the cap is still the best checkpoint
			// available — it is most of one — so it is kept, not discarded. What
			// must not happen is keeping it SILENTLY: this branch tested only
			// "no error, some text", so a cut summary was indistinguishable from
			// a whole one.
			truncated = stop == provider.StopLength
		default:
			strategy = CompactWarmFellBack
			fallbackReason = warmFallbackReason(stop, werr)
			if streamed {
				// sink is non-nil here: streamed can only be set through warmSink.
				sink("\n\n" + i18n.T("[the cache-aware summarizer did not finish; retrying with the dedicated one]") + "\n\n")
			}
		}
	}

	if summary == "" {
		// The bespoke summarization prefix: its own System, no Tools, the
		// transcript flattened into one user block. It matches nothing the
		// provider has cached, by construction — every compaction on this path is
		// a full-price cold re-read of the whole conversation. That is the cost
		// cache_aware_compaction (shipped on) exists to remove; this path now
		// serves the warm arm's fallbacks and explicit opt-outs.
		s, u, stop, cerr := a.drainSummaryRetrying(ctx, prefix.client, coldCompactRequest(prefix, transcript, midTurn), sink, &retries)
		usage = usage.Add(u)
		if cerr != nil {
			return CompactResult{}, cerr
		}
		if s == "" {
			return CompactResult{}, i18n.Errorf("empty summary from model")
		}
		summary = s
		// The cold path discarded its stop reason entirely. It is the FALLBACK,
		// so it runs for the compactions that already went wrong once — the
		// least safe place to lose the signal that this one ended early too.
		truncated = stop == provider.StopLength
	}

	// Estimate token count before compaction (rough: 1 token ~ 4 chars).
	tokensBefore := len(transcript) / 4
	// usage is the SUM of every attempt, including a warm one that fell back.
	// It has to be: a.cost folded each attempt into the cumulative total, and
	// SessionUsageDetail subtracts this row's usage back out of the last-turn
	// delta. Report less than was spent and the context gauge inherits the
	// difference.
	res = CompactResult{
		Summary:        summary,
		TokensBefore:   tokensBefore,
		Usage:          usage,
		Strategy:       strategy,
		FallbackReason: fallbackReason,
		Truncated:      truncated,
	}

	// Replace transcript: one synthetic user message with the summary, the
	// harness's own record of what was executed, and then the preserved tail.
	//
	// The ledger is appended AFTER the model's summary rather than folded into
	// it, and it says so: the summary is an account, this is evidence. Where they
	// disagree about whether something ran, this wins — a model can forget a line
	// it was asked to write, and the transcript cannot forget a block it contains.
	body := "## Context Summary (compacted)\n\n" + summary
	// Say it in the transcript, not only on the row. The row serves forensics
	// after the fact; this serves the model that has to work from a checkpoint
	// which stops mid-sentence, and which would otherwise read the last complete
	// thought it can see as the end of the account.
	ledger := executedActionsLedger(summarizable, a.ReadOnly, a.CWD)
	if truncated {
		// Which notice depends on whether a ledger actually follows. Pointing at
		// a list of executed calls is the most useful thing the notice can say —
		// that list is the one part of the checkpoint a token limit cannot cut
		// short — but a read-only stretch produces no ledger, and a notice that
		// promises a list which is not there is worse than one that stays quiet
		// about it.
		if ledger != "" {
			body += "\n\n" + i18n.P("compact.truncated", compactTruncatedNotice)
		} else {
			body += "\n\n" + i18n.P("compact.truncated.noledger", compactTruncatedNoticeBare)
		}
	}
	if ledger != "" {
		body += "\n\n" + ledger
	}
	synthetic := provider.Message{
		Role: provider.RoleUser,
		Content: []provider.Content{
			provider.TextBlock{Text: body},
		},
		Time: time.Now(),
		Meta: map[string]string{
			MetaCompaction:   "true",
			MetaTokensBefore: strconv.Itoa(tokensBefore),
		},
	}

	// The tail: at most keepTail messages, and at most a fraction of the context
	// window (see KeepTailMaxFraction — a fixed message count alone lets two
	// whole-file reads survive a compaction and reclaim nothing). Orphaned
	// tool_result blocks, whose matching tool_use was summarized away, are
	// removed: Anthropic rejects a transcript where a tool_result references a
	// tool_use id that doesn't exist.
	// Budget against the prefix's model — the one being compacted ON — rather
	// than whatever the agent holds now. After a /model switch the two differ,
	// and the window that matters is the one the summary was written against.
	// It also reads no agent field, so it cannot race a concurrent swap.
	budget := 0
	if m, err := provider.FindModel("", prefix.model); err == nil {
		budget = int(float64(m.EffectiveContextWindow()) * KeepTailMaxFraction)
	}
	tail := tailWithinBudget(msgs, keepTail, budget)

	next := make([]provider.Message, 0, 1+len(tail))
	next = append(next, synthetic)
	next = append(next, tail...)

	a.mu.Lock()
	a.messages = next
	a.rev++
	a.transcriptEpoch++
	persisted := append([]provider.Message(nil), next...)
	a.mu.Unlock()

	// Re-baseline the context gauge. LastTurnUsage still reflects the
	// pre-compaction request, so every fraction-driven policy check
	// (pre-turn, post-turn, mid-turn) would read stale-high until the
	// next request lands usage — and re-fire a pointless compaction on
	// the already-condensed transcript. Seed a rough estimate (the same
	// 1 token ≈ 4 chars heuristic as tokens_before); the next real
	// request corrects it.
	a.cost.SetLastTurn(provider.Usage{InputTokens: estimateTokens(next)})

	// Fired outside a.mu, like every other observer emit. The result rides
	// along so the durable checkpoint can record what the compaction cost —
	// on the compaction row, NOT a usage row (see CompactResult).
	a.fireTranscriptCompacted(persisted, res)

	return res, nil
}

// compactMaxTokens caps a summary. A truncated checkpoint is worse than a
// slightly expensive one, and output tokens are a rounding error next to the
// transcript-sized read that precedes them.
//
// 4096 was called generous and was not. It caps REASONING PLUS ANSWER, not the
// answer, and an adaptive-thinking model (Opus 4.7+) is sent no explicit
// thinking budget at all — it spends the cap however it likes and the summary
// gets the remainder. Measured on a dogfooded session: three of ten compactions
// reported exactly 4096 output tokens and ended mid-word, one of them mid-`**`,
// while carrying only ~10k characters of prose. About 2,500 tokens of summary,
// about 1,600 of thinking, and the checkpoint cut where the two collided.
//
// Nothing about the old value came from a provider limit. The models this bites
// advertise MaxOutput 128000, and both the anthropic and openai builders already
// clamp max_tokens down to what a model actually accepts, so a larger number
// here cannot produce a rejected request — only a longer summary when the model
// wants one. Sized to leave the observed ~2,500-token summary intact behind a
// thinking budget several times the observed ~1,600.
const compactMaxTokens = 16384

// coldCompactRequest builds the bespoke summarization request: a purpose-built
// system prompt, no tools, and the transcript flattened into a single user
// block. Nothing about it matches the conversation's cached prefix, so it is a
// full-price read of everything it summarizes — but it is also the most
// controlled framing available (the model is told, by its system prompt, that
// it is a summarizer and not an agent), which is why it stays the default and
// the fallback.
func coldCompactRequest(prefix promptPrefix, transcript string, midTurn bool) provider.Request {
	instruction := i18n.P("compact.instruction", compactionPrompt)
	if midTurn {
		instruction += "\n\n" + i18n.P("compact.instruction.midturn", midTurnCompactionAddendum)
	}
	// Wrap the transcript in tags so the model treats it as material to
	// summarize, not a conversation to continue.
	prompt := "<conversation>\n" + transcript + "\n</conversation>\n\n" + instruction

	return provider.Request{
		Model:       prefix.model,
		System:      i18n.P("compact.system", summarizationSystem),
		MaxTokens:   compactMaxTokens,
		Temperature: prefix.temperature,
		Messages: []provider.Message{
			{
				Role:    provider.RoleUser,
				Content: []provider.Content{provider.TextBlock{Text: prompt}},
				Time:    time.Now(),
			},
		},
	}
}

// warmCompactRequest builds the cache-aware summarization request: the SAME
// model, system, tools, thinking config and cache route the conversation has
// been running on, the SAME transcript it has been accumulating — and the
// summarization ask appended as ephemeral context.
//
// Every deviation from the live request is a byte the provider's prefix match
// would trip over, so the discipline here is to deviate as little as possible:
//
//   - The instruction rides EphemeralContext, a trailing block AFTER the cache
//     breakpoint carrying no cache_control. Asking for the summary costs a few
//     hundred uncached tokens and leaves the transcript behind it hitting cache.
//     Putting it in System instead would invalidate the system AND message
//     tiers — the request would cost precisely what it set out to save.
//   - The tools stay advertised. Withdrawing them is the obvious "we don't need
//     tools to summarize" optimization and it is a trap: the tools array is the
//     FIRST thing rendered, so changing it invalidates system and messages too.
//     A live tool list is the price of a warm transcript, and the model
//     answering with a tool_use anyway is what the fallback is for.
//   - The transcript is NOT pre-sliced to drop keepTail. Truncating it moves the
//     cache breakpoint and hands the provider a prefix it never cached; keeping
//     the tail is free, because it is already in the cache. keepTail is applied
//     locally afterward, and the model is simply told which messages will
//     survive verbatim.
//   - repairToolUseResultPairs is applied because oneTurn applies it, and the
//     bytes the provider cached are the REPAIRED ones. It is a no-op on a valid
//     transcript; here it is a cache-identity requirement, not a safety measure.
func warmCompactRequest(prefix promptPrefix, msgs []provider.Message, keepTail int, midTurn bool) provider.Request {
	return provider.Request{
		Model:            prefix.model,
		System:           prefix.system,
		Tools:            prefix.tools,
		Reasoning:        prefix.reasoning,
		ReasoningSet:     prefix.reasoningSet,
		Temperature:      prefix.temperature,
		PromptCacheKey:   prefix.cacheKey,
		MaxTokens:        compactMaxTokens,
		Messages:         repairToolUseResultPairs(msgs),
		EphemeralContext: warmCompactInstruction(keepTail, midTurn),
	}
}

// warmCompactInstruction is the summarization ask for the cache-aware path. It
// arrives as the last thing the model reads, from inside the agent's own
// persona and with its tools still live — so unlike the cold path, which can
// simply declare "you are a summarization assistant" in a system prompt it
// owns, this has to actively countermand the work in progress.
func warmCompactInstruction(keepTail int, midTurn bool) string {
	var sb strings.Builder
	sb.WriteString(i18n.P("compact.warm.preamble", warmCompactionPreamble))
	sb.WriteString("\n\n")
	sb.WriteString(i18n.P("compact.instruction", compactionPrompt))
	if midTurn {
		sb.WriteString("\n\n")
		sb.WriteString(i18n.P("compact.instruction.midturn", midTurnCompactionAddendum))
	}
	if keepTail > 0 {
		sb.WriteString("\n\n")
		sb.WriteString(i18n.P("compact.warm.keeptail",
			"terva keeps the %d most recent messages above verbatim alongside your summary. Account for them, but do not reproduce them at length.", keepTail))
	}
	return sb.String()
}

// warmFallbackReason classifies why the cache-aware attempt was abandoned, in
// terms the A/B can group by. The distinction matters: "tool_use" is a PROMPTING
// failure — the model was asked to summarize with its tools live and chose to
// use one, which better instructions might fix — while "rejected" is a SIZE
// failure, structural to a warm request being larger than a cold one, which
// better instructions cannot fix. A high rate of the first is a bug to work on;
// a high rate of the second is a ceiling on how often the feature can pay off.
func warmFallbackReason(stop provider.StopReason, err error) string {
	switch {
	case err != nil:
		if IsPayloadTooLargeError(err) || IsContextLengthError(err) {
			return "rejected_too_large"
		}
		// The transient ladder is already exhausted by the time this runs (see
		// drainSummaryRetrying), so an error that is STILL transient means the
		// provider was down for the whole of it. That is a fact about the
		// provider, not about the cache-aware strategy, and bucketing it as
		// "error" would read in the A/B as the warm path failing.
		var pe *provider.ProviderError
		if (errors.As(err, &pe) && pe.Transient) || provider.IsTransportError(err) {
			return "provider_unavailable"
		}
		return "error"
	case stop == provider.StopToolUse:
		return "tool_use"
	default:
		return "empty_summary"
	}
}

// drainSummary runs one summarization request to completion and returns the
// text it produced. Shared by both paths so their cost accounting cannot drift:
// every attempt folds its spend into the session total here, exactly once.
//
// Spend is added total-only. The last-turn snapshot is the CONTEXT gauge, which
// compactHeld re-baselines below; a summarization request is transcript-sized
// by construction, so letting it land there would re-arm every threshold check
// at stale-high on a transcript that was just condensed.
//
// The stream is drained even after an error rather than returned from early, so
// the provider's goroutine always runs to completion.
func (a *Agent) drainSummary(ctx context.Context, client provider.Client, req provider.Request, sink func(delta string)) (summary string, usage provider.Usage, stop provider.StopReason, err error) {
	stream, serr := client.Stream(ctx, req)
	if serr != nil {
		return "", provider.Usage{}, provider.StopError, serr
	}

	var sb strings.Builder
	stop = provider.StopEnd
	for ev := range stream {
		switch e := ev.(type) {
		case provider.EventTextDelta:
			sb.WriteString(e.Delta)
			if sink != nil {
				sink(e.Delta)
			}
		case provider.EventUsage:
			// Assign, don't accumulate: every provider emits exactly one
			// EventUsage per request (they fold their own cumulative
			// message_start / message_delta refreshes internally).
			usage = e.Usage
			a.cost.AddTotalOnly(e.Usage)
		case provider.EventDone:
			stop = e.Stop
			if e.Err != nil {
				err = e.Err
			}
		}
	}
	return strings.TrimSpace(sb.String()), usage, stop, err
}

// drainSummaryRetrying is drainSummary wrapped in the turn loop's
// transient-failure ladder — the SAME canRetryError / retryDelay pair runLoop
// uses, so the two cannot drift about what counts as transient or how long to
// wait for it.
//
// It exists because compaction had no ladder at all, and the gap was worst
// exactly where it hurt most. A provider overload ("Our servers are currently
// overloaded. Please try again later.") reaches us as an in-stream error frame,
// which is past the point doStreamWithRetry covers — that one retries only
// failures that happen before the response headers. So the single class of
// failure the wire protocol itself labels transient was the one class nothing
// retried, and it killed compactions on the first try. The result is the worst
// available outcome: the transcript stays at full size precisely when it was
// too big, which is how a session ends up wedged against the context limit with
// no way back down.
//
// retries is the allowance SHARED across a compaction's warm and cold attempts.
// It counts retries actually TAKEN rather than attempts made, and that
// distinction is what makes sharing safe: a warm attempt that fails
// non-transiently — a tool_use answer, an oversize rejection — spends nothing,
// so the cold fallback still gets the full budget it would have had.
func (a *Agent) drainSummaryRetrying(ctx context.Context, client provider.Client, req provider.Request, sink func(delta string), retries *int) (summary string, usage provider.Usage, stop provider.StopReason, err error) {
	for {
		// Per-attempt, and deliberately distinct from the caller's own streamed
		// flag: a stream that dies mid-summary has already put text in front of
		// the user, and the next attempt's summary would read as a continuation
		// of the abandoned one.
		streamed := false
		attemptSink := sink
		if sink != nil {
			attemptSink = func(delta string) {
				if delta != "" {
					streamed = true
				}
				sink(delta)
			}
		}

		s, u, st, aerr := a.drainSummary(ctx, client, req, attemptSink)
		// Accumulate across attempts, abandoned ones included. a.cost has
		// already folded each attempt into the cumulative total, and
		// SessionUsageDetail subtracts CompactResult.Usage back out of the
		// last-turn delta — so under-reporting here hands the difference to the
		// context gauge as phantom turn spend.
		usage = usage.Add(u)
		summary, stop, err = s, st, aerr

		if aerr == nil || !a.canRetryError(aerr, *retries) {
			return summary, usage, stop, err
		}
		delay := a.retryDelay(*retries, aerr)
		// The compaction ladder's half of the retry record. It has no event
		// sink to emit EvRetry on — only the text sink it is streaming the
		// summary into — so the observer is the whole of its visibility, and
		// without it a compaction that waits out a two-minute outage is
		// indistinguishable from one that was mysteriously slow. Phase is what
		// separates these from turn retries in the log: each of these carries
		// the entire transcript.
		a.fireRetry(RetryRecord{
			Phase:    RetryPhaseCompaction,
			Provider: providerOf(aerr),
			Attempt:  *retries + 1, // 1-based, matching the turn ladder
			Max:      a.MaxRetries,
			Delay:    delay,
			Err:      retryErrMsg(aerr),
		})
		*retries++
		if streamed && sink != nil {
			sink("\n\n" + i18n.T("[the summarizer was interrupted; retrying]") + "\n\n")
		}
		if sleepErr := sleepRetry(ctx, delay); sleepErr != nil {
			// Cancelled while backing off. Report the cancellation rather than
			// the provider error it was waiting out: the turn is over either
			// way, and callers key off context.Canceled to stay quiet about it.
			return summary, usage, stop, sleepErr
		}
	}
}

// The executed-actions ledger: a deterministic record of the state-changing tool
// calls a compaction is about to discard, extracted from the transcript rather
// than asked of a model.
//
// It exists because of an arithmetic fact. A tool step is exactly two messages
// (one assistant carrying the batch of tool_use, one tool message carrying the
// batch of tool_result), and AutoCompactKeepTail is 4 — so a compaction keeps the
// last TWO STEPS verbatim, a fixed number, while an agentic loop grows without
// bound. In a 50-step loop, 97 of 101 messages are summarized away and not one
// state-changing call survives verbatim. Every side effect the agent has caused
// reaches its resumed self ONLY through prose a model chose to write.
//
// midTurnCompactionAddendum asks the model for exactly this list, and asking is
// the weakest link in the chain: a forgotten line means the resuming agent
// re-runs `npm install`, re-applies a migration, re-sends a message. But the
// calls are right there in the transcript as structured blocks — name, exact
// arguments, and (via their results) whether they actually succeeded. Extracting
// them cannot forget. So the model's prose becomes a summary, and this becomes
// the record.
//
// BOUNDED, deliberately: the ledger rides in the compacted transcript forever
// after, so an unbounded one would spend the context the compaction just
// reclaimed. Identical calls collapse with a count, arguments are clipped, and an
// overflow is stated out loud rather than silently truncated.
const (
	// ledgerMaxEntries caps distinct actions. Sized from a real dogfood session
	// that ran 88 distinct state-changing calls before its first compaction — the
	// original 40 dropped 55% of them to prose-only. At ~45 tokens per clipped
	// line, 100 entries is ~4.5k tokens, negligible against the tens-of-thousands
	// a compaction reclaims, and it keeps the whole record deterministic rather
	// than leaning on the model's prose for the overflow. The overflow notice
	// still fires past this, because "a lot" and "unbounded" are different
	// promises.
	ledgerMaxEntries = 100
	// ledgerMaxArgChars clips each call's arguments. Enough to identify WHICH
	// file was written or WHICH command ran, which is all the resuming agent
	// needs to recognize it and not do it again.
	ledgerMaxArgChars = 160
)

// executedActionsLedger renders the state-changing calls in msgs. Empty when
// there are none — a read-only stretch of conversation needs no ledger.
//
// readOnly may be nil, and then every tool counts as state-changing. That is the
// safe direction: over-reporting costs tokens, under-reporting invites a repeated
// side effect.
//
// cwd is the directory bash commands run in, used only to recognize a shell
// preamble that identifies nothing (see ledgerArgs). Empty elides nothing.
func executedActionsLedger(msgs []provider.Message, readOnly *ReadOnlySet, cwd string) string {
	// Pair each call with its outcome. A FAILED call is the case that matters
	// most and is the easiest to get backwards: its effect does NOT exist, so an
	// agent told only "you already ran this" would skip work it still has to do.
	// A call with no result at all was dispatched into the dark — compaction can
	// land between the call and its result — and neither claim is safe there.
	failed, resolved := map[string]bool{}, map[string]bool{}
	for _, m := range msgs {
		for _, c := range m.Content {
			if tr, ok := c.(provider.ToolResultBlock); ok {
				resolved[tr.CallID] = true
				if tr.IsError {
					failed[tr.CallID] = true
				}
			}
		}
	}

	var order []string
	counts := map[string]int{}
	for _, m := range msgs {
		if m.Role != provider.RoleAssistant {
			continue
		}
		for _, c := range m.Content {
			tc, ok := c.(provider.ToolCallBlock)
			if !ok || readOnly.Has(tc.Name) {
				continue
			}
			line := "- " + tc.Name + " " + clip(ledgerArgs(tc.Name, tc.Arguments, cwd), ledgerMaxArgChars)
			switch {
			case failed[tc.ID]:
				line += "  → " + failureNote(tc.Name)
			case !resolved[tc.ID]:
				line += "  → OUTCOME UNKNOWN (dispatched, no result recorded)"
			}
			if counts[line] == 0 {
				order = append(order, line)
			}
			counts[line]++
		}
	}
	if len(order) == 0 {
		return ""
	}

	// Overflow keeps the MOST RECENT, which are the likeliest to be re-attempted
	// immediately on resumption — and says how many it dropped. A silent cap here
	// would read as "this is everything", which is the one thing the ledger must
	// never claim falsely.
	var omitted int
	if len(order) > ledgerMaxEntries {
		omitted = len(order) - ledgerMaxEntries
		order = order[len(order)-ledgerMaxEntries:]
	}

	var sb strings.Builder
	sb.WriteString(i18n.P("compact.ledger.header", executedActionsHeader))
	sb.WriteString("\n\n")
	if omitted > 0 {
		sb.WriteString(i18n.P("compact.ledger.omitted",
			"(this list omits %d earlier calls that changed state. The summary above is the only record of those.)\n\n",
			omitted))
	}
	for _, line := range order {
		sb.WriteString(line)
		if n := counts[line]; n > 1 {
			fmt.Fprintf(&sb, "  (x%d)", n)
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// failureNote describes what a failed call's failure actually implies about its
// effects. For every ordinary tool that is "nothing happened": an edit that
// fails writes no bytes, and telling the resuming agent it already ran would
// make it skip work it still has to do.
//
// A SHELL COMMAND IS NOT ONE ACTION. Its result is the exit status of the last
// stage, and a command that failed can have changed the workspace several times
// before it got there. Measured on a dogfooded session: 12 bash calls were
// marked failed in compaction ledgers and ALL TWELVE were composite — six of
// them `gofmt -w <file> && go test <pkg>`, where the rewrite certainly happened
// and only the test that followed failed. The ledger told the resuming agent
// that its effect did not exist.
//
// That is the same wrong claim the ledger exists to prevent, pointed the other
// way, so the note says what the harness actually knows: a non-zero exit, and
// an unknown amount of work already done.
func failureNote(tool string) string {
	if tool == "bash" {
		return i18n.T("FAILED (non-zero exit; earlier stages of the command may still have taken effect)")
	}
	return i18n.T("FAILED (its effect does NOT exist; it may still need doing)")
}

// ledgerArgs renders one call's arguments for the ledger, eliding the shell
// preamble that identifies nothing.
//
// A model that believes a shell's working directory carries over between calls
// re-anchors every command with `cd <cwd>`. It does not carry over — BashTool
// sets cmd.Dir on every call — so that `cd` is a no-op, and so is a leading
// `set +e` (the tool runs `sh -c`, which starts with errexit already off).
// Measured on a dogfooded session: 1,090 of 1,112 `cd`s pointed at the agent's
// own cwd, and the preamble averaged 92 of the 160 characters below, so more
// than half of this ledger's identification budget was spent on bytes that say
// nothing about WHICH command ran. The clip then fell inside the boilerplate and
// the entry named no command at all.
//
// Only a `cd` to cwd is elided; `cd /tmp/build && make` changes WHERE the work
// happened and survives whole. Anything unparseable, unrecognized, or merely
// prefix-similar (`cd /srv/app2` against /srv/app) is left exactly as sent —
// under-eliding costs characters, over-eliding would misreport the action.
func ledgerArgs(name string, args json.RawMessage, cwd string) string {
	raw := strings.TrimSpace(string(args))
	if name != "bash" || cwd == "" {
		return raw
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return raw
	}
	cmd, ok := m["command"].(string)
	if !ok {
		return raw
	}
	stripped := stripShellPreamble(cmd, cwd)
	if stripped == cmd {
		return raw
	}
	m["command"] = stripped
	// Without SetEscapeHTML(false) the encoder writes `&&` as `&&`,
	// spending 12 of the 160 characters below on a two-character operator. Shell
	// commands are chained with `&&`, so a handful per entry pushed the clip back
	// past the preamble elision it is paired with here. The ledger is prose the
	// model reads, never JSON anything parses, so the plain byte is strictly
	// better. A trailing newline is the encoder's, not ours.
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return raw
	}
	return strings.TrimRight(buf.String(), "\n")
}

// stripShellPreamble removes every leading no-op statement from cmd. It loops
// because the two forms appear in either order and in either combination.
func stripShellPreamble(cmd, dir string) string {
	for {
		rest, cut := cutShellNoop(cmd, dir)
		if !cut {
			return cmd
		}
		cmd = rest
	}
}

// cutShellNoop strips ONE leading no-op statement together with the separator
// that ends it, reporting whether it found one.
//
// The separator is required. Without it the match was not a whole statement —
// `cd /srv/app2` shares a prefix with /srv/app and `set +export` with `set +e` —
// and stripping either would rewrite the command into something that never ran.
func cutShellNoop(cmd, dir string) (string, bool) {
	s := strings.TrimLeft(cmd, " \t\r\n")
	n := noopStatementLen(s, dir)
	if n == 0 {
		return cmd, false
	}
	rest := strings.TrimLeft(s[n:], " \t")
	switch {
	case strings.HasPrefix(rest, "&&"):
		rest = rest[2:]
	case strings.HasPrefix(rest, ";"), strings.HasPrefix(rest, "\n"):
		rest = rest[1:]
	default:
		return cmd, false
	}
	return strings.TrimLeft(rest, " \t\r\n"), true
}

// noopStatementLen returns the byte length of a leading no-op statement in s, or
// 0. A `cd` counts only when its argument is exactly dir, bare or quoted; a
// trailing slash does not match, which under-elides rather than guessing.
func noopStatementLen(s, dir string) int {
	if strings.HasPrefix(s, "set +e") {
		return len("set +e")
	}
	const cd = "cd "
	if !strings.HasPrefix(s, cd) {
		return 0
	}
	arg := strings.TrimLeft(s[len(cd):], " \t")
	off := len(s) - len(arg)
	for _, cand := range []string{dir, `"` + dir + `"`, `'` + dir + `'`} {
		if strings.HasPrefix(arg, cand) {
			return off + len(cand)
		}
	}
	return 0
}

// clip truncates on a rune boundary so a multi-byte argument can't be cut in
// half into invalid UTF-8.
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// compactTruncatedNotice rides the checkpoint when the summarizer ran into
// compactMaxTokens. It points at the ledger deliberately: the one part of the
// checkpoint a token limit cannot cut short is the part the harness appends
// after generation.
const compactTruncatedNotice = `**The summary above stops early.** The model reached its output limit before it
completed the account. The last section breaks off, and an earlier section can
omit a fact. The list of tool calls below still records every call. Check the
workspace, or ask the user. A gap in the summary is not proof that something did
not happen.`

// compactTruncatedNoticeBare is the same notice for a compaction that produced
// no ledger, and so has no list to point the model at.
const compactTruncatedNoticeBare = `**The summary above stops early.** The model reached its output limit before it
completed the account. The last section breaks off, and an earlier section can
omit a fact. Check the workspace, or ask the user. A gap in the summary is not
proof that something did not happen.`

const executedActionsHeader = `## Executed Tool Calls (authoritative harness record)

The harness extracted this list from the transcript. No model wrote it. Every
call below already ran, and its effects already exist, unless the entry says
otherwise. Do not repeat any of them.

A shell command can start with ` + "`cd`" + ` into the directory it already runs in, or
with ` + "`set +e`" + `. The harness removes both from the entry. Neither one changes
what the command does.`

// estimateTokens is the crude transcript-size heuristic used to
// re-baseline the context gauge right after compaction (1 token ≈ 4
// chars of serialized text). Only threshold checks consume it, and the
// next completed request overwrites it with provider-reported usage.
func estimateTokens(msgs []provider.Message) int {
	return len(serializeTranscript(msgs)) / 4
}

// KeepTailMaxFraction bounds the keep-tail by SIZE as well as by count.
//
// A message count is the wrong unit on its own, and the gap is four orders of
// magnitude: an `ls` result is 20 tokens, a whole-file read is 40k. keepTail is 4
// messages — the last two tool steps — so when those two steps happen to be file
// reads, compaction faithfully preserves the very thing that blew the context.
//
// Measured on a transcript of 38 small steps followed by two whole-file reads:
// 83,577 tokens before, 83,526 after. It reclaimed 0.1%. The tail alone was
// 82,547 tokens.
//
// That is not merely ineffective, it is unrecoverable. Auto-compact fires at 85%,
// reclaims nothing, and the context keeps growing until a request is rejected as
// too large; PromptWithPolicy then compacts and retries exactly once; that
// compaction also reclaims nothing; the retry is rejected again and the error
// surfaces. The session cannot continue without /clear — and "read two files,
// then edit them" is the most ordinary agentic pattern there is.
//
// So the tail is capped at a fraction of the context window as well as at
// keepTail messages. In the common case (small results) the token cap is nowhere
// near binding and behavior is exactly as before; only a tail that would defeat
// the compaction is trimmed. Dropping an oversized read is safe in a way that
// dropping a write would not be: reads are idempotent, the model can simply read
// it again, and the summary records what was learned from it.
const KeepTailMaxFraction = 0.10

// tailWithinBudget picks the trailing messages to preserve verbatim: at most
// keepTail of them, and at most budget tokens' worth.
//
// It walks BACKWARD from the newest, taking whole messages while they fit, so the
// messages nearest the resumption point are the ones kept. A budget of zero (no
// context window known for the model) disables the size cap and restores the
// pure message count — today's behavior, unchanged, for anything terva can't
// measure.
//
// The result is passed through repairOrphanedToolResults, which is what makes
// the step boundary safe: if the budget affords a tool message but not the
// assistant tool_use that produced it, the orphaned result is dropped rather
// than sent to a provider that would reject it.
func tailWithinBudget(msgs []provider.Message, keepTail, budget int) []provider.Message {
	if keepTail <= 0 || len(msgs) == 0 {
		return nil
	}
	if keepTail > len(msgs) {
		keepTail = len(msgs)
	}
	cand := msgs[len(msgs)-keepTail:]
	if budget <= 0 {
		return repairOrphanedToolResults(cand)
	}

	total, start := 0, len(cand)
	for i := len(cand) - 1; i >= 0; i-- {
		cost := estimateTokens(cand[i : i+1])
		if total+cost > budget {
			break
		}
		total += cost
		start = i
	}
	return repairOrphanedToolResults(cand[start:])
}

// repairOrphanedToolResults removes tool_result content blocks (and
// entire messages that become empty) when the matching tool_use ID
// does not appear anywhere in the given messages. This happens after
// compaction when the tail preserves a tool_result but the tool_use
// that produced it was summarized away.
func repairOrphanedToolResults(msgs []provider.Message) []provider.Message {
	return provider.RepairOrphanedToolResults(msgs)
}

// serializeTranscript renders a list of provider.Message into a plain
// text transcript the summarization model can read without trying to
// continue the conversation.
func serializeTranscript(msgs []provider.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		// Skip tool-image mirror messages: they are a provider-wire
		// artifact (a synthetic user turn that re-sends a tool result's
		// images on providers that can't carry them inline). Their text
		// is a fixed prefix and their images can't be summarized, so
		// feeding them to the summarizer only adds a phantom user turn.
		if IsToolImageMirror(m) {
			continue
		}
		switch m.Role {
		case provider.RoleUser:
			sb.WriteString("\n--- user ---\n")
		case provider.RoleAssistant:
			sb.WriteString("\n--- assistant ---\n")
		case provider.RoleTool:
			sb.WriteString("\n--- tool ---\n")
		}
		for _, c := range m.Content {
			switch v := c.(type) {
			case provider.TextBlock:
				sb.WriteString(v.Text)
				sb.WriteString("\n")
			case provider.ImageBlock:
				fmt.Fprintf(&sb, "[image: %s, %d bytes]\n", v.MimeType, len(v.Data))
			case provider.ToolCallBlock:
				fmt.Fprintf(&sb, "[tool_call %s %s]\n", v.Name, string(v.Arguments))
			case provider.ToolResultBlock:
				// Mark failures. IsError used to be dropped here, which made a
				// tool_result that FAILED serialize identically to one that
				// succeeded — so the summarizer could only tell them apart by
				// reading the prose, and a terse failure ("ENOENT: no such file")
				// contains no word it could key on.
				//
				// That is not cosmetic in a mid-turn compaction. keepTail is 4
				// messages — exactly two tool steps — so in a long agentic loop
				// essentially every executed action reaches the resuming agent ONLY
				// through this summary. Reporting an aborted command as a completed
				// one is precisely how a resumed agent decides a side effect is
				// already done when it isn't, or re-runs one that is.
				//
				// (The cache-aware path never had this problem: it sends the native
				// tool_result blocks, and the provider serializes is_error itself.)
				tag := "[tool_result] "
				if v.IsError {
					tag = "[tool_result ERROR] "
				}
				for _, inner := range v.Content {
					switch iv := inner.(type) {
					case provider.TextBlock:
						sb.WriteString(tag)
						sb.WriteString(iv.Text)
						sb.WriteString("\n")
					case provider.ImageBlock:
						// An image result can't be summarized, but its EXISTENCE is
						// evidence the call ran. Dropping the block silently made a
						// screenshot-producing step look like it never happened.
						fmt.Fprintf(&sb, "%s[image: %s, %d bytes]\n", tag, iv.MimeType, len(iv.Data))
					}
				}
			}
		}
	}
	return sb.String()
}

const summarizationSystem = `Do not continue the conversation. Do not answer any question in the conversation. Output only the structured summary.

You are a context summarization assistant. Read the conversation between the user and a coding assistant. Then produce a structured summary in the exact format that the instruction gives.`

// warmCompactionPreamble opens the cache-aware summarization ask. The cold path
// gets to say "you are a summarization assistant" in a system prompt it owns;
// this one has to say it in a trailing user message, against a system prompt
// that says the model is a coding agent and a tools array that is still live.
// So it countermands explicitly, and it says why the conversation is about to
// vanish — a model that understands the summary IS the surviving context writes
// a better one.
const warmCompactionPreamble = `[compaction] Stop. Do not continue the task above, and do not call any tool.

The summary you write now replaces the conversation above, and terva then discards that conversation. Nothing else survives. Your summary is the only context that the next model, probably you, will have to continue this work from. Write it with that in mind.`

const compactionPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that the next model will use to continue the work.

Use this format exactly:

## Goal
[What does the user want to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements that the user gave]
- [Or "(none)" if the user gave none]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues that block progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const midTurnCompactionAddendum = `This summary interrupts an agent in the middle of a task. The conversation is inside an active tool-use loop. After your summary, the agent resumes directly from the most recent tool results, which terva keeps verbatim. Add one extra section, and make it exhaustive:

## Actions Already Executed
- [Every action that changed state: files created, edited, or deleted (exact paths). Commands run (the exact command and its outcome). Messages sent. Sub-agents started. The agent that resumes must never repeat one of these. Any ambiguity here causes duplicated side effects.]

Under In Progress, record the precise current step: what the agent was about to do next. Include exact file paths, line numbers, symbol names, and any error text the agent responded to. Do not restate large file contents. Name the file and the relevant location instead.`
