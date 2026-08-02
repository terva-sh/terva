package chat

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
)

// askerFor builds a ChatAsker over a started loop, the way botcmd does.
func askerFor(l *Loop) *ChatAsker {
	return NewChatAsker(context.Background(), func() *Loop { return l })
}

// pairChat drives a real inbound DM so the loop records chat "100" as the ask
// target. Poking l.pairedChatID directly would race the running loop goroutine
// under -race; pairedWith() alone is not enough, since it seeds the target with
// the USER id and answers are matched on the CHAT id.
//
// Returns the send count after pairing, so callers index past the agent's reply.
func pairChat(t *testing.T, conn *fakeConnector, l *Loop) int {
	t.Helper()
	conn.inbound <- msgFrom("7", "hello")
	conn.waitSends(t, 1)
	return 1
}

// A set is posed ONE QUESTION AT A TIME, in order, and each answer lands
// against its own question. The linear shape is the whole reason this needs a
// test: an implementation that fired all eight asks at once would deadlock on
// Loop's one-ask-at-a-time slot, and one that returned answers out of order
// would be indexed wrongly by every caller.
func TestChatAskerPosesSetInOrder(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	l := startLoop(t, conn, &scriptedClient{reply: "ok"}, pairedWith("7"))
	base := pairChat(t, conn, l)

	type result struct {
		ans []core.UserAnswer
		err error
	}
	done := make(chan result, 1)
	go func() {
		ans, err := askerFor(l).Ask(context.Background(), []core.UserQuestion{
			{Question: "which database?", Options: []string{"postgres", "sqlite"}},
			{Question: "run migrations?", Options: []string{"yes", "no"}},
		})
		done <- result{ans, err}
	}()

	sends := conn.waitSends(t, base+1)
	if q := sends[base]; !strings.Contains(q.Text, "which database?") ||
		!strings.Contains(q.Text, "question 1 of 2") {
		t.Fatalf("first question = %q", q.Text)
	}
	// The second question must NOT be out yet.
	if got := conn.sends(); len(got) > base+1 {
		t.Fatalf("both questions were posed at once: %+v", got)
	}
	conn.inbound <- msgFrom("7", "2") // sqlite

	sends = conn.waitSends(t, base+2)
	if q := sends[base+1]; !strings.Contains(q.Text, "run migrations?") ||
		!strings.Contains(q.Text, "question 2 of 2") {
		t.Fatalf("second question = %q", q.Text)
	}
	conn.inbound <- msgFrom("7", "1") // yes

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Ask: %v", r.err)
		}
		if len(r.ans) != 2 {
			t.Fatalf("want one answer per question, got %d", len(r.ans))
		}
		if got := r.ans[0].Answer; got != "sqlite" {
			t.Errorf("answer 0 = %q, want sqlite", got)
		}
		if got := r.ans[1].Answer; got != "yes" {
			t.Errorf("answer 1 = %q, want yes", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ask never resolved")
	}
}

// The no-answer contract, and the reason this type exists separately from
// ChatConfirmer: an expired QUESTION is a dismissal, not a failure. It must
// return Declined answers and a nil error — never an error the model could
// read as a broken channel and retry into a loop.
//
// And it must stop asking: a chat that ignored the first question will ignore
// the rest, so posting them anyway would burn one full timeout each and stall
// the turn for minutes.
func TestChatAskerTimeoutDeclinesRestWithoutAsking(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	l := startLoop(t, conn, &scriptedClient{reply: "ok"}, pairedWith("7"))
	pairChat(t, conn, l)

	asker := askerFor(l)
	asker.Timeout = 80 * time.Millisecond
	ans, err := asker.Ask(context.Background(), []core.UserQuestion{
		{Question: "first?", Options: []string{"a", "b"}},
		{Question: "second?", Options: []string{"a", "b"}},
		{Question: "third?", Options: []string{"a", "b"}},
	})
	if err != nil {
		t.Fatalf("a lapsed question must not be an error: %v", err)
	}
	if len(ans) != 3 {
		t.Fatalf("want 3 answers (one per question), got %d", len(ans))
	}
	for i, a := range ans {
		if !a.Declined {
			t.Errorf("answer %d: want Declined, got %+v", i, a)
		}
		if a.Note == "" {
			t.Errorf("answer %d: a decline must say why", i)
		}
	}

	// Only the FIRST question reached the chat. The withdrawal notice rides
	// along, so filter to the questions themselves.
	var asked int
	for _, s := range conn.sends() {
		if strings.Contains(s.Text, "?") && strings.Contains(s.Text, "reply with a number") {
			asked++
		}
	}
	if asked != 1 {
		t.Errorf("asked %d questions after a timeout; want 1 — the rest decline unasked", asked)
	}
}

