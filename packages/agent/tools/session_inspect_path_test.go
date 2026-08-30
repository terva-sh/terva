package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// inspectByPath runs session_inspect against a tool wired with sandbox, which
// is what bounds the `path` argument.
func inspectByPath(t *testing.T, tool *SessionInspectTool, args string) core.ToolResult {
	t.Helper()
	res, err := tool.Execute(context.Background(), json.RawMessage(args), func(string) {})
	if err != nil {
		t.Fatalf("Execute(%s): %v", args, err)
	}
	return res
}

// Finding D4, and the thing that actually blocked two harness reviews: the
// session under review was a file on disk — downloaded from another machine,
// belonging to no local project — so `session_inspect` could not touch it and
// both reviews were done with an out-of-tree Python script instead.
//
// The file was never protected. The model could `read` it or `cat` it; what it
// could not do was ANALYZE it. So this grants a lens, not access.
func TestSessionInspectReadsATranscriptByPath(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	downloads := testsupport.TempDir(t)
	transcript := filepath.Join(downloads, "20260729-184207-2f58d72f.jsonl")
	writeSessionFixture(t, transcript, cwd, "review this harness", "the full findings report")

	tool := &SessionInspectTool{TervaHome: home, CWD: cwd, Sandbox: NewSandbox(cwd)}
	res := inspectByPath(t, tool, `{"path":`+jsonStr(transcript)+`}`)
	if res.IsError {
		t.Fatalf("a readable transcript should inspect, got: %q", inspectText(t, res))
	}
	got := inspectText(t, res)
	if !strings.Contains(got, "the full findings report") {
		t.Errorf("listing did not show the file's events: %q", got)
	}
	// The id is a label derived from the filename, so a downloaded transcript
	// still reports the id it was recorded under — what a reader correlates
	// everything else about that session against.
	details, _ := res.Details.(map[string]any)
	if id, _ := details["session_id"].(string); id != "20260729-184207-2f58d72f" {
		t.Errorf("session_id = %q, want the filename stem", id)
	}
}

// The security argument in one test: `path` reaches exactly what `read` reaches.
// $TERVA_HOME/sessions is a registered secret root, so another project's
// transcripts stay closed — and close with the deny list's own explanation
// rather than a puzzling "no such session".
func TestSessionInspectPathCannotReachAnotherProjectsSessions(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	otherProject := testsupport.TempDir(t)

	// A real session belonging to a DIFFERENT project, in its own bucket under
	// $TERVA_HOME/sessions.
	otherDir := core.SessionsDir(home, otherProject)
	if err := os.MkdirAll(otherDir, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(otherDir, "20260101-000000-deadbeef.jsonl")
	writeSessionFixture(t, victim, otherProject, "their prompt", "their secrets")

	sandbox := NewSandbox(cwd)
	sandbox.Lock()
	sandbox.AddSecretRoot(filepath.Join(home, "sessions"), filepath.Join(home, "swarm"))
	tool := &SessionInspectTool{TervaHome: home, CWD: cwd, Sandbox: sandbox}

	res := inspectByPath(t, tool, `{"path":`+jsonStr(victim)+`}`)
	if !res.IsError {
		t.Fatalf("another project's transcript must be refused, got: %q", inspectText(t, res))
	}
	got := inspectText(t, res)
	if strings.Contains(got, "their secrets") {
		t.Fatalf("the refusal leaked the transcript it refused: %q", got)
	}
	// The deny list's own wording, which already explains that bash cannot reach
	// it either and /unjail does not lift it — far more actionable than a
	// generic refusal, and it means one message stays true if the policy moves.
	if !strings.Contains(got, "credentials or transcripts") {
		t.Errorf("refusal should carry the read policy's explanation, got: %q", got)
	}
}

// A jailed agent may read outside its own tree — that asymmetry is deliberate
// (bash is not path-jailed, so a read refusal bought nothing and cost turns).
// `path` inherits it rather than re-deciding: a transcript outside cwd is
// readable, which is the ONLY reason this feature works, since a downloaded
// session is never inside the project.
func TestSessionInspectPathFollowsReadNotTheWriteJail(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	outside := testsupport.TempDir(t)
	transcript := filepath.Join(outside, "s.jsonl")
	writeSessionFixture(t, transcript, cwd, "hello", "world")

	sandbox := NewSandbox(cwd)
	sandbox.Lock() // jailed: writes confined to cwd, reads are not
	tool := &SessionInspectTool{TervaHome: home, CWD: cwd, Sandbox: sandbox}

	res := inspectByPath(t, tool, `{"path":`+jsonStr(transcript)+`}`)
	if res.IsError {
		t.Fatalf("a jailed agent may READ outside cwd, so this must resolve: %q", inspectText(t, res))
	}
}

// session_id and path both name a transcript. Picking one silently would return
// an analysis of a file the caller did not ask about, and nothing in the output
// would reveal the substitution.
func TestSessionInspectRejectsBothIDAndPath(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	tool := &SessionInspectTool{TervaHome: home, CWD: cwd, Sandbox: NewSandbox(cwd)}

	res := inspectByPath(t, tool, `{"session_id":"20260101-000000-deadbeef","path":"/tmp/x.jsonl"}`)
	if !res.IsError {
		t.Fatal("a call naming both a session_id and a path must be refused")
	}
	got := inspectText(t, res)
	// Both corrected forms, the way the expand/cursor contradiction reports.
	if !strings.Contains(got, "BY ID") || !strings.Contains(got, "BY PATH") {
		t.Errorf("refusal should show both corrected forms, got: %q", got)
	}
}

// A path that is not a file at all fails with what went wrong, not with a
// transcript-parsing error further down.
func TestSessionInspectPathErrors(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	tool := &SessionInspectTool{TervaHome: home, CWD: cwd, Sandbox: NewSandbox(cwd)}

	// A missing transcript now reports through the shared not-found
	// diagnostic (notFoundError), so it names the condition rather than the
	// old "cannot read" framing. The intent is unchanged and the assertion is
	// narrower: it must say what went wrong, not fail parsing further down.
	res := inspectByPath(t, tool, `{"path":`+jsonStr(filepath.Join(cwd, "nope.jsonl"))+`}`)
	if !res.IsError || !strings.Contains(inspectText(t, res), "no such file or directory") {
		t.Errorf("a missing file should say so, got (err=%v): %q", res.IsError, inspectText(t, res))
	}

	res = inspectByPath(t, tool, `{"path":`+jsonStr(cwd)+`}`)
	if !res.IsError || !strings.Contains(inspectText(t, res), "is a directory") {
		t.Errorf("a directory should say so, got (err=%v): %q", res.IsError, inspectText(t, res))
	}
}

// jsonStr JSON-quotes a path so Windows separators survive into the args blob.
func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
