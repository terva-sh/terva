package tui_test

import (
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
	"terva.sh/terva/packages/tui/tuitest"
)

// The escape-stream tests assert what was EMITTED. They cannot see what the
// screen ends up showing, and that is the only thing a user experiences. This
// replays through the VT emulator and reads the grid.
func TestReflowShrinkLeavesNoStaleRowsOnScreen(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "")
	ft := tuitest.NewFakeTerm(40, 12)
	r := tui.NewRenderer(ft)
	r.Resize(40, 12)

	chat := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		chat = append(chat, fmt.Sprintf("row-%02d", i))
	}
	bottom := []string{"> "}
	r.DrawLog(chat, bottom, 0, 2)

	// A change ABOVE the viewport that also SHRINKS the buffer: the classic
	// "collapse tool output" shape that ctrl+t / ctrl+o produce.
	shrunk := append([]string{}, chat[:2]...)
	shrunk = append(shrunk, chat[10:]...) // drop 8 rows above the viewport
	r.DrawLog(shrunk, bottom, 0, 2)

	screen := strings.Join(ft.Screen().Rows(), "\n")
	// The last rows of the shrunken transcript must be on screen...
	if !strings.Contains(screen, "row-29") {
		t.Errorf("the new tail is not on screen:\n%s", screen)
	}
	// ...and nothing may appear twice.
	for i := 0; i < 30; i++ {
		row := fmt.Sprintf("row-%02d", i)
		if n := strings.Count(screen, row); n > 1 {
			t.Errorf("%q appears %d times on screen — stale rows were not cleared:\n%s", row, n, screen)
		}
	}
}

// The same shape, but shrinking so far that the tail moves UP the screen and
// leaves rows below it that must be erased.
func TestReflowShrinkErasesBelowTheNewTail(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "")
	ft := tuitest.NewFakeTerm(40, 12)
	r := tui.NewRenderer(ft)
	r.Resize(40, 12)

	chat := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		chat = append(chat, fmt.Sprintf("row-%02d", i))
	}
	bottom := []string{"> "}
	r.DrawLog(chat, bottom, 0, 2)

	// Keep the head, drop most of the middle: buffer 30 -> 14.
	shrunk := append([]string{}, chat[:2]...)
	shrunk = append(shrunk, chat[18:]...)
	r.DrawLog(shrunk, bottom, 0, 2)

	rows := ft.Screen().Rows()
	screen := strings.Join(rows, "\n")
	for i := 2; i < 18; i++ {
		row := fmt.Sprintf("row-%02d", i)
		if strings.Contains(screen, row) {
			t.Errorf("dropped row %q is still on screen:\n%s", row, screen)
		}
	}
}

// Repro attempt for a reported display bug: typing "/" opens the slash popup
// (the bottom band GROWS), then typing a letter narrows the matches (the band
// SHRINKS by a row). Reported symptom: the cursor lands a line above the
// composer and the typed text is invisible until one more key is pressed.
func TestBottomBandGrowThenShrinkKeepsComposerVisible(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "")
	ft := tuitest.NewFakeTerm(40, 12)
	r := tui.NewRenderer(ft)
	r.Resize(40, 12)

	chat := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		chat = append(chat, fmt.Sprintf("row-%02d", i))
	}

	// Idle: just the composer.
	r.DrawLog(chat, []string{"> "}, 0, 2)

	// "/" typed: popup with four matches appears ABOVE the composer.
	popup4 := []string{"/help", "/hooks", "/history", "/hush", "> /"}
	r.DrawLog(chat, popup4, 4, 3)

	// "h" typed: matches narrow to three. Band shrinks by one row.
	popup3 := []string{"/help", "/hooks", "/history", "> /h"}
	r.DrawLog(chat, popup3, 3, 4)

	rows := ft.Screen().Rows()
	screen := strings.Join(rows, "\n")
	if !strings.Contains(screen, "> /h") {
		t.Errorf("composer text '> /h' is not on screen:\n%s", screen)
	}
	// The cursor must sit on the composer row, not above it.
	_, cy := ft.Screen().Cursor()
	composerRow := -1
	for y, row := range rows {
		if strings.Contains(row, "> /h") {
			composerRow = y
		}
	}
	if composerRow < 0 {
		t.Fatalf("composer row not found:\n%s", screen)
	}
	if cy != composerRow {
		t.Errorf("cursor is on row %d but the composer is on row %d:\n%s", cy, composerRow, screen)
	}
	// And a stale copy of the wider popup must not linger.
	if n := strings.Count(screen, "/hush"); n > 0 {
		t.Errorf("the dropped '/hush' row is still on screen (%d):\n%s", n, screen)
	}
}

