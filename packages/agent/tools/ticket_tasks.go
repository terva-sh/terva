package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/agent/tools/tasks"
	"terva.sh/terva/packages/core"
)

// TaskBoard is the slice of the session task board that the ticket tools use.
// *tasks.Store satisfies it. The interface is here rather than a concrete
// pointer so this package does not depend on tasktool, and so a test can seed
// against a board it fully controls.
type TaskBoard interface {
	Create(specs []tasks.CreateSpec) ([]tasks.Task, error)
	List() []tasks.Task
	// SessionID is what a claim records so a ticket indexes into transcript
	// history. The board is the only session-scoped thing the ticket tools hold,
	// and it learns the id from Rebind after the tools are built, so this is read
	// at claim time and never cached.
	SessionID() string
	// Generations is the archived worklog a closing ticket records as a note.
	// Only the archived generations: the live list arrives through List, and the
	// two are rendered as separate sections.
	Generations() []tasks.Generation
	// Archive rolls tasks off the live list into a generation. A claim always
	// passes keepOpen true, so this can never move work that somebody left
	// open. It reports ok false when it found nothing to archive.
	Archive(keepOpen bool, label string) (tasks.Generation, int, bool, error)
}

// TaskBinder is implemented by a ticket tool that writes the session task
// board. The build layer calls WithTasks whenever the board changes identity,
// which happens twice: on a tool rebuild, where UseTasks carries the board
// across, and in bot mode, where each admitted group gets its own board.
//
// WithTasks returns a NEW tool rather than mutating the receiver, and that is
// the whole point. The per-group registry is a shallow copy of the shared one,
// so a tool that rebound itself in place would rebind the owner's copy too, and
// a claim made in a group chat would seed tasks onto the owner's private
// session board.
type TaskBinder interface {
	WithTasks(board TaskBoard) core.Tool
}

// WithTasks binds this claim tool to a task board, returning a copy.
func (t *TicketClaimTool) WithTasks(board TaskBoard) core.Tool {
	c := *t
	c.Tasks = board
	return &c
}

// sessionID is the terva session this claim belongs to, or empty when the
// session has no task board to read it from. Empty is correct rather than
// invented: git-ticket records the field only when a harness supplies it, and a
// made-up id would point at a transcript that does not exist.
func (t *TicketClaimTool) sessionID() string {
	if t.Tasks == nil {
		return ""
	}
	return t.Tasks.SessionID()
}

// seedReport says what a claim did to the task board. Created counts the tasks
// it made; Skipped names the reason it made none. Exactly one of them carries
// information, and the model needs the reason as much as the count, because
// "no tasks appeared" has several causes and only one of them is a problem.
type seedReport struct {
	Created int      `json:"created,omitempty"`
	TaskIDs []string `json:"task_ids,omitempty"`
	Skipped string   `json:"skipped,omitempty"`
	// Archived counts the finished tasks the claim rolled off the board before
	// it seeded. The claim does that on the caller's behalf, and a silent
	// archive is the objectionable one, so the result says what it did.
	Archived int `json:"archived,omitempty"`
}

