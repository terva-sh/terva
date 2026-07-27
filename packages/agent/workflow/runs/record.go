// Package runs is what a workflow run LEAVES ON DISK — its record and its
// journal — separated from the engine that produces it.
//
// The split is load-bearing rather than tidy. The engine needs a JavaScript
// runtime (jsengine → sobek), and linking that into the binary is exactly what
// the `terva_workflows` build tag gates; the artifacts it writes are plain JSON
// and need nothing. Anything that wants to READ a run — the control plane, the
// web dashboard, a future TUI pane — is untagged code that would otherwise drag
// sobek into every build to reach a struct definition. The alternative, a second
// declaration of the on-disk shape next to each reader, is the drift this repo
// has already paid for once (a dead twin swallows the patches meant for the
// living one). One definition, importable by anyone.
package runs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The run record: what a run WAS, as opposed to the journal's what-it-did.
//
// Before this, a run directory held `journal.jsonl` and nothing else, and the
// journal holds completed calls — so a finished run could not be found again.
// The id lived in the narration stream and in the resume hint, both on stderr.
// That is survivable when a run FAILS, because the CLI prints the hint on the
// way out; it is not survivable when the run is INTERRUPTED, which is the case
// where the hint goes to the stderr of a dying process and nobody has it.
//
// The price of not having this was measured rather than estimated. An
// interrupted run journaled one completed agent; the rerun produced an
// identical key for it, proving `--resume` would have replayed it, and that
// agent's own record says the rerun paid $3.3671 to redo work already on disk.
//
// What the journal cannot answer, and this does:
//
//   - the SCRIPT — never written anywhere, so a run's own definition could not
//     be read back without the file the operator happened to launch from
//   - the TOTAL agent count — the journal records what completed, so "stopped
//     at 1 of 6" needs the run to have said 6
//   - the COST, start and end times, and whether it finished at all
const recordName = "run.json"

// Root is where every run's state lives, given the swarm state root the host is
// using. One expression, so the CLI that writes runs and the control plane that
// lists them cannot disagree about where they are — a second copy of this join
// would surface as a dashboard that is simply empty, with nothing to blame.
func Root(swarmRoot string) string { return filepath.Join(swarmRoot, "workflows") }

// runIDPattern is the shape Run mints: "wf_" and six random bytes in hex.
var runIDPattern = regexp.MustCompile(`^wf_[0-9a-f]{12}$`)

// ValidRunID reports whether id is safe to use as a path element.
//
// Every read below joins the id onto the root, and once a run id can arrive
// from a network client — which is the entire point of the control-plane verbs
// — an unchecked one is a path traversal into the terva home. Callers taking an
// id from anywhere but their own ListRecords must pass it through here first.
//
// Deliberately stricter than "contains no separator": the generator's shape is
// known exactly, so accepting only that costs nothing and leaves no room for
// the next surprising-but-legal filename to matter.
func ValidRunID(id string) bool { return runIDPattern.MatchString(id) }

