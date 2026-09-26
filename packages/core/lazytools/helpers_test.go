package lazytools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// frame is terva's assembler in miniature: the system prompt as a Stable
// segment, then the note. It forwards tail delivery to the Visibility, as
// terva's assembler does, so the note's decay advances only when a request
// actually carried it.
type frame struct {
	system string
	v      *Visibility
}

func (f *frame) Assemble(mode core.AssembleMode) core.Frame {
	return core.Frame{Segments: []core.Segment{
		{Stability: core.Stable, Tag: "system", Content: f.system},
		f.v.Segment(mode),
	}}
}

func (f *frame) TailDelivered(ids []string) { f.v.TailDelivered(ids) }

var (
	_ core.ContextAssembler     = (*frame)(nil)
	_ core.TailDeliveryObserver = (*frame)(nil)
)

// newAgent builds an agent with lazy tools connected as a component: v
// advertises the core group plus active, and the frame carries v's note on
// the tail.
func newAgent(client provider.Client, tools core.Registry, gate core.Gate, active ...string) (*core.Agent, *Visibility) {
	v := New(active...)
	a, err := core.New(client, "m", core.WithAssembler(&frame{system: "sys", v: v}),
		core.WithTools(tools), core.WithGate(gate), core.WithComponent(v))
	if err != nil {
		panic(err)
	}
	return a, v
}

// flagTool is a minimal tool that records whether Execute ran, so a test can
// prove a hidden tool still dispatched.
type flagTool struct {
	name     string
	executed bool
}

func (f *flagTool) Name() string            { return f.name }
func (f *flagTool) Description() string     { return "d-" + f.name }
func (f *flagTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f *flagTool) Execute(context.Context, json.RawMessage, func(string)) (core.ToolResult, error) {
	f.executed = true
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: "ok"}}}, nil
}

func specNames(specs []provider.Tool) map[string]bool {
	m := make(map[string]bool, len(specs))
	for _, s := range specs {
		m[s.Name] = true
	}
	return m
}

// textOf joins a message's text blocks, for a failure message.
func textOf(msg provider.Message) string {
	var out string
	for _, c := range msg.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			if out != "" {
				out += "\n"
			}
			out += tb.Text
		}
	}
	return out
}

// extToolFake wraps a flagTool with an Extension() accessor, placing it in a
// capability group like a real extension / MCP tool.
type extToolFake struct {
	*flagTool
	group     string
	essential bool
}

func (e *extToolFake) Extension() string { return e.group }
func (e *extToolFake) Essential() bool   { return e.essential }

func extTool(name, group string) *extToolFake {
	return &extToolFake{flagTool: &flagTool{name: name}, group: group}
}

func essentialExtTool(name, group string) *extToolFake {
	return &extToolFake{flagTool: &flagTool{name: name}, group: group, essential: true}
}

// groupActivatorTool is a core tool (no Extension()) that activates a group
// when called — a stand-in for the activate_tools tool, so a test can drive a
// mid-turn activation. It reaches the Visibility the way activate_tools does,
// through the agent on the call's context.
type groupActivatorTool struct{ group string }

func (g *groupActivatorTool) Name() string            { return "activate" }
func (g *groupActivatorTool) Description() string     { return "activates a group" }
func (g *groupActivatorTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (g *groupActivatorTool) Execute(ctx context.Context, _ json.RawMessage, _ func(string)) (core.ToolResult, error) {
	Of(core.AgentFromContext(ctx)).Activate(g.group)
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: "activated"}}}, nil
}

// multiActivatorTool activates several groups in a single call, so one tool
// batch can dirty more than one group.
type multiActivatorTool struct{ groups []string }

func (m *multiActivatorTool) Name() string            { return "activate" }
func (m *multiActivatorTool) Description() string     { return "activates groups" }
func (m *multiActivatorTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (m *multiActivatorTool) Execute(ctx context.Context, _ json.RawMessage, _ func(string)) (core.ToolResult, error) {
	v := Of(core.AgentFromContext(ctx))
	for _, g := range m.groups {
		v.Activate(g)
	}
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: "activated"}}}, nil
}

// reqCaptureClient records req.Tools and req.EphemeralContext for every call. If
// toolThen is set, its first call (every odd call with toolEveryOdd) drives one
// tool call before ending. onCall, when set, runs at the top of request n — a
// test hook for acting mid-"reply" (e.g. queueing a message while the model
// speaks).
type reqCaptureClient struct {
	mu           sync.Mutex
	tools        [][]provider.Tool
	ephemeral    []string
	toolThen     string
	toolEveryOdd bool
	onCall       func(n int)
	calls        int
}

func (c *reqCaptureClient) Name() string { return "reqcap" }

func (c *reqCaptureClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.tools = append(c.tools, req.Tools)
	c.ephemeral = append(c.ephemeral, req.EphemeralContext)
	hook := c.onCall
	c.mu.Unlock()
	if hook != nil {
		hook(n)
	}

	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "reqcap", Model: req.Model}
		if c.toolThen != "" && (n == 1 || (c.toolEveryOdd && n%2 == 1)) {
			out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{ID: "t1", Name: c.toolThen, Arguments: json.RawMessage(`{}`)}},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "done"}},
		}}
	}()
	return out, nil
}

// seqToolClient plays a fixed sequence of tool calls (one per step), ending
// naturally once the sequence is exhausted — so a test can drive "activate on
// step 1, then use the newly visible tool on step 2". Captures req.Tools.
type seqToolClient struct {
	mu    sync.Mutex
	tools [][]provider.Tool
	seq   []string // tool to call at each step; past the end, the step ends
	calls int
}

func (c *seqToolClient) Name() string { return "seq" }

func (c *seqToolClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	n := c.calls
	c.calls++
	c.tools = append(c.tools, req.Tools)
	c.mu.Unlock()

	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "seq", Model: req.Model}
		if n < len(c.seq) && c.seq[n] != "" {
			out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{ID: fmt.Sprintf("s%d", n), Name: c.seq[n], Arguments: json.RawMessage(`{}`)}},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "done"}},
		}}
	}()
	return out, nil
}

// countingStore is a MemoryTranscriptStore that also counts every tool_group
// row written to it. The memory store dedupes groups on read, so its
// Transcript alone cannot show a duplicate write.
type countingStore struct {
	*core.MemoryTranscriptStore
	mu   sync.Mutex
	rows []string
}

func (s *countingStore) AppendToolGroupActivation(group string) error {
	s.mu.Lock()
	s.rows = append(s.rows, group)
	s.mu.Unlock()
	return s.MemoryTranscriptStore.AppendToolGroupActivation(group)
}

func (s *countingStore) groupRows() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.rows...)
}
