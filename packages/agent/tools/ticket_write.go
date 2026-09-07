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
	Revision string `json:"revision,omitempty"`
	Path     string `json:"path,omitempty"`
}

func (c *TicketCore) freshOut(ctx context.Context, s *ticket.Store, ref string) (core.ToolResult, error) {
	tk, err := s.Get(ctx, ref)
	if err != nil {
		return ticketResult(nil, err)
	}
	return ticketResult(ticketWriteOut{ticketRow: rowFromTicket(tk), Revision: tk.Revision, Path: tk.Path}, nil)
}

// applyWrite runs one mutation under the required precondition. On a stale
// revision it re-reads and names the current one, because the library's
// error carries the values in Details and the model needs them in prose.
func (c *TicketCore) applyWrite(ctx context.Context, ref, ifRevision string, m ticket.Mutation) (core.ToolResult, error) {
	if strings.TrimSpace(ref) == "" {
		return ticketResult(nil, fmt.Errorf("give ref: a ticket id, or a unique short form of it"))
	}
	if strings.TrimSpace(ifRevision) == "" {
		return ticketResult(nil, fmt.Errorf("give if_revision: the revision that ticket_get returned for this ticket"))
	}
	s, err := c.open()
	if err != nil {
		return ticketResult(nil, err)
	}
	res, err := s.Apply(ctx, ref, m, ticket.ApplyOptions{IfRevision: ifRevision, Actor: c.actor()})
	if err != nil {
		var te *ticket.Error
		if errors.As(err, &te) && te.Code == ticket.CodeStaleRevision {
			if cur, gerr := s.Get(ctx, ref); gerr == nil {
				return ticketResult(nil, fmt.Errorf("%s. The current revision is %s. Read the ticket again with ticket_get, and retry with that value.", err.Error(), cur.Revision))
			}
		}
		return ticketResult(nil, err)
	}
	return c.freshOut(ctx, s, res.Ticket.ID)
}

const ticketRefDesc = "The ticket id, or a unique short form of it."
const ticketIfRevisionDesc = "The revision that ticket_get returned for this ticket. A stale value refuses the write, and the refusal names the current revision."

func ticketRefRevisionProps() map[string]any {
	return map[string]any{
		"ref":         map[string]any{"type": "string", "description": ticketRefDesc},
		"if_revision": map[string]any{"type": "string", "description": ticketIfRevisionDesc},
	}
}

// --- ticket_create ----------------------------------------------------------

type TicketCreateTool struct{ *TicketCore }

type ticketCreateArgs struct {
	Title        string   `json:"title"`
	Type         string   `json:"type"`
	Priority     string   `json:"priority"`
	Labels       []string `json:"labels"`
	Description  string   `json:"description"`
	Parent       string   `json:"parent"`
	Dependencies []string `json:"dependencies"`
	DueOn        string   `json:"due_on"`
}

func (t *TicketCreateTool) Name() string { return "ticket_create" }
func (t *TicketCreateTool) Description() string {
	return i18n.D("tool.ticket_create.description", "Create a ticket in the .tickets store of this repository. The tool writes one file in the store, and it never publishes anything. A new ticket lands in draft, and the promotion of a draft is a decision for a person. Give title, and any of type, priority, labels, description, parent, dependencies, and due_on. The tool returns the new id, the path, and the revision.")
}
func (t *TicketCreateTool) ToolGroupName() string { return "ticket" }
func (t *TicketCreateTool) Schema() json.RawMessage {
	return mustSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":        map[string]any{"type": "string", "description": "The one-line title of the ticket."},
			"type":         map[string]any{"type": "string", "description": "The ticket type, for example task, bug, feature, or epic. The default comes from the store."},
			"priority":     map[string]any{"type": "string", "description": "The priority, for example low, normal, high, or urgent. The default comes from the store."},
			"labels":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The labels for the ticket."},
			"description":  map[string]any{"type": "string", "description": "The Markdown body of the Description section."},
			"parent":       map[string]any{"type": "string", "description": "The id of the parent ticket, for a child of an epic."},
			"dependencies": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The ids of the tickets that this ticket depends on."},
			"due_on":       map[string]any{"type": "string", "description": "The deadline as a date, for example 2026-12-31."},
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
		Title:        in.Title,
		Type:         in.Type,
		Priority:     in.Priority,
		Labels:       in.Labels,
		Dependencies: in.Dependencies,
		Description:  in.Description,
		Actor:        t.actor(),
	}
	if in.Parent != "" {
		opts.Parent = &in.Parent
	}
	if in.DueOn != "" {
		opts.DueOn = &in.DueOn
	}
	res, err := s.Create(ctx, opts)
	if err != nil {
		return ticketResult(nil, err)
	}
	return t.freshOut(ctx, s, res.Ticket.ID)
}

// --- ticket_update ----------------------------------------------------------

type TicketUpdateTool struct{ *TicketCore }

