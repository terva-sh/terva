package dialogs

import (
	"testing"

	"terva.sh/terva/packages/core"
)

// Seam test: Open filters empty sessions out of the wire list.

// TestSessionDialogListSeam: the /sessions picker consumes an injected lister
// (the carrier path wires the service's session group here) instead of
// scanning disk, and the empty-session filter still applies to wire rows.
func TestSessionDialogListSeam(t *testing.T) {
	d := NewSessionDialog()
	d.List = func() []core.SessionSummary {
		return []core.SessionSummary{
			{Path: "/s/a.jsonl", Title: "first", MessageCount: 3},
			{Path: "/s/empty.jsonl", MessageCount: 0},
		}
	}
	d.Open("/nonexistent-root", "/nonexistent-cwd")
	if len(d.sessions) != 1 || d.sessions[0].Path != "/s/a.jsonl" {
		t.Fatalf("sessions = %+v, want just the non-empty wire row", d.sessions)
	}
}
