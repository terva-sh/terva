// Package handlers turns the raw JSON tool arguments into store operations and
// the compact text the model sees in each tool result. It depends only on the
// task store (no terva SDK), so the wire parsing, the update pointer semantics
// (absent vs set-to-empty), and the result formatting are all unit-testable
// without spinning up the extension. The ext glue in app.go is a thin wrapper
// over these functions that adds panel side effects and the ToolResult type.
package handlers

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"terva.sh/terva/packages/agent/tools/tasks"
)

type createArgs struct {
	Tasks []struct {
		Title      string `json:"title"`
		ActiveForm string `json:"active_form"`
		Status     string `json:"status"`
		Note       string `json:"note"`
	} `json:"tasks"`
}

type updateArgs struct {
	ID           string  `json:"id"`
	Title        *string `json:"title"`
	ActiveForm   *string `json:"active_form"`
	Status       *string `json:"status"`
	Evidence     *string `json:"evidence"`
	Note         *string `json:"note"`
	ActivateNext string  `json:"activate_next"`
}

type listArgs struct {
	Archived   bool   `json:"archived"`
	Generation *int   `json:"generation"`
	Format     string `json:"format"`
}

type archiveArgs struct {
	KeepOpen bool   `json:"keep_open"`
	Label    string `json:"label"`
}

// List renders the current task list, the archive index (archived:true), or one
// archived generation (generation:N). It never mutates; the only error is a
// reference to a generation that doesn't exist.
func List(s *tasks.Store, raw json.RawMessage) (string, bool) {
	var in listArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return "invalid args: " + err.Error(), true
		}
	}
	md, ferr := wantMarkdown(in.Format)
	if ferr != "" {
		return ferr, true
	}
	// A specific archived generation is requested only when generation >= 1
	// (generations are numbered from 1). A model padding the call commonly emits
	// generation:0 — the JSON zero value — to mean "no particular generation";
	// treat 0/negative as unspecified and fall through to the current list (or the
	// index) rather than erroring it into a guessing loop.
	if in.Generation != nil && *in.Generation >= 1 {
		g, ok := s.Generation(*in.Generation)
		if !ok {
			return generationNotFoundMsg(s, *in.Generation), true
		}
		if md {
			return tasks.RenderGenerationMarkdown(g), false
		}
		return tasks.RenderGeneration(g), false
	}
	if in.Archived {
		if md {
			// The whole archive as one Markdown worklog — the T1 export.
			return tasks.RenderWorklogMarkdown(s.Generations()), false
		}
		return tasks.RenderArchiveIndex(s.Generations()), false
	}
	if md {
		return tasks.RenderListMarkdown(s.List()), false
	}
	return tasks.RenderCompact(s.List()), false
}

// wantMarkdown resolves the optional format arg. Empty and "text" keep the
// compact inline view; "markdown"/"md" switch to the checkbox worklog. An
// unrecognized value returns an actionable error string rather than silently
// falling back, so a typo is corrected instead of masked.
func wantMarkdown(format string) (md bool, errMsg string) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "text":
		return false, ""
	case "markdown", "md":
		return true, ""
	default:
		return false, fmt.Sprintf("unknown format %q — use \"text\" (default) or \"markdown\".", format)
	}
}

// generationNotFoundMsg builds an actionable error for a generation lookup that
// missed: it names the generations that DO exist so the model picks a real one
// instead of guessing.
func generationNotFoundMsg(s *tasks.Store, n int) string {
	gens := s.Generations()
	if len(gens) == 0 {
		return fmt.Sprintf("no archived generation %d — there are no archived lists yet. Call task_list with no arguments for the current list.", n)
	}
	seqs := make([]string, 0, len(gens))
	for _, g := range gens {
		seqs = append(seqs, strconv.Itoa(g.Seq))
	}
	return fmt.Sprintf("no archived generation %d. Available generation(s): %s — or call task_list with archived:true for the full index.", n, strings.Join(seqs, ", "))
}

