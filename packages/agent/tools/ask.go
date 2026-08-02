package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// AskUserTool asks the user a structured clarifying question mid-turn and
// resumes with their answer. Its only effect is the prompt, so the
// permission policy permits it in every mode (see core.AuthUserInteraction).
//
// Asker is the front-end channel (set after the registry is built, like
// the confirm gate's Confirmer). Only the workspace host sets it; every
// other mode — print/json/rpc/swarm, and today ACP and the SDK too —
// leaves it nil, and the tool then returns a model-readable result
// telling the agent to proceed on its best judgment rather than blocking.
type AskUserTool struct {
	Asker core.Asker
}

// askQuestion is one question within a call. The singular top-level
// fields on askArgs are the same shape, kept so a model that emits the
// original one-question form still works.
type askQuestion struct {
	Question string   `json:"question"`
	Slug     string   `json:"slug,omitempty"`
	Options  []string `json:"options,omitempty"`

	MultiSelect bool `json:"multi_select,omitempty"`
	// AllowCustom is a POINTER so nil ("the model did not say") stays
	// distinguishable from false ("the model closed the set"). See
	// [allowsCustom], which is the only thing that reads it.
	AllowCustom *bool `json:"allow_custom,omitempty"`
}

type askArgs struct {
	Question string   `json:"question"`
	Slug     string   `json:"slug,omitempty"`
	Options  []string `json:"options,omitempty"`

	MultiSelect bool          `json:"multi_select,omitempty"`
	AllowCustom *bool         `json:"allow_custom,omitempty"`
	Questions   []askQuestion `json:"questions,omitempty"`
}

// slugDesc is shared by both shapes so the two cannot drift into
// describing the same field differently.
// The schema is a hand-written JSON literal, so a description may not
// contain a double quote — it lands inside a JSON string and invalidates
// the whole document. Use single quotes.
const slugDesc = `Optional 1-3 word name for this question ('auth method', 'rollout order'), used where a front end has room for a label but not the whole question — the terminal shows it on the question's tab. Name the DECISION, not the answer. Optional, dropped if longer than 3 words or 24 characters, and never a substitute for a clear question.`

// multiSelectDesc is shared by both shapes for the same reason slugDesc is.
// Phrased around whether the options are mutually EXCLUSIVE rather than around
// how many the user may tick: that is the property the model actually knows,
// and it is the one nothing else can recover afterwards.
const multiSelectDesc = `Set true when the options are NOT mutually exclusive and the user may pick any number of them ('which of these should I enable?'). Leave false — the default — when exactly one answer makes sense ('which of these should I use?'). Only you can say which it is; nothing infers it from the option text.`

// optionsDesc is shared by both shapes, and says the thing the old wording
// left to inference.
//
// It used to read "Optional multiple-choice answers. Omit for a free-form
// question." — permission-shaped, and silent on the case that actually goes
// wrong. Across the 288 recorded questions the model supplies options on 242
// of them, 84%, and puts real rationale in them (median option 99 characters,
// longest 378); of the 46 that arrive WITHOUT options, 34 enumerate their
// choices in the question prose instead — '(a) … (b) … (c) …' — leaving the
// user a text box under a question that just listed its own answers.
//
// So the description names that case, and questions() hands one back rather
// than showing it. Long options are not the reason: the terminal wraps a
// 380-character option cleanly with a hanging indent. Nor is free text the
// co-equal alternative the old wording implied — only 8 of the 288 are
// genuinely open questions.
const optionsDesc = `Multiple-choice answers. If you are about to write '(a) … (b) …', 'Options:', or any enumerated list of choices INTO the question text, those are the options — put them here instead, one entry each, and keep the question itself to what is being decided. An option may be a full sentence with its rationale; the interface wraps it. A confirmation is a question too: 'Confirm as canon?' has options like 'confirm' and 'revise', and offering them turns four keystrokes into one. Omit only for a genuinely open question with no candidate answers ('what should this be called?').`

