package swarm

import "testing"

// TestIngestEventRecordsWorkerCost pins the cost accounting: a task_end carrying
// cost_usd sets the agent's cost on the snapshot, and because a worker reports a
// CUMULATIVE session total (a Claude result's total_cost_usd already sums prior
// turns) it is LAST-WINS — a later, larger total replaces the earlier one — never
// summed. A task_end with no cost must not zero a recorded figure.
func TestIngestEventRecordsWorkerCost(t *testing.T) {
	a := &Agent{ID: "w-1"}
	sink := agentSink{a: a}

	// Turn 1: the cumulative total so far.
	IngestEvent(NewEvent("task_end", map[string]any{"step": 1, "cost_usd": 0.015}), nil, sink, a)
	if got := a.Snapshot().CostUSD; got != 0.015 {
		t.Errorf("cost after turn 1 = %v, want 0.015", got)
	}

	// Turn 2: a LARGER cumulative total (turn 2's spend added to turn 1's). Summing
	// would give 0.0346 — the bug this pins against; last-wins gives 0.0196.
	IngestEvent(NewEvent("task_end", map[string]any{"step": 2, "cost_usd": 0.0196}), nil, sink, a)
	if got := a.Snapshot().CostUSD; got != 0.0196 {
		t.Errorf("cost after turn 2 = %v, want the latest cumulative 0.0196 (last-wins, not summed)", got)
	}

	// A costless task_end (a terva done today) must not clobber the figure.
	IngestEvent(NewEvent("task_end", map[string]any{"step": 3}), nil, sink, a)
	if got := a.Snapshot().CostUSD; got != 0.0196 {
		t.Errorf("a costless task_end zeroed the recorded cost: %v", got)
	}
}

// TestIngestEventRecordsUsageCost pins the terva/native cost route: a
// core.WireEvent `usage` event carries cumulative.cost_usd every turn (the rpc
// wire and the native child both emit it), so a terva-backed worker's spend
// lights up LIVE — mid-run, not only at the terminal event — and is likewise
// last-wins because cumulative.cost_usd is a running session total.
func TestIngestEventRecordsUsageCost(t *testing.T) {
	a := &Agent{ID: "w-3"}
	sink := agentSink{a: a}

	usage := func(cum float64) Event {
		return NewEvent("usage", map[string]any{
			"usage":      map[string]any{"cost_usd": cum},                 // per-turn delta; ignored
			"cumulative": map[string]any{"cost_usd": cum, "input": 100.0}, // the running total we read
		})
	}

	// Turn 1 usage arrives well before any task boundary — the point of the live
	// route is that cost shows up without waiting for the terminal event.
	IngestEvent(usage(0.004), nil, sink, a)
	if got := a.Snapshot().CostUSD; got != 0.004 {
		t.Errorf("cost after first usage = %v, want 0.004 (live, before task_end)", got)
	}

	// A larger cumulative total replaces the earlier one; summing would give
	// 0.011, the bug last-wins guards against.
	IngestEvent(usage(0.007), nil, sink, a)
	if got := a.Snapshot().CostUSD; got != 0.007 {
		t.Errorf("cost after second usage = %v, want the latest cumulative 0.007", got)
	}

	// The terminal `done`→task_end of a terva worker carries no cost (its cost
	// already rode the usage events) and must not zero the recorded figure.
	IngestEvent(NewEvent("task_end", map[string]any{"step": 1}), nil, sink, a)
	if got := a.Snapshot().CostUSD; got != 0.007 {
		t.Errorf("a costless task_end zeroed the usage-tracked cost: %v", got)
	}

	// A malformed usage event (no cumulative block) is ignored, not a panic.
	IngestEvent(NewEvent("usage", map[string]any{"usage": map[string]any{"cost_usd": 9.99}}), nil, sink, a)
	if got := a.Snapshot().CostUSD; got != 0.007 {
		t.Errorf("a usage event without a cumulative block changed cost: %v", got)
	}
}

// TestCostIgnoresNonTaskEnd: a per-turn turn_end (the core agent's intermediate
// event, which carries "stop" not "step") is neither a task boundary nor a cost
// carrier — cost rides only `usage` and `task_end` events — so a stray
// cost-shaped field on it must not be mistaken for spend.
func TestCostIgnoresNonTaskEnd(t *testing.T) {
	a := &Agent{ID: "w-2"}
	sink := agentSink{a: a}
	// A core per-turn turn_end with a stray cost-shaped field must be ignored.
	IngestEvent(NewEvent("turn_end", map[string]any{"stop": "end", "cost_usd": 9.99}), nil, sink, a)
	if got := a.Snapshot().CostUSD; got != 0 {
		t.Errorf("a per-turn turn_end set cost = %v, want 0 (only usage/task_end count)", got)
	}
}
