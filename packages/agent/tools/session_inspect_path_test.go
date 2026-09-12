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

// transcriptSandbox builds the sandbox the host builds: $TERVA_HOME/sessions
// and swarm/ denied to every read route, and registered as transcript roots so
// the sanctioned reader keeps one way in. state selects the jail posture,
// because the carve-out has to hold in all three.
func transcriptSandbox(t *testing.T, home, cwd, state string) *Sandbox {
	t.Helper()
	sb := NewSandbox(cwd)
	sb.AddSecretRoot(
		filepath.Join(home, "auth.json"),
		filepath.Join(home, "logs"),
		filepath.Join(home, "sessions"),
		filepath.Join(home, "swarm"),
	)
	sb.AddTranscriptRoot(filepath.Join(home, "sessions"), filepath.Join(home, "swarm"))
	if state != "initially-unjailed" {
		sb.Lock()
	}
	if state == "unlocked" {
		sb.Unlock()
	}
	return sb
}

// The reason this ticket exists: a session belonging to ANOTHER project was
// reachable with `cp` the whole time and unreachable through the one reader
// that redacts. Diagnosing a cost or cache problem in project B from project A
// is exactly when you need it, and it is the case with no workaround short of
// copying the file out and reading it raw.
//
// It holds in every jail state. Requiring /unjail to read a transcript would
// only teach the model to reach for `cp` again.
func TestSessionInspectPathReachesAnotherProjectsSessions(t *testing.T) {
	for _, state := range []string{"locked", "unlocked", "initially-unjailed"} {
		t.Run(state, func(t *testing.T) {
			home := testsupport.TempDir(t)
			cwd := testsupport.TempDir(t)
			otherProject := testsupport.TempDir(t)

			// A real session belonging to a DIFFERENT project, in its own
			// bucket under $TERVA_HOME/sessions.
			otherDir := core.SessionsDir(home, otherProject)
			if err := os.MkdirAll(otherDir, 0o700); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(otherDir, "20260101-000000-deadbeef.jsonl")
			writeSessionFixture(t, other, otherProject, "their prompt", "their findings")

			tool := &SessionInspectTool{TervaHome: home, CWD: cwd, Sandbox: transcriptSandbox(t, home, cwd, state)}
			res := inspectByPath(t, tool, `{"path":`+jsonStr(other)+`}`)
			if res.IsError {
				t.Fatalf("another project's transcript must inspect, got: %q", inspectText(t, res))
			}
			if got := inspectText(t, res); !strings.Contains(got, "their findings") {
				t.Errorf("listing did not show the transcript's events: %q", got)
			}
		})
	}
}

// The carve-out is narrow on two axes, and this pins both. Only a .jsonl, and
// only under a registered transcript root — so the credentials sitting one
// directory up stay refused on the very route that now reads transcripts.
//
// Without the extension test, any JSONL under the root would pass. Without the
// root test, session_inspect would become a general-purpose reader for any
// file named .jsonl anywhere on the deny list.
func TestSessionInspectPathStillRefusesCredentials(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)

	sessions := core.SessionsDir(home, cwd)
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A credential that happens to carry the transcript extension. The deny
	// list covers auth.json by exact path, and this is the shape that would
	// slip past a check keyed on the extension alone.
	for _, rel := range []string{"auth.json", "logs/bot.jsonl"} {
		if err := os.WriteFile(filepath.Join(home, rel), []byte(`{"token":"synthetic"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tool := &SessionInspectTool{TervaHome: home, CWD: cwd, Sandbox: transcriptSandbox(t, home, cwd, "locked")}
	for _, rel := range []string{"auth.json", "logs/bot.jsonl"} {
		res := inspectByPath(t, tool, `{"path":`+jsonStr(filepath.Join(home, rel))+`}`)
		if !res.IsError {
			t.Errorf("%s must stay refused on the transcript route, got: %q", rel, inspectText(t, res))
			continue
		}
		if got := inspectText(t, res); strings.Contains(got, "synthetic") {
			t.Errorf("the refusal for %s leaked the file: %q", rel, got)
		}
	}
}

// The asymmetry is the whole design, so it gets a test rather than a comment:
// session_inspect reads a transcript, and every raw route still refuses it.
//
// This is what makes the change a REDUCTION in what leaks. The refusal the raw
// routes give now names the reader that works, because a dead end is what sent
// the model to `cp` and an unredacted read of the copy.
//
// All three jail states, because the ticket reported the inconsistency in two
// of them: unjailed, the raw reader worked and the sanctioned one refused;
// jailed, every route refused.
func TestTranscriptCarveOutOpensOnlyTheSanctionedReader(t *testing.T) {
	for _, state := range []string{"locked", "unlocked", "initially-unjailed"} {
		t.Run(state, func(t *testing.T) {
			home := testsupport.TempDir(t)
			cwd := testsupport.TempDir(t)
			sessions := core.SessionsDir(home, cwd)
			if err := os.MkdirAll(sessions, 0o700); err != nil {
				t.Fatal(err)
			}
			transcript := filepath.Join(sessions, "20260101-000000-deadbeef.jsonl")
			writeSessionFixture(t, transcript, cwd, "my prompt", "my findings")

			sb := transcriptSandbox(t, home, cwd, state)
			args := mustJSON(t, map[string]any{"path": transcript, "pattern": "findings"})

			if _, err := (&ReadTool{CWD: cwd, Sandbox: sb}).Execute(context.Background(), args, nil); err == nil {
				t.Error("read must still refuse a transcript")
			}
			if _, err := (&GrepTool{CWD: cwd, Sandbox: sb}).Execute(context.Background(), args, nil); err == nil {
				t.Error("grep must still refuse a transcript")
			}
			pub := &stubPublisher{}
			if _, err := (&ShareFileTool{CWD: cwd, Sandbox: sb, Publisher: pub}).Execute(context.Background(), args, nil); err == nil {
				t.Error("share_file must still refuse a transcript")
			}
			if len(pub.calls) != 0 {
				t.Fatal("share_file published a transcript")
			}
			err := sb.CheckCommand("cat '" + filepath.ToSlash(transcript) + "'")
			if err == nil {
				t.Fatal("bash must still refuse a transcript named literally")
			}
			// The refusal routes to the reader that works. A dead end is what
			// produced the `cp` this change exists to remove.
			if !strings.Contains(err.Error(), "session_inspect") {
				t.Errorf("the refusal should name the sanctioned reader, got: %v", err)
			}

			// And the sanctioned reader reads it, in the same state.
			tool := &SessionInspectTool{TervaHome: home, CWD: cwd, Sandbox: sb}
			res := inspectByPath(t, tool, `{"path":`+jsonStr(transcript)+`}`)
			if res.IsError {
				t.Fatalf("session_inspect must read a transcript, got: %q", inspectText(t, res))
			}
		})
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
