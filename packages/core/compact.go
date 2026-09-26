package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"terva.sh/terva/packages/core/i18n"
	"terva.sh/terva/packages/core/transcriptcodec"
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
// that was just condensed. That is why the cost tracker books it total-only, why
// SetLastTurn re-baselines below, and why the durable record rides a
// "compaction" row rather than a "usage" row (session.SessionUsageDetail derives the
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
	// SupersededMessages is how many transcript messages this compaction
	// replaced.
	//
	// It exists for the provider strategy, where the checkpoint is an opaque
	// blob: the row's own messages tell a reader nothing about the size of what
	// is missing, and TokensBefore is a chars/4 estimate rather than a count of
	// turns. Recording it makes the row self-describing — "this replaced 47
	// messages, and they are still above it in the file" — which is what keeps a
	// server-compacted session auditable without paying for a second, human-
	// readable summary of the same transcript.
	SupersededMessages int
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
	//
	// It is an option beside cold rather than its replacement, because the two
	// do not necessarily produce equally good summaries. The cold path gets a
	// purpose-built summarization system prompt and a transcript framed as
	// material. The warm path asks for a summary from inside the agent's own
	// persona with its tools still advertised. A policy lists it to choose the
	// saving; the default does not.
	CompactWarm CompactStrategy = "warm"
	// CompactWarmFellBack is a warm attempt that produced nothing usable and was
	// finished by the cold one. Both were billed; the fallback RATE is a
	// first-class result of the A/B, not an error count — it directly discounts
	// the saving the warm arm appears to deliver.
	CompactWarmFellBack CompactStrategy = "warm_fallback_cold"
	// CompactProvider is the backend's own compaction: terva hands the
	// transcript to the provider and gets a replacement transcript back, instead
	// of summarizing anything itself.
	//
	// It is a different KIND of compaction from the two above, not a third
	// summarizer. Those produce prose terva can read, store, translate and
	// replay against any model. This produces an opaque blob only the issuing
	// provider can decrypt, standing in for the assistant turns it removed —
	// which is why it is gated, off by default, and why the file it writes has
	// to stay recoverable (see ReadSessionPreCompaction).
	CompactProvider CompactStrategy = "provider"
)

// providerFellBackTo names a compaction that TRIED the backend, failed, and was
// finished by one of the client-side summarizers.
//
// The name composes rather than collapsing, the way warm_fallback_cold does:
// the "provider_fallback_" prefix says the backend attempt happened and did not
// land, and the suffix says which summarizer paid for the result. Both halves
// are answers the A/B needs — how often the endpoint refuses, and what the
// refusal costs — and a single flat "it fell back" would keep neither.
func providerFellBackTo(client CompactStrategy) CompactStrategy {
	return CompactStrategy("provider_fallback_" + string(client))
}

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
//
// The strategies come from the policy's CompactRequested decision; a host
// that wants the policy's keep-tail too passes Compaction(CompactRequested).
// KeepTail.
func (a *Agent) Compact(ctx context.Context, keepTail int, sink func(delta string)) (CompactResult, error) {
	d := a.decideCompaction(CompactRequested)
	d.KeepTail = keepTail
	return a.CompactWith(ctx, d, sink)
}

// CompactWith is Compact under a decision's keep-tail and strategies, for a
// host that asked Compaction(point) itself and runs the compaction its own way,
// as the daemon does after a turn. Its Compact field is not consulted.
func (a *Agent) CompactWith(ctx context.Context, d CompactionDecision, sink func(delta string)) (CompactResult, error) {
	// Single-flight: a compaction wholesale-replaces a.messages, so it must
	// not run concurrently with a Prompt/Continue turn appending to the
	// transcript (or another Compact). Return ErrBusy instead.
	release, ok := a.acquire()
	if !ok {
		return CompactResult{}, ErrBusy
	}
	defer release()
	return a.compactHeld(ctx, d.KeepTail, sink, false, d.Strategies)
}

