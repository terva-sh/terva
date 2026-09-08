package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/core"
)

// spawnClaimExpiry bounds a claim made on a subagent's behalf.
//
// A claim on behalf of a process is different from a claim a person makes. The
// holder cannot release it if it dies, and a crashed subagent would otherwise
// hold a ticket forever. An expiry turns that litter into a lapse, so the worst
// case is a ticket that frees itself late rather than one nobody can pick up.
//
// Two hours is long enough for real work and short enough that a dead claim
// clears within a session. The supervisor does not renew it, so a subagent that
// outlives the window keeps working while the claim lapses. That is the right
// trade: the claim is a coordination hint, not a lock.
const spawnClaimExpiry = 2 * time.Hour

// TicketCoreFor returns the ticket core backing reg's ticket tools, or nil when
// reg carries none. The host wires swarm_spawn from the built registry, which
// is the only place the core exists, and a session with no store simply hands
// back nil so a ticket spawn refuses cleanly.
func TicketCoreFor(reg core.Registry) *TicketCore {
	if reg == nil {
		return nil
	}
	for _, t := range reg {
		if h, ok := t.(ticketCoreCarrier); ok {
			if tc := h.ticketCore(); tc != nil {
				return tc
			}
		}
	}
	return nil
}

// spawnTicket reads a ticket and reports why it cannot be handed to a subagent.
// It returns the ticket and an empty reason when the handover may proceed.
//
// The gate is the store's own Readiness verdict rather than a second definition
// of claimable written here. Readiness already means the status is ready, no
// live claim holds it, and every dependency is satisfied. Restating those rules
// in this package would give the repository two answers that drift apart.
//
// The reason is prose, because it goes back to a model that has to decide what
// to do instead. A bare "not ready" would send it guessing.
func (c *TicketCore) spawnTicket(ctx context.Context, ref string) (*ticket.Ticket, string, error) {
	s, err := c.open()
	if err != nil {
		return nil, "", err
	}
	tk, err := s.Get(ctx, ref)
	if err != nil {
		return nil, "", err
	}
	all, err := s.ReadinessWith(ctx, ticket.ReadyOptions{})
	if err != nil {
		return nil, "", err
	}
	r := all[tk.ID]
	if r.Ready {
		return tk, "", nil
	}
	return nil, spawnTicketRefusal(tk, r), nil
}

// spawnTicketRefusal turns a not-ready verdict into a sentence that names the
// next action. Readiness says a ticket is not ready without saying which of
// several reasons applies, so this reads the ticket's own state for the cases
// that are not about dependencies.
func spawnTicketRefusal(tk *ticket.Ticket, r ticket.Readiness) string {
	id := tk.ID
	switch strings.ToLower(strings.TrimSpace(tk.Status)) {
	case "draft":
		return fmt.Sprintf("%s is still a draft. A person promotes a draft to ready, so ask the user before you assign it.", id)
	case "done", "archived":
		return fmt.Sprintf("%s is already %s. Reopen it with ticket_transition before you assign it.", id, tk.Status)
	case "blocked":
		return fmt.Sprintf("%s is blocked. Move it out of blocked with ticket_transition before you assign it.", id)
	}
	if tk.Claim != nil && tk.Claim.Actor != "" {
		return fmt.Sprintf("%s is already claimed by %s. Wait for that claim to lapse, or release it with ticket_claim, before you assign it.", id, tk.Claim.Actor)
	}
	if len(r.Missing) > 0 {
		return fmt.Sprintf("%s depends on %s, which no ticket in this store claims. Fix the dependency before you assign it.", id, strings.Join(r.Missing, ", "))
	}
	if len(r.Blocking) > 0 {
		return fmt.Sprintf("%s depends on %s, which is not done yet. Finish that first, or assign that instead.", id, strings.Join(r.Blocking, ", "))
	}
	if len(r.BlockingChildren) > 0 {
		return fmt.Sprintf("%s waits on its children %s. Assign a child instead.", id, strings.Join(r.BlockingChildren, ", "))
	}
	return fmt.Sprintf("%s is not ready to be worked (status %s). Only a ready ticket with its dependencies met can be assigned.", id, tk.Status)
}

// claimForAgent claims the ticket for the subagent, not for the host.
//
// The actor rides on the apply rather than on the core, which is what makes a
// claim on another actor's behalf possible without touching the identity the
// host's own writes record. The subagent then signs its notes with the same id,
// because build.ticketActor reads TERVA_SWARM_AGENT_ID out of the child
// environment, so the claim and the work agree about who is doing it.
func (c *TicketCore) claimForAgent(ctx context.Context, ref, agentID, worktree string) error {
	s, err := c.open()
	if err != nil {
		return err
	}
	cur, err := s.Get(ctx, ref)
	if err != nil {
		return err
	}
	_, err = s.Apply(ctx, ref, ticket.ClaimTicket{
		Worktree:  worktree,
		Session:   agentID,
		ExpiresIn: spawnClaimExpiry,
	}, ticket.ApplyOptions{
		IfRevision: cur.Revision,
		Actor:      ticket.Actor{ID: "agent:terva/" + agentID, Name: agentID},
	})
	return err
}

