package tools

// The ticket_* write tools: slice 3 of docs/plans/git-ticket.md. Five
// workspace-mutation tools over the same TicketCore the read tools share.
// Every mutation except create requires if_revision, which the library
// leaves optional: agents read before they write, and multi-agent is where
// the races happen. A stale precondition comes back as a model-readable
// refusal that names the current revision, so the recovery is one
// ticket_get away.
//
// The actor is terva's, injected at registry build, and no schema offers
// it: a model does not choose who it is. The store records every change
// under that identity in updated_by and in Notes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

func (c *TicketCore) actor() ticket.Actor {
	return ticket.Actor{ID: c.ActorID, Name: c.ActorName}
}

// ticketWriteOut is what every write returns: the fresh row and the new
// revision, read back after the write so the model can chain the next
// mutation without a separate ticket_get.
type ticketWriteOut struct {
	ticketRow
	References []ticketReference `json:"references,omitempty"`
	Revision   string            `json:"revision,omitempty"`
	Path       string            `json:"path,omitempty"`
}

// freshRow is freshOut's body, split out for the caller that wraps the row in a
// larger payload. ticket_transition adds what it did to the worklog.
func (c *TicketCore) freshRow(ctx context.Context, s *ticket.Store, ref string) (ticketWriteOut, error) {
	tk, err := s.Get(ctx, ref)
	if err != nil {
		return ticketWriteOut{}, err
	}
	return ticketWriteOut{
		ticketRow:  rowFromTicket(tk),
		References: c.ticketReferences(tk.References),
		Revision:   tk.Revision,
		Path:       tk.Path,
	}, nil
}

func (c *TicketCore) freshOut(ctx context.Context, s *ticket.Store, ref string) (core.ToolResult, error) {
	row, err := c.freshRow(ctx, s, ref)
	if err != nil {
		return ticketResult(nil, err)
	}
	return ticketResult(row, nil)
}

// applyWrite runs one mutation under the required precondition. On a stale
// revision it re-reads and names the current one, because the library's
// error carries the values in Details and the model needs them in prose.
func (c *TicketCore) applyWrite(ctx context.Context, ref, ifRevision string, m ticket.Mutation) (core.ToolResult, error) {
	s, id, err := c.applyMutation(ctx, ref, ifRevision, m)
	if err != nil {
		return ticketResult(nil, err)
	}
	return c.freshOut(ctx, s, id)
}

// applyMutation is applyWrite's body, split out for the one caller that needs
// more than the standard row back. ticket_claim reads the ticket itself, so it
// can seed the task board from the acceptance criteria it just claimed.
func (c *TicketCore) applyMutation(ctx context.Context, ref, ifRevision string, m ticket.Mutation) (*ticket.Store, string, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, "", fmt.Errorf("give ref: a ticket id, or a unique short form of it")
	}
	if strings.TrimSpace(ifRevision) == "" {
		return nil, "", fmt.Errorf("give if_revision: the revision from your last write to this ticket, or from ticket_get")
	}
	s, err := c.open()
	if err != nil {
		return nil, "", err
	}
	res, err := s.Apply(ctx, ref, m, ticket.ApplyOptions{IfRevision: ifRevision, Actor: c.actor()})
	if err != nil {
		var te *ticket.Error
		if errors.As(err, &te) && te.Code == ticket.CodeStaleRevision {
			if cur, gerr := s.Get(ctx, ref); gerr == nil {
				return nil, "", fmt.Errorf("%s. The current revision is %s. Retry with that value. Read the ticket with ticket_get when you need to see the change.", err.Error(), cur.Revision)
			}
		}
		return nil, "", err
	}
	// This covers ticket_update, ticket_transition, and ticket_claim, which all
	// reach the store through here.
	c.Card.Invalidate()
	return s, res.Ticket.ID, nil
}

// ticketRefArg is one reference in the write schemas: the namespaced ref,
// and the file it points at. The path is what git ticket files <path>
// searches, so a reference without one is half a reference.
type ticketRefArg struct {
	Ref  string `json:"ref"`
	Path string `json:"path"`
}

