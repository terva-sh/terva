package expression

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/testsupport"
)

var t0 = time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return t0.Add(d) }

func toolError(member, tool string, d time.Duration) talkoot.Line {
	return talkoot.Line{Type: talkoot.LineToolError, At: at(d), Member: member, Tool: tool, Reason: "boom"}
}

func turn(member string, d time.Duration, failed bool) talkoot.Line {
	l := talkoot.Line{Type: talkoot.LineTurn, At: at(d), Member: member}
	if failed {
		l.Guard, l.Reason = talkoot.GuardFailed, "the provider refused the request"
	}
	return l
}

func envelope(id, from string, kind talkoot.Kind, d time.Duration, to ...string) talkoot.Line {
	return talkoot.Line{Type: talkoot.LineEnvelope, At: at(d), Envelope: &talkoot.Envelope{ID: id, From: from, To: to, Kind: kind, At: at(d)}}
}

func observe(e *Table, lines ...talkoot.Line) []Beat {
	var out []Beat
	for _, l := range lines {
		out = append(out, e.Observe(l)...)
	}
	return out
}

// Three failed tool calls in one turn frustrate a member, and six draw the
// strong form. Two do not.
func TestToolErrorsBuildInATurn(t *testing.T) {
	e := NewTable(1, nil)
	observe(e, toolError("jev", "edit", 0), toolError("jev", "edit", time.Second))
	if x := e.Expression("jev", at(time.Second)); x.Pose != "" {
		t.Fatalf("two tool errors = %+v, want no expression", x)
	}
	observe(e, toolError("jev", "bash", 2*time.Second))
	x := e.Expression("jev", at(2*time.Second))
	if x.Pose != PoseFrustrated || x.Intensity != 0 || x.Cause != "turn 1, tool_error x3 (edit, bash) -> frustrated" {
		t.Fatalf("three tool errors = %+v", x)
	}
	for i := range 3 {
		observe(e, toolError("jev", "read", time.Duration(3+i)*time.Second))
	}
	if x := e.Expression("jev", at(6*time.Second)); x.Pose != PoseFrustrated || x.Intensity != 1 {
		t.Fatalf("six tool errors = %+v, want the strong form", x)
	}
	// The held pose lasts its row's hold, and no longer.
	if x := e.Expression("jev", at(5*time.Second+10*time.Minute-1)); x.Pose != PoseFrustrated {
		t.Fatalf("inside the hold = %+v", x)
	}
	if x := e.Expression("jev", at(5*time.Second+10*time.Minute)); x.Pose != "" {
		t.Fatalf("past the hold = %+v", x)
	}
}

// A turn line ends the count, so errors in two turns do not add up. A clean
// turn clears the held pose, and a turn with an error in it does not.
func TestATurnEndsTheCountAndACleanTurnClears(t *testing.T) {
	e := NewTable(1, nil)
	observe(e, toolError("jev", "edit", 0), toolError("jev", "edit", 1), turn("jev", 2, false),
		toolError("jev", "edit", 3), toolError("jev", "edit", 4))
	if x := e.Expression("jev", at(5)); x.Pose != "" {
		t.Fatalf("two errors in each of two turns = %+v", x)
	}
	observe(e, toolError("jev", "edit", 5), turn("jev", 6, false))
	if x := e.Expression("jev", at(7)); x.Pose != PoseFrustrated {
		t.Fatalf("a turn with errors cleared the pose: %+v", x)
	}
	observe(e, turn("jev", time.Minute, false))
	if x := e.Expression("jev", at(time.Minute)); x.Pose != "" {
		t.Fatalf("a clean turn left %+v", x)
	}
}

