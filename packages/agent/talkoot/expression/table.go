package expression

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/talkoot"
)

// Signals the table reacts to. Each comes from one kind of room line.
const (
	// SignalToolError counts the failed tool calls in the member's current
	// turn, from tool_error lines.
	SignalToolError = "tool_error"
	// SignalRetry is the attempt of a provider retry in the current turn,
	// from retry lines.
	SignalRetry = "retry"
	// SignalTurnFailed counts the member's failed turns in a row, from turn
	// lines with the failed guard.
	SignalTurnFailed = "turn_failed"
	// SignalRefused is a guard that refused one of the member's sends.
	SignalRefused = "refused"
	// SignalCardWait is a question or an approval of the member's that has
	// waited in the inbox for a row's After.
	SignalCardWait = "card_wait"
	// SignalAnswered is a person's answer to the member's question.
	SignalAnswered = "answered"
	// SignalSent is a message or a handoff the member sent.
	SignalSent = "sent"
	// SignalPickedUp is a handoff the member sent that its recipient took.
	SignalPickedUp = "picked_up"
	// SignalApplied is a proposal of the member's that a person applied.
	SignalApplied = "applied"
	// SignalBudget is the percent of its daily budget a member has spent,
	// from the turn line that ends each of its turns.
	SignalBudget = "budget"
	// SignalWoke is a message that reached the member while it was idle,
	// from a delivery line that says so.
	SignalWoke = "woke"
	// SignalInterrupted is a turn of the member's that something asked to
	// stop, from its turn line.
	SignalInterrupted = "interrupted"
	// SignalReview is a turn of a reviewer's, from the delivery line that
	// started it. Its row holds until the turn's line, and at most its Hold.
	SignalReview = "review"
)

// Row is one row of a table: a signal, the count or the wait at which it
// fires, and the held pose or the beat it shows.
type Row struct {
	Signal string
	// Min is the count that fires a counted signal. Zero fires on the
	// first. Where several rows of one signal fire, the one with the
	// largest Min wins.
	Min int
	// After is how long a card waits before a card_wait row fires.
	After time.Duration
	// Pose and Intensity are the held pose a row shows, for Hold. Beat is
	// the beat it plays. A row sets one of Pose and Beat.
	Pose      string
	Intensity int
	Hold      time.Duration
	Beat      string
}

// DefaultTable is the first engine's table. The proposal's pose and beat
// tables are its source.
var DefaultTable = []Row{
	{Signal: SignalToolError, Min: 3, Pose: PoseFrustrated, Hold: 10 * time.Minute},
	{Signal: SignalToolError, Min: 6, Pose: PoseFrustrated, Intensity: 1, Hold: 10 * time.Minute},
	{Signal: SignalTurnFailed, Min: 1, Pose: PoseFrustrated, Hold: 10 * time.Minute},
	{Signal: SignalTurnFailed, Min: 2, Pose: PoseFrustrated, Intensity: 1, Hold: 10 * time.Minute},
	{Signal: SignalRetry, Min: 1, Pose: PoseWorried, Hold: 5 * time.Minute},
	{Signal: SignalRetry, Min: 3, Pose: PoseWorried, Intensity: 1, Hold: 5 * time.Minute},
	{Signal: SignalRefused, Pose: PoseFrustrated, Hold: 5 * time.Minute},
	{Signal: SignalBudget, Min: 80, Pose: PoseWorried, Hold: 10 * time.Minute},
	{Signal: SignalCardWait, After: 5 * time.Minute, Pose: PoseLookingUp, Intensity: 1},
	{Signal: SignalAnswered, Beat: BeatSlowBlink},
	{Signal: SignalSent, Beat: BeatGlance},
	{Signal: SignalPickedUp, Beat: BeatHappy},
	{Signal: SignalApplied, Beat: BeatHappy},
	{Signal: SignalWoke, Beat: BeatFastBlink},
	{Signal: SignalInterrupted, Beat: BeatFastBlink},
	{Signal: SignalReview, Pose: PoseSkeptical, Hold: time.Hour},
}

// TraceLen bounds each member's trace.
const TraceLen = 64

// maxHandoffs bounds the handoffs the table remembers until a recipient
// takes one. The oldest is forgotten first.
const maxHandoffs = 256

