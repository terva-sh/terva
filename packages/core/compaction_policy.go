package core

import (
	"context"
	"errors"

	"terva.sh/terva/packages/core/compactprose"
)

// Compaction is mechanics in the engine and policy in the host (decision 0021,
// rule 4). The engine measures the context, finds a safe cut, runs the
// strategies with their fallbacks, and installs the result. A
// CompactionPolicy decides whether to compact at a given point, how many recent
// messages to keep verbatim, and which strategies to try.
//
// Before the policy, the decision was split. The engine decided before a turn,
// on an oversized request, and between tool steps, and each front end carried
// its own copy of the check between turns. Those copies used the same
// constants only because nobody had changed one of them yet.

// CompactPoint is where the engine, or a host through the engine, asks the
// policy whether to compact.
type CompactPoint string

const (
	// CompactBeforeTurn is before a prompt is sent, when a resumed transcript
	// may already be near the limit (PromptWithPolicy).
	CompactBeforeTurn CompactPoint = "before_turn"
	// CompactMidTurn is the safe boundary between two tool steps of one turn,
	// so a long agentic turn cannot outgrow the window with no check.
	CompactMidTurn CompactPoint = "mid_turn"
	// CompactAfterTurn is after a turn ends. The host chooses the moment,
	// because what surrounds it (queued messages, snapshots, a reply to send)
	// is the host's, and asks through CompactIfDue or Compaction.
	CompactAfterTurn CompactPoint = "after_turn"
	// CompactOversize is after a provider rejected a request as too large:
	// whether to condense and retry once.
	CompactOversize CompactPoint = "oversize"
	// CompactRequested is a compaction something asked for outright, such as a
	// /compact command. The decision's Compact is not consulted; its KeepTail
	// and Strategies are.
	CompactRequested CompactPoint = "requested"
	// CompactPrefixChanged is before a prompt is sent, when something has
	// rewritten the cached prompt prefix since the last request (a model
	// switch, a tool set rebuilt) and the next request would re-read the
	// transcript at full price. Compact says whether to OFFER a compaction
	// first: the engine asks the user through the agent's Asker, and compacts only
	// on a yes, with this decision's KeepTail and Strategies. The engine makes
	// the offer only when Strategies allow CompactWarm, because only the warm
	// summarizer reads the transcript at cache rates, so only then is there a
	// saving to offer (turn_policy.go, offerCompactOnPrefixChange).
	CompactPrefixChanged CompactPoint = "prefix_changed"
)

// CompactionState is what the engine knows at a point: enough for a policy to
// decide without reading the agent.
type CompactionState struct {
	Point CompactPoint
	// Fraction is Used over Window, or 0 when either is unknown.
	Fraction float64
	// Used is the last request's prompt tokens; Window is the model's
	// working context window. Either is 0 when unknown.
	Used, Window int
	// Messages is the transcript's length.
	Messages int
}

// CompactionDecision is a policy's answer.
type CompactionDecision struct {
	// Compact says whether to compact at this point. Ignored for
	// CompactRequested.
	Compact bool
	// KeepTail is how many recent messages to keep verbatim after the
	// summary. The engine may keep fewer when they would not fit the window.
	KeepTail int
	// Strategies are the strategies to try. The engine tries them in the
	// fixed order CompactProvider, CompactWarm, CompactCold, skipping any not
	// listed: the backend's compaction runs before the keep-tail is applied,
	// and the cold summarizer is every path's fallback, so only a subset of
	// that order means anything. Nil is CompactCold alone: the one strategy
	// every provider supports and whose result every model can read.
	Strategies []CompactStrategy
}

// CompactionPolicy decides compaction for the engine. It is called from the
// turn loop and from hosts, possibly on different goroutines, and must not call
// back into the agent.
type CompactionPolicy interface {
	Decide(CompactionState) CompactionDecision
}

