package workspace

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// replyClient emits commentary plus a real tool call, then a final reply.
// Failed and interrupted variants retain visible partial text in the session.
type replyClient struct {
	calls                     atomic.Int64
	explicit, fail, interrupt bool
	// toPerson makes the explicit first step a talkoot_send to the person,
	// in words that differ from the final prose.
	toPerson bool
	entered  chan struct{}
}

func (*replyClient) Name() string { return "talkoot-reply-test" }
func (c *replyClient) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 2)
	if c.calls.Add(1) == 1 && c.explicit {
		args := `{"to":["jev"],"kind":"note","body":"Final reply."}`
		if c.toPerson {
			args = `{"to":["human"],"kind":"message","body":"Here is my answer, in my own words."}`
		}
		out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.TextBlock{Text: "Tool-step commentary."},
			provider.ToolCallBlock{ID: "send-1", Name: "talkoot_send", Arguments: []byte(args)},
		}}}
		close(out)
		return out, nil
	}
	go func() {
		defer close(out)
		out <- provider.EventTextDelta{Delta: "Final reply."}
		stop, err := provider.StopEnd, error(nil)
		if c.fail {
			stop, err = provider.StopError, provider.NewHTTPError("reply-test", 400, "", "refused")
		}
		if c.interrupt {
			close(c.entered)
			<-ctx.Done()
			stop, err = provider.StopError, errors.New("provider stopped after interrupt")
		}
		out <- provider.EventDone{Stop: stop, Err: err, Message: provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.ReasoningBlock{Summary: "Private reasoning."}, provider.TextBlock{Text: "Final reply."},
		}}}
	}()
	return out, nil
}

func replySession(t *testing.T, client provider.Client, rooted bool) (*wsSession, *talkoot.Router, *talkoot.Room, *recordDriver, talkoot.Envelope) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := newTurnTestSession(t, client)
	r, err := talkoot.Parse(crewText("/test"), "crew.md")
	if err != nil {
		t.Fatal(err)
	}
	r.ID = "crew"
	d := &recordDriver{}
	dir := testsupport.TempDir(t)
	if err := talkoot.CreateRoom(dir); err != nil {
		t.Fatal(err)
	}
	room := talkoot.OpenRoom(dir)
	rt, err := talkoot.NewRouter(r, room, talkoot.Drivers{Native: d, Worker: d}, talkoot.DefaultLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run := testRun("crew", rt, r)
	run.dir, run.room = dir, room
	b := &seatBinding{run: run, member: "helm"}
	s.ws.talkoot.seats = map[string]*seatBinding{s.id: b}
	s.agent.SetTools(core.Registry{"talkoot_send": &tools.TalkootSendTool{Seat: talkootSeat{b: b, w: s.ws}}})
	var root talkoot.Envelope
	if rooted {
		root, err = rt.Post("Drew", nil, "Please check it.", nil, "review")
		if err != nil {
			t.Fatal(err)
		}
	}
	return s, rt, room, d, root
}

func replyRoomLines(t *testing.T, room *talkoot.Room) []talkoot.Line {
	t.Helper()
	ls, err := room.Read()
	if err != nil {
		t.Fatal(err)
	}
	return ls
}

// deliverReply starts the member's turn the way the router does for a
// person's post: a queued delivery that the turn reads. A direct s.prompt is a
// person typing into the session, which must stay private.
func deliverReply(s *wsSession, text string) {
	s.queueTalkoot("[talkoot crew] "+text, true, func() {})
}

// 🚨 Dogfood finding (2026-10-01): a teammate's handoff in a chain a person
// rooted had its final reply mirrored to the person, so specialists reported
// to the room past their coordinator. Only a person's delivery may start a
// mirrored turn.
func TestNativeFinalReplySkipsATurnATeammateStarted(t *testing.T) {
	s, _, room, _, _ := replySession(t, &replyClient{}, true)
	s.queueTalkoot("[talkoot crew] Handoff from jev.", false, func() {})
	awaitReplyTurn(t, room)
	// The turn really produced a reply the mirror could have used.
	if got := finalTalkootText(s.agent.Messages()); got != "Final reply." {
		t.Fatalf("fixture: no final reply: %q", got)
	}
	for _, l := range replyRoomLines(t, room) {
		if l.Type == talkoot.LineEnvelope && l.Envelope.From == "helm" {
			t.Fatalf("a teammate-started turn published to the person: %+v", l.Envelope)
		}
	}
}

