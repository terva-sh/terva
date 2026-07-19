package modes

import (
	"testing"

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
		ID:      "wk1",
		Task:    "review the diff",
		Status:  "running",
		CostUSD: 0.0042,
		Backend: "claude",
	})
	if snap.CostUSD != 0.0042 {
		t.Errorf("CostUSD = %v, want 0.0042 (an attached dialog would show no worker spend)", snap.CostUSD)
	}
	if snap.Backend != "claude" {
		t.Errorf("Backend = %q, want %q (an attached dialog couldn't tell a Claude worker from a native child)", snap.Backend, "claude")
	}
}
