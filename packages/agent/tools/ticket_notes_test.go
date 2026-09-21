package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	ticket "github.com/terva-sh/git-ticket/ticket"
)

// ticketWithNotes seeds one ticket and appends n notes to it, and returns the
// core, the id, and the raw Notes section as the file holds it. The raw section
// is what every test here compares against, because the point of the change is
// that a reading and the file cannot disagree.
func ticketWithNotes(t *testing.T, n int) (*TicketCore, string, string) {
	t.Helper()
	tc := ticketToolStore(t, 1)
	s, err := tc.open()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ts, err := s.List(ctx, ticket.Filter{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 {
		t.Fatalf("seed store holds %d tickets", len(ts))
	}
	id := ts[0].ID
	for i := 1; i <= n; i++ {
		cur, err := s.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Apply(ctx, id,
			ticket.AppendNote{Text: fmt.Sprintf("Note number %d.\n\nA second paragraph, so an entry is more than one line.", i)},
			ticket.ApplyOptions{IfRevision: cur.Revision, Actor: ticket.Actor{ID: "agent:test", Name: "Test"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	cur, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return tc, id, cur.Body.Notes
}

func ticketGetNotes(t *testing.T, tc *TicketCore, ref, notes string) (ticketFull, bool, string) {
	t.Helper()
	get := &TicketGetTool{TicketCore: tc}
	args := map[string]string{"ref": ref}
	if notes != "" {
		args["notes"] = notes
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := get.Execute(context.Background(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := ticketResultText(t, res)
	if res.IsError {
		return ticketFull{}, true, text
	}
	var full ticketFull
	if err := json.Unmarshal([]byte(text), &full); err != nil {
		t.Fatal(err)
	}
	return full, false, text
}

// The case the change exists to protect. Most tickets carry one note, and a
// ticket_get that made the common read cost a second call would trade a real
// saving on the worst tickets for a tax on everything else.
func TestTicketGetKeepsASingleNoteWhole(t *testing.T) {
	tc, id, raw := ticketWithNotes(t, 1)
	full, isErr, text := ticketGetNotes(t, tc, id, "")
	if isErr {
		t.Fatalf("tool refused: %s", text)
	}
	if full.Notes != raw {
		t.Fatalf("one note was not returned whole:\n got %q\nwant %q", full.Notes, raw)
	}
	if strings.Contains(full.Notes, "hidden") {
		t.Errorf("one note earned a stand-in line: %q", full.Notes)
	}
}

// A ticket with no notes at all reads the same as it did, which is to say the
// field is absent rather than carrying a stand-in for nothing.
func TestTicketGetWithNoNotes(t *testing.T) {
	tc := ticketToolStore(t, 1)
	list := &TicketListTool{TicketCore: tc}
	res, _ := list.Execute(context.Background(), nil, nil)
	id := ticketPageFrom(t, res).Tickets[0].ID

	full, isErr, text := ticketGetNotes(t, tc, id, "")
	if isErr {
		t.Fatalf("tool refused: %s", text)
	}
	if full.Notes != "" {
		t.Errorf("a ticket with no notes returned %q", full.Notes)
	}
}

// The elision itself: the newest note whole, and one line that names how many
// were withheld and the range they span.
func TestTicketGetElidesOlderNotes(t *testing.T) {
	tc, id, raw := ticketWithNotes(t, 5)
	full, isErr, text := ticketGetNotes(t, tc, id, "")
	if isErr {
		t.Fatalf("tool refused: %s", text)
	}
	if !strings.Contains(full.Notes, "Note number 5.") {
		t.Errorf("the newest note is missing: %q", full.Notes)
	}
	for i := 1; i <= 4; i++ {
		if strings.Contains(full.Notes, fmt.Sprintf("Note number %d.", i)) {
			t.Errorf("note %d was not elided", i)
		}
	}
	// The stand-in has to name the count and the range, or the reader has a
	// puzzle rather than a summary.
	if !strings.Contains(full.Notes, "4 earlier notes, 1-4 hidden") {
		t.Errorf("the stand-in does not name the count and the range: %q", full.Notes)
	}
	// And it has to name the way back, inside the tool surface.
	if !strings.Contains(full.Notes, `notes="1-4"`) || !strings.Contains(full.Notes, `notes="list"`) {
		t.Errorf("the stand-in does not name the retrieval path: %q", full.Notes)
	}
	if len(full.Notes) >= len(raw) {
		t.Errorf("elided notes are %d bytes against a raw %d", len(full.Notes), len(raw))
	}
}

// One withheld note is a note, not "1 earlier notes".
func TestTicketGetStandInCountsOneNote(t *testing.T) {
	tc, id, _ := ticketWithNotes(t, 2)
	full, isErr, text := ticketGetNotes(t, tc, id, "")
	if isErr {
		t.Fatalf("tool refused: %s", text)
	}
	if !strings.Contains(full.Notes, "1 earlier note, 1 hidden") {
		t.Errorf("stand-in for a single hidden note: %q", full.Notes)
	}
}

// notes=all is the section byte for byte, which is what ticket_get returned
// before the elision existed. The escape hatch has to be exact, or the elision
// has hidden something a caller cannot recover.
func TestTicketGetNotesAllIsTheRawSection(t *testing.T) {
	tc, id, raw := ticketWithNotes(t, 5)
	full, isErr, text := ticketGetNotes(t, tc, id, "all")
	if isErr {
		t.Fatalf("tool refused: %s", text)
	}
	if full.Notes != raw {
		t.Fatalf("notes=all did not return the raw section:\n got %q\nwant %q", full.Notes, raw)
	}
}

// notes=list is what makes a range aimable: a reader cannot ask for note 3
// without a way to see which one that is.
func TestTicketGetNotesList(t *testing.T) {
	tc, id, _ := ticketWithNotes(t, 4)
	full, isErr, text := ticketGetNotes(t, tc, id, "list")
	if isErr {
		t.Fatalf("tool refused: %s", text)
	}
	lines := strings.Split(strings.TrimSpace(full.Notes), "\n")
	if len(lines) != 8 {
		t.Fatalf("index of 4 notes is %d lines:\n%s", len(lines), full.Notes)
	}
	for i := 1; i <= 4; i++ {
		if !strings.Contains(full.Notes, fmt.Sprintf("Note number %d.", i)) {
			t.Errorf("note %d is not in the index:\n%s", i, full.Notes)
		}
	}
	// An index is an index. The second paragraph of each note stays out of it.
	if strings.Contains(full.Notes, "A second paragraph") {
		t.Errorf("the index carried a whole note:\n%s", full.Notes)
	}
	if !strings.Contains(full.Notes, "agent:test") {
		t.Errorf("the index names no actor:\n%s", full.Notes)
	}
}

// A range returns those notes and no others, in the shape the file carries
// them, so a note read here and the same note in the Markdown are the same text.
func TestTicketGetNotesRange(t *testing.T) {
	tc, id, _ := ticketWithNotes(t, 5)
	full, isErr, text := ticketGetNotes(t, tc, id, "2-3")
	if isErr {
		t.Fatalf("tool refused: %s", text)
	}
	for _, want := range []string{"Note number 2.", "Note number 3."} {
		if !strings.Contains(full.Notes, want) {
			t.Errorf("%s is missing from the range:\n%s", want, full.Notes)
		}
	}
	for _, unwanted := range []string{"Note number 1.", "Note number 4.", "Note number 5."} {
		if strings.Contains(full.Notes, unwanted) {
			t.Errorf("%s is outside the range and came back anyway:\n%s", unwanted, full.Notes)
		}
	}
	if !strings.Contains(full.Notes, "**agent:test**") {
		t.Errorf("the range dropped the stamp:\n%s", full.Notes)
	}

	// A single number is a range of one.
	full, isErr, text = ticketGetNotes(t, tc, id, "4")
	if isErr {
		t.Fatalf("tool refused: %s", text)
	}
	if !strings.Contains(full.Notes, "Note number 4.") || strings.Contains(full.Notes, "Note number 3.") {
		t.Errorf("notes=4 returned:\n%s", full.Notes)
	}
}

// A refusal names the range the reader can ask for, because "out of range"
// that does not say the range is a second question.
func TestTicketGetNotesRefusals(t *testing.T) {
	tc, id, _ := ticketWithNotes(t, 3)

	_, isErr, text := ticketGetNotes(t, tc, id, "9")
	if !isErr {
		t.Fatal("a note past the end did not refuse")
	}
	if !strings.Contains(text, "numbered 1-3") {
		t.Errorf("the refusal does not name the range: %q", text)
	}

	_, isErr, text = ticketGetNotes(t, tc, id, "banana")
	if !isErr {
		t.Fatal("a word that is not a range did not refuse")
	}
	if !strings.Contains(text, "range like 2-5") {
		t.Errorf("the refusal does not name the forms: %q", text)
	}
}

// Asking for a range of a ticket that has no notes says so, rather than
// returning an empty string that reads like a note with nothing in it.
func TestTicketGetNotesRangeWithNoNotes(t *testing.T) {
	tc := ticketToolStore(t, 1)
	list := &TicketListTool{TicketCore: tc}
	res, _ := list.Execute(context.Background(), nil, nil)
	id := ticketPageFrom(t, res).Tickets[0].ID

	_, isErr, text := ticketGetNotes(t, tc, id, "1")
	if !isErr {
		t.Fatal("a range against no notes did not refuse")
	}
	if !strings.Contains(text, "no notes") {
		t.Errorf("refusal: %q", text)
	}
}

// ticket.Entries is what splits the section. This is the guard against a second
// notes parser growing here: the entry count the renderer works from is the
// library's, and an entry it returns is written back unchanged.
func TestNotesRenderingMatchesEntries(t *testing.T) {
	_, _, raw := ticketWithNotes(t, 3)
	entries := ticket.Entries(raw)
	if len(entries) != 3 {
		t.Fatalf("Entries split the section into %d", len(entries))
	}
	if got := renderEntries(entries); got != strings.TrimSpace(raw) {
		t.Errorf("a round trip through Entries changed the text:\n got %q\nwant %q", got, strings.TrimSpace(raw))
	}
}
