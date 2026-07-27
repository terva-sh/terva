package workspace

import (
	"context"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/workflow/runs"
	"terva.sh/terva/packages/i18n"
)

// The workflow dashboard's server half: read a run's durable artifacts and hand
// them to whichever client asked.
//
// Note what is NOT here. The engine that produces these runs is behind the
// terva_workflows build tag, because it links a JavaScript runtime; this file is
// untagged, and reads the artifacts through packages/agent/workflow/runs, which
// imports nothing but the standard library. So a lean daemon with no workflow
// engine at all still SERVES the dashboard for runs made by a full binary on the
// same machine — the reader and the writer never had to ship together.
//
// The runs are read from disk on every call rather than cached. A run is written
// by a separate process (`terva workflow run`, foreground, its own terminal), so
// this daemon has no event to invalidate a cache on; disk is the only source
// that can be right. Listings are small and the alternative is a dashboard that
// silently shows yesterday.

var _ ctrlproto.WorkflowsController = (*Workspace)(nil)

// workflowsRoot resolves the run root from the swarm this workspace actually
// has, so it agrees with `terva workflow` by construction — both sides go
// through runs.Root over the same swarm root.
func (w *Workspace) workflowsRoot() (string, bool) {
	if w.swarm == nil {
		return "", false
	}
	return runs.Root(w.swarm.Root()), true
}

// WorkflowRuns lists every run on this host, newest first.
func (w *Workspace) WorkflowRuns(_ context.Context) ([]ctrlproto.WorkflowRunInfo, error) {
	root, ok := w.workflowsRoot()
	if !ok {
		return nil, ctrlproto.Errorf(ctrlproto.CodeUnsupported, "%s", i18n.T("this host has no swarm, so it has no workflow runs"))
	}
	recs, err := runs.ListRecords(root)
	if err != nil {
		return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", i18n.T("workflow runs: %v", err))
	}
	out := make([]ctrlproto.WorkflowRunInfo, 0, len(recs))
	for _, r := range recs {
		out = append(out, workflowRunInfo(root, r))
	}
	return out, nil
}

// WorkflowRun opens one run: its record, the script as it ran, and every result
// it journaled.
func (w *Workspace) WorkflowRun(_ context.Context, p ctrlproto.WorkflowGetParams) (ctrlproto.WorkflowRunView, error) {
	root, ok := w.workflowsRoot()
	if !ok {
		return ctrlproto.WorkflowRunView{}, ctrlproto.Errorf(ctrlproto.CodeUnsupported, "%s", i18n.T("this host has no swarm, so it has no workflow runs"))
	}
	// The id becomes a path element, and it arrived from a network client.
	if !runs.ValidRunID(p.ID) {
		return ctrlproto.WorkflowRunView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("no workflow run %q", p.ID))
	}
	rec, err := runs.ReadRecord(root, p.ID)
	if err != nil {
		// A run directory can exist without a record (it predates run records),
		// and that is worth showing rather than 404ing: the journal is the half
		// an operator is hunting for.
		rec = runs.Record{RunID: p.ID}
		if runs.CompletedCalls(root, p.ID) == 0 {
			return ctrlproto.WorkflowRunView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("no workflow run %q", p.ID))
		}
	}
	view := ctrlproto.WorkflowRunView{
		Run:    workflowRunInfo(root, rec),
		Script: rec.Script,
		Args:   rec.Args,
	}
	// A journal that cannot be read is not a reason to withhold the record —
	// the script and the coordinates are exactly what the caller came for when
	// a run died before journaling anything.
	if results, rerr := runs.Results(root, p.ID); rerr == nil {
		view.Results = make([]ctrlproto.WorkflowRunResult, 0, len(results))
		for _, r := range results {
			view.Results = append(view.Results, ctrlproto.WorkflowRunResult{
				Label:   r.Label,
				AgentID: r.AgentID,
				Bytes:   len(r.Result),
				Result:  r.Result,
			})
		}
	}
	return view, nil
}

// workflowRunInfo projects a record onto the wire, resolving the two facts that
// live outside it: how many calls the journal holds, and whether replaying them
// would save real work.
func workflowRunInfo(root string, r runs.Record) ctrlproto.WorkflowRunInfo {
	done := runs.CompletedCalls(root, r.RunID)
	return ctrlproto.WorkflowRunInfo{
		ID:        r.RunID,
		Name:      r.Name,
		Status:    string(r.Status()),
		Started:   r.Started,
		Ended:     r.Ended,
		Completed: done,
		Agents:    r.Agents,
		Cached:    r.Cached,
		CostUSD:   r.CostUSD,
		CWD:       r.CWD,
		ScriptAt:  r.ScriptAt,
		Err:       r.Err,
		Resumable: r.Resumable(done),
	}
}