// Regression: typing "/" opens a slash popup listing every command, which can
// make the BOTTOM BAND taller than the screen. Then len(chatFrame) is below
// logViewportTop, so a change inside the popup satisfies
// firstChanged < logViewportTop and used to trigger the chat rebase — with a
// chat delta of 0, marking never-painted popup rows as already painted.
//
// Symptom on a real terminal: the typed text is invisible and the cursor sits
// a row above the composer until one more keystroke happens to dirty a row
// below the anchor.
func TestTallPopupChangeIsPaintedNotAccepted(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "")
	ft := tuitest.NewFakeTerm(40, 10)
	r := tui.NewRenderer(ft)
	r.Resize(40, 10)

	chat := []string{"chat-a", "chat-b", "chat-c"}

	// "/" typed: a popup taller than the 10-row screen.
	wide := []string{}
	for i := 0; i < 14; i++ {
		wide = append(wide, fmt.Sprintf("/cmd-%02d", i))
	}
	wide = append(wide, "> /")
	r.DrawLog(chat, wide, len(wide)-1, 3)

	// "h" typed: the list narrows and its CONTENT changes.
	narrow := []string{}
	for i := 0; i < 12; i++ {
		narrow = append(narrow, fmt.Sprintf("/help-%02d", i))
	}
	narrow = append(narrow, "> /h")
	r.DrawLog(chat, narrow, len(narrow)-1, 4)

	screen := strings.Join(ft.Screen().Rows(), "\n")
	if !strings.Contains(screen, "> /h") {
		t.Errorf("the typed text is not on screen — popup rows were accepted without painting:\n%s", screen)
	}
	// None of the pre-narrowing entries may survive.
	for i := 0; i < 14; i++ {
		if stale := fmt.Sprintf("/cmd-%02d", i); strings.Contains(screen, stale) {
			t.Errorf("stale popup row %q still on screen:\n%s", stale, screen)
		}
	}
}

// The exact sequence reported from a live terminal: type "/", then narrow the
// popup key by key, then over-type into NO matches, then backspace back into
// matches. The composer text must be visible and correct at every step.
//
// Over-typing was the user's own workaround — with no matches the popup
// collapses, the tall bottom band drops below the screen, and painting
// recovers. A test that never reaches zero matches would miss the whole
// mechanism.
func TestSlashPopupNarrowingKeepsComposerVisibleThroughout(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "")
	ft := tuitest.NewFakeTerm(48, 10)
	r := tui.NewRenderer(ft)
	r.Resize(48, 10)
	chat := []string{"chat-a", "chat-b"}

	popup := func(n int, typed string) []string {
		rows := make([]string, 0, n+1)
		for i := 0; i < n; i++ {
			rows = append(rows, fmt.Sprintf("/match-%02d", i))
		}
		return append(rows, "> "+typed)
	}

	steps := []struct {
		matches int
		typed   string
	}{
		{18, "/"},         // every command: band far taller than the screen
		{9, "/s"},         //
		{4, "/se"},        //
		{2, "/sessions"},  //
		{0, "/sessionss"}, // no matches: the band collapses
		{2, "/sessions"},  // backspace: matches return
	}
	for _, st := range steps {
		r.DrawLog(chat, popup(st.matches, st.typed), st.matches, len(st.typed)+2)
		screen := strings.Join(ft.Screen().Rows(), "\n")
		want := "> " + st.typed
		if !strings.Contains(screen, want) {
			t.Fatalf("after typing %q the composer %q is not on screen:\n%s", st.typed, want, screen)
		}
		// The cursor must be on the composer row, not floating above it.
		_, cy := ft.Screen().Cursor()
		composerRow := -1
		for y, row := range ft.Screen().Rows() {
			if strings.Contains(row, want) {
				composerRow = y
			}
		}
		if cy != composerRow {
			t.Errorf("typed %q: cursor on row %d, composer on row %d:\n%s",
				st.typed, cy, composerRow, screen)
		}
	}
}