// A failed turn frustrates a member, two in a row draw the strong form, and
// a turn that did not fail ends the row.
func TestFailedTurnsInARow(t *testing.T) {
	e := NewTable(1, nil)
	observe(e, turn("jev", 0, true))
	if x := e.Expression("jev", at(0)); x.Pose != PoseFrustrated || x.Intensity != 0 {
		t.Fatalf("one failed turn = %+v", x)
	}
	observe(e, turn("jev", time.Second, true))
	if x := e.Expression("jev", at(time.Second)); x.Pose != PoseFrustrated || x.Intensity != 1 {
		t.Fatalf("two failed turns = %+v", x)
	}
	// A turn with a tool error ends the row but keeps the mood, so the pose
	// held from the row keeps its strong form.
	observe(e, toolError("jev", "x", 2*time.Second), turn("jev", 3*time.Second, false), turn("jev", 4*time.Second, true))
	if x := e.Expression("jev", at(4*time.Second)); x.Intensity != 1 || x.Cause != "turn 4 failed, x1 in a row (the provider refused the request) -> frustrated (strong 1)" {
		t.Fatalf("a failed turn while the strong pose holds = %+v", x)
	}
	// A clean turn clears the pose, so the next failed turn starts over.
	observe(e, turn("jev", 5*time.Second, false), turn("jev", 6*time.Second, true))
	if x := e.Expression("jev", at(6*time.Second)); x.Intensity != 0 || x.Cause != "turn 6 failed, x1 in a row (the provider refused the request) -> frustrated" {
		t.Fatalf("a failed turn after a clean one = %+v", x)
	}
}

// A retry worries a member, and the third attempt draws the strong form.
func TestRetriesWorry(t *testing.T) {
	e := NewTable(1, nil)
	observe(e, talkoot.Line{Type: talkoot.LineRetry, At: at(0), Member: "jev", Attempt: 1, Reason: "overloaded"})
	if x := e.Expression("jev", at(0)); x.Pose != PoseWorried || x.Intensity != 0 {
		t.Fatalf("a retry = %+v", x)
	}
	observe(e, talkoot.Line{Type: talkoot.LineRetry, At: at(time.Second), Member: "jev", Attempt: 3, Reason: "overloaded"})
	if x := e.Expression("jev", at(time.Second)); x.Pose != PoseWorried || x.Intensity != 1 {
		t.Fatalf("a third attempt = %+v", x)
	}
	if x := e.Expression("jev", at(time.Second+5*time.Minute)); x.Pose != "" {
		t.Fatalf("past the hold = %+v", x)
	}
}

// A guard that refused a member's send frustrates it. A refused delivery is
// not the member's send.
func TestARefusedSendFrustrates(t *testing.T) {
	e := NewTable(1, nil)
	observe(e, talkoot.Line{Type: talkoot.LineGuard, At: at(0), Member: "gage", Guard: talkoot.GuardDelivery, Action: talkoot.ActionRefused})
	if x := e.Expression("gage", at(0)); x.Pose != "" {
		t.Fatalf("a refused delivery = %+v", x)
	}
	observe(e, talkoot.Line{Type: talkoot.LineGuard, At: at(0), Member: "jev", Guard: talkoot.GuardRate, Action: talkoot.ActionRefused, Reason: "8 envelopes in 1m0s"})
	if x := e.Expression("jev", at(0)); x.Pose != PoseFrustrated || x.Cause != "guard rate refused a send (8 envelopes in 1m0s) -> frustrated" {
		t.Fatalf("a refused send = %+v", x)
	}
}

