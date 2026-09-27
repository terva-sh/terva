package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/agent/talkoot"
)

// Ticket-backed handoffs for Talkoot members (TKT-01M396R1T). A member's
// ticket writes carry its own actor, each member that takes a claim is
// recorded on the ticket, and no member closes a ticket it worked on.
//
// 🔑 The store records no author for a change. terva records each member that
// takes a ticket's claim to work on it, on a note line, and every one of them
// is an author. A member that hands the work on is still an author, so it
// cannot close the ticket later through a teammate. A reviewer that receives
// the claim by a handoff holds it to review, not to work, so it is not
// recorded, and it can close the ticket.

// TicketMember is a Talkoot member as the ticket tools see it.
type TicketMember struct {
	// ID is the member's id in its roster.
	ID string
	// Actor is the identity the member's ticket writes carry.
	Actor string
	// Reviewer is the roster's reviewer flag. Only a reviewer closes a ticket.
	Reviewer bool
	// Session is the member's session, which a claim records. It is empty for
	// a member with no native session.
	Session string
}

// MemberActor is the actor a member's ticket writes carry:
// agent:<driver>/<member>, such as agent:native/jev.
func MemberActor(driver, member string) string {
	return "agent:" + driver + "/" + member
}

// claimHoldersMarker starts the note line that records the members that held
// a ticket's claim.
const claimHoldersMarker = "Claim holders: "

// claimHoldersNote is the note that records actors as claim holders.
func claimHoldersNote(actors ...string) string {
	return claimHoldersMarker + strings.Join(actors, ", ") + ".\n\nterva records each Talkoot member that holds this ticket's claim. None of them may move the ticket to done."
}

// holdersLine returns the actors a claim-holders line lists, and whether
// line is one.
func holdersLine(line string) (string, bool) {
	return strings.CutPrefix(strings.TrimSpace(line), claimHoldersMarker)
}

// ticketAuthors returns the actors that authored the work on a ticket: each
// actor on a claim-holders line.
//
// 🔑 The holder of the claim does not count by itself, because a reviewer
// holds it to review. A reviewer closes only when a line names another
// member, so a line is also what lets a close through. Only terva writes one:
// the ticket write tools refuse a member's arguments that hold one
// (refuseHoldersText),
// and the member's bash rule refuses the git ticket CLI. A line added to the
// ticket file by hand shows in the diff.
func ticketAuthors(tk *ticket.Ticket) map[string]bool {
	out := map[string]bool{}
	for line := range strings.SplitSeq(tk.Body.Notes, "\n") {
		rest, ok := holdersLine(line)
		if !ok {
			continue
		}
		for a := range strings.SplitSeq(strings.TrimSuffix(rest, "."), ",") {
			if a = strings.TrimSpace(a); a != "" {
				out[a] = true
			}
		}
	}
	return out
}

// refuseHoldersText reports why this session's member may not send a ticket
// write with these arguments, or empty when it may. A claim-holders line in
// any string the member wrote would name an author that no claim recorded.
//
// 🔑 It reads every string in the arguments, not the fields that land in
// Notes, because the store parses a "## Notes" heading inside a description
// or a plan as the Notes section.
func (c *TicketCore) refuseHoldersText(raw json.RawMessage) string {
	if _, ok := c.member(); !ok || len(raw) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	if !holdsHoldersLine(v) {
		return ""
	}
	return fmt.Sprintf("a line that starts %q records who held a ticket's claim, and only terva writes one. Word that line another way.", claimHoldersMarker)
}

// holdsHoldersLine reports whether any string in v has a claim-holders line.
func holdsHoldersLine(v any) bool {
	switch v := v.(type) {
	case string:
		for line := range strings.FieldsFuncSeq(v, func(r rune) bool { return r == '\n' || r == '\r' }) {
			if _, ok := holdersLine(line); ok {
				return true
			}
		}
	case []any:
		for _, e := range v {
			if holdsHoldersLine(e) {
				return true
			}
		}
	case map[string]any:
		for _, e := range v {
			if holdsHoldersLine(e) {
				return true
			}
		}
	}
	return false
}

// member returns the seat this session holds now, if any.
func (c *TicketCore) member() (TicketMember, bool) {
	if c == nil || c.Member == nil {
		return TicketMember{}, false
	}
	return c.Member()
}

