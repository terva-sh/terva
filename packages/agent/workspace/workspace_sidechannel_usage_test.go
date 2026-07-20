package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/provider"
)

// A session's recorded cost used to be the cost of its TURNS. Every one-off
// completion the daemon runs on the side — the World router's pick, the line it
// voices, suggest, side chat — went through streamText, which read text deltas
// and nothing else. The spend was real and the session file never saw it, so a
// scene driven largely by routed turns under-reported what it had actually cost.
//
// A routed turn is the sharpest case: it is TWO model calls (route, then voice)
// and the agent's own Run never executes, so before this fix such a turn booked
// exactly nothing.
func TestRoutedTurnBooksBothSideChannelCalls(t *testing.T) {
	cl := &scriptedClient{replies: []string{"Elira", "*She turns.* \"You came back.\""}}
	s := worldTestSession(t, cl, map[string]string{"Elira": "elira-ref"})
	sub := s.hub.add(nil, true)

	// Usage rows are written by the usage observers, so count what they see.
	var booked []provider.Usage
	s.agent.AddUsageObserver(func(u, _ provider.Usage) {
		booked = append(booked, u)
	})

	if err := s.prompt("I open the door.", nil); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	drainUntil(t, sub, "done")
	waitIdle(t, s)

	if len(cl.requests()) != 2 {
		t.Fatalf("precondition: expected router + voice = 2 model calls, got %d", len(cl.requests()))
	}
	if len(booked) != 2 {
		t.Fatalf("2 model calls were made but %d were booked — a side-channel call is spending unrecorded", len(booked))
	}

	// The running total carries both calls...
	want := scriptedCallUsage.Add(scriptedCallUsage)
	if got := s.agent.Cost(); got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens {
		t.Errorf("cumulative cost = %+v, want %+v", got, want)
	}

	// ...but the per-turn snapshot does NOT. That gauge measures this session's
	// CONTEXT, and a side-channel request's prompt is not this session's context —
	// letting one overwrite it would leave threshold checks reading a size the
	// transcript never had. (Same reason compaction books total-only.)
	if got := s.agent.LastTurnUsage(); got != (provider.Usage{}) {
		t.Errorf("side-channel spend leaked into the per-turn context gauge: %+v", got)
	}
}

// The card doctor and the title generator were the two one-off completions
// that did NOT go through streamText — each drained its own stream and dropped
// EventUsage, so a doctor pass or a title refresh spent real money the session
// file never saw. Both now book against the session they run for; nil is the
// deliberate form for callers with no session meter (a Library-scoped doctor
// run, GenerateSessionTitle's cold path).

func TestDoctorRunBooksUsage(t *testing.T) {
	reply := `{"note":"fine","proposals":[]}`
	cl := &scriptedClient{replies: []string{reply, reply}}
	s := newTurnTestSession(t, cl)
	var booked []provider.Usage
	s.agent.AddUsageObserver(func(u, _ provider.Usage) { booked = append(booked, u) })

	fields := doctorFields(card.Card{Name: "Ivy"})
	res, err := doctorRun(context.Background(), cl, "fake-model", "system", "user", fields, s)
	if err != nil {
		t.Fatalf("doctorRun: %v", err)
	}
	if res.Note != "fine" {
		t.Errorf("note = %q", res.Note)
	}
	if len(booked) != 1 || booked[0] != scriptedCallUsage {
		t.Fatalf("doctor call booked %v, want exactly one %+v — the doctor is spending unrecorded", booked, scriptedCallUsage)
	}

	// A plain Library-scoped run has no session: nil must stay a safe no-op.
	if _, err := doctorRun(context.Background(), cl, "fake-model", "system", "user", fields, nil); err != nil {
		t.Fatalf("nil-session doctorRun: %v", err)
	}
}

func TestGenerateTitleBooksUsage(t *testing.T) {
	cl := &scriptedClient{replies: []string{"A Fine Title", "Another"}}
	s := newTurnTestSession(t, cl)
	var booked []provider.Usage
	s.agent.AddUsageObserver(func(u, _ provider.Usage) { booked = append(booked, u) })

	if got := generateTitle(context.Background(), cl, "fake-model", "seed", s); got != "A Fine Title" {
		t.Errorf("title = %q", got)
	}
	if len(booked) != 1 || booked[0] != scriptedCallUsage {
		t.Fatalf("title call booked %v, want exactly one %+v — titling is spending unrecorded", booked, scriptedCallUsage)
	}

	// GenerateSessionTitle on a cold session has no agent to book against:
	// nil must stay a safe no-op and still produce the title.
	if got := generateTitle(context.Background(), cl, "fake-model", "seed", nil); got != "Another" {
		t.Errorf("nil-session title = %q", got)
	}
}
