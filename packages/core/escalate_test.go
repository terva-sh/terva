package core

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// --- tracker-level: the escalate watermark sits above the nudge ---

// The nudge trips at stallThreshold; escalation waits stallEscalateAfterNudge
// recurrences longer, so the model gets its one chance to heed the nudge before
// the harness reaches for a bigger lever.
func TestStallEscalationRaisesAboveTheNudgeWatermark(t *testing.T) {
	var tr stallTracker
	fail := func() {
		tr.observe(call("c", "task_update", `{"id":"task-4","status":"done"}`),
			result("c", "activate_next must name a different task", true))
	}
	for i := 0; i < stallThreshold+stallEscalateAfterNudge-1; i++ {
		fail()
		if _, ok := tr.escalation(); ok {
			t.Fatalf("escalation raised too early, at recurrence %d (watermark is %d)", i+1, stallThreshold+stallEscalateAfterNudge)
		}
	}
	if tr.nudge() == "" {
		t.Error("the nudge should already have fired by now")
	}
	fail() // crosses the escalate watermark
	sig, ok := tr.escalation()
	if !ok {
		t.Fatal("escalation must raise once the loop persists past the nudge")
	}
	if !strings.Contains(sig.reason, "task_update") || !strings.Contains(sig.reason, "activate_next") {
		t.Errorf("the escalation reason should name the tool and the error: %q", sig.reason)
	}
}

// The thrash trigger: a model that fails THREE different ways — each nudged, none
// repeated to the same-signature watermark — is escalated too. This is the
// dogfood case the monotonous watermark missed: four distinct nudges, zero
// escalation, because no single signature reached five.
func TestStallThrashEscalatesAcrossDistinctLoops(t *testing.T) {
	var tr stallTracker
	loop := func(tool, args, errText string) {
		for i := 0; i < stallThreshold; i++ { // exactly a nudge, never the ×5 watermark
			tr.observe(call("x", tool, args), result("x", errText, true))
		}
	}

	loop("read", `{"path":"a"}`, "boom reading a") // nudge 1
	if _, ok := tr.escalation(); ok {
		t.Fatal("a single nudged loop must not escalate — the nudge gets its chance")
	}
	loop("grep", `{"q":"b"}`, "no match for b") // nudge 2
	if _, ok := tr.escalation(); ok {
		t.Fatalf("two distinct nudges is still under the thrash threshold (%d)", stallThrashThreshold)
	}
	loop("bash", `{"cmd":"c"}`, "command c failed") // nudge 3 → thrash
	sig, ok := tr.escalation()
	if !ok {
		t.Fatalf("%d distinct nudged loops must escalate on thrash even though none repeated to %d",
			stallThrashThreshold, stallThreshold+stallEscalateAfterNudge)
	}
	if !strings.Contains(sig.reason, "different failing loops") {
		t.Errorf("the thrash reason should say it's stuck across different loops: %q", sig.reason)
	}
	if sig.signature != "thrash" {
		t.Errorf("a thrash escalation should carry the thrash signature, got %q", sig.signature)
	}
}

// Declining ("keep trying") gives the model a fresh window AND backs off: the
// next offer needs more evidence, so a hopeless case isn't re-asked every few
// calls, but a model that stays stuck can be offered again.
func TestStallForgiveGivesBreathingRoomAndBacksOff(t *testing.T) {
	var tr stallTracker
	nudge := func(tool, args, errText string) {
		for i := 0; i < stallThreshold; i++ { // exactly a nudge, never the monotonous watermark
			tr.observe(call("x", tool, args), result("x", errText, true))
		}
	}
	thrash := func() {
		nudge("read", `{"p":"a"}`, "boom a")
		nudge("grep", `{"q":"b"}`, "boom b")
		nudge("bash", `{"c":"c"}`, "boom c")
	}

	thrash() // 3 distinct nudges → escalation, as the harness would see it
	if _, ok := tr.escalation(); !ok {
		t.Fatal("precondition: 3 distinct nudges escalate")
	}

	// The harness offered and the user declined: markEscalated then forgive.
	tr.markEscalated()
	tr.forgive()
	if len(tr.steps) != 0 || tr.nudged != nil || tr.escalated {
		t.Error("forgive must wipe the loop state and re-arm escalation (a fresh window)")
	}
	if tr.declines != 1 {
		t.Errorf("forgive should count the decline, got %d", tr.declines)
	}

	// Backoff: the same three distinct loops are no longer enough — the thrash bar
	// rose to stallThrashThreshold+1.
	thrash()
	if _, ok := tr.escalation(); ok {
		t.Fatalf("after one decline, %d distinct nudges must be under the raised bar (%d)",
			stallThrashThreshold, stallThrashThreshold+1)
	}
	nudge("sed", `{"e":"d"}`, "boom d") // a 4th distinct loop clears the raised bar
	if _, ok := tr.escalation(); !ok {
		t.Fatal("a still-stuck model must be offered again once it clears the backed-off bar")
	}

	// A new turn wipes the decline count entirely.
	tr.reset()
	if tr.declines != 0 {
		t.Errorf("reset must clear declines for the next turn, got %d", tr.declines)
	}
}

