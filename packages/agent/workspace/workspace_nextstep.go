package workspace

// The next-step surface — one ephemeral completion that proposes what the user
// might type next. Stage 1 of docs/proposals/idle-suggestions.md.
//
// Where a side chat is open → ask → close, this captures, asks and discards in
// a single call. Nothing is registered, so there is no snapshot to leak, no cap
// on open snapshots, and no id for a client to forget to close.
//
// It borrows the side chat's SHAPE on purpose: the session's own system prompt,
// its own transcript, its own per-turn ephemeral tail, and then one appended
// question. That prefix is the session's own, so the request reads the prompt
// cache rather than paying to re-read the conversation — which is the property
// this feature was asked for. Building it on the suggest surface instead would
// have lost that, because suggest replaces the system prompt and assembles a
// fresh message list.
//
// It records NOTHING. The question never enters the transcript, the answer
// never enters the transcript, and neither reaches the session file. The one
// thing it does leave behind is spend: streamText hands usage back and every
// caller is expected to book it, so an idle suggestion shows up in the session's
// cost like any other side-channel call.

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

const (
	// nextStepMaxTokens bounds the answer. One line needs very little, and the
	// cap is only safe at this size because the request turns reasoning OFF —
	// see the request below. Leave the two together.
	nextStepMaxTokens = 200

	// nextStepMaxRunes bounds what reaches the composer. The token cap governs
	// spend; this governs the promise, and they are not the same job — a model
	// that ignores "one line" must not be able to push a paragraph into the
	// user's editor.
	nextStepMaxRunes = 240
)

// NextStepTag names the block so the model can tell it apart from the user's
// own words, and so any future detector matches a stable string rather than
// translated prose. Outside the i18n.P call deliberately: a translation must
// not be able to move it.
const NextStepTag = "[next step]"

// nextStepBody is the ask, and its ORDER is the point rather than its wording.
// The question arrives in the user role on the session's own transcript, so to
// the model it is indistinguishable from the user typing — the same class of
// message #672 and the shell-result block are about. Left unframed it reads as
// "do the next step", which is the one thing this must not cause: the user is
// idle, has asked for nothing, and is owed no work.
//
// So the prohibition comes before the request it governs. That position is
// measured rather than assumed (0/20 to 20/20 on final answers,
// scripts/eval/README.md).
//
// The second paragraph is shaped by what the answer is FOR: it goes into the
// user's own composer, as ghost text they may send verbatim. So it has to be
// their next line, in their voice — not advice about their next line. "The
// smallest next step, never a plan" is the proposal's rule and it is here for a
// stated reason: a suggestion good enough to accept without reading is
// habit-forming, and a one-line step is one the user can still judge at a
// glance. A five-part programme belongs in a real turn they can push back on.
const nextStepBody = `Do not answer this in the conversation. Do not start any work. This comes from terva, on its own account, while the user is idle. The user did not send it and has not asked you for anything.

Reply with one short line and nothing else. Give the smallest next step. Write it as the user would type it to you, in their voice. No preamble, no explanation, no quotes, no markdown, no list. Never more than one line. If no next step is obvious, reply with nothing.`

// nextStepBodyOnDemand is the same ask when the user REQUESTED it (/nextstep).
//
// It exists because the body above states, as a fact the model must reason
// from, that the user "did not send it and has not asked you for anything".
// On this path that is false. The rest of the ask is the same job, so only the
// provenance sentence moves — and the prohibition still comes first, which is
// the property the eval measured rather than any particular wording of it.
//
// The second paragraph is duplicated verbatim rather than shared through a
// const, and that is deliberate twice over: the extractor resolves a keyed
// default only through string literals (a const operand does not fold), and a
// translator needs the whole ask in one catalog entry. What duplication risks
// is drift, so TestNextStepBodiesShareTheRequest pins the two halves equal.
//
// Held apart from the idle body rather than assembled from it for the same
// reason the idle one is left untouched: that string is measured, and an edit
// here must not be able to reach it.
const nextStepBodyOnDemand = `Do not answer this in the conversation. Do not start any work. The user asked terva what they might send next, and this is that question. They want the line, not the work.

Reply with one short line and nothing else. Give the smallest next step. Write it as the user would type it to you, in their voice. No preamble, no explanation, no quotes, no markdown, no list. Never more than one line. If no next step is obvious, reply with nothing.`

