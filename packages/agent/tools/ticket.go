package tools

// The ticket_* read tools: slice 2 of docs/plans/git-ticket.md. They call
// git-ticket's ticket package directly rather than its cli package, because
// a tool wants structured values and not rendered text. All five are
// read-only and carry no mutation authority; the write tools are slice 3.
//
// Registration is store-gated in BuildToolRegistry: a repository with no
// .tickets/ store pays no schema cost. The store opens fresh on every call,
// because bash and git rewrite ticket files between turns and a cached
// handle would go stale against the disk.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// TicketStoreAvailable reports whether a .tickets store governs dir. It is
// the registration gate, probed once per registry build.
func TicketStoreAvailable(dir string) bool {
	_, err := ticket.Discover(dir)
	return err == nil
}

// TicketCore is the shared state of the ticket tools: the session cwd the
// store is discovered from, and the actor identity the write tools record.
// The actor rides here rather than in any schema, because a model does not
// choose who it is. Empty actor fields fall back to the first actor in the
// store's config.yml, per ApplyOptions.
type TicketCore struct {
	CWD string
	// ActorID is the identity mutations record, in the shape the plan
	// gives: agent:terva/<persona>. ActorName is the display name.
	ActorID   string
	ActorName string
	// SubagentID is this process's swarm id, and empty outside a swarm child.
	// A sub-agent works a ticket and never closes one, so the write tools read
	// this to refuse a closure rather than being dropped from the registry.
	SubagentID string
	// Card is the session's ticket context card. Every write here invalidates
	// it, so the next turn renders the store this write left behind rather than
	// the one it found. nil for a host that renders no card, which Invalidate
	// tolerates.
	Card *TicketCard

	// labels caches the store's label vocabulary, which the write schemas
	// carry. Reading it scans every ticket, and Schema runs again on every
	// tool rebuild, so it is read once per session.
	labelsOnce sync.Once
	labels     ticketLabels
}

func (c *TicketCore) open() (*ticket.Store, error) {
	return ticket.Discover(c.CWD)
}

// ticketResult marshals a payload as the tool's JSON, and returns a store
// error as IsError text so the model can read git-ticket's coded refusal
// (ticket_not_found, ambiguous_ref) and act on it.
func ticketResult(v any, err error) (core.ToolResult, error) {
	if err != nil {
		return core.ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: err.Error()}},
			IsError: true,
		}, nil
	}
	b, merr := json.Marshal(v)
	if merr != nil {
		return core.ToolResult{}, fmt.Errorf("encode result: %w", merr)
	}
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: string(b)}}}, nil
}

