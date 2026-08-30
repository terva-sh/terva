package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// These pin the F3 finding in
// docs/reviews/2026-08-29-local-model-harness-friction-review.md: a loop that
// happens entirely inside a code_execution script, where the model's own call
// looks different and productive every time.

// stallAgent is an agent with the detector on, which is what ReportInnerCall
// requires before it attributes anything.
func stallAgent() *Agent {
	a := &Agent{}
	a.SetStallDetection(true)
	return a
}

// innerFail reports one unproductive inner call against an outer call id,
// through the real context plumbing runOneTool sets up.
func innerFail(a *Agent, outerID, tool, text string) {
	ctx := contextWithOuterCall(ContextWithAgent(context.Background(), a), outerID)
	ReportInnerCall(ctx, tool, ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: text}},
		IsError: true,
	})
}

// scriptTurn is one code_execution call in the recorded shape: a DIFFERENT
// script each time (so the arguments never repeat), a DIFFERENT printed result
// each time (so the result fingerprint never repeats), and the same inner
// failure underneath. Neither outer axis can see this; only the inner one can.
func scriptTurn(a *Agent, i int, innerTool, innerErr string) {
	id := fmt.Sprintf("c%d", i)
	innerFail(a, id, innerTool, innerErr)
	a.stall.observe(
		call(id, "code_execution", fmt.Sprintf(`{"script":"attempt %d"}`, i)),
		result(id, fmt.Sprintf("EVAL_ERR variant %d", i), false),
	)
}

// The headline case. Three scripts that differ in every way the detector could
// previously observe, failing identically where it could not.
func TestStallInnerChurnTripsThroughAProductiveOuterResult(t *testing.T) {
	a := stallAgent()
	for i := 0; i < stallThreshold; i++ {
		scriptTurn(a, i, "read", "no such file or directory")
	}
	nudge := a.stall.nudge()
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
	a := stallAgent()
	for i := 0; i < stallWindow; i++ {
		id := fmt.Sprintf("c%d", i)
		// Identical to scriptTurn, minus the inner report.
		a.stall.observe(
			call(id, "code_execution", fmt.Sprintf(`{"script":"attempt %d"}`, i)),
			result(id, fmt.Sprintf("EVAL_ERR variant %d", i), false),
		)
	}
	if n := a.stall.nudge(); n != "" {
		t.Fatalf("the fixture must be invisible to the outer axes, or the inner test is not testing the inner path:\n%s", n)
	}
}

// One outer call contributes ONE churn step, however many host calls the
// script made. A script that probes ten paths and finds ten missing is doing
// its job; counting ten would trip the threshold inside a single call.
func TestStallInnerOneChurnStepPerOuterCall(t *testing.T) {
	a := stallAgent()

	innerFail(a, "c0", "read", "no such file or directory")
	for i := 0; i < 9; i++ { // nine more of the same, all inside ONE script
		innerFail(a, "c0", "read", "no such file or directory")
	}
	a.stall.observe(
		call("c0", "code_execution", `{"script":"probe ten paths"}`),
		result("c0", "10 missing", false),
	)
	if n := a.stall.nudge(); n != "" {
		t.Fatalf("ten failures inside ONE call is a probing script, not a loop:\n%s", n)
	}

	// Two more outer calls, one inner failure each, and the threshold is met
	// legitimately — three calls the model chose to make.
	scriptTurn(a, 1, "read", "no such file or directory")
	if n := a.stall.nudge(); n != "" {
		t.Fatalf("two outer calls is still below the threshold:\n%s", n)
	}
	scriptTurn(a, 2, "read", "no such file or directory")
	if a.stall.nudge() == "" {
		t.Fatal("three outer calls failing the same way inside must trip")
	}
}

// An outer result that classifies on its own keeps precedence: the inner
// signal is a fallback for the blind spot, not a replacement for the axis that
// already worked.
func TestStallInnerYieldsToAnUnproductiveOuterResult(t *testing.T) {
	a := stallAgent()
	for i := 0; i < stallThreshold; i++ {
		id := fmt.Sprintf("c%d", i)
		innerFail(a, id, "read", "INNER-ERROR-TEXT")
		a.stall.observe(
			call(id, "code_execution", fmt.Sprintf(`{"script":"attempt %d"}`, i)),
			result(id, "OUTER-ERROR-TEXT", true),
		)
	}
	nudge := a.stall.nudge()
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
	a := stallAgent()
	for i := 0; i < stallWindow; i++ {
		id := fmt.Sprintf("c%d", i)
		ctx := contextWithOuterCall(ContextWithAgent(context.Background(), a), id)
		for range 5 {
			ReportInnerCall(ctx, "read", ToolResult{
				Content: []provider.Content{provider.TextBlock{Text: "file contents"}},
			})
		}
		a.stall.observe(
			call(id, "code_execution", fmt.Sprintf(`{"script":"attempt %d"}`, i)),
			result(id, fmt.Sprintf("result %d", i), false),
		)
	}
	if n := a.stall.nudge(); n != "" {
		t.Fatalf("a script whose host calls succeed is working, not stalling:\n%s", n)
	}
}

