package workflow

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

type journalRow struct {
	Type    string          `json:"type"` // "started" | "result"
	Key     string          `json:"key"`
	AgentID string          `json:"agent_id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

type journal struct {
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
func agentKey(prompt string, opts map[string]any) (string, error) {
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
func openJournal(dir string) (*journal, error) {
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
	return &journal{f: f, cache: cache}, nil
}

func (j *journal) lookup(key string) (json.RawMessage, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	r, ok := j.cache[key]
	return r, ok
}

// appendLocked writes one row; callers hold j.mu.
func (j *journal) appendLocked(row journalRow) error {
	b, err := json.Marshal(row)
	if err != nil {
		return err
	}
	_, err = j.f.Write(append(b, '\n'))
	return err
}

func (j *journal) started(key, agentID string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	_ = j.appendLocked(journalRow{Type: "started", Key: key, AgentID: agentID})
}

func (j *journal) result(key, agentID string, result json.RawMessage) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.appendLocked(journalRow{Type: "result", Key: key, AgentID: agentID, Result: result}); err != nil {
		return err
	}
	j.cache[key] = result
	return nil
}

func (j *journal) close() {
	j.mu.Lock()
	defer j.mu.Unlock()
	_ = j.f.Close()
}
