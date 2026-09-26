package stall

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// These pin the F3 finding in
// docs/reviews/2026-08-29-local-model-harness-friction-review.md: a loop that
// happens entirely inside a code_execution script, where the model's own call
// looks different and productive every time.

// innerFail reports one unproductive inner call against an outer call id, as
// the step gate's Inner hears it from core.ReportInnerCall. That core routes
// a tool's report there is core's to prove (step_gate_test.go); the path
// through a real dispatch is TestStallInnerAttributionFlowsThroughADispatch.
func innerFail(d *Detector, outerID, tool, text string) {
	d.recordInner(outerID, tool, core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: text}},
		IsError: true,
	})
}

// scriptTurn is one code_execution call in the recorded shape: a DIFFERENT
// script each time (so the arguments never repeat), a DIFFERENT printed result
// each time (so the result fingerprint never repeats), and the same inner
// failure underneath. Neither outer axis can see this; only the inner one can.
func scriptTurn(d *Detector, i int, innerTool, innerErr string) {
	id := fmt.Sprintf("c%d", i)
	innerFail(d, id, innerTool, innerErr)
	d.t.observe(
		call(id, "code_execution", fmt.Sprintf(`{"script":"attempt %d"}`, i)),
		result(id, fmt.Sprintf("EVAL_ERR variant %d", i), false),
	)
}

// The headline case. Three scripts that differ in every way the detector could
// previously observe, failing identically where it could not.
func TestStallInnerChurnTripsThroughAProductiveOuterResult(t *testing.T) {
	d := on()
	for i := 0; i < stallThreshold; i++ {
		scriptTurn(d, i, "read", "no such file or directory")
	}
	nudge := d.t.nudge()
	if nudge == "" {
		t.Fatal("three scripts failing the same way inside must trip the churn axis")
	}
	if !strings.Contains(nudge, "code_execution") {
		t.Errorf("the nudge should name the tool the model actually called:\n%s", nudge)
	}
	// The detail has to name the INNER tool, or the model is shown an error it
	// never saw against a call it did not make.
	if !strings.Contains(nudge, "read") {
		t.Errorf("the nudge should name the inner tool that kept failing:\n%s", nudge)
	}
	if !strings.Contains(nudge, "no such file or directory") {
		t.Errorf("the nudge should carry the inner error:\n%s", nudge)
	}
}

// Guards the premise of the test above: without the inner signal these calls
// are invisible, so the fixture really does isolate what was added. If this
// ever trips, the headline test proves nothing.
func TestStallInnerFixtureIsInvisibleToBothOuterAxes(t *testing.T) {
	d := on()
	for i := 0; i < stallWindow; i++ {
		id := fmt.Sprintf("c%d", i)
		// Identical to scriptTurn, minus the inner report.
		d.t.observe(
			call(id, "code_execution", fmt.Sprintf(`{"script":"attempt %d"}`, i)),
			result(id, fmt.Sprintf("EVAL_ERR variant %d", i), false),
		)
	}
	if n := d.t.nudge(); n != "" {
		t.Fatalf("the fixture must be invisible to the outer axes, or the inner test is not testing the inner path:\n%s", n)
	}
}

// One outer call contributes ONE churn step, however many host calls the
// script made. A script that probes ten paths and finds ten missing is doing
// its job; counting ten would trip the threshold inside a single call.
func TestStallInnerOneChurnStepPerOuterCall(t *testing.T) {
	d := on()

	innerFail(d, "c0", "read", "no such file or directory")
	for i := 0; i < 9; i++ { // nine more of the same, all inside ONE script
		innerFail(d, "c0", "read", "no such file or directory")
	}
	d.t.observe(
		call("c0", "code_execution", `{"script":"probe ten paths"}`),
		result("c0", "10 missing", false),
	)
	if n := d.t.nudge(); n != "" {
		t.Fatalf("ten failures inside ONE call is a probing script, not a loop:\n%s", n)
	}

	// Two more outer calls, one inner failure each, and the threshold is met
	// legitimately — three calls the model chose to make.
	scriptTurn(d, 1, "read", "no such file or directory")
	if n := d.t.nudge(); n != "" {
		t.Fatalf("two outer calls is still below the threshold:\n%s", n)
	}
	scriptTurn(d, 2, "read", "no such file or directory")
	if d.t.nudge() == "" {
		t.Fatal("three outer calls failing the same way inside must trip")
	}
}

