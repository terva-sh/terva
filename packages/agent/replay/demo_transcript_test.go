package replay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/session"
)

// demoTranscript is the fixture the terminal demo is recorded from
// (TKT-01M2Y0YQ, reshot under TKT-01M2YPDEJ2): a scene of about twenty
// seconds at the default pace, recorded at half speed, with four permission
// prompts (one refused, one widened to the session) and one question answered
// with a note, so the recording shows a person choosing. The committed file
// is the source of truth for the demo, and this test is how it stays true: with UPDATE_FIXTURES set it rewrites the file
// from the scene below; without it, it replays the committed file and asserts
// the beats are still there, so a change to the transcript format that
// dropped one fails here rather than in a silent recording.
//
//	UPDATE_FIXTURES=1 go test ./packages/agent/replay/ -run TestDemoTranscript
const demoTranscript = "../../../assets/captures/terminal-demo.jsonl"

func writeDemoTranscript(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path)
	sess, err := session.NewSessionAtPath(path, "/home/ada/auth-service", "anthropic", "claude-opus-5", "demo")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 19, 14, 0, 0, 0, time.UTC)
	tick := func() time.Time { at = at.Add(3 * time.Second); return at }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	text := func(role provider.Role, s string, calls ...provider.ToolCallBlock) provider.Message {
		m := provider.Message{Role: role, Time: tick(), Content: []provider.Content{provider.TextBlock{Text: s}}}
		for _, c := range calls {
			m.Content = append(m.Content, c)
		}
		return m
	}
	result := func(callID, s string) provider.Message {
		return provider.Message{Role: provider.RoleTool, Time: tick(), Content: []provider.Content{
			provider.ToolResultBlock{CallID: callID, Content: []provider.Content{provider.TextBlock{Text: s}}},
		}}
	}
	args := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

	must(sess.AppendMessage(text(provider.RoleUser, "verifyToken in auth.go compares the token with ==. Fix it, run the tests, and note it in the changelog.")))

	must(sess.AppendMessage(text(provider.RoleAssistant, "Reading verifyToken and its test first.",
		provider.ToolCallBlock{ID: "call-read", Name: "read", Arguments: args(map[string]any{"path": "auth.go"})})))
	must(sess.AppendMessage(result("call-read",
		"func verifyToken(token, expected string) bool {\n\tif token == expected {\n\t\treturn true\n\t}\n\treturn false\n}")))
	must(sess.AppendMessage(text(provider.RoleAssistant, "",
		provider.ToolCallBlock{ID: "call-read-test", Name: "read", Arguments: args(map[string]any{"path": "auth_test.go"})})))
	must(sess.AppendMessage(result("call-read-test",
		"func TestVerifyToken(t *testing.T) {\n\tif !verifyToken(\"s3cret\", \"s3cret\") {\n\t\tt.Fatal(\"equal tokens must verify\")\n\t}\n\tif verifyToken(\"s3cret\", \"s3cres\") {\n\t\tt.Fatal(\"a one-byte difference must fail\")\n\t}\n}")))

	must(sess.AppendMessage(text(provider.RoleAssistant, "A plain == leaks timing: it returns as soon as one byte differs, so an attacker can learn the token one byte at a time. hmac.Equal compares in constant time, and the existing test still covers both cases.",
		provider.ToolCallBlock{ID: "call-edit", Name: "edit", Arguments: args(map[string]any{
			"path":       "auth.go",
			"old_string": "\tif token == expected {\n\t\treturn true\n\t}\n\treturn false",
			"new_string": "\treturn hmac.Equal([]byte(token), []byte(expected))",
		})})))
	// The person reads the diff, glances at the session-wide option, and
	// allows this call alone.
	must(sess.AppendPermission(session.PermissionRecord{
		CallID: "call-edit", Tool: "edit", Preview: "auth.go: replace 4 lines with 1",
		Asked: tick(), Waited: 2600 * time.Millisecond, Allow: true,
	}))
	must(sess.AppendMessage(result("call-edit", "edited auth.go: 1 replacement")))

	must(sess.AppendMessage(text(provider.RoleAssistant, "The tests, with the race detector since this is a compare.",
		provider.ToolCallBlock{ID: "call-race", Name: "bash", Arguments: args(map[string]any{"command": "go test -race ./..."})})))
	// Refused: the person walks past every allow to no, and the agent adapts.
	must(sess.AppendPermission(session.PermissionRecord{
		CallID: "call-race", Tool: "bash", Preview: "go test -race ./...",
		Asked: tick(), Waited: 2200 * time.Millisecond, Allow: false, Reason: "user declined: the race run takes minutes on this laptop, test the one package",
	}))
	must(sess.AppendMessage(provider.Message{Role: provider.RoleTool, Time: tick(), Content: []provider.Content{
		provider.ToolResultBlock{CallID: "call-race", IsError: true, Content: []provider.Content{provider.TextBlock{Text: "user declined: the race run takes minutes on this laptop, test the one package"}}},
	}}))

	must(sess.AppendMessage(text(provider.RoleAssistant, "The one package, then.",
		provider.ToolCallBlock{ID: "call-test", Name: "bash", Arguments: args(map[string]any{"command": "go test ./auth/"})})))
	must(sess.AppendPermission(session.PermissionRecord{
		CallID: "call-test", Tool: "bash", Preview: "go test ./auth/",
		Asked: tick(), Waited: 1200 * time.Millisecond, Allow: true,
	}))
	must(sess.AppendMessage(result("call-test", "ok  \tauth-service/auth\t0.412s")))

	must(sess.AppendMessage(text(provider.RoleAssistant, "Passing. The changelog has an Unreleased section; how should the entry read?",
		provider.ToolCallBlock{ID: "call-ask", Name: "ask_user_question", Arguments: args(map[string]any{
			"questions": []map[string]any{{"question": "How should the changelog entry read?", "options": []string{"Security: constant-time token compare", "Fixed: token compare timing leak", "Skip the changelog"}}},
		})})))
	// The person picks the second option and types a note explaining why.
	must(sess.AppendAsk(session.AskRecord{
		AskID: "ask_1",
		Questions: session.RecordQuestions([]core.UserQuestion{{
			Question: "How should the changelog entry read?", Slug: "changelog",
			Options:            []string{"Security: constant-time token compare", "Fixed: token compare timing leak", "Skip the changelog"},
			RecommendedOptions: []string{"Security: constant-time token compare"},
		}}),
		Answers: session.RecordAnswers([]core.UserAnswer{{Answer: "Fixed: token compare timing leak", Note: "our users read Fixed, not Security"}}),
		Asked:   tick(), Waited: 2800 * time.Millisecond,
	}))
	must(sess.AppendMessage(result("call-ask", "Fixed: token compare timing leak\nnote: our users read Fixed, not Security")))

	must(sess.AppendMessage(text(provider.RoleAssistant, "Under Fixed, then.",
		provider.ToolCallBlock{ID: "call-changelog", Name: "edit", Arguments: args(map[string]any{
			"path":       "CHANGELOG.md",
			"old_string": "## Unreleased\n",
			"new_string": "## Unreleased\n\n### Fixed\n\n- Token compare timing leak in verifyToken.\n",
		})})))
	// Allowed for the rest of the session: edits to this repo are trusted now.
	must(sess.AppendPermission(session.PermissionRecord{
		CallID: "call-changelog", Tool: "edit", Preview: "CHANGELOG.md: insert 4 lines",
		Asked: tick(), Waited: 1500 * time.Millisecond, Allow: true, Scope: session.PermissionScopeTool,
	}))
	must(sess.AppendMessage(result("call-changelog", "edited CHANGELOG.md: 1 replacement")))

	must(sess.AppendMessage(text(provider.RoleAssistant, "Done. verifyToken compares in constant time, the auth package passes, and the changelog carries the fix under Fixed. Nothing is committed; the diff is yours to review.")))
	must(sess.Close())
}