// A card that waits past five minutes raises the brow over a held pose, and
// Next names the moment. A close lowers it, and an answer blinks slowly.
func TestAWaitingCardRaisesTheBrow(t *testing.T) {
	e := NewTable(1, nil)
	observe(e, turn("jev", 0, true),
		talkoot.Line{Type: talkoot.LineCardOpen, At: at(time.Minute), Member: "jev", Card: talkoot.CardQuestion, Ref: "ask_1"})
	if x := e.Expression("jev", at(5*time.Minute)); x.Pose != PoseFrustrated {
		t.Fatalf("before the wait = %+v", x)
	}
	if n := e.Next(at(5 * time.Minute)); !n.Equal(at(6 * time.Minute)) {
		t.Fatalf("next = %s, want the wait's end", n)
	}
	x := e.Expression("jev", at(6*time.Minute))
	if x.Pose != PoseLookingUp || x.Intensity != 1 || x.Cause == "" {
		t.Fatalf("past the wait = %+v", x)
	}
	beats := observe(e, talkoot.Line{Type: talkoot.LineCardClose, At: at(7 * time.Minute), Member: "jev", Card: talkoot.CardQuestion, Ref: "ask_1", Outcome: talkoot.OutcomeAnswered})
	if len(beats) != 1 || beats[0].Beat != BeatSlowBlink || beats[0].Member != "jev" || beats[0].Cause != "question ask_1 answered -> slow-blink" {
		t.Fatalf("an answer played %+v", beats)
	}
	if x := e.Expression("jev", at(7*time.Minute)); x.Pose != PoseFrustrated {
		t.Fatalf("after the close = %+v, want the held pose back", x)
	}
	// A denied approval closes its card and plays nothing.
	observe(e, talkoot.Line{Type: talkoot.LineCardOpen, At: at(8 * time.Minute), Member: "jev", Card: talkoot.CardPermission, Ref: "call_1"})
	if b := observe(e, talkoot.Line{Type: talkoot.LineCardClose, At: at(9 * time.Minute), Member: "jev", Card: talkoot.CardPermission, Ref: "call_1", Outcome: talkoot.OutcomeDenied}); len(b) != 0 {
		t.Fatalf("a denial played %+v", b)
	}
}

// A weaker row that shows the same pose keeps the strong form and the later
// end: a refused post cannot cut two failed turns short.
func TestAShorterRowKeepsTheLongerHold(t *testing.T) {
	e := NewTable(1, nil)
	observe(e, turn("jev", 0, true), turn("jev", time.Minute, true),
		talkoot.Line{Type: talkoot.LineGuard, At: at(2 * time.Minute), Member: "jev", Guard: talkoot.GuardRate, Action: talkoot.ActionRefused, Reason: "8 envelopes"})
	if x := e.Expression("jev", at(8*time.Minute)); x.Pose != PoseFrustrated || x.Intensity != 1 {
		t.Fatalf("past the refusal's own hold = %+v, want the failed turns' strong hold", x)
	}
	if x := e.Expression("jev", at(11*time.Minute)); x.Pose != "" {
		t.Fatalf("past the failed turns' hold = %+v", x)
	}
}

// A pose that changes with time alone leaves its time in the trace: the end
// of a hold, and the moment an open card raises the brow.
func TestTheTraceNamesTheTimesAPoseChanges(t *testing.T) {
	e := NewTable(1, nil)
	observe(e, turn("jev", 0, true),
		talkoot.Line{Type: talkoot.LineCardOpen, At: at(time.Minute), Member: "jev", Card: talkoot.CardQuestion, Ref: "ask_1"})
	tr := e.Trace("jev")
	if len(tr) != 2 {
		t.Fatalf("trace = %+v", tr)
	}
	if want := "turn 1 failed, x1 in a row (the provider refused the request) -> frustrated, until 2026-09-30T09:10:00Z"; tr[0].Cause != want {
		t.Errorf("hold entry cause = %q, want %q", tr[0].Cause, want)
	}
	if want := "card ask_1 raises the brow at 2026-09-30T09:06:00Z if it is still open"; tr[1].Cause != want {
		t.Errorf("card entry cause = %q, want %q", tr[1].Cause, want)
	}
}

// A refused send is trouble in its turn, so the turn that holds it is not
// clean and does not clear the frown it caused.
func TestARefusedSendOutlastsItsOwnTurn(t *testing.T) {
	e := NewTable(1, nil)
	observe(e, talkoot.Line{Type: talkoot.LineGuard, At: at(0), Member: "jev", Guard: talkoot.GuardRate, Action: talkoot.ActionRefused, Reason: "8 envelopes"},
		turn("jev", time.Second, false))
	if x := e.Expression("jev", at(2*time.Second)); x.Pose != PoseFrustrated {
		t.Fatalf("after the refusal's own turn = %+v", x)
	}
	observe(e, turn("jev", time.Minute, false))
	if x := e.Expression("jev", at(time.Minute)); x.Pose != "" {
		t.Fatalf("after a clean turn = %+v", x)
	}
}

