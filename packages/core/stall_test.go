package core

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The fixture these tests pin is built from a real dogfood session
// (2195338e6198d936/20260714-063519-da133cf7.jsonl) where a small local model
// stalled three different ways. Two must trip the detector; one must NOT — a
// model trying genuinely different searches is working, not spinning, and a
// harness that nags it is worse than one that says nothing.

// Signature 1 (spin axis): the same file read four times, each answered by the
// read-dedup guard. Identical arguments, so spin catches it; the guard result is
// not an error, which is why the churn axis alone would miss it.
func TestStallTripsOnRepeatedIdenticalReads(t *testing.T) {
	var tr stallTracker
	for i := 0; i < 4; i++ {
		id := "r"
		tr.observe(
			call(id, "read", `{"path":"models.json"}`),
			result(id, "models.json — unchanged since you read it earlier", false),
		)
	}
	if tr.nudge() == "" {
		t.Fatal("four identical reads must trip the spin axis")
	}
	if !strings.Contains(tr.nudge(), "read") {
		t.Errorf("the nudge should name the repeated tool:\n%s", tr.nudge())
	}
}

// Signature 3 (the death loop), faithful: identical STRUCTURAL arguments
// (id/status/activate_next all task-4) with a different `evidence` string each
// time, and an identical error. This is the case that breaks the obvious design:
// hashing the whole arguments sees N distinct calls (evidence varied) and never
// trips. The detector survives it two ways at once — canonical args drop the
// evidence field (so spin still sees a repeat) and the error is identical (so
// churn trips regardless).
func TestStallTripsOnTheDeathLoopDespiteVaryingEvidence(t *testing.T) {
	const theError = "activate_next must name a different task than the one you're updating."
	var raw []string
	var tr stallTracker
	for i := 0; i < 4; i++ {
		id := "c"
		args := `{"id":"task-4","status":"done","activate_next":"task-4","evidence":"attempt ` +
			string(rune('A'+i)) + `: I will append the model entry to models.json"}`
		raw = append(raw, args)
		tr.observe(call(id, "task_update", args), result(id, theError, true))
	}
	// Guard the premise: the raw arguments really did differ each call, so a
	// whole-arguments hash would have produced distinct keys and missed this.
	if raw[0] == raw[3] {
		t.Fatal("test setup: the death loop's arguments must vary, or it proves nothing")
	}
	if tr.nudge() == "" {
		t.Fatal("the death loop must trip even though its arguments varied per call")
	}
	if !strings.Contains(tr.nudge(), "activate_next must name a different task") {
		t.Errorf("the nudge should quote the error the model keeps hitting:\n%s", tr.nudge())
	}
}

// Churn in isolation: vary the STRUCTURAL arguments too (task-4, task-5, task-6),
// keep the error identical. Now canonical args differ, so spin cannot fire — only
// the error-keyed axis can, and must. This is the "trying different task IDs,
// same rejection" loop that an args-only detector cannot see at all.
func TestStallErrorChurnCatchesWhatSpinCannot(t *testing.T) {
	const theError = "no such task in the current board"
	var tr stallTracker
	for i := 4; i <= 6; i++ {
		id := "c"
		args := `{"id":"task-` + string(rune('0'+i)) + `","status":"done"}`
		tr.observe(call(id, "task_update", args), result(id, theError, true))
	}
	if tr.nudge() == "" {
		t.Fatal("three distinct calls that fail the same way must trip the churn axis")
	}
}

// Signature 2 (the negative): the same tool failing, but on genuinely different
// inputs — three greps for different patterns, each a distinct no-match. The
// error text carries the command, so the normalized errors differ and churn does
// not fire; the arguments differ, so spin does not either. The model is
// exploring, and the detector must leave it alone.
func TestStallDoesNotTripOnLegitimateExploration(t *testing.T) {
	var tr stallTracker
	patterns := []string{"gemma", "openai-compatible", "oai.local"}
	for i, p := range patterns {
		id := "g"
		tr.observe(
			call(id, "bash", `{"command":"grep -rn `+p+` models.json"}`),
			result(id, "$ grep -rn "+p+" models.json [exit 1] Took 0.1s", true),
		)
		if tr.nudge() != "" {
			t.Fatalf("exploration step %d (%q) tripped the detector; different searches are progress, not a stall:\n%s", i, p, tr.nudge())
		}
	}
}