func awaitReplyTurn(t *testing.T, room *talkoot.Room) {
	t.Helper()
	waitTalkoot(t, "native reply turn to settle", func() bool {
		return slices.ContainsFunc(replyRoomLines(t, room), func(l talkoot.Line) bool { return l.Type == talkoot.LineTurn })
	})
}

func TestNativeFinalReplyAppearsInRoomWithoutSend(t *testing.T) {
	s, _, room, d, root := replySession(t, &replyClient{}, true)
	deliverReply(s, "Please check it.")
	awaitReplyTurn(t, room)
	var replies []talkoot.Envelope
	for _, l := range replyRoomLines(t, room) {
		if l.Type == talkoot.LineEnvelope && l.Envelope.From == "helm" {
			replies = append(replies, *l.Envelope)
		}
	}
	if len(replies) != 1 {
		t.Fatalf("want one completed room reply without talkoot_send, got %+v", replies)
	}
	e := replies[0]
	if e.Body != "Final reply." || e.From != "helm" || !slices.Equal(e.To, []string{"human:Drew"}) || e.Chain.Root != root.ID || e.ReplyTo != root.ID || e.Thread != "review" {
		t.Fatalf("final reply lost provenance: %+v", e)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.got) != 1 {
		t.Fatalf("final reply woke another member: %v", d.got)
	}
}

// 🚨 Dogfood finding (2026-10-01): mieli sent the person its answer with
// talkoot_send, then ended the turn with different prose, and the mirror
// posted that too. The body dedupe only catches identical text.
func TestNativeFinalReplySkipsATurnThatAlreadyMessagedThePerson(t *testing.T) {
	s, _, room, _, _ := replySession(t, &replyClient{explicit: true, toPerson: true}, true)
	deliverReply(s, "Please check it.")
	awaitReplyTurn(t, room)
	var bodies []string
	for _, l := range replyRoomLines(t, room) {
		if l.Type == talkoot.LineEnvelope && l.Envelope.From == "helm" && slices.Contains(l.Envelope.To, "human:Drew") {
			bodies = append(bodies, l.Envelope.Body)
		}
	}
	// The explicit send landed, and the final prose differs from it, so only
	// the new rule keeps the second copy out.
	if len(bodies) == 0 || bodies[0] != "Here is my answer, in my own words." {
		t.Fatalf("fixture: the explicit send to the person is missing: %q", bodies)
	}
	if got := finalTalkootText(s.agent.Messages()); got != "Final reply." {
		t.Fatalf("fixture: no distinct final prose: %q", got)
	}
	if len(bodies) != 1 {
		t.Fatalf("the person got the answer twice: %q", bodies)
	}
}

func TestNativeFinalReplyDoesNotDoublePostExplicitSend(t *testing.T) {
	s, _, room, d, _ := replySession(t, &replyClient{explicit: true}, true)
	deliverReply(s, "Check and tell jev.")
	awaitReplyTurn(t, room)
	var replies []talkoot.Envelope
	for _, l := range replyRoomLines(t, room) {
		if l.Type == talkoot.LineEnvelope && l.Envelope.From == "helm" {
			replies = append(replies, *l.Envelope)
		}
	}
	if len(replies) != 1 || replies[0].Body != "Final reply." || replies[0].Kind != talkoot.KindNote || !slices.Equal(replies[0].To, []string{"jev"}) {
		t.Fatalf("final reply doubled a tool send or published commentary: %+v", replies)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.got) != 1 {
		t.Fatal("a note or its mirror woke a member")
	}
}

func TestNativeFinalReplyRejectsPartialAndUnrootedTurns(t *testing.T) {
	for _, mode := range []string{"failed", "interrupted", "unrooted"} {
		t.Run(mode, func(t *testing.T) {
			c := &replyClient{fail: mode == "failed", interrupt: mode == "interrupted", entered: make(chan struct{})}
			s, _, room, _, _ := replySession(t, c, mode != "unrooted")
			deliverReply(s, "Check it.")
			if mode == "interrupted" {
				<-c.entered
				s.interruptTurn()
			}
			awaitReplyTurn(t, room)
			for _, l := range replyRoomLines(t, room) {
				if l.Type == talkoot.LineEnvelope && l.Envelope.From == "helm" {
					t.Fatalf("%s turn published partial text: %+v", mode, l.Envelope)
				}
			}
			// The provider really emitted visible text; this is not an empty failure.
			if !slices.ContainsFunc(s.agent.Messages(), func(m provider.Message) bool {
				return m.Role == provider.RoleAssistant && assistantVisibleText(m) == "Final reply."
			}) {
				t.Fatal("test never retained the partial assistant text")
			}
		})
	}
}

