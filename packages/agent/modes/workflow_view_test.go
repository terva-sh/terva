package modes

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/dialogs"
)

// The panel reaches workflows.list / workflows.get by type-asserting the
// carrier, because WorkflowsController is OPTIONAL — a replay carrier has no
// workflow root at all. Both real carriers assert it at compile time
// (workspace.Workspace and ctrlclient.Service each declare `var _`), which is
// what makes attach parity free: the same panel, the same two verbs, over the
// wire instead of in-process.
//
// What is worth testing here is the OTHER side of that: a carrier that does not
// serve the verbs must degrade to a clear message, not a panic and not a panel
// that opens onto an error.

// wfCarrier adds the optional controller to the shared fake.
type wfCarrier struct {
	*fakeCarrier
	runs []ctrlproto.WorkflowRunInfo
	view ctrlproto.WorkflowRunView
	err  error
	// gets records every run id the panel asked for.
	gets []string
}

func (c *wfCarrier) WorkflowRuns(context.Context) ([]ctrlproto.WorkflowRunInfo, error) {
	return c.runs, c.err
}

func (c *wfCarrier) WorkflowRun(_ context.Context, p ctrlproto.WorkflowGetParams) (ctrlproto.WorkflowRunView, error) {
	c.gets = append(c.gets, p.ID)
	return c.view, c.err
}

func TestWorkflowsPanelDegradesOnACarrierThatDoesNotServeIt(t *testing.T) {
	i := &Interactive{turns: newTurnEngine(), cfg: InteractiveConfig{Carrier: newFakeCarrier()}}

	i.openWorkflowsDialog() // must not panic
	if i.workflowDialog.Active() {
		t.Error("the panel opened against a carrier with no workflow verbs")
	}
	if !strings.Contains(i.statusErr, "not available") {
		t.Errorf("expected a clear unavailable message, got %q", i.statusErr)
	}
}

func TestWorkflowsPanelOpensAndFetchesARun(t *testing.T) {
	runs := []ctrlproto.WorkflowRunInfo{{ID: "wf_1a2b3c4d5e6f", Name: "review", Status: "incomplete", Completed: 4}}
	c := &wfCarrier{
		fakeCarrier: newFakeCarrier(),
		runs:        runs,
		view:        ctrlproto.WorkflowRunView{Run: runs[0], Script: "await agent('x')\n"},
	}
	i := &Interactive{turns: newTurnEngine(), cfg: InteractiveConfig{Carrier: c}, workflowDialog: dialogs.NewWorkflowDialog()}

	i.openWorkflowsDialog()
	if !i.workflowDialog.Active() {
		t.Fatalf("the panel did not open: %q", i.statusErr)
	}
	if got := i.workflowRunRows(); len(got) != 1 || got[0].ID != runs[0].ID {
		t.Fatalf("the run list did not reach the cache: %+v", got)
	}

	// Opening a run fills the view cache under the id the panel asked for.
	i.workflowDialog.Open(i.workflowRunRows, i.workflowRunView)
	i.fetchWorkflowRun(runs[0].ID)
	if len(c.gets) != 1 || c.gets[0] != runs[0].ID {
		t.Fatalf("the panel fetched %v, want [%s]", c.gets, runs[0].ID)
	}
}

// A view for one run must never render under another run's title. The host
// drops a stale reply, and workflowRunView refuses a mismatched cache entry —
// belt and braces, because the fetch is asynchronous and the operator can back
// out and open a different run while it is in flight.
func TestAStaleRunViewIsNotServedToThePanel(t *testing.T) {
	runs := []ctrlproto.WorkflowRunInfo{
		{ID: "wf_1a2b3c4d5e6f", Name: "first"},
		{ID: "wf_9f8e7d6c5b4a", Name: "second"},
	}
	c := &wfCarrier{fakeCarrier: newFakeCarrier(), runs: runs}
	i := &Interactive{turns: newTurnEngine(), cfg: InteractiveConfig{Carrier: c}, workflowDialog: dialogs.NewWorkflowDialog()}
	i.openWorkflowsDialog()

	// Cache holds the SECOND run's view while the panel shows the FIRST.
	i.mu.Lock()
	i.workflowView = &ctrlproto.WorkflowRunView{Run: runs[1], Script: "the wrong script"}
	i.mu.Unlock()

	// Nothing is open, so nothing matches.
	if v := i.workflowRunView(); v != nil {
		t.Errorf("a view was served with no run open: %+v", v.Run)
	}

	// With the second run's view cached and the first run open, the panel must
	// get nothing rather than the wrong script.
	c.view = ctrlproto.WorkflowRunView{Run: runs[1]}
	i.fetchWorkflowRun(runs[0].ID) // the reply is for runs[1] — a mismatched cache
	if v := i.workflowRunView(); v != nil && v.Run.ID != i.workflowDialog.Opened() {
		t.Errorf("the panel was served run %q while showing %q", v.Run.ID, i.workflowDialog.Opened())
	}
}

func TestWorkflowsCommandIsRegistered(t *testing.T) {
	spec, ok := lookupSlash("/workflows")
	if !ok {
		t.Fatal("/workflows is not registered")
	}
	if spec.hidden {
		t.Error("/workflows should be visible in the popup and /help")
	}
	// Read-only: it inspects records on disk and changes nothing, so cancelling
	// a running turn to open it would be gratuitous.
	if slashCancelsTurn("/workflows") {
		t.Error("/workflows is read-only and should not cancel the active turn")
	}
}