// A written-in answer round-trips as itself. This is the case that used to be
// declined unasked ("this chat can only offer a fixed list"), and the reason
// the floor exists: a question with no options at all is free-form by
// definition, so posing it is the only honest thing to do with it.
func TestChatAskerCarriesAWrittenInAnswer(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	l := startLoop(t, conn, &scriptedClient{reply: "ok"}, pairedWith("7"))
	base := pairChat(t, conn, l)

	done := make(chan []core.UserAnswer, 1)
	go func() {
		ans, _ := askerFor(l).Ask(context.Background(), []core.UserQuestion{
			{Question: "what should I name it?", AllowCustom: true},
		})
		done <- ans
	}()

	sends := conn.waitSends(t, base+1)
	if q := sends[base].Text; !strings.Contains(q, "what should I name it?") ||
		!strings.Contains(q, "your own") {
		t.Errorf("the question must invite a written-in answer: %q", q)
	}
	conn.inbound <- msgFrom("7", "call it gamma")

	select {
	case ans := <-done:
		if len(ans) != 1 {
			t.Fatalf("want 1 answer, got %d", len(ans))
		}
		if ans[0].Declined {
			t.Fatalf("a written-in answer is an answer, not a decline: %+v", ans[0])
		}
		if got := ans[0].Chosen(); len(got) != 1 || got[0] != "call it gamma" {
			t.Errorf("Chosen() = %v, want [call it gamma]", got)
		}
		// Nothing was picked from a list, so nothing should claim it was.
		if len(ans[0].Answers) != 0 {
			t.Errorf("Answers = %v, want empty for a written-in answer", ans[0].Answers)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ask never resolved")
	}
}

// Several choices come back as several. The model declared MultiSelect because
// it judged the options non-exclusive; collecting one and returning it
// unqualified used to narrow what the user agreed to, which is precisely what
// the floor now makes unnecessary.
func TestChatAskerCollectsSeveralChoices(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	l := startLoop(t, conn, &scriptedClient{reply: "ok"}, pairedWith("7"))
	base := pairChat(t, conn, l)

	done := make(chan []core.UserAnswer, 1)
	go func() {
		ans, _ := askerFor(l).Ask(context.Background(), []core.UserQuestion{
			{Question: "which to enable?", Options: []string{"logs", "traces", "metrics"}, MultiSelect: true},
		})
		done <- ans
	}()

	sends := conn.waitSends(t, base+1)
	if q := sends[base].Text; !strings.Contains(q, "commas") {
		t.Errorf("the question must invite several answers: %q", q)
	}
	conn.inbound <- msgFrom("7", "1,3")

	select {
	case ans := <-done:
		if len(ans) != 1 {
			t.Fatalf("want 1 answer, got %d", len(ans))
		}
		if got := ans[0].Chosen(); !reflect.DeepEqual(got, []string{"logs", "metrics"}) {
			t.Errorf("Chosen() = %v, want [logs metrics]", got)
		}
		// No note: nothing was degraded, and a note claiming otherwise would
		// tell the model it got less than it did.
		if ans[0].Note != "" {
			t.Errorf("Note = %q, want empty — nothing was narrowed", ans[0].Note)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ask never resolved")
	}
}

// With nowhere to ask, this is NOT a decline. "Nobody answered" and "there is
// no channel" are different facts and the model acts on them differently — the
// first is a judgement call it should make itself, the second is a setup
// problem worth reporting.
func TestChatAskerUnpairedIsAnErrorNotADecline(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	l := startLoop(t, conn, &scriptedClient{reply: "ok"}, Pairing{})

	_, err := askerFor(l).Ask(context.Background(), []core.UserQuestion{
		{Question: "anything?", Options: []string{"a"}},
	})
	if err == nil {
		t.Fatal("an unpaired bot must report no channel, not a silent decline")
	}
	if !strings.Contains(err.Error(), "paired") {
		t.Errorf("error should name the cause: %v", err)
	}
}

// A cancelled turn surfaces ctx.Err(), per the core.Asker contract, rather
// than a fabricated answer.
func TestChatAskerHonoursContextCancellation(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	l := startLoop(t, conn, &scriptedClient{reply: "ok"}, pairedWith("7"))
	base := pairChat(t, conn, l)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := askerFor(l).Ask(ctx, []core.UserQuestion{
			{Question: "waiting?", Options: []string{"a", "b"}},
		})
		done <- err
	}()
	conn.waitSends(t, base+1)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not unblock the ask")
	}
}