// refuseMemberClosure reports why this session's member may not move a ticket
// to done or archived, or empty when it may. A session with no seat is not
// refused here.
//
// Only a reviewer that did not author the change closes a ticket. The roster's
// reviewer flag marks a member whose approval closes a review, so a member that
// is neither is refused as well.
func (c *TicketCore) refuseMemberClosure(ctx context.Context, ref, status string) string {
	m, ok := c.member()
	if !ok {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case ticket.StatusDone, ticket.StatusArchived:
	default:
		return ""
	}
	// 🚨 A check that cannot read the ticket refuses. Only a ticket that does
	// not exist goes on to the write, which reports it by name.
	s, err := c.open()
	if err != nil {
		return fmt.Sprintf("the ticket store could not be read to check who authored %s: %v", ref, err)
	}
	tk, err := s.Get(ctx, ref)
	if err != nil {
		var te *ticket.Error
		if errors.As(err, &te) && te.Code == ticket.CodeTicketNotFound {
			return ""
		}
		return fmt.Sprintf("%s could not be read to check who authored it: %v", ref, err)
	}
	if ticketAuthors(tk)[m.Actor] {
		return fmt.Sprintf("you took the claim on %s to work on it, so you authored its change, and no member approves its own work. Hand it to a reviewer member with talkoot_handoff and a ticket:%s reference. The reviewer decides whether it is done.", tk.ID, tk.ID)
	}
	if !m.Reviewer {
		return fmt.Sprintf("only a reviewer member moves a ticket to %s, and you are not one. Hand %s to a reviewer member with talkoot_handoff and a ticket:%s reference.", status, tk.ID, tk.ID)
	}
	// 🔑 A reviewer that holds a claim is not recorded, so a ticket with no
	// recorded author could be the reviewer's own work.
	if len(ticketAuthors(tk)) == 0 {
		return fmt.Sprintf("no member is recorded as having worked on %s, so no review can close it. A member claims it with ticket_claim, does the work, and hands it to a reviewer.", tk.ID)
	}
	// The reviewer that closes is the one the work was handed to.
	if claimActor(tk) != m.Actor {
		return fmt.Sprintf("you do not hold the claim on %s, so it was not handed to you to review. Its author hands it to a reviewer with talkoot_handoff and a ticket:%s reference.", tk.ID, tk.ID)
	}
	return ""
}

// claimActor is the actor that holds tk's claim, or empty when none does.
func claimActor(tk *ticket.Ticket) string {
	if tk.Claim == nil {
		return ""
	}
	return tk.Claim.Actor
}

// refuseMemberCreateClosed reports why this session's member may not file a
// new ticket as done or archived, or empty when it may. A ticket filed closed
// records finished work that no reviewer saw.
func (c *TicketCore) refuseMemberCreateClosed(status string) string {
	if _, ok := c.member(); !ok {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case ticket.StatusDone, ticket.StatusArchived:
		return fmt.Sprintf("a Talkoot member does not file a ticket as %s. File it open, and hand it to a reviewer member with talkoot_handoff and a ticket reference. The reviewer decides whether it is done.", status)
	}
	return ""
}

// claimMutation is a claim, with the claim-holders line when a member takes
// it, as one write. A member that renews a claim it holds adds no line, so a
// reviewer that renews the claim a handoff gave it stays a reviewer.
func (c *TicketCore) claimMutation(ctx context.Context, ref string, claim ticket.ClaimTicket) ticket.Mutation {
	m, ok := c.member()
	if !ok {
		return claim
	}
	if s, err := c.open(); err == nil {
		if tk, err := s.Get(ctx, ref); err == nil && tk.Claim != nil && tk.Claim.Actor == m.Actor {
			return claim
		}
	}
	return ticket.Mutations{claim, ticket.AppendNote{Text: claimHoldersNote(m.Actor)}}
}

// ClaimNotMovedError is a handoff that went, whose claim move failed. The
// envelope is in the room, so the sender must not send it again.
type ClaimNotMovedError struct {
	Envelope talkoot.Envelope
	Err      error
}

func (e *ClaimNotMovedError) Error() string {
	return fmt.Sprintf("handoff %s went, but the ticket claim did not move: %v", e.Envelope.ID, e.Err)
}

func (e *ClaimNotMovedError) Unwrap() error { return e.Err }

// ErrNoTicketStore is CheckHandoff's answer when the directory has no store.
// A handoff then keeps its ticket references as text.
var ErrNoTicketStore = errors.New("no ticket store")

// HandoffTicket is a ticket a handoff moves, as CheckHandoff found it.
type HandoffTicket struct {
	ID string
	// Holder is the actor that held the claim, or empty when none did.
	Holder string
}