// maxCauseReason bounds the part of a line's reason that a cause quotes.
const maxCauseReason = 80

// Table is the table engine. Its state is each member's mood, built from the
// room lines it observed.
//
// The first table makes no random choice. It keeps the seed so that a table
// that does can replay, and so that the trace can name it.
type Table struct {
	mu      sync.Mutex
	seed    uint64
	rows    []Row
	members map[string]*mood
	// handoffs maps a handoff the table has seen to its sender, until a
	// recipient takes it. order keeps them oldest first.
	handoffs map[string]string
	order    []string
}

// mood is one member's state.
type mood struct {
	// turns counts the member's turn lines, so a cause names the turn.
	turns int
	// errors and tools count the failed tool calls of the current turn, and
	// name up to three of the tools. retry is the highest retry attempt in
	// it. failed counts the failed turns in a row.
	errors int
	tools  []string
	retry  int
	failed int
	// refused counts the current turn's refused sends.
	refused int
	// review says the held pose is the current turn's review, which its
	// turn line ends.
	review bool
	held   Expression
	until  time.Time
	// ended is set once the trace records the end of the held pose.
	ended bool
	// cards maps each open card to when it opened, and whether the trace
	// records its brow yet.
	cards map[string]*card
	trace []Entry
}

type card struct {
	opened time.Time
	raised bool
}

var _ Engine = (*Table)(nil)

// NewTable returns a table engine over rows, with the seed seed. Nil rows
// take DefaultTable.
func NewTable(seed uint64, rows []Row) *Table {
	if rows == nil {
		rows = DefaultTable
	}
	return &Table{seed: seed, rows: slices.Clone(rows), members: map[string]*mood{}, handoffs: map[string]string{}}
}

func (t *Table) Seed() uint64 { return t.seed }

func (t *Table) mood(member string) *mood {
	m := t.members[member]
	if m == nil {
		m = &mood{}
		t.members[member] = m
	}
	return m
}

// match returns the row of signal that n fires, the one with the largest Min
// at or below n.
func (t *Table) match(signal string, n int) (Row, bool) {
	var best Row
	found := false
	for _, r := range t.rows {
		if r.Signal != signal || max(r.Min, 1) > n {
			continue
		}
		if !found || r.Min > best.Min {
			best, found = r, true
		}
	}
	return best, found
}