// seedFromCriteria makes one task per unchecked acceptance criterion.
//
// Only the unchecked ones: a claim on a part-finished ticket should seed the
// work that is left, and a checked criterion seeded as a pending task would
// invite the model to redo it. Criterion holds the 1-based index into the FULL
// list, checked items included, because that is the positional index
// ticket.SetChecklistItem addresses. Deriving it from the filtered slice would
// check the wrong box, and would do it silently.
func seedFromCriteria(board TaskBoard, tk *ticket.Ticket) *seedReport {
	if board == nil {
		// No task tools in this session (chat/play/--no-tools, or a --tools
		// allowlist that dropped them). Not a failure, and not worth a line.
		return nil
	}
	items := ticket.Checklist(tk.Body.AcceptanceCriteria)
	if len(items) == 0 {
		return &seedReport{Skipped: "the ticket has no acceptance criteria to seed from"}
	}
	// Seeding onto a board that still holds open work would mix two tickets'
	// tasks with no way to tell them apart afterwards. Refusing is recoverable
	// and mixing is not, so this stops and says which board is in the way.
	if open := openTitles(board.List()); len(open) > 0 {
		return &seedReport{Skipped: fmt.Sprintf(
			"the task list already holds %d open task(s) (%s). Archive with task_archive first, then claim again to seed.",
			len(open), strings.Join(open, ", "))}
	}
	specs := make([]tasks.CreateSpec, 0, len(items))
	for i, it := range items {
		if it.Checked {
			continue
		}
		title := strings.TrimSpace(it.Text)
		if title == "" {
			continue
		}
		specs = append(specs, tasks.CreateSpec{
			Title:     title,
			Ticket:    tk.ID,
			Criterion: i + 1,
		})
	}
	if len(specs) == 0 {
		return &seedReport{Skipped: "every acceptance criterion is already checked"}
	}
	// This claim will seed. Anything still on the board is finished business
	// from an earlier ticket, because the guard above refused every open task.
	// Roll it off, or this ticket's tasks land beside it and every later turn
	// renders both lists.
	//
	// The position is load-bearing twice over. It sits after the open-task
	// refusal, so a claim never archives the caller's board and then refuses.
	// It sits after the empty-specs return for the same reason, so a claim never
	// archives and then declines to seed.
	//
	// keepOpen true does the same work as false at this line, because no open
	// task survived the guard. True is the safer way to write it: it cannot move
	// open work, whatever that guard becomes later.
	gen, _, rolled, err := board.Archive(true, "before "+tk.ID)
	if err != nil {
		return &seedReport{Skipped: "could not archive the finished tasks: " + err.Error()}
	}
	archived := 0
	if rolled {
		archived = len(gen.Tasks)
	}
	made, err := board.Create(specs)
	if err != nil {
		return &seedReport{Skipped: "could not seed the task list: " + err.Error()}
	}
	rep := &seedReport{Created: len(made), Archived: archived}
	for _, t := range made {
		rep.TaskIDs = append(rep.TaskIDs, t.ID)
	}
	return rep
}

// seedOptOut is the report for a claim the caller told not to seed. It names
// what the argument gave up, because that cost is otherwise invisible: no task
// carries a criterion of this ticket, so no close will ever check one.
//
// Silence here is what sent a session to `edit`. Over 126 ticket calls it
// passed seed_tasks false four times, and it hand-edited the criteria of every
// one of those tickets. Three of those edits checked every box in the file at
// once, with no revision precondition and no actor.
//
// A boardless session still says nothing, exactly as seedFromCriteria does.
// There is no task tool to reach for, so a reason that names one would be
// advice the model cannot take.
func seedOptOut(board TaskBoard) *seedReport {
	if board == nil {
		return nil
	}
	return &seedReport{Skipped: "you passed seed_tasks false, so this claim made no task. A task that you close checks the criterion it came from. Claim again without the argument to seed the board."}
}

// openTitles names the tasks that are neither done nor cancelled, capped so a
// refusal that lists them stays readable.
func openTitles(list []tasks.Task) []string {
	const max = 3
	var out []string
	n := 0
	for _, t := range list {
		if t.Status.IsTerminal() {
			continue
		}
		n++
		if len(out) < max {
			out = append(out, t.Title)
		}
	}
	if n > len(out) {
		out = append(out, fmt.Sprintf("and %d more", n-len(out)))
	}
	return out
}

// WithTasks binds this transition tool to a task board, returning a copy. The
// copy matters for the same reason it does on the claim tool: the per-group
// registry is a shallow copy of the shared one.
func (t *TicketTransitionTool) WithTasks(board TaskBoard) core.Tool {
	c := *t
	c.Tasks = board
	return &c
}

// --- the criterion half of the bridge ---------------------------------------

// ticketCoreCarrier is satisfied by every ticket tool, because each one embeds
// *TicketCore and the method promotes. The method is unexported, so nothing
// outside this package can claim to be a ticket tool.
type ticketCoreCarrier interface {
	ticketCore() *TicketCore
}

func (c *TicketCore) ticketCore() *TicketCore { return c }