// A different pose replaces the held one, even a longer and stronger one:
// the face shows the newest reading.
func TestTheNewestPoseWins(t *testing.T) {
	e := NewTable(1, nil)
	observe(e, turn("jev", 0, true), turn("jev", time.Minute, true),
		talkoot.Line{Type: talkoot.LineRetry, At: at(2 * time.Minute), Member: "jev", Attempt: 1, Reason: "overloaded"})
	if x := e.Expression("jev", at(3*time.Minute)); x.Pose != PoseWorried || x.Intensity != 0 {
		t.Fatalf("after a retry = %+v", x)
	}
	if x := e.Expression("jev", at(8*time.Minute)); x.Pose != "" {
		t.Fatalf("after the retry's hold = %+v", x)
	}
}

// Advance records what time alone changed, at the moment it changed, so the
// live engine, which the run's timer advances, and a replay, which advances
// to each line, keep the same trace.
func TestAdvanceRecordsWhatTimeChanged(t *testing.T) {
	lines := []talkoot.Line{
		turn("jev", 0, true),
		{Type: talkoot.LineCardOpen, At: at(time.Minute), Member: "jev", Card: talkoot.CardQuestion, Ref: "ask_1"},
		toolError("jev", "edit", 12*time.Minute),
	}
	live := NewTable(1, nil)
	observe(live, lines[:2]...)
	live.Advance(at(7 * time.Minute))
	live.Advance(at(11 * time.Minute))
	live.Advance(at(11 * time.Minute))
	observe(live, lines[2])
	replay := NewTable(1, nil)
	observe(replay, lines...)
	got := live.Trace("jev")
	if !reflect.DeepEqual(got, replay.Trace("jev")) {
		t.Fatalf("live trace\n%+v\nreplay trace\n%+v", got, replay.Trace("jev"))
	}
	var timed []string
	for _, e := range got {
		if e.Signal == "card_wait" || e.Signal == "hold ended" {
			timed = append(timed, fmt.Sprintf("%s %s %s", e.At.Sub(t0), e.Signal, e.Pose))
		}
	}
	if want := []string{"6m0s card_wait looking-up", "10m0s hold ended "}; !reflect.DeepEqual(timed, want) {
		t.Fatalf("timed entries = %q, want %q", timed, want)
	}
}

// A card open when the daemon stopped did not survive the stop, so the end of
// a replay closes it.
func TestAReplayClosesTheCardsOfTheStop(t *testing.T) {
	e := NewTable(1, nil)
	observe(e, talkoot.Line{Type: talkoot.LineCardOpen, At: at(0), Member: "jev", Card: talkoot.CardQuestion, Ref: "ask_1"})
	e.Replayed()
	if x := e.Expression("jev", at(time.Hour)); x.Pose != "" {
		t.Fatalf("a card from before the stop = %+v", x)
	}
	if n := e.Next(at(0)); !n.IsZero() {
		t.Fatalf("next = %s, want none", n)
	}
}

