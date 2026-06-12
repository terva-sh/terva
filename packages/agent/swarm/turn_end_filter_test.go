package swarm

import "testing"

// mustEvent decodes one JSONL line the way the runner's stdout loop
// does, so the test exercises the exact Data the production path sees.
func mustEvent(t *testing.T, line string) Event {
	t.Helper()
	ev, ok := parseEventLine(line)
	if !ok {
		t.Fatalf("parseEventLine(%q) failed", line)
	}
	return ev
}

// TestTaskLevelTurnEndFiltersCoreTurns is the regression for the
// premature auto-swarm "finished" bug. A multi-turn (tool-using)
// sub-agent emits a turn_end after every internal turn (these carry
// "stop") plus exactly one task-level turn_end when ag.Prompt returns
// (carries "step"+"error"). Only the latter must drive OnTurnEnd, or
// the watcher declares the agent done after its first tool call.
//
// The lines below are the real turn_end shapes captured from a hung
// session's events.jsonl.
func TestTaskLevelTurnEndFiltersCoreTurns(t *testing.T) {
	// Core per-turn turn_ends — must all be ignored.
	for _, line := range []string{
		`{"stop":"tool_use","time":"2026-06-10T13:35:06.168821011-05:00","type":"turn_end"}`,
		`{"stop":"end","time":"2026-06-10T13:35:26.365825818-05:00","type":"turn_end"}`,
	} {
		if _, _, ok := taskLevelTurnEnd(mustEvent(t, line)); ok {
			t.Fatalf("core per-turn turn_end was treated as task-level: %s", line)
		}
	}

	// The wrapper's task-level turn_end — must fire, carrying step+error.
	wrapper := `{"error":"","step":1,"time":"2026-06-10T13:35:26.366174867-05:00","type":"turn_end"}`
	step, errMsg, ok := taskLevelTurnEnd(mustEvent(t, wrapper))
	if !ok {
		t.Fatal("wrapper task-level turn_end was not recognised")
	}
	if step != 1 {
		t.Fatalf("step = %d, want 1", step)
	}
	if errMsg != "" {
		t.Fatalf("errMsg = %q, want empty", errMsg)
	}

	// A failed task-level turn_end carries its error through.
	failed := `{"error":"boom","step":2,"type":"turn_end"}`
	if _, e, ok := taskLevelTurnEnd(mustEvent(t, failed)); !ok || e != "boom" {
		t.Fatalf("failed task turn_end: ok=%v err=%q, want ok=true err=boom", ok, e)
	}

	// Non-turn_end events never qualify.
	for _, line := range []string{
		`{"step":1,"type":"turn_start"}`,
		`{"type":"agent_stopped","reason":"cancelled"}`,
	} {
		if _, _, ok := taskLevelTurnEnd(mustEvent(t, line)); ok {
			t.Fatalf("non-turn_end event qualified: %s", line)
		}
	}
}
