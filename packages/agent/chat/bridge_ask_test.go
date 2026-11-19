package chat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// askingBridge starts a bridge paired with user 7 and lets one message
// through, so the bridge knows which chat to ask in.
func askingBridge(t *testing.T, conn Connector, inbound chan<- Message) (*Bridge, *fakeHost) {
	t.Helper()
	host := &fakeHost{}
	b := &Bridge{Connector: conn, Host: host, Pairing: pairedWith("7")}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(b.Stop)
	inbound <- msgFrom("7", "hello")
	host.waitSubmitted(t, 1)
	return b, host
}

type askResult struct {
	answers []core.UserAnswer
	err     error
}

func askAsync(b *Bridge, qs []core.UserQuestion, opts QuestionOpts) <-chan askResult {
	out := make(chan askResult, 1)
	go func() {
		a, err := b.AskQuestions(context.Background(), qs, opts)
		out <- askResult{a, err}
	}()
	return out
}

func waitAsk(t *testing.T, ch <-chan askResult) askResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("AskQuestions did not return")
		return askResult{}
	}
}

// A free-text question takes the owner's next message as its answer, and that
// message does not reach the host as a prompt.
func TestBridgeTextFloorAnswersWithTheNextMessage(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	b, host := askingBridge(t, conn, conn.inbound)

	res := askAsync(b, []core.UserQuestion{{Question: "Which branch?"}}, QuestionOpts{From: "jev"})
	sent := conn.waitSends(t, 1)
	q := sent[len(sent)-1].Text
	if !strings.Contains(q, "jev asks:") || !strings.Contains(q, "Which branch?") {
		t.Fatalf("the question read %q, want the asker and the question", q)
	}
	if !strings.Contains(q, "skip") {
		t.Fatalf("the question read %q, and it must say how to leave it unanswered", q)
	}

	conn.inbound <- msgFrom("7", "the release branch")
	r := waitAsk(t, res)
	if r.err != nil || len(r.answers) != 1 || r.answers[0].Answer != "the release branch" || r.answers[0].Declined {
		t.Fatalf("AskQuestions = %+v, %v", r.answers, r.err)
	}
	host.mu.Lock()
	got := append([]string(nil), host.submitted...)
	host.mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("the host received %q: the answer went on as a prompt", got)
	}
}

// 🚨 One reply answers one question. The waiter clears the question only when
// it wakes, so a second message can arrive while the first reply is still in
// hand. That message is a prompt, and it must not vanish as a second answer.
func TestBridgeTextFloorTakesOnlyOneReply(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	b, _ := askingBridge(t, conn, conn.inbound)

	p := &bridgeTextAsk{ask: Ask{ChatID: "100", AllowCustom: true}, result: make(chan bridgeTextResult, 1)}
	b.mu.Lock()
	b.textAsk = p
	b.mu.Unlock()

	// Nobody reads p.result between the two calls, which is the window.
	if !b.takeTextAnswer(msgFrom("7", "the release branch")) {
		t.Fatal("the first reply was not taken as the answer")
	}
	if b.takeTextAnswer(msgFrom("7", "also check the logs")) {
		t.Fatal("a second message was taken as an answer to a question that already has one")
	}
	if r := <-p.result; r.answer.Text != "the release branch" {
		t.Fatalf("the question got %+v, want the first reply", r)
	}
}

// OnPosed runs before the first question goes out, and not for a set that is
// cancelled while it waits behind another.
func TestBridgeAskReportsWhenAQuestionIsPosed(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	b, _ := askingBridge(t, conn, conn.inbound)

	first := askAsync(b, []core.UserQuestion{{Question: "Which branch?"}}, QuestionOpts{})
	conn.waitSends(t, 1)

	ctx, cancel := context.WithCancel(context.Background())
	posed := false
	second := make(chan error, 1)
	go func() {
		_, err := b.AskQuestions(ctx, []core.UserQuestion{{Question: "Which tag?"}}, QuestionOpts{OnPosed: func() { posed = true }})
		second <- err
	}()
	cancel()
	if err := <-second; !errors.Is(err, context.Canceled) {
		t.Fatalf("the waiting set returned %v, want the cancellation", err)
	}
	if posed {
		t.Fatal("OnPosed ran for a question that never went out")
	}

	conn.inbound <- msgFrom("7", "main")
	waitAsk(t, first)
	ran := false
	res := askAsync(b, []core.UserQuestion{{Question: "Which tag?"}}, QuestionOpts{OnPosed: func() { ran = true }})
	conn.waitSends(t, 2)
	conn.inbound <- msgFrom("7", "v1")
	if r := waitAsk(t, res); r.err != nil || !ran {
		t.Fatalf("AskQuestions = %v with OnPosed ran=%v, want an answer and the callback", r.err, ran)
	}
}

