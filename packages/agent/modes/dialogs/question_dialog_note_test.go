package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// typeInto sends a string to whatever field is open.
func typeInto(d *QuestionDialog, s string) {
	for _, r := range s {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
}

// "Mostly this, but here is some extra context" had no shape in this
// dialog: the choice and the free-text answer were alternatives, so
// saying anything extra meant abandoning the option and retyping it.
// A note rides along with the choice instead.
func TestQuestionDialogNoteRidesWithTheChosenOption(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{
		Question: "Which database?", Options: []string{"Postgres", "SQLite"},
	})
	d.HandleKey(tui.Key{Kind: tui.KeyDown})            // onto SQLite
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'}) // annotate it
	typeInto(d, "for now — revisit when we shard")
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	ans := one(t, resp)
	if ans.Answer != "SQLite" {
		t.Fatalf("the note replaced the choice: %+v", ans)
	}
	if ans.Note != "for now — revisit when we shard" {
		t.Fatalf("note = %q, want the typed text", ans.Note)
	}
}

// The note is bound to the option it was written against. Moving to a
// different answer must not drag someone's reasoning about Postgres onto
// their choice of SQLite.
func TestQuestionDialogNoteDoesNotFollowTheCursor(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{
		Question: "Which database?", Options: []string{"Postgres", "SQLite"},
	})
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	typeInto(d, "about postgres specifically")
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})  // back to the list, note kept
	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // ...but onto the other option
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	ans := one(t, resp)
	if ans.Answer != "SQLite" {
		t.Fatalf("answer = %q, want SQLite", ans.Answer)
	}
	if ans.Note != "" {
		t.Fatalf("a note written about another option was sent anyway: %q", ans.Note)
	}
}

// Going back to the option brings its note back — it was kept, not
// discarded, so revising a choice twice does not cost the reasoning.
func TestQuestionDialogNoteReturnsWithItsOption(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{
		Question: "Which database?", Options: []string{"Postgres", "SQLite"},
	})
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	typeInto(d, "keep this")
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // away
	d.HandleKey(tui.Key{Kind: tui.KeyUp})   // and back
	if got := plainRender(d, 60); !strings.Contains(got, "keep this") {
		t.Fatalf("the note did not come back with its option:\n%s", got)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := one(t, resp); ans.Note != "keep this" {
		t.Fatalf("note = %q, want it restored", ans.Note)
	}
}

// The note and the custom answer are separate fields. One editor with a
// flag would put the custom text in the note box and vice versa.
func TestQuestionDialogNoteAndCustomAnswerDoNotShareABuffer(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{
		Question: "Which?", Options: []string{"Postgres"}, AllowCustom: true,
	})
	// Write a custom answer, back out, then annotate the real option.
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	typeInto(d, "something else entirely")
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	d.HandleKey(tui.Key{Kind: tui.KeyUp}) // onto Postgres
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	if got := plainRender(d, 60); strings.Contains(got, "something else entirely") {
		t.Fatalf("the custom answer leaked into the note field:\n%s", got)
	}
	typeInto(d, "with pgbouncer")
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	ans := one(t, resp)
	if ans.Answer != "Postgres" || ans.Note != "with pgbouncer" {
		t.Fatalf("got %+v, want Postgres + its note", ans)
	}
}

// The custom row is already the user's own words; a note on it would be a
// second box for the same thing.
func TestQuestionDialogNoOnTheCustomRow(t *testing.T) {
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{
		Question: "Which?", Options: []string{"Postgres"}, AllowCustom: true,
	})
	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // onto the custom row
	if got := legendRow(t, d, 90); strings.Contains(got, "note") {
		t.Fatalf("the custom row offers a note: %q", got)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	if got := plainRender(d, 60); strings.Contains(got, "note on") {
		t.Fatalf("n opened a note on the custom row:\n%s", got)
	}
}

// The affordance has to be visible, and it has to say which of the two
// things it will do.
func TestQuestionDialogLegendAdvertisesTheNote(t *testing.T) {
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{Question: "Which?", Options: []string{"Postgres", "SQLite"}})
	if got := legendRow(t, d, 90); !strings.Contains(got, "n adds note") {
		t.Fatalf("nothing advertises the note key: %q", got)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	typeInto(d, "written")
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if got := legendRow(t, d, 90); !strings.Contains(got, "n edits note") {
		t.Fatalf("an existing note still reads as if there were none: %q", got)
	}
}

// The note field names the option it is attached to: by the time it is
// open, the row it belongs to is off screen.
func TestQuestionDialogNoteFieldNamesItsOption(t *testing.T) {
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{Question: "Which?", Options: []string{"Postgres", "SQLite"}})
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	if got := plainRender(d, 60); !strings.Contains(got, "note on “SQLite”") {
		t.Fatalf("the note field does not say what it annotates:\n%s", got)
	}
}

// In a set the note commits like any other answer — enter moves on — and
// the review tab shows it under the answer rather than merged into it.
func TestQuestionSetShowsNotesOnTheSubmitTab(t *testing.T) {
	d := NewQuestionDialog()
	resp := askN(d,
		core.UserQuestion{Question: "one?", Options: []string{"a", "b"}},
		core.UserQuestion{Question: "two?", Options: []string{"x"}})
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	typeInto(d, "with caveats")
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // commits the note, moves on
	d.HandleKey(tui.Key{Kind: tui.KeyTab})   // onto submit

	body := plainRender(d, 70)
	if !strings.Contains(body, "with caveats") {
		t.Fatalf("the review tab hides the note:\n%s", body)
	}
	if strings.Contains(body, "a with caveats") {
		t.Fatalf("the review tab merged the note into the answer:\n%s", body)
	}

	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	ans := <-resp
	if len(ans) != 2 {
		t.Fatalf("want 2 answers, got %d", len(ans))
	}
	if ans[0].Answer != "a" || ans[0].Note != "with caveats" {
		t.Fatalf("answer 0 = %+v, want a + its note", ans[0])
	}
	if ans[1].Note != "" {
		t.Fatalf("answer 1 picked up a note it never had: %+v", ans[1])
	}
}

// A note the user opened and left empty is not a note.
func TestQuestionDialogEmptyNoteIsNotSent(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{Question: "Which?", Options: []string{"Postgres"}})
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	ans := one(t, resp)
	if ans.Answer != "Postgres" {
		t.Fatalf("answer = %q", ans.Answer)
	}
	if ans.Note != "" {
		t.Fatalf("an empty note was sent: %q", ans.Note)
	}
}

// The note shows under its option in the list, so an annotated answer
// does not look identical to one merely picked.
func TestQuestionDialogNoteShowsUnderItsOption(t *testing.T) {
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{Question: "Which?", Options: []string{"Postgres", "SQLite"}})
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	typeInto(d, "with pgbouncer in front")
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})

	rows := choiceLines(t, d, choiceTheme(), 60)
	var joined []string
	for _, r := range rows {
		joined = append(joined, r)
	}
	if !strings.Contains(strings.Join(joined, "\n"), "with pgbouncer in front") {
		t.Fatalf("the note is invisible in the list:\n%s", strings.Join(joined, "\n"))
	}
}
