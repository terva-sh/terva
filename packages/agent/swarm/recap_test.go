package swarm

import "testing"

// TestRecapStatusCollapsesDaemonRunning pins the fix for the misleading
// recap: a dispatched sub-agent is a long-lived daemon that keeps
// status=running after its task's turn_end, so the raw Status reads
// "running" even though the task is done. RecapStatus reports task terms
// — "completed" for the non-terminal states, real failures passed through
// — so a coordinator isn't told the crew is still working when it isn't.
func TestRecapStatusCollapsesDaemonRunning(t *testing.T) {
	cases := []struct {
		in   Status
		want string
	}{
		{StatusRunning, "completed"}, // the bug: daemon alive, task done
		{StatusDone, "completed"},
		{StatusDetached, "completed"},
		{StatusPending, "completed"},
		{StatusFailed, "failed"},
		{StatusKilled, "killed"},
	}
	for _, c := range cases {
		if got := (AgentSnapshot{Status: c.in}).RecapStatus(); got != c.want {
			t.Errorf("RecapStatus(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// TestFindingsPrefersLastAssistant pins that the recap surfaces the
// sub-agent's actual answer, falling back to the transcript tail only
// when no assistant message was captured.
func TestFindingsPrefersLastAssistant(t *testing.T) {
	if got := (AgentSnapshot{LastAssistant: "the findings", Tail: "ok pkg/x"}).Findings(); got != "the findings" {
		t.Errorf("Findings with LastAssistant = %q; want the assistant answer", got)
	}
	if got := (AgentSnapshot{Tail: "  ok pkg/x  "}).Findings(); got != "ok pkg/x" {
		t.Errorf("Findings fallback = %q; want trimmed tail", got)
	}
	if got := (AgentSnapshot{LastAssistant: "   "}).Findings(); got != "" {
		t.Errorf("Findings with blank-only fields = %q; want empty", got)
	}
}

// TestFindingsSurvivesGuardNudge pins the arbitration between the answer a
// child delivered BEFORE the finalize guard re-prompted it and whatever it
// said after. Observed in the 2026-07-08 review run: the security specialist
// ended with its full report, the guard fired on open tracked items, and its
// literal answer to the nudge — "confirmed, all tasks complete" — became the
// last assistant message and clobbered the recap findings.
func TestFindingsSurvivesGuardNudge(t *testing.T) {
	report := "## Security review report\nasset: user state\n- finding 1\n- finding 2"

	// The vartija case: housekeeping after the nudge → the report wins.
	s := AgentSnapshot{PreGuardAssistant: report, LastAssistant: "Confirmed: all tracked items are complete."}
	if got := s.Findings(); got != report {
		t.Errorf("housekeeping clobbered the report:\n%q", got)
	}

	// The arkkitehti case: the child re-states/extends the report after the
	// nudge → the newer, fuller text wins.
	restated := "All tracked items are now complete.\n\n" + report
	s = AgentSnapshot{PreGuardAssistant: report, LastAssistant: restated}
	if got := s.Findings(); got != restated {
		t.Errorf("restated report should win, got:\n%q", got)
	}

	// Guard never fired → unchanged behavior.
	s = AgentSnapshot{LastAssistant: "the findings"}
	if got := s.Findings(); got != "the findings" {
		t.Errorf("no-guard case = %q; want the last answer", got)
	}
}

// TestGuardNudgeEventPinsDeliverable drives the vartija sequence through the
// real event path: report → finalize-guard user_message → housekeeping. The
// snapshot's Findings must still be the report, live and after replay.
func TestGuardNudgeEventPinsDeliverable(t *testing.T) {
	msg := func(typ, text string) Event {
		return NewEvent(typ, map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
		})
	}
	report := "## Review report\n**Asset:** user state\n**Boundary:** local user vs workspace\n- finding 1: sidecars match the transcript glob\n- finding 2: delete leaves the sidecar behind"

	a := &Agent{}
	sink := agentSink{a: a}
	applyEventToSink(msg("assistant_message", report), sink)
	applyEventToSink(msg("user_message", OpenWorkGateMessage), sink)
	applyEventToSink(msg("assistant_message", "Confirmed: all tracked items are complete."), sink)
	if got := a.Snapshot().Findings(); got != report {
		t.Errorf("live Findings after guard = %q; want the report", got)
	}
	// An ordinary user follow-up is NOT a guard nudge: the child's answer
	// to it is a real answer and must win as usual.
	applyEventToSink(msg("user_message", "and what about the release overlay?"), sink)
	applyEventToSink(msg("assistant_message", "short answer"), sink)
	if got := a.Snapshot().Findings(); got != report {
		// PreGuard (the report) is still the longer candidate — by design it
		// keeps winning until a fuller post-guard answer lands.
		t.Errorf("Findings after follow-up = %q; want the report to keep winning on substance", got)
	}

	// Replay path (reloaded agent) arbitrates identically.
	b := &Agent{}
	replayEventsIntoAgent(b, []Event{
		msg("assistant_message", report),
		msg("user_message", OpenWorkGateMessage),
		msg("assistant_message", "Confirmed: all tracked items are complete."),
	})
	if got := b.Snapshot().Findings(); got != report {
		t.Errorf("replayed Findings after guard = %q; want the report", got)
	}
}

// TestApplyEventCapturesLastAssistant pins that an assistant_message
// event feeds the agent's LastAssistant (the recap's findings source),
// that the latest message wins, and that a tool-only turn (no text)
// never clobbers real findings with emptiness.
func TestApplyEventCapturesLastAssistant(t *testing.T) {
	a := &Agent{}
	sink := agentSink{a: a}

	msg := func(text string) Event {
		return NewEvent("assistant_message", map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
		})
	}

	applyEventToSink(msg("first pass findings"), sink)
	if got := a.Snapshot().LastAssistant; got != "first pass findings" {
		t.Fatalf("after first message LastAssistant = %q", got)
	}

	// A later, fuller answer replaces the earlier one.
	applyEventToSink(msg("final findings block"), sink)
	if got := a.Snapshot().LastAssistant; got != "final findings block" {
		t.Fatalf("latest message should win, got %q", got)
	}

	// A tool-only assistant turn (no text blocks) must not wipe findings.
	applyEventToSink(NewEvent("assistant_message", map[string]any{
		"content": []any{map[string]any{"type": "tool_call", "name": "bash"}},
	}), sink)
	if got := a.Snapshot().LastAssistant; got != "final findings block" {
		t.Fatalf("tool-only turn clobbered findings: %q", got)
	}
}
