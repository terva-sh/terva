package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/mcpbridge"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/permission"
)

// bridgeCall runs one seat-tool call as worker agentID of the crew talkoot.
func bridgeCall(t *testing.T, w *Workspace, agentID, tool, input string) mcpbridge.TeamReply {
	t.Helper()
	team := w.workerTeam(crewWorker(agentID))
	if team == nil {
		t.Fatal("a talkoot worker has no team")
	}
	return team(t.Context(), tool, json.RawMessage(input))
}

// A worker's send goes out as the member that holds its seat. A sender the
// model names is ignored, because the call names no member.
func TestAWorkerSendsAsItsMember(t *testing.T) {
	w, _ := seatedWorker(t)
	settleReads(t, w)
	r := bridgeCall(t, w, "agent-1", "talkoot_send", `{"from":"helm","to":["helm"],"kind":"message","body":"The schema looks sound."}`)
	if r.IsError || !strings.HasPrefix(r.Text, "Sent message ") || !strings.HasSuffix(r.Text, " to helm.") {
		t.Fatalf("reply = %+v, want the native tool's text", r)
	}
	sent := roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Envelope != nil && l.Envelope.Kind == talkoot.KindMessage && l.Envelope.Body == "The schema looks sound."
	})
	if len(sent) != 1 || sent[0].Envelope.From != "jev" || sent[0].Envelope.To[0] != "helm" {
		t.Fatalf("envelopes = %+v, want one from jev to helm", sent)
	}
}

// The roster marks the worker's own member.
func TestAWorkerReadsTheRosterAsItsMember(t *testing.T) {
	w, _ := seatedWorker(t)
	r := bridgeCall(t, w, "agent-1", "talkoot_roster", `{}`)
	if r.IsError || !strings.Contains(r.Text, "- jev (you)") || !strings.Contains(r.Text, "- helm") {
		t.Fatalf("reply = %+v, want the roster with jev as you", r)
	}
}

// A handoff meets the native tool's checks: it needs a reference.
func TestAWorkerHandoffNeedsAReference(t *testing.T) {
	w, _ := seatedWorker(t)
	r := bridgeCall(t, w, "agent-1", "talkoot_handoff", `{"to":["helm"],"body":"Take it."}`)
	if !r.IsError || !strings.Contains(r.Text, "at least one reference") {
		t.Fatalf("reply = %+v, want the native refusal", r)
	}
}

// A worker that holds no seat speaks for nobody, and a worker outside a
// talkoot has no team at all.
func TestAnUnseatedWorkerCallIsRefused(t *testing.T) {
	w, _ := seatedWorker(t)
	settleReads(t, w)
	before := roomLines(t, "crew", func(l talkoot.Line) bool { return l.Envelope != nil })
	r := bridgeCall(t, w, "agent-9", "talkoot_send", `{"to":["helm"],"kind":"message","body":"Hi."}`)
	if !r.IsError || r.Text != errWorkerUnseated.Error() {
		t.Fatalf("reply = %+v, want the unseated refusal", r)
	}
	if after := roomLines(t, "crew", func(l talkoot.Line) bool { return l.Envelope != nil }); len(after) != len(before) {
		t.Fatalf("an unseated call reached the room: %d envelopes, want %d", len(after), len(before))
	}
	if w.workerTeam(&swarm.Agent{ID: "agent-2", SessionID: "sess-1"}) != nil {
		t.Error("a worker outside a talkoot has a team")
	}
}

// The seat can move between the lookup and the router. A send checks the
// seat again inside the router call.
func TestAReseatedWorkerSendIsRefused(t *testing.T) {
	w, _ := seatedWorker(t)
	settleReads(t, w)
	run, member, ok := w.talkootWorker(t.Context(), "crew", "agent-1", nil)
	if !ok {
		t.Fatal("agent-1 holds no seat")
	}
	seat := workerSeat{ctx: t.Context(), w: w, run: run, member: member, agentID: "agent-1"}
	w.talkoot.mu.Lock()
	run.seats["jev"] = "agent-2"
	w.talkoot.mu.Unlock()
	if _, err := seat.Send(talkoot.Outgoing{To: []string{"helm"}, Kind: talkoot.KindMessage, Body: "Hi."}); err != errWorkerUnseated {
		t.Fatalf("send after the seat moved: %v, want the unseated refusal", err)
	}
}