// An outer result that classifies on its own keeps precedence: the inner
// signal is a fallback for the blind spot, not a replacement for the axis that
// already worked.
func TestStallInnerYieldsToAnUnproductiveOuterResult(t *testing.T) {
	d := on()
	for i := 0; i < stallThreshold; i++ {
		id := fmt.Sprintf("c%d", i)
		innerFail(d, id, "read", "INNER-ERROR-TEXT")
		d.t.observe(
			call(id, "code_execution", fmt.Sprintf(`{"script":"attempt %d"}`, i)),
			result(id, "OUTER-ERROR-TEXT", true),
		)
	}
	nudge := d.t.nudge()
	if nudge == "" {
		t.Fatal("precondition: an outer error repeated three times should trip on its own")
	}
	if !strings.Contains(nudge, "OUTER-ERROR-TEXT") {
		t.Errorf("the outer error should be reported, it is what the model can see:\n%s", nudge)
	}
	if strings.Contains(nudge, "INNER-ERROR-TEXT") {
		t.Errorf("the inner signal must not displace a classifying outer result:\n%s", nudge)
	}
}

// A script whose host calls all succeed contributes nothing. Only unproductive
// inner results are recorded, so ordinary scripted work never accumulates.
func TestStallInnerProductiveCallsRecordNothing(t *testing.T) {
	d := on()
	for i := 0; i < stallWindow; i++ {
		id := fmt.Sprintf("c%d", i)
		for range 5 {
			d.recordInner(id, "read", core.ToolResult{
				Content: []provider.Content{provider.TextBlock{Text: "file contents"}},
			})
		}
		d.t.observe(
			call(id, "code_execution", fmt.Sprintf(`{"script":"attempt %d"}`, i)),
			result(id, fmt.Sprintf("result %d", i), false),
		)
	}
	if n := d.t.nudge(); n != "" {
		t.Fatalf("a script whose host calls succeed is working, not stalling:\n%s", n)
	}
}

// When a script fails several ways, the class it failed on MOST carries the
// step, so the nudge reports the dominant problem rather than whichever error
// happened to land last.
func TestStallInnerDominantClassCarriesTheStep(t *testing.T) {
	d := on()
	for i := 0; i < stallThreshold; i++ {
		id := fmt.Sprintf("c%d", i)
		innerFail(d, id, "grep", "RARE-FAILURE")
		innerFail(d, id, "read", "COMMON-FAILURE")
		innerFail(d, id, "read", "COMMON-FAILURE")
		d.t.observe(
			call(id, "code_execution", fmt.Sprintf(`{"script":"attempt %d"}`, i)),
			result(id, fmt.Sprintf("printed %d", i), false),
		)
	}
	nudge := d.t.nudge()
	if nudge == "" {
		t.Fatal("the dominant inner failure should still trip the churn axis")
	}
	if !strings.Contains(nudge, "COMMON-FAILURE") {
		t.Errorf("the nudge should report the dominant failure:\n%s", nudge)
	}
	if strings.Contains(nudge, "RARE-FAILURE") {
		t.Errorf("the nudge should not report the minority failure:\n%s", nudge)
	}
}

// An attribution is consumed by the step it belongs to and can never be folded
// into a second one — otherwise one script's failure would keep counting
// against later calls that did not repeat it.
func TestStallInnerAttributionIsConsumedOnce(t *testing.T) {
	d := on()
	innerFail(d, "c0", "read", "boom")
	d.t.observe(
		call("c0", "code_execution", `{"script":"one"}`),
		result("c0", "printed", false),
	)
	if _, ok := d.t.takeInner("c0"); ok {
		t.Fatal("the attribution should have been drained by the step that used it")
	}
	// And an outer result that classified on its own still drains, so nothing
	// is left behind for a later call to inherit.
	innerFail(d, "c1", "read", "boom")
	d.t.observe(
		call("c1", "code_execution", `{"script":"two"}`),
		result("c1", "outer failure", true),
	)
	if _, ok := d.t.takeInner("c1"); ok {
		t.Fatal("an attribution must drain even when the outer result classified itself")
	}
}

