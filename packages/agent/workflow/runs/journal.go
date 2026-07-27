package runs

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// The journal is the run's step-result cache: one started/result row pair
// per completed agent() call, keyed by a content hash of (prompt, opts).
// Resume re-executes the script; any call whose key already has a result
// row returns it without spawning — key-addressed, not positional, so
// editing one stage invalidates only that stage. Failures are journaled
// NEVER (no error row type exists): a failed agent leaves no record and
// is simply retried on resume. Both properties are deliberate imports
// from the Claude Code journal this design was probed against
// (docs/proposals/workflow-structured-swarm.md — the format itself is
// terva-native; theirs is undocumented internal surface).

const journalName = "journal.jsonl"

// maxJournalLine bounds one row: a schema deliverable can be large, and the
// writer already uses this ceiling.
const maxJournalLine = 8 * 1024 * 1024

type journalRow struct {
	Type    string `json:"type"` // "started" | "result"
	Key     string `json:"key"`
	AgentID string `json:"agent_id,omitempty"`
	// Label is the script's name for this agent, carried so a row can be read
	// back to a slice without the narration that printed both. Narration is a
	// stderr stream: after the run it exists only wherever that stream happened
	// to land, which for an interrupted run is nowhere.
	//
	// Additive and omitempty: a journal written before this field resumes
	// unchanged, because resume matches on Key alone and Key still hashes the
	// full (prompt, opts) pair — label included, as it always did.
	Label  string          `json:"label,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

type Journal struct {
	// mu guards cache and the file: agent() bindings run concurrently on
	// their own goroutines (that is the point of the async profile), and
	// they all read and append here.
	mu    sync.Mutex
	f     *os.File
	cache map[string]json.RawMessage
}

// agentKey derives the cache key for one agent() call. ALL opts
// participate (label and phase included) — matching the probed Claude
// Code semantics, whose guidance "vary the prompt/label by index" exists
// precisely because identical (prompt, opts) pairs share one cache slot.
// encoding/json sorts map keys, so the marshal is canonical.
func AgentKey(prompt string, opts map[string]any) (string, error) {
	b, err := json.Marshal(struct {
		Prompt string         `json:"prompt"`
		Opts   map[string]any `json:"opts,omitempty"`
	}{prompt, opts})
	if err != nil {
		return "", fmt.Errorf("agent opts not serializable: %w", err)
	}
	sum := sha256.Sum256(b)
	return "v1:" + hex.EncodeToString(sum[:]), nil
}

// openJournal opens (creating if needed) the run's journal and loads
// every existing result row into the replay cache.
func OpenJournal(dir string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("workflow run dir: %w", err)
	}
	path := filepath.Join(dir, journalName)
	cache := map[string]json.RawMessage{}
	if existing, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(existing)
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for sc.Scan() {
			var row journalRow
			if json.Unmarshal(sc.Bytes(), &row) != nil {
				continue // a torn tail line (crash mid-write) is not fatal
			}
			if row.Type == "result" && row.Key != "" {
				cache[row.Key] = row.Result
			}
		}
		existing.Close()
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("workflow journal: %w", err)
	}
	return &Journal{f: f, cache: cache}, nil
}

func (j *Journal) Lookup(key string) (json.RawMessage, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	r, ok := j.cache[key]
	return r, ok
}

// appendLocked writes one row; callers hold j.mu.
func (j *Journal) appendLocked(row journalRow) error {
	b, err := json.Marshal(row)
	if err != nil {
		return err
	}
	_, err = j.f.Write(append(b, '\n'))
	return err
}

func (j *Journal) Started(key, agentID, label string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	_ = j.appendLocked(journalRow{Type: "started", Key: key, AgentID: agentID, Label: label})
}

func (j *Journal) Result(key, agentID, label string, result json.RawMessage) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.appendLocked(journalRow{Type: "result", Key: key, AgentID: agentID, Label: label, Result: result}); err != nil {
		return err
	}
	j.cache[key] = result
	return nil
}

func (j *Journal) Close() {
	j.mu.Lock()
	defer j.mu.Unlock()
	_ = j.f.Close()
}

// JournalResult is one completed agent call as a reader sees it: the label that
// produced it (TW-041), the agent that ran it, and what it returned.
type JournalResult struct {
	Label   string          `json:"label,omitempty"`
	AgentID string          `json:"agent_id,omitempty"`
	Key     string          `json:"key"`
	Result  json.RawMessage `json:"result"`
}

// InFlight is one agent that started and has not reported back — what the run
// is working on right now.
type InFlight struct {
	Label   string `json:"label,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
	Key     string `json:"key,omitempty"`
}