// One offer per turn: after the harness acts (markEscalated), a still-running
// loop must not raise another, and reset must clear it.
func TestStallEscalationIsRaisedAtMostOncePerTurn(t *testing.T) {
	var tr stallTracker
	fail := func() {
		tr.observe(call("c", "read", `{"path":"a"}`), result("c", "boom", true))
	}
	for i := 0; i < stallThreshold+stallEscalateAfterNudge; i++ {
		fail()
	}
	if _, ok := tr.escalation(); !ok {
		t.Fatal("precondition: should have raised")
	}
	tr.markEscalated()
	if _, ok := tr.escalation(); ok {
		t.Error("markEscalated must consume the request")
	}
	fail()
	fail()
	if _, ok := tr.escalation(); ok {
		t.Error("a turn offers escalation at most once, even if the loop continues")
	}
	tr.reset()
	if _, ok := tr.escalation(); ok || tr.escalated {
		t.Error("reset must clear the escalation state for the next turn")
	}
}

// --- end to end: the harness drives consent and the swap ---

type fakeEscalator struct {
	target    EscalationTarget
	hasTarget bool
	switched  bool
	err       error
	calls     []EscalationRequest
}

func (f *fakeEscalator) Target() (EscalationTarget, bool) { return f.target, f.hasTarget }

func (f *fakeEscalator) Escalate(_ context.Context, r EscalationRequest) (EscalationOutcome, error) {
	f.calls = append(f.calls, r)
	if f.err != nil {
		return EscalationOutcome{}, f.err
	}
	if !f.switched {
		return EscalationOutcome{Declined: true}, nil
	}
	return EscalationOutcome{Switched: true, ToProvider: f.target.Provider, ToModel: f.target.Model}, nil
}

func sonnetTarget(switched bool, err error) *fakeEscalator {
	return &fakeEscalator{
		target:    EscalationTarget{Provider: "anthropic", Model: "claude-sonnet-5"},
		hasTarget: true, switched: switched, err: err,
	}
}

// spinning is one dispatch that calls the always-failing tool with identical args.
func spinning(n int) []provider.Event {
	return []provider.Event{
		provider.EventStart{Provider: "scripted"},
		provider.EventUsage{Usage: provider.Usage{InputTokens: 100, OutputTokens: 10}},
		provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
			Role: provider.RoleAssistant,
			Content: []provider.Content{provider.ToolCallBlock{
				ID: fmt.Sprintf("call-%d", n), Name: "spin", Arguments: json.RawMessage(`{"x":1}`),
			}},
		}},
	}
}

// escalatingAgent loops for five dispatches (enough to cross the escalate
// watermark) then "succeeds" — standing in for the swapped-in model finishing the
// step. The fake Escalator does not really swap; this exercises the harness that
// decides to, asks about it, and stages the handoff.
func escalatingAgent(t *testing.T, esc Escalator, asker Asker, auto bool) (*Agent, *scriptedClient) {
	t.Helper()
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		if n >= 5 {
			return saidText("done", 100), nil
		}
		return spinning(n), nil
	}}
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := NewSessionAtPath(path, "/ws", "p", "m", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	a := NewAgent(client, "m", "you are terva", Registry{"spin": loopingTool{}})
	a.AdoptSessionIdentity(sess)
	a.MaxSteps = 20
	a.SetStallDetection(true)
	a.SetStuckLoopEscalation(true)
	a.Escalator = esc
	a.Asker = asker
	a.SetEscalateAuto(auto)
	return a, client
}

func countEphemeral(c *scriptedClient, marker string) int {
	n := 0
	for _, req := range c.calls() {
		if strings.Contains(req.EphemeralContext, marker) {
			n++
		}
	}
	return n
}