// Observe implements [Engine].
func (t *Table) Observe(l talkoot.Line) []Beat {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.advance(l.At)
	switch l.Type {
	case talkoot.LineToolError:
		m := t.mood(l.Member)
		m.errors++
		if l.Tool != "" && !slices.Contains(m.tools, l.Tool) && len(m.tools) < 3 {
			m.tools = append(m.tools, l.Tool)
		}
		signal := "tool_error " + l.Tool
		if r, ok := t.match(SignalToolError, m.errors); ok {
			t.hold(l.Member, l.At, signal, r, fmt.Sprintf("turn %d, tool_error x%d (%s)", m.turns+1, m.errors, strings.Join(m.tools, ", ")))
		} else {
			m.note(Entry{At: l.At, Signal: signal})
		}
	case talkoot.LineRetry:
		m := t.mood(l.Member)
		m.retry = max(m.retry, l.Attempt)
		signal := fmt.Sprintf("retry %d", l.Attempt)
		if r, ok := t.match(SignalRetry, m.retry); ok {
			t.hold(l.Member, l.At, signal, r, fmt.Sprintf("turn %d, retry attempt %d (%s)", m.turns+1, l.Attempt, quote(l.Reason)))
		} else {
			m.note(Entry{At: l.At, Signal: signal})
		}
	case talkoot.LineTurn:
		t.turn(l)
		if l.Interrupted {
			return t.beat(l.Member, l.At, "turn interrupted", SignalInterrupted, "", fmt.Sprintf("turn %d was interrupted", t.mood(l.Member).turns))
		}
	case talkoot.LineGuard:
		if l.Action == talkoot.ActionRefused && l.Member != "" && l.Guard != talkoot.GuardDelivery {
			t.mood(l.Member).refused++
			if r, ok := t.match(SignalRefused, 1); ok {
				t.hold(l.Member, l.At, "refused "+l.Guard, r, fmt.Sprintf("guard %s refused a send (%s)", l.Guard, quote(l.Reason)))
			}
		}
	case talkoot.LineCardOpen:
		m := t.mood(l.Member)
		if m.cards == nil {
			m.cards = map[string]*card{}
		}
		m.cards[l.Ref] = &card{opened: l.At}
		e := Entry{At: l.At, Signal: "card_open " + l.Card}
		if r, ok := t.match(SignalCardWait, 1); ok {
			e.Cause = fmt.Sprintf("card %s raises the brow at %s if it is still open", l.Ref, stamp(l.At.Add(r.After)))
		}
		m.note(e)
	case talkoot.LineCardClose:
		m := t.mood(l.Member)
		delete(m.cards, l.Ref)
		signal := "card_close " + l.Card + " " + l.Outcome
		if l.Card == talkoot.CardQuestion && l.Outcome == talkoot.OutcomeAnswered {
			return t.beat(l.Member, l.At, signal, SignalAnswered, "", "question "+l.Ref+" answered")
		}
		m.note(Entry{At: l.At, Signal: signal})
	case talkoot.LineEnvelope:
		return t.envelope(l)
	case talkoot.LineDelivery:
		var out []Beat
		if l.Woke {
			out = t.beat(l.Member, l.At, "woke", SignalWoke, "", "envelope "+l.Ref+" reached it idle")
		}
		if l.Reviewer {
			if r, ok := t.match(SignalReview, 1); ok {
				m := t.mood(l.Member)
				t.hold(l.Member, l.At, "review", r, fmt.Sprintf("turn %d is a review, from envelope %s", m.turns+1, l.Ref))
				m.review = true
			}
		}
		// A delivery that names its chain went to a driver that reports no
		// reads, so the delivery is the pickup.
		if l.Chain != "" {
			out = append(out, t.pickup(l)...)
		}
		return out
	case talkoot.LineRead:
		return t.pickup(l)
	case talkoot.LineRoster:
		if l.Proposal != "" && isMember(l.Proposer) {
			return t.beat(l.Proposer, l.At, "proposal applied", SignalApplied, "", "proposal "+l.Proposal+" applied")
		}
	}
	return nil
}

// turn ends the member's turn. A failed turn counts toward the failed rows.
// A clean turn, one with no failed tool call, no retry, and no refused send,
// clears the held pose: the member recovered.
func (t *Table) turn(l talkoot.Line) {
	m := t.mood(l.Member)
	m.turns++
	defer func() { m.errors, m.tools, m.retry, m.refused = 0, nil, 0, 0 }()
	// A review ends with its turn. A pose the turn's trouble set in its
	// place stays, as the rows below decide.
	if m.review {
		m.review = false
		if m.held.Pose == PoseSkeptical && l.At.Before(m.until) {
			m.held, m.until = Expression{}, time.Time{}
			m.note(Entry{At: l.At, Signal: "review ended", Cause: fmt.Sprintf("turn %d ended, so the review's skeptical clears", m.turns)})
		}
	}
	if l.Guard == talkoot.GuardFailed {
		m.failed++
		if r, ok := t.match(SignalTurnFailed, m.failed); ok {
			t.hold(l.Member, l.At, "turn failed", r, fmt.Sprintf("turn %d failed, x%d in a row (%s)", m.turns, m.failed, quote(l.Reason)))
			return
		}
		m.note(Entry{At: l.At, Signal: "turn failed"})
		return
	}
	m.failed = 0
	if m.errors == 0 && m.retry == 0 && m.refused == 0 && m.held.Pose != "" && l.At.Before(m.until) {
		m.held, m.until = Expression{}, time.Time{}
		m.note(Entry{At: l.At, Signal: "turn clean", Cause: fmt.Sprintf("turn %d ended clean, so the held pose clears", m.turns)})
	}
	t.budget(l)
}