func referenceMutations(rs []ticketRefArg) ticket.Mutations {
	var ms ticket.Mutations
	for _, r := range rs {
		m := ticket.AddReference{Ref: r.Ref}
		if r.Path != "" {
			p := r.Path
			m.Path = &p
		}
		ms = append(ms, m)
	}
	return ms
}

const ticketReferencesDesc = "The references to record. Each entry gives a ref, and an optional path. A ref carries a namespace, for example plan:forgejo-workflow. The path names the file, and the command git ticket files <path> then finds this ticket."

func ticketReferenceItemsSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ref":  map[string]any{"type": "string", "description": "The reference with its namespace, for example plan:release-process."},
			"path": map[string]any{"type": "string", "description": "The file the reference points at, relative to the repository root."},
		},
		"required": []string{"ref"},
	}
}

// ticketTitleLimitDesc carries the two numbers the store enforces. A title
// past the warning is a check finding, and CI runs check in strict mode, so
// a warning here becomes a red gate later. TestTicketTitleLimitsAreCurrent
// holds these numbers against the library constants.
const ticketTitleLimitDesc = "A title over 72 characters is a warning from ticket_check, and a title over 120 characters refuses the write."

const ticketBlocksOnDesc = "The edges that hold the ticket beyond its dependencies. The value none is the default. The value children keeps an epic open while one child is open. Set children after the children exist, because a check reports a childless parent as a warning."

const ticketRefDesc = "The ticket id, or a unique short form of it."
const ticketIfRevisionDesc = "The current revision of this ticket. Every ticket write returns the new revision. A second write to the same ticket takes that value, and it needs no ticket_get. A stale value refuses the write, and the refusal names the current revision."

func ticketRefRevisionProps() map[string]any {
	return map[string]any{
		"ref":         map[string]any{"type": "string", "description": ticketRefDesc},
		"if_revision": map[string]any{"type": "string", "description": ticketIfRevisionDesc},
	}
}

// --- ticket_create ----------------------------------------------------------

type TicketCreateTool struct{ *TicketCore }

