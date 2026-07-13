package tasks

import (
	"fmt"
	"sort"
	"strings"
)

// RenderCompact is the plain-text rendering returned inline in tool results, so
// the model always sees current state. Empty list => "No tasks."
func RenderCompact(tasks []Task) string {
	if len(tasks) == 0 {
		return "No tasks."
	}
	lines := make([]string, 0, len(tasks))
	for _, t := range tasks {
		lines = append(lines, compactLine(t))
	}
	return strings.Join(lines, "\n")
}

// RenderArchiveIndex renders the list of archived generations for
// task_list {archived:true}: one line each with seq, date, task count, how many
// are still open (non-terminal, matching the panel's notion of "open"), and an
// optional label. Newest last, as stored. Empty => a friendly note.
func RenderArchiveIndex(gens []Generation) string {
	if len(gens) == 0 {
		return "No archived task lists."
	}
	lines := make([]string, 0, len(gens))
	for _, g := range gens {
		open := 0
		for _, t := range g.Tasks {
			if !t.Status.IsTerminal() {
				open++
			}
		}
		line := fmt.Sprintf("gen %d  %s  %d task(s)", g.Seq, archiveDate(g.ArchivedAt), len(g.Tasks))
		if open > 0 {
			line += fmt.Sprintf(", %d open", open)
		}
		if lbl := CleanOneLine(g.Label, MaxLabelLen); lbl != "" {
			line += " — " + lbl
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// RenderGeneration renders one archived generation's tasks (read-only) for
// task_list {generation:N}, reusing the compact line format.
func RenderGeneration(g Generation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Archived gen %d (%s)", g.Seq, archiveDate(g.ArchivedAt))
	if lbl := CleanOneLine(g.Label, MaxLabelLen); lbl != "" {
		fmt.Fprintf(&b, " — %s", lbl)
	}
	b.WriteByte('\n')
	b.WriteString(RenderCompact(g.Tasks))
	return b.String()
}

// archiveDate shows the YYYY-MM-DD prefix of an RFC3339 timestamp, falling back
// to the whole string if it's shorter/unexpected.
func archiveDate(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

func compactLine(t Task) string {
	line := fmt.Sprintf("%s  %-8s %s", t.ID, t.Status, displayLabel(t))
	if ev := strings.TrimSpace(t.Evidence); ev != "" {
		line += " — " + CleanOneLine(ev, MaxEvidenceLen)
	} else if nt := strings.TrimSpace(t.Note); nt != "" {
		line += " — " + CleanOneLine(nt, MaxNoteLen)
	}
	return line
}

// displayLabel shows the present-continuous form for an active task, else the
// imperative title. It is sanitized to a single safe line so loaded or legacy
// data can't inject extra lines into tool output or the panel.
func displayLabel(t Task) string {
	label := t.Title
	if t.Status == StatusActive && strings.TrimSpace(t.ActiveForm) != "" {
		label = t.ActiveForm
	}
	return CleanOneLine(label, MaxTitleLen)
}

// PanelTitle builds the panel header. With exactly one active task it surfaces
// that task's active form; otherwise it shows the open count. A blocked count
// and an optional session title are appended.
func PanelTitle(tasks []Task, sessionTitle string) string {
	var active string
	activeCount, blocked, open := 0, 0, 0
	for _, t := range tasks {
		switch t.Status {
		case StatusActive:
			activeCount++
			active = displayLabel(t)
		case StatusBlocked:
			blocked++
		}
		if !t.Status.IsTerminal() {
			open++
		}
	}
	var title string
	if activeCount == 1 {
		title = "Tasks · " + active
	} else {
		title = fmt.Sprintf("Tasks (%d)", open)
	}
	if blocked > 0 {
		title += fmt.Sprintf(" · %d blocked", blocked)
	}
	if st := CleanOneLine(sessionTitle, MaxSessionTitle); st != "" {
		title += " — " + st
	}
	return title
}

func statusRank(s Status) int {
	switch s {
	case StatusBlocked:
		return 0
	case StatusActive:
		return 1
	default: // pending
		return 2
	}
}

// PanelLines renders the panel body. Open tasks are listed with blocked first;
// done/cancelled collapse to summary lines unless showDone is set.
func PanelLines(tasks []Task, showDone bool) []string {
	if len(tasks) == 0 {
		return []string{"  No tasks yet — the agent will populate this."}
	}
	var open, done, cancelled []Task
	for _, t := range tasks {
		switch t.Status {
		case StatusDone:
			done = append(done, t)
		case StatusCancelled:
			cancelled = append(cancelled, t)
		default:
			open = append(open, t)
		}
	}
	sort.SliceStable(open, func(i, j int) bool {
		return statusRank(open[i].Status) < statusRank(open[j].Status)
	})

	lines := make([]string, 0, len(open)+len(done)+len(cancelled)+2)
	for _, t := range open {
		lines = append(lines, "  "+panelRow(t))
	}
	if len(open) == 0 {
		lines = append(lines, "  All tasks complete.")
	}
	if showDone {
		for _, t := range done {
			lines = append(lines, "  "+panelRow(t))
		}
		for _, t := range cancelled {
			lines = append(lines, "  "+panelRow(t))
		}
		return lines
	}
	if len(done) > 0 {
		lines = append(lines, fmt.Sprintf("  done (%d)", len(done)))
	}
	if len(cancelled) > 0 {
		lines = append(lines, fmt.Sprintf("  cancelled (%d)", len(cancelled)))
	}
	return lines
}

func panelRow(t Task) string {
	row := fmt.Sprintf("%-9s %s", string(t.Status), displayLabel(t))
	if ev := strings.TrimSpace(t.Evidence); ev != "" {
		row += " — " + CleanOneLine(ev, MaxEvidenceLen)
	}
	return row
}

// PanelFooter is the static key hint line.
func PanelFooter() string {
	return "d expand/collapse done · r refresh · esc close"
}

// Bounds for the model-facing context card. The host caps a card at 4 KiB and
// truncates over-budget content; we stay well under that and cap the number of
// open lines so a large list can't flood every turn's context.
const (
	cardMaxOpenLines = 20
	cardMaxBytes     = 3500
	cardReasonLen    = 120
)

// RenderCard is the compact, bounded text injected into the model's context
// every turn as the live task list. Open tasks (blocked → active → pending) are
// listed; done/cancelled collapse to counts; blocked tasks carry a short reason.
// Empty list => "" (the caller clears the card).
func RenderCard(tasks []Task) string {
	if len(tasks) == 0 {
		return ""
	}
	var open []Task
	done, cancelled := 0, 0
	for _, t := range tasks {
		switch t.Status {
		case StatusDone:
			done++
		case StatusCancelled:
			cancelled++
		default:
			open = append(open, t)
		}
	}
	sort.SliceStable(open, func(i, j int) bool {
		return statusRank(open[i].Status) < statusRank(open[j].Status)
	})

	var b strings.Builder
	more := 0
	if len(open) > cardMaxOpenLines {
		more = len(open) - cardMaxOpenLines
		open = open[:cardMaxOpenLines]
	}
	for _, t := range open {
		b.WriteString(cardRow(t))
		b.WriteByte('\n')
	}
	if more > 0 {
		fmt.Fprintf(&b, "…and %d more open\n", more)
	}
	if done > 0 {
		fmt.Fprintf(&b, "done (%d)\n", done)
	}
	if cancelled > 0 {
		fmt.Fprintf(&b, "cancelled (%d)\n", cancelled)
	}
	out := strings.TrimRight(b.String(), "\n")
	if len(out) > cardMaxBytes {
		out = strings.ToValidUTF8(out[:cardMaxBytes], "") + "\n…(truncated)"
	}
	return out
}

func cardRow(t Task) string {
	row := fmt.Sprintf("%-8s %s", string(t.Status), displayLabel(t))
	if t.Status == StatusBlocked {
		if r := strings.TrimSpace(t.Evidence); r != "" {
			row += " — " + CleanOneLine(r, cardReasonLen)
		}
	}
	return row
}

// AnyOpen reports whether any task is genuine open work (pending or active),
// used to mark the card Blocking so the host nudges "review before done". A
// blocked task is an explicit, acknowledged park and does not count.
func AnyOpen(tasks []Task) bool {
	for _, t := range tasks {
		if t.Status == StatusPending || t.Status == StatusActive {
			return true
		}
	}
	return false
}

// openSummaryMaxNames bounds how many open-task labels the closing-the-list
// warning lists inline, so a large board can't flood a single result line.
const openSummaryMaxNames = 5

// OpenSummary classifies a list for the closing-the-list warning
// (handlers.Update). It returns the count of genuinely open tasks (pending or
// active, matching AnyOpen), how many of those are active, and a short bounded
// slice of "id status" labels for the warning text. Blocked tasks are
// acknowledged parks and are excluded, consistent with AnyOpen.
func OpenSummary(tasks []Task) (open, active int, names []string) {
	for _, t := range tasks {
		if t.Status != StatusPending && t.Status != StatusActive {
			continue
		}
		open++
		if t.Status == StatusActive {
			active++
		}
		if len(names) < openSummaryMaxNames {
			names = append(names, t.ID+" "+string(t.Status))
		}
	}
	return open, active, names
}

// UnfinishedSummary classifies a list for the archive warning: it counts every
// non-terminal task (pending, active, OR blocked) and returns a short bounded
// slice of "id status" labels. Unlike OpenSummary — which powers the
// closing-the-list warning and excludes blocked as an acknowledged park —
// archiving removes a task from the board entirely, and with no resume yet a
// filed-away blocked task is exactly the unfinished work most likely to be lost,
// so it counts here.
func UnfinishedSummary(tasks []Task) (count int, names []string) {
	for _, t := range tasks {
		if t.Status.IsTerminal() {
			continue
		}
		count++
		if len(names) < openSummaryMaxNames {
			names = append(names, t.ID+" "+string(t.Status))
		}
	}
	return count, names
}

// StatusGlance is the short TUI status-line segment (not model-facing): the
// active task and a done/total count. Empty when there's nothing to show.
func StatusGlance(tasks []Task) string {
	if len(tasks) == 0 {
		return ""
	}
	var active string
	done, total := 0, 0
	for _, t := range tasks {
		if t.Status == StatusCancelled {
			continue
		}
		total++
		if t.Status == StatusDone {
			done++
		}
		if t.Status == StatusActive && active == "" {
			active = displayLabel(t)
		}
	}
	if total == 0 {
		return ""
	}
	if active != "" {
		return fmt.Sprintf("▸ %s (%d/%d)", CleanOneLine(active, 60), done, total)
	}
	return fmt.Sprintf("tasks %d/%d", done, total)
}