var _ ctrlproto.NextStepController = (*Workspace)(nil)

// SuggestNextStep runs one ephemeral completion against the session as it
// stands and returns a single line the user might send next, or "" for no
// suggestion.
//
// "" is an ordinary answer, not a failure: the ask invites the model to stay
// quiet when nothing is obvious, and an empty composer offer is exactly how
// that should surface. Callers show nothing and must not treat it as an error.
//
// p.OnDemand switches the ask to the variant for a suggestion the user asked
// for. It changes the question's framing and nothing else — same cap, same
// reasoning-off, same no-tools, same nothing-recorded.
func (w *Workspace) SuggestNextStep(ctx context.Context, sess string, p ctrlproto.NextStepParams) (ctrlproto.NextStepResult, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return ctrlproto.NextStepResult{}, err
	}
	ag := s.agent
	if ag == nil || ag.Client == nil {
		// No credential resolved for this session yet (a credential-less boot
		// before /login). Nothing to complete against.
		return ctrlproto.NextStepResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("not logged in"))
	}
	// Read once, and answer against THAT. Messages returns a copy, so a turn
	// landing while the completion is in flight cannot shift the ground under it.
	msgs := ag.Messages()
	if len(msgs) == 0 {
		// Nothing has been said, so there is no next step to name. Returning
		// before the client is the point: an empty session must not cost a
		// completion to be told it has nothing to suggest.
		return ctrlproto.NextStepResult{}, nil
	}
	_, model := s.currentModel()
	body := i18n.P("nextstep.ask", nextStepBody)
	if p.OnDemand {
		body = i18n.P("nextstep.ask_on_demand", nextStepBodyOnDemand)
	}
	msgs = append(msgs, nextStepMessage(NextStepTag+" "+body))

	out, usage, err := streamText(ctx, ag.Client, provider.Request{
		Model:     model,
		System:    ag.System,
		Messages:  msgs,
		MaxTokens: nextStepMaxTokens,
		// ContextPreview, not ContextProvider: the side-effect-free twin. A real
		// turn's provider records which lore entries fired, and a suggestion the
		// user never sees must not write that state on the session's behalf.
		EphemeralContext: ag.ContextPreview(),
		// Reasoning OFF, explicitly — ReasoningSet is what makes it beat the
		// model's own default rather than merely failing to ask for thinking.
		//
		// Two reasons, and the first is not a preference. On OpenAI's reasoning
		// path the cap is sent as max_completion_tokens, which counts reasoning
		// tokens too, so nextStepMaxTokens would be spent thinking and the call
		// would return an empty string — a feature that looks enabled and never
		// fires. (Anthropic self-corrects, raising the cap above its thinking
		// budget; nothing guarantees the next provider does either.) The second
		// is that the user is sitting in front of an idle composer: a suggestion
		// that arrives after a long think has missed the moment it was for.
		Reasoning:    "",
		ReasoningSet: true,
		// No tools. A suggestion is a sentence the user may choose to send; it
		// must not be able to act, and least of all while they are away from the
		// keyboard.
	})
	// Booked before the error check: a completion that failed still spent what
	// it sent, and dropping it would make idle suggestions free as far as the
	// session's cost is concerned.
	s.recordSideChannelUsage(usage)
	if err != nil {
		return ctrlproto.NextStepResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "suggest a next step: %v", err)
	}
	return ctrlproto.NextStepResult{Line: firstLine(out)}, nil
}

// firstLine reduces a completion to the one line that may reach the composer:
// the first line with anything on it, trimmed and bounded.
//
// The ask already says one line, so this is not the mechanism — it is what
// holds when the model does not comply. Taking the FIRST line rather than
// joining them is the proposal's "never a plan" rule made structural: a model
// that answers with a numbered programme contributes its opening step and
// nothing else.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) > nextStepMaxRunes {
			// Sliced on a rune boundary: this string is bound for a terminal
			// editor, and half a rune renders as garbage there.
			line = string([]rune(line)[:nextStepMaxRunes])
		}
		return line
	}
	return ""
}

func nextStepMessage(text string) provider.Message {
	return provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: text}},
		Time:    time.Now(),
	}
}
