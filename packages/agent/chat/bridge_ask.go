package chat

// Questions over the Bridge.
//
// The Loop has asked in a chat since connector protocol v2. The Bridge had no
// reason to: it mirrors one session, and that session's own client shows its
// cards. A bridge bound to a talkoot is different. The person on the other end
// may have no client open at all, so a member's question has to reach the chat
// or it reaches nobody.

import (
	"context"
	"errors"
	"strings"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// ErrAskSkipped reports that the person declined to answer in chat. Nothing was
// answered, and the question is still open wherever else it is shown.
var ErrAskSkipped = errors.New("the question was skipped in chat")

// QuestionOpts shapes how AskQuestions poses a set.
type QuestionOpts struct {
	// From names who asks. It leads each question's text, because a chat that
	// carries a whole team cannot otherwise say whose question this is.
	From string
	// Timeout bounds the wait for each question. 0 means DefaultAskTimeout.
	Timeout time.Duration
	// TimeoutOutcome is what the chat reads when a question expires.
	TimeoutOutcome string
	// OnPosed runs once, just before the first question goes out. AskQuestions
	// serializes its callers, so a set can wait a long time behind another.
	// A caller whose question is cancelled needs to know whether the person
	// ever saw it.
	OnPosed func()
}

// bridgeTextAsk is the one text-floor question waiting on the owner.
type bridgeTextAsk struct {
	ask    Ask
	result chan bridgeTextResult // buffered(1); the first reply wins
}

type bridgeTextResult struct {
	answer  Answer
	skipped bool
}

// Say sends text to the paired chat under a speaker's name. An empty speaker
// sends it bare, as OnAssistantText does.
func (b *Bridge) Say(speaker, text string) {
	prefix := ""
	if speaker != "" {
		prefix = speaker + ": "
	}
	b.sendToPaired(text, prefix)
}

// owner returns the paired user's id, tracking a claim made while running.
func (b *Bridge) owner() string {
	b.mu.Lock()
	g := b.gate
	owner := b.Pairing.AllowedUserID
	b.mu.Unlock()
	if g != nil {
		g.mu.Lock()
		owner = g.pairing.AllowedUserID
		g.mu.Unlock()
	}
	return owner
}

// AskQuestions poses a question set in the paired chat, one question at a
// time, and returns one answer for each.
//
// 🔑 It never answers for the person, and that is the difference from
// ChatAsker. ChatAsker turns an expired question into a declined answer so a
// bot's model can go on alone. This returns an error and no answers. Its
// caller holds a question that is also open in another client, and a decline
// invented here would settle it there.
//
// The errors are ErrAskTimeout, ErrAskSkipped, the context's own error, and
// whatever the connector returned.
func (b *Bridge) AskQuestions(ctx context.Context, qs []core.UserQuestion, opts QuestionOpts) ([]core.UserAnswer, error) {
	if len(qs) == 0 {
		return nil, nil
	}
	b.askOnce.Do(func() { b.askSem = make(chan struct{}, 1) })
	select {
	case b.askSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-b.askSem }()

	chatID, err := b.pairedChat()
	if err != nil {
		return nil, err
	}
	var restrict []string
	if owner := b.owner(); owner != "" {
		restrict = []string{owner}
	}
	if opts.OnPosed != nil {
		opts.OnPosed()
	}

	out := make([]core.UserAnswer, len(qs))
	for i, q := range qs {
		ask := questionAsk(q, i, len(qs))
		if opts.From != "" {
			ask.Text = i18n.T("%s asks:", opts.From) + "\n" + ask.Text
		}
		ask.ChatID = chatID
		ask.RestrictTo = restrict
		ask.Timeout = opts.Timeout
		ask.TimeoutOutcome = opts.TimeoutOutcome
		ans, err := b.ask(ctx, ask)
		if err != nil {
			return nil, err
		}
		out[i] = answerFor(q, ans)
	}
	return out, nil
}

// ask poses one question. A connector that renders asks gets its own widget
// when the widget can carry the question, and everything else gets the text
// floor, by the same rule Loop.Ask applies.
func (b *Bridge) ask(ctx context.Context, a Ask) (Answer, error) {
	if asker, ok := b.Connector.(Asker); ok && b.Connector.Capabilities().Asks && widgetCanExpress(a) {
		return asker.Ask(ctx, a)
	}
	return b.textFloorAsk(ctx, a)
}

// textFloorAsk poses the question as numbered text and waits for the owner's
// next message.
//
// ⚠️ A free-text question takes ANY next message as its answer, and a
// talkoot's question can stay open for a long time. So this floor says so in
// the question, and it accepts "skip" as a way out. Without those two, a person
// who writes to the team about something else would answer a question they
// never read.
func (b *Bridge) textFloorAsk(ctx context.Context, a Ask) (Answer, error) {
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = DefaultAskTimeout
	}
	p := &bridgeTextAsk{ask: a, result: make(chan bridgeTextResult, 1)}
	b.mu.Lock()
	if b.textAsk != nil {
		b.mu.Unlock()
		return Answer{}, errors.New("another question is already waiting for an answer")
	}
	b.textAsk = p
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		if b.textAsk == p {
			b.textAsk = nil
		}
		b.mu.Unlock()
	}()

	text := renderTextAsk(a) + "\n" +
		i18n.T("your next message here answers this question. send \"skip\" to leave it unanswered.")
	if err := b.Connector.Send(ctx, Outgoing{ChatID: a.ChatID, ReplyTo: a.ReplyTo, Text: text}); err != nil {
		return Answer{}, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-p.result:
		if r.skipped {
			return Answer{}, ErrAskSkipped
		}
		return r.answer, nil
	case <-timer.C:
		if a.TimeoutOutcome != "" {
			_ = b.Connector.Send(context.Background(), Outgoing{ChatID: a.ChatID, Text: a.TimeoutOutcome})
		}
		return Answer{}, ErrAskTimeout
	case <-ctx.Done():
		return Answer{}, ctx.Err()
	}
}

// takeTextAnswer gives an open text-floor question first claim on m. It
// reports whether m was consumed.
//
// 🔑 An accepted reply detaches the question under the same lock that found
// it. The waiter clears the question only when it wakes, and a second message
// that arrived before then would otherwise be taken as an answer too, and
// then dropped, because the result holds one reply.
func (b *Bridge) takeTextAnswer(m Message) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.textAsk
	if p == nil || m.ChatID != p.ask.ChatID {
		return false
	}
	if len(p.ask.RestrictTo) > 0 && !containsUserID(p.ask.RestrictTo, m.UserID) {
		return false
	}
	var r bridgeTextResult
	switch strings.ToLower(strings.TrimSpace(m.Text)) {
	case "skip", "/skip":
		r.skipped = true
	default:
		ans, ok := parseAskReply(p.ask, m.Text)
		if !ok {
			return false
		}
		ans.UserID, ans.Username = m.UserID, m.Username
		ans.Attestation = AttestationBestEffort
		r.answer = ans
	}
	b.textAsk = nil
	p.result <- r // buffered(1), and the detach above leaves one sender
	return true
}
