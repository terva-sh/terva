package tui_test

// Stage 2 of docs/proposals/idle-suggestions.md: an offered next line, drawn
// where the user's text would go and belonging to nobody until they take it.
//
// The claims here are about what is on screen and what is in the buffer, and
// those are different things — so the load-bearing guard replays the editor
// through the VT emulator and reads the pane, rather than trusting the strings
// Render happened to return.

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"terva.sh/terva/packages/tui"
)

const ghostPrompt = "▌ "

func ghostEditor(t *testing.T, ghost string) *tui.Editor {
	t.Helper()
	ed := tui.NewEditor(ghostPrompt)
	ed.SetGhost(ghost)
	return ed
}

func renderedFirstRow(t *testing.T, ed *tui.Editor, width int) string {
	t.Helper()
	lines, _, _ := ed.Render(width)
	if len(lines) == 0 {
		t.Fatal("editor rendered no rows")
	}
	return lines[0]
}

// The offer is on screen while the composer is empty, hidden behind the user's
// own words the moment they type, and back again when they delete to empty —
// the proposal's hold-and-restore, which falls out of drawing it only when
// empty rather than out of a second slot and a timer.
func TestGhostHoldsWhileTheUserTypesAndReturnsWhenTheyStop(t *testing.T) {
	ed := ghostEditor(t, "run the tests")

	if got := renderedFirstRow(t, ed, 40); !strings.Contains(got, "run the tests") {
		t.Fatalf("the offer is not on the empty composer: %q", got)
	}

	ed.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'h'})
	got := renderedFirstRow(t, ed, 40)
	if strings.Contains(got, "run the tests") {
		t.Fatalf("the offer is still drawn behind typed text: %q", got)
	}
	if !strings.Contains(got, "h") {
		t.Fatalf("the typed text is missing: %q", got)
	}
	// Hidden, not discarded — otherwise the next assertion could only pass by
	// re-fetching a suggestion the editor has no way to ask for.
	if ed.Ghost() != "run the tests" {
		t.Fatalf("the offer was dropped on the first keystroke: %q", ed.Ghost())
	}

	ed.HandleKey(tui.Key{Kind: tui.KeyBackspace})
	if got := renderedFirstRow(t, ed, 40); !strings.Contains(got, "run the tests") {
		t.Fatalf("the offer did not come back on an empty composer: %q", got)
	}
}

// It is drawn, not held: the buffer never contains it, so an ignored offer
// cannot be submitted and the composer still reads as empty to everything that
// asks.
func TestGhostIsNeverPartOfTheBuffer(t *testing.T) {
	ed := ghostEditor(t, "run the tests")
	if !ed.IsEmpty() {
		t.Fatal("a composer holding only an offer must still read as empty")
	}
	if ed.Value() != "" {
		t.Fatalf("Value = %q, want empty", ed.Value())
	}
	if ed.SubmitValue() != "" {
		t.Fatalf("SubmitValue = %q — an unaccepted offer must never be sendable", ed.SubmitValue())
	}
}

// The caret sits where the user's own first character would go, not after the
// offer, and the composer does not grow.
//
// Its teeth are against the OTHER way to build this feature — putting the
// suggestion in the buffer and relying on typing to replace it. Do that and the
// caret lands past the offered text (verified: it moves to column 40), so the
// user's first keystroke arrives at the end of a line they did not write.
// Merely reordering the draw inside Render does not fail this one, because the
// caret is at rune 0 and the offer is drawn after it; that mistake shows up in
// the row count instead, which TestALongOfferIsBoundedToOneRow owns.
func TestGhostDoesNotMoveTheCaretOrGrowTheComposer(t *testing.T) {
	plain := tui.NewEditor(ghostPrompt)
	withGhost := ghostEditor(t, "run the tests and then look at the log")

	bare, bareRow, bareCol := plain.Render(40)
	offered, row, col := withGhost.Render(40)

	if row != bareRow || col != bareCol {
		t.Fatalf("caret moved to (%d,%d); an empty composer's caret is (%d,%d) with or without an offer",
			row, col, bareRow, bareCol)
	}
	if len(offered) != len(bare) {
		t.Fatalf("the composer grew to %d rows (empty is %d): the chat viewport is sized off this",
			len(offered), len(bare))
	}
}

// Tab is the accept key, and accepting inserts the offer as ordinary text the
// user can edit or send. Enter was rejected for this: on an empty composer it
// is a reflex, and an offer landing mid-reflex would send the model's idea as
// the user's message.
func TestTabAcceptsTheOfferAsOrdinaryText(t *testing.T) {
	ed := ghostEditor(t, "run the tests")

	ed.HandleKey(tui.Key{Kind: tui.KeyTab})
	if ed.Value() != "run the tests" {
		t.Fatalf("Value after Tab = %q, want the accepted line", ed.Value())
	}
	if ed.Ghost() != "" {
		t.Fatalf("the offer survived being accepted: %q", ed.Ghost())
	}
	// It is the user's text now: a second Tab must not paste it again.
	ed.HandleKey(tui.Key{Kind: tui.KeyTab})
	if ed.Value() != "run the tests" {
		t.Fatalf("a second Tab changed the buffer to %q", ed.Value())
	}
}

