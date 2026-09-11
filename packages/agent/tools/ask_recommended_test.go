package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
)

func TestRecommendedOptionsReachBothQuestionShapes(t *testing.T) {
	tests := []struct {
		name string
		args string
		want [][]string
	}{
		{
			name: "singular with duplicates",
			args: `{"question":"Which database?","options":["Postgres","SQLite","DuckDB"],"recommended_options":["SQLite","DuckDB","SQLite"]}`,
			want: [][]string{{"SQLite", "DuckDB"}},
		},
		{
			name: "plural with multiple recommendations",
			args: `{"questions":[` +
				`{"question":"Which database?","options":["Postgres","SQLite"],"recommended_options":["SQLite"]},` +
				`{"question":"Which caches?","options":["Redis","Memory","Disk"],"recommended_options":["Redis","Disk"]}]}`,
			want: [][]string{{"SQLite"}, {"Redis", "Disk"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			asker := &recordingAsker{answers: make([]core.UserAnswer, len(tc.want))}
			tool := &AskUserTool{Asker: asker}
			if _, err := tool.Execute(context.Background(), json.RawMessage(tc.args), nil); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			got := make([][]string, len(asker.got))
			for i, q := range asker.got {
				got[i] = q.RecommendedOptions
				for _, option := range q.Options {
					want := false
					for _, recommended := range q.RecommendedOptions {
						if option == recommended {
							want = true
						}
					}
					if gotRecommended := q.OptionRecommended(option); gotRecommended != want {
						t.Errorf("OptionRecommended(%q) = %v, want %v", option, gotRecommended, want)
					}
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("recommendations = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestRecommendedOptionsRejectUnknownLabels(t *testing.T) {
	tool := &AskUserTool{Asker: &recordingAsker{answers: []core.UserAnswer{{}}}}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"Which?","options":["one","two"],"recommended_options":["three"]}`), nil)
	if err == nil || !strings.Contains(err.Error(), `recommended option "three" is not in options`) {
		t.Fatalf("error = %v, want unknown recommendation error", err)
	}
}

func TestRecommendedOptionsRequireOptions(t *testing.T) {
	tool := &AskUserTool{Asker: &recordingAsker{answers: []core.UserAnswer{{}}}}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"What should it be called?","recommended_options":["service"]}`), nil)
	if err == nil || !strings.Contains(err.Error(), "recommended requires options") {
		t.Fatalf("error = %v, want optionless recommendation error", err)
	}
}

func TestAskSchemaDescribesExactRecommendations(t *testing.T) {
	schema := string((&AskUserTool{}).Schema())
	for _, want := range []string{"recommended_options", "exact option text", "match one item in 'options'"} {
		if !strings.Contains(schema, want) {
			t.Errorf("schema does not contain %q", want)
		}
	}
}