// allowCustomDesc is shared by both shapes, and is phrased around CLOSING the
// question because closing it is now the only thing saying this changes.
//
// It used to read "When options are given, also let the user type their own
// answer as well as picking from them." — an opt-in, and the model opted in on
// 185 of the 242 recorded questions it gave options to. Three quarters of the
// time the field was spent asking for the common case; the remaining quarter
// reads as inattention rather than a deliberately closed set, and each one of
// those left the user with no way to say the options had missed something.
const allowCustomDesc = `Whether the user may type an answer of their own INSTEAD OF picking one of the options. Defaults to true, which is nearly always what you want — options are your best guesses, not the full space of answers. Set it false only when the options genuinely are the complete set and anything outside them would be meaningless: a fixed enum, the files that actually exist, the three branches there are. Closing a set that only looks complete is the expensive mistake — the user picks the nearest wrong answer and cannot tell you it was wrong.`

const askSchema = `{"type":"object","properties":{` +
	`"question":{"type":"string","description":"The question to ask the user. Be specific. Use this for a single question; use 'questions' to ask several at once."},` +
	`"slug":{"type":"string","description":"` + slugDesc + `"},` +
	`"options":{"type":"array","items":{"type":"string"},"description":"` + optionsDesc + `"},` +
	`"multi_select":{"type":"boolean","description":"` + multiSelectDesc + `"},` +
	`"allow_custom":{"type":"boolean","description":"` + allowCustomDesc + `"},` +
	`"questions":{"type":"array","maxItems":8,"description":"Ask several related questions in ONE interruption instead of stalling the turn once per question. The user sees them together and answers at their own pace before submitting. Prefer this whenever more than one thing is unclear.","items":{"type":"object","properties":{` +
	`"question":{"type":"string","description":"The question to ask. Be specific."},` +
	`"slug":{"type":"string","description":"` + slugDesc + ` Most useful here: a set is navigated by tab, and named tabs say what is behind each one."},` +
	`"options":{"type":"array","items":{"type":"string"},"description":"` + optionsDesc + `"},` +
	`"multi_select":{"type":"boolean","description":"` + multiSelectDesc + `"},` +
	`"allow_custom":{"type":"boolean","description":"` + allowCustomDesc + `"}` +
	`},"required":["question"]}}` +
	`}}`

func (t *AskUserTool) Name() string { return "ask_user_question" }
func (t *AskUserTool) Description() string {
	return "Ask the user clarifying questions and wait for their answers, instead of guessing when requirements are ambiguous. Pass 'questions' to ask several at once — they are shown together and answered in one pass, which costs the user one interruption instead of several. Optionally offer multiple-choice options. Use sparingly, for decisions you genuinely cannot make yourself; it pauses the turn for a human."
}
func (t *AskUserTool) Schema() json.RawMessage { return json.RawMessage(askSchema) }

func (t *AskUserTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var a askArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	qs, err := a.questions()
	if err != nil {
		return core.ToolResult{}, err
	}

	// No interactive channel (print/json/rpc/swarm). Don't block or
	// fail; tell the model to proceed on its best judgment and state the
	// assumption. Mirrors the confirm gate's headless refusal.
	if t.Asker == nil {
		return core.ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: "No interactive channel is available to ask the user in this mode. Proceed with your best judgment and state the assumption you made; do not wait for an answer."}},
			Details: map[string]any{"asked": false, "reason": "no_channel"},
		}, nil
	}

	answers, err := t.Asker.Ask(ctx, qs)
	if err != nil {
		// Context cancelled or front-end failure — surface as an error
		// result so the model knows the question was not answered.
		return core.ToolResult{}, fmt.Errorf("ask_user_question: %w", err)
	}
	answers = core.PadAnswers(answers, len(qs))
	return askResult(qs, answers), nil
}

