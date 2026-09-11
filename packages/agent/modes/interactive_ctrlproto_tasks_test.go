package modes

import (
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// The attached /swarm dialog rebuilds an AgentSnapshot from the wire TaskInfo
// via taskInfoSnapshot; it must carry the worker-shaped fields (cost, backend)
// or an ATTACHED dialog lies by omission relative to a local one — the dialog
// already renders cost on its status line, and backend is what tells a Claude
// worker apart from a native child. Regression guard for R3 (the attach-fidelity
// bug) of docs/reviews/2026-07-15-eaw-review-from-orchestration-frontend.md.
func TestTaskInfoSnapshotCarriesWorkerFields(t *testing.T) {
	snap := taskInfoSnapshot(ctrlproto.TaskInfo{
		ID:        "wk1",
		Task:      "review the diff",
		Status:    "running",
		Turns:     12,
		ToolCalls: 34,
		LastEvent: "2026-07-04T10:03:00Z",
		CostUSD:   0.0042,
		Backend:   "claude",
		Reasoning: "medium",
	})
	if snap.Turns != 12 || snap.ToolCalls != 34 {
		t.Errorf("progress = (%d,%d), want (12,34)", snap.Turns, snap.ToolCalls)
	}
	wantLastEvent, _ := time.Parse(time.RFC3339, "2026-07-04T10:03:00Z")
	if !snap.LastEvent.Equal(wantLastEvent) {
		t.Errorf("LastEvent = %v, want %v", snap.LastEvent, wantLastEvent)
	}
	if snap.CostUSD != 0.0042 {
		t.Errorf("CostUSD = %v, want 0.0042 (an attached dialog would show no worker spend)", snap.CostUSD)
	}
	if snap.Backend != "claude" {
		t.Errorf("Backend = %q, want %q (an attached dialog couldn't tell a Claude worker from a native child)", snap.Backend, "claude")
	}
	if snap.Reasoning != "medium" {
		t.Errorf("Reasoning = %q, want %q (an attached dialog would lose the worker's thinking level)", snap.Reasoning, "medium")
	}
}
