package core

import "testing"

// The seam a tool uses to say "what this workspace can offer has changed".
// ticket_init is the first caller, and its result text promises the model one
// of two things depending on the answer here, so both halves are load-bearing.
func TestToolRefreshReportsWhetherAHostIsListening(t *testing.T) {
	a := NewAgent(nil, "test-model", "", Registry{})

	if a.ToolRefreshAvailable() {
		t.Error("a fresh agent claims a refresher before any host installed one")
	}
	if a.RequestToolRefresh("ticket-init") {
		t.Error("a request with no host reported success, so a one-shot run would promise tools it cannot get")
	}

	var reasons []string
	a.SetToolRefresher(func(reason string) { reasons = append(reasons, reason) })
	if !a.ToolRefreshAvailable() {
		t.Error("the agent does not see the refresher it was just given")
	}
	if !a.RequestToolRefresh("ticket-init") {
		t.Fatal("the request reported no host though one is installed")
	}
	if len(reasons) != 1 || reasons[0] != "ticket-init" {
		t.Errorf("the host was called with %v, want one call naming ticket-init", reasons)
	}

	// A host that clears the callback (a session teardown) stops being asked
	// rather than panicking on a nil call.
	a.SetToolRefresher(nil)
	if a.RequestToolRefresh("ticket-init") {
		t.Error("a cleared refresher still reported success")
	}
}

// The callback runs OUTSIDE a.mu. A host reaching back into the agent is the
// normal case (rebuildTools re-resolves and re-installs a registry), and doing
// that under the agent lock deadlocks. This test would hang rather than fail,
// which is the honest shape of the bug it guards.
func TestToolRefreshCallbackMayReEnterTheAgent(t *testing.T) {
	a := NewAgent(nil, "test-model", "", Registry{})
	a.SetToolRefresher(func(string) {
		a.SetTools(Registry{})
		a.ActivateGroup("ticket")
	})
	if !a.RequestToolRefresh("ticket-init") {
		t.Fatal("the request reported no host though one is installed")
	}
}
