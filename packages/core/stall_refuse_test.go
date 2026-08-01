package core

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/provider"
)

// The fixture here is the session that reopened a settled question. A model
// called task_update with `{"id":"task-62"}` — an id and no field to change —
// which the store accepted, applied to nothing, and answered "Updated task-62 →
// pending: <the same title>". Reading its own unchanged value back, the model
// concluded the write had not landed and sent the identical call again. It did
// that FORTY-FIVE times in one turn, then kept doing it across a user turn in
// which the user named the missing field outright.
//
// Both in-band notes landed and were acknowledged. Between calls the model
// narrated the correct diagnosis of its own bug — "every one of my task_update
// calls literally only passed {"id": "task-62"}" — and then made the call again.
// That is the fact that settles it: the loop is not a misunderstanding prose can
// clear up, because the model's own prose already said the right thing. Rungs 1
// and 2 are things terva SAYS; nothing it said could reach a loop whose next
// token was being chosen by forty-five identical precedents in the context.
//
// So the last rungs are things terva DOES: stop dispatching the call (which
// changes the RESULT, the other half of the pattern), and then stop the turn.
// docs/proposals/stuck-loop-escalation.md ruled refusal out on a false-positive
// argument; these pin the narrowness that answers it.

// noopUpdate is one turn of that loop: identical args, identical successful
// result. Nothing here is an error, which is the point — the churn axis never
// sees this loop at all.
func noopUpdate(tr *stallTracker, n int) {
	for i := 0; i < n; i++ {
		tr.observe(
			call("u", "task_update", `{"id":"task-62"}`),
			result("u", "Updated task-62 → pending: Anchor cephalopod design decisions", false),
		)
	}
}

func updateCall() provider.ToolCallBlock {
	return provider.ToolCallBlock{ID: "u", Name: "task_update", Arguments: json.RawMessage(`{"id":"task-62"}`)}
}

// Every watermark has to be reachable inside the window it is counted in, or the
// rung it gates is dead code that looks alive.
func TestStallLadderFitsInsideTheWindow(t *testing.T) {
	if stallRefuseAt > stallWindow {
		t.Fatalf("refusal at %d can never fire in a window of %d", stallRefuseAt, stallWindow)
	}
	if stallThreshold+stallEscalateAfterNudge > stallRefuseAt {
		t.Fatalf("refusal at %d must sit above the escalate watermark %d",
			stallRefuseAt, stallThreshold+stallEscalateAfterNudge)
	}
}

func TestStallRefusesTheCallOnceTheNotesHaveFailed(t *testing.T) {
	var tr stallTracker
	noopUpdate(&tr, stallRefuseAt-1)
	if _, ok := tr.refuse(updateCall()); ok {
		t.Fatalf("refused after %d repeats; the watermark is %d", stallRefuseAt-1, stallRefuseAt)
	}

	noopUpdate(&tr, 1)
	reason, ok := tr.refuse(updateCall())
	if !ok {
		t.Fatalf("%d identical results must stop being dispatched", stallRefuseAt)
	}
	// The model has to learn three things from this that it could not learn from
	// the notes: nothing ran, why running it again cannot help, and what happens
	// if it does it anyway.
	if !strings.Contains(reason, "task_update") {
		t.Errorf("the refusal must name the call:\n%s", reason)
	}
	if !strings.Contains(reason, "NOT run") {
		t.Errorf("a model that thinks the call landed will go looking for its effects:\n%s", reason)
	}
	if !strings.Contains(reason, "turn ends") {
		t.Errorf("the refusal should give notice of what happens next:\n%s", reason)
	}
}

// The refusals must not be fed back into the window they are derived from. They
// would push the spinning signature out of it and lift the block after a single
// refused call — the loop would resume, having cost an extra round trip to
// achieve nothing.
func TestStallRefusalsAreNotCountedAsTheModelsWork(t *testing.T) {
	var tr stallTracker
	noopUpdate(&tr, stallRefuseAt)

	for i := 1; i <= stallRefuseMax; i++ {
		reason, ok := tr.refuse(updateCall())
		if !ok {
			t.Fatalf("refusal %d: the block lifted while the loop was still running", i)
		}
		// What executeTools does with the refusal: it becomes a tool result, and
		// the tracker sees it paired with the call it never ran.
		tr.observe(call("u", "task_update", `{"id":"task-62"}`), result("u", reason, true))
	}
}

func TestStallEndsTheTurnWhenTheRefusalsAreIgnoredToo(t *testing.T) {
	var tr stallTracker
	noopUpdate(&tr, stallRefuseAt)

	for i := 1; i < stallRefuseMax; i++ {
		tr.refuse(updateCall())
		if _, done := tr.gaveUp(); done {
			t.Fatalf("gave up after %d refusal(s); the model gets %d", i, stallRefuseMax)
		}
	}
	tr.refuse(updateCall())
	g, done := tr.gaveUp()
	if !done {
		t.Fatalf("%d ignored refusals must end the turn", stallRefuseMax)
	}
	if g.tool != "task_update" || g.refusals != stallRefuseMax || g.count < stallRefuseAt {
		t.Errorf("the give-up should carry what happened, got %+v", g)
	}
	// Consumed, not latched: a second read must not end a second turn.
	if _, again := tr.gaveUp(); again {
		t.Error("the give-up signal fired twice")
	}
}

