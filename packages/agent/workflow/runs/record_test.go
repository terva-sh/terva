package runs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// These are the READER's tests: what a run directory says to something that did
// not produce it. The tests that drive the engine into producing one live with
// the engine (../record_test.go), which is the same split as the packages.

// A record with no Ended is `incomplete`, never `running`: telling those apart
// needs liveness, and claiming "running" about a dead run is worse than saying
// nothing. Both lead to the same next action.
func TestStatusDerivation(t *testing.T) {
	for _, tc := range []struct {
		name string
		rec  Record
		want Status
	}{
		{"never closed", Record{Started: "t"}, StatusIncomplete},
		{"closed clean", Record{Started: "t", Ended: "u"}, StatusDone},
		{"closed with an error", Record{Started: "t", Ended: "u", Err: "boom"}, StatusFailed},
	} {
		if got := tc.rec.Status(); got != tc.want {
			t.Errorf("%s: status %q, want %q", tc.name, got, tc.want)
		}
	}
	// Resumable needs BOTH: unfinished, and something on disk to replay.
	if (Record{Started: "t"}).Resumable(0) {
		t.Error("an incomplete run with nothing journaled is not worth resuming")
	}
	// A failed run is resumable too: the script threw, but the agents that
	// finished before it are on disk and would otherwise be paid for twice.
	if !(Record{Started: "t", Ended: "u", Err: "boom"}).Resumable(1) {
		t.Error("a failed run with journaled work is resumable — that is what the CLI's resume hint has always been for")
	}
	if !(Record{Started: "t"}).Resumable(1) {
		t.Error("an incomplete run with a journaled result is resumable")
	}
	if (Record{Started: "t", Ended: "u"}).Resumable(5) {
		t.Error("a finished run is not resumable")
	}
}

// Runs made before run records existed are exactly the ones an operator hunts
// for. Listing must not drop a directory just because it has no run.json.
func TestListIncludesRunsWithoutARecord(t *testing.T) {
	root := testsupport.TempDir(t)
	legacy := filepath.Join(root, "wf_deadbeef01")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "journal.jsonl"), []byte(`{"type":"result","key":"k","result":"\"x\""}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	recs, err := ListRecords(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].RunID != "wf_deadbeef01" {
		t.Fatalf("a recordless run directory was dropped: %+v", recs)
	}
	if recs[0].Status() != StatusIncomplete {
		t.Errorf("status %q, want incomplete", recs[0].Status())
	}
	if n := CompletedCalls(root, "wf_deadbeef01"); n != 1 {
		t.Errorf("its journal still has %d result(s), want 1", n)
	}
}

// Newest first, so the run an operator just lost is the first line.
func TestListIsNewestFirst(t *testing.T) {
	root := testsupport.TempDir(t)
	base := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	for i, id := range []string{"wf_old", "wf_mid", "wf_new"} {
		if err := WriteRecord(root, Record{RunID: id, Started: base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)}); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := ListRecords(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 || recs[0].RunID != "wf_new" || recs[2].RunID != "wf_old" {
		t.Fatalf("wrong order: %v", []string{recs[0].RunID, recs[1].RunID, recs[2].RunID})
	}
}

// Reading a run must never take a write handle on someone else's journal: the
// dashboard lists every run on the machine, including ones with a live writer.
func TestReadingDoesNotCreateAJournal(t *testing.T) {
	root := testsupport.TempDir(t)
	if err := WriteRecord(root, Record{RunID: "wf_norun", Started: "2026-07-26T12:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if n := CompletedCalls(root, "wf_norun"); n != 0 {
		t.Fatalf("counted %d completed calls in a run that never journaled", n)
	}
	if _, err := os.Stat(filepath.Join(root, "wf_norun", journalName)); !os.IsNotExist(err) {
		t.Error("counting a run's completed calls created its journal file — a reader must leave the run directory as it found it")
	}
}
