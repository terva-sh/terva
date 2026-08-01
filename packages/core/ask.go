package core

import (
	"context"
	"strings"
)

// UserQuestion is one structured clarification the agent asks the user
// mid-turn. Options, when non-empty, are presented as a multiple choice;
// AllowCustom additionally lets the user type a free-form answer. With no
// options the question is always free-form.
type UserQuestion struct {
	Question string
	// Slug is an optional 1-3 word name for the question, for front ends
	// that must refer to it in less room than the question itself takes —
	// the TUI's tab strip is one chip per question, and a chip can hold a
	// name or a number but not a sentence. Front ends fall back to the
	// position when it is empty, so it is never required and never the
	// only way to tell two questions apart. [SanitizeSlug] bounds it.
	Slug        string
	Options     []string
	AllowCustom bool
}

// SanitizeSlug bounds a model-supplied slug to something a chip can
// hold: one line, single-spaced, at most three words and 24 columns.
// Anything longer is dropped rather than clipped — half a name in a tab
// strip is worse than a number, which at least says where you are.
func SanitizeSlug(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	if len(strings.Split(s, " ")) > 3 || len([]rune(s)) > 24 {
		return ""
	}
	return s
}

// UserAnswer is the user's reply to one question. Answer holds the chosen
// option or the typed text; Declined is true when the user dismissed the
// prompt without answering (the agent should proceed with its best
// judgment, not block).
//
// Note is an addendum attached to a CHOSEN option — "this one, but watch
// the connection limits" — and is deliberately a separate field rather
// than text folded into Answer. Folded in, the reader can no longer tell
// "the user chose Postgres" from "the user wrote a sentence containing
// the word Postgres", which is exactly the distinction that makes the
// choice actionable. A front end that has no way to attach one simply
// leaves it empty; nothing downstream requires it.
type UserAnswer struct {
	Answer   string
	Note     string
	Declined bool
}

// MaxAskQuestions caps one ask. A set is meant to be the handful of
// decisions blocking the same piece of work — past that a front end's
// tab strip stops being readable and the user is filling in a form, not
// answering a question. Producers of large sets (raati's inquiry docket)
// split into several asks rather than exceeding it.
const MaxAskQuestions = 8

// Asker surfaces a SET of questions as one interruption and blocks until
// the user submits or ctx is cancelled. It is the question analog of
// Confirmer.
//
// A set, not a single question, because tool calls run strictly one at a
// time: an agent with three things to clarify would otherwise stall the
// turn three separate times, and the user would answer each without
// seeing the others. One call, one dialog, one submit — and a front end
// stays free to present the set as tabs, a stack, or a wizard.
//
// Implementations must return exactly one answer per question, in the
// order asked, so a caller can index answers against its own questions.
// A dismissal is len(qs) answers with Declined set, not a short slice and
// not an error. Use [AskOne] for the common single-question case.
//
// Implementations must honor ctx: on cancellation return ctx.Err() so the
// tool surfaces a cancellation rather than a bogus answer.
//
// Exactly one host binds this today — the workspace daemon (webAsker,
// re-bound on every tool rebuild via bindResolvedChannels); every other
// mode, ACP included, leaves it nil, and the ask_user_question tool then
// returns a model-readable "no channel" result rather than hanging.
type Asker interface {
	Ask(ctx context.Context, qs []UserQuestion) ([]UserAnswer, error)
}

// AskOne poses a single question and returns the single answer. Most
// callers inside core want exactly this, and routing them through one
// helper keeps the short-slice guard — which a misbehaving front end can
// still trip — in one place instead of at every call site.
func AskOne(ctx context.Context, a Asker, q UserQuestion) (UserAnswer, error) {
	ans, err := a.Ask(ctx, []UserQuestion{q})
	if err != nil {
		return UserAnswer{}, err
	}
	if len(ans) == 0 {
		return UserAnswer{Declined: true}, nil
	}
	return ans[0], nil
}

// PadAnswers returns answers sized to n, filling any shortfall with
// declines and dropping any excess. The Asker contract requires one
// answer per question; this is the guard for the front end that does not
// keep it, so an indexing caller reads a decline rather than panicking.
func PadAnswers(ans []UserAnswer, n int) []UserAnswer {
	if n < 0 {
		n = 0
	}
	if len(ans) >= n {
		return ans[:n]
	}
	out := make([]UserAnswer, n)
	copy(out, ans)
	for i := len(ans); i < n; i++ {
		out[i] = UserAnswer{Declined: true}
	}
	return out
}
