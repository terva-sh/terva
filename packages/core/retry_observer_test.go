package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// The turn ladder already had a live event. It gets the durable record too,
// because the live one dies with the turn.
func TestTurnRetryFiresTheObserver(t *testing.T) {
	a := newTestAgent(&overloadClient{failFor: 2}, "m", "sys", Registry{})
	a.retryBaseDelay = time.Millisecond

	var recs []RetryRecord
	a.addRetryObserver(func(rec RetryRecord) { recs = append(recs, rec) })

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
	a.addRetryObserver(func(rec RetryRecord) { recs = append(recs, rec) })

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
	a.addRetryObserver(func(RetryRecord) { fired++ })

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
