package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

func newNotesTestInteractive() *Interactive {
	i := &Interactive{dirty: make(chan struct{}, 1), turns: newTurnEngine()}
	i.cfg.Theme = tui.Theme{Muted: 8, Warning: 3, Error: 1, Tool: 2, Accent: 4}
	return i
}

func TestClearNotesRemovesOnlyOwnerNotes(t *testing.T) {
	i := newNotesTestInteractive()

	i.Notify("kagi", "info", "pending")
	i.Notify("kagi", "success", "approved")
	i.Notify("other", "info", "keep me")

	if len(i.extNotes) != 3 {
		t.Fatalf("expected 3 notes, got %d", len(i.extNotes))
	}

	i.ClearNotes("kagi")

	if len(i.extNotes) != 1 {
		t.Fatalf("expected 1 note after clear, got %d: %v", len(i.extNotes), i.extNotes)
	}
	if !strings.Contains(i.extNotes[0], "[other] ") {
		t.Fatalf("expected the surviving note to belong to other, got %q", i.extNotes[0])
	}
}

func TestClearNotesNoMatchKeepsNotes(t *testing.T) {
	i := newNotesTestInteractive()
	i.Notify("kagi", "info", "pending")

	i.ClearNotes("nope")

	if len(i.extNotes) != 1 {
		t.Fatalf("expected note to survive, got %d", len(i.extNotes))
	}
}
