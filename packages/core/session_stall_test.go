package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// A stall row round-trips to disk and is skipped by the loader: the rebuilt
// transcript holds only the real messages, so recording the detector's nudge
// never perturbs resume. Same forward-compat property as the escalation row one
// rung up.
func TestStallRowRoundTripsAndIsSkippedOnResume(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "openai-compatible", "gemma-4-26b", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "do the thing"}}}))
	must(s.AppendStall(StallRecord{
		Axis:   stallAxisChurn,
		Tool:   "task_update",
		Detail: "activate_next must name a different task",
	}))
	must(s.AppendMessage(provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "done"}}}))
	must(s.Close())

	// Resume: the loader keeps the two messages and silently skips the stall row.
	reopened, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	// OpenSession reopens the file for appending; Windows can't remove the
	// TempDir while that handle is live.
	defer reopened.Close()
	if len(msgs) != 2 {
		t.Fatalf("stall row must not enter the transcript: want 2 messages, got %d", len(msgs))
	}

	rec := readStallRow(t, path)
	if rec.Axis != stallAxisChurn {
		t.Errorf("axis = %q, want churn", rec.Axis)
	}
	if rec.Tool != "task_update" {
		t.Errorf("tool = %q, want task_update", rec.Tool)
	}
	if !strings.Contains(rec.Detail, "activate_next") {
		t.Errorf("detail = %q, want it to carry the repeated error", rec.Detail)
	}
}

// A nil session tolerates AppendStall, like every other Append.
func TestAppendStallOnNilSession(t *testing.T) {
	var s *Session
	if err := s.AppendStall(StallRecord{Axis: stallAxisSpin, Tool: "read"}); err != nil {
		t.Errorf("AppendStall on a nil session must be a no-op, got %v", err)
	}
}

// readStallRow returns the single stall row from a session file, failing if there
// is not exactly one.
func readStallRow(t *testing.T, path string) stallRecord {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var found []stallRecord
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var row sessionLine
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		if row.Type == recordStall && row.Stall != nil {
			found = append(found, *row.Stall)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one stall row, got %d", len(found))
	}
	return found[0]
}