// 🚨 Review finding on PR #1546: a seat alone made every turn public. A
// person who types into a member's session directly gets a private answer,
// even while the member's chain has a human root.
func TestNativeFinalReplyKeepsDirectSessionTurnPrivate(t *testing.T) {
	s, _, room, _, _ := replySession(t, &replyClient{}, true)
	if err := s.prompt("A private question.", nil, core.UserMessageExtras{}); err != nil {
		t.Fatal(err)
	}
	awaitReplyTurn(t, room)
	// The turn really produced a final reply that publishing could have used.
	if got := finalTalkootText(s.agent.Messages()); got != "Final reply." {
		t.Fatalf("fixture produced no final reply: %q", got)
	}
	for _, l := range replyRoomLines(t, room) {
		if l.Type == talkoot.LineEnvelope && l.Envelope.From == "helm" {
			t.Fatalf("a direct session turn published to the room: %+v", l.Envelope)
		}
	}
}

// 🚨 Review finding on PR #1546: a turn that appends no assistant message
// left the previous turn's reply as the transcript's last message, and the
// mirror published it again under the member's new chain.
func TestNativeFinalReplyIgnoresAnEarlierTurnsReply(t *testing.T) {
	s, rt, room, _, _ := replySession(t, &replyClient{}, true)
	deliverReply(s, "Please check it.")
	awaitReplyTurn(t, room)
	if _, err := rt.Post("Kaisa", []string{"helm"}, "Another question.", nil, ""); err != nil {
		t.Fatal(err)
	}
	// The second turn reads a delivery but its generation appends nothing.
	text := "[talkoot crew] Another question."
	s.reads.mu.Lock()
	s.reads.pending = append(s.reads.pending, talkootRead{text: text, person: true, read: func() {}})
	s.reads.mu.Unlock()
	var turnCtx context.Context
	waitTalkoot(t, "the turn slot", func() bool {
		ctx, err := s.beginTurn()
		turnCtx = ctx
		return err == nil
	})
	s.launchTurn(turnCtx, func(context.Context) error {
		s.reads.observe(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: text}}})
		return nil
	}, nil)
	waitTalkoot(t, "the second turn to settle", func() bool {
		n := 0
		for _, l := range replyRoomLines(t, room) {
			if l.Type == talkoot.LineTurn && l.Member == "helm" {
				n++
			}
		}
		return n == 2
	})
	// The fixture really left an earlier reply for the mirror to find.
	if got := finalTalkootText(s.agent.Messages()); got != "Final reply." {
		t.Fatalf("fixture left no earlier reply: %q", got)
	}
	for _, l := range replyRoomLines(t, room) {
		if l.Type == talkoot.LineEnvelope && l.Envelope.From == "helm" && slices.Contains(l.Envelope.To, "human:Kaisa") {
			t.Fatalf("an earlier turn's reply reached a new chain: %+v", l.Envelope)
		}
	}
}

// 🚨 Review finding on PR #1546 (round 2): the gate checked that a turn read
// a delivery, not that a delivery started it. A person's private prompt that
// drained a queued delivery partway published its answer to the room.
func TestNativeFinalReplyKeepsAPrivateTurnPrivateWhenItDrainsADelivery(t *testing.T) {
	s, rt, room, _, _ := replySession(t, &replyClient{}, true)
	deliverReply(s, "Please check it.")
	awaitReplyTurn(t, room)
	if _, err := rt.Post("Kaisa", []string{"helm"}, "Another question.", nil, ""); err != nil {
		t.Fatal(err)
	}
	text := "[talkoot crew] Another question."
	s.reads.mu.Lock()
	s.reads.pending = append(s.reads.pending, talkootRead{text: text, person: true, read: func() {}})
	s.reads.mu.Unlock()
	var turnCtx context.Context
	waitTalkoot(t, "the turn slot", func() bool {
		ctx, err := s.beginTurn()
		turnCtx = ctx
		return err == nil
	})
	takenBefore, repliesBefore := s.reads.taken.Load(), s.reads.replies.Load()
	// A person's private prompt opens the turn, a delivery drains at a safe
	// boundary, and the turn ends with an assistant message.
	s.launchTurn(turnCtx, func(context.Context) error {
		user := func(t string) provider.Message {
			return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: t}}}
		}
		s.reads.observe(user("A private question."))
		s.reads.observe(user(text))
		s.reads.observe(provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "Final reply."}}})
		return nil
	}, nil)
	waitTalkoot(t, "the second turn to settle", func() bool {
		n := 0
		for _, l := range replyRoomLines(t, room) {
			if l.Type == talkoot.LineTurn && l.Member == "helm" {
				n++
			}
		}
		return n == 2
	})
	// The turn read a delivery and produced a reply, so only who started it
	// keeps it private.
	if s.reads.taken.Load() == takenBefore || s.reads.replies.Load() == repliesBefore {
		t.Fatal("fixture: the turn did not read a delivery and reply")
	}
	for _, l := range replyRoomLines(t, room) {
		if l.Type == talkoot.LineEnvelope && l.Envelope.From == "helm" && slices.Contains(l.Envelope.To, "human:Kaisa") {
			t.Fatalf("a private turn published to the room: %+v", l.Envelope)
		}
	}
}