// A member's message glances at its first member recipient, and a person
// neither glances nor draws one. A handoff makes its sender happy when a
// recipient takes it, once.
func TestSendsGlanceAndAPickedUpHandoffIsHappy(t *testing.T) {
	e := NewTable(1, nil)
	if b := observe(e, envelope("e0", "human:sothr", talkoot.KindMessage, 0, "jev")); len(b) != 0 {
		t.Fatalf("a person's post played %+v", b)
	}
	b := observe(e, envelope("e1", "helm", talkoot.KindMessage, 0, "human:sothr", "jev", "gage"))
	if len(b) != 1 || b[0].Beat != BeatGlance || b[0].Member != "helm" || b[0].Toward != "jev" {
		t.Fatalf("a message played %+v", b)
	}
	if b := observe(e, envelope("e2", "helm", talkoot.KindHandoff, time.Second, "jev")); len(b) != 1 || b[0].Beat != BeatGlance {
		t.Fatalf("a handoff played %+v", b)
	}
	// A delivery to a driver that reports reads names no chain, and is not
	// the pickup. The read is.
	if b := observe(e, talkoot.Line{Type: talkoot.LineDelivery, At: at(2 * time.Second), Member: "jev", Ref: "e2"}); len(b) != 0 {
		t.Fatalf("a delivery before the read played %+v", b)
	}
	b = observe(e, talkoot.Line{Type: talkoot.LineRead, At: at(3 * time.Second), Member: "jev", Ref: "e2", Chain: "c"})
	if len(b) != 1 || b[0].Beat != BeatHappy || b[0].Member != "helm" || b[0].Cause != "handoff e2 picked up by jev -> happy" {
		t.Fatalf("the pickup played %+v", b)
	}
	if b := observe(e, talkoot.Line{Type: talkoot.LineRead, At: at(4 * time.Second), Member: "jev", Ref: "e2", Chain: "c"}); len(b) != 0 {
		t.Fatalf("a second pickup played %+v", b)
	}
	// A delivery to a driver that reports no reads names its chain, and it
	// is the pickup.
	observe(e, envelope("e3", "helm", talkoot.KindHandoff, 5*time.Second, "gage"))
	if b := observe(e, talkoot.Line{Type: talkoot.LineDelivery, At: at(6 * time.Second), Member: "gage", Ref: "e3", Chain: "c"}); len(b) != 1 || b[0].Beat != BeatHappy {
		t.Fatalf("a worker's pickup played %+v", b)
	}
}

// A member's proposal that a person applies makes the member happy. A
// person's own change plays nothing.
func TestAnAppliedProposalIsHappy(t *testing.T) {
	e := NewTable(1, nil)
	b := observe(e, talkoot.Line{Type: talkoot.LineRoster, At: at(0), By: "human:sothr", Proposal: "p1", Proposer: "helm"})
	if len(b) != 1 || b[0].Member != "helm" || b[0].Beat != BeatHappy {
		t.Fatalf("an applied proposal played %+v", b)
	}
	if b := observe(e, talkoot.Line{Type: talkoot.LineRoster, At: at(0), By: "human:sothr", Proposal: "p2", Proposer: "human:sothr"}); len(b) != 0 {
		t.Fatalf("a person's proposal played %+v", b)
	}
}

// The default table picks only from the closed sets, and every held pose it
// shows comes with a hold.
func TestTheDefaultTableKeepsToTheSets(t *testing.T) {
	for _, r := range DefaultTable {
		switch {
		case r.Pose != "" && r.Beat != "":
			t.Errorf("%+v sets a pose and a beat", r)
		case r.Pose != "" && !slices.Contains(Poses, r.Pose):
			t.Errorf("%+v shows a pose outside the set", r)
		case r.Beat != "" && !slices.Contains(Beats, r.Beat):
			t.Errorf("%+v plays a beat outside the set", r)
		case r.Pose != "" && r.Signal != SignalCardWait && r.Hold <= 0:
			t.Errorf("%+v holds its pose for no time", r)
		case r.Pose == "" && r.Beat == "":
			t.Errorf("%+v shows nothing", r)
		}
	}
}

// A trace keeps the newest TraceLen entries.
func TestTheTraceIsBounded(t *testing.T) {
	e := NewTable(1, nil)
	for i := range TraceLen + 5 {
		observe(e, talkoot.Line{Type: talkoot.LineCardOpen, At: at(time.Duration(i)), Member: "jev", Card: talkoot.CardQuestion, Ref: fmt.Sprint(i)})
	}
	tr := e.Trace("jev")
	if len(tr) != TraceLen || !tr[0].At.Equal(at(5)) {
		t.Fatalf("trace holds %d entries from %s", len(tr), tr[0].At)
	}
}

