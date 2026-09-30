package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/mcpbridge"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/permission"
)

// toolCallProvider answers a request that offers tools, and carries no tool
// result yet, with one call to name. Every other request gets a short reply.
// A turn therefore makes the call once, and ends after its result.
func toolCallProvider(name, args string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(string(body), `"tools"`) && !strings.Contains(string(body), `"role":"tool"`) {
			fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%s}}]},\"finish_reason\":null}]}\n\n", name, strconv.Quote(args))
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

// startInboxCrew makes the crew talkoot over provider, with helm in posture,
// watches its room, and wakes helm with a post.
func startInboxCrew(t *testing.T, provider http.HandlerFunc, posture string) (*Workspace, <-chan ctrlproto.Event) {
	t.Helper()
	cwd := talkootHome(t)
	w := openTalkootWorkspaceWith(t, cwd, provider)
	ctx := t.Context()
	text := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n    posture: " + posture + "\n  - id: jev\n    role: specialist\n---\n"
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: text}); err != nil {
		t.Fatal(err)
	}
	room, err := w.Subscribe(ctx, ctrlproto.TalkootAddr("crew"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.PostTalkoot(ctx, ctrlproto.TalkootPostParams{ID: "crew", By: "sothr", To: []string{"helm"}, Body: "start"}); err != nil {
		t.Fatal(err)
	}
	return w, room
}

// inbox returns the crew's cards without the kickoff card, which every new
// talkoot shows, and which workspace_talkoot_kickoff_test.go covers.
func inbox(t *testing.T, w *Workspace) []ctrlproto.TalkootCard {
	t.Helper()
	r, err := w.TalkootInbox(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	var out []ctrlproto.TalkootCard
	for _, c := range r.Cards {
		if c.Kind != ctrlproto.TalkootCardKickoff {
			out = append(out, c)
		}
	}
	return out
}

// A member's question shows in its own session and in the talkoot's inbox. A
// person's answer on the member's session resolves it in both places, and the
// member's turn goes on with the answer.
func TestAMemberQuestionReachesTheInbox(t *testing.T) {
	w, room := startInboxCrew(t, toolCallProvider("ask_user_question", `{"question":"Which port?","options":["8080","9090"]}`), "auto-edit")
	ctx := t.Context()

	ev := nextEvent(t, room, ctrlproto.EventTalkootInbox)
	card := *ev.Talkoot.Card
	if card.Kind != ctrlproto.TalkootCardAsk || card.Member != "helm" || card.Ask == nil || card.Ask.Question != "Which port?" {
		t.Fatalf("the inbox event carried %+v", card)
	}
	if want := memberView(t, w, "crew", "helm").Session; card.Session != want || card.ID != card.Ask.AskID {
		t.Fatalf("the card names session %q and id %q, want helm's session %q and the ask id %q", card.Session, card.ID, want, card.Ask.AskID)
	}
	if got := inbox(t, w); len(got) != 1 || got[0].ID != card.ID || got[0].Ask == nil || got[0].At.IsZero() {
		t.Fatalf("talkoot.inbox = %+v, want the one card", got)
	}

	// The member's own view holds the same card.
	sess, err := w.Subscribe(ctx, card.Session)
	if err != nil {
		t.Fatal(err)
	}
	snap := nextEvent(t, sess, ctrlproto.EventSnapshot)
	if snap.Snapshot == nil || len(snap.Snapshot.Asks) != 1 || snap.Snapshot.Asks[0].AskID != card.ID {
		t.Fatalf("helm's snapshot holds asks %+v, want the card", snap.Snapshot)
	}

	if err := w.Answer(ctx, card.Session, card.ID, []core.UserAnswer{{Answer: "8080"}}); err != nil {
		t.Fatal(err)
	}
	if got := nextEvent(t, sess, ctrlproto.EventAskResolved); got.Resolved == nil || got.Resolved.AskID != card.ID {
		t.Fatalf("helm's session resolved %+v", got.Resolved)
	}
	done := nextEvent(t, room, ctrlproto.EventTalkootInboxResolved).Talkoot.Card
	if done.ID != card.ID || done.Session != card.Session || done.Member != "helm" || done.Ask != nil {
		t.Fatalf("the resolved event carried %+v", done)
	}
	if got := inbox(t, w); len(got) != 0 {
		t.Fatalf("talkoot.inbox after the answer = %+v", got)
	}
	waitTalkoot(t, "helm's turn to end", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })
}

// A tool call that needs approval makes a permission card, and a person's
// decision on the member's session resolves it.
func TestAMemberApprovalReachesTheInbox(t *testing.T) {
	w, room := startInboxCrew(t, toolCallProvider("bash", `{"command":"echo hi"}`), "ask")
	ctx := t.Context()

	card := *nextEvent(t, room, ctrlproto.EventTalkootInbox).Talkoot.Card
	if card.Kind != ctrlproto.TalkootCardPermission || card.Member != "helm" || card.Permission == nil || card.Permission.Tool != "bash" || card.ID != card.Permission.CallID {
		t.Fatalf("the inbox event carried %+v", card)
	}
	if got := inbox(t, w); len(got) != 1 || got[0].ID != card.ID {
		t.Fatalf("talkoot.inbox = %+v, want the one card", got)
	}
	if err := w.Approve(ctx, card.Session, card.ID, permission.ConfirmDecision{Allow: false, Reason: "not now"}); err != nil {
		t.Fatal(err)
	}
	if done := nextEvent(t, room, ctrlproto.EventTalkootInboxResolved).Talkoot.Card; done.ID != card.ID || done.Permission != nil {
		t.Fatalf("the resolved event carried %+v", done)
	}
	if got := inbox(t, w); len(got) != 0 {
		t.Fatalf("talkoot.inbox after the decision = %+v", got)
	}
}

// Only a seated session's cards reach a talkoot. A session with no seat sends
// no inbox event and shows in no inbox, and the inbox lists the oldest card
// first.
func TestOnlyASeatedSessionReachesTheInbox(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	// talkootEmit calls each watcher on the caller's goroutine, so kinds is
	// current when each open or close returns.
	var kinds []string
	stop := w.talkootWatch("crew", func(ev talkootEvent) {
		if ev.Kind == "inbox" {
			kinds = append(kinds, ev.Wire.Type)
		}
	})
	defer stop()
	if _, err := w.PostTalkoot(ctx, ctrlproto.TalkootPostParams{ID: "crew", By: "sothr", To: []string{"helm"}, Body: "start"}); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's session", func() bool { return memberView(t, w, "crew", "helm").Session != "" })
	helm := w.existing(memberView(t, w, "crew", "helm").Session)
	if helm == nil {
		t.Fatal("helm's session is not live")
	}

	loner := newTestSession()
	loner.id, loner.ws = "loner", w
	w.mu.Lock()
	w.sessions[loner.id] = loner
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.sessions, loner.id)
		w.mu.Unlock()
	}()

	loner.openPermission(ctrlproto.PermissionRequest{CallID: "l1", Tool: "bash"})
	if len(kinds) != 0 {
		t.Fatalf("a session with no seat sent %v", kinds)
	}
	// The positive control: a seated session's card does reach the talkoot.
	// The older card has the larger id, so only an order by time passes.
	helm.openPermission(ctrlproto.PermissionRequest{CallID: "h2", Tool: "bash"})
	time.Sleep(time.Millisecond) // two cards never share a time
	helm.openAsk(ctrlproto.AskRequest{AskID: "h1", Question: "Which port?"})
	if len(kinds) != 2 {
		t.Fatalf("helm's two cards sent %v", kinds)
	}
	got := inbox(t, w)
	if len(got) != 2 || got[0].ID != "h2" || got[1].ID != "h1" {
		t.Fatalf("talkoot.inbox = %+v, want h2, the older card, then h1, and not the loner's card", got)
	}

	loner.closePermission("l1", talkoot.OutcomeApproved)
	helm.closePermission("h2", talkoot.OutcomeApproved)
	helm.closeAsk("h1", talkoot.OutcomeAnswered)
	if len(kinds) != 4 || kinds[2] != ctrlproto.EventTalkootInboxResolved || kinds[3] != ctrlproto.EventTalkootInboxResolved {
		t.Fatalf("the closes sent %v", kinds)
	}
	if got := inbox(t, w); len(got) != 0 {
		t.Fatalf("talkoot.inbox after the closes = %+v", got)
	}
}

