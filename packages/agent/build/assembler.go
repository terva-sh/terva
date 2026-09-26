package build

import (
	"sync"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/contextpressure"
	"terva.sh/terva/packages/core/exp/prefixwatch"
	"terva.sh/terva/packages/core/exp/transport"
	"terva.sh/terva/packages/core/lazytools"
	"terva.sh/terva/packages/core/shellresult"
	"terva.sh/terva/packages/core/stall"
)

// Assembler is terva's core.ContextAssembler: where terva's prompt conventions
// meet an engine that has none.
//
// Its Stable segments are the system prompt's sections from SystemSegments, in
// order, each tagged with its Source, so the engine sends exactly the bytes
// BuildSystemPrompt renders and /context can size each section by name. Its one
// Volatile segment is the host tail, tagged core.TailHost: the ticket and task
// cards, extension cards, memory recall, lore, and a card's steering, composed
// by EphemeralTail and PerTurnContext. The tail stays one segment because two of
// its parts are authored steering that must never be guarded, and its guards are
// applied per part in this package (see core/tail.go). After it come three
// components' segments, each empty unless it has something to say: the
// context-pressure note, a waiting "!" shell result, which is an event and
// belongs next to the message it annotates, and last the stuck-loop note, the
// one block entitled to change what the model does next.
//
// It is mutable, behind its own lock, because terva changes its frame while a
// session is live: a tools rebuild re-renders the system prompt (SetStable), and
// a lore or trust reload re-derives the tail (SetTail). The engine pins the
// Stable segments per turn segment, so a change made mid-turn applies from the
// next one.
type Assembler struct {
	mu       sync.Mutex
	stable   []core.Segment
	system   string // stable's joined text, kept to answer SetStable's changed
	tail     func() string
	peek     func() string
	shell    *shellresult.Slot
	pressure *contextpressure.Tracker
	stall    *stall.Detector

	// The two recorders carry no segment. They live here beside the other
	// components so terva has one place that holds an agent's components, and
	// AssemblerOf reaches all of them.
	watch     *prefixwatch.Watch
	transport *transport.Recorder

	// lazy is lazy tool visibility, or nil where it does not engage. Its note
	// is the last Volatile segment. Guarded by mu.
	lazy *lazytools.Visibility
}

var (
	_ core.ContextAssembler     = (*Assembler)(nil)
	_ core.TailDeliveryObserver = (*Assembler)(nil)
)

// NewAssembler returns an assembler holding segs as its Stable segments, no
// tail, a shell-result slot that is off, a context-pressure tracker that says
// nothing until it is attached to an agent, a stuck-loop detector that is
// off, and a prefix watch and a transport recorder that are off.
func NewAssembler(segs []PromptSegment) *Assembler {
	a := &Assembler{
		shell: &shellresult.Slot{}, pressure: &contextpressure.Tracker{}, stall: &stall.Detector{},
		watch: &prefixwatch.Watch{}, transport: &transport.Recorder{},
	}
	a.SetStable(segs)
	return a
}

// AssemblerOf returns the Assembler ag was built with by Resolved.NewAgent, or
// nil for an agent built some other way.
func AssemblerOf(ag *core.Agent) *Assembler {
	if ag == nil {
		return nil
	}
	a, _ := ag.Assembler().(*Assembler)
	return a
}

// mustAssemblerOf is AssemblerOf for the wiring that exists only to change
// terva's frame. An agent without one is a programming error, not a
// configuration: every host builds its agent through Resolved.NewAgent, and
// wiring that did nothing on another agent would drop the task card, the
// extension cards and the lore without a word.
func mustAssemblerOf(ag *core.Agent, caller string) *Assembler {
	a := AssemblerOf(ag)
	if a == nil {
		panic("build." + caller + ": the agent has no build.Assembler; build it with Resolved.NewAgent, or pass build.NewAssembler to core.New")
	}
	return a
}

