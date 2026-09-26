// Package stall is the stuck-loop hatch: a detector that watches a turn's
// tool steps for a model repeating itself without progress, and the ladder it
// climbs when it finds one. Rung 1 is a nudge on the ephemeral tail, rung 2 a
// firmer hold-off, rung 3 an escalation to a stronger model (or, where that
// cannot act, the hold-off in its place), then a refusal to run the call, and
// last the end of the turn. tracker.go holds the detector and its reasoning,
// escalate.go the later rungs, inner.go the host calls a tool makes on its own
// behalf.
//
// It is a component, not part of the engine (decision 0021,
// docs/plans/stall-component.md). A host that wants it holds a Detector and
// connects it in three places:
//
//   - It passes the Detector to core.WithComponent. That connects its step
//     gate, where it resets per prompt, reads each tool batch, escalates, and
//     ends a turn. Through WrapGate it also wraps the host's gate, outermost,
//     so a call proven redundant is answered without running and without
//     reaching a permission prompt.
//   - The host's assembler adds Segment() to every frame, which carries the
//     nudge, the hold-off or the handoff marker.
//   - The assembler implements core.TailDeliveryObserver and forwards the IDs
//     to TailDelivered, which spends that note.
//
// The records it produces, core.StallRecord and core.EscalationRecord, stay in
// the engine: the wire and the session store name them. The detector emits
// them as core.EvStall and core.EvEscalation, and a store writes them as rows
// from those events.
package stall

import (
	"context"
	"encoding/json"
	"sync"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// ID is the segment's tag, and so its tail block ID in events and recorded
// tail rows. It was core.TailStall while this lived in the engine, and kept
// its value so recorded rows read the same.
const ID = "stall"

// Detector is the stuck-loop hatch for one agent. The zero value is switched
// off, has no escalation, and asks nothing until it is attached.
//
// It is safe for concurrent use. The engine calls the step gate, the tool gate
// and TailDelivered from the turn loop, a tool reports inner calls from its own
// goroutine, and a frame preview may assemble from another. One lock guards the
// tracker, and it is never held across a question, an escalation or an event.
type Detector struct {
	mu sync.Mutex
	t  tracker

	// on arms the detector (engine feature stuck_loop_detection). escalate arms
	// rung 3 (stuck_loop_escalation); it depends on on, because the detector is
	// the trigger, and it is inert without an Escalator and a configured
	// target. auto swaps without asking (config escalation.auto).
	on, escalate, auto bool
	escalator          Escalator

	// agent is the one Bind was given. Rung 3 reads its Asker and its Model.
	agent *core.Agent

	// refusals are the rung-3 records the tool gate raised during the batch
	// running now. A gate has no sink, so the step gate emits them when the
	// batch ends.
	refusals []core.StallRecord
}

// SetEnabled switches the detector (engine feature stuck_loop_detection). Off
// makes every hook a no-op, except that a prompt's start still resets it, so a
// detector switched back on does not inherit a run from before.
func (d *Detector) SetEnabled(on bool) {
	d.mu.Lock()
	d.on = on
	d.mu.Unlock()
}

// Enabled reports whether the detector is armed. A host shows it, and a test of
// a host's shipped default reads it.
func (d *Detector) Enabled() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.on
}

// SetEscalation switches rung 3 (engine feature stuck_loop_escalation). It is
// inert while the detector is off, without an Escalator, and without a
// configured target.
func (d *Detector) SetEscalation(on bool) {
	d.mu.Lock()
	d.escalate = on
	d.mu.Unlock()
}

// EscalationEnabled reports whether rung 3 is armed.
func (d *Detector) EscalationEnabled() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.escalate
}

// SetEscalateAuto switches unasked escalation (config escalation.auto). Off,
// the user is asked before a swap that sends their transcript elsewhere.
func (d *Detector) SetEscalateAuto(on bool) {
	d.mu.Lock()
	d.auto = on
	d.mu.Unlock()
}

// EscalateAutoEnabled reports the auto-escalate policy.
func (d *Detector) EscalateAutoEnabled() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.auto
}

// SetEscalator binds the host's channel for swapping to a stronger model. Nil,
// the normal state for a host with no swap target, leaves rung 3 inert, and
// the hold-off speaks in its place.
func (d *Detector) SetEscalator(e Escalator) {
	d.mu.Lock()
	d.escalator = e
	d.mu.Unlock()
}

// Bind implements core.Binder: it keeps the agent the detector reports on,
// and renders the detector's notes in that agent's language. A host passes
// the detector to core.WithComponent, which calls Bind and connects its step
// gate and its gate wrapper. A detector serves one agent.
func (d *Detector) Bind(a *core.Agent) {
	d.mu.Lock()
	d.agent = a
	d.t.tr = a.Translator()
	d.mu.Unlock()
}

// StepGate implements core.StepGater.
func (d *Detector) StepGate() core.StepGate { return d.stepGate() }

// WrapGate implements core.GateWrapper with Gate, so the detector's refusal
// wraps the host's gate. A host that moves to core.WithComponent(d) passes its
// own gate to core.WithGate and drops the d.Gate(inner) it wrote for
// New. One that keeps it gets the gate back unchanged: WrapGate finds
// d's refusal already in inner's Unwrap chain, and a second copy would ask
// the tracker twice for every call.
func (d *Detector) WrapGate(inner core.Gate) core.Gate {
	for g := inner; g != nil; {
		if r, ok := g.(refusalGate); ok && r.d == d {
			return inner
		}
		u, ok := g.(interface{ Unwrap() core.Gate })
		if !ok {
			break
		}
		g = u.Unwrap()
	}
	return d.Gate(inner)
}