// questions normalises the two accepted shapes into one list: the
// singular top-level fields, the plural questions array, or both (a
// model that fills in each is taken at its word and gets the singular
// one first, since that is the field it treated as primary).
func (a askArgs) questions() ([]core.UserQuestion, error) {
	var qs []core.UserQuestion
	if strings.TrimSpace(a.Question) != "" {
		qs = append(qs, core.UserQuestion{
			Question: a.Question, Slug: core.SanitizeSlug(a.Slug),
			Options: a.Options, MultiSelect: a.MultiSelect, AllowCustom: allowsCustom(a.AllowCustom),
		})
	}
	for _, q := range a.Questions {
		if strings.TrimSpace(q.Question) == "" {
			return nil, fmt.Errorf("every entry in questions needs a question")
		}
		qs = append(qs, core.UserQuestion{
			Question: q.Question, Slug: core.SanitizeSlug(q.Slug),
			Options: q.Options, MultiSelect: q.MultiSelect, AllowCustom: allowsCustom(q.AllowCustom),
		})
	}
	if len(qs) == 0 {
		return nil, fmt.Errorf("question is required")
	}
	if len(qs) > core.MaxAskQuestions {
		return nil, fmt.Errorf("too many questions in one call (%d, max %d) — ask the most blocking ones now and the rest after you have those answers", len(qs), core.MaxAskQuestions)
	}
	for i, q := range qs {
		if len(q.Options) > 0 || !enumeratesItsOwnOptions(q.Question) {
			continue
		}
		where := "the question"
		if len(qs) > 1 {
			where = fmt.Sprintf("question %d", i+1)
		}
		// Handed back rather than shown. A tool that says no is one a model
		// can recover from, and the alternative is worse than a retry: the
		// user gets a text box under a question that just listed its own
		// answers, then types back one of the choices the model already had.
		return nil, fmt.Errorf("%s lists its choices in the question text but options is empty — put each choice in options instead (one entry each, a full sentence is fine) and leave the question to what is being decided", where)
	}
	return qs, nil
}

// allowsCustom resolves the tri-state allow_custom into the flag the front
// ends take: a model that says nothing gets the escape hatch, and only a model
// that says false loses it.
//
// The two failures are not the same size. A text box under a genuinely closed
// set is clutter. Its absence under a set that missed a case leaves the user
// picking the nearest wrong answer with no way to say it was wrong — and the
// model then acts on an answer the user never meant, with nothing anywhere
// recording that it was a compromise.
//
// This is the ONLY reader of the field, so the pointer never escapes the tool:
// core.UserQuestion carries the resolved bool, and every surface downstream —
// the terminal dialog, ctrlproto, the web client — keeps reading exactly what
// it read before. In the terminal the whole visible cost is one more row,
// "Type my own answer…", on questions that previously had none.
//
// One surface does read it as a request rather than a permission: the chat
// connector cannot carry written-in text at all, so [chat.askText] adds a line
// saying so whenever this is true, and it now says so on every optioned
// question instead of on the three quarters the model used to flag. The line
// stays true — that wire really cannot take one — but if it ever wants to mean
// "the model expected free text" again, this is the resolution that stopped
// carrying that, and the pointer above is where the distinction still lives.
func allowsCustom(p *bool) bool { return p == nil || *p }

// enumeratesItsOwnOptions reports whether a question has already written out
// the choices that belong in the options array.
//
// Tuned against every ask recorded on this machine — 288 questions — where it
// fires on 34 of the 46 that arrived without options and on none of the ones
// that are genuinely open ("what do the queen's eggs become?", "order name
// under Insectoid"). Of the 12 it leaves, 8 are those open questions and 4 are
// confirmations ("… Confirm as canon?"), which have no enumeration to detect:
// the schema's wording is what has to reach those, since a rule strict enough
// to catch them would also catch an open question ending in a question mark.
//
// A marker must be preceded by whitespace or punctuation so a parenthetical
// aside — "(see the comparison above)", "(a note on naming)" — cannot trip it,
// and followed by real text so a bare "(a)" cannot either.
//
// TWO markers are required, because one is a reference and not a list: "is (b)
// still the plan?" points at a choice offered earlier, and blocking it would
// stop the model asking a perfectly good follow-up. An explicit "Options:"
// lead-in needs no second marker — it has already said what it is.
var (
	optionMarker   = regexp.MustCompile(`(?m)(?:^|[\s;:—-])\((?:[a-eA-E]|[1-9])\)\s+\S`)
	optionLeadIn   = regexp.MustCompile(`(?m)(?:^|[\s.;—-])(?i:options:)\s`)
	minEnumMarkers = 2
)