// 🚨 Review findings on PR #1546 (round 3): a turn that a delivery opened
// published even after more input joined it. A second delivery moved the
// member's chain, so the reply went to the second person. A person's private
// message let the room see an answer to it.
//
// ⚠️ The second turn's generation only reports messages to the observer, so
// the transcript's final reply comes from a real first turn. Without it the
// mirror has no text, and the test passes whatever the gate does.
func TestNativeFinalReplySkipsATurnThatTookMoreInput(t *testing.T) {
	for name, joined := range map[string]string{
		"delivery": "[talkoot crew] A third question.",
		"person":   "A private follow-up.",
	} {
		t.Run(name, func(t *testing.T) {
			s, rt, room, _, _ := replySession(t, &replyClient{}, true)
			deliverReply(s, "Please check it.")
			awaitReplyTurn(t, room)
			if got := finalTalkootText(s.agent.Messages()); got != "Final reply." {
				t.Fatalf("fixture: no final reply for the mirror to find: %q", got)
			}
			if _, err := rt.Post("Kaisa", []string{"helm"}, "Another question.", nil, ""); err != nil {
				t.Fatal(err)
			}
			opening := "[talkoot crew] Another question."
			s.reads.mu.Lock()
			s.reads.pending = append(s.reads.pending,
				talkootRead{text: opening, person: true, read: func() {}},
				talkootRead{text: "[talkoot crew] A third question.", person: true, read: func() {}})
			s.reads.mu.Unlock()
			var turnCtx context.Context
			waitTalkoot(t, "the turn slot", func() bool {
				ctx, err := s.beginTurn()
				turnCtx = ctx
				return err == nil
			})
			openedBefore, repliesBefore := s.reads.opened.Load(), s.reads.replies.Load()
			user := func(t string) provider.Message {
				return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: t}}}
			}
			s.launchTurn(turnCtx, func(context.Context) error {
				s.reads.observe(user(opening))
				// A tool round is not input, so it must not count.
				s.reads.observe(provider.Message{Role: provider.RoleTool, Content: []provider.Content{provider.TextBlock{Text: "tool output"}}})
				s.reads.observe(user(joined))
				s.reads.observe(provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "Final reply."}}})
				return nil
			}, nil)
			waitTalkoot(t, "the second turn to settle", func() bool {
				n := 0
				for _, l := range replyRoomLines(t, room) {
					if l.Type == talkoot.LineTurn && l.Member == "helm" {
						n++
					}
				}
				return n == 2
			})
			// A delivery opened the turn and it replied, so only the joined
			// input keeps the reply out of the room.
			if s.reads.opened.Load() == openedBefore || s.reads.replies.Load() == repliesBefore {
				t.Fatal("fixture: a delivery did not open the turn, or the turn did not reply")
			}
			for _, l := range replyRoomLines(t, room) {
				if l.Type == talkoot.LineEnvelope && l.Envelope.From == "helm" && slices.Contains(l.Envelope.To, "human:Kaisa") {
					t.Fatalf("a turn that took more input published: %+v", l.Envelope)
				}
			}
		})
	}
}