type ticketCreateArgs struct {
	Title              string   `json:"title"`
	Type               string   `json:"type"`
	Priority           string   `json:"priority"`
	Labels             []string `json:"labels"`
	Assignees          []string `json:"assignees"`
	Milestone          string   `json:"milestone"`
	Description        string   `json:"description"`
	ImplementationPlan string   `json:"implementation_plan"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	DefinitionOfDone   []string `json:"definition_of_done"`
	Parent             string   `json:"parent"`
	Dependencies       []string `json:"dependencies"`
	DueOn              string   `json:"due_on"`
	BlocksOn           string   `json:"blocks_on"`
	// Status and Reason are the backport path of .tickets/CONVENTIONS.md.
	// The store accepts done and archived here and nothing else, so a
	// create can record work that is over. It can never file a ticket into
	// ready, which is the promotion a person owns.
	Status string `json:"status"`
	Reason string `json:"reason"`
	// References ride a second write, because CreateOptions has no field
	// for them. The tool call stays one call, and the store sees a create
	// and then one AddReference batch.
	References []ticketRefArg `json:"references"`
}

func (t *TicketCreateTool) Name() string { return "ticket_create" }
func (t *TicketCreateTool) Description() string {
	return i18n.D("tool.ticket_create.description", "Create a ticket in the .tickets store of this repository. The tool writes one file in the store, and it never publishes anything. A new ticket lands in draft, and the promotion of a draft is a decision for a person. Give title. The schema names every other field, and each one is optional. The tool returns the new id, the path, and the revision.")
}
func (t *TicketCreateTool) ToolGroupName() string { return "ticket" }
func (t *TicketCreateTool) Schema() json.RawMessage {
	return mustSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":               map[string]any{"type": "string", "description": "The one-line title of the ticket. " + ticketTitleLimitDesc},
			"type":                map[string]any{"type": "string", "enum": ticket.Types, "description": "The ticket type. The default comes from the store."},
			"priority":            map[string]any{"type": "string", "enum": ticket.Priorities, "description": "The priority. The default comes from the store."},
			"labels":              t.ticketLabelsProp("The labels for the ticket."),
			"assignees":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The actors to assign. No mutation changes this field later, so give it here or edit the file."},
			"milestone":           map[string]any{"type": "string", "description": "The milestone for the ticket."},
			"description":         map[string]any{"type": "string", "description": "The Markdown body of the Description section."},
			"implementation_plan": map[string]any{"type": "string", "description": "The Markdown body of the Implementation plan section."},
			"acceptance_criteria": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The Acceptance criteria, one entry for each item. The store writes them as unchecked boxes, in this order."},
			"definition_of_done":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The Definition of done, one entry for each item. The store writes them as unchecked boxes, in this order."},
			"parent":              map[string]any{"type": "string", "description": "The id of the parent ticket, for a child of an epic."},
			"dependencies":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The ids of the tickets that this ticket depends on."},
			"due_on":              map[string]any{"type": "string", "description": "The deadline as a date, for example 2026-12-31."},
			"blocks_on":           map[string]any{"type": "string", "enum": ticket.BlocksOnValues, "description": ticketBlocksOnDesc},
			"references":          map[string]any{"type": "array", "items": ticketReferenceItemsSchema(), "description": ticketReferencesDesc},
			"status":              map[string]any{"type": "string", "enum": []string{ticket.StatusDone, ticket.StatusArchived}, "description": "The status to file the ticket in. A new ticket lands in draft, and this field accepts done and archived only. Use it to record work that is already over. A person owns the promotion of a draft to ready, and no create can reach that status."},
			"reason":              map[string]any{"type": "string", "description": "The reason for an archived create. The store refuses it with any other status."},
		},
		"required": []string{"title"},
	})
}

func (t *TicketCreateTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in ticketCreateArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in)
	}
	if strings.TrimSpace(in.Title) == "" {
		return ticketResult(nil, fmt.Errorf("give title: the one-line title of the new ticket"))
	}
	s, err := t.open()
	if err != nil {
		return ticketResult(nil, err)
	}
	opts := ticket.CreateOptions{
		Title:              in.Title,
		Type:               in.Type,
		Priority:           in.Priority,
		Labels:             in.Labels,
		Assignees:          in.Assignees,
		Dependencies:       in.Dependencies,
		Description:        in.Description,
		ImplementationPlan: in.ImplementationPlan,
		AcceptanceCriteria: in.AcceptanceCriteria,
		DefinitionOfDone:   in.DefinitionOfDone,
		BlocksOn:           in.BlocksOn,
		Status:             in.Status,
		Reason:             in.Reason,
		Actor:              t.actor(),
	}
	if in.Parent != "" {
		opts.Parent = &in.Parent
	}
	if in.DueOn != "" {
		opts.DueOn = &in.DueOn
	}
	if in.Milestone != "" {
		opts.Milestone = &in.Milestone
	}
	res, err := s.Create(ctx, opts)
	if err != nil {
		return ticketResult(nil, err)
	}
	t.Card.Invalidate()
	// The references are a second write, and the create already made the
	// file. A failure here therefore leaves a ticket with no references,
	// so the refusal names the id and the repair. The write needs no
	// precondition, because the id is one second old and nobody else has
	// seen it.
	if ms := referenceMutations(in.References); len(ms) > 0 {
		if _, aerr := s.Apply(ctx, res.Ticket.ID, ms, ticket.ApplyOptions{Actor: t.actor()}); aerr != nil {
			return ticketResult(nil, fmt.Errorf("the store created %s, and the references did not land: %w. Add them with ticket_update", res.Ticket.ID, aerr))
		}
	}
	return t.freshOut(ctx, s, res.Ticket.ID)
}

// --- ticket_update ----------------------------------------------------------

type TicketUpdateTool struct{ *TicketCore }

type ticketUpdateArgs struct {
	Ref                string         `json:"ref"`
	IfRevision         string         `json:"if_revision"`
	Title              string         `json:"title"`
	Type               string         `json:"type"`
	Priority           string         `json:"priority"`
	DueOn              string         `json:"due_on"`
	Milestone          string         `json:"milestone"`
	Parent             string         `json:"parent"`
	BlocksOn           string         `json:"blocks_on"`
	Description        string         `json:"description"`
	ImplementationPlan string         `json:"implementation_plan"`
	Summary            string         `json:"summary"`
	AddLabels          []string       `json:"add_labels"`
	RemoveLabels       []string       `json:"remove_labels"`
	AddDependencies    []string       `json:"add_dependencies"`
	RemoveDependencies []string       `json:"remove_dependencies"`
	AddReferences      []ticketRefArg `json:"add_references"`
	RemoveReferences   []string       `json:"remove_references"`
}

func (a ticketUpdateArgs) mutations() ticket.Mutations {
	var ms ticket.Mutations
	if a.Title != "" {
		ms = append(ms, ticket.SetTitle{Title: a.Title})
	}
	if a.Type != "" {
		ms = append(ms, ticket.SetType{Type: a.Type})
	}
	if a.Priority != "" {
		ms = append(ms, ticket.SetPriority{Priority: a.Priority})
	}
	if a.DueOn != "" {
		due := a.DueOn
		ms = append(ms, ticket.SetDueOn{DueOn: &due})
	}
	if a.Milestone != "" {
		mile := a.Milestone
		ms = append(ms, ticket.SetMilestone{Milestone: &mile})
	}
	if a.Parent != "" {
		par := a.Parent
		ms = append(ms, ticket.SetParent{Parent: &par})
	}
	if a.BlocksOn != "" {
		ms = append(ms, ticket.SetBlocksOn{BlocksOn: a.BlocksOn})
	}
	if a.Description != "" {
		ms = append(ms, ticket.SetDescription{Text: a.Description})
	}
	if a.ImplementationPlan != "" {
		ms = append(ms, ticket.SetImplementationPlan{Text: a.ImplementationPlan})
	}
	if a.Summary != "" {
		ms = append(ms, ticket.SetSummary{Text: a.Summary})
	}
	for _, l := range a.AddLabels {
		ms = append(ms, ticket.AddLabel{Label: l})
	}
	for _, l := range a.RemoveLabels {
		ms = append(ms, ticket.RemoveLabel{Label: l})
	}
	for _, d := range a.AddDependencies {
		ms = append(ms, ticket.AddDependency{ID: d})
	}
	for _, d := range a.RemoveDependencies {
		ms = append(ms, ticket.RemoveDependency{ID: d})
	}
	ms = append(ms, referenceMutations(a.AddReferences)...)
	for _, r := range a.RemoveReferences {
		ms = append(ms, ticket.RemoveReference{Ref: r})
	}
	return ms
}

func (t *TicketUpdateTool) Name() string { return "ticket_update" }
func (t *TicketUpdateTool) Description() string {
	return i18n.D("tool.ticket_update.description", "Change the fields of one ticket. The tool writes the ticket file, and it never publishes anything. Give ref, if_revision, and at least one change. The changes apply as one write: all of them land, or none do. Pass the revision from your last write to this ticket, or read the ticket with ticket_get.\n\nThis tool does not change the status. Use ticket_transition for that.")
}
func (t *TicketUpdateTool) ToolGroupName() string { return "ticket" }
func (t *TicketUpdateTool) Schema() json.RawMessage {
	props := ticketRefRevisionProps()
	props["title"] = map[string]any{"type": "string", "description": "The new title. " + ticketTitleLimitDesc}
	props["type"] = map[string]any{"type": "string", "enum": ticket.Types, "description": "The new type."}
	props["priority"] = map[string]any{"type": "string", "enum": ticket.Priorities, "description": "The new priority."}
	props["due_on"] = map[string]any{"type": "string", "description": "The new deadline as a date, for example 2026-12-31."}
	props["milestone"] = map[string]any{"type": "string", "description": "The new milestone."}
	props["parent"] = map[string]any{"type": "string", "description": "The id of the new parent ticket."}
	props["blocks_on"] = map[string]any{"type": "string", "enum": ticket.BlocksOnValues, "description": ticketBlocksOnDesc}
	props["description"] = map[string]any{"type": "string", "description": "The new Markdown body of the Description section. It replaces the section."}
	props["implementation_plan"] = map[string]any{"type": "string", "description": "The new Markdown body of the Implementation plan section. It replaces the section."}
	props["summary"] = map[string]any{"type": "string", "description": "The new Markdown body of the Summary section. It replaces the section."}
	props["add_labels"] = t.ticketLabelsProp("The labels to add.")
	props["remove_labels"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The labels to remove."}
	props["add_dependencies"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The ticket ids to add as dependencies."}
	props["remove_dependencies"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The ticket ids to remove from the dependencies."}
	props["add_references"] = map[string]any{"type": "array", "items": ticketReferenceItemsSchema(), "description": ticketReferencesDesc + " A ref that the ticket already carries takes the new path."}
	props["remove_references"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The refs to remove from the references."}
	return mustSchema(map[string]any{
		"type":       "object",
		"properties": props,
		"required":   []string{"ref", "if_revision"},
	})
}

func (t *TicketUpdateTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in ticketUpdateArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in)
	}
	ms := in.mutations()
	if len(ms) == 0 {
		return ticketResult(nil, fmt.Errorf("give at least one change, for example title, priority, or add_labels"))
	}
	return t.applyWrite(ctx, in.Ref, in.IfRevision, ms)
}

// --- ticket_transition ------------------------------------------------------

type TicketTransitionTool struct {
	*TicketCore
	// Tasks is the session task board a closing transition records as a note,
	// and nil when the session carries no task tools. The build layer binds it
	// through WithTasks, never at construction.
	Tasks TaskBoard
}

// ticketTransitionOut is the standard write row plus what the transition did to
// the worklog. The report carries the reason when no note was written, because
// an absent note has several causes and only one of them is a problem.
type ticketTransitionOut struct {
	ticketWriteOut
	Worklog *worklogReport `json:"worklog,omitempty"`
}

type worklogReport struct {
	Noted   bool   `json:"noted,omitempty"`
	Skipped string `json:"skipped,omitempty"`
}

type ticketTransitionArgs struct {
	Ref        string `json:"ref"`
	IfRevision string `json:"if_revision"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
}

