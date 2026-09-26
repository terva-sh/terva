package core

import (
	"sync"

	"terva.sh/terva/packages/provider"
)

// TranscriptStore is where an agent's conversation persists. The engine holds
// the live transcript in memory and writes each change to the store as it
// happens; it never reads the store back. Loading is the host's: it reads a
// Transcript from wherever it keeps one and hands it to Agent.Resume.
//
// The methods are the writes a resume needs. A store that keeps the engine's
// diagnostic records too implements TranscriptDiagnostics as well.
//
// terva's store is the JSONL session file (decision 0007), in
// packages/session. MemoryTranscriptStore is the one in memory. Package
// transcripttest is the behavior suite either one has to pass.
//
// Calls are serialized: AttachTranscriptStore holds one lock across every
// write, so a store need not be safe for concurrent use by the engine.
type TranscriptStore interface {
	// AppendMessage records a message added to the transcript.
	AppendMessage(provider.Message) error
	// AppendCompaction records that compaction replaced the transcript with
	// messages. res carries the compaction's own spend and how it ran.
	AppendCompaction(messages []provider.Message, res CompactResult) error
	// AppendUsage records one request's usage.
	AppendUsage(UsageRecord) error
	// AppendToolGroupActivation records that the model activated a lazy tool
	// group. A resume restores the set, so the tools array, which sits ahead
	// of the whole transcript in the cached prefix, stays byte-identical.
	AppendToolGroupActivation(group string) error
	// AppendImageExclusion records that the provider rejected the image whose
	// raw bytes hash to sha256Hex. A resume drops every copy of it, so the
	// session does not re-send it and fail again.
	AppendImageExclusion(sha256Hex string) error
}

// TranscriptDiagnostics is the optional half of a store: records that explain
// a session afterwards and that a resume does not need. AttachTranscriptStore
// writes them when the store implements this interface.
type TranscriptDiagnostics interface {
	AppendEscalation(EscalationRecord) error
	AppendStall(StallRecord) error
	AppendRetry(RetryRecord) error
	AppendTail(TailRecord) error
	AppendPrefixDivergence(PrefixDivergence) error
	AppendTransport(provider.TransportInfo) error
	// AppendCacheCliff receives every cache-cliff event, ongoing and ending.
	// Which of them a store keeps is its own policy.
	AppendCacheCliff(CacheCliff) error
}

// UsageKind says whose request a usage record paid for.
type UsageKind int

const (
	// UsageTurn is a request of this conversation's own turns.
	UsageTurn UsageKind = iota
	// UsageDelegated is a sub-agent's spend, booked to this conversation.
	UsageDelegated
	// UsageSideChannel is a host's own one-off completion, named by Source.
	UsageSideChannel
)

// UsageRecord is one request's usage and the running total after it. The
// total is one timeline across every kind, so a crash recovers the true sum;
// the kind keeps a sub-agent's or a side channel's request from being read as
// one of this conversation's turns.
type UsageRecord struct {
	Kind       UsageKind
	Source     string // the side channel's name; set only for UsageSideChannel
	Usage      provider.Usage
	Cumulative provider.Usage
}

// Transcript is what a host loads from a store to resume a conversation.
type Transcript struct {
	Messages []provider.Message
	// Cumulative is the conversation's total usage so far.
	Cumulative provider.Usage
	// ResumeContext is the baseline for the context gauge: roughly what the
	// next prompt will cost. After a trailing compaction it is the compacted
	// transcript's estimate, not the last turn's prompt size.
	ResumeContext provider.Usage
	// ActiveToolGroups are the lazy tool groups the model had activated.
	ActiveToolGroups []string
}

// AttachTranscriptStore makes store receive every write the agent's
// conversation produces from here on, and the diagnostic records too when it
// implements TranscriptDiagnostics. It adds to the agent's observers rather
// than replacing them, and there is no detach: a host that switches stores
// builds a new agent, or keeps one store and switches what is behind it.
//
// A failed write latches on the agent (see RecordPersistenceError): the active
// run returns it at a safe boundary and later runs refuse to start, while the
// live transcript stays available for recovery.
func (a *Agent) AttachTranscriptStore(store TranscriptStore) {
	var mu sync.Mutex
	write := func(fn func() error) {
		mu.Lock()
		defer mu.Unlock()
		a.RecordPersistenceError(fn())
	}
	a.AddMessageObserver(func(m provider.Message) {
		write(func() error { return store.AppendMessage(m) })
	})
	a.addUsageObserver(func(u, cum provider.Usage) {
		write(func() error { return store.AppendUsage(UsageRecord{Kind: UsageTurn, Usage: u, Cumulative: cum}) })
	})
	a.addDelegatedUsageObserver(func(u, cum provider.Usage) {
		write(func() error { return store.AppendUsage(UsageRecord{Kind: UsageDelegated, Usage: u, Cumulative: cum}) })
	})
	a.addSideChannelUsageObserver(func(source string, u, cum provider.Usage) {
		write(func() error {
			return store.AppendUsage(UsageRecord{Kind: UsageSideChannel, Source: source, Usage: u, Cumulative: cum})
		})
	})
	a.addTranscriptCompactedObserver(func(messages []provider.Message, res CompactResult) {
		write(func() error { return store.AppendCompaction(messages, res) })
	})
	a.addImageExcludedObserver(func(sha256Hex string) {
		write(func() error { return store.AppendImageExclusion(sha256Hex) })
	})

	// A component's required records (AppendRecord), such as lazy tools'
	// group activations, go through the same writer.
	a.obsMu.Lock()
	a.recordWriters = append(a.recordWriters, func(fn func(TranscriptStore) error) {
		write(func() error { return fn(store) })
	})
	a.obsMu.Unlock()

	diag, ok := store.(TranscriptDiagnostics)
	if !ok {
		return
	}
	// The stuck-loop hatch's records are written from its events, the same
	// events clients receive, so the row and the live signal are one fact
	// emitted once. A component that emits them gets its rows written without a
	// hook of its own (docs/plans/stall-component.md, seam A).
	a.AddEventObserver(func(ev AgentEvent) {
		switch e := ev.(type) {
		case EvStall:
			write(func() error { return diag.AppendStall(e.StallRecord) })
		case EvEscalation:
			write(func() error { return diag.AppendEscalation(e.EscalationRecord) })
		}
	})
	a.addRetryObserver(func(rec RetryRecord) {
		write(func() error { return diag.AppendRetry(rec) })
	})
	a.addTailObserver(func(rec TailRecord) {
		write(func() error { return diag.AppendTail(rec) })
	})
	// A component's rows (AppendDiagnostic), such as the prefix watch's and the
	// transport recorder's, go through the same writer.
	a.obsMu.Lock()
	a.diagWriters = append(a.diagWriters, func(fn func(TranscriptDiagnostics) error) {
		write(func() error { return fn(diag) })
	})
	a.obsMu.Unlock()
}

// Resume loads t into the agent: the messages, the cost meter, the context
// gauge, and, when the agent's ToolVisibility is a GroupRestorer, the activated
// tool groups. Call it before the first turn, and after AdoptSessionIdentity so the restored groups ride the right cache route.
// It writes nothing to a store; the transcript came from one.
func (a *Agent) Resume(t Transcript) {
	a.SetMessages(t.Messages)
	a.SeedCost(t.Cumulative)
	a.SeedLastTurnUsage(t.ResumeContext)
	if r, ok := a.ToolVisibility().(GroupRestorer); ok {
		r.RestoreActiveGroups(t.ActiveToolGroups)
	}
}
