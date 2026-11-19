package workspace

// A chat bridge bound to a talkoot. Each test runs a real talkoot against a
// stub provider and a fake connector, and drives the bridge through
// AttachTalkootChat, the door terva web uses.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core"
)

// teamChatConn is a fakeChatConn whose first dials can fail.
type teamChatConn struct {
	*fakeChatConn
	failures atomic.Int32
}

func (c *teamChatConn) Connect(ctx context.Context) (chat.Identity, error) {
	if c.failures.Add(-1) >= 0 {
		return chat.Identity{}, errors.New("the homeserver is down")
	}
	return c.fakeChatConn.Connect(ctx)
}

// waitOutbound waits until the chat holds a line that ok accepts, and returns
// everything sent so far.
func (c *fakeChatConn) waitOutbound(t *testing.T, what string, ok func(string) bool) []string {
	t.Helper()
	var got []string
	waitFor(t, what, func() bool {
		got = c.outbound()
		return slices.ContainsFunc(got, ok)
	}, func() string { return "the chat holds: " + strings.Join(got, " | ") })
	return got
}

func has(sub string) func(string) bool {
	return func(s string) bool { return strings.Contains(s, sub) }
}

// startTeamChat makes the crew talkoot over provider, with helm as its
// coordinator in posture and jev in the default one. It binds a fake connector
// to it for the person sothr, paired with the account u1, and returns once the
// bridge is connected and its pump runs.
func startTeamChat(t *testing.T, provider http.HandlerFunc, posture string, failures int32) (*Workspace, *teamChatConn) {
	t.Helper()
	w, conn, name := attachTeamChat(t, provider, posture, failures, "u1")
	waitFor(t, "the talkoot's chat bridge to run its pump", func() bool {
		w.chat.mu.Lock()
		defer w.chat.mu.Unlock()
		b := w.chat.bridges[name]
		return b != nil && b.stopPump != nil
	}, func() string {
		st, _ := w.chatTeamView("crew")
		return "the bridge is " + st.State + " " + st.Error
	})
	return w, conn
}

// attachTeamChat is startTeamChat without the wait, with the paired account
// named. An empty owner leaves the connector unpaired.
func attachTeamChat(t *testing.T, provider http.HandlerFunc, posture string, failures int32, owner string) (*Workspace, *teamChatConn, string) {
	t.Helper()
	cwd := talkootHome(t)
	w := openTalkootWorkspaceWith(t, cwd, provider)
	text := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n    posture: " + posture + "\n  - id: jev\n    role: specialist\n---\n"
	if _, err := w.CreateTalkoot(t.Context(), ctrlproto.TalkootCreateParams{ID: "crew", Text: text}); err != nil {
		t.Fatal(err)
	}

	conn := &teamChatConn{fakeChatConn: newFakeChatConn(chat.Capabilities{})}
	conn.failures.Store(failures)
	name := "teamchat-" + strings.ReplaceAll(t.Name(), "/", "-")
	enabled := &atomic.Bool{}
	enabled.Store(true)
	chat.Register(chat.Service{
		Name:       name,
		Configured: func(string) bool { return enabled.Load() },
		NewConnector: func(string, func(string)) (chat.Connector, chat.Pairing, error) {
			return conn, chat.Pairing{AllowedUserID: owner, Save: func(string) error { return nil }}, nil
		},
	})
	t.Cleanup(func() { enabled.Store(false) })

	if err := w.AttachTalkootChat("crew", "sothr", name); err != nil {
		t.Fatal(err)
	}
	return w, conn, name
}

