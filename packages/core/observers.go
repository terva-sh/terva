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
//
// The observers for the records a transcript store keeps (usage, compaction,
// tool groups, image exclusions, and the diagnostics) are unexported. A host
// receives those writes through AttachTranscriptStore, which registers them
// together, so there is one way in rather than two (TKT-01M35WK12).

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

// addUsageObserver registers fn to fire after every request's usage row
// arrives, carrying that request's own usage plus the session's cumulative
// usage. A store persists these so a crash recovers the right cost figure, and so
// a resume can seed the context gauge from the per-request value (the
// cumulative one overstates it wildly on long sessions). nil is a no-op.
func (a *Agent) addUsageObserver(fn func(u, cumulative provider.Usage)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.usageObs = append(a.usageObs, fn)
	a.obsMu.Unlock()
}

// addDelegatedUsageObserver registers fn to fire when a sub-agent's spend is
// booked against this session (RecordDelegatedUsage), carrying the child's
// increment plus this session's cumulative total. Hosts persist it as a usage
// row MARKED delegated.
//
// Separate from addUsageObserver because the two answer different questions and
// only one of them is "what did this session's last request cost". Delegated
// spend was already kept out of the last-turn snapshot and out of RecentUsage()
// — whose comment warns that folding it in "would put a transcript-sized cold
// read in the middle of the strip labelled as a cache miss" — but it reached
// the persistence observer anyway, so the row on disk had exactly that defect.
// nil is a no-op.
func (a *Agent) addDelegatedUsageObserver(fn func(u, cumulative provider.Usage)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.delegatedUsageObs = append(a.delegatedUsageObs, fn)
	a.obsMu.Unlock()
}

// addSideChannelUsageObserver registers fn to fire when a host's one-off
// completion is booked against this session (RecordSideChannelUsage), carrying
// the source that spent it, the request's own usage, and this session's
// cumulative total. Hosts persist it as a usage row MARKED with the source.
//
// Separate from addUsageObserver for the reason the delegated one is: only the
// plain observer answers "what did this session's last request cost". A
// side-channel call was already kept out of the last-turn snapshot in memory,
// but it reached the persistence observer unmarked, so on disk an idle
// suggestion's request and a turn of the session were the same row.
//
// Registering one takes the side-channel calls OFF the plain observer for
// every registrant, not only this one: RecordSideChannelUsage falls back to
// the plain path only while nobody has registered here. A host that wants
// these calls for telemetry and persists on the plain observer must persist
// them here too, or they stop reaching disk. nil is a no-op.
func (a *Agent) addSideChannelUsageObserver(fn func(source string, u, cumulative provider.Usage)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.sideChannelUsageObs = append(a.sideChannelUsageObs, fn)
	a.obsMu.Unlock()
}

// addTranscriptCompactedObserver registers fn to fire after Compact replaces
// the in-memory transcript with the synthetic summary plus kept tail. Message
// observers do not fire for that wholesale replacement, so hosts append an
// explicit compaction checkpoint here. res carries what the compaction cost,
// so the checkpoint can record it (see CompactResult — that spend is cost, not
// context, and must never reach a usage row). nil is a no-op.
func (a *Agent) addTranscriptCompactedObserver(fn func(messages []provider.Message, res CompactResult)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.transcriptCompactedObs = append(a.transcriptCompactedObs, fn)
	a.obsMu.Unlock()
}

// addImageExcludedObserver registers fn to fire when image-rejection recovery
// drops an image the provider 400'd on, carrying its sha256. Hosts persist an
// exclude_image directive so the fix survives: a resumed session re-applies it
// instead of re-sending the bad image and re-failing. The recovery is paid
// once. nil is a no-op.
func (a *Agent) addImageExcludedObserver(fn func(sha256Hex string)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.imageExcludedObs = append(a.imageExcludedObs, fn)
	a.obsMu.Unlock()
}

// addRetryObserver registers fn to fire each time a transient provider failure
// is retried, from EITHER ladder — the turn loop or compaction (see
// RetryRecord.Phase). Hosts persist a "retry" session row here.
//
// An observer rather than only an event because the two ladders reach the user
// through different plumbing: the turn loop has an event sink to emit EvRetry
// on, and compaction has only a text sink for the summary it is streaming.
// Threading an event sink into Compact would change public API across every
// host and the SDK to reach the one path that lacks it; an observer is
// entry-point-agnostic and reaches all of them. nil is a no-op.
func (a *Agent) addRetryObserver(fn func(RetryRecord)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.retryObs = append(a.retryObs, fn)
	a.obsMu.Unlock()
}

// addTailObserver registers fn to fire when the ephemeral tail's COMPOSITION
// changes — the block of text appended to every request after the prompt-cache
// breakpoint, which is composed per request and otherwise discarded (see
// TailRecord). Hosts persist a "tail" session row here, because nothing else
// does: a session file records the model's reaction to a prompt injection and
// never the injection itself, which left one review inferring what the model had
// been shown by reading the harness source.
//
// Fires on change, not per request, so a session whose tail is stable writes one
// row rather than one per turn. nil is a no-op.
func (a *Agent) addTailObserver(fn func(TailRecord)) {
	if fn == nil {
		return
	}
	a.obsMu.Lock()
	a.tailObs = append(a.tailObs, fn)
	a.obsMu.Unlock()
}

