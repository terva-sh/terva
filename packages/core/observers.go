package core

import "terva.sh/terva/packages/provider"

// Agent observers.
//
// Hosts compose by REGISTERING, never by assigning. Every observer added is
// kept and called in registration order, so a host that wires the durable
// transcript and a host that mirrors turns to a chat bridge coexist without
// either knowing about the other.
//
// These replaced plain assignable fields (OnEvent, OnMessageAppended, OnUsage,
// OnTranscriptCompacted, OnImageExcluded). A field is last-writer-wins: a second
// caller silently unwired the first, with no compile error and no test signal
// unless some test happened to read the transcript back off disk. Composing
// meant capturing the previous func and calling it first — correct only if you
// knew to, and only if you ran after the wiring you were chaining onto. Two
// callers already hand-rolled that dance; a third would have dropped durable
// persistence on the floor.
//
// Registration is safe from any goroutine. Observers fire OUTSIDE the agent
// lock, so an observer may take its own locks and may call back into the agent.
// An observer must not register another observer (it would deadlock on the
// registry lock); nothing needs to, and the emit path snapshots first anyway.
//
// Ordering is registration order, and it is load-bearing where documented — the
// workspace registers its client broadcast first so the UI streams before the
// slower extension fan-out runs.

// AddEventObserver registers fn to receive every AgentEvent the loop emits, in
// addition to the per-Prompt sink. Observers run before the sink. nil is a
// no-op. Used by the extension manager, the hook engine, the ACP session
// translator and the control-plane broadcast.
func (a *Agent) AddEventObserver(fn func(AgentEvent)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.eventObs = append(a.eventObs, fn)
	a.obsMu.Unlock()
}

// AddMessageObserver registers fn to fire every time a message is appended to
// the in-memory transcript by the agent loop — the initial user prompt, each
// finalised assistant message, and each tool-results message (plus the
// synthetic OpenAI image mirror, if any). Hosts wire durable session
// persistence here, so turns land on disk as they happen rather than only on a
// clean exit. nil is a no-op.
func (a *Agent) AddMessageObserver(fn func(provider.Message)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.messageObs = append(a.messageObs, fn)
	a.obsMu.Unlock()
}

// AddUsageObserver registers fn to fire after every request's usage row
// arrives, carrying that request's own usage plus the session's cumulative
// usage. Hosts persist these so a crash recovers the right cost figure, and so
// a resume can seed the context gauge from the per-request value (the
// cumulative one overstates it wildly on long sessions). nil is a no-op.
func (a *Agent) AddUsageObserver(fn func(u, cumulative provider.Usage)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.usageObs = append(a.usageObs, fn)
	a.obsMu.Unlock()
}

// AddTranscriptCompactedObserver registers fn to fire after Compact replaces
// the in-memory transcript with the synthetic summary plus kept tail. Message
// observers do not fire for that wholesale replacement, so hosts append an
// explicit compaction checkpoint here. nil is a no-op.
func (a *Agent) AddTranscriptCompactedObserver(fn func(messages []provider.Message)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.transcriptCompactedObs = append(a.transcriptCompactedObs, fn)
	a.obsMu.Unlock()
}

// AddImageExcludedObserver registers fn to fire when image-rejection recovery
// drops an image the provider 400'd on, carrying its sha256. Hosts persist an
// exclude_image directive so the fix survives: a resumed session re-applies it
// instead of re-sending the bad image and re-failing. The recovery is paid
// once. nil is a no-op.
func (a *Agent) AddImageExcludedObserver(fn func(sha256Hex string)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.imageExcludedObs = append(a.imageExcludedObs, fn)
	a.obsMu.Unlock()
}

// ContinuationGate is one cause-tagged at-close continuation: consulted when a
// turn ends with a natural stop (the model produced a final message, no tool
// calls) to decide whether the prompt continues with one more segment. Hosts
// use gates to re-prompt the model while tracked work is still open (the
// open-work card, a coordinator's still-running sub-agents); activation
// continuation (docs/proposals/activation-continuation.md) registers its gate
// here too. Unlike the observers above a gate steers control flow, but it is
// registered the same way and for the same reason: the single assignable field
// this replaced (ContinueOnStop) forced each host to hand-compose every cause
// into one closure, and two hosts had already diverged.
type ContinuationGate struct {
	// Cause labels the gate in code and telemetry ("open-work", "swarm-hold",
	// "activation"). A label, not an identity — two gates may share one.
	Cause string
	// Fire returns the nudge to append (as a synthetic user message) and true
	// to continue the prompt. The loop consults gates only on StopEnd; stop is
	// passed for symmetry with any future boundary kinds. A ("", true) return
	// counts as a decline.
	Fire func(stop provider.StopReason) (nudge string, ok bool)
	// Cap bounds how many times this gate may fire within one Prompt; 0 means
	// once. Declines don't consume the budget.
	Cap int
}

// AddContinuationGate registers an at-close continuation gate. Gates are
// consulted in registration order — which IS priority order — and the first
// that fires wins the boundary; the rest wait for the next natural stop. A
// gate with a nil Fire is a no-op.
func (a *Agent) AddContinuationGate(g ContinuationGate) {
	if g.Fire == nil {
		return
	}
	a.obsMu.Lock()
	a.continuationGates = append(a.continuationGates, g)
	a.obsMu.Unlock()
}

// --- emit paths -------------------------------------------------------------
//
// Each snapshots under the registry read lock and fires outside it, so an
// observer may block, take locks, or re-enter the agent without stalling
// registration or deadlocking against the agent mutex.

func (a *Agent) eventObservers() []func(AgentEvent) {
	a.obsMu.RLock()
	defer a.obsMu.RUnlock()
	if len(a.eventObs) == 0 {
		return nil
	}
	obs := make([]func(AgentEvent), len(a.eventObs))
	copy(obs, a.eventObs)
	return obs
}

func (a *Agent) continuationGateSnapshot() []ContinuationGate {
	a.obsMu.RLock()
	defer a.obsMu.RUnlock()
	if len(a.continuationGates) == 0 {
		return nil
	}
	gates := make([]ContinuationGate, len(a.continuationGates))
	copy(gates, a.continuationGates)
	return gates
}

func (a *Agent) fireMessageAppended(m provider.Message) {
	a.obsMu.RLock()
	obs := make([]func(provider.Message), len(a.messageObs))
	copy(obs, a.messageObs)
	a.obsMu.RUnlock()
	for _, fn := range obs {
		fn(m)
	}
}

func (a *Agent) fireUsage(u, cumulative provider.Usage) {
	a.obsMu.RLock()
	obs := make([]func(u, cumulative provider.Usage), len(a.usageObs))
	copy(obs, a.usageObs)
	a.obsMu.RUnlock()
	for _, fn := range obs {
		fn(u, cumulative)
	}
}

func (a *Agent) fireTranscriptCompacted(messages []provider.Message) {
	a.obsMu.RLock()
	obs := make([]func(messages []provider.Message), len(a.transcriptCompactedObs))
	copy(obs, a.transcriptCompactedObs)
	a.obsMu.RUnlock()
	for _, fn := range obs {
		fn(messages)
	}
}

func (a *Agent) fireImageExcluded(sha256Hex string) {
	a.obsMu.RLock()
	obs := make([]func(sha256Hex string), len(a.imageExcludedObs))
	copy(obs, a.imageExcludedObs)
	a.obsMu.RUnlock()
	for _, fn := range obs {
		fn(sha256Hex)
	}
}