func TestTheInboxOfAnUnknownTalkootIsNotFound(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	_, err := w.TalkootInbox(context.Background(), ctrlproto.TalkootRef{ID: "nobody"})
	if code := talkootCode(err); code != ctrlproto.CodeNotFound {
		t.Fatalf("talkoot.inbox of an unknown talkoot answered %v (%q)", err, code)
	}
}

// A member with a question or an approval open in the inbox is waiting, until
// the last of its cards closes, and a status event says so.
func TestAnOpenCardMakesItsMemberWait(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.PostTalkoot(ctx, ctrlproto.TalkootPostParams{ID: "crew", By: "sothr", To: []string{"helm"}, Body: "start"}); err != nil {
		t.Fatal(err)
	}
	// The test provider fails helm's turn, and a pause would hide the wait.
	resumeFailed(t, w, "crew", "helm")
	helm := w.existing(memberView(t, w, "crew", "helm").Session)
	if helm == nil {
		t.Fatal("helm's session is not live")
	}
	var mu sync.Mutex
	var seen []string
	stop := w.talkootWatch("crew", func(ev talkootEvent) {
		if ev.Kind != "status" {
			return
		}
		for _, s := range ev.Status {
			if s.Member == "helm" {
				mu.Lock()
				seen = append(seen, wireTalkootStatus(s).Presence)
				mu.Unlock()
			}
		}
	})
	defer stop()
	presence := func() string { return memberView(t, w, "crew", "helm").Status.Presence() }
	if p := presence(); p != "idle" {
		t.Fatalf("before any card helm is %s", p)
	}
	// The question opens twice under one id, and one close ends it.
	helm.openAsk(ctrlproto.AskRequest{AskID: "h1", Question: "Which port?"})
	helm.openAsk(ctrlproto.AskRequest{AskID: "h1", Question: "Which port?"})
	helm.openPermission(ctrlproto.PermissionRequest{CallID: "h2", Tool: "bash"})
	waitTalkoot(t, "helm to wait", func() bool { return presence() == "waiting" })
	helm.closeAsk("h1", talkoot.OutcomeAnswered)
	if p := presence(); p != "waiting" {
		t.Errorf("with the approval still open helm is %s", p)
	}
	helm.closePermission("h2", talkoot.OutcomeApproved)
	waitTalkoot(t, "helm to stop waiting", func() bool { return presence() == "idle" })
	waitTalkoot(t, "the status events", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Contains(seen, "waiting") && seen[len(seen)-1] == "idle"
	})
}

