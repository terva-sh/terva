package tools

import (
	"context"
	"encoding/json"
	"fmt"
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
	Question    string   `json:"question"`
	Slug        string   `json:"slug,omitempty"`
	Options     []string `json:"options,omitempty"`
	AllowCustom bool     `json:"allow_custom,omitempty"`
}

type askArgs struct {
	Question    string        `json:"question"`
	Slug        string        `json:"slug,omitempty"`
	Options     []string      `json:"options,omitempty"`
	AllowCustom bool          `json:"allow_custom,omitempty"`
	Questions   []askQuestion `json:"questions,omitempty"`
}

// slugDesc is shared by both shapes so the two cannot drift into
// describing the same field differently.
// The schema is a hand-written JSON literal, so a description may not
// contain a double quote — it lands inside a JSON string and invalidates
// the whole document. Use single quotes.
const slugDesc = `Optional 1-3 word name for this question ('auth method', 'rollout order'), used where a front end has room for a label but not the whole question — the terminal shows it on the question's tab. Name the DECISION, not the answer. Optional, dropped if longer than 3 words or 24 characters, and never a substitute for a clear question.`

const askSchema = `{"type":"object","properties":{` +
	`"question":{"type":"string","description":"The question to ask the user. Be specific. Use this for a single question; use 'questions' to ask several at once."},` +
	`"slug":{"type":"string","description":"` + slugDesc + `"},` +
	`"options":{"type":"array","items":{"type":"string"},"description":"Optional multiple-choice answers. Omit for a free-form question."},` +
	`"allow_custom":{"type":"boolean","description":"When options are given, also let the user type their own answer instead of picking one."},` +
	`"questions":{"type":"array","maxItems":8,"description":"Ask several related questions in ONE interruption instead of stalling the turn once per question. The user sees them together and answers at their own pace before submitting. Prefer this whenever more than one thing is unclear.","items":{"type":"object","properties":{` +
	`"question":{"type":"string","description":"The question to ask. Be specific."},` +
	`"slug":{"type":"string","description":"` + slugDesc + ` Most useful here: a set is navigated by tab, and named tabs say what is behind each one."},` +
	`"options":{"type":"array","items":{"type":"string"},"description":"Optional multiple-choice answers. Omit for a free-form question."},` +
	`"allow_custom":{"type":"boolean","description":"When options are given, also let the user type their own answer instead of picking one."}` +
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
			Options: a.Options, AllowCustom: a.AllowCustom,
		})
	}
	for _, q := range a.Questions {
		if strings.TrimSpace(q.Question) == "" {
			return nil, fmt.Errorf("every entry in questions needs a question")
		}
		qs = append(qs, core.UserQuestion{
			Question: q.Question, Slug: core.SanitizeSlug(q.Slug),
			Options: q.Options, AllowCustom: q.AllowCustom,
		})
	}
	if len(qs) == 0 {
		return nil, fmt.Errorf("question is required")
	}
	if len(qs) > core.MaxAskQuestions {
		return nil, fmt.Errorf("too many questions in one call (%d, max %d) — ask the most blocking ones now and the rest after you have those answers", len(qs), core.MaxAskQuestions)
	}
	return qs, nil
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
		text := "User answered: " + answers[0].Answer
		details := map[string]any{"asked": true, "answer": answers[0].Answer}
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
			sb.WriteString(answers[i].Answer)
			if answers[i].Note != "" {
				sb.WriteString("\n   note: " + answers[i].Note)
			}
		}
		entry := map[string]any{
			"question": q.Question,
			"answer":   answers[i].Answer,
			"declined": answers[i].Declined,
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
