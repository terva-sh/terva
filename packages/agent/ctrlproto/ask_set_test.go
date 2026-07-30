package ctrlproto

import (
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/core"
)

// An ask carries a set now. The singular fields stay populated so a peer
// built before question sets still renders and answers the first
// question, rather than showing an empty card or a blank reply — the two
// halves of the compatibility have to hold in both directions.

func TestAskRequestMirrorsTheFirstQuestion(t *testing.T) {
	r := NewAskRequest("ask_1", []core.UserQuestion{
		{Question: "Which DB?", Options: []string{"Postgres", "SQLite"}, AllowCustom: true},
		{Question: "Migrate when?", Options: []string{"Now", "Later"}},
	})
	if len(r.Questions) != 2 {
		t.Fatalf("Questions = %+v, want both", r.Questions)
	}
	if r.Question != "Which DB?" || len(r.Options) != 2 || !r.AllowCustom {
		t.Fatalf("singular mirror = %q/%v/%v, want questions[0]", r.Question, r.Options, r.AllowCustom)
	}
}

// A request decoded from a peer that only knows the singular form still
// yields exactly one question.
func TestAskRequestSetFallsBackToTheSingularFields(t *testing.T) {
	var r AskRequest
	if err := json.Unmarshal([]byte(`{"ask_id":"a1","question":"Which DB?","options":["a","b"],"allow_custom":true}`), &r); err != nil {
		t.Fatal(err)
	}
	set := r.Set()
	if len(set) != 1 {
		t.Fatalf("Set() = %+v, want one question", set)
	}
	if set[0].Question != "Which DB?" || len(set[0].Options) != 2 || !set[0].AllowCustom {
		t.Fatalf("Set()[0] = %+v", set[0])
	}
}

func TestAskRequestSetPrefersTheQuestionList(t *testing.T) {
	r := AskRequest{
		AskID:    "a1",
		Question: "mirror",
		Questions: []AskQuestion{
			{Question: "one"}, {Question: "two"},
		},
	}
	if set := r.Set(); len(set) != 2 || set[0].Question != "one" {
		t.Fatalf("Set() = %+v, want the list", set)
	}
}

// The answer direction: a client that sends only the singular Answer
// still resolves, and one that sends the set is read positionally.
func TestAnswerParamsCoreReadsBothShapes(t *testing.T) {
	var old AnswerParams
	if err := json.Unmarshal([]byte(`{"ask_id":"a1","answer":{"answer":"yes"}}`), &old); err != nil {
		t.Fatal(err)
	}
	if got := old.Core(); len(got) != 1 || got[0].Answer != "yes" {
		t.Fatalf("single-answer client = %+v, want one answer", got)
	}

	var set AnswerParams
	if err := json.Unmarshal([]byte(`{"ask_id":"a1","answer":{"answer":"one"},"answers":[{"answer":"one"},{"declined":true}]}`), &set); err != nil {
		t.Fatal(err)
	}
	got := set.Core()
	if len(got) != 2 || got[0].Answer != "one" || !got[1].Declined {
		t.Fatalf("set client = %+v", got)
	}
}

// An empty ask must not mint a request that claims a question.
func TestAskRequestEmptySet(t *testing.T) {
	r := NewAskRequest("a1", nil)
	if r.Question != "" || len(r.Questions) != 0 {
		t.Fatalf("empty ask = %+v", r)
	}
}