func TestDemoTranscript(t *testing.T) {
	if os.Getenv("UPDATE_FIXTURES") != "" {
		writeDemoTranscript(t, demoTranscript)
	}
	rows, _, err := session.ReadReplayRows(demoTranscript)
	if err != nil {
		t.Fatalf("read the demo transcript (run `UPDATE_FIXTURES=1 go test ./packages/agent/replay/ -run TestDemoTranscript` to regenerate): %v", err)
	}
	count := map[session.ReplayRowKind]int{}
	for _, r := range rows {
		count[r.Kind]++
	}
	if count[session.ReplayRowPermission] != 4 || count[session.ReplayRowAsk] != 1 {
		t.Fatalf("the demo has %d permission rows and %d ask rows; want 4 and 1", count[session.ReplayRowPermission], count[session.ReplayRowAsk])
	}
	var refused, widened, noted bool
	for _, r := range rows {
		switch r.Kind {
		case session.ReplayRowPermission:
			refused = refused || !r.Permission.Allow
			widened = widened || r.Permission.Scope == session.PermissionScopeTool
		case session.ReplayRowAsk:
			noted = noted || (len(r.Ask.Answers) == 1 && r.Ask.Answers[0].Note != "")
		}
	}
	if !refused || !widened || !noted {
		t.Fatalf("the demo must show a refusal, a session-wide allow, and a noted answer; got refused=%v widened=%v noted=%v", refused, widened, noted)
	}
	frames := Synthesize(rows, Options{})
	var total time.Duration
	for _, f := range frames {
		total += f.Delay
	}
	// The scene must fit a short recording at 1x. The bound is generous so a
	// pace tweak does not fail it; the recipe's timeout is the real cap.
	if total > 30*time.Second {
		t.Errorf("the demo plays for %v at 1x (%v at the recorded half speed), which is longer than a demo should be", total, 2*total)
	}
	t.Logf("demo: %d rows, %d frames, %v at 1x", len(rows), len(frames), total)
}
