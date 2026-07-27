package modes

import (
	"context"
	"errors"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
)

// The /workflows panel reads the host's workflow run records over the two
// read-only verbs workflows.list / workflows.get.
//
// Those are METHODS, not a surface, and WorkflowsController is optional — a
// replay carrier has no workflow root and answers CodeUnsupported. So the
// carrier is type-asserted here exactly the way serve.go asserts it, and a host
// that does not serve the verbs gets a clean "not available here" instead of a
// panel that opens onto an error.
//
// Nothing is pushed: a run does not announce itself (it is a foreground process
// with no ctrlproto presence), so the panel fetches on open and on r. That is
// the same freshness the web lane has, and the honest limit until a run can
// report liveness — the record deliberately never says "running", because a
// bare pid lies after reuse and a crashed run would claim to be working.

// workflowsCarrier is the optional slice of the carrier this panel needs.
func (i *Interactive) workflowsCarrier() (ctrlproto.WorkflowsController, bool) {
	if i.cfg.Carrier == nil {
		return nil, false
	}
	wc, ok := i.cfg.Carrier.(ctrlproto.WorkflowsController)
	return wc, ok
}

// openWorkflowsDialog fetches the run list and shows the panel. The fetch is
// synchronous because there is nothing to show until it answers, and the list
// is a directory read of small JSON records.
func (i *Interactive) openWorkflowsDialog() {
	wc, ok := i.workflowsCarrier()
	if !ok {
		i.setStatusErr(i18n.T("workflow runs are not available in this mode"))
		return
	}
	runs, err := wc.WorkflowRuns(context.Background())
	if err != nil {
		if workflowsUnsupported(err) {
			i.setStatusErr(i18n.T("workflow runs are not available in this mode"))
			return
		}
		i.setStatusErr(i18n.T("workflow runs: %s", err.Error()))
		return
	}
	i.mu.Lock()
	i.workflowRuns = runs
	i.workflowView = nil
	i.mu.Unlock()
	i.workflowDialog.Open(i.workflowRunRows, i.workflowRunView)
	i.invalidate()
}

// refreshWorkflowRuns re-lists off the key handler so the panel stays painted
// while the fetch runs.
func (i *Interactive) refreshWorkflowRuns() {
	wc, ok := i.workflowsCarrier()
	if !ok {
		return
	}
	runs, err := wc.WorkflowRuns(context.Background())
	if err != nil {
		i.workflowDialog.SetError(err.Error())
		i.invalidate()
		return
	}
	i.mu.Lock()
	i.workflowRuns = runs
	i.mu.Unlock()
	i.invalidate()
}

// fetchWorkflowRun opens one run. The result is dropped if the panel has moved
// on (closed, or backed out to the list, or opened a different run) while the
// fetch was in flight — otherwise a slow read would paint the wrong run's
// script over whatever the operator is now looking at.
func (i *Interactive) fetchWorkflowRun(id string) {
	wc, ok := i.workflowsCarrier()
	if !ok {
		return
	}
	v, err := wc.WorkflowRun(context.Background(), ctrlproto.WorkflowGetParams{ID: id})
	if i.workflowDialog.Opened() != id {
		return
	}
	if err != nil {
		i.workflowDialog.SetError(err.Error())
		i.invalidate()
		return
	}
	i.mu.Lock()
	i.workflowView = &v
	i.mu.Unlock()
	i.workflowDialog.SetError("")
	i.invalidate()
}

// workflowRunRows / workflowRunView are the dialog's read-through into the
// cache. Read under mu; no fetch.
func (i *Interactive) workflowRunRows() []ctrlproto.WorkflowRunInfo {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.workflowRuns
}

func (i *Interactive) workflowRunView() *ctrlproto.WorkflowRunView {
	i.mu.Lock()
	defer i.mu.Unlock()
	if v := i.workflowView; v != nil && v.Run.ID == i.workflowDialog.Opened() {
		return v
	}
	// A view for a DIFFERENT run is not this run's view. Returning it would
	// render the previous run's script under the new run's title.
	return nil
}

// workflowsUnsupported reports the capability answer — this host does not serve
// the verb — as opposed to a real failure, which deserves its message on
// screen. The same distinction /tasks draws, for the same reason: a broken
// carrier must not masquerade as an intentional limitation.
func workflowsUnsupported(err error) bool {
	var ce *ctrlproto.Error
	if !errors.As(err, &ce) {
		return false
	}
	return ce.Code == ctrlproto.CodeUnsupported || ce.Code == ctrlproto.CodeNotFound
}
