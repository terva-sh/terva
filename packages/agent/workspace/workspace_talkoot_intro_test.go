package workspace

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
)

// introCrew is the crew talkoot over a provider that answers every turn well.
func introCrew(t *testing.T) *Workspace {
	t.Helper()
	return introCrewWith(t, okProvider)
}

// introCrewWith is the crew talkoot over provider, its kickoff waiting.
func introCrewWith(t *testing.T, provider http.HandlerFunc) *Workspace {
	t.Helper()
	cwd := talkootHome(t)
	w := openTalkootWorkspaceWith(t, cwd, provider)
	if _, err := w.CreateTalkoot(t.Context(), ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	return w
}

// startedCrew is introCrew after its kickoff, so a member that joins is a
// member that joins a running team.
func startedCrew(t *testing.T, provider http.HandlerFunc) *Workspace {
	t.Helper()
	cwd := talkootHome(t)
	w := openTalkootWorkspaceWith(t, cwd, provider)
	if _, err := w.CreateTalkoot(t.Context(), ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	if err := talkoot.SaveKickoff(run.dir, talkoot.Kickoff{State: ctrlproto.TalkootKickoffDone, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return w
}

// introProvider sends the two posts a joining member's instruction asks for,
// in the introduction turn alone: a note to jev and a message to helm. Every
// other request gets a short reply.
func introProvider(rw http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rw.Header().Set("Content-Type", "text/event-stream")
	if strings.Contains(string(body), "Introduce yourself to the talkoot") && !strings.Contains(string(body), `"role":"tool"`) {
		for i, args := range []string{
			`{"to":["jev"],"kind":"note","body":"atlas plans."}`,
			`{"to":["helm"],"kind":"message","body":"atlas plans; route planning to me."}`,
		} {
			fmt.Fprintf(rw, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":%d,\"id\":\"call_%d\",\"type\":\"function\",\"function\":{\"name\":\"talkoot_send\",\"arguments\":%s}}]},\"finish_reason\":null}]}\n\n", i, i, strconv.Quote(args))
		}
		fmt.Fprint(rw, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		fmt.Fprint(rw, "data: [DONE]\n\n")
		return
	}
	okProvider(rw, r)
}

func introsTo(t *testing.T, member string) []talkoot.Line {
	t.Helper()
	return roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Type == talkoot.LineEnvelope && l.Envelope.Kind == talkoot.KindIntro && len(l.Envelope.To) == 1 && l.Envelope.To[0] == member
	})
}

func cardsFor(t *testing.T, member string) []talkoot.Line {
	t.Helper()
	return roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineIntro && l.Member == member })
}

// An approved add gives the new member one introduction turn, in the name of
// the person who approved it. The turn counts against the member's caps.
func TestAnApprovedAddIntroducesTheNewMember(t *testing.T) {
	w := startedCrew(t, introProvider)
	add := []talkoot.Op{{Op: talkoot.OpAdd, Member: "atlas", Set: map[string]any{"role": "specialist"}}}
	p, err := w.talkootPropose("crew", talkoot.HumanPrefix+"sothr", add, "", "a planner", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootDecide(t.Context(), "crew", "kaisa", p.ID, ctrlproto.TalkootDecisionApprove, nil, ""); err != nil {
		t.Fatal(err)
	}
	intros := introsTo(t, "atlas")
	if len(intros) != 1 {
		t.Fatalf("the room holds %d introductions for atlas, want 1", len(intros))
	}
	e := intros[0].Envelope
	if e.From != talkoot.HumanPrefix+"kaisa" || !strings.Contains(e.Body, "as a message to helm, the coordinator") {
		t.Fatalf("the introduction: from %q, body %q", e.From, e.Body)
	}
	waitTalkoot(t, "atlas's introduction turn", func() bool { return memberView(t, w, "crew", "atlas").Status.Turns >= 1 })
	if n := len(introsTo(t, "helm")) + len(introsTo(t, "jev")); n != 0 {
		t.Errorf("the add introduced %d members that were already on the roster", n)
	}
	// The member's replies ride the introduction's chain: a note to the room
	// and a message to the coordinator.
	sent := roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Type == talkoot.LineEnvelope && l.Envelope.From == "atlas" && l.Envelope.Chain.Root == e.ID
	})
	var got []string
	for _, l := range sent {
		got = append(got, string(l.Envelope.Kind)+" to "+strings.Join(l.Envelope.To, ","))
	}
	if strings.Join(got, "; ") != "note to jev; message to helm" {
		t.Errorf("atlas sent %q, want a note to jev and a message to helm", got)
	}
}

// While the team's kickoff waits, a member that joins is introduced by the
// kickoff, and not twice.
func TestAJoinWaitsForAWaitingKickoff(t *testing.T) {
	w := introCrew(t)
	add := strings.TrimSuffix(string(crewText(w.cwd)), "---\n") + "  - id: atlas\n    role: specialist\n---\n"
	if _, err := w.talkootUpdate(t.Context(), "crew", "sothr", []byte(add)); err != nil {
		t.Fatal(err)
	}
	if n := len(introsTo(t, "atlas")) + len(cardsFor(t, "atlas")); n != 0 {
		t.Errorf("a member that joined before the kickoff was introduced %d times", n)
	}
	if k := kickoffCardOf(t, w); k == nil || len(k.Members) != 3 {
		t.Errorf("the kickoff card does not list the new member: %+v", k)
	}
}

