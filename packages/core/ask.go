package core

import "context"

// UserQuestion is a structured clarification the agent asks the user
// mid-turn. Options, when non-empty, are presented as a multiple choice;
// AllowCustom additionally lets the user type a free-form answer. With no
// options the question is always free-form.
type UserQuestion struct {
	Question    string
	Options     []string
	AllowCustom bool
}

// UserAnswer is the user's reply. Answer holds the chosen option or the
// typed text; Declined is true when the user dismissed the prompt without
// answering (the agent should proceed with its best judgment, not block).
type UserAnswer struct {
	Answer   string
	Declined bool
}

// Asker surfaces a UserQuestion to the user and blocks until they reply
// or ctx is cancelled. It is the question analog of Confirmer. Exactly one
// host binds it today — the workspace daemon (webAsker, re-bound on every
// tool rebuild via bindResolvedChannels); every other mode, ACP included,
// leaves it nil, and the ask_user_question tool then returns a
// model-readable "no channel" result rather than hanging.
//
// Implementations must honor ctx: on cancellation return ctx.Err() so the
// tool surfaces a cancellation rather than a bogus answer.
type Asker interface {
	Ask(ctx context.Context, q UserQuestion) (UserAnswer, error)
}
