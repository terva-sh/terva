// Package prefixwatch records where a conversation's cached prompt prefix
// broke, and watches for a provider-side cache outage while it is happening.
//
// It is an experiment (packages/core/exp): the rows it writes are measurements
// whose value is still being judged. TKT-01M396PNR decides whether it
// graduates to a stable component or is deleted.
//
// Two detectors share one Watch, because the second reads the first:
//
//   - The prefix ladder (ladder.go) digests every dispatched request's
//     cacheable prefix and records a core.PrefixDivergence when the transcript
//     was rebuilt rather than extended.
//   - The cache-cliff detector (cliff.go) watches the usage rows for a run of
//     cache collapses on an append-only prompt, and records a core.CacheCliff.
//     It leans on the ladder to tell a provider's collapse from terva's own
//     rebuild, which is why it runs only while the ladder does.
//
// A host passes a Watch to core.WithComponent, which binds it and registers
// its dispatch observer.
// The rows reach the agent's store through core's AppendDiagnostic, so a host
// that attached a store with core.TranscriptDiagnostics gets them written with
// no wiring of its own. A host that shows them live adds an Observer.
package prefixwatch

import (
	"sync"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// Watch is the prefix ladder and the cache-cliff detector for one agent. The
// zero value is switched off. It is safe for concurrent use.
type Watch struct {
	mu    sync.Mutex
	on    bool
	agent *core.Agent

	// last is the ladder of the last dispatched prefix, so the next dispatch
	// can locate where it diverged.
	last *prefixLadder
	// epoch counts the ladder's non-append divergences (and dispatches with no
	// predecessor to compare against). The cliff detector uses it to tell "the
	// provider dropped us" from "we rebuilt the prefix".
	epoch int
	cliff cacheCliffState

	observers []Observer
}

// Observer hears what the Watch records. Either hook may be nil.
type Observer struct {
	// Divergence is called for every divergence the ladder records.
	Divergence func(core.PrefixDivergence)
	// Cliff is called for every cache-cliff event: each dispatch of an
	// ongoing run, and the one that ends it.
	Cliff func(core.CacheCliff)
}

// SetEnabled switches both detectors (engine feature
// prefix_divergence_recording in terva, where it ships on because a diagnostic
// that ships off is never on when the rare thing happens).
//
// Off, the Watch does no work. The retained ladder is deliberately NOT
// cleared: a session that toggles the feature back on mid-run would otherwise
// have nothing to compare against and would miss the first divergence after
// it, which is the one most likely to be interesting.
func (w *Watch) SetEnabled(on bool) {
	w.mu.Lock()
	w.on = on
	w.mu.Unlock()
}

// Enabled reports whether the Watch is recording.
func (w *Watch) Enabled() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.on
}

// AddObserver registers o. Observers are called in registration order, after
// the row has been handed to the store, and outside the Watch's lock.
func (w *Watch) AddObserver(o Observer) {
	w.mu.Lock()
	w.observers = append(w.observers, o)
	w.mu.Unlock()
}

// Bind implements core.Binder: it keeps the agent whose store the rows go to.
// A host passes the Watch to core.WithComponent, which calls Bind and
// registers DispatchObserver. A Watch serves one agent.
func (w *Watch) Bind(a *core.Agent) {
	w.mu.Lock()
	w.agent = a
	w.mu.Unlock()
}

// DispatchObserver implements core.DispatchWatcher: the Watch hears each
// request sent and each event of its stream.
func (w *Watch) DispatchObserver() core.DispatchObserver {
	return core.DispatchObserver{Sent: w.sent, Event: w.event}
}

var (
	_ core.Binder          = (*Watch)(nil)
	_ core.DispatchWatcher = (*Watch)(nil)
)

// event feeds the stream's usage rows to the cliff detector.
func (w *Watch) event(ev provider.Event) {
	if u, ok := ev.(provider.EventUsage); ok {
		w.usage(u.Usage)
	}
}

// reportCliff writes a cliff event to the store and tells the observers.
func (w *Watch) reportCliff(cc core.CacheCliff) {
	w.mu.Lock()
	a := w.agent
	w.mu.Unlock()
	a.AppendDiagnostic(func(s core.TranscriptDiagnostics) error { return s.AppendCacheCliff(cc) })
	w.notify(func(o Observer) {
		if o.Cliff != nil {
			o.Cliff(cc)
		}
	})
}

func (w *Watch) notify(fn func(Observer)) {
	w.mu.Lock()
	obs := append([]Observer(nil), w.observers...)
	w.mu.Unlock()
	for _, o := range obs {
		fn(o)
	}
}