// canonicalArgs must collapse two calls that differ only in a reasoning field,
// and keep two calls that differ in a real argument apart.
func TestStallCanonicalArgsDropsThoughtFields(t *testing.T) {
	a := canonicalArgs(json.RawMessage(`{"path":"a.go","evidence":"because X"}`))
	b := canonicalArgs(json.RawMessage(`{"path":"a.go","evidence":"because Y, entirely different prose"}`))
	if a != b {
		t.Errorf("thought-only differences must collapse:\n%q\n%q", a, b)
	}
	c := canonicalArgs(json.RawMessage(`{"path":"b.go"}`))
	if a == c {
		t.Error("a real argument difference (path) must not collapse")
	}
	// Key order must not matter.
	if canonicalArgs(json.RawMessage(`{"x":1,"y":2}`)) != canonicalArgs(json.RawMessage(`{"y":2,"x":1}`)) {
		t.Error("argument key order must not affect the canonical form")
	}
}

// normalizeError must strip volatile tails so an identical failure collapses,
// while two different failing commands stay distinct.
func TestStallNormalizeErrorCollapsesVolatileTails(t *testing.T) {
	if normalizeError("Command failed [exit 1] Took 0.1s") != normalizeError("Command failed [exit 1] Took 2.4s") {
		t.Error("the same error with different durations/exit noise must collapse")
	}
	if normalizeError("$ grep foo [exit 1]") == normalizeError("$ grep bar [exit 1]") {
		t.Error("different commands must stay distinct so churn does not fire on exploration")
	}
}

// A signature nudges once per turn: a determined loop is the next rung's job, not
// a repeated note on every step.
func TestStallNudgesOncePerSignature(t *testing.T) {
	var tr stallTracker
	loop := func() {
		tr.observe(call("c", "task_update", `{"id":"task-4","status":"done"}`),
			result("c", "activate_next must name a different task", true))
	}
	loop()
	loop()
	loop() // trips here
	if tr.nudge() == "" {
		t.Fatal("expected a nudge after the third identical failure")
	}
	tr.clearNudge() // oneTurn delivered it
	loop()
	loop() // still looping, same signature
	if tr.nudge() != "" {
		t.Errorf("a signature already nudged this turn must not nudge again:\n%s", tr.nudge())
	}
}

// observe reports the nudge it newly fires — the return runLoop persists as a
// stall record — once per signature per turn, tagged with the axis that caught
// the loop. The spin axis (identical calls) carries no error detail; the churn
// axis (identical failures) carries the error slice.
func TestStallObserveReportsTheNudgeItFires(t *testing.T) {
	t.Run("spin axis, no detail", func(t *testing.T) {
		var tr stallTracker
		var got []stallEvent
		for i := 0; i < 4; i++ {
			// Identical args with a productive (non-error, non-guard) result: no
			// churn class, so only the spin axis can catch the repeat.
			got = append(got, tr.observe(
				call("r", "read", `{"path":"models.json"}`),
				result("r", "the file's normal contents", false))...)
		}
		if len(got) != 1 {
			t.Fatalf("exactly one nudge should be reported across the loop, got %d", len(got))
		}
		if got[0].axis != stallAxisSpin || got[0].tool != "read" {
			t.Errorf("want a spin nudge on read, got %+v", got[0])
		}
		if got[0].detail != "" {
			t.Errorf("the spin axis carries no error detail, got %q", got[0].detail)
		}
	})
	t.Run("churn axis, carries the error", func(t *testing.T) {
		const theError = "activate_next must name a different task"
		var tr stallTracker
		var got []stallEvent
		for i := 0; i < 4; i++ {
			// Vary the structural args so only the error-keyed axis can fire.
			args := `{"id":"task-` + string(rune('4'+i)) + `","status":"done"}`
			got = append(got, tr.observe(call("c", "task_update", args), result("c", theError, true))...)
		}
		if len(got) != 1 {
			t.Fatalf("churn should report exactly one nudge, got %d", len(got))
		}
		if got[0].axis != stallAxisChurn || got[0].tool != "task_update" {
			t.Errorf("want a churn nudge on task_update, got %+v", got[0])
		}
		if !strings.Contains(got[0].detail, "activate_next") {
			t.Errorf("the churn axis should carry the repeated error, got %q", got[0].detail)
		}
	})
}