// roomPosts returns every envelope in the crew's room, oldest first.
func roomPosts(t *testing.T, w *Workspace) []ctrlproto.TalkootEnvelope {
	t.Helper()
	page, err := w.TalkootRoom(t.Context(), ctrlproto.TalkootRoomParams{ID: "crew", Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var out []ctrlproto.TalkootEnvelope
	for _, l := range page.Lines {
		if l.Envelope != nil {
			out = append(out, *l.Envelope)
		}
	}
	return out
}

func quietProvider() http.HandlerFunc { return toolCallProvider("talkoot_roster", `{}`) }

// A message from the paired owner enters the room as the person's post to the
// coordinator. The room is the record, so that is where the test looks. The
// chat does not get its own message back.
func TestAChatMessageEntersTheRoomAsThePersonsPost(t *testing.T) {
	w, conn := startTeamChat(t, quietProvider(), "auto-edit", 0)

	conn.dm("ship the release")
	var posted *ctrlproto.TalkootEnvelope
	waitTalkoot(t, "the post to reach the room", func() bool {
		for _, e := range roomPosts(t, w) {
			if e.Body == "ship the release" {
				posted = &e
				return true
			}
		}
		return false
	})
	if posted.From != talkoot.HumanPrefix+"sothr" || !slices.Equal(posted.To, []string{"helm"}) {
		t.Fatalf("the post is from %q to %v, want the person sothr to the coordinator helm", posted.From, posted.To)
	}
	if posted.Chain.Root != posted.ID {
		t.Fatalf("the post's chain root is %q, want its own id %q: a person's post begins a chain", posted.Chain.Root, posted.ID)
	}

	// A post from the app goes out as "you", and it arrives after the chat's
	// own message would have. So when it is there, the echo had its chance.
	if _, err := w.PostTalkoot(t.Context(), ctrlproto.TalkootPostParams{ID: "crew", By: "sothr", Body: "from the app"}); err != nil {
		t.Fatal(err)
	}
	got := conn.waitOutbound(t, "the app's post to reach the chat", has("you: from the app"))
	if slices.ContainsFunc(got, has("ship the release")) {
		t.Fatalf("the chat got its own message back: %q", got)
	}
}

// What a member says to the person goes out. The coordinator speaks bare, and
// any other member is named. The members run in plan, the one posture that
// sends an envelope without an approval.
func TestAMembersMessageToThePersonReachesTheChat(t *testing.T) {
	w, conn := startTeamChat(t, toolCallProvider("talkoot_send", `{"to":["human"],"kind":"message","body":"The build is green."}`), "plan", 0)

	conn.dm("how is the build?")
	got := conn.waitOutbound(t, "helm's reply", has("The build is green."))
	if !slices.Contains(got, "The build is green.") {
		t.Fatalf("the chat holds %q, want the coordinator's reply with no name on it", got)
	}

	if _, err := w.PostTalkoot(t.Context(), ctrlproto.TalkootPostParams{ID: "crew", By: "sothr", To: []string{"jev"}, Body: "and you?"}); err != nil {
		t.Fatal(err)
	}
	conn.waitOutbound(t, "jev's reply, under jev's name", func(s string) bool { return s == "jev: The build is green." })
}

// An envelope between members stays in the room.
func TestTeammateTrafficStaysOutOfTheChat(t *testing.T) {
	w, conn := startTeamChat(t, toolCallProvider("talkoot_send", `{"to":["jev"],"kind":"message","body":"Jev, check the build."}`), "plan", 0)

	conn.dm("look into the build")
	waitTalkoot(t, "helm's envelope to jev", func() bool {
		return slices.ContainsFunc(roomPosts(t, w), func(e ctrlproto.TalkootEnvelope) bool {
			return e.From == "helm" && e.Body == "Jev, check the build."
		})
	})
	// The marker arrives after the envelope's event, so the envelope had its
	// chance to leak.
	if _, err := w.PostTalkoot(t.Context(), ctrlproto.TalkootPostParams{ID: "crew", By: "sothr", To: []string{"helm"}, Body: "marker"}); err != nil {
		t.Fatal(err)
	}
	got := conn.waitOutbound(t, "the marker", has("you: marker"))
	if slices.ContainsFunc(got, has("check the build")) {
		t.Fatalf("teammate traffic reached the chat: %q", got)
	}
}

func askProvider() http.HandlerFunc {
	return toolCallProvider("ask_user_question", `{"question":"Which port?","options":["8080","9090"]}`)
}

// A member's question reaches the chat, and the person's reply there settles
// the card in the inbox.
func TestAMembersQuestionIsAnsweredFromTheChat(t *testing.T) {
	w, conn := startTeamChat(t, askProvider(), "auto-edit", 0)

	conn.dm("start the server")
	got := conn.waitOutbound(t, "helm's question", has("Which port?"))
	q := got[len(got)-1]
	if !strings.Contains(q, "helm asks:") || !strings.Contains(q, "9090") {
		t.Fatalf("the question read %q, want the asker and the options", q)
	}
	waitTalkoot(t, "the card to open", func() bool { return len(inbox(t, w)) == 1 })

	conn.dm("2")
	waitTalkoot(t, "the card to close", func() bool { return len(inbox(t, w)) == 0 })
	waitTalkoot(t, "helm's turn to end", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })

	// The answer went to the card and not to the room as a post.
	for _, e := range roomPosts(t, w) {
		if e.Body == "2" {
			t.Fatal("the answer entered the room as a post")
		}
	}
}

// When the app answers first, the chat lets go of the question and says that
// it closed. The notice does not claim an answer, because a card also closes
// when its member's turn ends.
// The person's next message is then a post again.
func TestAnAnswerInTheAppWithdrawsTheChatQuestion(t *testing.T) {
	w, conn := startTeamChat(t, askProvider(), "auto-edit", 0)

	conn.dm("start the server")
	conn.waitOutbound(t, "helm's question", has("Which port?"))
	var card ctrlproto.TalkootCard
	waitTalkoot(t, "the card to open", func() bool {
		cards := inbox(t, w)
		if len(cards) == 1 {
			card = cards[0]
		}
		return len(cards) == 1
	})
	if err := w.Answer(t.Context(), card.Session, card.ID, []core.UserAnswer{{Answer: "8080"}}); err != nil {
		t.Fatal(err)
	}
	conn.waitOutbound(t, "the chat to say the question closed", has("that question is closed"))

	conn.dm("2")
	waitTalkoot(t, "the late reply to enter the room as a post", func() bool {
		return slices.ContainsFunc(roomPosts(t, w), func(e ctrlproto.TalkootEnvelope) bool { return e.Body == "2" })
	})
}

// 🚨 A question nobody answers in chat stays open in the inbox. The bridge
// never answers for the person: a declined answer written at the timeout would
// settle the card for the app too, and the member would go on without them.
func TestAnUnansweredChatQuestionStaysInTheInbox(t *testing.T) {
	wait := chatTeamAskWait
	chatTeamAskWait = 50 * time.Millisecond
	t.Cleanup(func() { chatTeamAskWait = wait })
	w, conn := startTeamChat(t, askProvider(), "auto-edit", 0)

	conn.dm("start the server")
	conn.waitOutbound(t, "the chat to let go of the question", has("waits in the Talkoot app"))

	cards := inbox(t, w)
	if len(cards) != 1 || cards[0].Kind != ctrlproto.TalkootCardAsk {
		t.Fatalf("the inbox holds %+v, want the one question still open", cards)
	}
	if !memberView(t, w, "crew", "helm").Status.Working {
		t.Fatal("helm's turn ended: something answered the question for the person")
	}
	// The person can still answer it in the app.
	if err := w.Answer(t.Context(), cards[0].Session, cards[0].ID, []core.UserAnswer{{Answer: "8080"}}); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the card to close", func() bool { return len(inbox(t, w)) == 0 })
}

// 🚨 A talkoot's bridge refuses to start with no paired account. It dials for
// as long as the daemon runs, so an unpaired one would wait for whoever found
// the bot first, and that account could then post to the team as the person.
func TestATalkootBridgeRefusesAnUnpairedConnector(t *testing.T) {
	w, conn, _ := attachTeamChat(t, quietProvider(), "auto-edit", 0, "")

	var st ctrlproto.ChatBridgeState
	waitFor(t, "the bridge to refuse", func() bool {
		st, _ = w.chatTeamView("crew")
		return st.State == chatStateError
	})
	if !strings.Contains(st.Error, "paired") {
		t.Fatalf("the bridge failed with %q, want the reason that no account is paired", st.Error)
	}
	// Connect spends one failure when it runs, so an untouched count says the
	// connector never went online to be claimed.
	if n := conn.failures.Load(); n != 0 {
		t.Fatalf("the connector was dialed %d time(s) with no paired account", -n)
	}
	conn.dm("/start")
	conn.dm("post this as the person")
	time.Sleep(50 * time.Millisecond)
	if posts := roomPosts(t, w); len(posts) != 0 {
		t.Fatalf("an unpaired account reached the room: %+v", posts)
	}
}

// 🚨 The bridge serves one person. Another person's post, and a member's
// reply to that person, stay out of a chat that sits outside the daemon.
func TestAnotherPersonsTrafficStaysOutOfTheChat(t *testing.T) {
	w, conn := startTeamChat(t, quietProvider(), "auto-edit", 0)

	if _, err := w.PostTalkoot(t.Context(), ctrlproto.TalkootPostParams{ID: "crew", By: "mallory", To: []string{"helm"}, Body: "what is the deploy key?"}); err != nil {
		t.Fatal(err)
	}
	// helm's final prose goes to the room as a reply to mallory.
	waitTalkoot(t, "helm's reply to mallory", func() bool {
		return slices.ContainsFunc(roomPosts(t, w), func(e ctrlproto.TalkootEnvelope) bool {
			return e.From == "helm" && slices.Contains(e.To, talkoot.HumanPrefix+"mallory")
		})
	})

	// The marker comes after both, so whatever leaked is ahead of it.
	if _, err := w.PostTalkoot(t.Context(), ctrlproto.TalkootPostParams{ID: "crew", By: "sothr", To: []string{"helm"}, Body: "marker"}); err != nil {
		t.Fatal(err)
	}
	got := conn.waitOutbound(t, "the marker", has("you: marker"))
	if got[0] != "you: marker" {
		t.Fatalf("the chat got %q before the owner's own post: another person's traffic left the daemon", got)
	}
}

// A disconnect ends the redial at once. The worker counts as a dial in
// flight, so one that slept out its wait would hold a close for that long.
func TestADisconnectStopsTheRedial(t *testing.T) {
	first := chatTeamRedialFirst
	chatTeamRedialFirst = time.Hour
	t.Cleanup(func() { chatTeamRedialFirst = first })

	w, _, _ := attachTeamChat(t, quietProvider(), "auto-edit", 1000, "u1")
	waitFor(t, "the first dial to fail", func() bool {
		st, _ := w.chatTeamView("crew")
		return st.State == chatStateError
	})
	if err := w.chatDisconnect(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		w.chat.dials.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the redial worker still waits after the bridge was disconnected")
	}
}

// A kickoff card is named in chat and left for the app, as a permission is.
func TestAKickoffCardIsNamedInChat(t *testing.T) {
	w, conn := startTeamChat(t, quietProvider(), "auto-edit", 0)

	ev := ctrlproto.TalkootInboxEvent("crew", ctrlproto.TalkootCard{Kind: ctrlproto.TalkootCardKickoff, ID: "kickoff"})
	w.talkootEmit(talkootEvent{Talkoot: "crew", Kind: "inbox", Wire: &ev})
	conn.waitOutbound(t, "the kickoff to be named", has("waits for its kickoff"))
}

// 🚨 A full queue can drop the event that says the app settled a card. The
// chat question must not stay open after that: it would take the person's
// next message as its answer, and the message would reach nobody.
func TestADroppedResolutionWithdrawsTheChatQuestion(t *testing.T) {
	w, conn := startTeamChat(t, askProvider(), "auto-edit", 0)

	conn.dm("start the server")
	conn.waitOutbound(t, "helm's question", has("Which port?"))
	var card ctrlproto.TalkootCard
	waitTalkoot(t, "the card to open", func() bool {
		cards := inbox(t, w)
		if len(cards) == 1 {
			card = cards[0]
		}
		return len(cards) == 1
	})

	// Fill the queue without waking the pump, so the next event is dropped.
	// The pump handles no event of this kind.
	var pump *chatTeamPump
	w.chat.mu.Lock()
	for _, b := range w.chat.bridges {
		pump = b.pump
	}
	w.chat.mu.Unlock()
	pump.mu.Lock()
	for len(pump.queue) < chatTeamQueueMax {
		pump.queue = append(pump.queue, talkootEvent{Kind: "filler"})
	}
	pump.mu.Unlock()

	if err := w.Answer(t.Context(), card.Session, card.ID, []core.UserAnswer{{Answer: "8080"}}); err != nil {
		t.Fatal(err)
	}
	conn.waitOutbound(t, "the count of what was dropped", has("did not reach this chat"))

	conn.dm("2")
	waitTalkoot(t, "the next message to enter the room as a post", func() bool {
		return slices.ContainsFunc(roomPosts(t, w), func(e ctrlproto.TalkootEnvelope) bool { return e.Body == "2" })
	})
}

// The queue has a bound, and the pump counts what the bound refused. It
// queues only the kinds it acts on, so a member's status changes cannot fill
// the queue.
func TestThePumpQueueIsBounded(t *testing.T) {
	p := &chatTeamPump{wake: make(chan struct{}, 1)}
	for range 50 {
		p.enqueue(talkootEvent{Kind: "status"})
	}
	if len(p.queue) != 0 {
		t.Fatalf("the pump queued %d status events, and it acts on none", len(p.queue))
	}
	for range chatTeamQueueMax + 7 {
		p.enqueue(talkootEvent{Kind: "envelope"})
	}
	if len(p.queue) != chatTeamQueueMax || p.dropped != 7 {
		t.Fatalf("the queue holds %d events with %d dropped, want %d and 7", len(p.queue), p.dropped, chatTeamQueueMax)
	}
}

// "skip" leaves the question for the app.
func TestSkipLeavesTheQuestionForTheApp(t *testing.T) {
	w, conn := startTeamChat(t, askProvider(), "auto-edit", 0)

	conn.dm("start the server")
	conn.waitOutbound(t, "helm's question", has("Which port?"))
	conn.dm("skip")
	conn.waitOutbound(t, "the chat to confirm the skip", has("left for the Talkoot app"))
	if cards := inbox(t, w); len(cards) != 1 {
		t.Fatalf("the inbox holds %+v, want the question still open", cards)
	}
}

// 🚨 Chat cannot approve a tool call. The card is named, and a reply that
// reads like an approval is only a post. Decision 0029 rule 21 lets chat
// grant from a verified device, and this bridge cannot check one.
func TestChatCannotApproveAToolCall(t *testing.T) {
	w, conn := startTeamChat(t, toolCallProvider("bash", `{"command":"echo hi"}`), "ask", 0)

	conn.dm("run the check")
	conn.waitOutbound(t, "the approval to be named", has("helm waits for an approval"))
	var card ctrlproto.TalkootCard
	waitTalkoot(t, "the permission card", func() bool {
		cards := inbox(t, w)
		if len(cards) == 1 {
			card = cards[0]
		}
		return len(cards) == 1
	})
	if card.Kind != ctrlproto.TalkootCardPermission {
		t.Fatalf("the card is a %s, want a permission", card.Kind)
	}

	for _, reply := range []string{"yes", "approve", "1", "allow"} {
		conn.dm(reply)
	}
	waitTalkoot(t, "the replies to enter the room as posts", func() bool {
		return slices.ContainsFunc(roomPosts(t, w), func(e ctrlproto.TalkootEnvelope) bool { return e.Body == "allow" })
	})
	cards := inbox(t, w)
	if len(cards) != 1 || cards[0].ID != card.ID || cards[0].Kind != ctrlproto.TalkootCardPermission {
		t.Fatalf("the inbox holds %+v, want the permission card still open", cards)
	}
}

// "stop" pauses the whole team, and the reply says where to lift the pause.
func TestStopFromChatPausesTheTeam(t *testing.T) {
	w, conn := startTeamChat(t, quietProvider(), "auto-edit", 0)

	conn.dm("stop")
	conn.waitOutbound(t, "the stop reply", has("paused the team"))
	waitTalkoot(t, "every member to be paused", func() bool {
		return memberView(t, w, "crew", "helm").Status.Paused != "" && memberView(t, w, "crew", "jev").Status.Paused != ""
	})

	conn.dm("/status")
	conn.waitOutbound(t, "the status reply", has("paused ("))
}

// The team's view and the chat pane both say what the bridge serves. A
// talkoot's bridge refuses to move to a session, and a disconnect ends it.
func TestTheTeamViewShowsItsChatBridge(t *testing.T) {
	w, _ := startTeamChat(t, quietProvider(), "auto-edit", 0)

	v, err := w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Chat == nil || v.Chat.State != chatStateConnected || v.Chat.Talkoot != "crew" || v.Chat.Username != "fakebot" || v.Chat.PairedID != "u1" {
		t.Fatalf("the team's chat is %+v, want a connected bridge for crew paired with u1", v.Chat)
	}
	if st := w.chatView().Bridge; st.Talkoot != "crew" || st.Session != "" {
		t.Fatalf("the chat pane says %+v, want the talkoot and no session", st)
	}
	// The list carries the bridge too. The app reads the list again on
	// talkoots_changed, and it reads the team's view only when the roster moves.
	list, err := w.Talkoots(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Chat == nil || list[0].Chat.State != chatStateConnected || list[0].Chat.Talkoot != "crew" {
		t.Fatalf("the list says %+v, want crew with a connected bridge", list)
	}

	sess, err := w.CreateSession(t.Context(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.chatRebind(sess.ID); err == nil || !strings.Contains(err.Error(), "crew") {
		t.Fatalf("chatRebind = %v, want a refusal that names the talkoot", err)
	}

	// A disconnect tells the app, which then reads the list again.
	ws, err := w.Subscribe(t.Context(), ctrlproto.AddrWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.chatDisconnect(); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, ws, ctrlproto.EventTalkootsChanged)
	if v, err = w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"}); err != nil || v.Chat != nil {
		t.Fatalf("after a disconnect the team's chat is %+v, %v", v.Chat, err)
	}
	if list, err = w.Talkoots(t.Context()); err != nil || len(list) != 1 || list[0].Chat != nil {
		t.Fatalf("after a disconnect the list says %+v, %v", list, err)
	}
}

// A handshake that fails is tried again. The daemon does not wait for it, so
// a homeserver that is down at boot does not keep the Talkoot app down.
func TestATalkootBridgeDialsAgainAfterAFailedHandshake(t *testing.T) {
	first := chatTeamRedialFirst
	chatTeamRedialFirst = 20 * time.Millisecond
	t.Cleanup(func() { chatTeamRedialFirst = first })

	// startTeamChat returns only once the pump runs, which is after the two
	// failed dials.
	w, conn := startTeamChat(t, quietProvider(), "auto-edit", 2)
	if n := conn.failures.Load(); n >= 0 {
		t.Fatalf("the connector still has %d failures to spend: the bridge connected without dialing again", n+1)
	}
	if st, ok := w.chatTeamView("crew"); !ok || st.State != chatStateConnected || st.Error != "" {
		t.Fatalf("the bridge is %+v, want connected with no error", st)
	}
	conn.dm("still there?")
	waitTalkoot(t, "a post after the redial", func() bool {
		return slices.ContainsFunc(roomPosts(t, w), func(e ctrlproto.TalkootEnvelope) bool { return e.Body == "still there?" })
	})
}

// A bridge needs a talkoot this daemon runs and a person's name.
func TestAttachTalkootChatRefusesABadTarget(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	if err := w.AttachTalkootChat("nobody", "sothr", "any"); err == nil {
		t.Fatal("a talkoot that does not exist was accepted")
	}
	if _, err := w.CreateTalkoot(t.Context(), ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	if err := w.AttachTalkootChat("crew", "two words", "any"); err == nil || !strings.Contains(err.Error(), "person") {
		t.Fatalf("AttachTalkootChat with a bad name = %v, want a refusal about the person", err)
	}
	if _, ok := w.chatTeamView("crew"); ok {
		t.Fatal("a refused attach left a bridge in the registry")
	}
}
