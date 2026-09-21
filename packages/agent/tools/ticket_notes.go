package tools

// Reading a ticket should not cost its whole history.
//
// ticket_get returned the entire Notes section on every read, so an agent that
// asked what a ticket is about paid for everything anybody had ever written on
// it. Measured over this store on 2026-09-15, before the change: 56,214 words
// of notes across 95 tickets, of which about 38,224 sit in older notes that a
// reading of the current state does not need. The worst single ticket returned
// 5,578 words from one call.
//
// git-ticket fixed the same defect on its own read path in v0.18.0, and the
// primitive it exported there, ticket.Entries, is what this uses. There is no
// second notes parser in terva: the section is the document and Entries is a
// reading of it, so a reading here and the file cannot disagree.
//
// This is shape and nothing else. No file changes, and nothing is deleted. The
// whole section stays one argument away.

import (
	"fmt"
	"strconv"
	"strings"

	ticket "github.com/terva-sh/git-ticket/ticket"
)

// notesShownInFull is how many of the newest notes ticket_get returns whole.
//
// One, and a count rather than a size threshold, for the reason upstream gives:
// a threshold makes the same ticket read differently in two stores and leaves a
// reader nothing to predict from. The newest note is nearly always the live one,
// and every older note is one argument away.
const notesShownInFull = 1

// renderNotes is the Notes section that ticket_get returns, under the value of
// the tool's notes argument.
//
// An empty argument elides, which is the default because most reads want the
// current state. "all" is the whole section byte for byte, which is what the
// tool returned before this existed. "list" is an index. Anything else is a note
// number, or a contiguous range of them.
func renderNotes(text, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return compactNotes(text), nil
	}
	if strings.EqualFold(arg, "all") {
		return text, nil
	}
	entries := ticket.Entries(text)
	if len(entries) == 0 {
		return "", fmt.Errorf("this ticket has no notes: give notes=all, or leave notes empty")
	}
	if strings.EqualFold(arg, "list") {
		return noteIndex(entries), nil
	}
	from, to, err := parseNoteRange(arg, len(entries))
	if err != nil {
		return "", err
	}
	return renderEntries(entries[from-1 : to]), nil
}

// compactNotes renders the newest note in full, and one line standing in for
// the rest.
//
// A ticket with one note returns exactly what it returned before. That is the
// case worth protecting: most tickets have one note, and making them cost a
// second call would trade a real saving on the worst tickets for a tax on
// everything else.
func compactNotes(text string) string {
	entries := ticket.Entries(text)
	if len(entries) <= notesShownInFull {
		return text
	}
	hidden := entries[:len(entries)-notesShownInFull]
	shown := entries[len(entries)-notesShownInFull:]
	return notesElidedLine(hidden) + "\n\n" + renderEntries(shown)
}

// notesElidedLine stands in for the notes that ticket_get did not return.
//
// It carries the count, the numbers, and the argument that gets them back,
// because a reader who has to work out how to retrieve something has been handed
// a puzzle rather than a summary. The range is contiguous and starts at one, so
// naming its bounds says everything a list of numbers would.
//
// It names no ticket id, where git-ticket's own line does. The id is a field of
// the same payload, so a reader holding this line is holding that too, and the
// CLI has no such envelope to lean on.
func notesElidedLine(hidden []ticket.Entry) string {
	span := strconv.Itoa(hidden[0].Index)
	if len(hidden) > 1 {
		span = fmt.Sprintf("%d-%d", hidden[0].Index, hidden[len(hidden)-1].Index)
	}
	noun := "notes"
	if len(hidden) == 1 {
		noun = "note"
	}
	return fmt.Sprintf("_%d earlier %s, %s hidden. Read them with notes=%q on ticket_get, or notes=\"list\" for an index._",
		len(hidden), noun, span, span)
}

// noteIndex is one line per note: the number, the instant, the actor, and the
// opening line. It is what makes a range aimable, since a reader cannot ask for
// note 3 without a way to see which one that is.
func noteIndex(entries []ticket.Entry) string {
	var b strings.Builder
	for _, e := range entries {
		actor := e.Actor
		if actor == "" {
			actor = "unattributed"
		}
		at := e.At
		if at == "" {
			at = "no instant"
		}
		fmt.Fprintf(&b, "%3d  %s  %s\n     %s\n", e.Index, at, actor, noteOpeningLine(e.Text))
	}
	return strings.TrimRight(b.String(), "\n")
}

// noteOpeningLine is the first line of an entry, for the index. A long one is
// cut, because the index earns its keep by being scannable.
func noteOpeningLine(text string) string {
	line := text
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	const width = 72
	if len([]rune(line)) > width {
		return string([]rune(line)[:width-1]) + "…"
	}
	if line == "" {
		return "(empty)"
	}
	return line
}

// renderEntries writes entries back in the shape the file carries them, so a
// note returned by ticket_get and the same note in the Markdown read
// identically.
func renderEntries(entries []ticket.Entry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, renderNoteEntry(e))
	}
	return strings.Join(parts, "\n\n")
}

func renderNoteEntry(e ticket.Entry) string {
	if e.Actor == "" && e.At == "" {
		return e.Text
	}
	return fmt.Sprintf("**%s** at %s\n\n%s", e.Actor, e.At, e.Text)
}

// parseNoteRange reads "3" or "2-5" against the count of notes the ticket has.
//
// Both bounds are reported against a count the reader can see, because "out of
// range" that does not say the range is a second question.
func parseNoteRange(arg string, count int) (int, int, error) {
	lo, hi, found := strings.Cut(arg, "-")
	from, err := strconv.Atoi(strings.TrimSpace(lo))
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not a note number, a range like 2-5, all, or list", arg)
	}
	to := from
	if found {
		to, err = strconv.Atoi(strings.TrimSpace(hi))
		if err != nil {
			return 0, 0, fmt.Errorf("%q is not a note number, a range like 2-5, all, or list", arg)
		}
	}
	if from < 1 || to > count || from > to {
		noun := "notes"
		if count == 1 {
			noun = "note"
		}
		return 0, 0, fmt.Errorf("%s is not a range of this ticket's %d %s; they are numbered 1-%d",
			arg, count, noun, count)
	}
	return from, to, nil
}