// scanRows walks every row of a run's journal read-only, in file order. The
// read-only discipline is scanResults' (see below) and matters just as much
// here: a dashboard listing every run must not mint journals in the ones it
// finds without a run in flight.
func scanRows(dir string, fn func(journalRow)) error {
	f, err := os.Open(filepath.Join(dir, journalName))
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxJournalLine)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var row journalRow
		if err := json.Unmarshal(line, &row); err != nil {
			continue // a torn tail line is not a reason to lose the rest
		}
		fn(row)
	}
	return sc.Err()
}

// readInFlight returns the agents that started and never resulted, in start
// order.
//
// The data was always here. The runner has journaled a "started" row per agent
// since labels landed; every reader filtered to type=="result" and threw the
// rest away, so "which agents are working right now" was on disk and unread —
// the same shape as the run record itself being unreachable before the
// dashboard existed.
//
// Started-minus-resulted, not a live process probe: an agent whose row is
// started with no result either is working or died with the run. Which of those
// it is, is the RUN's question, and the record's heartbeat answers it — so a
// crashed run's in-flight list reads as "what it was doing when it stopped",
// which is exactly what an operator wants before deciding to resume.
func readInFlight(dir string) ([]InFlight, error) {
	var order []InFlight
	done := map[string]bool{}
	seen := map[string]bool{}
	err := scanRows(dir, func(row journalRow) {
		switch row.Type {
		case "started":
			// Keyed, and deduped by key: a resumed run re-journals a started row
			// for a call it is retrying, and the same call listed twice would
			// read as two agents doing one job.
			if row.Key == "" || seen[row.Key] {
				return
			}
			seen[row.Key] = true
			order = append(order, InFlight{Label: row.Label, AgentID: row.AgentID, Key: row.Key})
		case "result":
			done[row.Key] = true
		}
	})
	if err != nil {
		return nil, err
	}
	out := order[:0]
	for _, f := range order {
		if !done[f.Key] {
			out = append(out, f)
		}
	}
	return out, nil
}

// scanResults walks a run's result rows read-only, in file order.
//
// Every reader goes through here rather than through OpenJournal, and the
// distinction is not stylistic: OpenJournal takes a WRITE handle and creates
// both the directory and the file if they are missing. That is right for a run
// about to append to it and wrong for anything merely looking — a dashboard
// listing every run on the machine would otherwise mint a journal inside each
// one it found without a run in flight, and hold a writer on the journals of
// the runs that do.
func scanResults(dir string, fn func(journalRow)) error {
	return scanRows(dir, func(row journalRow) {
		if row.Type != "result" {
			return
		}
		fn(row)
	})
}

// countResults reports how many distinct calls a run journaled — what --resume
// would replay. Distinct by key, matching the replay cache it stands in for:
// the cache is keyed, so two rows sharing a key have always been one result.
func countResults(dir string) int {
	seen := map[string]bool{}
	_ = scanResults(dir, func(row journalRow) {
		if row.Key != "" {
			seen[row.Key] = true
		}
	})
	return len(seen)
}

// readResults re-reads a run's journal from disk.
func readResults(dir string) ([]JournalResult, error) {
	var out []JournalResult
	err := scanResults(dir, func(row journalRow) {
		out = append(out, JournalResult{
			Label: row.Label, AgentID: row.AgentID, Key: row.Key, Result: row.Result,
		})
	})
	return out, err
}
