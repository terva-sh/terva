package stall

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// These pin how the detector connects to a host: its segment, the delivery
// report that spends it, the gate it wraps, and the step gate that emits what
// the gate refused. The ladder itself is tested in the files named for it.

// The segment carries whatever note is staged, under the tag recorded tail
// rows have always used, and nothing while the detector is off.
func TestTheSegmentCarriesTheStagedNote(t *testing.T) {
	d := on()
	d.t.stageHandoff("[loop check] note")
	seg := d.Segment()
	if seg.Tag != "stall" || seg.Stability != core.Volatile {
		t.Errorf("segment = %+v, want a Volatile segment tagged stall", seg)
	}
	if seg.Content != "[loop check] note" {
		t.Errorf("content = %q, want the staged note", seg.Content)
	}
	// Reading it spends nothing: a retried request must carry it again.
	if d.Segment().Content == "" {
		t.Error("assembling the segment spent the note")
	}
	d.SetEnabled(false)
	if c := d.Segment().Content; c != "" {
		t.Errorf("a detector switched off still sent %q", c)
	}
}

// Any delivery report spends the note, including one that does not name it: a
// request landed, and either the note rode it or the tail was suppressed.
func TestADeliveryReportSpendsTheNote(t *testing.T) {
	d := on()
	d.t.stageHandoff("[loop check] note")
	d.TailDelivered(nil)
	if c := d.Segment().Content; c != "" {
		t.Errorf("the note survived a delivered request: %q", c)
	}
}

// A note is scoped to the turn that staged it: the prompt's start resets the
// detector, so a note staged before a prompt never rides that prompt.
func TestANudgeDoesNotSurviveATurnBoundary(t *testing.T) {
	c := &scriptedClient{name: "scripted", script: func(int, provider.Request) ([]provider.Event, error) {
		return saidText("done", 100), nil
	}}
	d := on()
	a := newAgent(c, "m", core.Registry{}, d)
	d.t.stageHandoff("[stuck loop] you have called read on the same file four times")

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if strings.Contains(c.calls()[0].EphemeralContext, "stuck loop") {
		t.Error("a nudge armed before the turn was delivered; it is scoped to the turn that armed it")
	}
}

// countingGate allows every call and counts the calls it was asked about.
type countingGate struct {
	asked int
	deny  bool
}

func (g *countingGate) CheckTool(context.Context, provider.ToolCallBlock, core.Tool) (bool, string, json.RawMessage) {
	g.asked++
	if g.deny {
		return false, "the user said no", nil
	}
	return true, "", nil
}

// The wrapper refuses a proven-redundant call without asking the gate it
// wraps, so no permission prompt asks about a call that will not run. Every
// other call is the inner gate's to decide, a denial included.
func TestTheGateAnswersARedundantCallWithoutAskingTheInnerGate(t *testing.T) {
	d := on()
	inner := &countingGate{}
	g := d.Gate(inner)

	ok, _, _ := g.CheckTool(context.Background(), updateCall(), nil)
	if !ok || inner.asked != 1 {
		t.Fatalf("an ordinary call: allowed=%v, inner asked %d times; want allowed after one ask", ok, inner.asked)
	}
	inner.deny = true
	if ok, reason, _ := g.CheckTool(context.Background(), updateCall(), nil); ok || reason != "the user said no" {
		t.Errorf("the inner gate's denial did not come through: allowed=%v reason=%q", ok, reason)
	}
	inner.deny = false

	noopUpdate(&d.t, stallRefuseAt)
	asked := inner.asked
	ok, reason, _ := g.CheckTool(context.Background(), updateCall(), nil)
	if ok {
		t.Fatal("a call proven redundant was allowed")
	}
	if !strings.Contains(reason, "did not run") {
		t.Errorf("the refusal should say the call did not run:\n%s", reason)
	}
	if inner.asked != asked {
		t.Error("the inner gate was asked about a call the detector refused")
	}
}

// Wrapping a missing gate must not hide it: core.New refuses a nil gate,
// and a wrapper that called a nil inner would fail per call instead.
func TestWrappingANilGateIsNil(t *testing.T) {
	if g := on().Gate(nil); g != nil {
		t.Errorf("Gate(nil) = %v, want nil", g)
	}
}

// The host's ladder stays visible through the wrapper, so a test of a host's
// gate can still ask which one it built.
func TestTheWrapperUnwrapsToTheHostsGate(t *testing.T) {
	inner := &countingGate{}
	u, ok := on().Gate(inner).(interface{ Unwrap() core.Gate })
	if !ok || u.Unwrap() != core.Gate(inner) {
		t.Error("the wrapper does not unwrap to the gate it was given")
	}
}

// A host moving to core.WithComponent(d) may keep the d.Gate(inner) it wrote
// for New. The engine then wraps once, not twice, so the tracker is asked
// once per call. Another detector's refusal is not d's, and d still wraps it.
func TestWithComponentDoesNotWrapAGateTwice(t *testing.T) {
	d := on()
	own := d.Gate(&countingGate{})
	a, err := core.New(nil, "m", core.WithGate(own), core.WithComponent(d))
	if err != nil {
		t.Fatal(err)
	}
	if a.Gate() != own {
		t.Errorf("the agent's gate is %T; want the host's own d.Gate, not a second wrapper around it", a.Gate())
	}

	other := on().Gate(&countingGate{})
	if d.WrapGate(other) == other {
		t.Error("d did not wrap a gate that holds another detector's refusal")
	}
}

// A gate has no sink, so a refusal is queued and the step gate emits it when
// the batch ends, ahead of the nudges that batch produced. A refusal made
// before the detector was switched off is still emitted, because it happened.
func TestARefusalIsEmittedWhenTheBatchEnds(t *testing.T) {
	d := on()
	noopUpdate(&d.t, stallRefuseAt)
	if ok, _, _ := d.Gate(core.AllowAll).CheckTool(context.Background(), updateCall(), nil); ok {
		t.Fatal("precondition: the call should have been refused")
	}
	d.SetEnabled(false)
	var got []core.StallRecord
	d.after(context.Background(), core.StepState{}, collectStalls(&got))
	if len(got) != 1 || got[0].Rung != 3 || got[0].Tool != "task_update" {
		t.Fatalf("the queued refusal should be emitted once as rung 3, got %+v", got)
	}
	d.after(context.Background(), core.StepState{}, collectStalls(&got))
	if len(got) != 1 {
		t.Errorf("a refusal was emitted twice: %+v", got)
	}
}

// A refusal queued by a batch that never finished, because the turn was
// cancelled, is dropped when the next prompt begins: it describes a step no
// After will see.
func TestAQueuedRefusalDoesNotOutliveItsPrompt(t *testing.T) {
	d := on()
	noopUpdate(&d.t, stallRefuseAt)
	d.Gate(core.AllowAll).CheckTool(context.Background(), updateCall(), nil)
	d.stepGate().Begin()
	var got []core.StallRecord
	d.after(context.Background(), core.StepState{}, collectStalls(&got))
	if len(got) != 0 {
		t.Errorf("a refusal from the last prompt was emitted in this one: %+v", got)
	}
}
