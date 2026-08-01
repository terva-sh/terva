package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
)

// Multi-select exists because some option sets are not mutually exclusive, and
// only the model knows which. These pin the two halves of that: the flag
// survives the schema into the question, and what comes back is rendered so the
// model cannot misread how many things it was told.

type recordingAsker struct {
	got     []core.UserQuestion
	answers []core.UserAnswer
}

func (a *recordingAsker) Ask(ctx context.Context, qs []core.UserQuestion) ([]core.UserAnswer, error) {
	a.got = qs
	return a.answers, nil
}

func askWith(t *testing.T, args string, answers []core.UserAnswer) (*recordingAsker, core.ToolResult) {
	t.Helper()
	asker := &recordingAsker{answers: answers}
	tool := &AskUserTool{Asker: asker}
	res, err := tool.Execute(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return asker, res
}

// The flag has to reach the front end, on BOTH accepted shapes. A schema field
// the tool then drops would look exactly like a front end that ignored it.
func TestMultiSelectReachesTheQuestion(t *testing.T) {
	t.Run("singular", func(t *testing.T) {
		asker, _ := askWith(t,
			`{"question":"Which to enable?","options":["a","b"],"multi_select":true}`,
			[]core.UserAnswer{{Answers: []string{"a"}}})
		if len(asker.got) != 1 || !asker.got[0].MultiSelect {
			t.Fatalf("MultiSelect did not survive into the question: %+v", asker.got)
		}
	})

	t.Run("set", func(t *testing.T) {
		asker, _ := askWith(t,
			`{"questions":[{"question":"Which one?","options":["a","b"]},`+
				`{"question":"Which ones?","options":["c","d"],"multi_select":true}]}`,
			[]core.UserAnswer{{Answer: "a"}, {Answers: []string{"c", "d"}}})
		if len(asker.got) != 2 {
			t.Fatalf("got %d questions, want 2", len(asker.got))
		}
		if asker.got[0].MultiSelect {
			t.Error("question 1 is single-select and must not be flagged multi")
		}
		if !asker.got[1].MultiSelect {
			t.Error("question 2 declared multi_select and lost it")
		}
	})

	// The default has to be single-select. A question the model said nothing
	// about must not become "tick as many as you like": that widens what the
	// user is agreeing to, on a decision the model never marked as additive.
	t.Run("defaults off", func(t *testing.T) {
		asker, _ := askWith(t,
			`{"question":"Which one?","options":["a","b"]}`,
			[]core.UserAnswer{{Answer: "a"}})
		if asker.got[0].MultiSelect {
			t.Error("multi_select defaulted ON; it must default to a single choice")
		}
	})
}

// What the model reads back. Each case is one the bare option text cannot say.
func TestMultiSelectResultNamesTheRelationship(t *testing.T) {
	cases := []struct {
		name   string
		answer core.UserAnswer
		want   string
		absent string
	}{{
		name:   "several",
		answer: core.UserAnswer{Answers: []string{"redis", "postgres"}},
		// Bare "redis, postgres" cannot be told from one option containing a
		// comma — and that decides whether the model sets up one thing or two.
		want: "all of: redis, postgres",
	}, {
		name:   "exactly one of several",
		answer: core.UserAnswer{Answers: []string{"redis"}},
		// The user could have taken more and chose not to. That is a decision,
		// and rendering it as a plain answer throws it away.
		want: "only: redis",
	}, {
		name:   "none",
		answer: core.UserAnswer{Answers: []string{}},
		// An answer, not an absence of one. Rendered empty it reads as a
		// front-end bug and the model would be right to distrust it.
		want: "none of the options",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, res := askWith(t,
				`{"question":"Which to enable?","options":["redis","postgres"],"multi_select":true}`,
				[]core.UserAnswer{tc.answer})
			text := askText(t, res)
			if !strings.Contains(text, tc.want) {
				t.Errorf("result = %q; want it to contain %q", text, tc.want)
			}
			if details(t, res)["answers"] == nil {
				t.Error("Details carries no 'answers' list; a structured reader has only the joined string")
			}
		})
	}
}

// A single-select answer keeps the shape the model has been reading all along.
// Multi-select is the new case, not the common one, and dressing every answer
// up in "only:" would be a change to every existing ask.
func TestSingleSelectResultIsUnchanged(t *testing.T) {
	_, res := askWith(t,
		`{"question":"Which one?","options":["redis","postgres"]}`,
		[]core.UserAnswer{{Answer: "redis"}})
	text := askText(t, res)
	if !strings.Contains(text, "User answered: redis") {
		t.Errorf("result = %q; want the plain single-choice shape", text)
	}
	if strings.Contains(text, "only:") || strings.Contains(text, "all of:") {
		t.Errorf("result = %q; a single-select answer must not gain multi-select framing", text)
	}
	if _, ok := details(t, res)["answers"]; ok {
		t.Error("Details gained an 'answers' list for a single-choice question")
	}
}

func details(t *testing.T, res core.ToolResult) map[string]any {
	t.Helper()
	m, ok := res.Details.(map[string]any)
	if !ok {
		t.Fatalf("Details is %T, want map[string]any", res.Details)
	}
	return m
}

// Chosen() is the one correct way to read a choice, so it has to be right for
// every shape a front end can produce — including the old one that only ever
// sets Answer.
func TestChosenNormalisesEveryAnswerShape(t *testing.T) {
	cases := []struct {
		name string
		in   core.UserAnswer
		want []string
	}{
		{"legacy single", core.UserAnswer{Answer: "a"}, []string{"a"}},
		{"multi", core.UserAnswer{Answer: "a, b", Answers: []string{"a", "b"}}, []string{"a", "b"}},
		{"empty", core.UserAnswer{}, nil},
		{"declined", core.UserAnswer{Answer: "a", Declined: true}, nil},
		{"multi none", core.UserAnswer{Answers: []string{}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.Chosen()
			if len(got) != len(tc.want) {
				t.Fatalf("Chosen() = %q; want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("Chosen() = %q; want %q", got, tc.want)
				}
			}
		})
	}
}
