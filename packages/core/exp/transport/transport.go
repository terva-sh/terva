// Package transport records how each request physically reached the provider:
// connection reuse, the edge it landed on, and the provider's request ID. Some
// cache misses have nothing to do with the prompt, and these rows are the only
// data about that layer.
//
// It is an experiment (packages/core/exp): TKT-01M396PNR decides whether it
// graduates to a stable component or is deleted.
//
// A host passes a Recorder to core.WithComponent, which binds it and registers
// its dispatch observer.
// The provider captures the picture whether or not anyone records it (one
// httptrace callback and three header reads) and sends it as a
// provider.EventTransport. The Recorder hands it to the agent's store through
// core's AppendDiagnostic, as a "net" row next to the request's usage row.
package transport

import (
	"sync"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// Recorder writes each request's transport picture to the agent's store. The
// zero value is switched off. It is safe for concurrent use.
type Recorder struct {
	mu    sync.Mutex
	on    bool
	agent *core.Agent
}

// SetEnabled switches the recorder (engine feature transport_recording in
// terva, on by default; the off switch exists for anyone who would rather not
// persist edge and request identifiers in session files).
func (r *Recorder) SetEnabled(on bool) {
	r.mu.Lock()
	r.on = on
	r.mu.Unlock()
}

// Enabled reports whether the recorder is writing.
func (r *Recorder) Enabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.on
}

// Bind implements core.Binder: it keeps the agent whose store the rows go to.
// A host passes the Recorder to core.WithComponent, which calls Bind and
// registers DispatchObserver. A Recorder serves one agent.
func (r *Recorder) Bind(a *core.Agent) {
	r.mu.Lock()
	r.agent = a
	r.mu.Unlock()
}

// DispatchObserver implements core.DispatchWatcher: the recorder hears each
// event of a request's stream.
func (r *Recorder) DispatchObserver() core.DispatchObserver {
	return core.DispatchObserver{Event: r.event}
}

var (
	_ core.Binder          = (*Recorder)(nil)
	_ core.DispatchWatcher = (*Recorder)(nil)
)

func (r *Recorder) event(ev provider.Event) {
	e, ok := ev.(provider.EventTransport)
	if !ok {
		return
	}
	r.mu.Lock()
	on, a := r.on, r.agent
	r.mu.Unlock()
	if !on {
		return
	}
	a.AppendDiagnostic(func(s core.TranscriptDiagnostics) error { return s.AppendTransport(e.Info) })
}