// The block is derived from the live window rather than latched, so it expires
// on its own. This is the answer to the proposal's false-positive worry: a
// refusal cannot outlive the evidence for it, because the moment the model does
// other work the premise ("nothing about this call is changing") stops holding.
func TestStallRefusalExpiresWhenTheModelDoesSomethingElse(t *testing.T) {
	var tr stallTracker
	noopUpdate(&tr, stallRefuseAt)
	if _, ok := tr.refuse(updateCall()); !ok {
		t.Fatal("precondition: the loop should be blocked")
	}

	for i := 0; i < stallWindow; i++ {
		tr.observe(
			call("w", "write", `{"path":"Octopus.md"}`),
			result("w", "wrote 123 lines", false),
		)
	}
	if _, ok := tr.refuse(updateCall()); ok {
		t.Error("a call the model stopped repeating must be dispatched again")
	}
}

// The loop in the fixture outlived a user turn: the user typed an instruction
// naming the missing field, and the very next call was the identical no-op.
// Counting only within the turn hands a wedged model a fresh allowance every
// time someone tries to intervene.
func TestStallRefusesSoonerWhenTheLoopSurvivesAUserTurn(t *testing.T) {
	var tr stallTracker
	noopUpdate(&tr, stallRefuseAt)
	tr.reset() // the user speaks; a new turn begins

	noopUpdate(&tr, stallThreshold-1)
	if _, ok := tr.refuse(updateCall()); ok {
		t.Fatal("a fresh turn still needs a local pattern before anything is refused")
	}
	noopUpdate(&tr, 1)
	if _, ok := tr.refuse(updateCall()); !ok {
		t.Errorf("a loop that walked through a user turn should be refused at %d local repeats, not %d",
			stallThreshold, stallRefuseAt)
	}
}

// The other half of that composition: history alone never refuses. Without a
// local run there is no evidence the loop is still live, and a new turn is
// usually the user asking for something else.
func TestStallRefusalAlwaysNeedsALocalPattern(t *testing.T) {
	var tr stallTracker
	noopUpdate(&tr, stallRefuseAt*2)
	tr.reset()

	for i := 0; i < stallThreshold-1; i++ {
		if _, ok := tr.refuse(updateCall()); ok {
			t.Fatalf("refused on repeat %d of a new turn with no local pattern", i)
		}
		noopUpdate(&tr, 1)
	}
}

// A model arriving on an escalation swap inherits the transcript, not the
// strikes. Escalating exists to give the stuck step another chance, and a turn
// one refusal from ending would not be one.
func TestStallEscalationPardonsTheIncomingModel(t *testing.T) {
	var tr stallTracker
	noopUpdate(&tr, stallRefuseAt)
	for i := 0; i < stallRefuseMax-1; i++ {
		tr.refuse(updateCall())
	}

	tr.pardon() // what maybeEscalate does on a successful swap

	if _, done := tr.gaveUp(); done {
		t.Fatal("the swap should have cleared the give-up")
	}
	// Still blocked — the evidence that this call's output is not moving is as
	// true for the new model — but with a full allowance of its own.
	for i := 1; i <= stallRefuseMax; i++ {
		if _, ok := tr.refuse(updateCall()); !ok {
			t.Fatalf("refusal %d: the block should have survived the swap", i)
		}
		if _, done := tr.gaveUp(); done != (i == stallRefuseMax) {
			t.Errorf("refusal %d: gave up=%v", i, done)
		}
	}
}

// spinProbeTool records whether it was ever dispatched: the claim under test is
// that the tool does not RUN, and only the tool can testify to that.
type spinProbeTool struct{ runs atomic.Int32 }

func (t *spinProbeTool) Name() string            { return "task_update" }
func (t *spinProbeTool) Description() string     { return "spins" }
func (t *spinProbeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *spinProbeTool) Execute(context.Context, json.RawMessage, func(string)) (ToolResult, error) {
	t.runs.Add(1)
	return ToolResult{Content: []provider.Content{provider.TextBlock{
		Text: "Updated task-62 → pending: Anchor cephalopod design decisions",
	}}}, nil
}

