package runs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// Liveness is the one fact the run record could never supply. The pid was
// always there and always useless — it lies after reuse — so a run that was
// working and a run whose process had died read identically, and an operator
// either waited on nothing or re-paid for work still in flight.
//
// A heartbeat cannot lie the same way: it is a timestamp the running process
// has to keep rewriting, so if it stops advancing, whatever was advancing it is
// gone.

func mustBeat(t *testing.T, root, id string, at time.Time) {
	t.Helper()
	if err := Beat(root, id, at); err != nil {
		t.Fatalf("Beat: %v", err)
	}
}

func TestStatusTellsRunningFromCrashed(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	stamp := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339) }

	cases := []struct {
		name string
		rec  Record
		want Status
	}{
		{"a fresh heartbeat is a live run", Record{Started: stamp(-time.Hour), Heartbeat: stamp(-5 * time.Second)}, StatusRunning},
		{"a stopped heartbeat is a crash", Record{Started: stamp(-time.Hour), Heartbeat: stamp(-10 * time.Minute)}, StatusCrashed},
		// Three intervals of slack: a loaded machine or a descheduled process
		// must not be declared dead while it is working.
		{"one missed tick is not a crash", Record{Heartbeat: stamp(-(HeartbeatInterval + time.Second))}, StatusRunning},
		{"three missed ticks is", Record{Heartbeat: stamp(-(3*HeartbeatInterval + time.Second))}, StatusCrashed},
		// The pre-liveness answer, preserved exactly.
		{"no heartbeat at all stays incomplete", Record{Started: stamp(-time.Hour)}, StatusIncomplete},
		{"an unparseable heartbeat stays incomplete", Record{Heartbeat: "yesterday"}, StatusIncomplete},
		// A closing record wins over any tick: the run said it was finished.
		{"ended beats a stale heartbeat", Record{Ended: stamp(-time.Hour), Heartbeat: stamp(-time.Hour)}, StatusDone},
		{"ended with an error is failed", Record{Ended: stamp(-time.Hour), Err: "boom"}, StatusFailed},
		// Clock skew between the writing host and this reader must not read as
		// death; over-reporting life is recoverable by looking again.
		{"a future heartbeat is not a crash", Record{Heartbeat: stamp(time.Hour)}, StatusRunning},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.rec.StatusAt(now); got != c.want {
				t.Errorf("StatusAt = %q, want %q", got, c.want)
			}
		})
	}
}

// Resuming a LIVE run would start a second process against the same journal —
// double-paying for the calls in flight and racing the writer that owns the
// file. Before liveness existed the case could not be detected; now it must be.
func TestALiveRunIsNotOfferedForResume(t *testing.T) {
	now := time.Now()
	live := Record{Heartbeat: now.UTC().Format(time.RFC3339)}
	if live.Resumable(4) {
		t.Error("a running run must not be advertised as resumable")
	}
	dead := Record{Heartbeat: now.Add(-time.Hour).UTC().Format(time.RFC3339)}
	if !dead.Resumable(4) {
		t.Error("a crashed run sitting on completed work is exactly what resume is for")
	}
	if dead.Resumable(0) {
		t.Error("a crashed run with nothing journaled has nothing to replay")
	}
	done := Record{Ended: now.UTC().Format(time.RFC3339)}
	if done.Resumable(4) {
		t.Error("a finished run has nothing left to replay")
	}
}

func TestBeatStampsTheRecordWithoutLosingIt(t *testing.T) {
	root := testsupport.TempDir(t)
	rec := Record{RunID: "wf_0123456789ab", Name: "review", Started: "2026-07-27T12:00:00Z", Script: "await agent('x')"}
	if err := WriteRecord(root, rec); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}

	at := time.Date(2026, 7, 27, 12, 0, 30, 0, time.UTC)
	mustBeat(t, root, rec.RunID, at)

	got, err := ReadRecord(root, rec.RunID)
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	if got.Heartbeat != at.Format(time.RFC3339) {
		t.Errorf("heartbeat = %q, want %q", got.Heartbeat, at.Format(time.RFC3339))
	}
	// The rest of the record has to survive the restamp: Beat re-reads and
	// rewrites, and a beat that dropped the script would destroy the one copy
	// of what actually ran.
	if got.Script != rec.Script || got.Name != rec.Name || got.Started != rec.Started {
		t.Errorf("Beat lost part of the record: %+v", got)
	}
}

// A tick that lands after the closing write must not resurrect the run or drop
// the counts the ending carried.
func TestBeatDoesNotReopenAFinishedRun(t *testing.T) {
	root := testsupport.TempDir(t)
	rec := Record{RunID: "wf_0123456789ab", Started: "2026-07-27T12:00:00Z",
		Ended: "2026-07-27T12:05:00Z", Agents: 6, CostUSD: 1.25}
	if err := WriteRecord(root, rec); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	mustBeat(t, root, rec.RunID, time.Now())

	got, _ := ReadRecord(root, rec.RunID)
	if got.Heartbeat != "" {
		t.Error("a finished run should not be restamped")
	}
	if got.Agents != 6 || got.CostUSD != 1.25 {
		t.Errorf("the closing record was clobbered: %+v", got)
	}
	if got.Status() != StatusDone {
		t.Errorf("status = %q, want done", got.Status())
	}
}

// The in-flight data was on disk all along — every reader filtered to
// type=="result" and threw the started rows away.
func TestInFlightIsStartedMinusResulted(t *testing.T) {
	root := testsupport.TempDir(t)
	dir := filepath.Join(root, "wf_0123456789ab")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rows := []journalRow{
		{Type: "started", Key: "k1", AgentID: "a1", Label: "review:correctness"},
		{Type: "started", Key: "k2", AgentID: "a2", Label: "review:security"},
		{Type: "result", Key: "k1", AgentID: "a1", Label: "review:correctness", Result: json.RawMessage(`{"ok":1}`)},
		{Type: "started", Key: "k3", AgentID: "a3", Label: "review:perf"},
		// A resume re-journals a started row for a call it is retrying; the same
		// call listed twice would read as two agents doing one job.
		{Type: "started", Key: "k2", AgentID: "a2", Label: "review:security"},
	}
	var buf []byte
	for _, r := range rows {
		b, _ := json.Marshal(r)
		buf = append(buf, append(b, '\n')...)
	}
	if err := os.WriteFile(filepath.Join(dir, journalName), buf, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := InFlightCalls(root, "wf_0123456789ab")
	if err != nil {
		t.Fatalf("InFlightCalls: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("in flight = %+v, want 2 (k2 and k3)", got)
	}
	if got[0].Label != "review:security" || got[1].Label != "review:perf" {
		t.Errorf("in flight should be in start order, got %+v", got)
	}
	// And the completed count is untouched by the started rows.
	if n := CompletedCalls(root, "wf_0123456789ab"); n != 1 {
		t.Errorf("CompletedCalls = %d, want 1", n)
	}
}

// Reading a run must never create anything — a dashboard lists every run on the
// machine, and a reader that minted journals would write into all of them.
func TestReadingInFlightDoesNotCreateAJournal(t *testing.T) {
	root := testsupport.TempDir(t)
	if _, err := InFlightCalls(root, "wf_0123456789ab"); err == nil {
		t.Error("reading an absent run should fail, not succeed silently")
	}
	if _, err := os.Stat(filepath.Join(root, "wf_0123456789ab")); !os.IsNotExist(err) {
		t.Error("reading created the run directory")
	}
}