// DefaultCompactionPolicy compacts when the context passes a threshold, at
// every point its mode allows. Its zero value is the policy an agent with none
// uses: compact past 85% of the window, keep the last 4 messages, in the
// `steps` mode, with the cold summarizer alone.
type DefaultCompactionPolicy struct {
	// Threshold is the context fraction to compact at. Zero means
	// AutoCompactThreshold.
	Threshold float64
	// KeepTail is the keep-tail to ask for. Zero means AutoCompactKeepTail.
	KeepTail int
	// Mode, when set, supplies the live mode, read at every decision so a
	// settings change applies without rebuilding the agent. Nil, and any
	// value that is not a known mode, is AutoCompactSteps.
	Mode func() AutoCompactMode
	// Strategies is passed through to every decision.
	Strategies []CompactStrategy
}

var _ CompactionPolicy = DefaultCompactionPolicy{}

// Decide implements CompactionPolicy.
func (p DefaultCompactionPolicy) Decide(s CompactionState) CompactionDecision {
	keep := p.KeepTail
	if keep == 0 {
		keep = AutoCompactKeepTail
	}
	threshold := p.Threshold
	if threshold == 0 {
		threshold = AutoCompactThreshold
	}
	d := CompactionDecision{KeepTail: keep, Strategies: p.Strategies}
	mode := p.mode()
	switch s.Point {
	case CompactRequested:
		d.Compact = true
	case CompactOversize:
		// `off` means context-limit failures surface to the user, who
		// compacts by hand, so it refuses the retry too.
		d.Compact = mode != AutoCompactOff
	case CompactMidTurn:
		d.Compact = mode == AutoCompactSteps && s.Fraction >= threshold
	case CompactBeforeTurn, CompactAfterTurn:
		d.Compact = mode != AutoCompactOff && s.Fraction >= threshold
	case CompactPrefixChanged:
		// No offer: a host that wants its users asked says so in its policy.
		d.Compact = false
	}
	return d
}

func (p DefaultCompactionPolicy) mode() AutoCompactMode {
	if p.Mode != nil {
		switch m := p.Mode(); m {
		case AutoCompactOff, AutoCompactTurns, AutoCompactSteps:
			return m
		}
	}
	return AutoCompactSteps
}

// CompactionPolicy returns the policy the agent decides compaction with: the
// one WithCompactionPolicy passed, or DefaultCompactionPolicy when none was.
func (a *Agent) CompactionPolicy() CompactionPolicy {
	a.mu.Lock()
	p := a.compactionPolicy
	a.mu.Unlock()
	if p != nil {
		return p
	}
	return DefaultCompactionPolicy{}
}

// decideCompaction asks the policy at point with the agent's current state,
// and nothing more. Compaction adds the engine's own check.
func (a *Agent) decideCompaction(point CompactPoint) CompactionDecision {
	used, window := a.ContextUsage()
	a.mu.Lock()
	n := len(a.messages)
	a.mu.Unlock()
	s := CompactionState{Point: point, Used: used, Window: window, Messages: n}
	if used > 0 && window > 0 {
		s.Fraction = float64(used) / float64(window)
	}
	d := a.CompactionPolicy().Decide(s)
	if d.KeepTail < 0 {
		d.KeepTail = 0
	}
	return d
}

// Compaction is the policy's decision at point. At the three automatic points
// that fire on a full window (before, during and after a turn) it also says no
// when the transcript is no longer than the keep-tail: there is nothing to
// summarize, and a compaction would fail with ErrNothingToCompact.
func (a *Agent) Compaction(point CompactPoint) CompactionDecision {
	d := a.decideCompaction(point)
	switch point {
	case CompactBeforeTurn, CompactMidTurn, CompactAfterTurn:
		if d.Compact && !a.canCompact(d.KeepTail) {
			d.Compact = false
		}
	}
	return d
}

// CompactIfDue compacts when the policy says to at point, and reports whether
// it ran. It is the call a host makes where it used to check the threshold
// itself, typically CompactAfterTurn once a turn has ended and before the host
// takes its next input. It emits EvCompactStart and EvCompactEnd through sink
// (which may be nil) and to extensions.
//
// A transcript with nothing left to summarize is not a failure. A cancelled
// context is not reported as one either: the caller cancelled.
func (a *Agent) CompactIfDue(ctx context.Context, point CompactPoint, sink func(AgentEvent)) (res CompactResult, ran bool, err error) {
	d := a.Compaction(point)
	if !d.Compact {
		return CompactResult{}, false, nil
	}
	if sink == nil {
		sink = func(AgentEvent) {}
	}
	start := EvCompactStart{Reason: "context near limit"}
	sink(start)
	a.emitLifecycle(start)
	res, err = a.CompactWith(ctx, d, nil)
	if errors.Is(err, ErrNothingToCompact) {
		err = nil
	}
	end := EvCompactEnd{Usage: res.Usage}
	if err != nil && ctx.Err() == nil {
		end.Err = err.Error()
	}
	sink(end)
	a.emitLifecycle(end)
	return res, true, err
}