// script is a day of a small team, with every signal the table reads.
func script() []talkoot.Line {
	var ls []talkoot.Line
	ls = append(ls,
		envelope("e1", "human:sothr", talkoot.KindMessage, 0, "helm"),
		envelope("e2", "helm", talkoot.KindHandoff, time.Second, "jev"),
		talkoot.Line{Type: talkoot.LineRead, At: at(2 * time.Second), Member: "jev", Ref: "e2", Chain: "e1"},
	)
	for i := range 4 {
		ls = append(ls, toolError("jev", "edit", time.Duration(3+i)*time.Second))
	}
	ls = append(ls,
		talkoot.Line{Type: talkoot.LineRetry, At: at(8 * time.Second), Member: "jev", Attempt: 1, Reason: "overloaded"},
		turn("jev", 9*time.Second, true),
		talkoot.Line{Type: talkoot.LineCardOpen, At: at(10 * time.Second), Member: "helm", Card: talkoot.CardQuestion, Ref: "ask_1"},
		talkoot.Line{Type: talkoot.LineCardClose, At: at(20 * time.Minute), Member: "helm", Card: talkoot.CardQuestion, Ref: "ask_1", Outcome: talkoot.OutcomeAnswered},
		talkoot.Line{Type: talkoot.LineRoster, At: at(21 * time.Minute), By: "human:sothr", Proposal: "p1", Proposer: "jev"},
		talkoot.Line{Type: talkoot.LineGuard, At: at(22 * time.Minute), Member: "gage", Guard: talkoot.GuardRate, Action: talkoot.ActionRefused, Reason: "8 envelopes"},
		turn("jev", 23*time.Minute, false),
		spent("helm", 23*time.Minute+30*time.Second, 9, 10),
		talkoot.Line{Type: talkoot.LineDelivery, At: at(23*time.Minute + 40*time.Second), Member: "gage", Ref: "e1", Woke: true},
		interrupted("gage", 23*time.Minute+50*time.Second),
		review("jev", "e9", 23*time.Minute+55*time.Second),
	)
	return ls
}

// snapshot is everything an engine shows after its lines: each member's
// expression at several times, and its trace.
func snapshot(e Engine) map[string]any {
	out := map[string]any{"seed": e.Seed()}
	for _, m := range []string{"helm", "jev", "gage"} {
		for _, d := range []time.Duration{0, 9 * time.Second, 6 * time.Minute, 15 * time.Minute, 24 * time.Minute} {
			out[fmt.Sprintf("%s@%s", m, d)] = e.Expression(m, at(d))
		}
		out[m+" trace"] = e.Trace(m)
	}
	return out
}

// 🔑 A replay of the sealed room with the same seed shows what the live
// engine showed. The lines go through a real room, so a field the room does
// not keep would show here as a difference.
func TestAReplayOfTheRoomShowsWhatTheLiveEngineShowed(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := talkoot.CreateRoom(dir); err != nil {
		t.Fatal(err)
	}
	room := talkoot.OpenRoom(dir)
	live := NewTable(42, nil)
	var liveBeats []Beat
	for _, l := range script() {
		if err := room.Append(l); err != nil {
			t.Fatal(err)
		}
		liveBeats = append(liveBeats, live.Observe(l)...)
	}
	lines, err := talkoot.OpenRoom(dir).Read()
	if err != nil {
		t.Fatal(err)
	}
	replay := NewTable(42, nil)
	var replayBeats []Beat
	for _, l := range lines {
		replayBeats = append(replayBeats, replay.Observe(l)...)
	}
	if got, want := snapshot(replay), snapshot(live); !reflect.DeepEqual(got, want) {
		t.Fatalf("the replay differs from the live engine:\nreplay %+v\nlive   %+v", got, want)
	}
	if !reflect.DeepEqual(replayBeats, liveBeats) {
		t.Fatalf("beats differ:\nreplay %+v\nlive   %+v", replayBeats, liveBeats)
	}
	// Every expression and every beat names its cause.
	for _, b := range liveBeats {
		if b.Cause == "" {
			t.Errorf("a beat with no cause: %+v", b)
		}
	}
	// The wake and the interrupt went through the room, so the replay plays
	// their fast blinks only if the room kept both flags.
	blinks := 0
	for _, b := range replayBeats {
		if b.Beat == BeatFastBlink {
			blinks++
		}
	}
	if blinks != 3 {
		t.Errorf("the replay played %d fast blinks, want the two wakes' and the interrupt's", blinks)
	}
	// The review went through the room, so the replay shows skeptical only if
	// the room kept the reviewer flag.
	if x := replay.Expression("jev", at(24*time.Minute)); x.Pose != PoseSkeptical {
		t.Errorf("the replay's jev at 24m = %+v, want the review's skeptical", x)
	}
	// The budget turn went through the room, so the replay worries helm only
	// if the room kept the day's spend and budget.
	if x := replay.Expression("helm", at(24*time.Minute)); x.Pose != PoseWorried {
		t.Errorf("the replay's helm at 24m = %+v, want the budget worry", x)
	}
	if len(liveBeats) < 4 {
		t.Errorf("the script played %d beats, want its glance, pickup, answer, and proposal", len(liveBeats))
	}
	for k, v := range snapshot(live) {
		if x, ok := v.(Expression); ok && x.Pose != "" && x.Cause == "" {
			t.Errorf("%s: an expression with no cause: %+v", k, x)
		}
	}
}