// ticketRow is the summary shape ticket_list, ticket_search, and
// ticket_ready return. The full body stays behind ticket_get, so a listing
// of a large store does not carry every description with it.
type ticketRow struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Type         string   `json:"type"`
	Status       string   `json:"status"`
	StatusReason string   `json:"status_reason,omitempty"`
	Priority     string   `json:"priority"`
	Labels       []string `json:"labels,omitempty"`
	Assignees    []string `json:"assignees,omitempty"`
	Milestone    string   `json:"milestone,omitempty"`
	Parent       string   `json:"parent,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
	BlocksOn     string   `json:"blocks_on,omitempty"`
	DueOn        string   `json:"due_on,omitempty"`
	ClaimedBy    string   `json:"claimed_by,omitempty"`
	UpdatedAt    string   `json:"updated_at,omitempty"`
}

// ticketReference is a reference in terva's own JSON casing. The library
// struct has no tags, so it marshals as Ref and Path, and the write tools
// take ref and path in their arguments. One shape in both directions
// spares the model from reading a reference back in a casing it cannot
// send.
type ticketReference struct {
	Ref  string `json:"ref"`
	Path string `json:"path,omitempty"`
}

func ticketReferences(rs []ticket.Reference) []ticketReference {
	if len(rs) == 0 {
		return nil
	}
	out := make([]ticketReference, 0, len(rs))
	for _, r := range rs {
		var p string
		if r.Path != nil {
			p = *r.Path
		}
		out = append(out, ticketReference{Ref: r.Ref, Path: p})
	}
	return out
}

func rowFromTicket(t *ticket.Ticket) ticketRow {
	r := ticketRow{
		ID:           t.ID,
		Title:        t.Title,
		Type:         t.Type,
		Status:       t.Status,
		Priority:     t.Priority,
		Labels:       t.Labels,
		Assignees:    t.Assignees,
		Dependencies: t.Dependencies,
		UpdatedAt:    t.UpdatedAt.String(),
	}
	if t.StatusReason != nil {
		r.StatusReason = *t.StatusReason
	}
	if t.Milestone != nil {
		r.Milestone = *t.Milestone
	}
	if t.Parent != nil {
		r.Parent = *t.Parent
	}
	if t.DueOn != nil {
		r.DueOn = *t.DueOn
	}
	if t.BlocksOn != "" && t.BlocksOn != ticket.BlocksOnNone {
		r.BlocksOn = t.BlocksOn
	}
	if t.Claim != nil {
		r.ClaimedBy = t.Claim.Actor
	}
	return r
}

// Paging, per the requirement in docs/standard-tools.md: a store with a
// thousand tickets must not blow a context window. The convention is
// glob's: offset skips, limit caps, and next_offset appears exactly when
// the result was cut short.
const (
	ticketPageDefault = 50
	ticketPageMax     = 200
)

type ticketPageResult struct {
	Total      int         `json:"total"`
	Offset     int         `json:"offset"`
	More       bool        `json:"more"`
	NextOffset int         `json:"next_offset,omitempty"`
	Tickets    []ticketRow `json:"tickets"`
}

func ticketPageOf(ts []*ticket.Ticket, offset, limit int) ticketPageResult {
	if limit <= 0 {
		limit = ticketPageDefault
	}
	if limit > ticketPageMax {
		limit = ticketPageMax
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(ts) {
		offset = len(ts)
	}
	hi := offset + limit
	if hi > len(ts) {
		hi = len(ts)
	}
	rows := make([]ticketRow, 0, hi-offset)
	for _, t := range ts[offset:hi] {
		rows = append(rows, rowFromTicket(t))
	}
	out := ticketPageResult{Total: len(ts), Offset: offset, More: hi < len(ts), Tickets: rows}
	if out.More {
		out.NextOffset = hi
	}
	return out
}

// ticketFilterArgs is the filter surface ticket_list and ticket_search
// share. It mirrors ticket.Filter field for field.
type ticketFilterArgs struct {
	Status    []string `json:"status"`
	Type      []string `json:"type"`
	Priority  []string `json:"priority"`
	Labels    []string `json:"labels"`
	Assignees []string `json:"assignees"`
	Milestone []string `json:"milestone"`
	Parent    []string `json:"parent"`
	DueBy     string   `json:"due_by"`
	All       bool     `json:"all"`
}

func (a ticketFilterArgs) filter() ticket.Filter {
	return ticket.Filter{
		Status:    a.Status,
		Type:      a.Type,
		Priority:  a.Priority,
		Labels:    a.Labels,
		Assignees: a.Assignees,
		Milestone: a.Milestone,
		Parent:    a.Parent,
		DueBy:     a.DueBy,
		All:       a.All,
	}
}

func ticketFilterProps() map[string]any {
	strArr := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	return map[string]any{
		"status":    strArr("Return the tickets with one of these statuses only."),
		"type":      strArr("Return the tickets with one of these types only."),
		"priority":  strArr("Return the tickets with one of these priorities only."),
		"labels":    strArr("Return the tickets that carry all of these labels."),
		"assignees": strArr("Return the tickets with one of these assignees only."),
		"milestone": strArr("Return the tickets with one of these milestones only."),
		"parent":    strArr("Return the direct children of these ticket ids only. An empty string selects the tickets with no parent."),
		"due_by":    map[string]any{"type": "string", "description": "Return the tickets due on or before this date, for example 2026-12-31."},
		"all":       map[string]any{"type": "boolean", "description": "Include every ticket, with done and archived tickets. The default is open work only."},
	}
}

func ticketPagingProps() map[string]any {
	return map[string]any{
		"offset": map[string]any{"type": "integer", "description": "The number of tickets to skip before the tool returns results. To get the next page, use the next_offset value from a result that the tool cut short."},
		"limit":  map[string]any{"type": "integer", "description": "The maximum number of tickets in one result. The default is 50, and the maximum is 200."},
	}
}

func ticketSchemaWith(extra map[string]any, required ...string) json.RawMessage {
	props := map[string]any{}
	for k, v := range ticketFilterProps() {
		props[k] = v
	}
	for k, v := range ticketPagingProps() {
		props[k] = v
	}
	for k, v := range extra {
		props[k] = v
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return mustSchema(schema)
}

// --- ticket_list ------------------------------------------------------------

type TicketListTool struct{ *TicketCore }

type ticketListArgs struct {
	ticketFilterArgs
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

func (t *TicketListTool) Name() string { return "ticket_list" }
func (t *TicketListTool) Description() string {
	return i18n.D("tool.ticket_list.description", "List the tickets in the .tickets store of this repository. This tool only reads, and it changes nothing. The tool returns JSON with total, offset, more, and tickets. Each row gives the id, the title, the type, the status, the priority, the labels, the parent, the dependencies, and claimed_by. Without filters the tool returns open work only, and all includes done and archived tickets. Use offset and limit for the pages, and read the full body of one ticket with ticket_get.")
}
func (t *TicketListTool) ToolGroupName() string { return "ticket" }
func (t *TicketListTool) Schema() json.RawMessage {
	return ticketSchemaWith(nil)
}

func (t *TicketListTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in ticketListArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in) // every argument is optional
	}
	s, err := t.open()
	if err != nil {
		return ticketResult(nil, err)
	}
	ts, err := s.List(ctx, in.filter())
	if err != nil {
		return ticketResult(nil, err)
	}
	return ticketResult(ticketPageOf(ts, in.Offset, in.Limit), nil)
}

// --- ticket_search ----------------------------------------------------------

type TicketSearchTool struct{ *TicketCore }

type ticketSearchArgs struct {
	Text  string `json:"text"`
	Regex bool   `json:"regex"`
	ticketFilterArgs
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

func (t *TicketSearchTool) Name() string { return "ticket_search" }
func (t *TicketSearchTool) Description() string {
	return i18n.D("tool.ticket_search.description", "Search the tickets of this repository for a text fragment. This tool only reads, and it changes nothing. The match ignores letter case and finds the text in any position of a ticket file. With the regular-expression flag the text is an RE2 pattern. The tool takes the same filters, offset, and limit as ticket_list, and it returns rows of the same shape.")
}
func (t *TicketSearchTool) ToolGroupName() string { return "ticket" }
func (t *TicketSearchTool) Schema() json.RawMessage {
	return ticketSchemaWith(map[string]any{
		"text":  map[string]any{"type": "string", "description": "The text to find. The match ignores letter case."},
		"regex": map[string]any{"type": "boolean", "description": "Read the text as an RE2 pattern instead of a plain fragment."},
	}, "text")
}

func (t *TicketSearchTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in ticketSearchArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in)
	}
	if strings.TrimSpace(in.Text) == "" {
		return ticketResult(nil, fmt.Errorf("give text: the fragment or the RE2 pattern to find"))
	}
	s, err := t.open()
	if err != nil {
		return ticketResult(nil, err)
	}
	ts, err := s.Search(ctx, ticket.Query{Text: in.Text, Regex: in.Regex, Filter: in.filter()})
	if err != nil {
		return ticketResult(nil, err)
	}
	return ticketResult(ticketPageOf(ts, in.Offset, in.Limit), nil)
}

// --- ticket_get -------------------------------------------------------------

type TicketGetTool struct{ *TicketCore }

type ticketGetArgs struct {
	Ref string `json:"ref"`
}

// ticketFull is ticket_get's shape: the row plus the body sections and the
// read evidence. Revision names the exact bytes read, which is what a
// precondition will want when the write tools arrive in slice 3.
type ticketFull struct {
	ticketRow
	CreatedAt          string            `json:"created_at,omitempty"`
	Description        string            `json:"description,omitempty"`
	AcceptanceCriteria string            `json:"acceptance_criteria,omitempty"`
	DefinitionOfDone   string            `json:"definition_of_done,omitempty"`
	ImplementationPlan string            `json:"implementation_plan,omitempty"`
	Notes              string            `json:"notes,omitempty"`
	Comments           string            `json:"comments,omitempty"`
	Summary            string            `json:"summary,omitempty"`
	References         []ticketReference `json:"references,omitempty"`
	// NextStatuses is where ticket_transition may take this ticket from
	// where it stands. The lifecycle table lived in .tickets/CONVENTIONS.md
	// and in the refusal a bad move earns, so every transition cost a
	// document lookup or a failed write. The read that precedes the write
	// answers it instead.
	NextStatuses []string `json:"next_statuses,omitempty"`
	Path         string   `json:"path,omitempty"`
	Revision     string   `json:"revision,omitempty"`
}

func (t *TicketGetTool) Name() string { return "ticket_get" }
func (t *TicketGetTool) Description() string {
	return i18n.D("tool.ticket_get.description", "Read one ticket in full. This tool only reads, and it changes nothing. Give ref as a ticket id, or as a unique short form of the id. The tool returns JSON with the frontmatter fields, the body sections, the references, and the path. The result includes revision, which names the exact bytes that the tool read. It also gives next_statuses, the statuses that ticket_transition accepts for this ticket now.")
}
func (t *TicketGetTool) ToolGroupName() string { return "ticket" }
func (t *TicketGetTool) Schema() json.RawMessage {
	return mustSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ref": map[string]any{"type": "string", "description": "The ticket id, or a unique short form of it."},
		},
		"required": []string{"ref"},
	})
}

func (t *TicketGetTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in ticketGetArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in)
	}
	if strings.TrimSpace(in.Ref) == "" {
		return ticketResult(nil, fmt.Errorf("give ref: a ticket id, or a unique short form of it"))
	}
	s, err := t.open()
	if err != nil {
		return ticketResult(nil, err)
	}
	tk, err := s.Get(ctx, in.Ref)
	if err != nil {
		return ticketResult(nil, err)
	}
	out := ticketFull{
		ticketRow:          rowFromTicket(tk),
		CreatedAt:          tk.CreatedAt.String(),
		Description:        tk.Body.Description,
		AcceptanceCriteria: tk.Body.AcceptanceCriteria,
		DefinitionOfDone:   tk.Body.DefinitionOfDone,
		ImplementationPlan: tk.Body.ImplementationPlan,
		Notes:              tk.Body.Notes,
		Comments:           tk.Body.Comments,
		Summary:            tk.Body.Summary,
		References:         ticketReferences(tk.References),
		NextStatuses:       ticket.PermittedTransitions(tk.Status),
		Path:               tk.Path,
		Revision:           tk.Revision,
	}
	return ticketResult(out, nil)
}

// --- ticket_ready -----------------------------------------------------------

type TicketReadyTool struct{ *TicketCore }

type ticketReadyArgs struct {
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

func (t *TicketReadyTool) Name() string { return "ticket_ready" }
func (t *TicketReadyTool) Description() string {
	return i18n.D("tool.ticket_ready.description", "List the tickets that an actor can pick up now. This tool only reads, and it changes nothing. A ready ticket has the status ready, no live claim, and every dependency done. The rows, offset, and limit have the same shape as ticket_list.")
}
func (t *TicketReadyTool) ToolGroupName() string { return "ticket" }
func (t *TicketReadyTool) Schema() json.RawMessage {
	return mustSchema(map[string]any{"type": "object", "properties": ticketPagingProps()})
}

func (t *TicketReadyTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in ticketReadyArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in)
	}
	s, err := t.open()
	if err != nil {
		return ticketResult(nil, err)
	}
	ts, err := s.Ready(ctx)
	if err != nil {
		return ticketResult(nil, err)
	}
	return ticketResult(ticketPageOf(ts, in.Offset, in.Limit), nil)
}

// --- ticket_check -----------------------------------------------------------

type TicketCheckTool struct{ *TicketCore }

type ticketCheckArgs struct {
	Strict bool `json:"strict"`
}

// ticketFinding carries the message alongside the four recorded keys.
// git-ticket's own Finding JSON drops the message by contract; a model
// acting on a finding wants the human text too.
type ticketFinding struct {
	Code    string `json:"code"`
	File    string `json:"file,omitempty"`
	Ticket  string `json:"ticket,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message,omitempty"`
}

func ticketFindings(fs []ticket.Finding) []ticketFinding {
	out := make([]ticketFinding, 0, len(fs))
	for _, f := range fs {
		out = append(out, ticketFinding{Code: f.Code, File: f.File, Ticket: f.Ticket, Field: f.Field, Message: f.Message})
	}
	return out
}

func (t *TicketCheckTool) Name() string { return "ticket_check" }
func (t *TicketCheckTool) Description() string {
	return i18n.D("tool.ticket_check.description", "Validate the ticket store, and report the findings as JSON. This tool only reads, and it changes nothing. An error fails the store, and a warning does not. Set strict to true, and ok is then false on warnings too. Each entry gives a stable code, the file, the ticket, and a message.")
}
func (t *TicketCheckTool) ToolGroupName() string { return "ticket" }
func (t *TicketCheckTool) Schema() json.RawMessage {
	return mustSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"strict": map[string]any{"type": "boolean", "description": "Also fail on warnings, as the CI gate does."},
		},
	})
}

func (t *TicketCheckTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var in ticketCheckArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in)
	}
	s, err := t.open()
	if err != nil {
		return ticketResult(nil, err)
	}
	rep, err := s.Check(ctx)
	if err != nil {
		return ticketResult(nil, err)
	}
	ok := len(rep.Errors) == 0 && (!in.Strict || len(rep.Warnings) == 0)
	return ticketResult(map[string]any{
		"ok":       ok,
		"strict":   in.Strict,
		"errors":   ticketFindings(rep.Errors),
		"warnings": ticketFindings(rep.Warnings),
	}, nil)
}
