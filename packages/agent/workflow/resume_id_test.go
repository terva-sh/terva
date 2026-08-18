package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/workflow/runs"
)

// runs.ValidRunID exists because the id becomes a path element, and its doc
// says callers taking an id from anywhere but their own ListRecords must pass
// it through first. Run was that caller and did not.
//
// The cheap half is a typo: --resume wf_abc123 (one character short) minted a
// brand-new run under that name and re-ran the whole script, which reads as
// "the resume found nothing" rather than as a mistake. The expensive half is
// that the id is joined onto the workflows root unchecked.
func TestAResumeIDThatIsNotARunIDIsRefused(t *testing.T) {
	for _, id := range []string{
		"wf_abc123",                     // a real id, one byte short — the typo
		"wf_ABC123DEF456",               // right length, wrong case
		"nope",                          // not the shape at all
		"../../../../etc",               // the traversal ValidRunID's doc names
		"wf_000000000000/../../escapee", // a valid prefix with a tail
	} {
		t.Run(id, func(t *testing.T) {
			opts := runOpts(t)
			opts.ResumeID = id

			res, err := Run(context.Background(), &fakeEngine{}, []byte(testScript), opts)
			if err == nil {
				t.Fatalf("--resume %q was accepted and ran as %q", id, res.RunID)
			}
			if !strings.Contains(err.Error(), "not a run id") {
				t.Errorf("error does not name the cause: %v", err)
			}
			// Nothing may be created under the root, including by a traversal
			// that lands somewhere legal-looking.
			entries, rerr := os.ReadDir(opts.Root)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if len(entries) != 0 {
				t.Errorf("the refused resume still created %d entr(ies) under the root: %v", len(entries), entries)
			}
		})
	}
}

// The complement: a real id must still resume. Without this, "refuse every
// ResumeID" would pass the test above.
func TestAMintedRunIDStillResumes(t *testing.T) {
	opts := runOpts(t)
	first, err := Run(context.Background(), &fakeEngine{}, []byte(testScript), opts)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !runs.ValidRunID(first.RunID) {
		t.Fatalf("Run minted %q, which its own validator rejects", first.RunID)
	}

	eng2 := &fakeEngine{}
	opts.ResumeID = first.RunID
	if _, err := Run(context.Background(), eng2, []byte(testScript), opts); err != nil {
		t.Fatalf("resuming a minted id: %v", err)
	}
	if eng2.spawns != 0 {
		t.Errorf("the resume spawned %d agents, want pure replay", eng2.spawns)
	}
}

// Record.Agents is documented as "the total the script asked for (spawned +
// replayed)". The runner wrote Result.Agents straight through, which is the
// narrower SPAWNED count — so a resumed run's total collapsed towards zero
// while runs.CompletedCalls, the numerator every reader pairs it with, counted
// the replays.
//
// A pure replay is the clean case: three calls, all replayed. The record said
// zero, which `terva workflow list` renders as a bare "3" and the TUI dialog as
// "3/?" — the total silently lost on exactly the runs a resume produces.
func TestAResumedRunRecordsTheTotalItWasAskedFor(t *testing.T) {
	opts := runOpts(t)
	first, err := Run(context.Background(), &fakeEngine{}, []byte(testScript), opts)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	eng2 := &fakeEngine{}
	opts.ResumeID = first.RunID
	second, err := Run(context.Background(), eng2, []byte(testScript), opts)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if second.Agents != 0 || second.CachedAgents != 3 {
		t.Fatalf("fixture is not a pure replay: spawned=%d cached=%d", second.Agents, second.CachedAgents)
	}

	rec, err := runs.ReadRecord(opts.Root, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Agents != 3 {
		t.Errorf("Agents = %d, want 3 — nothing spawned, but the script asked for three", rec.Agents)
	}
	if rec.Cached != 3 {
		t.Errorf("Cached = %d, want 3", rec.Cached)
	}
	// The invariant the readers assume: the numerator never exceeds the total.
	if done := runs.CompletedCalls(opts.Root, first.RunID); done > rec.Agents {
		t.Errorf("%d calls completed against a total of %d — the reader renders a ratio above one",
			done, rec.Agents)
	}
}

// The partial resume: one call edited, two replayed. The total must count all
// three, not the one that re-ran.
//
// Note what this does NOT assert. runs.CompletedCalls counts DISTINCT KEYS ever
// journaled, and an edited call mints a new key while the superseded one stays
// in the append-only journal — so the numerator here is 4 against a total of 3.
// That is a separate defect in the numerator, not this one, and pinning it as
// expected would bake it in.
func TestAPartialResumeCountsEveryCallTheScriptAskedFor(t *testing.T) {
	opts := runOpts(t)
	first, err := Run(context.Background(), &fakeEngine{}, []byte(testScript), opts)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	edited := strings.Replace(testScript, "'beta task'", "'beta task v2'", 1)
	eng2 := &fakeEngine{}
	opts.ResumeID = first.RunID
	second, err := Run(context.Background(), eng2, []byte(edited), opts)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if second.Agents != 1 || second.CachedAgents != 2 {
		t.Fatalf("fixture is not a PARTIAL resume: spawned=%d cached=%d", second.Agents, second.CachedAgents)
	}

	rec, err := runs.ReadRecord(opts.Root, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Agents != 3 {
		t.Errorf("Agents = %d, want 3 — one spawned plus two replayed is what the script asked for", rec.Agents)
	}
	if rec.Cached != 2 {
		t.Errorf("Cached = %d, want 2 — the replayed subset, which stays narrower than the total", rec.Cached)
	}
}

// A first run has nothing to replay, so total and spawned agree there. Kept so
// the fix cannot be read as "Agents is now always CompletedCalls" — the two
// counts are computed by different code and must agree on both paths.
func TestAFirstRunRecordsTheSameTotalItSpawned(t *testing.T) {
	opts := runOpts(t)
	res, err := Run(context.Background(), &fakeEngine{}, []byte(testScript), opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	rec, err := runs.ReadRecord(opts.Root, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Agents != res.Agents || rec.Cached != 0 {
		t.Errorf("first run: record agents=%d cached=%d, result spawned=%d", rec.Agents, rec.Cached, res.Agents)
	}
	if _, err := os.Stat(filepath.Join(opts.Root, res.RunID)); err != nil {
		t.Errorf("the run directory is not under the root: %v", err)
	}
}