// strategyAllowed reports whether a compaction under strategies may try s.
// Nil allows cold alone. Warm and the backend's compaction are a policy's to
// ask for: warm writes the summary from inside the agent's persona with its
// tools advertised, which is a quality trade, and the backend's result is a
// blob only that provider can read.
func strategyAllowed(strategies []CompactStrategy, s CompactStrategy) bool {
	if strategies == nil {
		return s == CompactCold
	}
	for _, x := range strategies {
		if x == s {
			return true
		}
	}
	return false
}

// CompactionPrompts is the text a compaction sends to the model and leaves in
// the transcript. Every field is optional: the engine fills an empty one from
// the neutral default in packages/core/compactprose. The engine assembles the
// pieces, and the order and separators are its own; the words are the host's.
type CompactionPrompts struct {
	// System is the summarizer's system prompt for a summary sent as its own
	// request (the cold strategy).
	System string
	// Instruction asks for the checkpoint and gives its format.
	Instruction string
	// MidTurnAddendum follows the instruction when the summary interrupts a
	// tool-use loop.
	MidTurnAddendum string
	// WarmPreamble opens a summary asked for at the end of the conversation
	// itself (the warm strategy).
	WarmPreamble string
	// WarmKeepTail says that n recent messages survive beside the summary.
	WarmKeepTail func(n int) string
	// TruncatedNotice follows a summary that reached the output limit, when
	// the executed-actions list follows it; TruncatedNoticeBare when not.
	TruncatedNotice, TruncatedNoticeBare string
	// ProviderNotice stands in for a summary the provider made on its side.
	ProviderNotice string
	// LedgerHeader opens the executed-actions list.
	LedgerHeader string
	// LedgerOmitted notes that n earlier calls did not fit the list, and
	// returns the line and the blank line after it.
	LedgerOmitted func(n int) string
	// LedgerFailed marks a call to the named tool that returned an error.
	LedgerFailed func(tool string) string
	// LedgerUnknown marks a call with no recorded result: compaction landed
	// between the call and its result.
	LedgerUnknown string
}

// CompactionPrompter is implemented by a CompactionPolicy that supplies its own
// compaction text.
type CompactionPrompter interface {
	CompactionPrompts() CompactionPrompts
}

// compactionPrompts is the policy's text with every empty field filled from
// the neutral default.
func (a *Agent) compactionPrompts() CompactionPrompts {
	var p CompactionPrompts
	if cp, ok := a.CompactionPolicy().(CompactionPrompter); ok {
		p = cp.CompactionPrompts()
	}
	fill := func(s *string, def string) {
		if *s == "" {
			*s = def
		}
	}
	fill(&p.System, compactprose.System)
	fill(&p.Instruction, compactprose.Instruction)
	fill(&p.MidTurnAddendum, compactprose.MidTurnAddendum)
	fill(&p.WarmPreamble, compactprose.WarmPreamble)
	fill(&p.TruncatedNotice, compactprose.TruncatedNotice)
	fill(&p.TruncatedNoticeBare, compactprose.TruncatedNoticeBare)
	fill(&p.ProviderNotice, compactprose.ProviderNotice)
	fill(&p.LedgerHeader, compactprose.LedgerHeader)
	fill(&p.LedgerUnknown, compactprose.LedgerUnknown)
	if p.WarmKeepTail == nil {
		p.WarmKeepTail = compactprose.WarmKeepTail
	}
	if p.LedgerOmitted == nil {
		p.LedgerOmitted = compactprose.LedgerOmitted
	}
	if p.LedgerFailed == nil {
		p.LedgerFailed = compactprose.LedgerFailed
	}
	return p
}