func (t *TicketTransitionTool) Name() string { return "ticket_transition" }
func (t *TicketTransitionTool) Description() string {
	return i18n.D("tool.ticket_transition.description", "Move one ticket to another status. The tool writes the ticket file, and it never publishes anything. Give ref, status, and if_revision. A reason is necessary for blocked, and for a reopen from done. The store refuses a transition that the lifecycle does not permit, and the refusal names the permitted ones. Leave the promotion of a draft to a person, unless the user tells you otherwise.\n\nA move to done, archived, or blocked also writes a worklog note. The note holds the tasks that carry this ticket, with their evidence. The tool writes no note when the task list holds no work for this ticket. It gives the reason in the result.")
}
func (t *TicketTransitionTool) ToolGroupName() string { return "ticket" }
func (t *TicketTransitionTool) Schema() json.RawMessage {
	props := ticketRefRevisionProps()
	props["status"] = map[string]any{"type": "string", "description": "The status to move to, for example ready, in-progress, blocked, done, or archived."}
	props["reason"] = map[string]any{"type": "string", "description": "The reason for the move. It is necessary for blocked, and for a reopen from done."}
	return mustSchema(map[string]any{
		"type":       "object",
		"properties": props,
		"required":   []string{"ref", "if_revision", "status"},
	})
}