// While a kickoff runs, a member that joins introduces itself with notes, as
// the kickoff's members do. A message would wake the coordinator before its
// own introduction.
func TestAJoinDuringAKickoffSendsNotes(t *testing.T) {
	w := startedCrew(t, okProvider)
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	run.kicking.Store(true)
	defer run.kicking.Store(false)
	add := strings.TrimSuffix(string(crewText(w.cwd)), "---\n") + "  - id: atlas\n    role: specialist\n---\n"
	if _, err := w.talkootUpdate(t.Context(), "crew", "sothr", []byte(add)); err != nil {
		t.Fatal(err)
	}
	intros := introsTo(t, "atlas")
	if len(intros) != 1 {
		t.Fatalf("the room holds %d introductions for atlas, want 1", len(intros))
	}
	if body := intros[0].Envelope.Body; !strings.Contains(body, "A note wakes nobody.") || strings.Contains(body, "as a message to helm") {
		t.Errorf("a member that joined during the kickoff was told %q", body)
	}
}

// A kickoff record that does not read leaves the kickoff's state unknown, so
// a member that joins gets a plain card and no turn.
func TestAJoinWithAnUnreadableKickoffGetsAPlainCard(t *testing.T) {
	w := startedCrew(t, okProvider)
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.dir, talkoot.KickoffFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	add := strings.TrimSuffix(string(crewText(w.cwd)), "---\n") + "  - id: atlas\n    role: specialist\n---\n"
	if _, err := w.talkootUpdate(t.Context(), "crew", "sothr", []byte(add)); err != nil {
		t.Fatal(err)
	}
	if cards := cardsFor(t, "atlas"); len(cards) != 1 || !strings.Contains(cards[0].Text, "kickoff record did not read") {
		t.Errorf("atlas's card: %+v", cards)
	}
	if n := len(introsTo(t, "atlas")); n != 0 {
		t.Errorf("a join with an unreadable kickoff got %d introduction turns", n)
	}
}

// A worker member without the user's external_workers gate, and a member the
// team's pause holds, cannot take a turn. Each gets a plain card from its entry, and no turn. A
// client receives a card as a talkoot_intro event.
func TestAMemberThatCannotTakeATurnGetsAPlainCard(t *testing.T) {
	w := startedCrew(t, okProvider)
	ctx := t.Context()
	room, err := w.Subscribe(ctx, ctrlproto.TalkootAddr("crew"))
	if err != nil {
		t.Fatal(err)
	}
	cwd := w.cwd
	withTess := string(crewText(cwd))
	withTess = strings.Replace(withTess, "---\n", "", 1)
	withTess = "---\n" + strings.TrimSuffix(withTess, "---\n") + "  - id: tess\n    role: specialist\n    driver: claude\n---\n"
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", []byte(withTess)); err != nil {
		t.Fatal(err)
	}
	if cards := cardsFor(t, "tess"); len(cards) != 1 || !strings.Contains(cards[0].Text, "external_workers") || !strings.Contains(cards[0].Text, "tess, the specialist") {
		t.Fatalf("tess's card: %+v", cards)
	}
	if len(introsTo(t, "tess")) != 0 {
		t.Error("a worker without the gate got an introduction turn")
	}
	if l := nextEvent(t, room, ctrlproto.EventTalkootIntro).Talkoot.Line; l == nil || l.Type != talkoot.LineIntro || l.Member != "tess" || !strings.Contains(l.Text, "tess, the specialist") {
		t.Errorf("the talkoot_intro event carried %+v", l)
	}

	if err := w.talkootPause(ctx, "crew", "sothr", "", "", "lunch"); err != nil {
		t.Fatal(err)
	}
	withRook := strings.TrimSuffix(withTess, "---\n") + "  - id: rook\n    role: specialist\n---\n"
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", []byte(withRook)); err != nil {
		t.Fatal(err)
	}
	if cards := cardsFor(t, "rook"); len(cards) != 1 || !strings.Contains(cards[0].Text, "the member is paused") {
		t.Fatalf("rook's card: %+v", cards)
	}
	if len(introsTo(t, "rook")) != 0 {
		t.Error("a paused member got an introduction turn")
	}
}

// A member that spent its day's turns cannot take an introduction turn. Spend
// is kept by member id for the day, so a member added back after it spent its
// cap gets a plain card and no model call.
func TestAMemberWithItsCapSpentGetsAPlainCard(t *testing.T) {
	w := startedCrew(t, okProvider)
	ctx := t.Context()
	capped := strings.Replace(string(crewText(w.cwd)), "  - id: jev\n    role: specialist\n", "  - id: jev\n    role: specialist\n    turns_per_day: 1\n", 1)
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", []byte(capped)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(ctx, "crew", "sothr", []string{"jev"}, "Check the schema.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "jev to spend its turn", func() bool { return memberView(t, w, "crew", "jev").Status.Paused != "" })
	without := strings.Replace(capped, "  - id: jev\n    role: specialist\n    turns_per_day: 1\n", "", 1)
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", []byte(without)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootUpdate(ctx, "crew", "sothr", []byte(capped)); err != nil {
		t.Fatal(err)
	}
	if cards := cardsFor(t, "jev"); len(cards) != 1 || !strings.Contains(cards[0].Text, "the member is paused") {
		t.Fatalf("jev's card: %+v", cards)
	}
	if n := len(introsTo(t, "jev")); n != 0 {
		t.Errorf("a member with its cap spent got %d introduction turns", n)
	}
}
