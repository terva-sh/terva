package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

type fakeAsker struct {
	ans  core.UserAnswer
	err  error
	gotQ core.UserQuestion
}

func (f *fakeAsker) Ask(ctx context.Context, q core.UserQuestion) (core.UserAnswer, error) {
	f.gotQ = q
	return f.ans, f.err
}

func askText(t *testing.T, res core.ToolResult) string {
	t.Helper()
	return res.Content[0].(provider.TextBlock).Text
}

func TestAskUserNoChannelProceeds(t *testing.T) {
	tool := &AskUserTool{} // nil Asker = headless
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"question": "Which DB?"}), nil)
	if err != nil {
		t.Fatalf("headless ask should not error: %v", err)
	}
	if res.IsError {
		t.Error("headless ask should not be an error result")
	}
	if !strings.Contains(askText(t, res), "Proceed with your best judgment") {
		t.Errorf("expected proceed guidance, got %q", askText(t, res))
	}
	if res.Details.(map[string]any)["asked"] != false {
		t.Errorf("asked should be false, got %v", res.Details)
	}
}

func TestAskUserReturnsAnswer(t *testing.T) {
	fa := &fakeAsker{ans: core.UserAnswer{Answer: "Postgres"}}
	tool := &AskUserTool{Asker: fa}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"question": "Which DB?",
		"options":  []string{"Postgres", "SQLite"},
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if fa.gotQ.Question != "Which DB?" || len(fa.gotQ.Options) != 2 {
		t.Errorf("question not passed through: %+v", fa.gotQ)
	}
	if !strings.Contains(askText(t, res), "Postgres") {
		t.Errorf("answer not surfaced: %q", askText(t, res))
	}
	if res.Details.(map[string]any)["answer"] != "Postgres" {
		t.Errorf("answer detail wrong: %v", res.Details)
	}
}

func TestAskUserDeclined(t *testing.T) {
	tool := &AskUserTool{Asker: &fakeAsker{ans: core.UserAnswer{Declined: true}}}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"question": "x?"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(askText(t, res), "declined") {
		t.Errorf("expected declined message, got %q", askText(t, res))
	}
}

func TestAskUserAskerError(t *testing.T) {
	tool := &AskUserTool{Asker: &fakeAsker{err: errors.New("cancelled")}}
	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"question": "x?"}), nil)
	if err == nil {
		t.Error("an Asker error should surface as a tool error")
	}
}

func TestAskUserMissingQuestion(t *testing.T) {
	tool := &AskUserTool{Asker: &fakeAsker{}}
	if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"question": "  "}), nil); err == nil {
		t.Error("blank question should error")
	}
}
