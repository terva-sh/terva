package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// currentRevision reads the revision a write must pass as if_revision.
//
// Tests re-read it rather than reusing the one a claim returned, because
// closing a seeded task ticks a criterion, and that rewrites the ticket file.
// A revision held across a task close is stale by design.
func currentRevision(t *testing.T, tc *TicketCore, id string) string {
	t.Helper()
	s, err := tc.open()
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return tk.Revision
}

// ticketNotes returns the raw Notes section of a ticket.
func ticketNotes(t *testing.T, tc *TicketCore, id string) string {
	t.Helper()
	s, err := tc.open()
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return tk.Body.Notes
}

// runTransition moves a ticket and returns the parsed payload. It reads the
// current revision itself, so a test can move a ticket without tracking the
// rewrites a criterion tick causes.
func runTransition(t *testing.T, tc *TicketCore, board TaskBoard, id, status, reason string) ticketTransitionOut {
	t.Helper()
	args := map[string]any{"ref": id, "if_revision": currentRevision(t, tc, id), "status": status}
	if reason != "" {
		args["reason"] = reason
	}
	raw, _ := json.Marshal(args)
	res, err := (&TicketTransitionTool{TicketCore: tc}).WithTasks(board).Execute(context.Background(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("transition to %s refused: %s", status, ticketResultText(t, res))
	}
	var out ticketTransitionOut
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Closing a ticket lands the session's work for it as a note, with the evidence
// the tasks carried. That evidence is why the note is worth writing: it is the
// only place the ticket records how each criterion was satisfied.
func TestClosingATicketWritesTheWorklogNote(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "sess-worklog")
	bindChecker(board, tc)

	id, rev := readyTicket(t, tc, []string{"build the thing", "test the thing"})
	runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": rev})
	closeTask(t, board, taskForCriterion(t, board, 1).ID, "commit abc123")
	if _, _, _, err := board.Archive(false, "slice one"); err != nil {
		t.Fatal(err)
	}

	runTransition(t, tc, board, id, "in-progress", "")
	out := runTransition(t, tc, board, id, "done", "")

	if out.Worklog == nil || !out.Worklog.Noted {
		t.Fatalf("the transition did not report a worklog note: %+v", out.Worklog)
	}
	notes := ticketNotes(t, tc, id)
	if !strings.Contains(notes, "build the thing") {
		t.Errorf("the worklog note is missing the task title:\n%s", notes)
	}
	if !strings.Contains(notes, "commit abc123") {
		t.Errorf("the worklog note is missing the task evidence:\n%s", notes)
	}
	if !strings.Contains(notes, "slice one") {
		t.Errorf("the worklog note is missing the generation label:\n%s", notes)
	}
}

// A session can work several tickets. Closing one must never copy another one's
// work into a permanent record, so the filter runs per ticket and reaches
// inside a generation that holds both.
func TestTheWorklogNoteExcludesAnotherTicketsWork(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "sess-two-tickets")
	bindChecker(board, tc)

	idA, revA := readyTicket(t, tc, []string{"alpha work"})
	runClaim(t, tc, board, map[string]any{"ref": idA, "if_revision": revA})
	closeTask(t, board, taskForCriterion(t, board, 1).ID, "alpha evidence")
	if _, _, _, err := board.Archive(false, "alpha slice"); err != nil {
		t.Fatal(err)
	}

	idB, revB := readyTicket(t, tc, []string{"beta work"})
	runClaim(t, tc, board, map[string]any{"ref": idB, "if_revision": revB})

	runTransition(t, tc, board, idA, "in-progress", "")
	runTransition(t, tc, board, idA, "done", "")

	notes := ticketNotes(t, tc, idA)
	if !strings.Contains(notes, "alpha work") {
		t.Errorf("ticket A's note lost its own work:\n%s", notes)
	}
	if strings.Contains(notes, "beta work") {
		t.Errorf("ticket A's note recorded ticket B's work:\n%s", notes)
	}
}

