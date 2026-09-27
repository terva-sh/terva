package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
)

// citedAsker is a fakeAsker whose answers the room records.
type citedAsker struct {
	fakeAsker
	rec AnswerRecord
}

func (c *citedAsker) AskCited(ctx context.Context, qs []core.UserQuestion) ([]core.UserAnswer, AnswerRecord, error) {
	ans, err := c.Ask(ctx, qs)
	return ans, c.rec, err
}

func TestARecordedAnswerNamesItsReference(t *testing.T) {
	args := mustJSON(t, map[string]any{"question": "Accept the cut?", "options": []string{"Accept cut", "Keep it"}})
	a := &citedAsker{fakeAsker: fakeAsker{ans: core.UserAnswer{Answer: "Accept cut"}}, rec: AnswerRecord{Ref: "answer:01K"}}
	res, err := (&AskUserTool{Asker: a}).Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := askText(t, res)
	if !strings.HasPrefix(text, "User answered: Accept cut\n") || !strings.Contains(text, "records this answer as answer:01K. To tell a teammate") {
		t.Errorf("result: %q", text)
	}
	if res.Details.(map[string]any)["cite"] != "answer:01K" {
		t.Errorf("details: %v", res.Details)
	}

	// No record, no reference: a session outside a talkoot reads the answer
	// as before.
	a.rec = AnswerRecord{}
	res, err = (&AskUserTool{Asker: a}).Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if text := askText(t, res); text != "User answered: Accept cut" {
		t.Errorf("an unrecorded answer: %q", text)
	}
}

// A record that failed still returns the answer, and tells the model that a
// teammate cannot check it.
func TestAFailedRecordIsNamedInTheResult(t *testing.T) {
	args := mustJSON(t, map[string]any{"question": "Accept the cut?", "options": []string{"Accept cut", "Keep it"}})
	a := &citedAsker{fakeAsker: fakeAsker{ans: core.UserAnswer{Answer: "Accept cut"}}, rec: AnswerRecord{Err: errors.New("the talkoot is closed")}}
	res, err := (&AskUserTool{Asker: a}).Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := askText(t, res)
	if !strings.HasPrefix(text, "User answered: Accept cut\n") || !strings.Contains(text, "could not record this answer, so a teammate cannot check it (the talkoot is closed)") {
		t.Errorf("result: %q", text)
	}
	if d := res.Details.(map[string]any); d["cite_error"] != "the talkoot is closed" || d["cite"] != nil {
		t.Errorf("details: %v", d)
	}
}
