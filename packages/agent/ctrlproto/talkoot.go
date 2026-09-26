package ctrlproto

import (
	"context"
	"time"

	"terva.sh/terva/packages/core"
)

// Talkoot: a persistent agent team.
//
// A talkoot is a roster of members, a sealed room that records every envelope
// between them, and a router that wakes each member with its envelopes. The
// daemon that runs one owns its room, and these verbs are how a person's
// client lists, reads, starts, edits, and steers it. A member never reaches the
// router through these verbs. It reaches the router through its own team
// tools, which carry its identity. See docs/proposals/talkoot.md.
//
// TalkootController is OPTIONAL and gated by [GroupTalkoot]. A carrier that
// runs no talkoots does not implement it, and a verb answers CodeUnsupported.
//
// 🔑 The by fields name the person a line records. The connection carries a
// capability mask and no identity, so the caller states by and the daemon
// records it. It attributes a change. It does not authorize one: [CapSteer]
// does that.
type TalkootController interface {
	// Talkoots lists every talkoot on this host, running here or not.
	Talkoots(ctx context.Context) ([]TalkootSummary, error)
	// Talkoot returns a talkoot this daemon runs: its roster, each member's
	// status, and the deliveries that wait.
	Talkoot(ctx context.Context, p TalkootRef) (TalkootView, error)
	// TalkootRoom returns a page of the room, oldest line first.
	TalkootRoom(ctx context.Context, p TalkootRoomParams) (TalkootRoomPage, error)
	// CreateTalkoot writes a new talkoot from the text of its talkoot.md, and
	// starts it. Its home must be this daemon's workspace.
	CreateTalkoot(ctx context.Context, p TalkootCreateParams) (TalkootView, error)
	// UpdateTalkoot replaces a running talkoot's roster.
	UpdateTalkoot(ctx context.Context, p TalkootUpdateParams) (TalkootView, error)
	// PostTalkoot posts a person's message to the team.
	PostTalkoot(ctx context.Context, p TalkootPostParams) (TalkootEnvelope, error)
	// PauseTalkoot pauses a member, a chain, or the whole talkoot.
	PauseTalkoot(ctx context.Context, p TalkootPauseParams) error
	// ResumeTalkoot lifts a pause, and releases the deliveries it held.
	ResumeTalkoot(ctx context.Context, p TalkootResumeParams) error
}

// TalkootSummary is one talkoot in a list.
type TalkootSummary struct {
	ID   string `json:"id"`
	Home string `json:"home,omitempty"`
	// Running is true when this daemon runs the talkoot. A talkoot homed in
	// another directory runs in the daemon of that directory.
	Running bool `json:"running"`
	// Problem says why a talkoot does not run, when it should.
	Problem string `json:"problem,omitempty"`
}

// TalkootListResult is the talkoot.list reply.
type TalkootListResult struct {
	Talkoots []TalkootSummary `json:"talkoots"`
}

// TalkootRef names one talkoot.
//
// The id travels in params, not in the frame's sess field, for the reason a
// workflow run's does: sess is a session handle, and a talkoot is not one.
type TalkootRef struct {
	ID string `json:"id"`
}

// TalkootView is a running talkoot.
type TalkootView struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Title           string  `json:"title,omitempty"`
	Home            string  `json:"home"`
	BudgetUSDPerDay float64 `json:"budget_usd_per_day,omitempty"`
	// Text is the talkoot.md the roster came from. An editor changes it and
	// sends it back with talkoot.update.
	Text    string          `json:"text,omitempty"`
	Members []TalkootMember `json:"members"`
	// Held names each member with a delivery that waits for a resume or a
	// working slot.
	Held []string `json:"held,omitempty"`
}

// TalkootMember is one member: what the roster says, and what the router
// reports.
type TalkootMember struct {
	ID              string  `json:"id"`
	Role            string  `json:"role"`
	Title           string  `json:"title,omitempty"`
	Persona         string  `json:"persona,omitempty"`
	Driver          string  `json:"driver,omitempty"`
	Model           string  `json:"model,omitempty"`
	Tier            string  `json:"tier,omitempty"`
	Posture         string  `json:"posture,omitempty"`
	Workspace       string  `json:"workspace,omitempty"`
	Reviewer        bool    `json:"reviewer,omitempty"`
	BudgetUSDPerDay float64 `json:"budget_usd_per_day,omitempty"`
	TurnsPerDay     int     `json:"turns_per_day,omitempty"`
	// Session is the member's session id, once its first delivery made one.
	Session string              `json:"session,omitempty"`
	Status  TalkootMemberStatus `json:"status"`
}

// TalkootMemberStatus is a member's state as the router sees it. Spend and
// turns count the current day.
type TalkootMemberStatus struct {
	Member   string  `json:"member"`
	Working  bool    `json:"working,omitempty"`
	Paused   string  `json:"paused,omitempty"`
	SpendUSD float64 `json:"spend_usd,omitempty"`
	Turns    int     `json:"turns,omitempty"`
}