// The live list is work the session never archived. It still happened, so it
// lands as a final section rather than vanishing at close.
func TestTheWorklogNoteIncludesUnarchivedTasks(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "sess-live")
	bindChecker(board, tc)

	id, rev := readyTicket(t, tc, []string{"unarchived work"})
	runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": rev})
	closeTask(t, board, taskForCriterion(t, board, 1).ID, "never archived")

	runTransition(t, tc, board, id, "in-progress", "")
	out := runTransition(t, tc, board, id, "done", "")

	if out.Worklog == nil || !out.Worklog.Noted {
		t.Fatalf("a live-only board wrote no note: %+v", out.Worklog)
	}
	if notes := ticketNotes(t, tc, id); !strings.Contains(notes, "unarchived work") {
		t.Errorf("the note dropped the unarchived task:\n%s", notes)
	}
}

// A park is when the record of what was tried is worth most, so blocked writes
// the worklog too.
func TestBlockingATicketAlsoRecordsTheWorklog(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "sess-blocked")
	bindChecker(board, tc)

	id, rev := readyTicket(t, tc, []string{"attempted work"})
	runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": rev})

	out := runTransition(t, tc, board, id, "blocked", "waiting on upstream")
	if out.Worklog == nil || !out.Worklog.Noted {
		t.Fatalf("blocking wrote no worklog note: %+v", out.Worklog)
	}
	if notes := ticketNotes(t, tc, id); !strings.Contains(notes, "attempted work") {
		t.Errorf("the blocked note is missing the work:\n%s", notes)
	}
}

// A move that is not a close leaves the notes alone. Writing a worklog on every
// status change would bury the ticket in duplicates of the same list.
func TestANonClosingTransitionWritesNoWorklog(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "sess-open")
	bindChecker(board, tc)

	id, rev := readyTicket(t, tc, []string{"ongoing work"})
	runClaim(t, tc, board, map[string]any{"ref": id, "if_revision": rev})

	out := runTransition(t, tc, board, id, "in-progress", "")
	if out.Worklog != nil {
		t.Errorf("moving to in-progress reported a worklog: %+v", out.Worklog)
	}
	if notes := ticketNotes(t, tc, id); strings.Contains(notes, "ongoing work") {
		t.Errorf("moving to in-progress wrote a worklog note:\n%s", notes)
	}
}

// Nothing to write is not a failure, but it is worth saying. An absent note has
// several causes and the model cannot tell them apart from silence.
func TestAnEmptyWorklogWritesNoNoteAndSaysWhy(t *testing.T) {
	tc := ticketCore(t)
	board := seedBoard(t, "sess-empty")
	bindChecker(board, tc)

	// No claim, so the board holds nothing for this ticket.
	id, _ := readyTicket(t, tc, []string{"never claimed"})
	runTransition(t, tc, board, id, "in-progress", "")
	out := runTransition(t, tc, board, id, "done", "")

	if out.Worklog == nil {
		t.Fatal("an empty worklog reported nothing at all")
	}
	if out.Worklog.Noted {
		t.Error("an empty worklog claimed it wrote a note")
	}
	if !strings.Contains(out.Worklog.Skipped, "no work for this ticket") {
		t.Errorf("the skip reason does not say why: %q", out.Worklog.Skipped)
	}
}

// A ticket note sits under the file's own "## Notes" heading, so a worklog
// rendered at H2 would open a sibling section and split the ticket in two.
func TestDemoteHeadingsKeepsTheWorklogInsideTheNote(t *testing.T) {
	got := demoteHeadings("## Tasks\n\n- [x] a thing\n\n## Generation 1\n")
	want := "### Tasks\n\n- [x] a thing\n\n### Generation 1\n"
	if got != want {
		t.Errorf("demoteHeadings\n got: %q\nwant: %q", got, want)
	}
	if strings.Contains(got, "\n## ") {
		t.Error("an H2 survived, which would open a sibling section in the ticket")
	}
}
