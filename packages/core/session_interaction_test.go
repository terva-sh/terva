package core

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// A permission row and an ask row round-trip through both replay readers with
// their fields intact, and the ordinary loader ignores them: they change
// nothing in the transcript a resumed session sees.
func TestInteractionRowsRoundTrip(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := NewSessionAtPath(path, "/cwd", "prov", "model", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "go"}}}); err != nil {
		t.Fatal(err)
	}
	asked := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	perm := PermissionRecord{CallID: "call-1", Tool: "bash", Preview: "go test ./...", Asked: asked, Waited: 4 * time.Second, Allow: true}
	if err := sess.AppendPermission(perm); err != nil {
		t.Fatal(err)
	}
	ask := AskRecord{
		AskID:     "ask_1",
		Questions: RecordQuestions([]UserQuestion{{Question: "Which?", Slug: "which", Options: []string{"a", "b"}, RecommendedOptions: []string{"a"}}}),
		Answers:   RecordAnswers([]UserAnswer{{Answer: "b"}}),
		Asked:     asked,
		Waited:    2 * time.Second,
	}
	if err := sess.AppendAsk(ask); err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	rows, _, err := ReadReplayRows(path)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []ReplayRowKind
	for _, r := range rows {
		kinds = append(kinds, r.Kind)
	}
	if len(rows) != 3 || rows[1].Kind != ReplayRowPermission || rows[2].Kind != ReplayRowAsk {
		t.Fatalf("rows: %v", kinds)
	}
	if got := rows[1].Permission; got != perm {
		t.Errorf("permission row: %+v, want %+v", got, perm)
	}
	if got := rows[2].Ask; got.AskID != "ask_1" || got.Waited != 2*time.Second || len(got.Questions) != 1 || got.Questions[0].Recommended[0] != "a" || got.Answers[0].Answer != "b" {
		t.Errorf("ask row: %+v", got)
	}
	qs := rows[2].Ask.UserQuestions()
	if len(qs) != 1 || qs[0].RecommendedOptions[0] != "a" || qs[0].Slug != "which" {
		t.Errorf("questions back: %+v", qs)
	}

	var streamed []ReplayRowKind
	if _, _, err := StreamReplayRows(context.Background(), path, 0, func(_ int, r ReplayRow) {
		streamed = append(streamed, r.Kind)
	}); err != nil {
		t.Fatal(err)
	}
	if len(streamed) != 3 || streamed[1] != ReplayRowPermission || streamed[2] != ReplayRowAsk {
		t.Errorf("streamed: %v", streamed)
	}

	// The loader that resumes a session sees one message and nothing else.
	re, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	if n := len(msgs); n != 1 {
		t.Errorf("resumed session has %d messages, want 1", n)
	}
}

// A nil session accepts both appends, like the other append helpers, because
// a mode with no transcript still prompts.
func TestInteractionRowsNilSession(t *testing.T) {
	var s *Session
	if err := s.AppendPermission(PermissionRecord{}); err != nil {
		t.Error(err)
	}
	if err := s.AppendAsk(AskRecord{}); err != nil {
		t.Error(err)
	}
}

// The scope of an allow survives the round trip, and is derived from the
// decision the same way the dialog's list is ordered.
func TestPermissionRecordScope(t *testing.T) {
	for _, tc := range []struct {
		d    ConfirmDecision
		want string
	}{
		{ConfirmDecision{Allow: true}, PermissionScopeCall},
		{ConfirmDecision{Allow: true, RememberTool: true}, PermissionScopeTool},
		{ConfirmDecision{Allow: true, PersistTool: true}, PermissionScopeToolSaved},
		{ConfirmDecision{Allow: true, RememberAll: true}, PermissionScopeAll},
		{ConfirmDecision{Allow: false, RememberAll: true}, PermissionScopeCall},
	} {
		if got := PermissionScopeOf(tc.d); got != tc.want {
			t.Errorf("PermissionScopeOf(%+v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
