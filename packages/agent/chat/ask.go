package chat

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// ChatAsker renders the agent loop's QUESTIONS as connector asks — the
// question analog of ChatConfirmer, over the same connproto `ask`/`answer`
// surface (stage G, feature "asks") with the same numbered-text floor for
// connectors that do not implement it.
//
// It is what makes `ask_user_question` and the prefix-change guard work on a
// chat host. Before it, core.Asker was nil everywhere except the workspace
// daemon, so a bot with a human on the other end and a working approval
// channel still answered "no interactive channel" to every question. The
// transport was never the gap: connproto has carried asks since stage G and
// ChatConfirmer has used them for approvals all along. Only the binding was
// missing.
//
// Deliberately NOT a second ask mechanism. Everything about where to ask, who
// may answer, how long to wait and what a lapsed question looks like is
// Loop.Ask's, so a question and an approval behave identically in a chat and
// there is one place to change either.
type ChatAsker struct {
	ctx context.Context
	// loop is resolved lazily because the daemon builds its agent BEFORE its
	// loop (the loop takes the agent), while SetAsker must run BEFORE NewAgent
	// for the agent to pick the channel up. A getter breaks that cycle without
	// a mutable field anyone could re-point mid-session.
	loop func() *Loop
	sem  chan struct{}

	// Timeout bounds one question; 0 means DefaultAskTimeout. Separate from
	// the approval timeout because the stakes differ — a lapsed approval
	// denies a tool call, a lapsed question just means the agent decides for
	// itself — even though both currently use the same default.
	Timeout time.Duration
}

// NewChatAsker builds the asker. ctx is the daemon's lifetime, used only for
// sends that must survive a turn being torn down.
func NewChatAsker(ctx context.Context, loop func() *Loop) *ChatAsker {
	return &ChatAsker{ctx: ctx, loop: loop, sem: make(chan struct{}, 1)}
}

var _ core.Asker = (*ChatAsker)(nil)