type ticketUpdateArgs struct {
	Ref                string   `json:"ref"`
	IfRevision         string   `json:"if_revision"`
	Title              string   `json:"title"`
	Type               string   `json:"type"`
	Priority           string   `json:"priority"`
	DueOn              string   `json:"due_on"`
	Milestone          string   `json:"milestone"`
	Parent             string   `json:"parent"`
	Description        string   `json:"description"`
	ImplementationPlan string   `json:"implementation_plan"`
	Summary            string   `json:"summary"`
	AddLabels          []string `json:"add_labels"`
	RemoveLabels       []string `json:"remove_labels"`
	AddDependencies    []string `json:"add_dependencies"`
	RemoveDependencies []string `json:"remove_dependencies"`
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
	return ms
}

func (t *TicketUpdateTool) Name() string { return "ticket_update" }
func (t *TicketUpdateTool) Description() string {
	return i18n.D("tool.ticket_update.description", "Change the fields of one ticket. The tool writes the ticket file, and it never publishes anything. Give ref, if_revision, and at least one change. The changes apply as one write: all of them land, or none do. Read the ticket first with ticket_get, and pass its revision as if_revision.\n\nThis tool does not change the status. Use ticket_transition for that.")
}
func (t *TicketUpdateTool) ToolGroupName() string { return "ticket" }
func (t *TicketUpdateTool) Schema() json.RawMessage {
	props := ticketRefRevisionProps()
	props["title"] = map[string]any{"type": "string", "description": "The new title."}
	props["type"] = map[string]any{"type": "string", "description": "The new type."}
	props["priority"] = map[string]any{"type": "string", "description": "The new priority."}
	props["due_on"] = map[string]any{"type": "string", "description": "The new deadline as a date, for example 2026-12-31."}
	props["milestone"] = map[string]any{"type": "string", "description": "The new milestone."}
	props["parent"] = map[string]any{"type": "string", "description": "The id of the new parent ticket."}
	props["description"] = map[string]any{"type": "string", "description": "The new Markdown body of the Description section. It replaces the section."}
	props["implementation_plan"] = map[string]any{"type": "string", "description": "The new Markdown body of the Implementation plan section. It replaces the section."}
	props["summary"] = map[string]any{"type": "string", "description": "The new Markdown body of the Summary section. It replaces the section."}
	props["add_labels"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The labels to add."}
	props["remove_labels"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The labels to remove."}
	props["add_dependencies"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The ticket ids to add as dependencies."}
	props["remove_dependencies"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The ticket ids to remove from the dependencies."}
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

type TicketTransitionTool struct{ *TicketCore }

type ticketTransitionArgs struct {
	Ref        string `json:"ref"`
	IfRevision string `json:"if_revision"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
}

func (t *TicketTransitionTool) Name() string { return "ticket_transition" }
func (t *TicketTransitionTool) Description() string {
	return i18n.D("tool.ticket_transition.description", "Move one ticket to another status. The tool writes the ticket file, and it never publishes anything. Give ref, status, and if_revision. A reason is necessary for blocked, and for a reopen from done. The store refuses a transition that the lifecycle does not permit, and the refusal names the permitted ones. Leave the promotion of a draft to a person, unless the user tells you otherwise.")
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
	return t.applyWrite(ctx, in.Ref, in.IfRevision, ticket.SetStatus{Status: in.Status, Reason: in.Reason})
}

// --- ticket_claim -----------------------------------------------------------

type TicketClaimTool struct{ *TicketCore }

type ticketClaimArgs struct {
	Ref              string `json:"ref"`
	IfRevision       string `json:"if_revision"`
	Release          bool   `json:"release"`
	Branch           string `json:"branch"`
	ExpiresInMinutes int    `json:"expires_in_minutes"`
	Force            bool   `json:"force"`
}

func (t *TicketClaimTool) Name() string { return "ticket_claim" }
func (t *TicketClaimTool) Description() string {
	return i18n.D("tool.ticket_claim.description", "Claim one ticket before you work it, or release your claim. The tool writes the ticket file, and it never publishes anything. Give ref and if_revision, and set release to true to release your claim. A claim of a ticket that you already hold renews it. A claim is metadata and not a status. Move the status with ticket_transition.\n\nSet force to true only when the user tells you to take work from another actor. The store then records the displaced claim.")
}
func (t *TicketClaimTool) ToolGroupName() string { return "ticket" }
func (t *TicketClaimTool) Schema() json.RawMessage {
	props := ticketRefRevisionProps()
	props["release"] = map[string]any{"type": "boolean", "description": "Release your claim instead of a claim."}
	props["branch"] = map[string]any{"type": "string", "description": "The git branch that the work rides on, recorded in the claim."}
	props["expires_in_minutes"] = map[string]any{"type": "integer", "description": "The life of the claim in minutes. Zero means the store default, and a renewal with zero keeps the expiry that the claim carries."}
	props["force"] = map[string]any{"type": "boolean", "description": "Take a live claim from another actor. The store records the displaced claim in Notes."}
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
	return t.applyWrite(ctx, in.Ref, in.IfRevision, ticket.ClaimTicket{
		Branch:    in.Branch,
		ExpiresIn: time.Duration(in.ExpiresInMinutes) * time.Minute,
		Force:     in.Force,
	})
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