// Tab with nothing on offer stays the no-op it has always been in the editor,
// so this feature adds no behaviour to a composer that never got a suggestion.
func TestTabWithNoOfferChangesNothing(t *testing.T) {
	ed := tui.NewEditor(ghostPrompt)
	ed.SetValue("half a thought")
	ed.HandleKey(tui.Key{Kind: tui.KeyTab})
	if ed.Value() != "half a thought" {
		t.Fatalf("Tab edited the buffer: %q", ed.Value())
	}
}

// Accepting is refused while the buffer is non-empty — the same rule as
// drawing it. Otherwise Tab would insert text the user could not see was on
// hand.
func TestAnUnseenOfferCannotBeAccepted(t *testing.T) {
	ed := ghostEditor(t, "run the tests")
	ed.SetGhost("run the tests")
	ed.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'x'})

	if ed.AcceptGhost() {
		t.Fatal("accepted an offer that was not on screen")
	}
	if ed.Value() != "x" {
		t.Fatalf("buffer = %q, want the typed text alone", ed.Value())
	}
}

// Whoever installs a buffer owns the composer, so the offer is over: submit and
// Clear both come through SetValue, and Restore brings back the user's own
// parked writing, which outranks a suggestion.
func TestInstallingABufferDropsTheOffer(t *testing.T) {
	t.Run("SetValue", func(t *testing.T) {
		ed := ghostEditor(t, "run the tests")
		ed.SetValue("something else")
		if ed.Ghost() != "" {
			t.Fatalf("offer survived SetValue: %q", ed.Ghost())
		}
	})
	t.Run("Clear", func(t *testing.T) {
		ed := ghostEditor(t, "run the tests")
		ed.Clear()
		if ed.Ghost() != "" {
			t.Fatalf("offer survived Clear: %q", ed.Ghost())
		}
	})
	t.Run("Restore", func(t *testing.T) {
		parked := tui.NewEditor(ghostPrompt)
		parked.SetValue("a parked draft")
		snap := parked.State()

		ed := ghostEditor(t, "run the tests")
		ed.Restore(snap)
		if ed.Ghost() != "" {
			t.Fatalf("offer survived Restore: %q", ed.Ghost())
		}
		if ed.Value() != "a parked draft" {
			t.Fatalf("Value = %q, want the restored draft", ed.Value())
		}
	})
}

// The offer is bounded to the row it is drawn on, marked with an ellipsis so a
// trimmed suggestion cannot read as a whole one. Accepting still takes the
// whole line — what is trimmed is the view.
func TestALongOfferIsBoundedToOneRow(t *testing.T) {
	const width = 30
	long := "run the tests, then fix the failing import, then push the branch"
	ed := ghostEditor(t, long)

	lines, _, _ := ed.Render(width)
	if len(lines) != 1 {
		t.Fatalf("the offer wrapped to %d rows; it must stay on one", len(lines))
	}
	if w := runewidth.StringWidth(lines[0]); w > width {
		t.Fatalf("rendered row is %d columns wide, terminal is %d: %q", w, width, lines[0])
	}
	if !strings.Contains(lines[0], "…") {
		t.Fatalf("a trimmed offer must say so: %q", lines[0])
	}

	ed.HandleKey(tui.Key{Kind: tui.KeyTab})
	if ed.Value() != long {
		t.Fatalf("accepting inserted the TRIMMED view, not the offer: %q", ed.Value())
	}
}

// Styling is applied for display only. The stored line stays plain, because
// accepting it puts it in the buffer, where escape sequences would be sent to
// the model and counted by every width computation in the editor.
func TestOfferStylingNeverReachesTheBuffer(t *testing.T) {
	ed := ghostEditor(t, "run the tests")
	ed.GhostStyle = func(s string) string { return "<dim>" + s + "</dim>" }

	if got := renderedFirstRow(t, ed, 40); !strings.Contains(got, "<dim>run the tests</dim>") {
		t.Fatalf("the offer was drawn unstyled: %q", got)
	}
	ed.HandleKey(tui.Key{Kind: tui.KeyTab})
	if ed.Value() != "run the tests" {
		t.Fatalf("styling leaked into the buffer: %q", ed.Value())
	}
}

// The claim is about what the user sees, so this one goes all the way to the
// cells: the editor's rows are painted through the real renderer into the VT
// emulator, and the pane is read back. A Render() string assertion cannot tell
// you the caret is sitting on the prompt rather than out past the offer.
func TestTheOfferOnTheRenderedPane(t *testing.T) {
	const cols, rows = 40, 6
	ft, r := newVTRenderer(t, cols, rows, "")
	ed := ghostEditor(t, "run the tests")

	lines, caretRow, caretCol := ed.Render(cols)
	r.DrawLog([]string{"the assistant just replied"}, lines, caretRow, caretCol)

	screen := ft.Screen()
	var composer string
	for _, row := range screen.Rows() {
		if strings.Contains(row, ghostPrompt) {
			composer = row
			break
		}
	}
	if composer == "" {
		t.Fatalf("no composer row on the pane:\n%s", screen.Text())
	}
	if !strings.Contains(composer, "run the tests") {
		t.Fatalf("the offer is not on the pane: %q\n%s", composer, screen.Text())
	}
	// The caret belongs at the user's first column, in front of the offer.
	x, _ := screen.Cursor()
	if want := runewidth.StringWidth(ghostPrompt); x != want {
		t.Fatalf("caret column %d, want %d — it must sit where the user types, "+
			"not after the offered text", x, want)
	}
}