// Record is one run's durable identity. Written when the run starts and
// rewritten when it ends, so an interrupted run leaves the started half behind
// rather than nothing.
type Record struct {
	RunID    string `json:"run_id"`
	Name     string `json:"name,omitempty"` // meta.name
	Started  string `json:"started"`        // RFC3339
	Ended    string `json:"ended,omitempty"`
	CWD      string `json:"cwd,omitempty"`
	ScriptAt string `json:"script_at,omitempty"` // the path it was launched from

	// Script is the source, verbatim. Copied rather than referenced because
	// the whole point is to read a run back without the launching host's
	// filesystem — a path that has since moved, been edited, or belongs to
	// another machine answers nothing. It also makes a run auditable: what ran
	// is recorded, not what the file says today.
	Script string `json:"script,omitempty"`

	// Args as given, so a rerun can be reproduced exactly.
	Args json.RawMessage `json:"args,omitempty"`

	// PID of the launching process. Recorded for diagnosis — matching a run to
	// something in a process list, or to a log line — and deliberately NOT
	// probed for liveness. A pid lies after reuse: the number outlives the
	// process, so a probe can find something very much alive that has nothing
	// to do with this run. Heartbeat below is what answers that question, and
	// it is the field a reader should consult.
	PID int `json:"pid,omitempty"`

	// Heartbeat is when the running process last said it was alive, RFC3339.
	// This is the liveness the PID could never supply: a pid lies after reuse,
	// but a timestamp the runner has to keep rewriting cannot — if it stops
	// advancing, the process that was advancing it is gone.
	//
	// Absent on a record written before this existed, and on one written by a
	// runner that never got to its first tick. Both fall back to the old
	// answer (incomplete), so an old run reads exactly as it always did.
	Heartbeat string `json:"heartbeat,omitempty"`

	// Filled in at the end. Agents is the total the script asked for
	// (spawned + replayed); Cached is how many replayed.
	Agents  int     `json:"agents,omitempty"`
	Cached  int     `json:"cached,omitempty"`
	CostUSD float64 `json:"cost_usd,omitempty"`
	Err     string  `json:"error,omitempty"`
}

// Status is the run's outcome as a reader should see it.
type Status string

const (
	// StatusIncomplete is the honest "cannot tell": a run with no closing
	// record and no usable heartbeat. It covers a run from before heartbeats
	// existed, and one that died before its first tick.
	StatusIncomplete Status = "incomplete"
	// StatusRunning is a run whose heartbeat is fresh — a process is alive and
	// still working. Only ever claimed on evidence.
	StatusRunning Status = "running"
	// StatusCrashed is a run whose heartbeat stopped advancing without a
	// closing record: the process died without getting to write its ending.
	// Before, this was indistinguishable from a run still in flight, so an
	// operator waited on work that had already stopped.
	StatusCrashed Status = "crashed"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

// HeartbeatInterval is how often a running workflow restamps its record.
const HeartbeatInterval = 10 * time.Second

// heartbeatGrace is how long a heartbeat may go unrefreshed before the run is
// called crashed. Three intervals, not one: a loaded machine, a slow disk, or a
// process descheduled mid-tick must not be reported dead while it is working.
// The cost of waiting is a stale "running" for half a minute; the cost of being
// hasty is telling someone to re-pay for a run that was about to finish.
const heartbeatGrace = 3 * HeartbeatInterval

// Status derives the outcome, as of now.
func (r Record) Status() Status { return r.StatusAt(time.Now()) }

// StatusAt is Status against a supplied clock, so the liveness rule is testable
// without sleeping.
//
// Ended wins over any heartbeat: a run that wrote its closing record is
// finished, whatever a stale tick says. Only an unfinished run consults
// liveness at all.
func (r Record) StatusAt(now time.Time) Status {
	switch {
	case r.Ended != "" && r.Err != "":
		return StatusFailed
	case r.Ended != "":
		return StatusDone
	}
	beat, err := time.Parse(time.RFC3339, r.Heartbeat)
	if r.Heartbeat == "" || err != nil {
		return StatusIncomplete // no evidence either way — the old answer
	}
	// A heartbeat from the FUTURE is not evidence of life; it is a clock skew
	// between the writer and this reader (a run recorded on another machine, a
	// container with a drifted clock). Treat it as fresh rather than crashed:
	// over-reporting life is recoverable by looking again, and calling a live
	// run dead invites re-paying for work in flight.
	if now.Sub(beat) > heartbeatGrace {
		return StatusCrashed
	}
	return StatusRunning
}

// Resumable reports whether resuming this run could save work: it did not
// reach a clean finish, and it journaled at least one completed call to replay.
//
// Both non-done statuses count. A run whose script threw is `failed` and a run
// killed mid-flight is `incomplete`, and either can be sitting on completed
// agents — the CLI has always printed a resume hint on failure for exactly that
// reason. Only a clean finish has nothing left to replay.
// A run that is still RUNNING is not resumable, and this is the reason the
// check is not simply "!= done": resuming a live run would start a second
// process against the same journal, double-paying for the calls in flight and
// racing the writer that owns the file. Before liveness existed the case could
// not be detected, so "incomplete" had to include it.
func (r Record) Resumable(completed int) bool {
	switch r.Status() {
	case StatusDone, StatusRunning:
		return false
	}
	return completed > 0
}

func recordPath(root, runID string) string { return filepath.Join(root, runID, recordName) }

// WriteRecord persists r atomically. A half-written record would make a run
// unreadable, which is the failure this whole file exists to prevent.
func WriteRecord(root string, r Record) error {
	dir := filepath.Join(root, r.RunID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, recordName+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, recordPath(root, r.RunID))
}

// ReadRecord loads one run's record.
func ReadRecord(root, runID string) (Record, error) {
	var r Record
	b, err := os.ReadFile(recordPath(root, runID))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("run %s: %w", runID, err)
	}
	if r.RunID == "" {
		r.RunID = runID
	}
	return r, nil
}

