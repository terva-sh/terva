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

type askArgs struct {
	Question    string   `json:"question"`
	Options     []string `json:"options,omitempty"`
	AllowCustom bool     `json:"allow_custom,omitempty"`
}

const askSchema = `{"type":"object","properties":{` +
	`"question":{"type":"string","description":"The question to ask the user. Be specific."},` +
	`"options":{"type":"array","items":{"type":"string"},"description":"Optional multiple-choice answers. Omit for a free-form question."},` +
	`"allow_custom":{"type":"boolean","description":"When options are given, also let the user type their own answer instead of picking one."}` +
	`},"required":["question"]}`

func (t *AskUserTool) Name() string { return "ask_user_question" }
func (t *AskUserTool) Description() string {
	return "Ask the user a clarifying question and wait for their answer, instead of guessing when requirements are ambiguous. Optionally offer multiple-choice options. Use sparingly, for decisions you genuinely cannot make yourself; it pauses the turn for a human."
}
func (t *AskUserTool) Schema() json.RawMessage { return json.RawMessage(askSchema) }

func (t *AskUserTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var a askArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(a.Question) == "" {
		return core.ToolResult{}, fmt.Errorf("question is required")
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

	ans, err := t.Asker.Ask(ctx, core.UserQuestion{
		Question:    a.Question,
		Options:     a.Options,
		AllowCustom: a.AllowCustom,
	})
	if err != nil {
		// Context cancelled or front-end failure — surface as an error
		// result so the model knows the question was not answered.
		return core.ToolResult{}, fmt.Errorf("ask_user_question: %w", err)
	}
	if ans.Declined {
		return core.ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: "The user declined to answer. Proceed with your best judgment and state the assumption you made."}},
			Details: map[string]any{"asked": true, "declined": true},
		}, nil
	}
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: "User answered: " + ans.Answer}},
		Details: map[string]any{"asked": true, "answer": ans.Answer},
	}, nil
}