// The bridge serves the team verbs alone. The orchestrator refuses any other
// seat tool, whatever a bridge process sends.
func TestTheBridgeServesTeamVerbsOnly(t *testing.T) {
	w, _ := seatedWorker(t)
	for _, tool := range []string{"talkoot_note_write", "talkoot_propose", "bash"} {
		r := bridgeCall(t, w, "agent-1", tool, `{}`)
		if want := fmt.Sprintf("the talkoot bridge does not serve %q", tool); !r.IsError || r.Text != want {
			t.Errorf("%s: reply = %+v, want a refusal", tool, r)
		}
	}
}

// sentBy returns the room's envelopes from member with body.
func sentBy(t *testing.T, member, body string) []talkoot.Line {
	t.Helper()
	return roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Envelope != nil && l.Envelope.From == member && l.Envelope.Body == body
	})
}

// 🔑 The daemon is the seat tools' one gate, and it holds the person's rules.
// A deny rule for talkoot_send stops a worker member as it stops a native one.
func TestAWorkerSendMeetsThePersonsRules(t *testing.T) {
	w, _ := seatedWorker(t)
	settleReads(t, w)
	cfg := `{"talkoot_enabled": true, "external_workers_enabled": true, "permissions": [{"tool": "talkoot_send", "decision": "deny", "reason": "no sends today"}]}`
	if err := os.WriteFile(filepath.Join(os.Getenv("TERVA_HOME"), "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	r := bridgeCall(t, w, "agent-1", "talkoot_send", `{"to":["helm"],"kind":"message","body":"Done."}`)
	if !r.IsError || !strings.Contains(r.Text, "no sends today") {
		t.Fatalf("reply = %+v, want the rule's refusal", r)
	}
	if got := sentBy(t, "jev", "Done."); len(got) != 0 {
		t.Fatalf("a denied send reached the room: %+v", got)
	}
}

// A worker member in the ask posture waits for a person, as a native one
// does. The card opens in the talkoot's inbox under the member, and the
// send goes once the person approves it.
func TestAnAskPostureWorkerSendWaitsForThePerson(t *testing.T) {
	w, _ := seatedWorkerWith(t, "    posture: ask\n")
	settleReads(t, w)
	team := w.workerTeam(crewWorker("agent-1"))
	got := make(chan mcpbridge.TeamReply, 1)
	go func() {
		got <- team(t.Context(), "talkoot_send", json.RawMessage(`{"to":["helm"],"kind":"message","body":"Ready for review."}`))
	}()
	var card ctrlproto.TalkootCard
	waitTalkoot(t, "the card", func() bool {
		cards := permissionCards(t, w)
		if len(cards) == 1 {
			card = cards[0]
		}
		return len(cards) == 1
	})
	if card.Member != "jev" || card.Permission == nil || card.Permission.Tool != "talkoot_send" || !strings.Contains(card.Permission.Preview, "Ready for review.") {
		t.Fatalf("card = %+v", card)
	}
	if n := len(sentBy(t, "jev", "Ready for review.")); n != 0 {
		t.Fatal("the send went before the person answered")
	}
	if err := w.Approve(t.Context(), card.Session, card.ID, permission.ConfirmDecision{Allow: true}); err != nil {
		t.Fatal(err)
	}
	if r := <-got; r.IsError {
		t.Fatalf("reply = %+v, want the send", r)
	}
	if n := len(sentBy(t, "jev", "Ready for review.")); n != 1 {
		t.Fatalf("the room holds %d approved sends, want 1", n)
	}
}

// A roster call checks the seat inside the router call too, so a worker
// that lost its seat reads nobody's roster.
func TestAReseatedWorkerRosterIsRefused(t *testing.T) {
	w, _ := seatedWorker(t)
	run, member, ok := w.talkootWorker(t.Context(), "crew", "agent-1", nil)
	if !ok {
		t.Fatal("agent-1 holds no seat")
	}
	seat := workerSeat{ctx: t.Context(), w: w, run: run, member: member, agentID: "agent-1"}
	w.talkoot.mu.Lock()
	run.seats["jev"] = "agent-2"
	w.talkoot.mu.Unlock()
	if _, err := seat.Roster(); err != errWorkerUnseated {
		t.Fatalf("roster after the seat moved: %v, want the unseated refusal", err)
	}
}

// The daemon's pre-tool-use hooks run for a bridge call as they run for a
// native member's, and a hook's deny stops the send.
func TestAWorkerSendMeetsThePersonsHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the hook is a shell script")
	}
	cfg := `{"talkoot_enabled": true, "external_workers_enabled": true, "hooks": {"pre_tool_use": [{"command": "sh", "args": ["-c", "cat >/dev/null; echo held by the hook >&2; exit 2"], "tools": "talkoot_send"}]}}`
	w, _ := seatedWorkerConfig(t, "", cfg)
	settleReads(t, w)
	r := bridgeCall(t, w, "agent-1", "talkoot_send", `{"to":["helm"],"kind":"message","body":"Hooked."}`)
	if !r.IsError || !strings.Contains(r.Text, "held by the hook") {
		t.Fatalf("reply = %+v, want the hook's refusal", r)
	}
	if got := sentBy(t, "jev", "Hooked."); len(got) != 0 {
		t.Fatalf("a hooked send reached the room: %+v", got)
	}
	// The roster matches no hook and still runs.
	if r := bridgeCall(t, w, "agent-1", "talkoot_roster", `{}`); r.IsError {
		t.Fatalf("roster: %+v", r)
	}
}