// ErrNoCompactionStrategy is a compaction whose policy left no strategy to
// finish it: every listed one failed or could not apply, and the cold
// summarizer, the usual last resort, was not listed.
var ErrNoCompactionStrategy = errors.New("no compaction strategy left to try")

// compactMidTurn condenses the transcript from INSIDE an active tool
// loop (runLoop's step boundary): the single-flight guard is already
// held, and the summarization prompt gains the mid-task addendum — the
// resuming agent needs a precise ledger of already-executed actions so
// it never repeats a side effect, which the idle-time format doesn't
// demand.
func (a *Agent) compactMidTurn(ctx context.Context, keepTail int, strategies []CompactStrategy) (CompactResult, error) {
	return a.compactHeld(ctx, keepTail, nil, true, strategies)
}

// compactHeld is Compact's body for callers that already hold the
// single-flight guard — the mid-turn auto-compact runs inside runLoop,
// where Prompt/Continue own the slot, so re-acquiring would deadlock
// into ErrBusy. midTurn selects the mid-task summarization addendum.
// strategies is the decision's list; see CompactionDecision.Strategies.
func (a *Agent) compactHeld(ctx context.Context, keepTail int, sink func(delta string), midTurn bool, strategies []CompactStrategy) (res CompactResult, err error) {
	if err := a.PersistenceError(); err != nil {
		return CompactResult{}, err
	}
	defer func() { err = a.withPersistenceError(err) }()
	// Summarize against the prefix the provider still has WARM — the last one
	// dispatched — not whatever the agent happens to hold now. When a host
	// swaps the model (or an extension reload rewrites the tools) the agent's
	// fields have already moved on, and the compaction that swap triggered
	// would otherwise be sent, cold and transcript-sized, to a model that has
	// never seen this conversation. See compactionPrefix.
	prefix, warm := a.compactionPrefix()
	prose := a.compactionPrompts()

	a.mu.Lock()
	msgs := append([]provider.Message(nil), a.messages...)
	readOnly := a.readOnly.Snapshot()
	// The ledger asks each call's tool to describe it. SetTools swaps the map
	// whole and never writes into it, so the reference is a snapshot.
	tools := a.tools
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
	if keepTail == len(msgs) {
		return CompactResult{}, ErrNothingToCompact
	}

	var (
		summary        string
		usage          provider.Usage
		strategy       = CompactCold
		fallbackReason string
		truncated      bool
	)

	// The backend's own compaction, when the client has one and the operator
	// asked for it.
	//
	// Tried FIRST because it subsumes both summarizers rather than competing
	// with them: it produces the checkpoint itself, so there is no summarization
	// call for the warm path to make cheap. And tried BEFORE keepTail is applied
	// — the backend decides what it keeps verbatim (measured: every user turn),
	// so terva's tail policy has nothing to say here.
	//
	// Every failure is a fallback, never an error. A compaction is usually
	// running because the conversation has already outgrown its window, which is
	// the worst possible moment to refuse one — and the client summarizers below
	// are still there, still correct, and merely more expensive.
	var providerReason string
	if strategyAllowed(strategies, CompactProvider) {
		if sc, ok := provider.ServerCompactorFor(prefix.client); ok {
			next, pres, perr := compactViaProvider(ctx, a.translator, prose, sc, prefix, msgs, readOnly, tools)
			// The attempt is billed whether or not it lands, so its spend joins
			// the total either way — a failed compaction that reported nothing
			// would hide real money in exactly the arm being evaluated.
			//
			// AddTotalOnly, not Add: this is the same invariant the summarizer
			// paths hold (drainSummary), and it matters MORE here. The endpoint
			// reads the whole transcript, so its input count is transcript-sized
			// by construction — as a context sample it would re-arm every
			// threshold at stale-high on the transcript that was just condensed,
			// and fire another compaction on the next turn.
			a.cost.AddTotalOnly(pres.Usage)
			usage = usage.Add(pres.Usage)
			if perr == nil {
				pres.Usage = usage
				a.installCompaction(next, pres)
				return pres, nil
			}
			providerReason = providerFallbackReason(perr)
		}
	}

	// Choose one boundary before summarization. The tail is an unchanged
	// suffix; every message before it feeds the summary, ledger, and counts.
	// Use the dispatched model's window, which can differ after a model swap.
	budget := 0
	if m, err := a.Catalog().FindModel("", prefix.model); err == nil {
		budget = int(float64(m.EffectiveContextWindow()) * keepTailMaxFraction)
	}
	tail := tailWithinBudget(msgs, keepTail, budget)
	summarizable := msgs[:len(msgs)-len(tail)]
	transcript := serializeTranscript(summarizable)

	// One transient-retry allowance for the WHOLE compaction, shared by both
	// summarizers. See drainSummaryRetrying for why it is shared rather than
	// per-strategy: a warm attempt that already burned the budget waiting out an
	// overloaded provider must not hand the cold fallback a fresh one to burn
	// against the same provider, at full price.
	retries := 0

	// The cache-aware path: summarize against the prefix the provider already
	// holds. Only worth attempting when a prefix was actually dispatched —
	// otherwise there is nothing warm to be aware of.
	if warm && strategyAllowed(strategies, CompactWarm) {
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
		s, u, stop, werr := a.drainSummaryRetrying(ctx, prefix.client, warmCompactRequest(prose, prefix, msgs, len(tail), midTurn), warmSink, &retries)
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
				sink("\n\n" + i18n.In(a.translator).T("[the cache-aware summarizer did not finish; retrying with the dedicated one]") + "\n\n")
			}
		}
	}

	if summary == "" && !strategyAllowed(strategies, CompactCold) {
		return CompactResult{}, ErrNoCompactionStrategy
	}
	if summary == "" {
		// The bespoke summarization prefix: its own System, no Tools, the
		// transcript flattened into one user block. It matches nothing the
		// provider has cached, by construction — every compaction on this path is
		// a full-price cold re-read of the whole conversation. That is the cost
		// cache_aware_compaction (shipped on) exists to remove; this path now
		// serves the warm arm's fallbacks and explicit opt-outs.
		s, u, stop, cerr := a.drainSummaryRetrying(ctx, prefix.client, coldCompactRequest(prose, prefix, transcript, midTurn), sink, &retries)
		usage = usage.Add(u)
		if cerr != nil {
			return CompactResult{}, cerr
		}
		if s == "" {
			return CompactResult{}, i18n.In(a.translator).Errorf("empty summary from model")
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
	// session.SessionUsageDetail subtracts this row's usage back out of the last-turn
	// delta. Report less than was spent and the context gauge inherits the
	// difference.
	// An abandoned server-side attempt is recorded on the row that DID produce
	// the checkpoint, because it has no row of its own — the compaction row is
	// written once, by whoever finished. Composing the names keeps both facts:
	// that the backend was asked and declined, and what the decline cost.
	if providerReason != "" {
		strategy = providerFellBackTo(strategy)
		fallbackReason = joinFallbackReasons(providerReason, fallbackReason)
	}

	res = CompactResult{
		Summary:            summary,
		TokensBefore:       tokensBefore,
		Usage:              usage,
		Strategy:           strategy,
		FallbackReason:     fallbackReason,
		Truncated:          truncated,
		SupersededMessages: len(summarizable),
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
	ledger := executedActionsLedger(prose, summarizable, readOnly, tools)
	if truncated {
		// Which notice depends on whether a ledger actually follows. Pointing at
		// a list of executed calls is the most useful thing the notice can say —
		// that list is the one part of the checkpoint a token limit cannot cut
		// short — but a read-only stretch produces no ledger, and a notice that
		// promises a list which is not there is worse than one that stays quiet
		// about it.
		if ledger != "" {
			body += "\n\n" + prose.TruncatedNotice
		} else {
			body += "\n\n" + prose.TruncatedNoticeBare
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

	next := make([]provider.Message, 0, 1+len(tail))
	next = append(next, synthetic)
	next = append(next, tail...)

	a.installCompaction(next, res)
	return res, nil
}

// installCompaction swaps next in as the live transcript and publishes the
// compaction: re-baseline the gauge, then fire the observer that persists the
// checkpoint.
//
// Shared by every strategy on purpose. The three steps are separable in
// appearance and not in fact — a transcript replaced without the re-baseline
// re-fires auto-compact on the condensed transcript, and one replaced without
// the observer is condensed in memory and whole on disk, so the next resume
// silently un-compacts it. A second strategy that open-coded two of the three
// would be the obvious place for that to go wrong.
func (a *Agent) installCompaction(next []provider.Message, res CompactResult) {
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
func coldCompactRequest(prose CompactionPrompts, prefix promptPrefix, transcript string, midTurn bool) provider.Request {
	instruction := prose.Instruction
	if midTurn {
		instruction += "\n\n" + prose.MidTurnAddendum
	}
	// Wrap the transcript in tags so the model treats it as material to
	// summarize, not a conversation to continue.
	prompt := "<conversation>\n" + transcript + "\n</conversation>\n\n" + instruction

	return provider.Request{
		Model:       prefix.model,
		System:      prose.System,
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
//     the tail is free, because it is already in the cache. keepTail names the
//     suffix selected before summarization. The instruction tells the model
//     how many messages will survive verbatim.
//   - transcriptcodec.RepairToolUseResultPairs is applied because oneTurn applies it, and the
//     bytes the provider cached are the REPAIRED ones. It is a no-op on a valid
//     transcript; here it is a cache-identity requirement, not a safety measure.
func warmCompactRequest(prose CompactionPrompts, prefix promptPrefix, msgs []provider.Message, keepTail int, midTurn bool) provider.Request {
	// Pair repair can append result stubs to message content. Give it separate
	// message headers so the ledger and cold fallback keep the original input.
	wireMessages := append([]provider.Message(nil), msgs...)
	return provider.Request{
		Model:            prefix.model,
		System:           prefix.system,
		Tools:            prefix.tools,
		Reasoning:        prefix.reasoning,
		ReasoningSet:     prefix.reasoningSet,
		Temperature:      prefix.temperature,
		PromptCacheKey:   prefix.cacheKey,
		MaxTokens:        compactMaxTokens,
		Messages:         transcriptcodec.RepairToolUseResultPairs(wireMessages),
		EphemeralContext: warmCompactInstruction(prose, keepTail, midTurn),
	}
}

// warmCompactInstruction is the summarization ask for the cache-aware path. It
// arrives as the last thing the model reads, from inside the agent's own
// persona and with its tools still live — so unlike the cold path, which can
// simply declare "you are a summarization assistant" in a system prompt it
// owns, this has to actively countermand the work in progress.
func warmCompactInstruction(prose CompactionPrompts, keepTail int, midTurn bool) string {
	var sb strings.Builder
	sb.WriteString(prose.WarmPreamble)
	sb.WriteString("\n\n")
	sb.WriteString(prose.Instruction)
	if midTurn {
		sb.WriteString("\n\n")
		sb.WriteString(prose.MidTurnAddendum)
	}
	if keepTail > 0 {
		sb.WriteString("\n\n")
		sb.WriteString(prose.WarmKeepTail(keepTail))
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
		if isPayloadTooLargeError(err) || isContextLengthError(err) {
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

// providerCompactionMaxShare caps how big a server-side compaction's result may
// be, as a share of what it replaced, before terva throws it away and
// summarizes client-side instead.
//
// This is not defensive programming against a hypothetical. Measured against the
// codex endpoint, the returned blob is BOUNDED (~2.1–3.5 KB across a 33x range
// of input size) but the user turns come back VERBATIM, every one of them — so
// the compaction ratio is entirely a function of how much of the transcript the
// user typed. On a 4-turn prose fixture the result was LARGER than its input;
// break-even landed around 20 turns; only past that does it win. A tool-heavy
// coding session sits far on the winning side, and a chat-heavy one does not.
//
// Without a floor, the losing case is not merely disappointing, it LOOPS:
// auto-compact fires on a context fraction, a compaction that reclaims nothing
// leaves the fraction where it was, and the next turn fires another one. The
// client summarizer is more expensive per call and bounds everything, which is
// the right trade exactly when the backend's is not paying.
const providerCompactionMaxShare = 0.8

// compactViaProvider runs one server-side compaction and assembles the
// transcript it replaces the conversation with.
//
// Returns the replacement transcript and a partly-filled CompactResult. The
// result carries Usage even on the error paths, because the call is billed
// before it can be judged unusable.
func compactViaProvider(ctx context.Context, tr i18n.Translator, prose CompactionPrompts, sc provider.ServerCompactor, prefix promptPrefix, msgs []provider.Message, readOnly *ReadOnlySet, tools Registry) ([]provider.Message, CompactResult, error) {
	// No tools, no reasoning config: the endpoint takes neither. What it does
	// take is the same model, instructions and cache key the conversation has
	// been running on, and transcriptcodec.RepairToolUseResultPairs because those are the bytes
	// the provider has already seen (see warmCompactRequest, same reasoning).
	out, usage, err := sc.CompactServerSide(ctx, provider.Request{
		Model:          prefix.model,
		System:         prefix.system,
		PromptCacheKey: prefix.cacheKey,
		Messages:       transcriptcodec.RepairToolUseResultPairs(msgs),
	})
	res := CompactResult{Usage: usage}
	if err != nil {
		return nil, res, err
	}

	// The ledger rides along here for the reason it rides along on the client
	// paths, only more so: the backend drops the assistant turns wholesale, so
	// EVERY executed tool call is inside the blob rather than the last few. It is
	// terva's own evidence of what ran — a model can omit a line from a summary,
	// and the harness cannot omit a block it recorded — and it is what stops a
	// mid-turn compaction from ending with the resuming agent re-running a side
	// effect it has already had.
	//
	// Over all of msgs, not a summarizable prefix of it: nothing survives
	// verbatim on this path, so there is no tail whose calls are still visible.
	next := append([]provider.Message(nil), out...)
	next = append(next, providerCompactionNotice(prose, executedActionsLedger(prose, msgs, readOnly, tools)))

	before, after := estimateTokens(msgs), estimateTokens(next)
	if after > int(float64(before)*providerCompactionMaxShare) {
		return nil, res, fmt.Errorf("%w: %d tokens in, %d out", errCompactionNotWorthIt, before, after)
	}

	res.Strategy = CompactProvider
	// Sized over msgs rather than over a keepTail-trimmed slice, because msgs is
	// what was actually compacted away. The number means the same thing as on
	// the other rows — how big the thing this replaced was — and is measured on
	// the set each strategy really consumed.
	res.TokensBefore = len(serializeTranscript(msgs)) / 4
	res.SupersededMessages = len(msgs)
	// Deliberately NOT a summary of the conversation. Generating one would mean
	// paying the transcript-sized read this strategy exists to avoid, purely so a
	// human could read what the model already has. The auditable copy is the
	// session file, which is append-only: the turns are still above the
	// compaction row, and ReadSessionPreCompaction reads them back.
	res.Summary = i18n.In(tr).T("The provider compacted this conversation on its side. The summary is encrypted and cannot be shown here; the original turns remain in the session file.")
	return next, res, nil
}

// errCompactionNotWorthIt marks a server-side compaction that came back too
// large to be worth keeping. A sentinel rather than a string so the fallback
// classifier can name it without matching prose.
var errCompactionNotWorthIt = errors.New("server-side compaction reclaimed too little")

// providerCompactionNotice is the divider a server-side compaction leaves in
// the transcript, carrying the executed-actions ledger when there is one.
//
// It is APPENDED, after the replacement transcript, not prepended like the
// client paths' synthetic summary. That ordering is the whole feature: the
// backend's items have to stay the exact prefix of the next request, and a
// message inserted in front of them would invalidate the cached prefix this
// strategy exists to preserve.
//
// It is written even with an empty ledger, because the transcript must say a
// compaction happened. Display surfaces render a MetaCompaction message as a
// divider rather than as a user turn, and on this path there is no other
// message to carry that mark — the blob is opaque and the user turns around it
// are genuinely the user's.
func providerCompactionNotice(prose CompactionPrompts, ledger string) provider.Message {
	body := compactionSummaryHeader + "\n\n" + prose.ProviderNotice
	if ledger != "" {
		body += "\n\n" + ledger
	}
	return provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: body}},
		Time:    time.Now(),
		Meta:    map[string]string{MetaCompaction: "true"},
	}
}

// providerFallbackReason classifies why the server-side compaction was
// abandoned, in the same terms warmFallbackReason uses and for the same reason:
// the A/B needs to separate "the endpoint refused" from "the endpoint answered
// and the answer was not worth keeping". The first is a availability ceiling,
// the second is the measured shape of the strategy (see
// providerCompactionMaxShare) and says the workload was wrong for it.
func providerFallbackReason(err error) string {
	switch {
	case errors.Is(err, errCompactionNotWorthIt):
		return "provider_reclaimed_too_little"
	case isPayloadTooLargeError(err) || isContextLengthError(err):
		return "provider_rejected_too_large"
	default:
		var pe *provider.ProviderError
		if (errors.As(err, &pe) && pe.Transient) || provider.IsTransportError(err) {
			return "provider_unavailable"
		}
		return "provider_error"
	}
}

// joinFallbackReasons keeps both halves of a two-stage fallback. Dropping
// either would make the row lie by omission: only the server-side reason says
// why the cheap path was not taken, and only the client one says why the
// expensive path then cost what it did.
func joinFallbackReasons(reasons ...string) string {
	var kept []string
	for _, r := range reasons {
		if r != "" {
			kept = append(kept, r)
		}
	}
	return strings.Join(kept, "; ")
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
	if err := a.PersistenceError(); err != nil {
		return "", provider.Usage{}, provider.StopError, err
	}
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
		// session.SessionUsageDetail subtracts CompactResult.Usage back out of the
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
			Max:      a.maxRetries,
			Delay:    delay,
			Err:      retryErrMsg(aerr),
		})
		*retries++
		if streamed && sink != nil {
			sink("\n\n" + i18n.In(a.translator).T("[the summarizer was interrupted; retrying]") + "\n\n")
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
// tools is where each call's tool is looked up, so a tool can render its own
// entry (LedgerArgsRenderer) and its own failure note (LedgerFailureNoter). A
// call whose tool is gone, or does not implement them, gets its raw arguments
// and the policy's note. tools may be nil.
//
// The lookup is by name, against the registry as it stands at compaction, not
// the tool that made the call. A transcript resumed from disk records only the
// name, so there is nothing else to look up, and the ledger has always resolved
// by name. A host that registers a different kind of tool under a name that
// earlier calls used will have those calls described by the new one.
func executedActionsLedger(prose CompactionPrompts, msgs []provider.Message, readOnly *ReadOnlySet, tools Registry) string {
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
			tool := tools[tc.Name]
			line := "- " + tc.Name + " " + clip(ledgerArgs(tool, tc.Arguments), ledgerMaxArgChars)
			switch {
			case failed[tc.ID]:
				line += "  → " + ledgerFailed(prose, tool, tc.Name)
			case !resolved[tc.ID]:
				line += "  → " + prose.LedgerUnknown
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
	sb.WriteString(prose.LedgerHeader)
	sb.WriteString("\n\n")
	if omitted > 0 {
		sb.WriteString(prose.LedgerOmitted(omitted))
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

// ledgerArgs renders one call's arguments for the ledger: the tool's own
// rendering when it has one, else the arguments as sent.
func ledgerArgs(tool Tool, args json.RawMessage) string {
	if r, ok := tool.(LedgerArgsRenderer); ok {
		return r.LedgerArgs(args)
	}
	return strings.TrimSpace(string(args))
}

// ledgerFailed is the note on a failed call: the tool's own when it has one,
// else the policy's.
func ledgerFailed(prose CompactionPrompts, tool Tool, name string) string {
	if n, ok := tool.(LedgerFailureNoter); ok {
		return n.LedgerFailed()
	}
	return prose.LedgerFailed(name)
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

// estimateTokens is the crude transcript-size heuristic used to
// re-baseline the context gauge right after compaction (1 token ≈ 4
// chars of serialized text). Only threshold checks consume it, and the
// next completed request overwrites it with provider-reported usage.
func estimateTokens(msgs []provider.Message) int {
	n := len(serializeTranscript(msgs))
	// A compaction blob serializes to a short marker, which is right for a
	// summarizer's prompt and wrong for a size estimate: it is real prompt on the
	// wire (measured at ~2.1–3.5 KB), and this number is what decides when the
	// NEXT compaction fires. Counted at its own size so a transcript whose
	// history lives entirely inside a blob does not read as nearly empty — which
	// would park the gauge at the floor and let the context grow unwatched.
	for _, m := range msgs {
		for _, c := range m.Content {
			if cb, ok := c.(provider.CompactionBlock); ok {
				n += len(cb.Encrypted)
			}
		}
	}
	return n / 4
}

// keepTailMaxFraction bounds the keep-tail by SIZE as well as by count.
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
const keepTailMaxFraction = 0.10

// tailWithinBudget picks the trailing messages to preserve verbatim: at most
// keepTail of them, and at most budget tokens' worth.
//
// It walks BACKWARD from the newest, taking whole messages while they fit, so the
// messages nearest the resumption point are the ones kept. A budget of zero (no
// context window known for the model) disables the size cap and restores the
// pure message count — today's behavior, unchanged, for anything terva can't
// measure.
//
// If a result has no matching call in the tail, advance the boundary past its
// whole message. This keeps the tail an unchanged suffix, so its complement
// includes every removed block, including text beside an orphaned result.
func tailWithinBudget(msgs []provider.Message, keepTail, budget int) []provider.Message {
	if keepTail <= 0 || len(msgs) == 0 {
		return nil
	}
	if keepTail > len(msgs) {
		keepTail = len(msgs)
	}
	cand := msgs[len(msgs)-keepTail:]
	start := 0
	if budget > 0 {
		total := 0
		start = len(cand)
		for i := len(cand) - 1; i >= 0; i-- {
			cost := estimateTokens(cand[i : i+1])
			if total+cost > budget {
				break
			}
			total += cost
			start = i
		}
	}
	// Remember call positions because moving the boundary can also remove
	// calls whose results appear later in the candidate tail.
	calls := make(map[string]int)
	for i := start; i < len(cand); i++ {
		for _, block := range cand[i].Content {
			if call, ok := block.(provider.ToolCallBlock); ok {
				calls[call.ID] = i
			}
		}
		for _, block := range cand[i].Content {
			if result, ok := block.(provider.ToolResultBlock); ok {
				if pos, found := calls[result.CallID]; !found || pos < start {
					start = i + 1
					break
				}
			}
		}
	}
	return cand[start:]
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
			case provider.CompactionBlock:
				// A marker, like an image, and for the same reason: the content
				// cannot be rendered as text. What matters is that it is rendered
				// at ALL — a transcript already compacted by the backend, being
				// summarized again for a different provider, would otherwise read
				// as though the assistant simply never spoke, and the client
				// summarizer would faithfully report a conversation with no
				// assistant in it.
				fmt.Fprintf(&sb, "[compaction summary by %s: %d bytes, not readable here]\n", v.Provider, len(v.Encrypted))
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
