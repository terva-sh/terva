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

// AddDelegatedUsageObserver registers fn to fire when a sub-agent's spend is
// booked against this session (RecordDelegatedUsage), carrying the child's
// increment plus this session's cumulative total. Hosts persist it as a usage
// row MARKED delegated.
//
// Separate from AddUsageObserver because the two answer different questions and
// only one of them is "what did this session's last request cost". Delegated
// spend was already kept out of the last-turn snapshot and out of RecentUsage()
// — whose comment warns that folding it in "would put a transcript-sized cold
// read in the middle of the strip labelled as a cache miss" — but it reached
// the persistence observer anyway, so the row on disk had exactly that defect.
// nil is a no-op.
func (a *Agent) AddDelegatedUsageObserver(fn func(u, cumulative provider.Usage)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.delegatedUsageObs = append(a.delegatedUsageObs, fn)
	a.obsMu.Unlock()
}

// AddTranscriptCompactedObserver registers fn to fire after Compact replaces
// the in-memory transcript with the synthetic summary plus kept tail. Message
// observers do not fire for that wholesale replacement, so hosts append an
// explicit compaction checkpoint here. res carries what the compaction cost,
// so the checkpoint can record it (see CompactResult — that spend is cost, not
// context, and must never reach a usage row). nil is a no-op.
func (a *Agent) AddTranscriptCompactedObserver(fn func(messages []provider.Message, res CompactResult)) {
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

// AddToolGroupActivatedObserver registers fn to fire when a capability group is
// newly activated (activate_tools, or a skill's allowed-tools surfacing). Hosts
// persist a "tool_group" session row so the activation survives a resume.
//
// Without it, activation is in-memory only: NewAgent rebuilds activeGroups from
// config, so every --resume silently drops what the model activated, changing
// the tools array the provider has cached the whole transcript behind. nil is a
// no-op.
func (a *Agent) AddToolGroupActivatedObserver(fn func(group string)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.toolGroupObs = append(a.toolGroupObs, fn)
	a.obsMu.Unlock()
}

// AddEscalationObserver registers fn to fire when rung 3 of the stuck-loop hatch
// resolves an escalation decision — a swap to a stronger model, or a decline, a
// stop, or a failed swap (see EscalationRecord.Disposition). Hosts persist an
// "escalation" session row here: the swap itself writes only a "meta" row (via
// UpdateModel), byte-identical to a user /model switch, so this is the provenance
// that tells a harness escalation apart from a human one in the log. Fires only
// once a target is configured (unconfigured users get no rows) and for every
// disposition, not just successful swaps. nil is a no-op.
func (a *Agent) AddEscalationObserver(fn func(EscalationRecord)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.escalationObs = append(a.escalationObs, fn)
	a.obsMu.Unlock()
}

// AddStallObserver registers fn to fire when the stuck-loop detector nudges —
// rung 1 of the hatch: a model repeated the same call, or hit the same failure,
// past the threshold (see StallRecord). Fires once per distinct loop per turn.
// Hosts persist a "stall" session row here so the detector's action is visible
// after the fact — the thing that otherwise happens only on the ephemeral tail,
// leaving no trace of whether it fired. Unlike escalation this needs no
// configured target: any session with the (default-on) detector records nudges.
// nil is a no-op.
func (a *Agent) AddStallObserver(fn func(StallRecord)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.stallObs = append(a.stallObs, fn)
	a.obsMu.Unlock()
}

// AddTailObserver registers fn to fire when the ephemeral tail's COMPOSITION
// changes — the block of text appended to every request after the prompt-cache
// breakpoint, which is composed per request and otherwise discarded (see
// TailRecord). Hosts persist a "tail" session row here, because nothing else
// does: a session file records the model's reaction to a prompt injection and
// never the injection itself, which left one review inferring what the model had
// been shown by reading the harness source.
//
// Fires on change, not per request, so a session whose tail is stable writes one
// row rather than one per turn. nil is a no-op.
func (a *Agent) AddTailObserver(fn func(TailRecord)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.tailObs = append(a.tailObs, fn)
	a.obsMu.Unlock()
}

// AddPrefixDivergenceObserver registers fn to fire when a dispatch's cacheable
// prefix diverges from the previous dispatch's at a rung the two SHARE — i.e.
// the transcript was rebuilt rather than extended, so the provider re-reads
// everything from that point at full price.
//
// Never fires for an ordinary append, which is every healthy request. Hosts
// persist a "prefix" session row, because this is the one cost driver that
// leaves no other trace: a mutated prefix looks identical to a healthy one in
// the transcript, and shows up only as a cache-read figure that nothing explains.
// nil is a no-op.
func (a *Agent) AddPrefixDivergenceObserver(fn func(PrefixDivergence)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.prefixDivObs = append(a.prefixDivObs, fn)
	a.obsMu.Unlock()
}

// AddQueueDrainedObserver registers fn to fire when the agent loop consumes
// queued user messages at a mid-turn safe boundary, carrying what it took.
//
// This is the one queue mutation a host cannot already know about. Every other
// one it performs itself — QueueMessage, SetQueuedMessages, ShiftQueuedMessage —
// and announces on the way out. The loop's own drain happens on the turn
// goroutine, between steps, with nobody watching: the messages leave the queue
// and arrive in the transcript, and a host that mirrors the queue rather than
// tracking it goes on rendering prompts the agent has already sent. Hosts wire
// this to whatever they broadcast the queue with.
//
// Fired with a.mu released, so an observer may read the agent. nil is a no-op.
func (a *Agent) AddQueueDrainedObserver(fn func(drained []string)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.queueDrainedObs = append(a.queueDrainedObs, fn)
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

func (a *Agent) fireTail(rec TailRecord) {
	a.obsMu.RLock()
	obs := make([]func(TailRecord), len(a.tailObs))
	copy(obs, a.tailObs)
	a.obsMu.RUnlock()
	for _, fn := range obs {
		fn(rec)
	}
}

func (a *Agent) firePrefixDivergence(d PrefixDivergence) {
	a.obsMu.RLock()
	obs := make([]func(PrefixDivergence), len(a.prefixDivObs))
	copy(obs, a.prefixDivObs)
	a.obsMu.RUnlock()
	for _, fn := range obs {
		fn(d)
	}
}

func (a *Agent) fireTranscriptCompacted(messages []provider.Message, res CompactResult) {
	a.obsMu.RLock()
	obs := make([]func(messages []provider.Message, res CompactResult), len(a.transcriptCompactedObs))
	copy(obs, a.transcriptCompactedObs)
	a.obsMu.RUnlock()
	for _, fn := range obs {
		fn(messages, res)
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

func (a *Agent) fireDelegatedUsage(u, cumulative provider.Usage) {
	a.obsMu.RLock()
	obs := make([]func(u, cumulative provider.Usage), len(a.delegatedUsageObs))
	copy(obs, a.delegatedUsageObs)
	a.obsMu.RUnlock()
	for _, fn := range obs {
		fn(u, cumulative)
	}
}

func (a *Agent) fireToolGroupActivated(group string) {
	a.obsMu.RLock()
	obs := make([]func(group string), len(a.toolGroupObs))
	copy(obs, a.toolGroupObs)
	a.obsMu.RUnlock()
	for _, fn := range obs {
		fn(group)
	}
}

func (a *Agent) fireEscalation(rec EscalationRecord) {
	a.obsMu.RLock()
	obs := make([]func(EscalationRecord), len(a.escalationObs))
	copy(obs, a.escalationObs)
	a.obsMu.RUnlock()
	for _, fn := range obs {
		fn(rec)
	}
}

func (a *Agent) fireStall(rec StallRecord) {
	a.obsMu.RLock()
	obs := make([]func(StallRecord), len(a.stallObs))
	copy(obs, a.stallObs)
	a.obsMu.RUnlock()
	for _, fn := range obs {
		fn(rec)
	}
}

// fireQueueDrained runs with a.mu released — observers call back into the host,
// which reads the agent.
func (a *Agent) fireQueueDrained(drained []string) {
	a.obsMu.RLock()
	obs := make([]func(drained []string), len(a.queueDrainedObs))
	copy(obs, a.queueDrainedObs)
	a.obsMu.RUnlock()
	for _, fn := range obs {
		fn(drained)
	}
}
