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

// A cliff row round-trips to disk and is skipped by the loader — the same
// forward-compat property as stall, prefix and net rows.
//
// The close row is the one worth guarding: it reports the totals the run
// REACHED, while the detector's end-of-run event carries the zero CacheCliff
// by contract. A close row that wrote the event's zeros would say a collapse
// ended having wasted nothing, which is the opposite of the fact it exists to
// record.
func TestCliffRowRoundTripsAndIsSkippedOnResume(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "openai-codex", "gpt-5.6-sol", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "go"}}}))
	must(s.AppendCacheCliff(CacheCliff{Dispatches: 2, RereadTokens: 240_000, Ongoing: true}, true))
	must(s.AppendMessage(provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "done"}}}))
	must(s.AppendCacheCliff(CacheCliff{Dispatches: 18, RereadTokens: 1_900_000}, false))
	must(s.Close())

	reopened, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(msgs) != 2 {
		t.Fatalf("cliff rows must not enter the transcript: want 2 messages, got %d", len(msgs))
	}

	rows := readCliffRows(t, path)
	if len(rows) != 2 {
		t.Fatalf("want 2 cliff rows, got %d", len(rows))
	}
	if !rows[0].Ongoing || rows[0].Dispatches != 2 || rows[0].RereadTokens != 240_000 {
		t.Errorf("open row did not survive: %+v", rows[0])
	}
	if rows[1].Ongoing {
		t.Errorf("close row must carry ongoing=false, got %+v", rows[1])
	}
	if rows[1].Dispatches != 18 || rows[1].RereadTokens != 1_900_000 {
		t.Errorf("close row must carry the run's totals, not the event's zeros: %+v", rows[1])
	}
}

// A run still open when the session ends leaves an open row and no close. That
// asymmetry IS the signal — it says the collapse was live when the process
// stopped, which is exactly the session an experiment wants to catch.
func TestCliffRunOpenAtSessionEndLeavesNoCloseRow(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "openai-codex", "gpt-5.6-sol", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "go"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendCacheCliff(CacheCliff{Dispatches: 3, RereadTokens: 90_000, Ongoing: true}, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	rows := readCliffRows(t, path)
	if len(rows) != 1 || !rows[0].Ongoing {
		t.Fatalf("want exactly one open row and no close, got %+v", rows)
	}
}

func TestAppendCacheCliffOnNilSession(t *testing.T) {
	var s *Session
	if err := s.AppendCacheCliff(CacheCliff{Dispatches: 4, Ongoing: true}, true); err != nil {
		t.Errorf("AppendCacheCliff on a nil session must be a no-op, got %v", err)
	}
}

func readCliffRows(t *testing.T, path string) []cacheCliffRecord {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var found []cacheCliffRecord
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var row sessionLine
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		if row.Type == recordCliff && row.Cliff != nil {
			found = append(found, *row.Cliff)
		}
	}
	return found
}