// Assemble implements core.ContextAssembler. A peek calls the tail's
// side-effect-free twin, falling back to the tail itself when there is none.
func (a *Assembler) Assemble(mode core.AssembleMode) core.Frame {
	a.mu.Lock()
	segs := make([]core.Segment, len(a.stable), len(a.stable)+4)
	copy(segs, a.stable)
	fn := a.tail
	if mode == core.AssemblePeek && a.peek != nil {
		fn = a.peek
	}
	a.mu.Unlock()
	// Called outside the lock: the tail reads the agent, and the agent calls
	// Assemble outside its own.
	if fn != nil {
		segs = append(segs, core.Segment{Stability: core.Volatile, Tag: core.TailHost, Content: fn()})
	}
	// The inactive-group note goes last, after the other component notes, as
	// it did when the engine appended it itself.
	segs = append(segs, a.pressure.Segment(), a.shell.Segment(), a.stall.Segment())
	if lz := a.Lazy(); lz != nil {
		segs = append(segs, lz.Segment(mode))
	}
	return core.Frame{Segments: segs}
}

// TailDelivered implements core.TailDeliveryObserver for the components whose
// segments depend on what earlier requests carried.
func (a *Assembler) TailDelivered(ids []string) {
	a.pressure.TailDelivered(ids)
	a.shell.TailDelivered(ids)
	a.stall.TailDelivered(ids)
	a.Lazy().TailDelivered(ids)
}

// ContextPressure is the tracker that words the context-pressure note through
// the agent's compaction policy (CompactionPolicy.PressureNote).
func (a *Assembler) ContextPressure() *contextpressure.Tracker { return a.pressure }

// Stall is the stuck-loop detector. Resolved.NewAgent attaches it, wraps the
// host's gate with it and binds the escalation channel; the engine features
// stuck_loop_detection and stuck_loop_escalation switch it.
func (a *Assembler) Stall() *stall.Detector { return a.stall }

// PrefixWatch is the prefix-divergence recorder and cache-cliff detector, an
// experiment (core/exp/prefixwatch). The engine feature
// prefix_divergence_recording switches it, and the workspace hears its cliff
// events to show a note while one runs.
func (a *Assembler) PrefixWatch() *prefixwatch.Watch { return a.watch }

// Transport is the transport recorder, an experiment (core/exp/transport). The
// engine feature transport_recording switches it.
func (a *Assembler) Transport() *transport.Recorder { return a.transport }

// Lazy is lazy tool visibility (core/lazytools), or nil where it does not
// engage. Resolved.NewAgent attaches it; the engine feature
// activation_continuation and the per-provider override switch its
// continuation. A nil *lazytools.Visibility is safe to call.
func (a *Assembler) Lazy() *lazytools.Visibility {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lazy
}

func (a *Assembler) setLazy(v *lazytools.Visibility) {
	a.mu.Lock()
	a.lazy = v
	a.mu.Unlock()
}

// ShellResult is the slot that carries a "!" escape's result into the next
// request. The engine feature shell_result_context switches it and the
// daemon's shell.result verb fills it.
func (a *Assembler) ShellResult() *shellresult.Slot { return a.shell }

// SetStable replaces the Stable segments and reports whether the system prompt
// they render changed. The daemon uses the answer to tell clients the prompt
// cache is about to miss. A retag alone reports no change, because the bytes a
// provider caches are the same.
func (a *Assembler) SetStable(segs []PromptSegment) (changed bool) {
	stable := make([]core.Segment, len(segs))
	for i, s := range segs {
		stable[i] = core.Segment{Stability: core.Stable, Tag: s.Source, Content: s.Text}
	}
	system := core.Frame{Segments: stable}.SystemText()
	a.mu.Lock()
	defer a.mu.Unlock()
	changed = a.system != system
	a.stable, a.system = stable, system
	return changed
}

// SetTail replaces the host tail: tail for a request, and peek, its twin
// without per-request side effects, for the /context view and the engine's
// prefix checks. Either may be nil.
func (a *Assembler) SetTail(tail, peek func() string) {
	a.mu.Lock()
	a.tail, a.peek = tail, peek
	a.mu.Unlock()
}

// wrapTail replaces the tail pair with wrap applied to each, in one step, so a
// request cannot see one wrapped and the other not.
func (a *Assembler) wrapTail(wrap func(func() string) func() string) {
	a.mu.Lock()
	a.tail, a.peek = wrap(a.tail), wrap(a.peek)
	a.mu.Unlock()
}
