package ctrlproto

import (
	"context"
	"encoding/json"
)

// Watching the workflow engine work.
//
// A workflow run fans agents out over the swarm and journals what each returns.
// Until now the only way to see any of that was the terminal the run was
// launched from: progress went to stderr, and when that terminal was gone the
// run's id, its script, and every report it produced were on disk and
// unreachable in practice. The first real run cost $3.37 to redo work already
// journaled, because nobody could find it.
//
// These two verbs are the read side of that. They are deliberately NOT in the
// control group: nothing here reconfigures the host, and a client that watches
// without the authority to change anything is exactly the client this serves.
//
// WorkflowsController is OPTIONAL, like the Stage controllers: a replay carrier
// has no workflow root to read, and answers CodeUnsupported rather than
// pretending it has no runs.
type WorkflowsController interface {
	// WorkflowRuns lists every run the host knows about, newest first.
	WorkflowRuns(ctx context.Context) ([]WorkflowRunInfo, error)
	// WorkflowRun returns one run with its script and journaled results.
	WorkflowRun(ctx context.Context, p WorkflowGetParams) (WorkflowRunView, error)
}

// WorkflowRunInfo is one row in the dashboard.
//
// Status is "running", "crashed", "done", "failed", or "incomplete". The first
// two are earned: a running process restamps a heartbeat on the record, so a
// fresh stamp means alive and a stopped one means the process died without
// writing its ending. "incomplete" survives as the honest "cannot tell" — a run
// recorded before heartbeats existed, or one that died before its first tick.
//
// Completed/Agents is what an operator actually reads: "1/6" is the whole
// finding, because it says how much work is sitting on disk waiting to be
// replayed instead of paid for again.
type WorkflowRunInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"` // the script's meta.name
	Status  string `json:"status"`
	Started string `json:"started,omitempty"` // RFC 3339
	Ended   string `json:"ended,omitempty"`
	// Completed is what the journal holds; Agents is the total the script asked
	// for, known only once the run closed (so 0 on an interrupted run).
	Completed int     `json:"completed"`
	Agents    int     `json:"agents,omitempty"`
	Cached    int     `json:"cached,omitempty"`
	CostUSD   float64 `json:"cost_usd,omitempty"`
	CWD       string  `json:"cwd,omitempty"`
	ScriptAt  string  `json:"script_at,omitempty"`
	Err       string  `json:"error,omitempty"`
	// Running is how many agents have started and not yet reported back.
	// Alongside Completed it separates "6 done, nothing moving" from "6 done,
	// 4 in flight" — the difference between a run to resume and one to wait for.
	Running int `json:"running,omitempty"`
	// Resumable: resuming would replay real work. Derived server-side because
	// the rule is the server's and has already changed twice — a FAILED run
	// counts (a script that threw can still be sitting on completed agents),
	// and a RUNNING one does not, because resuming a live run would start a
	// second process against the same journal.
	Resumable bool `json:"resumable,omitempty"`
}

// WorkflowRunsResult is the workflows.list reply.
type WorkflowRunsResult struct {
	Runs []WorkflowRunInfo `json:"runs"`
}

// WorkflowGetParams names the run to open.
//
// The id travels in PARAMS, not in the frame's sess field, for the same reason
// an archived session's does: sess is a live session handle that subscriptions
// and turn routing key on, and a workflow run is not a session at all.
type WorkflowGetParams struct {
	ID string `json:"id"`
}

// WorkflowRunView is one run, opened.
//
// Script is the source as it ran, recorded verbatim at launch — not read back
// from ScriptAt, which may have moved, been edited, or belong to another
// machine. Reading it here is the point: inspecting what a run did should not
// require shell access to the host that ran it.
type WorkflowRunView struct {
	Run     WorkflowRunInfo     `json:"run"`
	Script  string              `json:"script,omitempty"`
	Args    json.RawMessage     `json:"args,omitempty"`
	Results []WorkflowRunResult `json:"results,omitempty"`
	// InFlight is the agents that started and have not reported back, by the
	// label the script gave them. On a running run that is the answer to "what
	// is it doing"; on a crashed one, to "what was it doing when it stopped".
	InFlight []WorkflowAgent `json:"in_flight,omitempty"`
}

// WorkflowAgent names one agent with no result yet. Deliberately thin: there is
// no per-agent progress to report, because a workflow agent journals once, at
// the end. Knowing WHICH slices are outstanding is the whole signal.
type WorkflowAgent struct {
	Label   string `json:"label,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
}

// WorkflowRunResult is one completed agent call: the label that names it
// (rather than the generated agent id, which a fan-out sharing a prompt
// preamble makes unreadable), and what it returned.
//
// Bytes is sent alongside Result so a client can collapse a large report behind
// its size instead of pasting a hundred kilobytes into the page. Deliverables
// really are that big — the run this was built for produced one at 98 KB.
type WorkflowRunResult struct {
	Label   string          `json:"label,omitempty"`
	AgentID string          `json:"agent_id,omitempty"`
	Bytes   int             `json:"bytes,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}