// 🚨 A config that cannot be read would take the person's rules with it, so
// the bridge refuses the call rather than run it on the posture alone.
func TestAnUnreadableConfigRefusesABridgeCall(t *testing.T) {
	w, _ := seatedWorker(t)
	settleReads(t, w)
	if err := os.WriteFile(filepath.Join(os.Getenv("TERVA_HOME"), "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := bridgeCall(t, w, "agent-1", "talkoot_send", `{"to":["helm"],"kind":"message","body":"Unchecked."}`)
	if !r.IsError || !strings.Contains(r.Text, "cannot check this call") {
		t.Fatalf("reply = %+v, want the fail-closed refusal", r)
	}
	if got := sentBy(t, "jev", "Unchecked."); len(got) != 0 {
		t.Fatalf("an unchecked send reached the room: %+v", got)
	}
}

// teamReply waits for a bridge call's reply.
func teamReply(t *testing.T, got <-chan mcpbridge.TeamReply) mcpbridge.TeamReply {
	t.Helper()
	select {
	case r := <-got:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("the bridge call did not return")
		return mcpbridge.TeamReply{}
	}
}

// askCards returns the talkoot's open question cards.
func askCards(t *testing.T, w *Workspace) []ctrlproto.TalkootCard {
	t.Helper()
	cards, err := w.talkootInbox(t.Context(), "crew")
	if err != nil {
		t.Fatal(err)
	}
	var out []ctrlproto.TalkootCard
	for _, c := range cards {
		if c.Kind == ctrlproto.TalkootCardAsk {
			out = append(out, c)
		}
	}
	return out
}

// 🔑 A worker member asks a person as a native member does: the question opens
// a card in the talkoot's inbox under the member, the person answers it on the
// talkoot's address, the answer becomes a room line from the member, and the
// member reads the answer and its answer: reference.
func TestAWorkerAsksThePerson(t *testing.T) {
	w, _ := seatedWorker(t)
	team := w.workerTeam(crewWorker("agent-1"))
	got := make(chan mcpbridge.TeamReply, 1)
	go func() {
		got <- team(t.Context(), "ask_user_question", json.RawMessage(`{"question":"Ship the schema today?","options":["Ship it","Hold"]}`))
	}()
	var card ctrlproto.TalkootCard
	waitTalkoot(t, "the question card", func() bool {
		cards := askCards(t, w)
		if len(cards) == 1 {
			card = cards[0]
		}
		return len(cards) == 1
	})
	if card.Member != "jev" || card.Session != ctrlproto.TalkootAddr("crew") || card.Ask == nil || card.Ask.Question != "Ship the schema today?" {
		t.Fatalf("card = %+v", card)
	}
	if err := w.Answer(t.Context(), card.Session, card.ID, []core.UserAnswer{{Answer: "Ship it", Note: "after lunch"}}); err != nil {
		t.Fatal(err)
	}
	r := teamReply(t, got)
	if r.IsError || !strings.Contains(r.Text, "Ship it") {
		t.Fatalf("reply = %+v, want the person's answer", r)
	}
	lines := roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineAnswer })
	if len(lines) != 1 || lines[0].Member != "jev" || lines[0].Answers[0].Chosen[0] != "Ship it" || lines[0].Answers[0].Note != "after lunch" {
		t.Fatalf("answer lines = %+v, want jev's", lines)
	}
	if !strings.Contains(r.Text, "answer:"+lines[0].Ref) {
		t.Fatalf("reply = %q, want it to name answer:%s", r.Text, lines[0].Ref)
	}
	if n := len(askCards(t, w)); n != 0 {
		t.Fatalf("%d question cards stay open after the answer", n)
	}
}