// DispatchObserver hears each turn request the engine dispatches: the request
// once it has reached the provider, and then every provider event of its
// stream. A component that measures what went on the wire attaches through it,
// such as the prefix watch and the transport recorder in packages/core/exp.
// Either hook may be nil.
//
// Only the agent's own turn requests are dispatched through it. A compaction's
// summarizer request, a side channel and a sub-agent are not: their usage does
// not describe this conversation's prompt, and folding it in is the confusion
// that once made a child's cold start read as the parent's cache collapsing.
type DispatchObserver struct {
	// Sent is called once the provider has accepted the request, with the
	// request exactly as the engine sent it. It runs on the turn goroutine,
	// outside the agent's lock, before the stream is read.
	Sent func(req provider.Request)
	// Event is called for each provider event of that request's stream, in
	// order, on the turn goroutine and after the engine has acted on the event.
	// A row the engine writes for an event, such as the usage row, is therefore
	// written before any row an observer writes for it. It runs once per
	// streamed delta, so it must be cheap.
	Event func(ev provider.Event)
}

// addDispatchObserver registers o. Observers are snapshotted per request, so
// one added while a request streams hears the next. An observer with no hooks
// is a no-op.
func (a *Agent) addDispatchObserver(o DispatchObserver) {
	if o.Sent == nil && o.Event == nil {
		return
	}
	a.obsMu.Lock()
	a.dispatchObs = append(a.dispatchObs, o)
	a.obsMu.Unlock()
}

func (a *Agent) dispatchObservers() []DispatchObserver {
	a.obsMu.RLock()
	defer a.obsMu.RUnlock()
	if len(a.dispatchObs) == 0 {
		return nil
	}
	return append([]DispatchObserver(nil), a.dispatchObs...)
}

// AppendDiagnostic hands write to every store attached with
// AttachTranscriptStore that implements TranscriptDiagnostics, through the same
// serialized writer as the transcript's own rows, so a component's row is
// ordered with them and a failed write latches the persistence error the same
// way (see RecordPersistenceError). With no such store it does nothing.
//
// It is how a component records a diagnostic it produces, such as a prefix
// divergence, without holding the store itself.
func (a *Agent) AppendDiagnostic(write func(TranscriptDiagnostics) error) {
	if write == nil {
		return
	}
	a.obsMu.RLock()
	writers := append([](func(func(TranscriptDiagnostics) error))(nil), a.diagWriters...)
	a.obsMu.RUnlock()
	for _, w := range writers {
		w(write)
	}
}

// AppendRecord hands write to every store attached with AttachTranscriptStore,
// through the same serialized writer as the transcript's own rows. It is the
// twin of AppendDiagnostic for a record the store contract requires, such as a
// tool-group activation that a component produces: the row is ordered with the
// others, and a failed write latches the persistence error. With no store
// attached it does nothing.
func (a *Agent) AppendRecord(write func(TranscriptStore) error) {
	if write == nil {
		return
	}
	a.obsMu.RLock()
	writers := append([](func(func(TranscriptStore) error))(nil), a.recordWriters...)
	a.obsMu.RUnlock()
	for _, w := range writers {
		w(write)
	}
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
	// Fallback puts the gate after every gate without it, whatever order they
	// were registered in. A convenience continuation sets it, so that a gate
	// for unfinished work (open work, the swarm hold) always outranks it: lazy
	// tools' activation gate is one.
	Fallback bool
}

// AddContinuationGate registers an at-close continuation gate. Gates are
// consulted in registration order — which IS priority order — with every
// Fallback gate after the others, and the first that fires wins the
// boundary; the rest wait for the next natural stop. A gate with a nil Fire
// is a no-op.
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
	gates := make([]ContinuationGate, 0, len(a.continuationGates))
	for _, fallback := range []bool{false, true} {
		for _, g := range a.continuationGates {
			if g.Fallback == fallback {
				gates = append(gates, g)
			}
		}
	}
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

func (a *Agent) fireSideChannelUsage(source string, u, cumulative provider.Usage) {
	a.obsMu.RLock()
	obs := make([]func(source string, u, cumulative provider.Usage), len(a.sideChannelUsageObs))
	copy(obs, a.sideChannelUsageObs)
	a.obsMu.RUnlock()
	for _, fn := range obs {
		fn(source, u, cumulative)
	}
}

// fireRetry runs with a.mu released, like its siblings — observers call back
// into the host, which reads the agent. Both ladders call it: the compaction
// one holds the agent lock for the whole compaction, so this must never be
// invoked under it.
func (a *Agent) fireRetry(rec RetryRecord) {
	a.obsMu.RLock()
	obs := make([]func(RetryRecord), len(a.retryObs))
	copy(obs, a.retryObs)
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