// "skip" releases the question without an answer. The caller learns that
// nothing was answered, and no declined answer is invented for the person.
func TestBridgeTextFloorSkipAnswersNothing(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	b, host := askingBridge(t, conn, conn.inbound)

	res := askAsync(b, []core.UserQuestion{{Question: "Which branch?"}}, QuestionOpts{})
	conn.waitSends(t, 1)
	conn.inbound <- msgFrom("7", "Skip")
	r := waitAsk(t, res)
	if !errors.Is(r.err, ErrAskSkipped) || r.answers != nil {
		t.Fatalf("AskQuestions = %+v, %v, want ErrAskSkipped and no answers", r.answers, r.err)
	}
	host.mu.Lock()
	n := len(host.submitted)
	host.mu.Unlock()
	if n != 1 {
		t.Fatalf("the host received %d prompts: the skip went on as a prompt", n)
	}
}

// A question with options and no free text leaves an unrelated message alone.
// That message is a prompt, and the question stays open for a real answer.
func TestBridgeTextFloorLeavesAnUnrelatedMessageAsAPrompt(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	b, host := askingBridge(t, conn, conn.inbound)

	res := askAsync(b, []core.UserQuestion{{Question: "Which port?", Options: []string{"8080", "9090"}}}, QuestionOpts{})
	conn.waitSends(t, 1)

	conn.inbound <- msgFrom("7", "also check the logs")
	if got := host.waitSubmitted(t, 2); got[1] != "also check the logs" {
		t.Fatalf("the host received %q", got)
	}
	conn.inbound <- msgFrom("7", "2")
	r := waitAsk(t, res)
	if r.err != nil || len(r.answers) != 1 || r.answers[0].Answer != "9090" {
		t.Fatalf("AskQuestions = %+v, %v, want 9090", r.answers, r.err)
	}
}

// An expired question returns ErrAskTimeout and no answers. ChatAsker turns
// the same expiry into a declined answer, and a bridge's caller must not get
// that: its question is open somewhere else too.
func TestBridgeAskTimeoutAnswersNothing(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	b, _ := askingBridge(t, conn, conn.inbound)

	res := askAsync(b, []core.UserQuestion{{Question: "Which branch?"}, {Question: "Which tag?"}},
		QuestionOpts{Timeout: 30 * time.Millisecond, TimeoutOutcome: "left for the app"})
	r := waitAsk(t, res)
	if !errors.Is(r.err, ErrAskTimeout) || r.answers != nil {
		t.Fatalf("AskQuestions = %+v, %v, want ErrAskTimeout and no answers", r.answers, r.err)
	}
	sent := conn.sends()
	if last := sent[len(sent)-1].Text; last != "left for the app" {
		t.Fatalf("the chat last read %q, want the timeout outcome", last)
	}
	for _, s := range sent {
		if strings.Contains(s.Text, "Which tag?") {
			t.Fatal("the second question was posed after the first one expired")
		}
	}
}

// askerConnector renders asks itself, and records what it was asked.
type askerConnector struct {
	*fakeConnector
	mu    sync.Mutex
	asked []Ask
	reply Answer
}

func (c *askerConnector) Ask(_ context.Context, a Ask) (Answer, error) {
	c.mu.Lock()
	c.asked = append(c.asked, a)
	c.mu.Unlock()
	return c.reply, nil
}

