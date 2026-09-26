package prefixwatch

import (
	"context"
	"sync"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

func msgText(role provider.Role, text string) provider.Message {
	return provider.Message{Role: role, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

// frame is the smallest assembler: one Stable segment for the system prompt.
type frame struct{ system string }

func (f frame) Assemble(core.AssembleMode) core.Frame {
	return core.Frame{Segments: []core.Segment{{Stability: core.Stable, Tag: "system", Content: f.system}}}
}

// newAgent builds an agent with a Watch switched on and attached.
func newAgent(c provider.Client) (*core.Agent, *Watch) {
	w := &Watch{}
	w.SetEnabled(true)
	a, err := core.New(c, "m", core.WithAssembler(frame{system: "sys"}), core.WithTools(core.Registry{}),
		core.WithGate(core.AllowAll), core.WithComponent(w))
	if err != nil {
		panic(err)
	}
	return a, w
}

// records is what a Watch reported to its observers.
type records struct {
	mu          sync.Mutex
	divergences []core.PrefixDivergence
	cliffs      []core.CacheCliff
}

func (r *records) divs() []core.PrefixDivergence {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]core.PrefixDivergence(nil), r.divergences...)
}

// record adds an observer to w that keeps everything it reports.
func record(w *Watch) *records {
	r := &records{}
	w.AddObserver(Observer{
		Divergence: func(d core.PrefixDivergence) {
			r.mu.Lock()
			r.divergences = append(r.divergences, d)
			r.mu.Unlock()
		},
		Cliff: func(cc core.CacheCliff) {
			r.mu.Lock()
			r.cliffs = append(r.cliffs, cc)
			r.mu.Unlock()
		},
	})
	return r
}

// textClient answers every request with a short summary-shaped text, which
// both an ordinary turn and a compaction accept.
type textClient struct{}

func (textClient) Name() string { return "text" }

func (textClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "text", Model: req.Model}
		// Non-empty text: a compaction rejects an empty summary.
		out <- provider.EventTextDelta{Delta: "## Goal\nkeep going"}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "## Goal\nkeep going"}},
		}}
	}()
	return out, nil
}

// scriptedClient answers request n with whatever the script returns for it.
type scriptedClient struct {
	script func(n int, req provider.Request) ([]provider.Event, error)

	mu sync.Mutex
	n  int
}

func (c *scriptedClient) Name() string { return "scripted" }

func (c *scriptedClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	n := c.n
	c.n++
	c.mu.Unlock()
	evs, err := c.script(n, req)
	if err != nil {
		return nil, err
	}
	out := make(chan provider.Event, len(evs))
	go func() {
		defer close(out)
		for _, e := range evs {
			out <- e
		}
	}()
	return out, nil
}