// budget holds worry after a turn that leaves the member past a budget row's
// percent of its daily budget. Each such turn holds it again.
//
// 🔑 The worry is a background reading. A pose the turn's own trouble set,
// such as frustration at failed calls, stays, because it says more about the
// turn. A spent budget pauses the member, and the pause draws its own face,
// so the worry stops short of the whole budget.
func (t *Table) budget(l talkoot.Line) {
	if l.DayBudgetUSD <= 0 || l.DaySpendUSD >= l.DayBudgetUSD {
		return
	}
	m := t.mood(l.Member)
	if m.held.Pose != "" && m.held.Pose != PoseWorried && l.At.Before(m.until) {
		return
	}
	pct := int(100 * l.DaySpendUSD / l.DayBudgetUSD)
	if r, ok := t.match(SignalBudget, pct); ok {
		t.hold(l.Member, l.At, fmt.Sprintf("budget %d%%", pct), r,
			fmt.Sprintf("turn %d brought the day's spend to $%.2f of the member's $%.2f (%d%%)", m.turns, l.DaySpendUSD, l.DayBudgetUSD, pct))
	}
}

// hold sets the member's held pose from row r. The same pose held already
// keeps the stronger form and the later end, so a weaker row never cuts a
// strong pose short. A different pose replaces the held one, even a stronger
// or a longer one: the face shows the newest reading of the member. The trace
// entry names the end, and Advance records it when it comes.
func (t *Table) hold(member string, at time.Time, signal string, r Row, cause string) {
	m := t.mood(member)
	x := Expression{Pose: r.Pose, Intensity: r.Intensity}
	until := at.Add(r.Hold)
	if m.held.Pose == x.Pose && at.Before(m.until) {
		x.Intensity = max(x.Intensity, m.held.Intensity)
		until = maxTime(until, m.until)
	}
	x.Cause = cause + " -> " + poseName(x)
	m.held, m.until, m.ended = x, until, false
	m.note(Entry{At: at, Signal: signal, Pose: x.Pose, Intensity: x.Intensity, Cause: x.Cause + ", until " + stamp(until)})
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// stamp formats a time for a cause.
func stamp(at time.Time) string { return at.UTC().Format(time.RFC3339) }

// beat plays the beat of signal's row for member.
func (t *Table) beat(member string, at time.Time, signal, row, toward, cause string) []Beat {
	r, ok := t.match(row, 1)
	if !ok || r.Beat == "" {
		t.mood(member).note(Entry{At: at, Signal: signal})
		return nil
	}
	b := Beat{Member: member, Beat: r.Beat, Toward: toward, Cause: cause + " -> " + r.Beat, At: at}
	t.mood(member).note(Entry{At: at, Signal: signal, Beat: b.Beat, Toward: toward, Cause: b.Cause})
	return []Beat{b}
}

// envelope plays a glance toward the first member an envelope of the
// member's goes to, and remembers a handoff until a recipient takes it.
func (t *Table) envelope(l talkoot.Line) []Beat {
	e := l.Envelope
	if e == nil || !isMember(e.From) {
		return nil
	}
	if e.Kind != talkoot.KindMessage && e.Kind != talkoot.KindHandoff {
		return nil
	}
	if e.Kind == talkoot.KindHandoff {
		if _, ok := t.handoffs[e.ID]; !ok {
			t.handoffs[e.ID] = e.From
			t.order = append(t.order, e.ID)
			if len(t.order) > maxHandoffs {
				delete(t.handoffs, t.order[0])
				t.order = t.order[1:]
			}
		}
	}
	i := slices.IndexFunc(e.To, isMember)
	if i < 0 {
		t.mood(e.From).note(Entry{At: l.At, Signal: "sent " + string(e.Kind)})
		return nil
	}
	return t.beat(e.From, l.At, "sent "+string(e.Kind), SignalSent, e.To[i], fmt.Sprintf("sent %s %s to %s", e.Kind, e.ID, e.To[i]))
}

// pickup plays happy for the sender of a handoff that its recipient took.
// Only the first recipient to take it counts.
func (t *Table) pickup(l talkoot.Line) []Beat {
	from, ok := t.handoffs[l.Ref]
	if !ok || from == l.Member {
		return nil
	}
	delete(t.handoffs, l.Ref)
	t.order = slices.DeleteFunc(t.order, func(id string) bool { return id == l.Ref })
	return t.beat(from, l.At, "handoff picked up", SignalPickedUp, "", fmt.Sprintf("handoff %s picked up by %s", l.Ref, l.Member))
}

// Replayed implements [Engine]. A card lives in a running daemon's memory,
// so no card open before the stop is still open.
func (t *Table) Replayed() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, m := range t.members {
		m.cards = nil
	}
}

