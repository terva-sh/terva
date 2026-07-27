package workflow

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/workflow/runs"
)

// TW-042, the WRITE half. A run directory held only journal.jsonl, and the
// journal holds completed calls — so a finished run could not be found again.
// The id lived in the narration stream and the resume hint, both on stderr.
// Survivable when a run FAILS (the CLI prints the hint on the way out); not
// survivable when it is INTERRUPTED, which is exactly when nobody has the
// terminal.
//
// The reader's own tests live in runs/record_test.go — these drive the engine
// into producing a record, which is the only thing that needs the engine.
func TestAnInterruptedRunLeavesAFindableRecord(t *testing.T) {
	opts := runOpts(t)
	opts.ScriptPath = "/plans/review.js"
	opts.CWD = "/work/repo"

	// A script that dies partway: the first agent completes and journals, the
	// second throws. That is the shape of the real interruption.
	const script = `export const meta = { name: 'half-run', description: 'x' }
await agent('first slice', { label: 'one' })
throw new Error('interrupted')
`
	if _, err := Run(context.Background(), &fakeEngine{}, []byte(script), opts); err == nil {
		t.Fatal("expected the run to fail")
	}

	recs, err := runs.ListRecords(opts.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("want 1 record, got %d — a stopped run left nothing to find", len(recs))
	}
	r := recs[0]
	if r.Name != "half-run" {
		t.Errorf("record lost the script name: %q", r.Name)
	}
	if r.ScriptAt != "/plans/review.js" || r.CWD != "/work/repo" {
		t.Errorf("record lost its launch coordinates: %q %q", r.ScriptAt, r.CWD)
	}
	if !strings.Contains(r.Script, "first slice") {
		t.Error("the script source was not recorded — a run cannot be read back without the launching host's filesystem")
	}
	if done := runs.CompletedCalls(opts.Root, r.RunID); done != 1 {
		t.Errorf("journaled %d completed calls, want 1 — the resumable work is what makes the record worth having", done)
	}
}

// A completed run's output must be readable without re-running the script —
// the reports were on disk the whole time and unreachable in practice.
func TestResultsAreReadableAfterTheRun(t *testing.T) {
	opts := runOpts(t)
	res, err := Run(context.Background(), &fakeEngine{}, []byte(testScript), opts)
	if err != nil {
		t.Fatal(err)
	}
	got, err := runs.Results(opts.Root, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 journaled results, got %d", len(got))
	}
	// TW-041's label rides along, which is what makes a result attributable to
	// a slice rather than to an opaque agent id.
	var labels []string
	for _, r := range got {
		labels = append(labels, r.Label)
	}
	for _, want := range []string{"a", "b"} {
		if !strings.Contains(strings.Join(labels, ","), want) {
			t.Errorf("result labels %v are missing %q", labels, want)
		}
	}
}

// The rerun guard: a second launch of the same script in the same cwd must find
// the stopped run. Matched on source, not path — resume keys on the script's
// content, so an edited file at the same path would replay nothing.
func TestFindIncompleteMatchesSourceNotPath(t *testing.T) {
	opts := runOpts(t)
	opts.CWD = "/work/repo"
	opts.ScriptPath = "/plans/a.js"
	const script = `export const meta = { name: 'stopper', description: 'x' }
await agent('slice', { label: 'one' })
throw new Error('stop')
`
	if _, err := Run(context.Background(), &fakeEngine{}, []byte(script), opts); err == nil {
		t.Fatal("expected failure")
	}

	if _, done, ok := runs.FindIncomplete(opts.Root, script, "/work/repo"); !ok || done != 1 {
		t.Errorf("same source + cwd should match: ok=%v done=%d", ok, done)
	}
	if _, _, ok := runs.FindIncomplete(opts.Root, script, "/somewhere/else"); ok {
		t.Error("a different cwd must not match — those agents ran against other files")
	}
	if _, _, ok := runs.FindIncomplete(opts.Root, script+"\n// edited\n", "/work/repo"); ok {
		t.Error("an edited script must not match: resume keys on content, so it would replay nothing")
	}
}