// reset clears everything so detection never spans turns.
func TestStallResetsPerTurn(t *testing.T) {
	var tr stallTracker
	for i := 0; i < 3; i++ {
		tr.observe(call("c", "read", `{"path":"a"}`), result("c", "unchanged since you read it earlier", false))
	}
	if tr.nudge() == "" {
		t.Fatal("precondition: should have tripped")
	}
	tr.reset()
	if tr.nudge() != "" || len(tr.steps) != 0 || tr.nudged != nil {
		t.Error("reset must clear steps, the nudged set, and any pending nudge")
	}
	// After reset, a single repeat of the previously-tripped call must not
	// immediately re-trip — the count started over.
	tr.observe(call("c", "read", `{"path":"a"}`), result("c", "unchanged since you read it earlier", false))
	if tr.nudge() != "" {
		t.Error("one call after reset must not trip; the window was cleared")
	}
}

// loopingTool always fails the same way — a stand-in for the tool a model gets
// wedged on.
type loopingTool struct{}

func (loopingTool) Name() string            { return "spin" }
func (loopingTool) Description() string     { return "always fails identically" }
func (loopingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (loopingTool) Execute(context.Context, json.RawMessage, func(string)) (ToolResult, error) {
	return ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: "boom: the same failure again"}},
		IsError: true,
	}, nil
}

// End to end: a looping turn drives the detector through runLoop, and the nudge
// reaches exactly one dispatched request on the ephemeral tail — never the
// transcript, and never more than once.
func TestStallNudgeRidesTheEphemeralTailEndToEnd(t *testing.T) {
	loop := func(n int) []provider.Event {
		return []provider.Event{
			provider.EventStart{Provider: "scripted"},
			provider.EventUsage{Usage: provider.Usage{InputTokens: 100, OutputTokens: 10}},
			provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role: provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{
					// Distinct id per dispatch keeps the transcript well-formed;
					// the arguments (what spin keys on) stay identical.
					ID: "call-" + string(rune('a'+n)), Name: "spin", Arguments: json.RawMessage(`{"x":1}`),
				}},
			}},
		}
	}
	// Five spinning dispatches — enough to trip (three observes) and then carry
	// the nudge — then the turn ends on its own so Prompt returns cleanly.
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		if n >= 5 {
			return saidText("done", 100), nil
		}
		return loop(n), nil
	}}

	newAgent := func(detect bool) (*Agent, *scriptedClient) {
		path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
		sess, err := NewSessionAtPath(path, "/ws", "p", "m", "0.0.0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sess.Close() })
		a := NewAgent(client, "m", "you are terva", Registry{"spin": loopingTool{}})
		a.AdoptSessionIdentity(sess)
		a.MaxSteps = 20
		a.SetStallDetection(detect)
		return a, client
	}

	a, c := newAgent(true)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	nudged, firstNudge := 0, -1
	for i, req := range c.calls() {
		if strings.Contains(req.EphemeralContext, "[loop check]") {
			nudged++
			if firstNudge < 0 {
				firstNudge = i
			}
		}
	}
	if nudged != 1 {
		t.Errorf("the nudge must ride exactly one dispatch, got %d", nudged)
	}
	if firstNudge < stallThreshold {
		t.Errorf("the nudge fired at dispatch %d, before the detector could have tripped (threshold %d)", firstNudge, stallThreshold)
	}
	// It must never land in the durable transcript.
	for _, m := range a.Messages() {
		for _, ct := range m.Content {
			if tb, ok := ct.(provider.TextBlock); ok && strings.Contains(tb.Text, "[loop check]") {
				t.Error("the nudge leaked into the transcript; it must ride the ephemeral tail only")
			}
		}
	}

	// With detection off, no dispatch carries a nudge.
	client.mu.Lock()
	client.reqs = nil
	client.mu.Unlock()
	off, c2 := newAgent(false)
	if err := off.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt (detection off): %v", err)
	}
	for _, req := range c2.calls() {
		if strings.Contains(req.EphemeralContext, "[loop check]") {
			t.Error("detection is off; no nudge should be emitted")
		}
	}
}