// lock takes the one-ask-at-a-time slot without becoming unstoppable while
// waiting: an abandoned turn's question must not make a live turn's question
// wait out the whole timeout. Mirrors ChatConfirmer.lock exactly.
func (a *ChatAsker) lock(ctx context.Context) bool {
	select {
	case a.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (a *ChatAsker) unlock() { <-a.sem }

// Ask implements core.Asker over the connector's ask surface.
//
// A chat is a LINEAR medium, so a set is posed one question at a time in
// order. That is slower than the TUI's tab strip but it is the only shape a
// chat has; the alternative — one message carrying eight questions — cannot
// collect eight separate answers.
//
// The no-answer contract is the load-bearing part, and it is deliberately
// different from ChatConfirmer's. An approval that expires must FAIL CLOSED
// (deny the tool call). A QUESTION that expires is a dismissal: core.Asker
// defines that as one Declined answer per question, not an error. So a bot
// whose owner never replies gets a model that proceeds on its own judgement,
// exactly like a TUI user closing the dialog — it does not hang, and it does
// not surface a failure the model would be tempted to retry into a loop.
func (a *ChatAsker) Ask(ctx context.Context, qs []core.UserQuestion) ([]core.UserAnswer, error) {
	if len(qs) == 0 {
		return nil, nil
	}
	l := a.loop()
	if l == nil {
		return nil, errors.New("no chat connector is running to ask in")
	}
	if !a.lock(ctx) {
		return nil, ctx.Err()
	}
	defer a.unlock()

	chatID, replyTo := l.AskTarget()
	if chatID == "" {
		// Not a decline: nobody was asked. The model should read this the way
		// it reads a nil Asker — no channel — rather than as a refusal.
		return nil, errors.New("no paired chat to ask in")
	}
	var restrict []string
	if owner := l.OwnerID(); owner != "" {
		restrict = []string{owner}
	}

	out := make([]core.UserAnswer, len(qs))
	for i, q := range qs {
		opts := make([]AskOption, len(q.Options))
		for j, o := range q.Options {
			// Key is the 1-based position so it agrees with the number the
			// text fallback prints; matchAskOption accepts the number, the
			// key or the label, so every reply shape lands on the same key.
			opts[j] = AskOption{Key: strconv.Itoa(j + 1), Label: o}
		}

		ans, err := l.Ask(ctx, Ask{
			ChatID: chatID, ReplyTo: replyTo,
			Text:        askText(q, i, len(qs)),
			Options:     opts,
			MultiSelect: q.MultiSelect,
			// A question with no options at all is free-form by definition
			// (core.UserQuestion), so it needs the written-in path whether or
			// not the model set the flag — otherwise it would be posed with
			// nothing to answer it with.
			AllowCustom:    q.AllowCustom || len(q.Options) == 0,
			Timeout:        a.Timeout,
			RestrictTo:     restrict,
			TimeoutOutcome: i18n.T("no answer — the agent will decide for itself"),
		})
		switch {
		case errors.Is(err, ErrAskTimeout):
			// Nobody is there. Decline this question AND every one after it
			// rather than posting seven more into a chat already proven
			// silent — the remaining asks would each burn their own timeout
			// and stall the turn for minutes.
			note := i18n.T("no answer within the time limit")
			for k := i; k < len(qs); k++ {
				out[k] = core.UserAnswer{Declined: true, Note: note}
			}
			return out, nil
		case err != nil:
			// Cancellation included: the contract says surface ctx.Err() so
			// the tool reports a cancellation rather than a bogus answer.
			return nil, err
		}
		out[i] = answerFor(q, ans)
	}
	return out, nil
}

// askText renders one question, with its position when there are several so a
// reader in a linear chat knows how many more are coming.
//
// It carries no note about what the channel cannot do, because there is no
// longer anything it cannot do: the floor accepts several choices and a
// written-in answer alike, and Loop.Ask says which shapes THIS question takes
// in its reply instruction. Two places describing the same constraint is how
// they end up disagreeing.
func askText(q core.UserQuestion, i, n int) string {
	var b strings.Builder
	if n > 1 {
		fmt.Fprintf(&b, "%s\n", i18n.T("question %d of %d", i+1, n))
	}
	b.WriteString(q.Question)
	return b.String()
}

// answerFor maps a connector answer back onto the question's own option text.
func answerFor(q core.UserQuestion, ans Answer) core.UserAnswer {
	// A written-in answer stands on its own: it is not an option, so it is
	// reported as the answer with nothing in Answers. Checked FIRST because a
	// custom reply carries no key at all.
	if ans.Text != "" {
		return core.UserAnswer{Answer: ans.Text}
	}

	keys := ans.Keys
	if len(keys) == 0 && ans.Key != "" {
		keys = []string{ans.Key}
	}
	choices := make([]string, 0, len(keys))
	for _, k := range keys {
		idx, err := strconv.Atoi(strings.TrimSpace(k))
		if err != nil || idx < 1 || idx > len(q.Options) {
			// A connector echoed a key we never offered. Treat it as a decline
			// with the reason attached rather than guessing which option was
			// meant — a wrong guess here is a decision the user did not make.
			return core.UserAnswer{Declined: true, Note: i18n.T(
				"the connector returned an answer that matched none of the offered choices")}
		}
		choices = append(choices, q.Options[idx-1])
	}
	if len(choices) == 0 {
		return core.UserAnswer{Declined: true, Note: i18n.T(
			"the connector returned an answer that matched none of the offered choices")}
	}

	out := core.UserAnswer{Answer: choices[0]}
	if q.MultiSelect {
		// Fill BOTH fields on a multi-select question even when one choice came
		// back: Chosen() reads whichever the shape implies, and a caller reading
		// Answers must not find nil and conclude the user picked nothing.
		out.Answers = choices
	}
	return out
}