// Archive parks the current list as a new generation and clears it (everything by
// default; only finished tasks when keep_open is set), returning the result text
// plus an isError flag. The default behavior — archiving open tasks too — is
// called out explicitly so the model isn't surprised into recreating work.
func Archive(s *tasks.Store, raw json.RawMessage) (string, bool) {
	var in archiveArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return "invalid args: " + err.Error(), true
		}
	}
	gen, dropped, ok, err := s.Archive(in.KeepOpen, in.Label)
	if err != nil {
		return err.Error(), true
	}
	if !ok {
		if in.KeepOpen {
			return "Nothing to archive: no done/cancelled tasks to roll off — the current list is unchanged.", false
		}
		return "Nothing to archive: the task list is already empty.", false
	}

	done, cancelled, open := 0, 0, 0
	for _, t := range gen.Tasks {
		switch t.Status {
		case tasks.StatusDone:
			done++
		case tasks.StatusCancelled:
			cancelled++
		default:
			open++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Archived generation %d", gen.Seq)
	if lbl := strings.TrimSpace(gen.Label); lbl != "" {
		fmt.Fprintf(&b, " (%s)", lbl)
	}
	fmt.Fprintf(&b, ": %d task(s) — %d done, %d cancelled, %d open.\n", len(gen.Tasks), done, cancelled, open)
	if in.KeepOpen {
		b.WriteString("Open tasks were kept in the current list.\n")
	} else {
		b.WriteString("The current list is now empty — ready for the next phase.\n")
		// Archiving the whole board files away every non-terminal task — pending,
		// active, AND blocked. Blocked counts here (unlike the closing-list warning's
		// OpenSummary): with no resume yet, a filed-away blocked task is the
		// unfinished work most likely to be lost, so name it so the model knows it
		// left the board.
		if n, names := tasks.UnfinishedSummary(gen.Tasks); n > 0 {
			label := strings.Join(names, ", ")
			if n > len(names) {
				label += fmt.Sprintf(", +%d more", n-len(names))
			}
			fmt.Fprintf(&b, "note: parked %d unfinished task(s) (%s) into gen %d (task_list generation:%d) — they're off the board now. Recreate any you still intend to do, or pass keep_open:true next time to keep unfinished (incl. blocked) tasks on the board.\n",
				n, label, gen.Seq, gen.Seq)
		}
	}
	if dropped > 0 {
		fmt.Fprintf(&b, "note: archive limit reached — dropped %d oldest generation(s).\n", dropped)
	}
	b.WriteString("\n")
	b.WriteString(tasks.RenderCompact(s.List()))
	return b.String(), false
}

// Create parses task_create args, creates the tasks, and returns the result
// text plus an isError flag.
func Create(s *tasks.Store, raw json.RawMessage) (string, bool) {
	var in createArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return "invalid args: " + err.Error(), true
	}
	if len(in.Tasks) == 0 {
		return "task_create requires at least one task in `tasks`", true
	}
	specs := make([]tasks.CreateSpec, 0, len(in.Tasks))
	for _, t := range in.Tasks {
		specs = append(specs, tasks.CreateSpec{
			Title:      t.Title,
			ActiveForm: t.ActiveForm,
			Status:     tasks.Status(strings.TrimSpace(t.Status)),
			Note:       t.Note,
		})
	}
	created, err := s.Create(specs)
	if err != nil {
		return err.Error(), true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Created %d task(s):\n", len(created))
	for _, t := range created {
		fmt.Fprintf(&b, "  %s  %s  %s\n", t.ID, t.Status, t.Title)
	}
	b.WriteString("\n")
	b.WriteString(tasks.RenderCompact(s.List()))
	return b.String(), false
}

// Update parses task_update args, applies the patch (enforcing the one-active
// invariant), and returns the result text plus an isError flag.
func Update(s *tasks.Store, raw json.RawMessage) (string, bool) {
	var in updateArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return "invalid args: " + err.Error(), true
	}
	patch := tasks.UpdatePatch{
		ID:         in.ID,
		Title:      in.Title,
		ActiveForm: in.ActiveForm,
		Evidence:   in.Evidence,
		Note:       in.Note,
	}
	if in.Status != nil {
		st := tasks.Status(strings.TrimSpace(*in.Status))
		patch.Status = &st
	}

	// activate_next steps away from this task and focuses the next one in a single
	// step. Empty is treated as absent (ignore zero-value padding). It is valid
	// only with a "stepping away" status — done, cancelled, or blocked — and is
	// validated BEFORE any mutation so a bad request changes nothing (no
	// half-applied transition).
	nextID := strings.TrimSpace(in.ActivateNext)
	if nextID != "" {
		// Every branch here rejects BEFORE any mutation, so each says "No state
		// changed." (kills the "did my close half-apply?" doubt) and, where it can,
		// names the corrected next action instead of only what was wrong (TW-013 F3).
		if patch.Status == nil || !isSteppingAway(*patch.Status) {
			return "activate_next only applies when you step away from this task — set status to \"done\", \"cancelled\", or \"blocked\" in the same call, or omit activate_next to keep this task active. No state changed.", true
		}
		if nextID == strings.TrimSpace(in.ID) {
			return fmt.Sprintf("activate_next cannot name the task you're closing (%s) — a task can't activate itself. To close it only, omit activate_next.%s No state changed.", nextID, suggestPending(s, nextID)), true
		}
		if !taskExists(s, nextID) {
			return fmt.Sprintf("activate_next: no task with id %q — call task_list for the current ids.%s No state changed.", nextID, suggestPending(s, "")), true
		}
	}

	updated, deactivated, err := s.Update(patch)
	if err != nil {
		return err.Error(), true
	}

	// With a validated activate_next, focus the next task in the same step. The
	// store enforces the one-active invariant, so this also returns any task it
	// demoted (other than the one we just completed).
	var nextActivated, nextDeactivated *tasks.Task
	if nextID != "" {
		active := tasks.StatusActive
		na, nd, nerr := s.Update(tasks.UpdatePatch{ID: nextID, Status: &active})
		if nerr != nil {
			return fmt.Sprintf("completed %s but could not activate %s: %v", updated.ID, nextID, nerr), true
		}
		nextActivated = &na
		if nd != nil && nd.ID != updated.ID {
			nextDeactivated = nd
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Updated %s → %s: %s\n", updated.ID, updated.Status, updated.Title)
	if deactivated != nil {
		fmt.Fprintf(&b, "Deactivated %s (was active): %s\n", deactivated.ID, deactivated.Title)
	}
	if nextActivated != nil {
		fmt.Fprintf(&b, "Activated %s (next): %s\n", nextActivated.ID, nextActivated.Title)
		if nextDeactivated != nil {
			fmt.Fprintf(&b, "Deactivated %s (was active): %s\n", nextDeactivated.ID, nextDeactivated.Title)
		}
	}
	// Soft evidence nudge: name the task and the status so the recommendation is
	// actionable, not generic. Stays a note (never isError) — evidence is
	// encouraged, not mechanically required.
	if (updated.Status == tasks.StatusDone || updated.Status == tasks.StatusBlocked) && strings.TrimSpace(updated.Evidence) == "" {
		fmt.Fprintf(&b, "note: %s marked %s without evidence — add a passing test command, an edited path, or a short reason so %q is checkable.\n",
			updated.ID, updated.Status, string(updated.Status))
	}
	// List once: reused for the closing-the-list warning and the inline render.
	list := s.List()
	// Soft closing-the-list warning: marking a task done/cancelled when real work
	// (pending/active) remains and nothing is currently active means the agent
	// closed its focus and left work behind. Observational — the transition still
	// applied. Blocked tasks are acknowledged parks (excluded by OpenSummary).
	if updated.Status == tasks.StatusDone || updated.Status == tasks.StatusCancelled {
		if open, active, names := tasks.OpenSummary(list); open > 0 && active == 0 {
			label := strings.Join(names, ", ")
			if open > len(names) {
				label += fmt.Sprintf(", +%d more", open-len(names))
			}
			fmt.Fprintf(&b, "note: %s %s, but %d task(s) still open (%s) and none active — mark the next one active or finish them before wrapping up.",
				updated.ID, updated.Status, open, label)
			// Teach the one-step path: a model that hits this can avoid the gap next
			// time by closing-and-focusing in a single call. This warning fires on
			// done and cancelled, and activate_next is valid for both.
			b.WriteString(" Tip: pass activate_next when you close a task to focus the next one in the same step.")
			b.WriteByte('\n')
		}
	}
	b.WriteString("\n")
	b.WriteString(tasks.RenderCompact(list))
	return b.String(), false
}

// taskExists reports whether a task with the given id is in the current list.
func taskExists(s *tasks.Store, id string) bool {
	for _, t := range s.List() {
		if t.ID == id {
			return true
		}
	}
	return false
}

// suggestPending names a pending task the model can activate instead — turning a
// rejected activate_next into an actionable next step. Skips exclude (the id
// that was rejected) and only offers a pending task (never the one being
// closed, which is active or terminal). Empty when the board has no other
// pending task to point at, so the caller's sentence stays clean.
func suggestPending(s *tasks.Store, exclude string) string {
	for _, t := range s.List() {
		if t.Status != tasks.StatusPending || t.ID == exclude {
			continue
		}
		label := strings.TrimSpace(t.Title)
		if label == "" {
			label = strings.TrimSpace(t.ActiveForm)
		}
		return fmt.Sprintf(" To continue, activate a pending task, e.g. %s %q.", t.ID, tasks.CleanOneLine(label, 60))
	}
	return ""
}

// isSteppingAway reports whether a status takes the current task out of active
// focus — done (finished), cancelled (abandoned), or blocked (parked) — which is
// exactly when activate_next ("close/park this, focus the next") makes sense.
func isSteppingAway(s tasks.Status) bool {
	return s == tasks.StatusDone || s == tasks.StatusCancelled || s == tasks.StatusBlocked
}
