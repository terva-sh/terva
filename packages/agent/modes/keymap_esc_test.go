package modes

import (
	"context"
	"testing"

	"terva.sh/terva/packages/tui"
)

// newEscTestInteractive is the smallest Interactive that can take an Esc: the
// suggesters stay nil (both are nil-safe on an empty editor), and the editor
// exists so keyEsc can ask it for the current value.
func newEscTestInteractive() *Interactive {
	i := &Interactive{
		dirty: make(chan struct{}, 1),
		turns: newTurnEngine(),
		ed:    tui.NewEditor("> "),
	}
	i.cfg.Theme = tui.Theme{Muted: 8, Warning: 3, Error: 1, Tool: 2, Accent: 4}
	return i
}

// Esc clears the ✖/✓ status lines WITHOUT consuming the press. The gap this
// pins from one side: a worktree sweep's "removed 6, kept 6" report used to
// sit pinned until the next prompt, the one piece of chrome Esc couldn't
// touch. And from the other: the clear must ride along on a pass-through,
// because a persistent ambient status ("not logged in") taxing every
// draft-clearing Esc with an extra keypress was the regression the first cut
// of this fix shipped.
func TestEscClearsStatusLinesWithoutConsumingThePress(t *testing.T) {
	i := newEscTestInteractive()
	i.statusErr = "internal: removed 6, kept 6 with uncommitted or unmerged work"
	i.statusOK = "removed worktree x"
	i.ed.Insert("a half-typed draft")

	if got := i.keyEsc(context.Background(), tui.Key{Kind: tui.KeyEsc}); got != keyPass {
		t.Fatalf("esc over status lines = %v, want keyPass (the draft still needs it)", got)
	}
	if i.statusErr != "" || i.statusOK != "" {
		t.Errorf("status lines survived esc: err=%q ok=%q", i.statusErr, i.statusOK)
	}
}

// When notes are up, the press is consumed for the block as before — and the
// status lines still go with it, so dismissal is one gesture, not a hunt.
func TestEscTakesStatusAndNotesTogether(t *testing.T) {
	i := newEscTestInteractive()
	i.statusErr = "boom"
	i.Notify("workspace", "info", "2 swarm worktrees leased")

	if got := i.keyEsc(context.Background(), tui.Key{Kind: tui.KeyEsc}); got != keyHandled {
		t.Fatalf("esc = %v, want keyHandled", got)
	}
	if i.statusErr != "" || len(i.extNotes) != 0 {
		t.Errorf("chrome survived one esc: err=%q notes=%d", i.statusErr, len(i.extNotes))
	}

	// Nothing left: the next press passes through untouched.
	if got := i.keyEsc(context.Background(), tui.Key{Kind: tui.KeyEsc}); got != keyPass {
		t.Errorf("esc with nothing to dismiss = %v, want keyPass", got)
	}
}