// The default path: ask first, and on consent swap and stage a handoff.
func TestEscalationAsksThenSwapsOnConsent(t *testing.T) {
	esc := sonnetTarget(true, nil)
	asker := &recordingAsker{answer: func(q UserQuestion) UserAnswer {
		return UserAnswer{Answer: q.Options[0]} // "Escalate"
	}}
	a, c := escalatingAgent(t, esc, asker, false)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if len(esc.calls) != 1 {
		t.Fatalf("Escalate must be called exactly once, got %d", len(esc.calls))
	}
	if !strings.Contains(esc.calls[0].Reason, "spin") {
		t.Errorf("the request should carry the stall reason: %q", esc.calls[0].Reason)
	}
	qs := asker.questions()
	if len(qs) != 1 {
		t.Fatalf("expected exactly one consent question, got %d", len(qs))
	}
	if !strings.Contains(qs[0].Question, "claude-sonnet-5") || !strings.Contains(qs[0].Question, "anthropic") {
		t.Errorf("the ask must name the destination model and provider (egress is real):\n%s", qs[0].Question)
	}
	if n := countEphemeral(c, "[handoff]"); n != 1 {
		t.Errorf("the handoff marker must ride exactly one dispatch, got %d", n)
	}
	if n := countEphemeral(c, "[loop check]"); n != 1 {
		t.Errorf("the earlier nudge should also have ridden one dispatch, got %d", n)
	}
	// The handoff must never persist into the durable transcript.
	for _, m := range a.Messages() {
		for _, ct := range m.Content {
			if tb, ok := ct.(provider.TextBlock); ok && strings.Contains(tb.Text, "[handoff]") {
				t.Error("the handoff marker leaked into the transcript")
			}
		}
	}
}

// auto skips the ask entirely.
func TestEscalationAutoSwapsWithoutAsking(t *testing.T) {
	esc := sonnetTarget(true, nil)
	a, c := escalatingAgent(t, esc, nil, true) // no Asker at all
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(esc.calls) != 1 {
		t.Fatalf("auto escalation must swap without asking, Escalate calls = %d", len(esc.calls))
	}
	if n := countEphemeral(c, "[handoff]"); n != 1 {
		t.Errorf("handoff should ride one dispatch, got %d", n)
	}
}

// "Keep trying" leaves the current model in place; the turn runs to its natural end.
func TestEscalationDeclinedKeepsTheCurrentModel(t *testing.T) {
	esc := sonnetTarget(true, nil)
	asker := &recordingAsker{answer: func(q UserQuestion) UserAnswer {
		return UserAnswer{Answer: q.Options[1]} // "Keep trying"
	}}
	a, c := escalatingAgent(t, esc, asker, false)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(esc.calls) != 0 {
		t.Errorf("declining must not call Escalate, got %d calls", len(esc.calls))
	}
	if n := countEphemeral(c, "[handoff]"); n != 0 {
		t.Errorf("no handoff without a swap, got %d", n)
	}
	// The decline forgave the loop state, so the model kept trying with room.
	if a.stall.declines != 1 {
		t.Errorf("declining should forgive once (breathing room), got declines=%d", a.stall.declines)
	}
}

// "Stop" ends the turn cleanly, before any swap.
func TestEscalationStopEndsTheTurn(t *testing.T) {
	esc := sonnetTarget(true, nil)
	asker := &recordingAsker{answer: func(q UserQuestion) UserAnswer {
		return UserAnswer{Answer: q.Options[2]} // "Stop"
	}}
	a, c := escalatingAgent(t, esc, asker, false)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(esc.calls) != 0 {
		t.Errorf("Stop must not call Escalate, got %d", len(esc.calls))
	}
	// The turn ended at the offer (5 spinning dispatches), before the 6th that a
	// continued loop would have made.
	if got := len(c.calls()); got != 5 {
		t.Errorf("Stop should end the turn at the offer; expected 5 dispatches, got %d", got)
	}
}

// A nil Escalator (the production state until the host binds one) is inert: the
// loop is detected and nudged, nothing escalates, nothing panics.
func TestEscalationInertWithoutAnEscalator(t *testing.T) {
	a, c := escalatingAgent(t, nil, nil, false)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if n := countEphemeral(c, "[loop check]"); n != 1 {
		t.Errorf("detection should still nudge with no Escalator, got %d", n)
	}
	if n := countEphemeral(c, "[handoff]"); n != 0 {
		t.Errorf("nothing should escalate with no Escalator, got %d handoffs", n)
	}
}

// A failed swap is non-fatal: the turn keeps running on the current model.
func TestEscalationFailureIsNonFatal(t *testing.T) {
	esc := sonnetTarget(false, fmt.Errorf("no credential for anthropic"))
	a, c := escalatingAgent(t, esc, nil, true)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("a failed escalation must not fail the turn, got %v", err)
	}
	if len(esc.calls) != 1 {
		t.Errorf("Escalate should have been attempted once, got %d", len(esc.calls))
	}
	if n := countEphemeral(c, "[handoff]"); n != 0 {
		t.Errorf("a failed swap stages no handoff, got %d", n)
	}
}

// --- the escalation record: one per resolved decision, disposition and all ---

// records registers an escalation observer and returns a getter for what it saw,
// so a test can assert the record the host would persist.
func records(a *Agent) func() []EscalationRecord {
	var got []EscalationRecord
	a.AddEscalationObserver(func(rec EscalationRecord) { got = append(got, rec) })
	return func() []EscalationRecord { return got }
}