var (
	_ core.Binder      = (*Detector)(nil)
	_ core.StepGater   = (*Detector)(nil)
	_ core.GateWrapper = (*Detector)(nil)
)

// stepGate is how the detector follows the turn loop.
func (d *Detector) stepGate() core.StepGate {
	return core.StepGate{
		Cause: "stall",
		// Stuck-loop counting is scoped to one prompt's tool steps: a repeat
		// across prompts is usually the user asking again, not the model stuck.
		// reset keeps the one thing that reading does not explain, a signature
		// still recurring when the last prompt ended, so a loop spanning the
		// boundary resumes its ladder rather than being handed a fresh budget.
		// Unconditional: a detector switched back on must not inherit a run from
		// before it was switched off.
		Begin: func() {
			d.mu.Lock()
			d.t.reset()
			// A refusal queued by a batch that never finished (the turn was
			// cancelled mid-batch) describes a step no After will see.
			d.refusals = nil
			d.mu.Unlock()
		},
		// Feed the step to the detector; a trip stages a one-request nudge that
		// the next request rides on the ephemeral tail (never the transcript).
		// If the loop has persisted past the nudge, rung 3 may offer to escalate
		// to a stronger model, and where it cannot (no Escalator, no configured
		// target, nobody to consent, which between them are the default state)
		// rung 2 speaks once more instead of leaving the loop unremarked. A
		// user-chosen "stop" ends the turn cleanly; an escalation continues the
		// loop on the new model with a handoff marker staged.
		After: d.after,
		Inner: d.recordInner,
	}
}

func (d *Detector) after(ctx context.Context, s core.StepState, emit func(core.AgentEvent)) bool {
	d.mu.Lock()
	refused := d.refusals
	d.refusals = nil
	if !d.on {
		d.mu.Unlock()
		// A refusal made before the detector was switched off still happened.
		emitStalls(emit, refused)
		return false
	}
	var nudges []core.StallRecord
	for _, ev := range d.t.observe(s.Assistant, s.Results) {
		nudges = append(nudges, core.StallRecord{Axis: ev.axis, Tool: ev.tool, Detail: ev.detail, Rung: 1})
	}
	d.mu.Unlock()
	// One event is both copies: the observers write the session row
	// (AttachTranscriptStore), and the sink reaches clients. The refusals come
	// first, because they happened during the batch.
	emitStalls(emit, refused)
	emitStalls(emit, nudges)
	if d.maybeEscalate(ctx, emit) {
		return true
	}
	// And when even refusing to run the call did not break it, the turn ends.
	// Checked after escalation so a swap still wins: the incoming model is
	// pardoned there and gets its own strikes.
	return d.giveUp(emit)
}

func emitStalls(emit func(core.AgentEvent), recs []core.StallRecord) {
	for _, r := range recs {
		emit(core.EvStall{StallRecord: r})
	}
}

// Segment is the Volatile segment for the next request: the staged note, or
// empty. Side-effect free, because the engine re-assembles per retry attempt
// and the note must ride every attempt until one lands.
func (d *Detector) Segment() core.Segment {
	seg := core.Segment{Stability: core.Volatile, Tag: ID}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.on {
		seg.Content = d.t.nudge()
	}
	return seg
}

// TailDelivered spends the staged note. It clears on every report, not only
// one that names ID: a request has landed, so the note rode it or the engine
// suppressed the tail for a continue turn. A note lives for one turn (Begin
// resets it), so a continue turn starts with nothing staged and clearing is a
// no-op there.
func (d *Detector) TailDelivered([]string) {
	d.mu.Lock()
	d.t.clearNudge()
	d.mu.Unlock()
}

// Gate wraps inner with the detector's last rung before the turn ends: a call
// it has already proved redundant is answered with a refusal and never
// reaches inner, so no permission prompt asks about a call that will not run.
// Every other call is inner's to decide. A host puts it outermost.
//
// Every earlier rung is a note the model may agree with and ignore; this one
// changes the RESULT, which is the only channel a determined loop is still
// reading.
//
// The engine checks an unknown tool and unparseable arguments before it asks
// the gate, so those calls get the engine's answer rather than the refusal.
// Both tell the model the call did not run.
//
// The wrapper has an Unwrap method that returns inner, so a host or a test can
// still see which ladder it built. A nil inner returns nil, so core.New
// refuses it as it would the bare nil: wrapping must not turn a missing gate
// into one that fails per call.
func (d *Detector) Gate(inner core.Gate) core.Gate {
	if inner == nil {
		return nil
	}
	return refusalGate{d: d, inner: inner}
}

// refusalGate is the gate Detector.Gate returns.
type refusalGate struct {
	d     *Detector
	inner core.Gate
}

var _ core.Gate = refusalGate{}

// CheckTool implements core.Gate.
func (g refusalGate) CheckTool(ctx context.Context, call provider.ToolCallBlock, tool core.Tool) (bool, string, json.RawMessage) {
	d := g.d
	d.mu.Lock()
	reason, refused := "", false
	if d.on {
		reason, refused = d.t.refuse(call)
		if refused {
			d.refusals = append(d.refusals, core.StallRecord{Axis: stallAxisSpin, Tool: call.Name, Rung: 3})
		}
	}
	d.mu.Unlock()
	if refused {
		return false, reason, nil
	}
	return g.inner.CheckTool(ctx, call, tool)
}

// Unwrap returns the gate this one wraps.
func (g refusalGate) Unwrap() core.Gate { return g.inner }
