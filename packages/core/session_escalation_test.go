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

// An escalation row round-trips to disk and is skipped by the loader: the
// rebuilt transcript holds only the real messages, so recording the escalation
// never perturbs resume. That is the whole safety property of the row — it adds
// provenance beside the swap's "meta" row without touching the conversation.
func TestEscalationRowRoundTripsAndIsSkippedOnResume(t *testing.T) {
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
	must(s.AppendEscalation(EscalationRecord{
		Reason:      "stuck on task_update ×5: activate_next must name a different task",
		Tool:        "task_update",
		FromModel:   "gemma-4-26b",
		ToProvider:  "openai-codex",
		ToModel:     "gpt-5.6-sol",
		Auto:        true,
		Disposition: EscalationSwitched,
	}))
	must(s.AppendMessage(provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "done"}}}))
	must(s.Close())

	// Resume: the loader keeps the two messages and silently skips the escalation
	// row (the row-type switch has no case for it and no default).
	reopened, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	// OpenSession reopens the file for appending; Windows can't remove the
	// TempDir while that handle is live.
	defer reopened.Close()
	if len(msgs) != 2 {
		t.Fatalf("escalation row must not enter the transcript: want 2 messages, got %d", len(msgs))
	}

	// The row is on disk, decodable, with its provenance intact — this is what a
	// reader (or a jq query mining a session) recovers.
	rec := readEscalationRow(t, path)
	if rec.Disposition != string(EscalationSwitched) {
		t.Errorf("disposition = %q, want switched", rec.Disposition)
	}
	if rec.FromModel != "gemma-4-26b" || rec.ToProvider != "openai-codex" || rec.ToModel != "gpt-5.6-sol" {
		t.Errorf("from/to not round-tripped: %+v", rec)
	}
	if !rec.Auto {
		t.Error("Auto must round-trip")
	}
	if rec.Tool != "task_update" || !strings.Contains(rec.Reason, "activate_next") {
		t.Errorf("tool/reason not round-tripped: %+v", rec)
	}
}

// A nil session tolerates AppendEscalation, like every other Append — live-only
// conversations have no file to write to.
func TestAppendEscalationOnNilSession(t *testing.T) {
	var s *Session
	if err := s.AppendEscalation(EscalationRecord{Disposition: EscalationSwitched}); err != nil {
		t.Errorf("AppendEscalation on a nil session must be a no-op, got %v", err)
	}
}

// readEscalationRow returns the single escalation row from a session file,
// failing if there is not exactly one.
func readEscalationRow(t *testing.T, path string) escalationRecord {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var found []escalationRecord
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var row sessionLine
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		if row.Type == recordEscalation && row.Escalation != nil {
			found = append(found, *row.Escalation)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one escalation row, got %d", len(found))
	}
	return found[0]
}