// A connector that renders asks gets the question as its own widget, limited
// to the paired owner. A question the widget cannot carry goes to the floor.
func TestBridgeAskUsesTheConnectorWidgetWhenItCan(t *testing.T) {
	conn := &askerConnector{fakeConnector: newFakeConnector(Capabilities{Asks: true}), reply: Answer{Key: "1", Attestation: AttestationAttested}}
	b, _ := askingBridge(t, conn, conn.inbound)

	answers, err := b.AskQuestions(context.Background(), []core.UserQuestion{{Question: "Which port?", Options: []string{"8080", "9090"}}}, QuestionOpts{})
	if err != nil || len(answers) != 1 || answers[0].Answer != "8080" {
		t.Fatalf("AskQuestions = %+v, %v, want 8080", answers, err)
	}
	conn.mu.Lock()
	asked := append([]Ask(nil), conn.asked...)
	conn.mu.Unlock()
	if len(asked) != 1 || asked[0].ChatID != "100" || len(asked[0].RestrictTo) != 1 || asked[0].RestrictTo[0] != "7" {
		t.Fatalf("the connector was asked %+v, want one ask in chat 100 limited to user 7", asked)
	}

	// Free text cannot round-trip through a one-key widget.
	res := askAsync(b, []core.UserQuestion{{Question: "Which branch?"}}, QuestionOpts{})
	conn.waitSends(t, 1)
	conn.inbound <- msgFrom("7", "main")
	if r := waitAsk(t, res); r.err != nil || r.answers[0].Answer != "main" {
		t.Fatalf("the free-text question = %+v, %v", r.answers, r.err)
	}
	conn.mu.Lock()
	n := len(conn.asked)
	conn.mu.Unlock()
	if n != 1 {
		t.Fatalf("the widget was asked %d times, and the free-text question must use the floor", n)
	}
}

// StopText replaces the reply to /stop, for a host whose stop is not a
// cancelled turn.
func TestBridgeStopTextReplacesTheDefaultReply(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	host := &fakeHost{}
	b := &Bridge{Connector: conn, Host: host, Pairing: pairedWith("7"), StopText: "paused the team."}
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)
	conn.inbound <- msgFrom("7", "/stop")
	if s := conn.waitSends(t, 1); s[0].Text != "paused the team." {
		t.Fatalf("the stop reply = %q", s[0].Text)
	}
}

// TextOnly drops an attachment before the host sees it, says so, and still
// forwards the text.
func TestBridgeTextOnlyDropsAttachments(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	host := &fakeHost{}
	b := &Bridge{Connector: conn, Host: host, Pairing: pairedWith("7"), TextOnly: true, TextOnlyNote: "text only"}
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)

	m := msgFrom("7", "see this")
	m.Images = []provider.ImageBlock{{}}
	m.Files = []FileAttachment{{Name: "notes.txt"}}
	conn.inbound <- m
	if got := host.waitSubmitted(t, 1); got[0] != "see this" {
		t.Fatalf("the host received %q, want the bare text with no attachment manifest", got[0])
	}
	if s := conn.waitSends(t, 1); s[0].Text != "text only" {
		t.Fatalf("the chat read %q, want the note", s[0].Text)
	}

	// An attachment with no text has nothing left to forward.
	only := msgFrom("7", "")
	only.Images = []provider.ImageBlock{{}}
	conn.inbound <- only
	conn.waitSends(t, 2)
	host.mu.Lock()
	n := len(host.submitted)
	host.mu.Unlock()
	if n != 1 {
		t.Fatalf("the host received %d prompts, and an empty one must not be forwarded", n)
	}
}

// Say names a speaker, and sends bare when there is none.
func TestBridgeSayNamesTheSpeaker(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	b, _ := askingBridge(t, conn, conn.inbound)
	b.Say("jev", "the build is green")
	b.Say("", "merged")
	s := conn.waitSends(t, 2)
	if s[0].Text != "jev: the build is green" || s[1].Text != "merged" {
		t.Fatalf("the chat read %q and %q", s[0].Text, s[1].Text)
	}
}