func enumeratesItsOwnOptions(question string) bool {
	if optionLeadIn.MatchString(question) {
		return true
	}
	return len(optionMarker.FindAllStringIndex(question, minEnumMarkers)) >= minEnumMarkers
}

// renderChoice writes what the user picked in a form the model cannot misread.
//
// Every multi-select answer is rendered with its relationship named, because
// all three cases are ambiguous without it:
//
//   - "A, B" alone cannot be told from ONE option whose text contains a comma,
//     and the difference decides whether the model does one thing or two.
//   - One tick out of five is not the same fact as one option out of five. The
//     user could have taken more and chose not to, which is a decision, and the
//     bare option text throws it away.
//   - No ticks at all is an ANSWER — "none of these" — not an absence of one.
//     Rendered as empty it reads as a front-end bug, and the model would be
//     right to distrust it.
//
// A declined question never reaches here; the caller renders that. A
// single-select answer keeps its plain shape, which is what the model has been
// reading all along.
func renderChoice(q core.UserQuestion, a core.UserAnswer) string {
	if !q.MultiSelect {
		return a.Answer
	}
	switch chosen := a.Chosen(); len(chosen) {
	case 0:
		return "none of the options"
	case 1:
		return "only: " + chosen[0]
	default:
		return "all of: " + strings.Join(chosen, ", ")
	}
}

// askResult renders the answers for the model. A single question keeps
// its original one-line shape and Details key, because that is what the
// model has been reading all along and a set is the new case, not the
// common one. A set is numbered against the questions so the model can
// tell which answer belongs to which — position alone is too easy to
// misread when one of them was declined.
func askResult(qs []core.UserQuestion, answers []core.UserAnswer) core.ToolResult {
	if len(qs) == 1 {
		if answers[0].Declined {
			return core.ToolResult{
				Content: []provider.Content{provider.TextBlock{Text: "The user declined to answer. Proceed with your best judgment and state the assumption you made."}},
				Details: map[string]any{"asked": true, "declined": true},
			}
		}
		text := "User answered: " + renderChoice(qs[0], answers[0])
		details := map[string]any{"asked": true, "answer": answers[0].Answer}
		if chosen := answers[0].Chosen(); qs[0].MultiSelect {
			details["answers"] = chosen
		}
		// The note is rendered on its own line, and labelled. Appended to
		// the answer it would read as part of the option the user picked,
		// which is the one reading it must not have: the choice is what to
		// act on, the note is what to account for while doing it.
		if note := answers[0].Note; note != "" {
			text += "\nTheir note on that answer: " + note
			details["note"] = note
		}
		return core.ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: text}},
			Details: details,
		}
	}

	var sb strings.Builder
	declined := 0
	details := make([]map[string]any, 0, len(qs))
	sb.WriteString("User answered:")
	for i, q := range qs {
		sb.WriteString(fmt.Sprintf("\n%d. %s\n   ", i+1, q.Question))
		if answers[i].Declined {
			declined++
			sb.WriteString("(declined)")
		} else {
			sb.WriteString(renderChoice(q, answers[i]))
			if answers[i].Note != "" {
				sb.WriteString("\n   note: " + answers[i].Note)
			}
		}
		entry := map[string]any{
			"question": q.Question,
			"answer":   answers[i].Answer,
			"declined": answers[i].Declined,
		}
		if q.MultiSelect {
			entry["answers"] = answers[i].Chosen()
		}
		if answers[i].Note != "" {
			entry["note"] = answers[i].Note
		}
		details = append(details, entry)
	}
	if declined > 0 {
		sb.WriteString("\n\nFor each question marked (declined), proceed with your best judgment and state the assumption you made.")
	}
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: sb.String()}},
		Details: map[string]any{"asked": true, "answers": details, "declined": declined == len(qs)},
	}
}