func (t *TicketTransitionTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in ticketTransitionArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in)
	}
	if strings.TrimSpace(in.Status) == "" {
		return ticketResult(nil, fmt.Errorf("give status: the status to move the ticket to"))
	}
	// Closure is the dispatcher's call, never the sub-agent's. Refused before the
	// write, so a refused close changes nothing.
	if refusal := t.refuseSubagentClosure(in.Ref, in.Status); refusal != "" {
		return ticketResult(nil, fmt.Errorf("%s", refusal))
	}
	s, id, err := t.applyMutation(ctx, in.Ref, in.IfRevision, ticket.SetStatus{Status: in.Status, Reason: in.Reason})
	if err != nil {
		return ticketResult(nil, err)
	}
	rep := t.recordWorklog(ctx, s, id, in.Status)
	row, err := t.freshRow(ctx, s, id)
	if err != nil {
		return ticketResult(nil, err)
	}
	return ticketResult(ticketTransitionOut{ticketWriteOut: row, Worklog: rep}, nil)
}

// recordWorklog appends this ticket's share of the session task board as a note
// when the ticket closes.
//
// It runs after the status write, and a failure never fails the transition. The
// ticket did move, and returning the move as an error would invite the model to
// move it again, which the lifecycle would then refuse.
func (t *TicketTransitionTool) recordWorklog(ctx context.Context, s *ticket.Store, id, status string) *worklogReport {
	if !closesTicket(status) {
		return nil
	}
	body, skipped := worklogFor(t.Tasks, id)
	if skipped != "" {
		return &worklogReport{Skipped: skipped}
	}
	if body == "" {
		return nil
	}
	cur, err := s.Get(ctx, id)
	if err != nil {
		return &worklogReport{Skipped: "could not read the ticket back for the worklog note: " + err.Error()}
	}
	if _, err := s.Apply(ctx, id, ticket.AppendNote{Text: body},
		ticket.ApplyOptions{IfRevision: cur.Revision, Actor: t.actor()}); err != nil {
		return &worklogReport{Skipped: "could not append the worklog note: " + err.Error()}
	}
	return &worklogReport{Noted: true}
}

