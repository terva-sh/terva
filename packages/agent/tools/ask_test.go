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
	// ans is the reply to a single-question ask; answers, when set,
	// replies to a set positionally.
	ans     core.UserAnswer
	answers []core.UserAnswer
	err     error
	gotQ    core.UserQuestion   // first question, for the single-question tests
	gotSet  []core.UserQuestion // the whole set as it arrived
}

func (f *fakeAsker) Ask(ctx context.Context, qs []core.UserQuestion) ([]core.UserAnswer, error) {
	f.gotSet = qs
	if len(qs) > 0 {
		f.gotQ = qs[0]
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.answers != nil {
		return f.answers, nil
	}
	return []core.UserAnswer{f.ans}, nil
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

// A set goes over as one call and comes back as one result. Tool calls
// run strictly one at a time, so without this an agent with three things
// to clarify stalls the turn three separate times.
func TestAskUserQuestionSet(t *testing.T) {
	fa := &fakeAsker{answers: []core.UserAnswer{
		{Answer: "SQLite"}, {Declined: true}, {Answer: "billing-api"},
	}}
	tool := &AskUserTool{Asker: fa}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"questions": []map[string]any{
			{"question": "Which DB?", "options": []string{"Postgres", "SQLite"}},
			{"question": "Migrate when?", "options": []string{"Now", "At deploy"}},
			{"question": "Name it?"},
		},
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fa.gotSet) != 3 {
		t.Fatalf("the Asker saw %d questions, want 3", len(fa.gotSet))
	}
	got := askText(t, res)
	for _, want := range []string{"1. Which DB?", "SQLite", "2. Migrate when?", "(declined)", "3. Name it?", "billing-api"} {
		if !strings.Contains(got, want) {
			t.Fatalf("result missing %q:\n%s", want, got)
		}
	}
	// A partial decline must still tell the model what to do about it.
	if !strings.Contains(got, "best judgment") {
		t.Fatalf("a declined question needs its guidance:\n%s", got)
	}
	det := res.Details.(map[string]any)
	if det["declined"] != false {
		t.Errorf("declined should be false when only some were: %v", det)
	}
	answers, ok := det["answers"].([]map[string]any)
	if !ok || len(answers) != 3 {
		t.Fatalf("answers detail = %v", det["answers"])
	}
	if answers[1]["declined"] != true {
		t.Errorf("per-question declined flag missing: %v", answers[1])
	}
}

// The singular and plural fields together read as one ordered set, so a
// model that fills in both is not silently half-ignored.
func TestAskUserMergesSingularAndPlural(t *testing.T) {
	fa := &fakeAsker{answers: []core.UserAnswer{{Answer: "a"}, {Answer: "b"}}}
	tool := &AskUserTool{Asker: fa}
	if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"question":  "first?",
		"questions": []map[string]any{{"question": "second?"}},
	}), nil); err != nil {
		t.Fatal(err)
	}
	if len(fa.gotSet) != 2 || fa.gotSet[0].Question != "first?" || fa.gotSet[1].Question != "second?" {
		t.Fatalf("merged set = %+v", fa.gotSet)
	}
}

// A front end that breaks the one-answer-per-question contract must not
// index out of range in the tool.
func TestAskUserPadsAShortAnswerSet(t *testing.T) {
	fa := &fakeAsker{answers: []core.UserAnswer{{Answer: "only one"}}}
	tool := &AskUserTool{Asker: fa}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"questions": []map[string]any{{"question": "one?"}, {"question": "two?"}},
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := askText(t, res); !strings.Contains(got, "only one") || !strings.Contains(got, "(declined)") {
		t.Fatalf("a short set should pad with declines:\n%s", got)
	}
}

func TestAskUserRejectsTooManyQuestions(t *testing.T) {
	qs := make([]map[string]any, core.MaxAskQuestions+1)
	for i := range qs {
		qs[i] = map[string]any{"question": "q?"}
	}
	tool := &AskUserTool{Asker: &fakeAsker{}}
	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"questions": qs}), nil)
	if err == nil {
		t.Fatal("over the cap should error rather than open an unreadable dialog")
	}
	if !strings.Contains(err.Error(), "max") {
		t.Errorf("the error should tell the model the cap: %v", err)
	}
}

func TestAskUserRejectsABlankQuestionInASet(t *testing.T) {
	tool := &AskUserTool{Asker: &fakeAsker{}}
	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"questions": []map[string]any{{"question": "fine?"}, {"question": "  "}},
	}), nil)
	if err == nil {
		t.Fatal("a blank question inside a set should error")
	}
}
