package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The turn ladder already had a live event. It gets the durable record too,
// because the live one dies with the turn.
func TestTurnRetryFiresTheObserver(t *testing.T) {
	a := NewAgent(&overloadClient{failFor: 2}, "m", "sys", Registry{})
	a.RetryBaseDelay = time.Millisecond

	var recs []RetryRecord
	a.AddRetryObserver(func(rec RetryRecord) { recs = append(recs, rec) })

	if err := a.Prompt(context.Background(), "go", nil, func(AgentEvent) {}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	for i, rec := range recs {
		if rec.Phase != RetryPhaseTurn {
			t.Errorf("record %d: Phase = %q, want %q", i, rec.Phase, RetryPhaseTurn)
		}
		if rec.Attempt != i+1 {
			t.Errorf("record %d: Attempt = %d, want %d", i, rec.Attempt, i+1)
		}
		if rec.Provider != "openai-codex" || !strings.Contains(rec.Err, "overloaded") {
			t.Errorf("record %d = %+v, want the provider and its message", i, rec)
		}
	}
}

// The point of the change: compaction retries through the same ladder but has
// only a text sink for the summary it is streaming, so EvRetry never reached
// it. Without the observer, a compaction that waits out a two-minute outage is
// indistinguishable from one that was slow for no reason.
func TestCompactionRetryFiresTheObserver(t *testing.T) {
	var a *Agent
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		switch {
		case n == 0:
			return saidText("hi", 50), nil
		case n <= 2: // the warm attempt blips twice, then recovers
			return overloaded(), nil
		default:
			return saidText("## Goal\nship it", 100), nil
		}
	}}
	a = retryingAgent(t, client)

	var recs []RetryRecord
	a.AddRetryObserver(func(rec RetryRecord) { recs = append(recs, rec) })

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Compact(context.Background(), 0, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2 (one per retried compaction attempt)", len(recs))
	}
	for i, rec := range recs {
		// Phase is the whole reason the field exists: these each carried the
		// entire transcript, and a reader must be able to tell them from the
		// cheap turn retries above.
		if rec.Phase != RetryPhaseCompaction {
			t.Errorf("record %d: Phase = %q, want %q", i, rec.Phase, RetryPhaseCompaction)
		}
		if rec.Attempt != i+1 {
			t.Errorf("record %d: Attempt = %d, want %d (1-based, matching the turn ladder)", i, rec.Attempt, i+1)
		}
		if rec.Delay <= 0 {
			t.Errorf("record %d: Delay = %v, want the wait it took", i, rec.Delay)
		}
	}
}

// A clean compaction must stay silent, or the row stops meaning anything.
func TestCleanCompactionRecordsNoRetry(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		if n == 0 {
			return saidText("hi", 50), nil
		}
		return saidText("## Goal\nship it", 100), nil
	}}
	a := retryingAgent(t, client)
	fired := 0
	a.AddRetryObserver(func(RetryRecord) { fired++ })

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Compact(context.Background(), 0, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if fired != 0 {
		t.Fatalf("got %d retry records on a clean compaction, want 0", fired)
	}
}

// The row must persist what a reader needs AND be invisible to resume — the
// same contract the stall and tail rows hold to.
func TestRetryRowPersistsAndIsSkippedByTheLoader(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "s.jsonl")
	sess, err := NewSessionAtPath(path, dir, "openai-codex", "m", "test")
	if err != nil {
		t.Fatalf("NewSessionAtPath: %v", err)
	}
	if err := sess.AppendMessage(provider.Message{Role: provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hello"}}}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := sess.AppendRetry(RetryRecord{
		Phase: RetryPhaseCompaction, Provider: "openai-codex", Attempt: 3, Max: 6,
		Delay: 8 * time.Second, Err: "Our servers are currently overloaded.",
	}); err != nil {
		t.Fatalf("AppendRetry: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var found map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) == nil && row["type"] == "retry" {
			found, _ = row["retry"].(map[string]any)
		}
	}
	if found == nil {
		t.Fatal("no retry row written")
	}
	if found["phase"] != "compaction" {
		t.Errorf("phase = %v, want compaction", found["phase"])
	}
	if found["delay_ms"] != float64(8000) {
		t.Errorf("delay_ms = %v, want 8000", found["delay_ms"])
	}
	if found["attempt"] != float64(3) || found["max"] != float64(6) {
		t.Errorf("attempt/max = %v/%v, want 3/6", found["attempt"], found["max"])
	}

	// Resume must not see it. An informational row that reached the transcript
	// would be replayed to the model as content.
	_, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := len(msgs); got != 1 {
		t.Fatalf("resumed with %d messages, want 1 — the retry row entered the transcript", got)
	}
}