// --- ticket_claim -----------------------------------------------------------

type TicketClaimTool struct {
	*TicketCore
	// Tasks is the session task board a claim seeds, and nil when the session
	// carries no task tools. The build layer binds it through WithTasks, never
	// at construction, so each conversation gets the board that belongs to it.
	Tasks TaskBoard
}

type ticketClaimArgs struct {
	Ref              string `json:"ref"`
	IfRevision       string `json:"if_revision"`
	Release          bool   `json:"release"`
	Branch           string `json:"branch"`
	ExpiresInMinutes int    `json:"expires_in_minutes"`
	Force            bool   `json:"force"`
	// SeedTasks is a pointer because it defaults to true. An absent JSON bool
	// reads as false, so a plain bool would turn seeding off for every caller
	// that does not mention it.
	SeedTasks *bool `json:"seed_tasks"`
}

// ticketClaimOut is the standard write row plus what the claim did to the task
// board. SeededTasks is absent only when the session has no board at all. An
// opt-out reports what it gave up, because silence read as success and sent a
// session to `edit` for every criterion it wanted to check.
type ticketClaimOut struct {
	ticketWriteOut
	SeededTasks *seedReport `json:"seeded_tasks,omitempty"`
}

func (t *TicketClaimTool) Name() string { return "ticket_claim" }
func (t *TicketClaimTool) Description() string {
	return i18n.D("tool.ticket_claim.description", "Claim one ticket before you work it, or release your claim. The tool writes the ticket file, and it never publishes anything. Give ref and if_revision, and set release to true to release your claim. A claim of a ticket that you already hold renews it. A claim is metadata and not a status. Move the status with ticket_transition.\n\nA claim also seeds the task list. The tool makes one task for each acceptance criterion that is not checked yet. A task that you close then checks its criterion. Set seed_tasks to false to leave the task list alone. The tool reports a reason whenever it seeds nothing, and the opt-out is one of those reasons.\n\nThe tool seeds nothing when the list still holds open tasks. A mix of two tickets' tasks is hard to undo. The reason names the tasks that are in the way.\n\nA claim archives the tasks that you closed before it seeds. The task list then shows the work of this ticket alone. The result gives the count.\n\nSet force to true only when the user tells you to take work from another actor. The store then records the displaced claim.")
}
func (t *TicketClaimTool) ToolGroupName() string { return "ticket" }
func (t *TicketClaimTool) Schema() json.RawMessage {
	props := ticketRefRevisionProps()
	props["release"] = map[string]any{"type": "boolean", "description": "Release your claim instead of a claim."}
	props["branch"] = map[string]any{"type": "string", "description": "The git branch that the work rides on, recorded in the claim."}
	props["expires_in_minutes"] = map[string]any{"type": "integer", "description": "The life of the claim in minutes. Zero means the store default, and a renewal with zero keeps the expiry that the claim carries."}
	props["force"] = map[string]any{"type": "boolean", "description": "Take a live claim from another actor. The store records the displaced claim in Notes."}
	props["seed_tasks"] = map[string]any{"type": "boolean", "description": "Seed the task list from the acceptance criteria that are not checked yet. The default is true. Set it to false to claim the ticket and leave the task list alone."}
	return mustSchema(map[string]any{
		"type":       "object",
		"properties": props,
		"required":   []string{"ref", "if_revision"},
	})
}

