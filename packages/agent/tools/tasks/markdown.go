package tasks

import (
	"fmt"
	"strings"
)

// Markdown renderers turn task lists and archived generations into a portable,
// human-readable worklog: a checkbox per task, evidence inline. Archived
// generations are the per-slice worklog this exports — the "task archive =
// canonical worklog" story (retro P1), now a first-class artifact since tasks
// are in core. Every function here is pure (data in, string out); all display
// fields pass through CleanOneLine, which strips newlines, so a title or piece
// of evidence can never inject a stray heading or list item.

// RenderWorklogMarkdown renders every archived generation as one Markdown
// document: an H1 title, then an H2 section per generation (oldest first, as
// stored). This is the full worklog export (task_list {archived:true,
// format:"markdown"}).
func RenderWorklogMarkdown(gens []Generation) string {
	var b strings.Builder
	b.WriteString("# Task worklog\n\n")
	if len(gens) == 0 {
		b.WriteString("_No archived task lists yet._\n")
		return b.String()
	}
	for i, g := range gens {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(RenderGenerationMarkdown(g))
	}
	return b.String()
}

// RenderGenerationMarkdown renders one archived generation as an H2 section: the
// heading (seq, date, optional label) then a checkbox line per task.
func RenderGenerationMarkdown(g Generation) string {
	var b strings.Builder
	b.WriteString("## " + generationHeading(g) + "\n\n")
	writeTaskChecklist(&b, g.Tasks)
	return b.String()
}

// RenderListMarkdown renders the current (live) list as an H2 checkbox section,
// so task_list {format:"markdown"} yields a worklog fragment for the in-progress
// slice too, not only archived ones.
func RenderListMarkdown(tasks []Task) string {
	var b strings.Builder
	b.WriteString("## Tasks\n\n")
	writeTaskChecklist(&b, tasks)
	return b.String()
}

func writeTaskChecklist(b *strings.Builder, tasks []Task) {
	if len(tasks) == 0 {
		b.WriteString("_No tasks._\n")
		return
	}
	for _, t := range tasks {
		b.WriteString(markdownTaskLine(t))
		b.WriteByte('\n')
	}
}

func generationHeading(g Generation) string {
	h := fmt.Sprintf("Generation %d — %s", g.Seq, archiveDate(g.ArchivedAt))
	if lbl := CleanOneLine(g.Label, MaxLabelLen); lbl != "" {
		h += " — " + lbl
	}
	return h
}

// markdownTaskLine is one GitHub-style checklist item: [x] for done, [ ]
// otherwise; a status tag for the non-obvious states (active/blocked/cancelled);
// a struck-through title for cancelled work; and the task's evidence (or its
// note) inline so the line is self-contained.
func markdownTaskLine(t Task) string {
	box := "[ ]"
	if t.Status == StatusDone {
		box = "[x]"
	}
	title := CleanOneLine(t.Title, MaxTitleLen)
	if t.Status == StatusCancelled && title != "" {
		title = "~~" + title + "~~"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "- %s %s", box, t.ID)
	// The empty box already reads as "pending"; done reads from [x]. Tag only the
	// states a reader can't infer from the checkbox.
	if t.Status != StatusDone && t.Status != StatusPending {
		fmt.Fprintf(&b, " (%s)", t.Status)
	}
	if title != "" {
		b.WriteByte(' ')
		b.WriteString(title)
	}
	if ev := strings.TrimSpace(t.Evidence); ev != "" {
		b.WriteString(" — " + CleanOneLine(ev, MaxEvidenceLen))
	} else if nt := strings.TrimSpace(t.Note); nt != "" {
		b.WriteString(" — " + CleanOneLine(nt, MaxNoteLen))
	}
	return b.String()
}