// A question whose bridge hung up closes its card, because no one would read
// the answer.
func TestAWorkerQuestionClosesWhenTheCallEnds(t *testing.T) {
	w, _ := seatedWorker(t)
	team := w.workerTeam(crewWorker("agent-1"))
	ctx, cancel := context.WithCancel(t.Context())
	got := make(chan mcpbridge.TeamReply, 1)
	go func() {
		got <- team(ctx, "ask_user_question", json.RawMessage(`{"question":"Still there?"}`))
	}()
	waitTalkoot(t, "the question card", func() bool { return len(askCards(t, w)) == 1 })
	cancel()
	if r := teamReply(t, got); !r.IsError {
		t.Fatalf("reply = %+v, want an error", r)
	}
	waitTalkoot(t, "the card to close", func() bool { return len(askCards(t, w)) == 0 })
	if n := len(roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineAnswer })); n != 0 {
		t.Fatalf("an unanswered question left %d answer lines", n)
	}
}

// A worker that holds no seat asks nobody.
func TestAnUnseatedWorkerCannotAsk(t *testing.T) {
	w, _ := seatedWorker(t)
	r := bridgeCall(t, w, "agent-9", "ask_user_question", `{"question":"Anyone?"}`)
	if !r.IsError || r.Text != errWorkerUnseated.Error() {
		t.Fatalf("reply = %+v, want the unseated refusal", r)
	}
	if n := len(askCards(t, w)); n != 0 {
		t.Fatalf("an unseated worker opened %d cards", n)
	}
}

// The asker finds the worker's seat again when it asks. A worker that moved to
// another member's seat since its call began asks as nobody, so the question
// cannot open under a member that did not ask it.
func TestAReseatedWorkerQuestionIsRefused(t *testing.T) {
	w, _ := seatedWorker(t)
	run, member, ok := w.talkootWorker(t.Context(), "crew", "agent-1", nil)
	if !ok {
		t.Fatal("agent-1 holds no seat")
	}
	asker := &workerAsker{seat: workerSeat{ctx: t.Context(), w: w, run: run, member: member, agentID: "agent-1"}}
	w.talkoot.mu.Lock()
	run.seats["jev"] = "agent-2"
	run.seats["helm"] = "agent-1"
	w.talkoot.mu.Unlock()
	// Bounded, so a question that opened anyway fails here rather than wait.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, _, err := asker.AskCited(ctx, []core.UserQuestion{{Question: "Whose question?"}}); err != errWorkerUnseated {
		t.Fatalf("ask after the seat moved: %v, want the unseated refusal", err)
	}
	if n := len(askCards(t, w)); n != 0 {
		t.Fatalf("a reseated worker opened %d cards", n)
	}
}

// 🔑 The daemon bounds a worker's question itself, below the worker's call
// timeout. The worker reads why its question ended, the card closes, and an
// answer that comes later reaches no one and records nothing.
func TestAnUnansweredWorkerQuestionEnds(t *testing.T) {
	w, _ := seatedWorker(t)
	old := workerQuestionWait
	workerQuestionWait = 300 * time.Millisecond
	t.Cleanup(func() { workerQuestionWait = old })
	team := w.workerTeam(crewWorker("agent-1"))
	got := make(chan mcpbridge.TeamReply, 1)
	go func() {
		got <- team(t.Context(), "ask_user_question", json.RawMessage(`{"question":"Anyone there?"}`))
	}()
	var card ctrlproto.TalkootCard
	waitTalkoot(t, "the question card", func() bool {
		cards := askCards(t, w)
		if len(cards) == 1 {
			card = cards[0]
		}
		return len(cards) == 1
	})
	r := teamReply(t, got)
	if !r.IsError || !strings.Contains(r.Text, "did not answer") {
		t.Fatalf("reply = %+v, want the unanswered error", r)
	}
	waitTalkoot(t, "the card to close", func() bool { return len(askCards(t, w)) == 0 })
	if err := w.Answer(t.Context(), card.Session, card.ID, []core.UserAnswer{{Answer: "Too late"}}); err != nil {
		t.Fatal(err)
	}
	if n := len(roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineAnswer })); n != 0 {
		t.Fatalf("a late answer left %d answer lines", n)
	}
}

// The daemon's wait ends before the worker's call does.
func TestTheQuestionWaitEndsBeforeTheCall(t *testing.T) {
	if call := time.Duration(mcpbridge.TeamCallTimeoutMS) * time.Millisecond; mcpbridge.TeamQuestionWait >= call || mcpbridge.TeamQuestionWait <= 0 {
		t.Fatalf("question wait %s, call bound %s", mcpbridge.TeamQuestionWait, call)
	}
}
