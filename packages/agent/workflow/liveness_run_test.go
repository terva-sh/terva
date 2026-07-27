package workflow

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/workflow/runs"
)

// The engine half of liveness: a run must read as RUNNING from the moment it
// starts, and stop reading that way the moment its process is gone.
//
// The record is stamped alive at launch rather than only on the first tick,
// which is what makes the very case that motivated this legible: a run watched
// in its first seconds. Waiting a full interval before admitting the run is
// alive would leave a fresh run indistinguishable from a crashed one for
// exactly as long as someone is most likely to be looking at it.

// midRunEngine runs fn while the first agent is awaiting its result, so a test
// can inspect the record with the run genuinely in flight.
//
// The probe hangs off AwaitTask, not Spawn: the started row is journaled AFTER
// Spawn returns, because it carries the handle's agent id. A probe inside Spawn
// runs before that row exists and reports zero agents in flight — which looks
// exactly like the reader being broken.
type midRunEngine struct {
	fakeEngine
	fired bool
	fn    func()
}

type probeHandle struct {
	Handle
	fn func()
}

func (h probeHandle) AwaitTask(ctx context.Context) Outcome {
	if h.fn != nil {
		h.fn()
	}
	return h.Handle.AwaitTask(ctx)
}

func (e *midRunEngine) Spawn(ctx context.Context, req swarm.SpawnRequest) (Handle, error) {
	h, err := e.fakeEngine.Spawn(ctx, req)
	if err != nil || e.fired || e.fn == nil {
		return h, err
	}
	e.fired = true
	return probeHandle{Handle: h, fn: e.fn}, nil
}

func TestARunReadsAsRunningWhileItIsRunning(t *testing.T) {
	opts := runOpts(t)

	var midStatus runs.Status
	var midInFlight int
	eng := &midRunEngine{fn: func() {
		recs, err := runs.ListRecords(opts.Root)
		if err != nil || len(recs) == 0 {
			return
		}
		midStatus = recs[0].Status()
		// The agent that triggered this has journaled its started row, so the
		// in-flight read should see it before any result exists.
		f, _ := runs.InFlightCalls(opts.Root, recs[0].RunID)
		midInFlight = len(f)
	}}

	const script = `export const meta = { name: 'live', description: 'x' }
await agent('first slice', { label: 'one' })
await agent('second slice', { label: 'two' })
`
	if _, err := Run(context.Background(), eng, []byte(script), opts); err != nil {
		t.Fatalf("run: %v", err)
	}

	if midStatus != runs.StatusRunning {
		t.Errorf("mid-run status = %q, want running — a run in flight must not read as crashed or unknown", midStatus)
	}
	if midInFlight == 0 {
		t.Error("no agent read as in flight while one was running — the started rows are not reaching the reader")
	}

	// And once it closes, it is done: the heartbeat must not keep it looking alive.
	recs, err := runs.ListRecords(opts.Root)
	if err != nil || len(recs) != 1 {
		t.Fatalf("records: %v %d", err, len(recs))
	}
	if got := recs[0].Status(); got != runs.StatusDone {
		t.Errorf("finished run reads %q, want done", got)
	}
	if recs[0].Agents != 2 {
		t.Errorf("the closing record lost its counts (agents=%d) — a late heartbeat clobbered it", recs[0].Agents)
	}
	if left, _ := runs.InFlightCalls(opts.Root, recs[0].RunID); len(left) != 0 {
		t.Errorf("a finished run still reports %d agents in flight", len(left))
	}
}

// A run that dies without writing its ending is the case the pid could never
// answer. Here the script throws, so the record IS closed — the crash path
// proper is the one where nothing closes it, which liveness_test.go covers on
// the record directly. What matters here is that a failed run keeps its resume
// offer and does not claim to be running.
func TestAFailedRunDoesNotClaimToBeRunning(t *testing.T) {
	opts := runOpts(t)
	const script = `export const meta = { name: 'thrower', description: 'x' }
await agent('first slice', { label: 'one' })
throw new Error('interrupted')
`
	if _, err := Run(context.Background(), &fakeEngine{}, []byte(script), opts); err == nil {
		t.Fatal("expected the run to fail")
	}
	recs, err := runs.ListRecords(opts.Root)
	if err != nil || len(recs) != 1 {
		t.Fatalf("records: %v %d", err, len(recs))
	}
	r := recs[0]
	if got := r.Status(); got != runs.StatusFailed {
		t.Errorf("status = %q, want failed", got)
	}
	if !strings.Contains(r.Err, "interrupted") {
		t.Errorf("the failure was not recorded: %q", r.Err)
	}
	if !r.Resumable(runs.CompletedCalls(opts.Root, r.RunID)) {
		t.Error("a failed run sitting on a completed agent is exactly what resume is for")
	}
}
