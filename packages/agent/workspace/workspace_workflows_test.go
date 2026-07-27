package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/workflow/runs"
	"terva.sh/terva/packages/testsupport"
)

// workflowFixture builds a workspace whose swarm root is a tempdir, plus the
// workflow run root underneath it, and returns both.
func workflowFixture(t *testing.T) (*Workspace, string) {
	t.Helper()
	tmp := testsupport.TempDir(t)
	w := &Workspace{root: tmp, cwd: tmp, sessions: map[string]*wsSession{},
		swarm: swarm.New(swarm.Config{Root: tmp, RepoRoot: tmp})}
	root := runs.Root(tmp)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return w, root
}

func writeRun(t *testing.T, root string, rec runs.Record, journal string) {
	t.Helper()
	if err := runs.WriteRecord(root, rec); err != nil {
		t.Fatal(err)
	}
	if journal != "" {
		if err := os.WriteFile(filepath.Join(root, rec.RunID, "journal.jsonl"), []byte(journal), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The dashboard's whole premise: a run made by a separate foreground process,
// in a terminal that is gone, is readable here. Nothing in this test involves
// the engine — the daemon reads the artifacts, and it must not need the engine
// (or its build tag) to do so.
func TestWorkflowRunsListsWhatTheEngineLeftBehind(t *testing.T) {
	w, root := workflowFixture(t)
	writeRun(t, root, runs.Record{
		RunID: "wf_0000000000aa", Name: "review", Started: "2026-07-26T10:00:00Z",
		ScriptAt: "/plans/review.js", CWD: "/work/repo", Script: "export const meta = {}",
	}, `{"type":"result","key":"k1","label":"bugs","result":"\"found\""}`+"\n")
	writeRun(t, root, runs.Record{
		RunID: "wf_0000000000bb", Name: "audit", Started: "2026-07-26T12:00:00Z",
		Ended: "2026-07-26T12:30:00Z", Agents: 4, Cached: 1, CostUSD: 2.5,
	}, "")

	got, err := w.WorkflowRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 runs, got %d", len(got))
	}
	// Newest first, so the run an operator just lost is the first row.
	if got[0].ID != "wf_0000000000bb" {
		t.Errorf("wrong order: %s first", got[0].ID)
	}
	if got[0].Status != "done" || got[0].CostUSD != 2.5 || got[0].Agents != 4 {
		t.Errorf("finished run projected wrong: %+v", got[0])
	}
	if got[0].Resumable {
		t.Error("a finished run is not resumable")
	}

	// The interrupted one is the row that matters: status honest, and the
	// completed count present so "1 agent already on disk" is visible without
	// opening it.
	stopped := got[1]
	if stopped.Status != "incomplete" {
		t.Errorf("status %q — a record with no end is incomplete, never running", stopped.Status)
	}
	if stopped.Completed != 1 {
		t.Errorf("completed %d, want 1 — the journaled work is what makes the row worth reading", stopped.Completed)
	}
	if !stopped.Resumable {
		t.Error("an interrupted run with a journaled result is resumable")
	}
	if stopped.ScriptAt != "/plans/review.js" || stopped.CWD != "/work/repo" {
		t.Errorf("lost the launch coordinates: %+v", stopped)
	}
}

// The ask this was built for: read the script without shell access to the host
// that ran it. The source is the one the run RECORDED, not whatever sits at
// ScriptAt today.
func TestWorkflowRunServesTheRecordedScriptAndResults(t *testing.T) {
	w, root := workflowFixture(t)
	const src = "export const meta = { name: 'review' }\nawait agent('slice one')\n"
	writeRun(t, root, runs.Record{
		RunID: "wf_0000000000aa", Name: "review", Started: "2026-07-26T10:00:00Z",
		ScriptAt: "/gone/review.js", Script: src,
	}, `{"type":"started","key":"k1","label":"bugs"}`+"\n"+
		`{"type":"result","key":"k1","agent_id":"bugs-7f2a","label":"bugs","result":{"finding":"one"}}`+"\n")

	v, err := w.WorkflowRun(context.Background(), ctrlproto.WorkflowGetParams{ID: "wf_0000000000aa"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Script != src {
		t.Errorf("script came back as %q — it must be the source as it ran", v.Script)
	}
	if len(v.Results) != 1 {
		t.Fatalf("want 1 result, got %d (a `started` row is not a result)", len(v.Results))
	}
	r := v.Results[0]
	if r.Label != "bugs" || r.AgentID != "bugs-7f2a" {
		t.Errorf("result lost its attribution: %+v", r)
	}
	if r.Bytes != len(r.Result) {
		t.Errorf("bytes %d does not describe the %d-byte result it ships with", r.Bytes, len(r.Result))
	}
	// Forwarded verbatim as raw JSON — a schema deliverable stays an object, not
	// a string the client has to unwrap twice.
	if string(r.Result) != `{"finding":"one"}` {
		t.Errorf("result body %s", r.Result)
	}
}

// The run id becomes a path element and arrives from a network client. Before
// ValidRunID it was joined onto the root unchecked, which reads any run.json on
// the machine.
func TestWorkflowRunRefusesAnIDThatIsNotOne(t *testing.T) {
	w, root := workflowFixture(t)
	// A record one level up from the run root — i.e. inside the swarm home,
	// which a traversal would reach.
	outside := filepath.Dir(root)
	if err := runs.WriteRecord(outside, runs.Record{RunID: "secrets", Script: "PRIVATE"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"../secrets",
		"..",
		"wf_0000000000aa/../../secrets",
		"wf_ZZZZ",
		"",
	} {
		v, err := w.WorkflowRun(context.Background(), ctrlproto.WorkflowGetParams{ID: id})
		if err == nil {
			t.Errorf("id %q was accepted, returning %+v", id, v)
		}
		if strings.Contains(v.Script, "PRIVATE") {
			t.Errorf("id %q read a record outside the run root", id)
		}
	}
}

// A daemon with no swarm says so rather than reporting an empty dashboard,
// which would read as "no runs" when the truth is "not looked".
func TestWorkflowRunsWithoutASwarmIsUnsupportedNotEmpty(t *testing.T) {
	w := &Workspace{sessions: map[string]*wsSession{}}
	if _, err := w.WorkflowRuns(context.Background()); err == nil {
		t.Error("a swarmless host reported an empty run list instead of refusing")
	}
}

// Runs that predate run records are exactly the ones an operator hunts for: the
// directory and its journal are there, run.json is not.
func TestWorkflowRunOpensARecordlessRun(t *testing.T) {
	w, root := workflowFixture(t)
	dir := filepath.Join(root, "wf_0000000000cc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"),
		[]byte(`{"type":"result","key":"k","result":"\"old\""}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := w.WorkflowRun(context.Background(), ctrlproto.WorkflowGetParams{ID: "wf_0000000000cc"})
	if err != nil {
		t.Fatalf("a recordless run with journaled work is not a 404: %v", err)
	}
	if len(v.Results) != 1 || v.Run.Completed != 1 {
		t.Errorf("its journal did not come through: %+v", v)
	}
	// An id with no directory at all is still a miss.
	if _, err := w.WorkflowRun(context.Background(), ctrlproto.WorkflowGetParams{ID: "wf_0000000000dd"}); err == nil {
		t.Error("an id with nothing behind it should not resolve")
	}
}
