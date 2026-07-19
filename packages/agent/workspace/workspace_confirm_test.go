package workspace

import (
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// A worker's approval request must carry the worker id as a first-class field
// (PermissionRequest.Agent), not only baked into the callID string convention
// ("worker-<id>-<seq>"): a board correlates the ask to the worker's lane tile
// off the field, never by parsing the callID. Regression guard for R1 of the
// orchestration reconciliation
// (docs/reviews/2026-07-15-eaw-review-from-orchestration-frontend.md).
func TestWorkerConfirmerCarriesAgentID(t *testing.T) {
	s := newTestSession()
	sub := s.hub.add(nil, false)

	c := &workerConfirmer{s: s, ctx: t.Context(), agentID: "wk7"}
	go c.Confirm("bash", "worker wk7: ls -la")

	ev := recvEvent(t, sub)
	if ev.Type != ctrlproto.EventPermissionRequest {
		t.Fatalf("want permission_request, got %q", ev.Type)
	}
	if ev.Permission == nil {
		t.Fatal("permission_request carried no PermissionRequest payload")
	}
	if ev.Permission.Agent != "wk7" {
		t.Fatalf("PermissionRequest.Agent = %q, want the worker id %q", ev.Permission.Agent, "wk7")
	}
	// Unblock the parked Confirm so its goroutine exits cleanly.
	s.approve(ev.Permission.CallID, core.ConfirmDecision{Allow: true})
}
