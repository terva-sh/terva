package core

import (
	"context"
	"testing"

	"terva.sh/terva/packages/provider"
)

// ReadReplayRows and StreamReplayRows are two readers of the same file. One
// routes through walkSession's hooks; the other re-implements the usage decode
// inline, for a reason that stands (the stream must retain nothing).
//
// Re-implementing it dropped a field. The inline struct declared Usage,
// Cumulative and At, while the walk hook also carried Delegated — and
// session_inspect, the ONLY production reader of ReplayRow.Delegated, reads
// through the streaming one. So `if r.Delegated` had never once been true in
// production: a sub-agent's cold prompt was counted as one of this session's own
// turns, and the cache-hit rate the tool calls "the single most actionable
// number here" was computed over the child's prompts.
//
// The guard that looked like coverage tested ReadReplayRows — the reader no
// consumer uses. This one requires the two to AGREE, which is the property that
// makes a second implementation safe.
func TestBothReplayReadersAgreeOnEveryRow(t *testing.T) {
	s := mvNewSession(t,
		mvMsg(provider.RoleUser, "u0"),
		mvMsg(provider.RoleAssistant, "a1"),
	)
	// One of this session's own turns, and one delegated to a sub-agent.
	if err := s.AppendUsage(provider.Usage{InputTokens: 100, OutputTokens: 10}, provider.Usage{InputTokens: 100, OutputTokens: 10}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendDelegatedUsage(provider.Usage{InputTokens: 250000, OutputTokens: 500}, provider.Usage{InputTokens: 250100, OutputTokens: 510}); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	want, _, err := ReadReplayRows(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []ReplayRow
	if _, _, err := StreamReplayRows(context.Background(), path, 0, func(_ int, r ReplayRow) {
		got = append(got, r)
	}); err != nil {
		t.Fatal(err)
	}

	if len(got) != len(want) {
		t.Fatalf("row counts differ: stream %d, read %d", len(got), len(want))
	}
	// Guard the guard: a fixture with no delegated row would make the field
	// comparison below vacuous, which is how this went unnoticed.
	delegated := 0
	for _, r := range want {
		if r.Kind == ReplayRowUsage && r.Delegated {
			delegated++
		}
	}
	if delegated != 1 {
		t.Fatalf("fixture has %d delegated usage rows, want exactly 1 — "+
			"without one this test cannot detect a dropped Delegated flag", delegated)
	}

	for i := range want {
		if got[i].Kind != want[i].Kind {
			t.Errorf("row %d: kind stream=%v read=%v", i, got[i].Kind, want[i].Kind)
			continue
		}
		if want[i].Kind != ReplayRowUsage {
			continue
		}
		if got[i].Delegated != want[i].Delegated {
			t.Errorf("row %d: Delegated stream=%v read=%v — the two readers disagree, so whichever "+
				"one a consumer picked decides whether sub-agent spend is counted as this session's own",
				i, got[i].Delegated, want[i].Delegated)
		}
		if got[i].Usage != want[i].Usage {
			t.Errorf("row %d: Usage stream=%+v read=%+v", i, got[i].Usage, want[i].Usage)
		}
		if got[i].Cumulative != want[i].Cumulative {
			t.Errorf("row %d: Cumulative stream=%+v read=%+v", i, got[i].Cumulative, want[i].Cumulative)
		}
	}
}