// refuseSubagentClosure reports why this process may not move a ticket to a
// terminal status, or empty when it may.
//
// A sub-agent works a ticket and never closes one. Whether the work is finished
// is a judgement about the report, and it belongs to the actor that can read
// that report against the criteria. That is the dispatcher, not the child that
// produced it. No actor approves its own output.
//
// The tool stays in the registry and refuses, rather than being removed. An
// absent tool teaches nothing: a child cannot tell a capability this host lacks
// from one it is being denied, so it retries or invents a way around. A refusal
// that names the reason and the next action ends the question.
func (c *TicketCore) refuseSubagentClosure(ref, status string) string {
	if c.SubagentID == "" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "archived":
	default:
		// Every other move is ordinary work. A sub-agent may park a ticket it
		// cannot finish, and blocked is how it says so.
		return ""
	}
	return fmt.Sprintf("a sub-agent does not close a ticket, so %s stays open. You hold the claim on it. Record what you did with ticket_comment, and report against each acceptance criterion. The dispatcher that started you reads that report and decides whether to close it.", ref)
}

// ticketBrief renders a ticket as the self-contained brief a subagent needs.
//
// A subagent starts with no conversation context, which is the gap this fills.
// The ticket already holds what a briefing would have to repeat: what the work
// is, how it should be done, what counts as finished, and what to read first.
//
// The criteria arrive as text rather than as checkboxes. A subagent cannot tick
// them, because closure belongs to the dispatcher, and a checkbox invites it to
// try. The heading levels start at H2 so the brief nests under the task text.
func ticketBrief(tk *ticket.Ticket) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Ticket %s: %s\n\n", tk.ID, tk.Title)
	fmt.Fprintf(&b, "Type %s, priority %s.", tk.Type, tk.Priority)
	if len(tk.Labels) > 0 {
		fmt.Fprintf(&b, " Labels: %s.", strings.Join(tk.Labels, ", "))
	}
	b.WriteString("\n")
	section(&b, "Description", tk.Body.Description)
	section(&b, "Implementation plan", tk.Body.ImplementationPlan)
	section(&b, "Definition of done", tk.Body.DefinitionOfDone)
	if items := ticket.Checklist(tk.Body.AcceptanceCriteria); len(items) > 0 {
		b.WriteString("\n### Acceptance criteria\n\n")
		for i, it := range items {
			state := "open"
			if it.Checked {
				state = "already met"
			}
			fmt.Fprintf(&b, "%d. %s (%s)\n", i+1, strings.TrimSpace(it.Text), state)
		}
	}
	if len(tk.References) > 0 {
		b.WriteString("\n### References\n\n")
		for _, r := range tk.References {
			if r.Path != nil && strings.TrimSpace(*r.Path) != "" {
				fmt.Fprintf(&b, "- %s (%s)\n", r.Ref, strings.TrimSpace(*r.Path))
				continue
			}
			fmt.Fprintf(&b, "- %s\n", r.Ref)
		}
	}
	b.WriteString("\nYou hold the claim on this ticket. Do not close it and do not tick a criterion. Report what you did against each criterion, and the dispatcher decides.\n")
	return b.String()
}

// spawnSchema decides the report contract for a ticket spawn. An explicit
// deliverable_schema wins, because a caller who wrote one was specific about the
// report it wants, and the derivation from the criteria is the convenience for
// the caller who wrote none.
func spawnSchema(explicit json.RawMessage, tk *ticket.Ticket) json.RawMessage {
	if len(explicit) > 0 {
		return explicit
	}
	return criteriaSchema(tk)
}

// criteriaSchema turns the ticket's acceptance criteria into the contract the
// sub-agent's report must satisfy, and returns nil when the ticket has none.
//
// One entry per criterion, each carrying the criterion text, whether the work
// met it, and the evidence. That shape makes the dispatcher's review a
// structured check rather than a reading of prose: the criteria come back in
// the same order they went out, and a criterion the child skipped is missing
// rather than merely unmentioned.
//
// The top level is an object because a provider tool schema must be one, and
// the array hangs off a single "criteria" property.
func criteriaSchema(tk *ticket.Ticket) json.RawMessage {
	items := ticket.Checklist(tk.Body.AcceptanceCriteria)
	if len(items) == 0 {
		return nil
	}
	lines := make([]string, 0, len(items))
	for i, it := range items {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, strings.TrimSpace(it.Text)))
	}
	raw, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"criteria": map[string]any{
				"type": "array",
				"description": "One entry for each acceptance criterion of " + tk.ID +
					", in this order:\n" + strings.Join(lines, "\n"),
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"criterion": map[string]any{"type": "string", "description": "The text of the acceptance criterion."},
						"met":       map[string]any{"type": "boolean", "description": "Give true when your work satisfies this criterion."},
						"evidence":  map[string]any{"type": "string", "description": "How you know. Give a command that passes, a path you changed, or a short reason."},
					},
					"required": []string{"criterion", "met", "evidence"},
				},
			},
		},
		"required": []string{"criteria"},
	})
	if err != nil {
		// The map is built here from strings, so this cannot fail in practice.
		// Returning nil degrades to a prose report rather than failing the spawn.
		return nil
	}
	return raw
}

func section(b *strings.Builder, heading, body string) {
	if strings.TrimSpace(body) == "" {
		return
	}
	fmt.Fprintf(b, "\n### %s\n\n%s\n", heading, strings.TrimSpace(body))
}