// When a script fails several ways, the class it failed on MOST carries the
// step, so the nudge reports the dominant problem rather than whichever error
// happened to land last.
func TestStallInnerDominantClassCarriesTheStep(t *testing.T) {
	a := stallAgent()
	for i := 0; i < stallThreshold; i++ {
		id := fmt.Sprintf("c%d", i)
		innerFail(a, id, "grep", "RARE-FAILURE")
		innerFail(a, id, "read", "COMMON-FAILURE")
		innerFail(a, id, "read", "COMMON-FAILURE")
		a.stall.observe(
			call(id, "code_execution", fmt.Sprintf(`{"script":"attempt %d"}`, i)),
			result(id, fmt.Sprintf("printed %d", i), false),
		)
	}
	nudge := a.stall.nudge()
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
	a := stallAgent()
	innerFail(a, "c0", "read", "boom")
	a.stall.observe(
		call("c0", "code_execution", `{"script":"one"}`),
		result("c0", "printed", false),
	)
	if _, ok := a.stall.takeInner("c0"); ok {
		t.Fatal("the attribution should have been drained by the step that used it")
	}
	// And an outer result that classified on its own still drains, so nothing
	// is left behind for a later call to inherit.
	innerFail(a, "c1", "read", "boom")
	a.stall.observe(
		call("c1", "code_execution", `{"script":"two"}`),
		result("c1", "outer failure", true),
	)
	if _, ok := a.stall.takeInner("c1"); ok {
		t.Fatal("an attribution must drain even when the outer result classified itself")
	}
}

// reset() is the turn boundary. An attribution whose outer step was never
// observed — a cancelled turn, a call refused before dispatch — must not
// survive to be folded into an unrelated call next turn.
func TestStallInnerAttributionsDoNotCrossTheTurnBoundary(t *testing.T) {
	a := stallAgent()
	innerFail(a, "c0", "read", "boom")
	a.stall.reset()
	if _, ok := a.stall.takeInner("c0"); ok {
		t.Fatal("a pending attribution must not survive reset()")
	}
}

// ReportInnerCall is inert outside a model-issued dispatch. A direct call, a
// test harness, an extension's host_tool_call: none of them have an outer step
// to attribute to, and none of them should panic or record.
func TestReportInnerCallIsInertWithoutADispatch(t *testing.T) {
	res := ToolResult{Content: []provider.Content{provider.TextBlock{Text: "boom"}}, IsError: true}

	// No agent in context at all.
	ReportInnerCall(context.Background(), "read", res)

	// An agent, but no outer call id (the ext host_tool_call door).
	a := stallAgent()
	ReportInnerCall(ContextWithAgent(context.Background(), a), "read", res)
	if _, ok := a.stall.takeInner(""); ok {
		t.Error("a dispatch with no outer call must attribute nothing")
	}

	// An outer call id, but detection off: the feature is opt-in.
	off := &Agent{}
	ctx := contextWithOuterCall(ContextWithAgent(context.Background(), off), "c0")
	ReportInnerCall(ctx, "read", res)
	if _, ok := off.stall.takeInner("c0"); ok {
		t.Error("nothing should be recorded while stall detection is off")
	}
}

// innerCallingTool stands in for code_execution: each Execute makes host calls
// of its own and reports every outcome exactly as tools.dispatchHostTool does,
// then returns a productive result of its own that differs every time.
//
// It is a fake for one reason only — packages/core cannot import the concrete
// tools it dispatches. What it does NOT fake is the path under test: the
// context it reports on is the one runOneTool built, so a regression that
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

func (f *innerCallingTool) Execute(ctx context.Context, _ json.RawMessage, _ func(string)) (ToolResult, error) {
	f.runs++
	for range f.per {
		ReportInnerCall(ctx, f.inner, ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: f.failWith}},
			IsError: true,
		})
	}
	// A productive outer result, different on every run: the shape that made
	// the recorded loop invisible to both outer axes.
	return ToolResult{Content: []provider.Content{provider.TextBlock{
		Text: fmt.Sprintf("script finished, printed variant %d", f.runs),
	}}}, nil
}

// End to end through the real dispatch. runOneTool is what names the executing
// call, and ReportInnerCall is inert without that name — so this fails if the
// context plumbing is dropped anywhere between the two.
func TestStallInnerAttributionFlowsThroughRunOneTool(t *testing.T) {
	a := stallAgent()
	tool := &innerCallingTool{inner: "read", failWith: "no such file or directory", per: 2}
	reg := Registry{"code_execution": tool}
	sink := func(AgentEvent) {}

	for i := 0; i < stallThreshold; i++ {
		tc := provider.ToolCallBlock{
			ID:        fmt.Sprintf("c%d", i),
			Name:      "code_execution",
			Arguments: json.RawMessage(fmt.Sprintf(`{"script":"attempt %d"}`, i)),
		}
		res := a.runOneTool(context.Background(), tc, reg, sink)
		a.stall.observe(
			provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{tc}},
			provider.Message{Role: provider.RoleTool, Content: []provider.Content{
				provider.ToolResultBlock{CallID: tc.ID, Content: res.Content, IsError: res.IsError},
			}},
		)
	}
	if tool.runs != stallThreshold {
		t.Fatalf("precondition: the tool should have run %d times, got %d", stallThreshold, tool.runs)
	}
	nudge := a.stall.nudge()
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
	a := stallAgent()
	for i := 0; i < stallThreshold; i++ {
		id := fmt.Sprintf("c%d", i)
		ctx := contextWithOuterCall(ContextWithAgent(context.Background(), a), id)
		ReportInnerCall(ctx, "read", ToolResult{
			Content: []provider.Content{provider.TextBlock{
				Text: "page.html — unchanged since you read it earlier this session",
			}},
		})
		a.stall.observe(
			call(id, "code_execution", fmt.Sprintf(`{"script":"attempt %d"}`, i)),
			result(id, fmt.Sprintf("printed %d", i), false),
		)
	}
	if a.stall.nudge() == "" {
		t.Fatal("a non-error harness guard repeated inside a script must trip the churn axis")
	}
}
