// Package coretest builds engine values for tests outside packages/core.
//
// Most tests that construct a core.Agent go through here, so a change to how
// an agent is built edits this file and packages/core's own newTestAgent, and
// few test files. packages/core's tests cannot import this package (it
// imports core), which is why they keep their own copy.
package coretest

import (
	"sync"

	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/stall"
	"terva.sh/terva/packages/provider"
)

// newAgent is core.New for the helpers below, with terva's catalog, the one
// build.NewAgent passes, so a test that writes modelreg's layers is read back
// by the agent. A test helper has no error to return, so a configuration
// core.New refuses panics.
func newAgent(client provider.Client, model string, opts ...core.Option) *core.Agent {
	a, err := core.New(client, model, append(opts, core.WithCatalog(modelreg.Registry()))...)
	if err != nil {
		panic("coretest: " + err.Error())
	}
	return a
}

// NewAgent builds an agent for a test that exercises the loop rather than the
// permission gate. It passes core.AllowAll, so every tool call runs. opts
// reach core.New after the helper's own.
func NewAgent(client provider.Client, model, system string, tools core.Registry, opts ...core.Option) *core.Agent {
	return NewAgentWithGate(client, model, system, tools, core.AllowAll, opts...)
}

// NewAgentWithAssembler builds an agent over a given assembler, for a test that
// drives a host's own, such as terva's build.Assembler, which the build wiring
// requires and this package cannot import.
func NewAgentWithAssembler(client provider.Client, model string, asm core.ContextAssembler, tools core.Registry) *core.Agent {
	return newAgent(client, model, core.WithAssembler(asm), core.WithTools(tools), core.WithGate(core.AllowAll))
}

// NewAgentWithGate builds an agent whose tool calls go through gate, for a test
// that exercises a gate or a ladder.
//
// The system prompt goes into a Frame, which a test reaches through FrameOf to
// change the frame mid-session the way a host's assembler would.
func NewAgentWithGate(client provider.Client, model, system string, tools core.Registry, gate core.Gate, opts ...core.Option) *core.Agent {
	return newAgent(client, model, append([]core.Option{core.WithAssembler(&Frame{system: system}), core.WithTools(tools), core.WithGate(gate)}, opts...)...)
}

// NewAgentWithStall builds an agent with a stuck-loop detector connected in the
// four places a host connects it: the frame carries its segment and delivery
// reports reach it, and as a component its step gate is connected and its gate
// wraps core.AllowAll. The detector is switched on; escalation is the test's
// to arm. opts reach core.New after the helper's own.
func NewAgentWithStall(client provider.Client, model, system string, tools core.Registry, opts ...core.Option) (*core.Agent, *stall.Detector) {
	d := &stall.Detector{}
	d.SetEnabled(true)
	a := newAgent(client, model, append([]core.Option{core.WithAssembler(&Frame{system: system, stall: d}), core.WithTools(tools),
		core.WithGate(core.AllowAll), core.WithComponent(d)}, opts...)...)
	return a, d
}

// Frame is a mutable core.ContextAssembler: one Stable segment for the system
// prompt, and one Volatile segment tagged core.TailHost from the host function,
// or from the peek function when the engine asks for core.AssemblePeek and one
// is set, then a stuck-loop detector's segment when NewAgentWithStall built it.
// It is the shape of terva's build.Assembler with none of its content;
// this package cannot import build, whose tests import this package.
type Frame struct {
	mu     sync.Mutex
	system string
	host   func() string
	peek   func() string
	stall  *stall.Detector
}

var (
	_ core.ContextAssembler     = (*Frame)(nil)
	_ core.TailDeliveryObserver = (*Frame)(nil)
)

// FrameOf returns the Frame an agent from NewAgent was built with, or nil.
func FrameOf(ag *core.Agent) *Frame {
	f, _ := ag.Assembler().(*Frame)
	return f
}

// Assemble implements core.ContextAssembler.
func (f *Frame) Assemble(mode core.AssembleMode) core.Frame {
	f.mu.Lock()
	system, fn := f.system, f.host
	if mode == core.AssemblePeek && f.peek != nil {
		fn = f.peek
	}
	f.mu.Unlock()
	var fr core.Frame
	if system != "" {
		fr.Segments = append(fr.Segments, core.Segment{Stability: core.Stable, Tag: "system", Content: system})
	}
	if fn != nil {
		fr.Segments = append(fr.Segments, core.Segment{Stability: core.Volatile, Tag: core.TailHost, Content: fn()})
	}
	if f.stall != nil {
		fr.Segments = append(fr.Segments, f.stall.Segment())
	}
	return fr
}

// TailDelivered implements core.TailDeliveryObserver, for the detector
// NewAgentWithStall connected.
func (f *Frame) TailDelivered(ids []string) {
	if f.stall != nil {
		f.stall.TailDelivered(ids)
	}
}

// SetSystem replaces the system prompt.
func (f *Frame) SetSystem(s string) {
	f.mu.Lock()
	f.system = s
	f.mu.Unlock()
}

// SetHost replaces the host tail.
func (f *Frame) SetHost(fn func() string) {
	f.mu.Lock()
	f.host = fn
	f.mu.Unlock()
}

// SetPeek replaces the tail the engine reads for core.AssemblePeek.
func (f *Frame) SetPeek(fn func() string) {
	f.mu.Lock()
	f.peek = fn
	f.mu.Unlock()
}