func interrupted(member string, d time.Duration) talkoot.Line {
	l := turn(member, d, false)
	l.Interrupted = true
	return l
}

func spent(member string, d time.Duration, spend, budget float64) talkoot.Line {
	l := turn(member, d, false)
	l.DaySpendUSD, l.DayBudgetUSD = spend, budget
	return l
}

// A turn that leaves a member past 80 percent of its daily budget worries it,
// and each such turn holds the worry again. Below 80 percent, at the whole
// budget, and with no budget of its own, nothing shows.
func TestABudgetPast80PercentWorries(t *testing.T) {
	e := NewTable(1, nil)
	observe(e, spent("jev", 0, 7.9, 10))
	if x := e.Expression("jev", at(0)); x.Pose != "" {
		t.Fatalf("79 percent = %+v, want nothing", x)
	}
	observe(e, spent("jev", time.Minute, 8.5, 10))
	x := e.Expression("jev", at(time.Minute))
	if x.Pose != PoseWorried || x.Intensity != 0 || x.Cause != "turn 2 brought the day's spend to $8.50 of the member's $10.00 (85%) -> worried" {
		t.Fatalf("85 percent = %+v", x)
	}
	if x := e.Expression("jev", at(time.Minute+10*time.Minute)); x.Pose != "" {
		t.Fatalf("past the hold = %+v", x)
	}
	observe(e, spent("jev", 20*time.Minute, 9, 10))
	if x := e.Expression("jev", at(29*time.Minute)); x.Pose != PoseWorried {
		t.Fatalf("a later turn at 90 percent = %+v, want the worry held again", x)
	}
	// The whole budget pauses the member, and the pause draws its own face.
	f := NewTable(1, nil)
	observe(f, spent("jev", 0, 10, 10))
	if x := f.Expression("jev", at(0)); x.Pose != "" {
		t.Fatalf("the whole budget = %+v, want nothing", x)
	}
	observe(f, turn("helm", 0, false))
	if x := f.Expression("helm", at(0)); x.Pose != "" {
		t.Fatalf("no budget of its own = %+v", x)
	}
}

// The worry does not replace a pose the turn's own trouble set, and a failed
// turn shows its failure alone.
func TestABudgetWorryYieldsToTheTurnsTrouble(t *testing.T) {
	e := NewTable(1, nil)
	observe(e, toolError("jev", "edit", 0), toolError("jev", "edit", 1), toolError("jev", "bash", 2), spent("jev", 3, 9, 10))
	if x := e.Expression("jev", at(3)); x.Pose != PoseFrustrated {
		t.Fatalf("a frustrating turn past 80 percent = %+v, want frustrated", x)
	}
	f := NewTable(1, nil)
	l := spent("jev", 0, 9, 10)
	l.Guard, l.Reason = talkoot.GuardFailed, "the provider refused the request"
	observe(f, l)
	if x := f.Expression("jev", at(0)); x.Pose != PoseFrustrated {
		t.Fatalf("a failed turn past 80 percent = %+v, want frustrated", x)
	}
}

