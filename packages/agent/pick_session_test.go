package agent

import (
	"testing"

	"terva.sh/terva/packages/core"
)

// The pre-TUI --resume fallback picker filters zero-message sessions, the
// same rule as the in-TUI dialog: resuming an empty session is a no-op, and
// fresh/crashed empties would otherwise pad the list until the next prune.
func TestPickableSessionsDropsEmpties(t *testing.T) {
	all := []core.SessionSummary{
		{Path: "a.jsonl", MessageCount: 3, Title: "kept"},
		{Path: "b.jsonl", MessageCount: 0},
		{Path: "c.jsonl", MessageCount: 1},
	}
	got := pickableSessions(all)
	if len(got) != 2 {
		t.Fatalf("want 2 pickable sessions, got %d: %+v", len(got), got)
	}
	if got[0].Path != "a.jsonl" || got[1].Path != "c.jsonl" {
		t.Fatalf("wrong sessions survived the filter: %+v", got)
	}
}
