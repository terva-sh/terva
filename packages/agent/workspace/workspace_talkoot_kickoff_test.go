package workspace

import (
	"bytes"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
)

func kickoffCardOf(t *testing.T, w *Workspace) *ctrlproto.TalkootKickoff {
	t.Helper()
	r, err := w.TalkootInbox(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Cards {
		if c.Kind == ctrlproto.TalkootCardKickoff {
			return c.Kickoff
		}
	}
	return nil
}

func kickoffState(t *testing.T, w *Workspace) string {
	t.Helper()
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	k, _, err := talkoot.LoadKickoff(run.dir)
	if err != nil {
		t.Fatal(err)
	}
	return k.State
}

// A new talkoot waits for a person's kickoff. Its card lists the members in
// kickoff order, the coordinator last, with what each introduction is
// estimated to cost, and the create wakes nobody.
func TestCreatingATalkootLeavesAKickoffCard(t *testing.T) {
	cwd := talkootHome(t)
	var calls atomic.Int64
	w := openTalkootWorkspaceWith(t, cwd, func(rw http.ResponseWriter, r *http.Request) { calls.Add(1); okProvider(rw, r) })
	text := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n  - id: jev\n    role: specialist\n    model: claude-sonnet-4-5\n  - id: tess\n    role: specialist\n    driver: claude\n---\n"
	if _, err := w.CreateTalkoot(t.Context(), ctrlproto.TalkootCreateParams{ID: "crew", Text: text}); err != nil {
		t.Fatal(err)
	}
	k := kickoffCardOf(t, w)
	if k == nil || k.State != ctrlproto.TalkootKickoffWaiting {
		t.Fatalf("the kickoff card: %+v", k)
	}
	var order []string
	for _, m := range k.Members {
		order = append(order, m.Member)
	}
	if got := strings.Join(order, ","); got != "jev,tess,helm" {
		t.Fatalf("the kickoff order is %s, want jev,tess,helm", got)
	}
	jev, tess, helm := k.Members[0], k.Members[1], k.Members[2]
	want := float64(introInputTokens+len(text)/4)*3/1e6 + float64(introOutputTokens)*15/1e6
	if jev.EstimateUSD == nil || math.Abs(*jev.EstimateUSD-want) > 1e-9 || jev.Model != "claude-sonnet-4-5" {
		t.Errorf("jev's estimate: %+v, want %f", jev, want)
	}
	if tess.Plain == "" || tess.EstimateUSD != nil {
		t.Errorf("tess, a worker, should get a plain card at no cost: %+v", tess)
	}
	if helm.EstimateUSD != nil || helm.Plain != "" {
		t.Errorf("helm's model has no price, so it counts as an unpriced turn: %+v", helm)
	}
	if k.UnpricedTurns != 1 || math.Abs(k.EstimateUSD-want) > 1e-9 {
		t.Errorf("the total: %f and %d unpriced turns", k.EstimateUSD, k.UnpricedTurns)
	}
	if n := calls.Load(); n != 0 || roomEnvelopesOf(t, "crew") != 0 {
		t.Errorf("the create woke a member: %d requests", n)
	}
}

func roomEnvelopesOf(t *testing.T, id string) int {
	t.Helper()
	return len(roomLines(t, id, func(l talkoot.Line) bool { return l.Type == talkoot.LineEnvelope }))
}

// leadProvider has the coordinator do what its kickoff instruction asks: in
// the lead introduction's turn it asks the person a question. Every other
// request gets a short reply.
func leadProvider(rw http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	if strings.Contains(string(body), "who does what") && !strings.Contains(string(body), `"role":"tool"`) {
		toolCallProvider("ask_user_question", `{"question":"Which part first?","options":["the schema","the API"]}`)(rw, r)
		return
	}
	okProvider(rw, r)
}

// A kickoff introduces each member in roster order, and the coordinator after
// the others' turns end. Every introduction but the coordinator's is a note,
// and the coordinator's turn puts its question in the inbox. A team has one
// kickoff.
func TestAKickoffIntroducesTheCoordinatorLast(t *testing.T) {
	w := introCrewWith(t, leadProvider)
	plan, err := w.KickoffTalkoot(t.Context(), ctrlproto.TalkootKickoffParams{ID: "crew", By: "sothr"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.State != ctrlproto.TalkootKickoffRunning || plan.By != talkoot.HumanPrefix+"sothr" {
		t.Fatalf("the kickoff: %+v", plan)
	}
	if kickoffCardOf(t, w) != nil {
		t.Error("the kickoff card still waits after the kickoff")
	}
	waitTalkoot(t, "the kickoff to finish", func() bool { return kickoffState(t, w) == ctrlproto.TalkootKickoffDone })
	var who []string
	for _, l := range roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Type == talkoot.LineEnvelope && l.Envelope.Kind == talkoot.KindIntro
	}) {
		who = append(who, l.Envelope.To[0])
		body := l.Envelope.Body
		switch l.Envelope.To[0] {
		case "jev":
			if !strings.Contains(body, "as a note to helm. A note wakes nobody.") {
				t.Errorf("jev's instruction: %q", body)
			}
		case "helm":
			if !strings.Contains(body, "who does what") || !strings.Contains(body, "ask_user_question") {
				t.Errorf("helm's instruction: %q", body)
			}
		}
	}
	if got := strings.Join(who, ","); got != "jev,helm" {
		t.Fatalf("the introductions went to %s, want jev,helm", got)
	}
	// helm's introduction waited until jev's turn had ended.
	var jevTurn, helmIntro = -1, -1
	for i, l := range roomLines(t, "crew", func(talkoot.Line) bool { return true }) {
		switch {
		case l.Type == talkoot.LineTurn && l.Member == "jev" && jevTurn < 0:
			jevTurn = i
		case l.Type == talkoot.LineEnvelope && l.Envelope.Kind == talkoot.KindIntro && l.Envelope.To[0] == "helm":
			helmIntro = i
		}
	}
	if jevTurn < 0 || helmIntro < jevTurn {
		t.Errorf("helm's introduction (line %d) came before jev's turn ended (line %d)", helmIntro, jevTurn)
	}
	// The coordinator asked the person the first question.
	waitTalkoot(t, "helm's question", func() bool {
		for _, c := range inbox(t, w) {
			if c.Kind == ctrlproto.TalkootCardAsk && c.Member == "helm" && c.Ask != nil && c.Ask.Question == "Which part first?" {
				return true
			}
		}
		return false
	})
	if _, err := w.KickoffTalkoot(t.Context(), ctrlproto.TalkootKickoffParams{ID: "crew", By: "sothr"}); talkootCode(err) != ctrlproto.CodeConflict {
		t.Errorf("a second kickoff: %v, want conflict", err)
	}
}

// A skipped kickoff writes a plain card for each member and calls no model.
func TestASkippedKickoffCallsNoModel(t *testing.T) {
	cwd := talkootHome(t)
	var calls atomic.Int64
	w := openTalkootWorkspaceWith(t, cwd, func(rw http.ResponseWriter, r *http.Request) { calls.Add(1); okProvider(rw, r) })
	if _, err := w.CreateTalkoot(t.Context(), ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	plan, err := w.KickoffTalkoot(t.Context(), ctrlproto.TalkootKickoffParams{ID: "crew", By: "sothr", Skip: true})
	if err != nil || plan.State != ctrlproto.TalkootKickoffSkipped {
		t.Fatalf("the skipped kickoff: %+v, %v", plan, err)
	}
	// The reply reports what the skip did: a plain card for each member, at
	// no cost.
	if plan.EstimateUSD != 0 || plan.UnpricedTurns != 0 {
		t.Errorf("a skipped kickoff reports a cost: %+v", plan)
	}
	for _, m := range plan.Members {
		if m.Plain != skippedIntro || m.EstimateUSD != nil {
			t.Errorf("a skipped kickoff reports %+v", m)
		}
	}
	for _, m := range []string{"jev", "helm"} {
		if cards := cardsFor(t, m); len(cards) != 1 || !strings.Contains(cards[0].Text, "skipped the introductions") {
			t.Errorf("%s's card: %+v", m, cards)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 0 || roomEnvelopesOf(t, "crew") != 0 {
		t.Errorf("a skipped kickoff made %d requests", n)
	}
	if kickoffState(t, w) != ctrlproto.TalkootKickoffSkipped {
		t.Error("the kickoff is not recorded as skipped")
	}
	if _, err := w.KickoffTalkoot(t.Context(), ctrlproto.TalkootKickoffParams{ID: "crew", By: "sothr", Skip: true}); talkootCode(err) != ctrlproto.CodeConflict {
		t.Errorf("a second kickoff: %v, want conflict", err)
	}
}

// A team made before kickoffs shipped has no record, and still runs one.
func TestATeamWithNoKickoffRecordCanRunOne(t *testing.T) {
	w := introCrew(t)
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(run.dir, talkoot.KickoffFile)); err != nil {
		t.Fatal(err)
	}
	if kickoffCardOf(t, w) != nil {
		t.Error("a team with no record shows a kickoff card")
	}
	if _, err := w.KickoffTalkoot(t.Context(), ctrlproto.TalkootKickoffParams{ID: "crew", By: "not a name"}); err == nil || !strings.Contains(err.Error(), "must name a person") {
		t.Errorf("a kickoff in a name that is not a person's: %v", err)
	}
	if _, err := w.KickoffTalkoot(t.Context(), ctrlproto.TalkootKickoffParams{ID: "crew", By: "sothr", Skip: true}); err != nil {
		t.Fatalf("a kickoff for a team with no record: %v", err)
	}
	if kickoffState(t, w) != ctrlproto.TalkootKickoffSkipped {
		t.Error("the kickoff is not recorded")
	}
}

// A kickoff that a stop or a restart cut short stays running on disk. No
// goroutine runs it, so it may run again. One that still runs refuses.
func TestAnInterruptedKickoffCanRunAgain(t *testing.T) {
	w := introCrew(t)
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	if err := talkoot.SaveKickoff(run.dir, talkoot.Kickoff{State: ctrlproto.TalkootKickoffRunning, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	run.kicking.Store(true)
	if _, err := w.KickoffTalkoot(t.Context(), ctrlproto.TalkootKickoffParams{ID: "crew", By: "sothr", Skip: true}); talkootCode(err) != ctrlproto.CodeConflict {
		t.Errorf("a kickoff while one runs: %v, want conflict", err)
	}
	run.kicking.Store(false)
	if _, err := w.KickoffTalkoot(t.Context(), ctrlproto.TalkootKickoffParams{ID: "crew", By: "sothr", Skip: true}); err != nil {
		t.Errorf("a kickoff after one was cut short: %v", err)
	}
}

// runningKickoff records crew's kickoff as running in this process, as
// talkootKickoff does before it starts runKickoff.
func runningKickoff(t *testing.T, w *Workspace) *talkootRun {
	t.Helper()
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	if err := talkoot.SaveKickoff(run.dir, talkoot.Kickoff{State: ctrlproto.TalkootKickoffRunning, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	run.kicking.Store(true)
	return run
}

// A kickoff with an introduction that failed is not done. It stays running on
// disk with no goroutine to run it, so a person may run it again.
func TestAKickoffWithAFailedIntroductionCanRunAgain(t *testing.T) {
	w := introCrew(t)
	run := runningKickoff(t, w)
	// The router refuses an introduction in a name that is not a person's,
	// and talkootKickoff checked the name before this.
	w.runKickoff(run, "not a name", kickoffOrder(*run.roster.Load()))
	if got := kickoffState(t, w); got != ctrlproto.TalkootKickoffRunning {
		t.Errorf("after a failed introduction the kickoff is %s, want running", got)
	}
	if run.kicking.Load() {
		t.Error("the kickoff still counts as running here after it ended")
	}
	if _, err := w.KickoffTalkoot(t.Context(), ctrlproto.TalkootKickoffParams{ID: "crew", By: "sothr", Skip: true}); err != nil {
		t.Errorf("a retry after a failed introduction: %v", err)
	}
}

// A member a roster update removed while the kickoff ran is owed no
// introduction, and its absence fails nothing.
func TestAKickoffSkipsAMemberThatLeft(t *testing.T) {
	w := introCrew(t)
	run := runningKickoff(t, w)
	order := append([]talkoot.Member{{ID: "ghost", Role: talkoot.RoleSpecialist, Driver: talkoot.DriverNative}}, kickoffOrder(*run.roster.Load())...)
	w.runKickoff(run, "sothr", order)
	if got := kickoffState(t, w); got != ctrlproto.TalkootKickoffDone {
		t.Errorf("a kickoff whose member left is %s, want done", got)
	}
	if n := len(introsTo(t, "ghost")) + len(cardsFor(t, "ghost")); n != 0 {
		t.Errorf("a member that left got %d introductions", n)
	}
}

// A member that joins while the kickoff runs is one the coordinator waits
// for, as it waits for the kickoff's own members.
func TestTheCoordinatorWaitsForAMemberThatJoinedTheKickoff(t *testing.T) {
	w := introCrewWith(t, func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if strings.Contains(string(body), "You are atlas") {
			time.Sleep(300 * time.Millisecond)
		}
		okProvider(rw, r)
	})
	run := runningKickoff(t, w)
	add := strings.TrimSuffix(string(crewText(w.cwd)), "---\n") + "  - id: atlas\n    role: specialist\n---\n"
	if _, err := w.talkootUpdate(t.Context(), "crew", "sothr", []byte(add)); err != nil {
		t.Fatal(err)
	}
	// Only the coordinator is left for this kickoff to introduce.
	var lead []talkoot.Member
	for _, m := range kickoffOrder(*run.roster.Load()) {
		if m.Role == talkoot.RoleCoordinator {
			lead = append(lead, m)
		}
	}
	w.runKickoff(run, "sothr", lead)
	atlasTurn, helmIntro := -1, -1
	for i, l := range roomLines(t, "crew", func(talkoot.Line) bool { return true }) {
		switch {
		case l.Type == talkoot.LineTurn && l.Member == "atlas" && atlasTurn < 0:
			atlasTurn = i
		case l.Type == talkoot.LineEnvelope && l.Envelope.Kind == talkoot.KindIntro && l.Envelope.To[0] == "helm":
			helmIntro = i
		}
	}
	if atlasTurn < 0 || helmIntro < atlasTurn {
		t.Errorf("helm's introduction (line %d) came before atlas's turn ended (line %d)", helmIntro, atlasTurn)
	}
}