// A message that reaches an idle member, and a turn something interrupted,
// each play a fast blink. A delivery to a busy member and a turn that ended
// on its own play none. A woken delivery that is also a pickup plays both.
func TestAWakeAndAnInterruptBlinkFast(t *testing.T) {
	e := NewTable(1, nil)
	busy := talkoot.Line{Type: talkoot.LineDelivery, At: at(0), Member: "jev", Ref: "e1"}
	woke := busy
	woke.Woke, woke.Ref, woke.At = true, "e2", at(time.Second)
	cut := turn("jev", 2*time.Second, false)
	cut.Interrupted = true
	var got []string
	for _, b := range observe(e, busy, turn("jev", time.Second/2, false), woke, cut) {
		got = append(got, b.Member+" "+b.Beat+" "+b.Cause)
	}
	want := []string{
		"jev fast-blink envelope e2 reached it idle -> fast-blink",
		"jev fast-blink turn 2 was interrupted -> fast-blink",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("beats = %q, want %q", got, want)
	}
	f := NewTable(1, nil)
	pick := talkoot.Line{Type: talkoot.LineDelivery, At: at(time.Second), Member: "jev", Ref: "e2", Chain: "e1", Woke: true}
	beats := observe(f, envelope("e2", "helm", talkoot.KindHandoff, 0, "jev"), pick)
	var kinds []string
	for _, b := range beats {
		kinds = append(kinds, b.Member+" "+b.Beat)
	}
	if want := []string{"helm glance", "jev fast-blink", "helm happy"}; !slices.Equal(kinds, want) {
		t.Fatalf("a woken pickup = %q, want %q", kinds, want)
	}
}

func review(member, ref string, d time.Duration) talkoot.Line {
	return talkoot.Line{Type: talkoot.LineDelivery, At: at(d), Member: member, Ref: ref, Woke: true, Reviewer: true}
}

// A reviewer looks skeptical from the delivery that starts its turn until the
// turn's line, and no longer than the row's hold. A member that is no
// reviewer does not.
func TestAReviewerLooksSkepticalForItsTurn(t *testing.T) {
	e := NewTable(1, nil)
	observe(e, review("yelp", "e1", 0))
	x := e.Expression("yelp", at(10*time.Minute))
	if x.Pose != PoseSkeptical || x.Cause != "turn 1 is a review, from envelope e1 -> skeptical" {
		t.Fatalf("a reviewer at work = %+v", x)
	}
	observe(e, turn("yelp", 20*time.Minute, false))
	if x := e.Expression("yelp", at(20*time.Minute)); x.Pose != "" {
		t.Fatalf("after the review's turn = %+v, want nothing", x)
	}
	// A turn with one failed call is not clean, so the clean-turn rule
	// leaves the held pose. The review still ends with the turn.
	observe(e, review("yelp", "e3", 22*time.Minute), toolError("yelp", "bash", 23*time.Minute), turn("yelp", 24*time.Minute, false))
	if x := e.Expression("yelp", at(24*time.Minute)); x.Pose != "" {
		t.Fatalf("after a review's unclean turn = %+v, want nothing", x)
	}
	observe(e, review("yelp", "e2", 30*time.Minute))
	if x := e.Expression("yelp", at(30*time.Minute+time.Hour)); x.Pose != "" {
		t.Fatalf("past the row's hold = %+v, want nothing", x)
	}
	f := NewTable(1, nil)
	woke := review("helm", "e1", 0)
	woke.Reviewer = false
	observe(f, woke)
	if x := f.Expression("helm", at(time.Minute)); x.Pose != "" {
		t.Fatalf("a woken member that is no reviewer = %+v", x)
	}
}

// Trouble in a review replaces skeptical, as any newer reading does, and the
// review's end does not clear it.
func TestTroubleInAReviewOutlastsIt(t *testing.T) {
	e := NewTable(1, nil)
	observe(e, review("yelp", "e1", 0), toolError("yelp", "bash", 1), toolError("yelp", "bash", 2), toolError("yelp", "read", 3), turn("yelp", 4, false))
	if x := e.Expression("yelp", at(4)); x.Pose != PoseFrustrated {
		t.Fatalf("a troubled review after its turn = %+v, want frustrated", x)
	}
}