// TalkootCreateParams is the talkoot.create payload. Text is the whole
// talkoot.md: YAML frontmatter, then the charter.
type TalkootCreateParams struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// TalkootUpdateParams is the talkoot.update payload. Text replaces the whole
// talkoot.md. The id and the home cannot change.
type TalkootUpdateParams struct {
	ID   string `json:"id"`
	By   string `json:"by"`
	Text string `json:"text"`
}

// TalkootPostParams is the talkoot.post payload. An empty To reaches the
// coordinator.
type TalkootPostParams struct {
	ID     string   `json:"id"`
	By     string   `json:"by"`
	To     []string `json:"to,omitempty"`
	Body   string   `json:"body"`
	Refs   []string `json:"refs,omitempty"`
	Thread string   `json:"thread,omitempty"`
}

// TalkootRoomParams pages the room backward. A zero Before reads the newest
// page. A zero Limit takes the server's default.
type TalkootRoomParams struct {
	ID     string `json:"id"`
	Before int    `json:"before,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// TalkootRoomPage is a page of the room, oldest line first. Next is the Before
// that reads the page before this one, and it is 0 at the start of the room.
type TalkootRoomPage struct {
	Lines []TalkootLine `json:"lines"`
	Next  int           `json:"next,omitempty"`
	Total int           `json:"total"`
}

// TalkootPauseParams pauses one member, one chain, or, with neither, the
// whole talkoot.
type TalkootPauseParams struct {
	ID     string `json:"id"`
	By     string `json:"by"`
	Member string `json:"member,omitempty"`
	Chain  string `json:"chain,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// TalkootResumeParams lifts the pause that a TalkootPauseParams of the same
// scope set, or a guard's.
type TalkootResumeParams struct {
	ID     string `json:"id"`
	By     string `json:"by"`
	Member string `json:"member,omitempty"`
	Chain  string `json:"chain,omitempty"`
}

// TalkootEnvelope is one message in a talkoot. The daemon builds From and
// Chain. A sender names neither.
type TalkootEnvelope struct {
	ID      string       `json:"id"`
	Talkoot string       `json:"talkoot"`
	From    string       `json:"from"`
	To      []string     `json:"to"`
	Kind    string       `json:"kind"`
	Body    string       `json:"body"`
	Refs    []string     `json:"refs,omitempty"`
	Thread  string       `json:"thread,omitempty"`
	ReplyTo string       `json:"reply_to,omitempty"`
	Chain   TalkootChain `json:"chain"`
	At      time.Time    `json:"at"`
}

// TalkootChain ties an envelope to the person's post that began its work.
// Root is that post's envelope id, and Hops counts the member envelopes since.
type TalkootChain struct {
	Root string `json:"root"`
	Hops int    `json:"hops"`
}

// TalkootLine is one line of the room. Type says which fields it sets:
// envelope, turn, guard, resume, delivery, read, seat, or roster. A line that
// did not parse reads as damaged. The seal that the room keeps on each line
// stays in the room.
type TalkootLine struct {
	Type     string           `json:"type"`
	At       time.Time        `json:"at"`
	Envelope *TalkootEnvelope `json:"envelope,omitempty"`
	Member   string           `json:"member,omitempty"`
	Chain    string           `json:"chain,omitempty"`
	CostUSD  float64          `json:"cost_usd,omitempty"`
	Guard    string           `json:"guard,omitempty"`
	Action   string           `json:"action,omitempty"`
	Reason   string           `json:"reason,omitempty"`
	By       string           `json:"by,omitempty"`
	SpendUSD float64          `json:"spend_usd,omitempty"`
	Ref      string           `json:"ref,omitempty"`
	Notes    int              `json:"notes,omitempty"`
}

// TalkootEvent is the body of the talkoot_* events on a [TalkootAddr]
// address.
type TalkootEvent struct {
	ID string `json:"id"`
	// Line is set on [EventTalkootEnvelope] and [EventTalkootRoster].
	Line *TalkootLine `json:"line,omitempty"`
	// Members is set on [EventTalkootStatus]: every member's status, in
	// roster order.
	Members []TalkootMemberStatus `json:"members,omitempty"`
}

// TalkootEnvelopeEvent builds an [EventTalkootEnvelope] event.
func TalkootEnvelopeEvent(id string, l TalkootLine) Event {
	return Event{WireEvent: core.WireEvent{Type: EventTalkootEnvelope}, Talkoot: &TalkootEvent{ID: id, Line: &l}}
}

// TalkootRosterEvent builds an [EventTalkootRoster] event.
func TalkootRosterEvent(id string, l TalkootLine) Event {
	return Event{WireEvent: core.WireEvent{Type: EventTalkootRoster}, Talkoot: &TalkootEvent{ID: id, Line: &l}}
}

// TalkootStatusEvent builds an [EventTalkootStatus] event.
func TalkootStatusEvent(id string, members []TalkootMemberStatus) Event {
	return Event{WireEvent: core.WireEvent{Type: EventTalkootStatus}, Talkoot: &TalkootEvent{ID: id, Members: members}}
}

// TalkootsChangedEvent builds an [EventTalkootsChanged] signal.
func TalkootsChangedEvent() Event {
	return Event{WireEvent: core.WireEvent{Type: EventTalkootsChanged}}
}