// CheckHandoff reports whether sender may hand the tickets refs name to
// recipient. Each ticket must be in the store that cwd finds, open, and
// unclaimed or claimed by the sender. A ticket goes to a reviewer only from
// the member that holds its claim. It returns each ticket once, and
// ErrNoTicketStore when cwd has no store.
func CheckHandoff(ctx context.Context, cwd string, refs []string, sender, recipient TicketMember) ([]HandoffTicket, error) {
	c := &TicketCore{CWD: cwd}
	s, err := c.open()
	if err != nil {
		if !TicketStoreAvailable(cwd) {
			return nil, ErrNoTicketStore
		}
		return nil, err
	}
	var out []HandoffTicket
	seen := map[string]bool{}
	for _, ref := range refs {
		tk, err := s.Get(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("ticket:%s names no ticket in this workspace's store (%v); name a ticket that is there, or hand the work on with a path or a note", ref, err)
		}
		switch tk.Status {
		case ticket.StatusDone, ticket.StatusArchived:
			return nil, fmt.Errorf("%s is %s, so there is no work on it to hand on; hand the work on without the ticket reference", tk.ID, tk.Status)
		}
		if h := claimActor(tk); h != "" && h != sender.Actor {
			return nil, fmt.Errorf("%s is claimed by %s, not by you (%s), so you cannot hand it on; ask its holder, or hand the work on without the ticket reference", tk.ID, tk.Claim.Actor, sender.Actor)
		}
		// 🔑 A reviewer that holds a claim is not recorded as an author. A
		// ticket handed over unclaimed would reach a reviewer with no record
		// of who did the work, so the worker claims it first.
		if recipient.Reviewer && claimActor(tk) == "" {
			return nil, fmt.Errorf("%s has no claim, and %s is a reviewer; claim it with ticket_claim first, so the ticket records who did the work, and then hand it on", tk.ID, recipient.ID)
		}
		// Two references can name one ticket, by its id and by a prefix.
		if seen[tk.ID] {
			continue
		}
		seen[tk.ID] = true
		out = append(out, HandoffTicket{ID: tk.ID, Holder: claimActor(tk)})
	}
	return out, nil
}

// MoveClaims moves the claim on each ticket to the recipient of handoff via,
// in one write per ticket. It records as claim holders the sender, when it
// held the claim, and the recipient, unless either is a reviewer: a reviewer
// holds a claim to review it. A sender that hands on an unclaimed ticket is
// recorded even as a reviewer, since nothing else records who did that work.
// The write carries the recipient's actor, because the claim is the
// recipient's, so a note names the sender and the handoff.
//
// A ticket whose claim changed hands since CheckHandoff is not moved. Any
// other change, such as a note the recipient wrote as its turn began, is not
// a reason to fail.
func MoveClaims(ctx context.Context, cwd string, tks []HandoffTicket, sender, recipient TicketMember, via string) error {
	c := &TicketCore{CWD: cwd}
	s, err := c.open()
	if err != nil {
		return err
	}
	var errs []error
	for _, tk := range tks {
		if err := moveClaim(ctx, s, cwd, tk, sender, recipient, via); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", tk.ID, err))
		}
	}
	return errors.Join(errs...)
}

// moveClaim moves one ticket's claim. It reads the ticket and writes against
// that revision, and reads again when another write lands in between.
func moveClaim(ctx context.Context, s *ticket.Store, cwd string, tk HandoffTicket, sender, recipient TicketMember, via string) error {
	var err error
	for range 3 {
		var cur *ticket.Ticket
		if cur, err = s.Get(ctx, tk.ID); err != nil {
			return err
		}
		if h := claimActor(cur); h != tk.Holder {
			return fmt.Errorf("the claim moved from %q to %q after the handoff was checked", tk.Holder, h)
		}
		claim := ticket.ClaimTicket{Worktree: cwd, Session: recipient.Session, Force: true}
		// The work stays on its branch when it changes hands.
		if cur.Claim != nil && cur.Claim.Branch != nil {
			claim.Branch = *cur.Claim.Branch
		}
		var holders []string
		if tk.Holder == "" || (tk.Holder == sender.Actor && !sender.Reviewer) {
			holders = append(holders, sender.Actor)
		}
		if !recipient.Reviewer {
			holders = append(holders, recipient.Actor)
		}
		note := fmt.Sprintf("%s handed this ticket to %s with Talkoot handoff %s, which moved the claim.", sender.Actor, recipient.Actor, via)
		if len(holders) > 0 {
			note = claimHoldersNote(holders...) + "\n\n" + note
		}
		_, err = s.Apply(ctx, tk.ID, ticket.Mutations{claim, ticket.AppendNote{Text: note}}, ticket.ApplyOptions{
			IfRevision: cur.Revision,
			Actor:      ticket.Actor{ID: recipient.Actor, Name: recipient.ID},
		})
		var te *ticket.Error
		if err == nil || !errors.As(err, &te) || te.Code != ticket.CodeStaleRevision {
			return err
		}
	}
	return err
}