func (t *TicketClaimTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in ticketClaimArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in)
	}
	if in.Release {
		return t.applyWrite(ctx, in.Ref, in.IfRevision, ticket.ReleaseClaim{})
	}
	s, id, err := t.applyMutation(ctx, in.Ref, in.IfRevision, ticket.ClaimTicket{
		Branch:    in.Branch,
		ExpiresIn: time.Duration(in.ExpiresInMinutes) * time.Minute,
		Force:     in.Force,
		Session:   t.sessionID(),
	})
	if err != nil {
		return ticketResult(nil, err)
	}
	tk, err := s.Get(ctx, id)
	if err != nil {
		return ticketResult(nil, err)
	}
	out := ticketClaimOut{ticketWriteOut: ticketWriteOut{
		ticketRow:  rowFromTicket(tk),
		References: t.ticketReferences(tk.References),
		Revision:   tk.Revision,
		Path:       tk.Path,
	}}
	// The claim already landed. A seeding failure past this point must not read
	// as a failed claim, so it travels in the payload and never as an error.
	if in.SeedTasks != nil && !*in.SeedTasks {
		out.SeededTasks = seedOptOut(t.Tasks)
	} else {
		out.SeededTasks = seedFromCriteria(t.Tasks, tk)
	}
	return ticketResult(out, nil)
}

// --- ticket_fix -------------------------------------------------------------

// TicketFixTool is check's other half. It classifies with the write tools
// and not beside ticket_check, because it moves and rewrites files in the
// user's repository. The name rhymes with a read, and the authority does
// not.
//
// It carries no if_revision. The other five write tools each target one
// ticket, and a precondition names the bytes they read. Fix walks the whole
// store, so no single revision gates it. dry_run is the preview instead.
type TicketFixTool struct{ *TicketCore }

type ticketFixArgs struct {
	DryRun bool `json:"dry_run"`
}

// ticketRepair is one repair, in terva's JSON casing. The library struct
// carries no tags, and its content field is unexported anyway.
type ticketRepair struct {
	Kind   string   `json:"kind"`
	Codes  []string `json:"codes,omitempty"`
	Ticket string   `json:"ticket,omitempty"`
	From   string   `json:"from,omitempty"`
	To     string   `json:"to,omitempty"`
}

