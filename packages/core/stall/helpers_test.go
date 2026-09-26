package stall

import (
	"context"
	"encoding/json"
	"sync"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

func call(id, name, args string) provider.Message {
	return provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.ToolCallBlock{ID: id, Name: name, Arguments: json.RawMessage(args)}},
	}
}

func result(id, text string, isErr bool) provider.Message {
	return provider.Message{
		Role: provider.RoleTool,
		Content: []provider.Content{provider.ToolResultBlock{
			CallID: id, IsError: isErr,
			Content: []provider.Content{provider.TextBlock{Text: text}},
		}},
	}
}

// on is a detector switched on and attached to nothing, for the tests that
// drive its hooks directly.
func on() *Detector {
	d := &Detector{}
	d.SetEnabled(true)
	return d
}

// host is the smallest assembler that carries the detector the way terva's
// does: a system prompt, then the detector's segment, with every delivery
// report forwarded.
type host struct {
	system string
	d      *Detector
}

func (h *host) Assemble(core.AssembleMode) core.Frame {
	return core.Frame{Segments: []core.Segment{
		{Stability: core.Stable, Tag: "system", Content: h.system},
		h.d.Segment(),
	}}
}

func (h *host) TailDelivered(ids []string) { h.d.TailDelivered(ids) }

var _ core.TailDeliveryObserver = (*host)(nil)

// newAgent builds an agent with d connected in all four places a host
// connects it: the frame and the delivery report through the assembler, the
// step gate and the tool gate as a component.
func newAgent(c provider.Client, model string, tools core.Registry, d *Detector, opts ...core.Option) *core.Agent {
	a, err := core.New(c, model, append([]core.Option{
		core.WithAssembler(&host{system: "you are terva", d: d}),
		core.WithTools(tools),
		core.WithGate(core.AllowAll),
		core.WithComponent(d)}, opts...)...)
	if err != nil {
		panic(err)
	}
	return a
}

// detectorOf returns the detector newAgent connected to a.
func detectorOf(a *core.Agent) *Detector { return a.Assembler().(*host).d }

// scriptedClient answers request n with whatever the script returns for it, and
// records every request it was handed.
type scriptedClient struct {
	name   string
	script func(n int, req provider.Request) ([]provider.Event, error)

	mu   sync.Mutex
	reqs []provider.Request
}

func (c *scriptedClient) Name() string { return c.name }

func (c *scriptedClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	n := len(c.reqs)
	c.reqs = append(c.reqs, req)
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

func (c *scriptedClient) calls() []provider.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Request(nil), c.reqs...)
}

// saidText is a plain text answer with a usage row.
func saidText(text string, input int) []provider.Event {
	return []provider.Event{
		provider.EventStart{Provider: "scripted"},
		provider.EventTextDelta{Delta: text},
		provider.EventUsage{Usage: provider.Usage{InputTokens: input, OutputTokens: 10}},
		provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: text}},
		}},
	}
}

// calledTool is one assistant turn that calls name with args, as call id.
func calledTool(id, name, args string) []provider.Event {
	return []provider.Event{
		provider.EventStart{Provider: "scripted"},
		provider.EventUsage{Usage: provider.Usage{InputTokens: 100, OutputTokens: 10}},
		provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
			Role: provider.RoleAssistant,
			Content: []provider.Content{provider.ToolCallBlock{
				ID: id, Name: name, Arguments: json.RawMessage(args),
			}},
		}},
	}
}

// recordingAsker answers every question with answer and keeps what it was
// asked.
type recordingAsker struct {
	answer func(core.UserQuestion) core.UserAnswer

	mu  sync.Mutex
	got []core.UserQuestion
}

func (r *recordingAsker) Ask(_ context.Context, qs []core.UserQuestion) ([]core.UserAnswer, error) {
	r.mu.Lock()
	r.got = append(r.got, qs...)
	r.mu.Unlock()
	out := make([]core.UserAnswer, len(qs))
	for i, q := range qs {
		out[i] = r.answer(q)
	}
	return out, nil
}

func (r *recordingAsker) questions() []core.UserQuestion {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]core.UserQuestion(nil), r.got...)
}

// collectStalls drains the stall records an agent's sink emits into a slice.
func collectStalls(evs *[]core.StallRecord) func(core.AgentEvent) {
	return func(ev core.AgentEvent) {
		if s, ok := ev.(core.EvStall); ok {
			*evs = append(*evs, s.StallRecord)
		}
	}
}
