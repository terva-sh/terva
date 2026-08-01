package swarm

import (
	"testing"
	"time"
)

// The counters exist because activity is a LEVEL, not a record. A run that
// ends on tool_result reports "idle" while it is mid-turn, which is exactly
// what a dashboard showed for a healthy agent 31 minutes in.
func TestProgressCountsTurnsAndToolsSeparately(t *testing.T) {
	a := &Agent{ID: "a1"}
	sink := agentSink{a: a}

	for _, ev := range []Event{
		NewEvent("turn_start", map[string]any{"step": 0}),
		NewEvent("tool_call", map[string]any{"name": "read"}),
		NewEvent("tool_result", map[string]any{}),
		NewEvent("tool_call", map[string]any{"name": "bash"}),
		NewEvent("tool_result", map[string]any{}),
		NewEvent("turn_start", map[string]any{"step": 1}),
		NewEvent("tool_call", map[string]any{"name": "edit"}),
	} {
		IngestEvent(ev, nil, sink, a)
	}

	turns, tools, _ := a.Progress()
	if turns != 2 {
		t.Errorf("turns = %d, want 2", turns)
	}
	if tools != 3 {
		t.Errorf("toolCalls = %d, want 3", tools)
	}
	// The state the counters are meant to disambiguate: activity says idle,
	// the numbers say three tool calls of work happened.
	if got := a.Activity(); got != "tool: edit" {
		t.Logf("activity = %q", got)
	}
}

// A snapshot is what every dashboard actually reads.
func TestSnapshotCarriesProgress(t *testing.T) {
	a := &Agent{ID: "a1"}
	sink := agentSink{a: a}
	IngestEvent(NewEvent("turn_start", map[string]any{}), nil, sink, a)
	IngestEvent(NewEvent("tool_call", map[string]any{"name": "grep"}), nil, sink, a)

	snap := a.Snapshot()
	if snap.Turns != 1 || snap.ToolCalls != 1 {
		t.Fatalf("snapshot progress = %d turns / %d tools, want 1/1", snap.Turns, snap.ToolCalls)
	}
	if snap.LastEvent.IsZero() {
		t.Fatal("snapshot LastEvent is zero after two events")
	}
}

// Every event is a heartbeat, including ones that count for nothing else. An
// agent streaming stdout for ten minutes is working, and reporting it as
// silent would be the same lie the counters exist to stop telling.
func TestAnyEventIsAHeartbeat(t *testing.T) {
	a := &Agent{ID: "a1"}
	sink := agentSink{a: a}

	early := time.Now().Add(-10 * time.Minute)
	IngestEvent(Event{Type: "turn_start", Time: early, Data: map[string]any{}}, nil, sink, a)
	_, _, last := a.Progress()
	if !last.Equal(early) {
		t.Fatalf("lastEvent = %v, want %v", last, early)
	}

	recent := time.Now()
	IngestEvent(Event{Type: "stdout", Time: recent, Data: map[string]any{"text": "still going"}}, nil, sink, a)
	turns, tools, last := a.Progress()
	if !last.Equal(recent) {
		t.Fatalf("stdout did not advance lastEvent: %v, want %v", last, recent)
	}
	if turns != 1 || tools != 0 {
		t.Fatalf("stdout moved a counter it should not: %d turns / %d tools", turns, tools)
	}
}

// Timestamps come from the event, not from the clock at read time. This is
// what lets a replayed log date a detached agent honestly rather than
// reporting that every one of its events happened when terva booted.
func TestLastEventUsesTheEventsOwnTimeAndNeverGoesBackwards(t *testing.T) {
	a := &Agent{ID: "a1"}
	sink := agentSink{a: a}

	newest := time.Now().Add(-time.Minute)
	IngestEvent(Event{Type: "tool_call", Time: newest, Data: map[string]any{"name": "read"}}, nil, sink, a)
	// An out-of-order or clock-skewed event must not rewind the heartbeat:
	// "quiet for 5 minutes" would otherwise appear out of nowhere mid-run.
	IngestEvent(Event{Type: "tool_result", Time: newest.Add(-5 * time.Minute), Data: map[string]any{}}, nil, sink, a)

	if _, _, last := a.Progress(); !last.Equal(newest) {
		t.Fatalf("lastEvent = %v, want it pinned at %v", last, newest)
	}
	// A zero-time event (a synthesised lifecycle marker) leaves it alone
	// rather than wiping it.
	IngestEvent(Event{Type: "agent_ready", Data: map[string]any{}}, nil, sink, a)
	if _, _, last := a.Progress(); !last.Equal(newest) {
		t.Fatalf("a zero-time event clobbered lastEvent: %v", last)
	}
}

// The live path and the replay path are near-twins (IngestEvent /
// replayEventsIntoAgent), and a twin that misses a patch is how a resumed
// agent reports zero work forever. They share one classifier; this is the
// test that says so.
func TestReplayRebuildsProgressFromTheLog(t *testing.T) {
	at := time.Now().Add(-2 * time.Hour)
	evs := []Event{
		{Type: "turn_start", Time: at, Data: map[string]any{}},
		{Type: "tool_call", Time: at.Add(time.Second), Data: map[string]any{"name": "read"}},
		{Type: "tool_call", Time: at.Add(2 * time.Second), Data: map[string]any{"name": "bash"}},
		{Type: "turn_start", Time: at.Add(3 * time.Second), Data: map[string]any{}},
	}

	a := &Agent{ID: "a1"}
	replayEventsIntoAgent(a, evs)

	turns, tools, last := a.Progress()
	if turns != 2 || tools != 2 {
		t.Fatalf("replayed progress = %d turns / %d tools, want 2/2", turns, tools)
	}
	if want := at.Add(3 * time.Second); !last.Equal(want) {
		t.Fatalf("replayed lastEvent = %v, want the log's last stamp %v", last, want)
	}
}