// ListRecords returns every run under root, newest first.
//
// A directory without a record is still listed, from its id and mtime alone:
// runs made before this existed are exactly the ones an operator is most likely
// to be hunting for, and dropping them would reproduce the gap.
func ListRecords(root string) ([]Record, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Record, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "wf_") {
			continue
		}
		r, err := ReadRecord(root, e.Name())
		if err != nil {
			r = Record{RunID: e.Name()}
			if info, ierr := e.Info(); ierr == nil {
				r.Started = info.ModTime().UTC().Format(time.RFC3339)
			}
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started > out[j].Started })
	return out, nil
}

// CompletedCalls counts the results a run journaled — what `--resume` would
// replay, and the numerator in "stopped at 1 of 6".
func CompletedCalls(root, runID string) int {
	return countResults(filepath.Join(root, runID))
}

// Results returns each journaled result with the label that produced it, so a
// finished run's output is readable without re-running the script.
func Results(root, runID string) ([]JournalResult, error) {
	return readResults(filepath.Join(root, runID))
}

// InFlightCalls returns the agents that started and have not reported back —
// what the run is working on, by the label the script gave it.
func InFlightCalls(root, runID string) ([]InFlight, error) {
	return readInFlight(filepath.Join(root, runID))
}

// Beat restamps a run's heartbeat. Called on a ticker by the running process;
// this is the whole liveness mechanism.
//
// It re-reads the record before rewriting rather than restamping a caller's
// copy, because the closing write carries counts and a cost this one must not
// clobber if the two ever interleave. A read failure is a no-op: a missing
// heartbeat degrades the run to "incomplete", which is the pre-liveness answer
// and safe, while a partial rewrite would corrupt the record itself.
func Beat(root, runID string, at time.Time) error {
	rec, err := ReadRecord(root, runID)
	if err != nil {
		return err
	}
	if rec.Ended != "" {
		return nil // finished between ticks; nothing to claim
	}
	rec.Heartbeat = at.UTC().Format(time.RFC3339)
	return WriteRecord(root, rec)
}

// FindIncomplete returns the newest incomplete run of the same script in the
// same cwd, if one exists and has work to replay. The point is to say so before
// re-spending: a rerun of an interrupted run pays again for everything the
// journal already holds.
//
// Matched on the script SOURCE, not its path — a path is not what resume keys
// on, and an edited file at the same path would replay nothing.
func FindIncomplete(root, script, cwd string) (Record, int, bool) {
	recs, err := ListRecords(root)
	if err != nil {
		return Record{}, 0, false
	}
	for _, r := range recs {
		if r.Script != script || r.CWD != cwd {
			continue
		}
		if done := CompletedCalls(root, r.RunID); r.Resumable(done) {
			return r, done, true
		}
	}
	return Record{}, 0, false
}
