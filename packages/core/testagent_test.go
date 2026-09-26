package core

import (
	"sync"

	"terva.sh/terva/packages/provider"
)

// newTestAgent builds an agent for a test that exercises the loop rather than
// the permission gate. Most tests in this package construct through it or
// newAgentOver. This package's tests cannot import
// packages/agent/internal/coretest, which imports core, so the two stay
// separate copies.
//
// It passes AllowAll, so every tool call runs.
func newTestAgent(client provider.Client, model, system string, tools Registry) *Agent {
	return newTestAgentWithGate(client, model, system, tools, AllowAll)
}

// newTestAgentWithGate builds an agent whose tool calls go through gate, for a
// test that exercises the gate.
//
// The system prompt goes into a testFrame, which a test reaches through
// testFrameOf to change the frame mid-session the way a host's assembler would.
func newTestAgentWithGate(client provider.Client, model, system string, tools Registry, gate Gate) *Agent {
	return mustNew(client, model, WithAssembler(&testFrame{system: system}), WithTools(tools), WithGate(gate), WithCatalog(testCatalog))
}

// newAgentOver builds an agent over asm with no tools, for a test of the
// assembler seam. A nil asm is an empty frame.
func newAgentOver(client provider.Client, model string, asm ContextAssembler) *Agent {
	return mustNew(client, model, WithAssembler(asm), WithTools(Registry{}), WithGate(AllowAll))
}

// mustNew is New for a test helper, which has no error to return. A
// configuration New refuses panics.
func mustNew(client provider.Client, model string, opts ...Option) *Agent {
	a, err := New(client, model, opts...)
	if err != nil {
		panic(err)
	}
	return a
}

// testCatalog is the registry this package's tests write model rows into.
// newTestAgent's agents read it; an agent a test builds another way calls
// SetCatalog(testCatalog) itself.
var testCatalog = provider.NewRegistry()

// testFrame is a mutable ContextAssembler: one Stable segment for the system
// prompt, and one Volatile segment tagged TailHost from host, or from peek when
// the engine asks for AssemblePeek and peek is set. It is the shape of terva's
// own assembler with none of its content.
type testFrame struct {
	mu     sync.Mutex
	system string
	host   func() string
	peek   func() string
	// note, when set, is a component note after the host segment, told what
	// each request carried.
	note *decayNote
}

func (f *testFrame) Assemble(mode AssembleMode) Frame {
	f.mu.Lock()
	system, fn := f.system, f.host
	if mode == AssemblePeek && f.peek != nil {
		fn = f.peek
	}
	f.mu.Unlock()
	var fr Frame
	if system != "" {
		fr.Segments = append(fr.Segments, Segment{Stability: Stable, Tag: "system", Content: system})
	}
	if fn != nil {
		fr.Segments = append(fr.Segments, Segment{Stability: Volatile, Tag: TailHost, Content: fn()})
	}
	if note := f.noteOf(); note != nil {
		fr.Segments = append(fr.Segments, note.segment())
	}
	return fr
}

func (f *testFrame) noteOf() *decayNote {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.note
}

// TailDelivered implements TailDeliveryObserver for the note.
func (f *testFrame) TailDelivered(ids []string) {
	if note := f.noteOf(); note != nil {
		note.delivered(ids)
	}
}

func (f *testFrame) setNote(n *decayNote) {
	f.mu.Lock()
	f.note = n
	f.mu.Unlock()
}

func (f *testFrame) setSystem(s string) {
	f.mu.Lock()
	f.system = s
	f.mu.Unlock()
}

func (f *testFrame) setHost(fn func() string) {
	f.mu.Lock()
	f.host = fn
	f.mu.Unlock()
}

func (f *testFrame) setPeek(fn func() string) {
	f.mu.Lock()
	f.peek = fn
	f.mu.Unlock()
}

// testFrameOf returns the testFrame an agent from newTestAgent was built with.
func testFrameOf(a *Agent) *testFrame { return a.assembler.(*testFrame) }

// neutralProse is the compaction text an agent with no policy of its own
// sends: the neutral default, filled in by the engine.
func neutralProse() CompactionPrompts { return (&Agent{}).compactionPrompts() }