// The auto swap emits exactly one record, disposition "switched", carrying the
// from/to models and the stall reason a reader needs to tell it apart from a
// user /model switch.
func TestEscalationRecordsASwitch(t *testing.T) {
	esc := sonnetTarget(true, nil)
	a, _ := escalatingAgent(t, esc, nil, true)
	got := records(a)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	recs := got()
	if len(recs) != 1 {
		t.Fatalf("expected exactly one escalation record, got %d", len(recs))
	}
	r := recs[0]
	if r.Disposition != EscalationSwitched {
		t.Errorf("disposition = %q, want switched", r.Disposition)
	}
	if !r.Auto {
		t.Error("an auto swap must record Auto=true")
	}
	if r.FromModel != "m" {
		t.Errorf("FromModel = %q, want the pre-swap model %q", r.FromModel, "m")
	}
	if r.ToProvider != "anthropic" || r.ToModel != "claude-sonnet-5" {
		t.Errorf("target not recorded: %q/%q", r.ToProvider, r.ToModel)
	}
	if !strings.Contains(r.Reason, "spin") || r.Tool != "spin" {
		t.Errorf("record should carry the stall reason and tool: reason=%q tool=%q", r.Reason, r.Tool)
	}
}

// Every non-switch disposition is recorded too — a declined, stopped, or failed
// escalation is as worth knowing as a completed one.
func TestEscalationRecordsEveryDisposition(t *testing.T) {
	keepTrying := func(q UserQuestion) UserAnswer { return UserAnswer{Answer: q.Options[1]} }
	stop := func(q UserQuestion) UserAnswer { return UserAnswer{Answer: q.Options[2]} }
	cases := []struct {
		name   string
		esc    *fakeEscalator
		answer func(UserQuestion) UserAnswer
		auto   bool
		want   EscalationDisposition
		detail string // substring expected in Detail, "" for none
	}{
		{"declined", sonnetTarget(true, nil), keepTrying, false, EscalationDeclined, ""},
		{"stopped", sonnetTarget(true, nil), stop, false, EscalationStopped, ""},
		{"failed", sonnetTarget(false, fmt.Errorf("no credential for anthropic")), nil, true, EscalationFailed, "no credential"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var asker Asker
			if tc.answer != nil {
				asker = &recordingAsker{answer: tc.answer}
			}
			a, _ := escalatingAgent(t, tc.esc, asker, tc.auto)
			got := records(a)
			if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
				t.Fatalf("Prompt: %v", err)
			}
			recs := got()
			if len(recs) != 1 {
				t.Fatalf("expected exactly one record, got %d", len(recs))
			}
			if recs[0].Disposition != tc.want {
				t.Errorf("disposition = %q, want %q", recs[0].Disposition, tc.want)
			}
			if tc.detail != "" && !strings.Contains(recs[0].Detail, tc.detail) {
				t.Errorf("Detail = %q, want it to contain %q", recs[0].Detail, tc.detail)
			}
		})
	}
}

// The live path (PR 3): the same spinning turn emits EvStall when the detector
// nudges and EvEscalation when it swaps, on the Prompt sink — what a UI renders
// in real time, alongside the durable rows.
func TestStallAndEscalationEmitLiveEvents(t *testing.T) {
	esc := sonnetTarget(true, nil)
	a, _ := escalatingAgent(t, esc, nil, true) // auto: swaps without asking
	var stalls []EvStall
	var escs []EvEscalation
	sink := func(ev AgentEvent) {
		switch e := ev.(type) {
		case EvStall:
			stalls = append(stalls, e)
		case EvEscalation:
			escs = append(escs, e)
		}
	}
	if err := a.Prompt(context.Background(), "go", nil, sink); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(stalls) != 1 {
		t.Fatalf("the nudge should emit exactly one EvStall, got %d", len(stalls))
	}
	if stalls[0].Tool != "spin" || stalls[0].Axis != stallAxisChurn {
		t.Errorf("EvStall should name the looping tool and axis: %+v", stalls[0].StallRecord)
	}
	if len(escs) != 1 {
		t.Fatalf("the swap should emit exactly one EvEscalation, got %d", len(escs))
	}
	if escs[0].Disposition != EscalationSwitched || escs[0].ToModel != "claude-sonnet-5" {
		t.Errorf("EvEscalation should report the switch and its target: %+v", escs[0].EscalationRecord)
	}
}

// No target, no record: a host with an Escalator but no configured destination
// (the shipped default) leaves the log clean.
func TestEscalationRecordsNothingWithoutATarget(t *testing.T) {
	esc := &fakeEscalator{hasTarget: false}
	a, _ := escalatingAgent(t, esc, nil, true)
	got := records(a)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if n := len(got()); n != 0 {
		t.Errorf("no target configured must record nothing, got %d records", n)
	}
}
