package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/tools"
)

// Ticket-backed handoffs (TKT-01M396R1T). A seated member's ticket writes
// carry its own actor, and a handoff that names a ticket moves the ticket's
// claim to the recipient. tools/ticket_member.go holds the store side.

// talkootTicketMember is a member's ticket identity in run.
func (w *Workspace) talkootTicketMember(run *talkootRun, member string) (tools.TicketMember, bool) {
	m, ok := memberOf(*run.roster.Load(), member)
	if !ok {
		return tools.TicketMember{}, false
	}
	driver := memberDriver(m)
	w.talkoot.mu.Lock()
	sess := run.seats[member]
	w.talkoot.mu.Unlock()
	return tools.TicketMember{ID: m.ID, Actor: tools.MemberActor(driver, m.ID), Reviewer: m.Reviewer, Session: sess}, true
}

// memberDriver is m's driver, native when the roster names none.
func memberDriver(m talkoot.Member) string {
	if m.Driver == "" {
		return talkoot.DriverNative
	}
	return m.Driver
}

// sessionTicketMember is the member a session is seated as now, which its
// ticket tools read on each call. A revoked seat answers no member.
func (w *Workspace) sessionTicketMember(sessID string) (tools.TicketMember, bool) {
	w.talkoot.mu.Lock()
	b := w.talkoot.seats[sessID]
	w.talkoot.mu.Unlock()
	if b == nil {
		return tools.TicketMember{}, false
	}
	b.mu.RLock()
	revoked := b.revoked
	b.mu.RUnlock()
	if revoked {
		return tools.TicketMember{}, false
	}
	return w.talkootTicketMember(b.run, b.member)
}

// seatNoTicket reports whether the session that holds seat b runs with its
// ticket tools turned off.
func (w *Workspace) seatNoTicket(b *seatBinding) bool {
	w.talkoot.mu.Lock()
	var sessID string
	for id, sb := range w.talkoot.seats {
		if sb == b {
			sessID = id
			break
		}
	}
	w.talkoot.mu.Unlock()
	if s := w.existing(sessID); s != nil {
		return s.argsSnapshot().NoTicket
	}
	return false
}

// ticketHandoff is a handoff's claim move, checked before the send.
type ticketHandoff struct {
	tickets           []tools.HandoffTicket
	sender, recipient tools.TicketMember
}

// checkTicketHandoff checks a handoff that names tickets before the router
// sees it. It returns nil when there is nothing to move: the envelope is not
// a handoff, it names no ticket, or the workspace has no ticket store, which
// keeps ticket references as text. Tickets turned off, in the configuration
// or for the sender's session with --no-ticket, keep them as text too.
//
// ⚠️ It runs before Send takes the seat's lock, because it takes the talkoot
// lock, and an unseat revokes a seat while it holds that lock.
func (w *Workspace) checkTicketHandoff(ctx context.Context, b *seatBinding, o talkoot.Outgoing) (*ticketHandoff, error) {
	if o.Kind != talkoot.KindHandoff {
		return nil, nil
	}
	var refs []string
	for _, r := range o.Refs {
		if id, ok := strings.CutPrefix(r, "ticket:"); ok && strings.TrimSpace(id) != "" {
			refs = append(refs, strings.TrimSpace(id))
		}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	if !tools.TicketStoreAvailable(w.cwd) || !config.TicketsEnabled(w.cwd) || w.seatNoTicket(b) {
		return nil, nil
	}
	if len(o.To) != 1 {
		return nil, fmt.Errorf("talkoot: a handoff that names a ticket moves the ticket's claim, so it goes to one member, not %d", len(o.To))
	}
	sender, ok := w.talkootTicketMember(b.run, b.member)
	if !ok {
		return nil, errSeatRevoked
	}
	recipient, ok := w.talkootTicketMember(b.run, o.To[0])
	if !ok {
		return nil, fmt.Errorf("talkoot: %q is not a member of %s", o.To[0], b.run.id)
	}
	// 🔑 An external member's actor would be agent:<driver>/<id>, the form a
	// real session of that harness writes under, so a claim could pass
	// between the two. Only native members hold claims until the bridge
	// (TKT-01M396QZ) gives external members a ticket identity.
	for _, id := range []string{b.member, o.To[0]} {
		if m, _ := memberOf(*b.run.roster.Load(), id); memberDriver(m) != talkoot.DriverNative {
			return nil, fmt.Errorf("talkoot: %s runs on the %s driver, and a ticket claim moves only between native members; hand the work on without the ticket reference", id, memberDriver(m))
		}
	}
	tks, err := tools.CheckHandoff(ctx, w.cwd, refs, sender, recipient)
	if errors.Is(err, tools.ErrNoTicketStore) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("talkoot: %w", err)
	}
	return &ticketHandoff{tickets: tks, sender: sender, recipient: recipient}, nil
}

// move moves the claims after the handoff went. A failure comes back as a
// ClaimNotMovedError, because the envelope is already in the room.
func (h *ticketHandoff) move(ctx context.Context, cwd string, e talkoot.Envelope) error {
	if h == nil {
		return nil
	}
	if err := tools.MoveClaims(ctx, cwd, h.tickets, h.sender, h.recipient, e.ID); err != nil {
		return &tools.ClaimNotMovedError{Envelope: e, Err: err}
	}
	return nil
}