// 🚨 Review finding on PR #1546 (round 4): an opening message that carried
// two deliveries counted as one input, so the reply could reach the second
// chain's person. Core appends one text per message today; this guards it.
func TestNativeFinalReplySkipsAnOpeningMessageWithTwoDeliveries(t *testing.T) {
	s, rt, room, _, _ := replySession(t, &replyClient{}, true)
	deliverReply(s, "Please check it.")
	awaitReplyTurn(t, room)
	if got := finalTalkootText(s.agent.Messages()); got != "Final reply." {
		t.Fatalf("fixture: no final reply for the mirror to find: %q", got)
	}
	if _, err := rt.Post("Kaisa", []string{"helm"}, "Another question.", nil, ""); err != nil {
		t.Fatal(err)
	}
	first, second := "[talkoot crew] One.", "[talkoot crew] Another question."
	s.reads.mu.Lock()
	s.reads.pending = append(s.reads.pending, talkootRead{text: first, person: true, read: func() {}}, talkootRead{text: second, person: true, read: func() {}})
	s.reads.mu.Unlock()
	var turnCtx context.Context
	waitTalkoot(t, "the turn slot", func() bool {
		ctx, err := s.beginTurn()
		turnCtx = ctx
		return err == nil
	})
	takenBefore, repliesBefore := s.reads.taken.Load(), s.reads.replies.Load()
	s.launchTurn(turnCtx, func(context.Context) error {
		s.reads.observe(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: first}, provider.TextBlock{Text: second}}})
		s.reads.observe(provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "Final reply."}}})
		return nil
	}, nil)
	waitTalkoot(t, "the second turn to settle", func() bool {
		n := 0
		for _, l := range replyRoomLines(t, room) {
			if l.Type == talkoot.LineTurn && l.Member == "helm" {
				n++
			}
		}
		return n == 2
	})
	if s.reads.taken.Load()-takenBefore != 2 || s.reads.replies.Load() == repliesBefore {
		t.Fatal("fixture: the opening message did not read two deliveries and reply")
	}
	for _, l := range replyRoomLines(t, room) {
		if l.Type == talkoot.LineEnvelope && l.Envelope.From == "helm" && slices.Contains(l.Envelope.To, "human:Kaisa") {
			t.Fatalf("a two-delivery opening published: %+v", l.Envelope)
		}
	}
}

func TestNativeFinalReplyRevokedSeatCannotPublish(t *testing.T) {
	s, _, room, _, _ := replySession(t, &replyClient{}, true)
	end, publish, closing := s.ws.talkootTurn(s.id)
	if closing || publish == nil {
		t.Fatal("turn did not capture its seat")
	}
	// A new binding cannot authorize this old turn, even as the same member.
	old := s.ws.talkoot.seats[s.id]
	old.revoke()
	s.ws.talkoot.seats[s.id] = &seatBinding{run: old.run, member: "jev"}
	publish("An old turn's reply.")
	end(0, true, "", false)
	for _, l := range replyRoomLines(t, room) {
		if l.Type == talkoot.LineEnvelope && l.Envelope.From != "human:Drew" {
			t.Fatalf("revoked turn spoke through another seat: %+v", l.Envelope)
		}
	}
}

func TestFinalTalkootTextOnlySelectsFinalProse(t *testing.T) {
	for _, tc := range []struct {
		name string
		msgs []provider.Message
		want string
	}{
		{name: "empty"},
		{name: "incomplete", msgs: []provider.Message{{Role: provider.RoleAssistant, Meta: map[string]string{core.MetaIncomplete: "true"}, Content: []provider.Content{provider.TextBlock{Text: "partial"}}}}},
		{name: "user", msgs: []provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "user"}}}}},
		{name: "tool commentary", msgs: []provider.Message{{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "commentary"}, provider.ToolCallBlock{ID: "x"}}}}},
		{name: "final", msgs: []provider.Message{{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "old"}}}, {Role: provider.RoleAssistant, Content: []provider.Content{provider.ReasoningBlock{Summary: "private"}, provider.TextBlock{Text: "final"}}}}, want: "final"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := finalTalkootText(tc.msgs); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Exercise the real native queue and read callback, not only a record driver.
func TestNativeQueuedFinalReplyPreservesReadChain(t *testing.T) {
	w, release, entered := openHeldWorkspace(t)
	root, err := w.talkootPost(context.Background(), "crew", "Drew", nil, "Check it.", nil, "topic")
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	release()
	waitTalkoot(t, "real native turn end", func() bool {
		return len(roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineTurn && l.Member == "helm" })) > 0
	})
	replies := roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineEnvelope && l.Envelope.From == "helm" })
	if len(replies) != 1 || replies[0].Envelope.To[0] != "human:Drew" || replies[0].Envelope.Chain.Root != root.ID || replies[0].Envelope.Body != "ok" {
		t.Fatalf("real native final reply missing: %+v", replies)
	}
	if memberView(t, w, "crew", "jev").Session != "" {
		t.Fatal("room mirror woke jev")
	}
}