// reset() is the turn boundary. An attribution whose outer step was never
// observed — a cancelled turn, a call refused before dispatch — must not
// survive to be folded into an unrelated call next turn.
func TestStallInnerAttributionsDoNotCrossTheTurnBoundary(t *testing.T) {
	d := on()
	innerFail(d, "c0", "read", "boom")
	d.t.reset()
	if _, ok := d.t.takeInner("c0"); ok {
		t.Fatal("a pending attribution must not survive reset()")
	}
}

// Detection off means nothing is kept: an inner report reaching a detector
// that is switched off leaves no attribution behind to be folded in later.
func TestStallInnerRecordsNothingWhileOff(t *testing.T) {
	d := &Detector{}
	innerFail(d, "c0", "read", "boom")
	if _, ok := d.t.takeInner("c0"); ok {
		t.Error("nothing should be recorded while stall detection is off")
	}
}

// innerCallingTool stands in for code_execution: each Execute makes host calls
// of its own and reports every outcome exactly as tools.dispatchHostTool does,
// then returns a productive result of its own that differs every time.
//
// It is a fake for one reason only — this package cannot import the concrete
// tools. What it does NOT fake is the path under test: the context it reports
// on is the one the engine built for the dispatch, so a regression that
// stopped naming the outer call would take this test down with it.
type innerCallingTool struct {
	inner    string
	failWith string
	per      int
	runs     int
}

func (f *innerCallingTool) Name() string            { return "code_execution" }
func (f *innerCallingTool) Description() string     { return "runs a script over host tools" }
func (f *innerCallingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

func (f *innerCallingTool) Execute(ctx context.Context, _ json.RawMessage, _ func(string)) (core.ToolResult, error) {
	f.runs++
	for range f.per {
		core.ReportInnerCall(ctx, f.inner, core.ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: f.failWith}},
			IsError: true,
		})
	}
	// A productive outer result, different on every run: the shape that made
	// the recorded loop invisible to both outer axes.
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{
		Text: fmt.Sprintf("script finished, printed variant %d", f.runs),
	}}}, nil
}

// End to end through the real dispatch. The engine names the executing call
// and hands the prompt's step gates to the tool, and core.ReportInnerCall is
// inert without both, so this fails if the plumbing is dropped anywhere
// between the loop, the tool and the detector's Inner.
func TestStallInnerAttributionFlowsThroughADispatch(t *testing.T) {
	tool := &innerCallingTool{inner: "read", failWith: "no such file or directory", per: 2}
	c := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		if n > stallThreshold {
			return saidText("done", 100), nil
		}
		return calledTool(fmt.Sprintf("c%d", n), "code_execution", fmt.Sprintf(`{"script":"attempt %d"}`, n)), nil
	}}
	a := newAgent(c, "m", core.Registry{"code_execution": tool}, on())
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	if tool.runs < stallThreshold {
		t.Fatalf("precondition: the tool should have run at least %d times, got %d", stallThreshold, tool.runs)
	}
	nudge := ""
	for _, req := range c.calls() {
		if strings.Contains(req.EphemeralContext, "[loop check]") {
			nudge = req.EphemeralContext
			break
		}
	}
	if nudge == "" {
		t.Fatal("a script looping on its own host calls must nudge when dispatched for real")
	}
	if !strings.Contains(nudge, "read") || !strings.Contains(nudge, "no such file or directory") {
		t.Errorf("the nudge should name the inner tool and its error:\n%s", nudge)
	}
}

// The inner path reuses unproductiveResult, so a harness guard that is not an
// error still counts — the read-dedup stub being the canonical one. This is
// the same rule the outer axis applies, which is the point of sharing it.
func TestStallInnerRecognisesAGuardResultNotOnlyAnError(t *testing.T) {
	d := on()
	for i := 0; i < stallThreshold; i++ {
		id := fmt.Sprintf("c%d", i)
		d.recordInner(id, "read", core.ToolResult{
			Content: []provider.Content{provider.TextBlock{
				Text: "page.html — unchanged since you read it earlier this session",
			}},
		})
		d.t.observe(
			call(id, "code_execution", fmt.Sprintf(`{"script":"attempt %d"}`, i)),
			result(id, fmt.Sprintf("printed %d", i), false),
		)
	}
	if d.t.nudge() == "" {
		t.Fatal("a non-error harness guard repeated inside a script must trip the churn axis")
	}
}