// Through the production caller. The tracker's own tests drive record/refuse
// directly, which would pass just as well if runOneTool never consulted it — the
// gap that let a two-halves-correct change ship with nobody in the slot.
func TestStallRefusalReachesDispatchAndIsRecorded(t *testing.T) {
	tool := &spinProbeTool{}
	a := &Agent{}
	a.SetStallDetection(true)
	reg := Registry{"task_update": tool}
	var got []StallRecord
	sink := collectStalls(&got)

	for i := 0; i < stallRefuseAt; i++ {
		res := a.runOneTool(context.Background(), updateCall(), reg, sink)
		a.stall.observe(
			call("u", "task_update", `{"id":"task-62"}`),
			result("u", "Updated task-62 → pending: Anchor cephalopod design decisions", res.IsError),
		)
	}
	if int(tool.runs.Load()) != stallRefuseAt {
		t.Fatalf("precondition: %d calls should have run, got %d", stallRefuseAt, tool.runs.Load())
	}

	res := a.runOneTool(context.Background(), updateCall(), reg, sink)
	if int(tool.runs.Load()) != stallRefuseAt {
		t.Errorf("the refused call still reached the tool (%d runs)", tool.runs.Load())
	}
	if !res.IsError {
		t.Error("a refusal must arrive as a tool error, not as a result the model can read past")
	}
	if len(got) != 1 || got[0].Rung != 3 || got[0].Tool != "task_update" {
		t.Fatalf("the refusal must be recorded as rung 3, got %+v", got)
	}
}

// Detection off means detection off: the feature toggle has to reach the rung
// that acts, not only the ones that talk.
func TestStallRefusalIsSkippedWhenDetectionIsOff(t *testing.T) {
	tool := &spinProbeTool{}
	a := &Agent{} // stallDetect defaults off at the core zero value
	reg := Registry{"task_update": tool}

	for i := 0; i < stallRefuseAt*2; i++ {
		a.runOneTool(context.Background(), updateCall(), reg, func(AgentEvent) {})
	}
	if int(tool.runs.Load()) != stallRefuseAt*2 {
		t.Errorf("with detection off every call must be dispatched, got %d of %d",
			tool.runs.Load(), stallRefuseAt*2)
	}
}

// wedgedClient is the fixture's model: it answers every step with the same tool
// call, forever, exactly as a model whose next token is being chosen against a
// context full of that call would. The escape hatch after wedgedClientGiveUp
// steps exists only so a regression fails the test instead of hanging it — the
// loop it emits is genuinely unbounded, and MaxSteps is 0 outside the SDK.
const wedgedClientGiveUp = 40

type wedgedClient struct{ steps atomic.Int32 }

func (c *wedgedClient) Name() string { return "wedged" }

func (c *wedgedClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	n := c.steps.Add(1)
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "wedged", Model: req.Model}
		if n > wedgedClientGiveUp {
			out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: "fine, I stop"}},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
			Role: provider.RoleAssistant,
			Content: []provider.Content{
				provider.TextBlock{Text: "setting the status this time"},
				provider.ToolCallBlock{ID: "u", Name: "task_update", Arguments: json.RawMessage(`{"id":"task-62"}`)},
			},
		}}
	}()
	return out, nil
}

// The whole claim, through the whole loop: a model that will not stop repeating
// itself gets stopped. Everything above tests a piece of the ladder; this is the
// thing the user actually reported, and it is the one that would have caught the
// gap — the ladder's parts were all individually correct while a turn ran 45
// identical calls with nothing left to say.
func TestAWedgedTurnEndsInsteadOfRunningForever(t *testing.T) {
	tool := &spinProbeTool{}
	client := &wedgedClient{}
	a := NewAgent(client, "wedged-model", "system", Registry{"task_update": tool})
	a.SetStallDetection(true)

	var got []StallRecord
	if err := a.Prompt(context.Background(), "add the octopus", nil, collectStalls(&got)); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	if int(client.steps.Load()) > wedgedClientGiveUp {
		t.Fatal("the turn only ended because the fixture ran out of patience — the ladder never stopped it")
	}
	// Seven dispatches to earn the block, then nothing but refusals.
	if int(tool.runs.Load()) != stallRefuseAt {
		t.Errorf("the tool ran %d times, want %d (the calls after the block must not reach it)",
			tool.runs.Load(), stallRefuseAt)
	}
	// The user is owed the whole story: told, told again, blocked, stopped.
	rungs := map[int]int{}
	for _, r := range got {
		rungs[r.Rung]++
	}
	if rungs[1] == 0 || rungs[3] != stallRefuseMax || rungs[4] != 1 {
		t.Errorf("want a nudge, %d refusals and one give-up, got %v", stallRefuseMax, rungs)
	}
}

// The give-up is the user's answer, not the model's: it says what ended the turn
// in terms someone reading the pane can act on.
func TestStallGiveUpRecordsWhyTheTurnEnded(t *testing.T) {
	a := &Agent{}
	a.SetStallDetection(true)
	var got []StallRecord
	sink := collectStalls(&got)

	noopUpdate(&a.stall, stallRefuseAt)
	for i := 0; i < stallRefuseMax; i++ {
		a.stall.refuse(updateCall())
	}
	if !a.stallGiveUp(sink) {
		t.Fatal("the turn should have ended")
	}
	if len(got) != 1 || got[0].Rung != 4 {
		t.Fatalf("want one rung-4 record, got %+v", got)
	}
	if !strings.Contains(got[0].Detail, "task_update") {
		t.Errorf("the reason should name the loop:\n%s", got[0].Detail)
	}
	if a.stallGiveUp(sink) {
		t.Error("the turn ended twice on one signal")
	}
}