func ticketRepairs(rs []ticket.Repair) []ticketRepair {
	out := make([]ticketRepair, 0, len(rs))
	for _, r := range rs {
		out = append(out, ticketRepair{Kind: r.Kind, Codes: r.Codes, Ticket: r.Ticket, From: r.From, To: r.To})
	}
	return out
}

func (t *TicketFixTool) Name() string { return "ticket_fix" }
func (t *TicketFixTool) Description() string {
	return i18n.D("tool.ticket_fix.description", "Repair the ticket store, and report what the pass changed. The tool moves and rewrites files in the store, and it never publishes anything. It repairs three problems only. One is a file name that does not match its ticket id. One is an archived ticket in the wrong directory. One is a stale epics.md.\n\nThe report also holds every problem that remains, because each of those needs a decision that only you can make. Set dry_run to true to read the repairs first. The tool then writes nothing.")
}
func (t *TicketFixTool) ToolGroupName() string { return "ticket" }
func (t *TicketFixTool) Schema() json.RawMessage {
	return mustSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"dry_run": map[string]any{"type": "boolean", "description": "Plan the repairs and write nothing. The findings that the repairs would clear stay in the report."},
		},
	})
}

func (t *TicketFixTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in ticketFixArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in)
	}
	s, err := t.open()
	if err != nil {
		return ticketResult(nil, err)
	}
	res, err := s.Fix(ctx, ticket.FixOptions{DryRun: in.DryRun})
	if err != nil {
		return ticketResult(nil, err)
	}
	// A dry run writes nothing, so it leaves the card alone.
	if !in.DryRun {
		t.Card.Invalidate()
	}
	out := map[string]any{
		"dry_run": in.DryRun,
		"repairs": ticketRepairs(res.Repairs),
	}
	// Fix returns the report of the store as it stands when the pass ends.
	// Under dry_run that store still holds the findings the repairs would
	// have cleared, which is what makes the preview readable.
	if rep := res.Report; rep != nil {
		out["ok"] = len(rep.Errors) == 0
		out["errors"] = ticketFindings(rep.Errors)
		out["warnings"] = ticketFindings(rep.Warnings)
	}
	return ticketResult(out, nil)
}

// --- ticket_comment ---------------------------------------------------------

type TicketCommentTool struct{ *TicketCore }

type ticketCommentArgs struct {
	Ref        string `json:"ref"`
	IfRevision string `json:"if_revision"`
	Text       string `json:"text"`
	Kind       string `json:"kind"`
}

func (t *TicketCommentTool) Name() string { return "ticket_comment" }
func (t *TicketCommentTool) Description() string {
	return i18n.D("tool.ticket_comment.description", "Append a comment or a note to one ticket. The tool writes the ticket file, and it never publishes anything. Give ref, text, and if_revision. The kind field selects comment or note, and the default is comment. The store stamps the entry with your identity and the time.")
}
func (t *TicketCommentTool) ToolGroupName() string { return "ticket" }
func (t *TicketCommentTool) Schema() json.RawMessage {
	props := ticketRefRevisionProps()
	props["text"] = map[string]any{"type": "string", "description": "The Markdown text of the entry."}
	props["kind"] = map[string]any{"type": "string", "enum": []string{"comment", "note"}, "description": "The section for the entry. A comment is discussion, and a note is a work record. The default is comment."}
	return mustSchema(map[string]any{
		"type":       "object",
		"properties": props,
		"required":   []string{"ref", "if_revision", "text"},
	})
}

func (t *TicketCommentTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in ticketCommentArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in)
	}
	if strings.TrimSpace(in.Text) == "" {
		return ticketResult(nil, fmt.Errorf("give text: the Markdown text of the entry"))
	}
	var m ticket.Mutation
	switch in.Kind {
	case "", "comment":
		m = ticket.AppendComment{Text: in.Text}
	case "note":
		m = ticket.AppendNote{Text: in.Text}
	default:
		return ticketResult(nil, fmt.Errorf("kind must be comment or note, not %q", in.Kind))
	}
	return t.applyWrite(ctx, in.Ref, in.IfRevision, m)
}