// CriterionCheckerFor returns a checker backed by whatever ticket tools reg
// holds, or nil when it holds none. The build layer hands the result to the
// task store, which calls it when a task that carries a criterion closes.
//
// Any ticket tool will do, because they all share one *TicketCore.
func CriterionCheckerFor(reg core.Registry) tasks.CriterionChecker {
	if reg == nil {
		return nil
	}
	for _, t := range reg {
		if h, ok := t.(ticketCoreCarrier); ok {
			if tc := h.ticketCore(); tc != nil {
				return &ticketCriterionChecker{core: tc}
			}
		}
	}
	return nil
}

type ticketCriterionChecker struct{ core *TicketCore }

// CheckCriterion reads the ticket for its current revision and then ticks the
// box. It reads rather than taking a revision from the caller because no model
// supplied one: the task board asks for this, not a tool call.
func (c *ticketCriterionChecker) CheckCriterion(ref string, index int) error {
	// handlers.Update carries no context, and threading one through the task
	// store to reach a local file write would change every caller for nothing.
	ctx := context.Background()
	s, err := c.core.open()
	if err != nil {
		return err
	}
	cur, err := s.Get(ctx, ref)
	if err != nil {
		return err
	}
	_, err = s.Apply(ctx, ref, ticket.SetChecklistItem{
		Section: ticket.AcceptanceCriteria,
		Index:   index,
		Checked: true,
	}, ticket.ApplyOptions{IfRevision: cur.Revision, Actor: c.core.actor()})
	return err
}

// --- the worklog half of the bridge -----------------------------------------

// closesTicket says whether a transition to this status should record the
// worklog. The user's ruling is every terminal status and blocked as well: a
// ticket parked mid-flight is exactly when the record of what was tried is
// worth most.
func closesTicket(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "archived", "blocked":
		return true
	}
	return false
}

// worklogFor renders this ticket's share of the session task board as Markdown
// for a note, and returns the reason instead when there is nothing to write.
//
// The filter is per ticket, not per session. A session can work several
// tickets, and closing one of them must not copy another one's work into a
// permanent record. Generations are filtered down to their matching tasks and
// then dropped when nothing survives, so a mixed generation contributes only
// the part that belongs here.
func worklogFor(board TaskBoard, ticketID string) (body, skipped string) {
	if board == nil {
		// No task tools in this session. Not a failure, and not worth a line.
		return "", ""
	}
	gens := generationsForTicket(board.Generations(), ticketID)
	live := tasksForTicket(board.List(), ticketID)
	if len(gens) == 0 && len(live) == 0 {
		return "", "the task board holds no work for this ticket, so no worklog note was written"
	}
	var b strings.Builder
	b.WriteString("Task worklog for this ticket, from the session task board.\n")
	for _, g := range gens {
		b.WriteString("\n")
		b.WriteString(demoteHeadings(tasks.RenderGenerationMarkdown(g)))
	}
	if len(live) > 0 {
		// The live list is work the session never archived. It still happened, so
		// it lands as a final section rather than vanishing at close.
		b.WriteString("\n")
		b.WriteString(demoteHeadings(tasks.RenderListMarkdown(live)))
	}
	return b.String(), ""
}

func tasksForTicket(list []tasks.Task, ticketID string) []tasks.Task {
	var out []tasks.Task
	for _, t := range list {
		if t.Ticket == ticketID {
			out = append(out, t)
		}
	}
	return out
}

func generationsForTicket(gens []tasks.Generation, ticketID string) []tasks.Generation {
	var out []tasks.Generation
	for _, g := range gens {
		kept := tasksForTicket(g.Tasks, ticketID)
		if len(kept) == 0 {
			continue
		}
		g.Tasks = kept
		out = append(out, g)
	}
	return out
}

// demoteHeadings pushes every Markdown heading down one level. The task
// renderers emit H2, which is the right level for a task_list result, but a
// ticket note sits under the file's own "## Notes" heading and an H2 there
// would open a sibling section instead of staying inside the note.
//
// A line scan is enough because every display field the renderers write passes
// through tasks.CleanOneLine, which strips newlines. No task title or piece of
// evidence can smuggle in a fenced block whose content this would corrupt.
func demoteHeadings(md string) string {
	lines := strings.Split(md, "\n")
	for i, ln := range lines {
		if strings.HasPrefix(ln, "#") {
			lines[i] = "#" + ln
		}
	}
	return strings.Join(lines, "\n")
}