// Expression implements [Engine]. A card that has waited past its row's
// wait raises the brow over any held pose: a person's attention is the
// thing the member needs.
func (t *Table) Expression(member string, now time.Time) Expression {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.members[member]
	if m == nil {
		return Expression{}
	}
	if r, ok := t.match(SignalCardWait, 1); ok {
		if ref, opened, ok := m.oldestCard(); ok && !now.Before(opened.Add(r.After)) {
			x := Expression{Pose: r.Pose, Intensity: r.Intensity}
			x.Cause = fmt.Sprintf("card %s open since %s, past %s -> %s", ref, stamp(opened), r.After, poseName(x))
			return x
		}
	}
	if now.Before(m.until) {
		return m.held
	}
	return Expression{}
}

// Next implements [Engine].
func (t *Table) Next(now time.Time) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	var next time.Time
	soonest := func(at time.Time) {
		if at.After(now) && (next.IsZero() || at.Before(next)) {
			next = at
		}
	}
	wait, waits := t.match(SignalCardWait, 1)
	for _, m := range t.members {
		soonest(m.until)
		if _, opened, ok := m.oldestCard(); ok && waits {
			soonest(opened.Add(wait.After))
		}
	}
	return next
}

// Advance implements [Engine].
func (t *Table) Advance(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.advance(now)
}

// advance records the end of each held pose, and each open card whose wait
// ran out, up to now. A member's entries go in the order of their moments.
func (t *Table) advance(now time.Time) {
	wait, waits := t.match(SignalCardWait, 1)
	for _, m := range t.members {
		var due []Entry
		if m.held.Pose != "" && !m.ended && !now.Before(m.until) {
			m.ended = true
			due = append(due, Entry{At: m.until, Signal: "hold ended", Cause: poseName(m.held) + " held until " + stamp(m.until)})
		}
		for ref, c := range m.cards {
			if !waits || c.raised || now.Before(c.opened.Add(wait.After)) {
				continue
			}
			c.raised = true
			x := Expression{Pose: wait.Pose, Intensity: wait.Intensity}
			due = append(due, Entry{At: c.opened.Add(wait.After), Signal: "card_wait", Pose: x.Pose, Intensity: x.Intensity,
				Cause: fmt.Sprintf("card %s open since %s, past %s -> %s", ref, stamp(c.opened), wait.After, poseName(x))})
		}
		slices.SortFunc(due, func(a, b Entry) int {
			if c := a.At.Compare(b.At); c != 0 {
				return c
			}
			return strings.Compare(a.Cause, b.Cause)
		})
		for _, e := range due {
			m.note(e)
		}
	}
}

// Trace implements [Engine].
func (t *Table) Trace(member string) []Entry {
	t.mu.Lock()
	defer t.mu.Unlock()
	if m := t.members[member]; m != nil {
		return slices.Clone(m.trace)
	}
	return nil
}

func (m *mood) note(e Entry) {
	if len(m.trace) == TraceLen {
		m.trace = append(m.trace[:0], m.trace[1:]...)
	}
	m.trace = append(m.trace, e)
}

// oldestCard returns the member's open card that opened first. Two cards
// that opened together are ordered by their ids, so the choice is repeatable.
func (m *mood) oldestCard() (string, time.Time, bool) {
	var ref string
	var at time.Time
	for r, c := range m.cards {
		if ref == "" || c.opened.Before(at) || (c.opened.Equal(at) && r < ref) {
			ref, at = r, c.opened
		}
	}
	return ref, at, ref != ""
}

// isMember reports whether id names a member rather than a person or the
// recruiter.
func isMember(id string) bool {
	return id != "" && !strings.HasPrefix(id, talkoot.HumanPrefix) && !strings.HasPrefix(id, talkoot.RecruiterPrefix)
}

// poseName names a pose and its form, for a cause.
func poseName(x Expression) string {
	if x.Intensity > 0 {
		return fmt.Sprintf("%s (strong %d)", x.Pose, x.Intensity)
	}
	return x.Pose
}

// quote bounds a line's reason for a cause.
func quote(s string) string {
	if len(s) <= maxCauseReason {
		return s
	}
	cut := maxCauseReason
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
