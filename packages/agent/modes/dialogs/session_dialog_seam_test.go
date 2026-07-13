package dialogs

import (
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
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

// The `g` binding emits a GenerateTitle action for the cursor row; the host
// runs the (blocking) generation and lands the result via SetTitleFor, which
// no-ops when the row has since disappeared.
func TestSessionDialogGenerateTitleAction(t *testing.T) {
	d := NewSessionDialog()
	d.List = func() []core.SessionSummary {
		return []core.SessionSummary{
			{Path: "/s/a.jsonl", Title: "old title", MessageCount: 3},
			{Path: "/s/b.jsonl", MessageCount: 2},
		}
	}
	d.Open("/nonexistent-root", "/nonexistent-cwd")

	act := d.HandleKey(tui.Key{Kind: tui.KeyDown})
	if act.GenerateTitle {
		t.Fatal("cursor move must not trigger generation")
	}
	act = d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'g'})
	if !act.GenerateTitle || act.Path != "/s/b.jsonl" {
		t.Fatalf("g action = %+v, want GenerateTitle for the cursor row", act)
	}

	d.SetTitleFor("/s/b.jsonl", "fresh title")
	if d.sessions[1].Title != "fresh title" {
		t.Fatalf("SetTitleFor did not update the row: %+v", d.sessions[1])
	}
	d.SetTitleFor("/s/gone.jsonl", "whatever") // must not panic or mutate
	if d.sessions[0].Title != "old title" {
		t.Fatalf("unrelated row mutated: %+v", d.sessions[0])
	}
}