// A worker member's question opens on the talkoot's carrier, and it makes the
// member wait as a native member's question does, until a person answers.
func TestAWorkerQuestionMakesItsMemberWait(t *testing.T) {
	w, _ := seatedWorker(t)
	waiting := func() bool { return memberView(t, w, "crew", "jev").Status.Waiting }
	if waiting() {
		t.Fatal("jev waits before it asks")
	}
	team := w.workerTeam(crewWorker("agent-1"))
	got := make(chan mcpbridge.TeamReply, 1)
	go func() {
		got <- team(t.Context(), "ask_user_question", json.RawMessage(`{"question":"Ship the schema today?","options":["Ship it","Hold"]}`))
	}()
	waitTalkoot(t, "jev to wait", waiting)
	if p := wireTalkootStatus(memberView(t, w, "crew", "jev").Status).Presence; p != "waiting" {
		t.Errorf("jev's presence is %q while its question is open", p)
	}
	cards := askCards(t, w)
	if len(cards) != 1 {
		t.Fatalf("%d question cards, want 1", len(cards))
	}
	if err := w.Answer(t.Context(), cards[0].Session, cards[0].ID, []core.UserAnswer{{Answer: "Ship it"}}); err != nil {
		t.Fatal(err)
	}
	teamReply(t, got)
	waitTalkoot(t, "jev to stop waiting", func() bool { return !waiting() })
}
