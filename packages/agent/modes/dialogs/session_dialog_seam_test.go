package dialogs

import (
	"strings"
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

// Refresh re-lists an OPEN picker (the attached TUI got a sessions_changed
// broadcast) and keeps the cursor on the same session even when its index
// shifts; a closed dialog never re-lists.
func TestSessionDialogRefreshPreservesCursor(t *testing.T) {
	rows := []core.SessionSummary{
		{Path: "/s/a.jsonl", Title: "a", MessageCount: 3},
		{Path: "/s/b.jsonl", Title: "b", MessageCount: 2},
		{Path: "/s/c.jsonl", Title: "c", MessageCount: 1},
	}
	d := NewSessionDialog()
	d.List = func() []core.SessionSummary { return rows }
	d.Open("/x", "/y")
	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // cursor -> b
	if d.sessions[d.cursor].Path != "/s/b.jsonl" {
		t.Fatalf("precondition: cursor not on b: %+v", d.sessions[d.cursor])
	}

	// Another client deletes "a"; b shifts from index 1 to 0. Refresh must keep
	// the cursor on b, not on whatever now sits at the old index.
	rows = rows[1:]
	d.Refresh("/x", "/y")
	if d.sessions[d.cursor].Path != "/s/b.jsonl" {
		t.Fatalf("Refresh lost the cursor's session: got %+v", d.sessions[d.cursor])
	}

	// A closed dialog is never re-listed.
	d.Close()
	rows = nil
	d.Refresh("/x", "/y")
	if len(d.sessions) == 0 {
		t.Fatal("Refresh re-listed a closed dialog")
	}
}

// A row's leading glyph reflects the session's live state — ● a turn in flight,
// ○ materialized but idle, two spaces for a cold on-disk session — so the
// attached /sessions picker shows at a glance which sessions are running.
func TestFormatSessionRowStatusGlyph(t *testing.T) {
	cases := []struct {
		name string
		s    core.SessionSummary
		want string
	}{
		{"busy", core.SessionSummary{Live: true, Busy: true, Title: "x", MessageCount: 1}, "● "},
		{"live idle", core.SessionSummary{Live: true, Title: "x", MessageCount: 1}, "○ "},
		{"cold", core.SessionSummary{Title: "x", MessageCount: 1}, "  "},
	}
	for _, c := range cases {
		row := FormatSessionRowPlain(c.s, 80)
		if !strings.HasPrefix(row, c.want) {
			t.Errorf("%s: row %q does not start with glyph %q", c.name, row, c.want)
		}
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
