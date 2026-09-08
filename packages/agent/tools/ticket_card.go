package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/core"
)

// ticketCardRefresh bounds how often the card rescans the store when nothing
// this session did invalidated it.
//
// A write through the ticket_* tools calls Invalidate and the next render is
// exact. This interval is only the catch-up path for a write terva did not
// make: the git ticket CLI, another agent in another worktree, or a merge that
// moved files under us. Those go unnoticed until the interval lapses, which is
// the staleness the design accepts in exchange for not scanning every turn.
const ticketCardRefresh = 30 * time.Second

// ticketCardEscaper neutralizes the tag-like sequences a ticket title could use
// to break the card frame or forge a system block. A title is authored text and
// reaches the model verbatim, so it gets the same treatment the task card gives
// a task title. Targeted rather than a blanket angle-bracket escape, so an
// ordinary < or > in a title survives.
var ticketCardEscaper = strings.NewReplacer(
	"</ticket-context", "<\\/ticket-context",
	"<ticket-context", "<\\ticket-context",
	"<system-reminder", "<\\system-reminder",
	"</system-reminder", "<\\/system-reminder",
)

// TicketCard renders the per-turn ticket context card: the ticket this session
// claims, and how much work stands behind it.
//
// It renders NOTHING in two cases, and both are deliberate. A workspace with no
// .tickets/ store pays no context cost at all, which is the point of gating the
// whole ticket surface on the store. A session that claims nothing pays none
// either, because a card that only counts other people's work is a standing tax
// on every turn for a session that is not working a ticket.
//
// That second rule also pays for the first half of this type. The scan only
// ever runs for a session holding a claim, so the cache below protects a cost
// that most sessions never incur.
type TicketCard struct {
	// CWD is where the store is discovered from, as the ticket tools do it.
	CWD string
	// Session returns this session's id, which is what a claim records at
	// schema 3. It is a func because the id arrives after the tools are built
	// and changes on resume, fork, and new.
	Session func() string
	// Now is the clock, overridable in a test. nil means time.Now.
	Now func() time.Time

	mu    sync.Mutex
	card  string
	at    time.Time
	fresh bool
}

// TicketCardFor returns the ticket card a built registry carries, bound to the
// session id that names a claim.
//
// One helper because three hosts wire this, and the survivor bugs this feature
// had to guard against all came from a host doing the wiring its own way. It
// returns nil for a workspace with no store, and a nil card renders nothing.
func TicketCardFor(reg core.Registry, session func() string) *TicketCard {
	tc := TicketCoreFor(reg)
	if tc == nil || tc.Card == nil {
		return nil
	}
	if session != nil {
		tc.Card.Session = session
	}
	return tc.Card
}

// Invalidate drops the cached card, so the next render rescans. Every write
// through the ticket_* tools calls this.
func (c *TicketCard) Invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.fresh = false
	c.mu.Unlock()
}

func (c *TicketCard) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Ephemeral is the model-facing card for the per-turn ephemeral tail, framed as
// a clearly attributed block. It returns the empty string when there is nothing
// to say, and the caller then omits the block entirely.
func (c *TicketCard) Ephemeral() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if c.fresh && now.Sub(c.at) < ticketCardRefresh {
		return c.frame(c.card)
	}
	c.card = c.render()
	c.at = now
	c.fresh = true
	return c.frame(c.card)
}

func (c *TicketCard) frame(card string) string {
	if card == "" {
		return ""
	}
	return "<ticket-context>\n" + ticketCardEscaper.Replace(card) + "\n</ticket-context>"
}

// render scans the store and builds the card body, or returns empty for the two
// cases that render nothing. The caller holds c.mu.
func (c *TicketCard) render() string {
	if c.Session == nil {
		return ""
	}
	sess := strings.TrimSpace(c.Session())
	if sess == "" {
		return ""
	}
	// No store means no card, and no cost. Discover fails for a workspace that
	// has none, which is the overwhelming majority of them.
	s, err := ticket.Discover(c.CWD)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	all, err := s.List(ctx, ticket.Filter{All: true})
	if err != nil {
		return ""
	}
	held := claimedBySession(all, sess, c.now())
	if held == nil {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "claimed: %s %s", held.ID, strings.TrimSpace(held.Title))
	// The queue counts follow the claim, never lead it. They are context for the
	// ticket in hand, not a work queue the model should go shopping in.
	ready, drafts := queueDepth(ctx, s, all)
	fmt.Fprintf(&b, "\nqueue: %d ready, %d draft", ready, drafts)
	return b.String()
}

// claimedBySession finds the ticket this session holds. It matches on the
// claim's session id rather than its actor, because two sessions of the same
// persona share an actor and would otherwise each claim the other's work.
//
// An expired claim does not count. A claim lapses rather than being released
// when the holder dies, so a lapsed one names work nobody is doing.
func claimedBySession(all []*ticket.Ticket, sess string, now time.Time) *ticket.Ticket {
	for _, tk := range all {
		if tk == nil || tk.Claim == nil || tk.Claim.Session == nil {
			continue
		}
		if strings.TrimSpace(*tk.Claim.Session) != sess {
			continue
		}
		if tk.Claim.Expired(now) {
			continue
		}
		return tk
	}
	return nil
}

// queueDepth counts what stands behind the claimed ticket: the tickets an actor
// could pick up now, and the drafts waiting on a person to promote them.
//
// The draft count comes from the list already in hand. The ready count needs the
// store's own readiness verdict, because ready is not a status: it also means no
// live claim and every dependency done.
func queueDepth(ctx context.Context, s *ticket.Store, all []*ticket.Ticket) (ready, drafts int) {
	for _, tk := range all {
		if tk != nil && tk.Status == "draft" {
			drafts++
		}
	}
	rs, err := s.ReadyWith(ctx, ticket.ReadyOptions{})
	if err != nil {
		return 0, drafts
	}
	return len(rs), drafts
}
