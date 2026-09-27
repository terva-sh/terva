package talkoot

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Driver hands a rendered envelope to one member as a user turn. When the
// member is working, the text lands at the next safe boundary of that turn.
// When it is idle, the text starts a turn. A driver never interrupts a tool
// call (decision 0022 rule 6).
//
// The workspace implements the two drivers: a native one over a daemon
// session's queue, and a worker one over the swarm's SendUserTurn. This
// package imports neither.
type Driver interface {
	Deliver(talkoot string, m Member, text string) error
}

// A ReadDriver says when a delivery reaches the member's turn. The router
// then moves the member into the delivery's chain at that moment, and not
// when the driver takes the text. A busy member reads a queued envelope at
// its turn's next safe boundary. Until then it still works, and sends, in its
// old chain.
//
// A driver that is only a Driver moves the member when the router calls it.
// That is exact for an idle member and early for a busy one.
type ReadDriver interface {
	Driver
	// DeliverRead is Deliver, and the driver calls Router.Read with r once,
	// when text enters the member's turn and before the turn acts on it. That
	// can be before DeliverRead returns. When the text never reaches a turn,
	// the driver never calls Read, and the member keeps its chain.
	DeliverRead(talkoot string, m Member, text string, r Receipt) error
}

// Receipt names one delivery to a [ReadDriver]. Only the router makes one.
type Receipt struct {
	member, ref, chain string
}

// Drivers picks a driver by the member's driver field.
type Drivers struct {
	Native Driver // for DriverNative
	Worker Driver // for every registered worker backend
}

func (d Drivers) forMember(m Member) Driver {
	if m.Driver == DriverNative {
		return d.Native
	}
	return d.Worker
}

// Limits are the guard values from the proposal's guard table. They are
// starting guesses to measure against, not tuned numbers.
type Limits struct {
	HopLimit        int           // member envelopes per chain before it pauses
	SendsPerWindow  int           // envelopes one member may send per SendWindow
	SendWindow      time.Duration //
	DuplicateWindow time.Duration // the same body to the same recipient inside it is dropped
	MaxWorking      int           // members working at once; further wakes wait
}

// DefaultLimits are the proposal's starting values.
func DefaultLimits() Limits {
	return Limits{
		HopLimit:        12,
		SendsPerWindow:  20,
		SendWindow:      10 * time.Minute,
		DuplicateWindow: 10 * time.Minute,
		MaxWorking:      4,
	}
}

// Guard names, as the room records them.
const (
	GuardHops      = "hops"
	GuardRate      = "rate"
	GuardDuplicate = "duplicate"
	GuardSpend     = "spend"
	GuardTeamSpend = "team-spend"
	GuardTurns     = "turns"
	GuardWorking   = "working"
	GuardHumanRoot = "human-root"
	GuardPerson    = "person"
	GuardDelivery  = "delivery"
	GuardCost      = "cost"
	GuardRoom      = "room"
)

// Errors a sender sees. Each one reads as an instruction to the model that
// sent the envelope.
var (
	ErrNoHumanRoot = errors.New("talkoot: this envelope has no human post at the root of its chain; a teammate can reply only inside work a person started")
	ErrRateLimited = errors.New("talkoot: you have sent too many envelopes recently; wait before you send again")
	ErrDuplicate   = errors.New("talkoot: you already sent this to every recipient recently; it was dropped")
	ErrPaused      = errors.New("talkoot: paused; a person has to resume it before anything more is sent")
)

type chainState struct {
	hops      int
	allowance int // hops allowed before the chain pauses; a resume adds HopLimit
}

type pending struct {
	env Envelope
	to  string
}

// delivery is one envelope routed to one member and not yet handed to its
// driver. dispatch claims the chain and the notes just before the driver runs.
type delivery struct {
	driver Driver
	member Member
	env    Envelope
}

// grant is one delivery's claim on its member's notes, and, for a driver that
// does not report reads, on its chain, from just before the driver runs until
// it returns.
type grant struct {
	root  string
	state int // grantPending, grantDelivered, or grantFailed
	notes []string
	epoch int // the member's turn epoch when the grant was made
	// reads is set for a ReadDriver. The grant then leaves the chain alone,
	// and Read moves the member.
	reads bool
}

const (
	grantPending = iota
	grantDelivered
	grantFailed
)

// grants are a member's claims in the order they were made. base is the chain
// the member held before the oldest claim still listed.
type grants struct {
	base string
	list []*grant
}

// Router takes envelopes from people and members, applies the guards, appends
// everything to the room, and delivers to each recipient. Decision 0022 rules
// 4 to 8 are its contract.
type Router struct {
	mu      sync.Mutex
	roster  Roster
	room    *Room
	drivers Drivers
	limits  Limits
	now     func() time.Time

	day       string
	spend     map[string]float64 // member -> today's spend
	turns     map[string]int     // member -> today's turns
	teamSpend float64
	chains    map[string]*chainState // chain root -> state
	active    map[string]string      // member -> the chain whose envelope woke it last
	grants    map[string]*grants     // member -> deliveries whose driver is running
	turning   map[string]bool        // member -> a delivered envelope started or joined a turn
	epoch     map[string]int         // member -> turns ended, so a late delivery sees one ended
	inflight  map[string]int         // member -> deliveries routed and not yet returned
	pauses    pauses
	sends     map[string][]time.Time
	recent    map[string]time.Time // dedupe key -> when sent
	// held are the deliveries that wait for a resume or a working slot.
	// Replay rebuilds them: a waking envelope with no delivery line and no
	// failed-delivery line for a recipient is still owed to it.
	held    []pending
	notes   map[string][]string  // member -> rendered notes for its next turn
	refused map[string]time.Time // member and guard -> when its last refusal line was written
	// owed holds, during replay only, the index in held of each envelope and
	// recipient that has no outcome yet.
	owed map[string]int
	// stopped holds every delivery from Stop on.
	stopped bool
}

// NewRouter builds a router for a validated roster and replays the room, so
// the day's spend, the chains, and every pause survive a daemon restart. A
// spend cap that a restart resets is a cap anyone can clear.
func NewRouter(r Roster, room *Room, d Drivers, l Limits, now func() time.Time) (*Router, error) {
	if d.Native == nil || d.Worker == nil {
		return nil, errors.New("talkoot: the router needs a native and a worker driver")
	}
	if now == nil {
		now = time.Now
	}
	rt := &Router{
		roster: r, room: room, drivers: d, limits: l, now: now,
		spend: map[string]float64{}, turns: map[string]int{},
		chains: map[string]*chainState{}, active: map[string]string{}, grants: map[string]*grants{},
		turning: map[string]bool{}, epoch: map[string]int{}, inflight: map[string]int{},
		pauses: pauses{},
		sends:  map[string][]time.Time{}, recent: map[string]time.Time{},
		notes: map[string][]string{}, refused: map[string]time.Time{}, owed: map[string]int{},
	}
	rt.day = rt.dayOf(now())
	lines, err := room.Read()
	if err != nil {
		return nil, err
	}
	for _, l := range lines {
		rt.replay(l)
	}
	// 🚨 A damaged entry is never written, and the next append rewrites the
	// head. So a cut end would vanish after one post, and a failed line could
	// be removed after it, taking the pause along. The pause becomes a sealed
	// line, once for each run of damage, so it holds until a person resumes.
	if why := unsealedDamage(lines); why != "" {
		l := Line{Type: LineGuard, At: now(), Guard: GuardRoom, Action: ActionPaused, Reason: damageReason(why)}
		if err := room.Append(l); err != nil {
			return nil, fmt.Errorf("talkoot: the room is damaged, and the pause for it cannot be recorded: %w", err)
		}
		rt.replay(l)
	}
	// Settled deliveries left gaps in held. What remains is still owed.
	owed := rt.held[:0]
	for _, p := range rt.held {
		if p.to != "" {
			owed = append(owed, p)
		}
	}
	rt.held, rt.owed = owed, nil
	return rt, nil
}

// Release retries every held delivery. The workspace calls it once the
// drivers can deliver, after a restart has replayed what was still owed.
// NewRouter does not, because a driver called before its member is bound
// fails, and a failed delivery is not retried.
func (rt *Router) Release() {
	rt.mu.Lock()
	ds := rt.releaseLocked()
	rt.mu.Unlock()
	rt.dispatch(ds)
}

func owedKey(ref, member string) string { return ref + "\x00" + member }

// settleOwed marks a replayed delivery as done, delivered or failed.
func (rt *Router) settleOwed(ref, member string) {
	if i, ok := rt.owed[owedKey(ref, member)]; ok {
		rt.held[i].to = ""
		delete(rt.owed, owedKey(ref, member))
	}
}

func (rt *Router) dayOf(t time.Time) string { return t.In(rt.now().Location()).Format(time.DateOnly) }

// roll resets the daily counters when the day changes. Pauses stay: only a
// person resumes.
func (rt *Router) roll(now time.Time) {
	if d := rt.dayOf(now); d != rt.day {
		rt.day = d
		rt.spend = map[string]float64{}
		rt.turns = map[string]int{}
		rt.teamSpend = 0
	}
}

func (rt *Router) chain(root string) *chainState {
	cs := rt.chains[root]
	if cs == nil {
		cs = &chainState{allowance: rt.limits.HopLimit}
		rt.chains[root] = cs
	}
	return cs
}

func (rt *Router) replay(l Line) {
	switch l.Type {
	case LineEnvelope:
		e := l.Envelope
		if e == nil {
			return
		}
		cs := rt.chain(e.Chain.Root)
		cs.hops = max(cs.hops, e.Chain.Hops)
		// A note waits in its recipient's buffer until a delivery line
		// says how many notes a delivery carried. A waking envelope is owed
		// to each recipient until its delivery line or failed-delivery line.
		for _, to := range e.To {
			if _, ok := rt.roster.member(to); !ok {
				continue
			}
			if !e.Kind.wakes() {
				rt.notes[to] = append(rt.notes[to], render(rt.roster, *e))
				continue
			}
			rt.owed[owedKey(e.ID, to)] = len(rt.held)
			rt.held = append(rt.held, pending{env: *e, to: to})
		}
		if !strings.HasPrefix(e.From, HumanPrefix) {
			// Only the envelopes still inside a window matter to a guard, so
			// replay loads no more than those.
			if rt.now().Sub(e.At) < rt.limits.SendWindow {
				rt.sends[e.From] = append(rt.sends[e.From], e.At)
			}
			if rt.now().Sub(e.At) < rt.limits.DuplicateWindow {
				for _, to := range e.To {
					rt.recent[dedupeKey(e.From, to, e.Kind, e.Body)] = e.At
				}
			}
		}
	case LineTurn:
		// The turn line names the chain the member worked in. An envelope
		// line cannot: a held or failed delivery never reached the member,
		// and replay must not hand it a chain it never saw.
		if l.Chain != "" {
			rt.active[l.Member] = l.Chain
		}
		// 🚨 A member that has left the roster still spent the team's money.
		// Dropping its turns would lower the team's spend with every update
		// that removes a member.
		m, ok := rt.roster.member(l.Member)
		if !ok {
			m = Member{ID: l.Member}
		}
		if rt.dayOf(l.At) == rt.day {
			rt.spend[l.Member] += l.CostUSD
			rt.turns[l.Member]++
			rt.teamSpend += l.CostUSD
			rt.capTrips(m, l.Reason)
		} else if l.Reason != "" && !rt.pauses.has(memberScope(l.Member), pauseCost) {
			// A bad-cost pause outlasts the day, as every pause does.
			rt.pauses.set(memberScope(l.Member), pauseCost, l.Reason)
		}
	case LineGuard:
		switch {
		case l.Action == ActionPaused:
			rt.pauses.set(scopeOf(l.Member, l.Chain), kindOfGuard(l.Guard), l.Reason)
		case l.Guard == GuardDelivery:
			rt.settleOwed(l.Ref, l.Member)
		}
	case LineResume:
		rt.resumeLocked(l.Member, l.Chain, l.Guard == GuardPerson)
	case LineDelivery:
		// A delivery to a ReadDriver names no chain. Its read line moves the
		// member, when the member reads it.
		if l.Chain != "" {
			rt.active[l.Member] = l.Chain
		}
		rt.settleOwed(l.Ref, l.Member)
		if n := min(l.Notes, len(rt.notes[l.Member])); n > 0 {
			rt.notes[l.Member] = rt.notes[l.Member][n:]
		}
	case LineRead:
		if l.Chain != "" {
			rt.active[l.Member] = l.Chain
		}
	case LineDamaged:
		// 🚨 A damaged line may have been a turn, and its spend is gone. The
		// talkoot stays paused until a person has looked and resumes it.
		if !rt.pauses.has(talkootScope, pauseRoom) {
			rt.pauses.set(talkootScope, pauseRoom, damageReason(l.Reason))
		}
	}
}

// damageReason tells the person what a resume past damage costs.
func damageReason(why string) string {
	return why + ", so a turn's spend or a pause may be missing, and a resume goes on without it; check the room, then resume the talkoot"
}

// unsealedDamage returns the reason for the first damaged entry that no
// sealed room pause follows, or "" when every run of damage has one.
func unsealedDamage(lines []Line) string {
	why, more := "", 0
	for _, l := range lines {
		switch {
		case l.Type == LineDamaged && why == "":
			why = l.Reason
		case l.Type == LineDamaged:
			more++
		case l.Type == LineGuard && l.Guard == GuardRoom && l.Action == ActionPaused:
			why, more = "", 0
		}
	}
	if more > 0 {
		why += fmt.Sprintf(" (and %d more)", more)
	}
	return why
}

// resumeLocked lifts a pause. The live Resume and replay both call it.
//
// 🔑 Pauses stack, and a person's own pause is the top layer. A resume lifts
// that layer alone when there is one. A cap, a bad cost, or a guard under it
// stays, and the status then names it, so a person who paused a member for
// lunch does not also wave through the budget it reached meanwhile. A resume
// with no person's pause on the scope lifts everything the status named.
//
// personOnly is the layer the live resume chose, which its room line records.
// Replay follows the line rather than choosing again, because a pause the
// room never recorded would change the choice.
func (rt *Router) resumeLocked(member, chain string, personOnly bool) {
	scope := scopeOf(member, chain)
	if personOnly {
		rt.pauses.drop(scope, pausePerson)
		return
	}
	rt.pauses.clear(scope)
	if chain != "" {
		// A resumed chain gets another full allowance. Without it, the next
		// send would trip the same limit at once.
		cs := rt.chain(chain)
		cs.allowance = cs.hops + rt.limits.HopLimit
	}
}

func dedupeKey(from, to string, k Kind, body string) string {
	return from + "\x00" + to + "\x00" + string(k) + "\x00" + strings.TrimSpace(body)
}

// Post records a person's post and delivers it. A post with no recipient goes
// to the coordinator. Every post is the root of a new chain.
func (rt *Router) Post(human string, to []string, body string, refs []string, thread string) (Envelope, error) {
	if !tokenPattern.MatchString(human) {
		return Envelope{}, fmt.Errorf("talkoot: the person's name %q must be 1 to 64 letters, digits, and . _ @ -", human)
	}
	if len(to) == 0 {
		to = []string{rt.roster.coordinator().ID}
	}
	o := Outgoing{To: to, Kind: KindMessage, Body: body, Refs: refs, Thread: thread}
	if err := validateOutgoing(rt.roster, o, remedy{person: true}); err != nil {
		return Envelope{}, err
	}
	if err := rt.checkNotes(o.Refs, remedy{person: true}); err != nil {
		return Envelope{}, err
	}
	rt.mu.Lock()
	now := rt.now()
	e := rt.envelope(HumanPrefix+human, o, now)
	e.Chain = Chain{Root: e.ID}
	rt.chain(e.ID)
	if err := rt.room.Append(Line{Type: LineEnvelope, At: now, Envelope: &e}); err != nil {
		rt.mu.Unlock()
		return Envelope{}, err
	}
	var ds []delivery
	for _, id := range e.To {
		ds = append(ds, rt.routeLocked(e, id, false)...)
	}
	rt.mu.Unlock()
	rt.dispatch(ds)
	return e, nil
}

// Send records a member's envelope and delivers it. The router sets From and
// the chain. The chain is the one whose envelope last woke the sender, so a
// member that no person's work has reached cannot send (decision 0022 rule 7).
func (rt *Router) Send(from string, o Outgoing) (Envelope, error) {
	if _, ok := rt.roster.member(from); !ok {
		return Envelope{}, fmt.Errorf("talkoot: %q is not a member of %s", from, rt.roster.ID)
	}
	if err := validateOutgoing(rt.roster, o, remedy{}); err != nil {
		return Envelope{}, err
	}
	if err := rt.checkNotes(o.Refs, remedy{}); err != nil {
		return Envelope{}, err
	}
	rt.mu.Lock()
	now := rt.now()
	e, ds, err := rt.sendLocked(from, o, now)
	rt.mu.Unlock()
	if err != nil {
		return Envelope{}, err
	}
	rt.dispatch(ds)
	return e, nil
}

// Propose records a member's roster proposal as an envelope addressed to no
// member, and returns it. It passes the guards a send passes: the chain needs
// a person at its root, and the rate and hop limits count it. It delivers
// nothing. The proposal itself waits for a person, outside the router.
func (rt *Router) Propose(from, summary string) (Envelope, error) {
	if _, ok := rt.roster.member(from); !ok {
		return Envelope{}, fmt.Errorf("talkoot: %q is not a member of %s", from, rt.roster.ID)
	}
	if err := checkSummary(summary); err != nil {
		return Envelope{}, err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	e, _, err := rt.sendLocked(from, Outgoing{To: []string{}, Kind: KindProposal, Body: summary}, rt.now())
	return e, err
}

// ProposeAs records a person's roster proposal. Like a post, it starts its own
// chain, and no guard applies to a person.
func (rt *Router) ProposeAs(human, summary string) (Envelope, error) {
	if !tokenPattern.MatchString(human) {
		return Envelope{}, fmt.Errorf("talkoot: the person's name %q must be 1 to 64 letters, digits, and . _ @ -", human)
	}
	if err := checkSummary(summary); err != nil {
		return Envelope{}, err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	now := rt.now()
	e := rt.envelope(HumanPrefix+human, Outgoing{To: []string{}, Kind: KindProposal, Body: summary}, now)
	e.Chain = Chain{Root: e.ID}
	if err := rt.room.Append(Line{Type: LineEnvelope, At: now, Envelope: &e}); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

func checkSummary(summary string) error {
	if strings.TrimSpace(summary) == "" {
		return errors.New("talkoot: the proposal's summary is empty")
	}
	if len(summary) > MaxBodyBytes {
		return fmt.Errorf("talkoot: the proposal's summary is %d bytes, above the %d limit", len(summary), MaxBodyBytes)
	}
	return nil
}

func (rt *Router) sendLocked(from string, o Outgoing, now time.Time) (Envelope, []delivery, error) {
	guard := func(name, action, reason, chain string, err error) (Envelope, []delivery, error) {
		l := Line{Type: LineGuard, At: now, Guard: name, Action: action, Reason: reason, Chain: chain}
		if action != ActionPaused || chain == "" {
			l.Member = from
		}
		// 🚨 A member in a loop sends as fast as it is refused. One refusal
		// line per member and guard per window records the pattern, and the
		// room does not grow with every retry.
		if action == ActionPaused || now.Sub(rt.refused[from+"\x00"+name]) >= rt.limits.SendWindow {
			if appendErr := rt.room.Append(l); appendErr != nil {
				return Envelope{}, nil, appendErr
			}
			if action != ActionPaused {
				rt.refused[from+"\x00"+name] = now
			}
		}
		return Envelope{}, nil, err
	}

	root := rt.active[from]
	if root == "" {
		return guard(GuardHumanRoot, ActionRefused, "no human post at the root of the chain", "", ErrNoHumanRoot)
	}
	cs := rt.chain(root)
	if why := rt.pausedWhy(from, root); why != "" {
		return Envelope{}, nil, fmt.Errorf("%w (%s)", ErrPaused, why)
	}

	recent := rt.sends[from][:0]
	for _, t := range rt.sends[from] {
		if now.Sub(t) < rt.limits.SendWindow {
			recent = append(recent, t)
		}
	}
	rt.sends[from] = recent
	if len(recent) >= rt.limits.SendsPerWindow {
		reason := fmt.Sprintf("%d envelopes in %s", len(recent), rt.limits.SendWindow)
		return guard(GuardRate, ActionRefused, reason, "", ErrRateLimited)
	}

	hops := cs.hops + 1
	if hops > cs.allowance {
		reason := fmt.Sprintf("the chain reached %d hops", cs.hops)
		rt.pauses.set(chainScope(root), pauseGuard, reason)
		return guard(GuardHops, ActionPaused, reason, root, fmt.Errorf("%w (this chain: %s)", ErrPaused, reason))
	}

	// A proposal has no recipient to drop a duplicate for. The rate limit
	// bounds a member that proposes the same change again and again.
	//
	// 🚨 validateOutgoing already refuses the kind on a send. This refusal
	// does not rely on it: a proposal with a recipient would skip the dedupe
	// and land as a note.
	if o.Kind == KindProposal && len(o.To) > 0 {
		return Envelope{}, nil, errors.New("talkoot: a proposal is addressed to no member")
	}
	if o.Kind != KindProposal {
		var to, dropped []string
		for _, id := range o.To {
			if t, ok := rt.recent[dedupeKey(from, id, o.Kind, o.Body)]; ok && now.Sub(t) < rt.limits.DuplicateWindow {
				dropped = append(dropped, id)
				continue
			}
			to = append(to, id)
		}
		if len(to) == 0 {
			return guard(GuardDuplicate, ActionDropped, "the same body to the same recipients", "", ErrDuplicate)
		}
		if len(dropped) > 0 {
			if err := rt.room.Append(Line{Type: LineGuard, At: now, Guard: GuardDuplicate, Action: ActionDropped,
				Member: from, Reason: "already sent to " + strings.Join(dropped, ", ")}); err != nil {
				return Envelope{}, nil, err
			}
		}
		o.To = to
	}

	cs.hops = hops
	e := rt.envelope(from, o, now)
	e.Chain = Chain{Root: root, Hops: hops}
	if err := rt.room.Append(Line{Type: LineEnvelope, At: now, Envelope: &e}); err != nil {
		return Envelope{}, nil, err
	}
	rt.sends[from] = append(rt.sends[from], now)
	// The dedupe map holds whole bodies, so it drops what has left the window.
	for k, t := range rt.recent {
		if now.Sub(t) >= rt.limits.DuplicateWindow {
			delete(rt.recent, k)
		}
	}
	var ds []delivery
	for _, id := range e.To {
		rt.recent[dedupeKey(from, id, e.Kind, e.Body)] = now
		ds = append(ds, rt.routeLocked(e, id, false)...)
	}
	return e, ds, nil
}

func (rt *Router) envelope(from string, o Outgoing, now time.Time) Envelope {
	return Envelope{
		ID: newID(now), Talkoot: rt.roster.ID, From: from, To: o.To, Kind: o.Kind,
		Body: o.Body, Refs: o.Refs, Thread: o.Thread, ReplyTo: o.ReplyTo, At: now,
	}
}

// routeLocked decides what happens to one envelope for one recipient: it is
// buffered as a note, held behind a pause or the working limit, or returned
// as a delivery for dispatch after the lock is released. A delivery takes a
// working slot here, so one batch cannot fill more slots than the limit.
//
// retry marks a delivery that already waited, so a second wait writes no
// second queued line to the room.
func (rt *Router) routeLocked(e Envelope, to string, retry bool) []delivery {
	m, _ := rt.roster.member(to)
	if !e.Kind.wakes() {
		rt.notes[to] = append(rt.notes[to], render(rt.roster, e))
		return nil
	}
	if rt.blockedLocked(e, to) {
		rt.held = append(rt.held, pending{env: e, to: to})
		return nil
	}
	if !rt.busy(to) && rt.workingCount() >= rt.limits.MaxWorking {
		rt.held = append(rt.held, pending{env: e, to: to})
		if retry {
			return nil
		}
		rt.record(Line{Type: LineGuard, At: rt.now(), Guard: GuardWorking, Action: ActionQueued,
			Member: to, Reason: fmt.Sprintf("%d members are already working", rt.workingCount()), Ref: e.ID})
		return nil
	}
	rt.inflight[to]++
	return []delivery{{driver: rt.drivers.forMember(m), member: m, env: e}}
}

// busy reports whether a member holds a working slot: a turn is running, or a
// delivery to it is on its way.
func (rt *Router) busy(id string) bool { return rt.turning[id] || rt.inflight[id] > 0 }

func (rt *Router) workingCount() int {
	n := 0
	for _, m := range rt.roster.Members {
		if rt.busy(m.ID) {
			n++
		}
	}
	return n
}

// dispatch calls the drivers with the lock released, because a driver may
// start a turn that reports back into the router.
//
// Each delivery is checked against the pauses once more just before its
// driver runs, so a pause that arrived after routing holds it. A pause that
// lands while the driver call is already running cannot stop it, the same as
// a pause a moment later.
//
// A failed delivery frees its working slot, so it releases what waited for
// one, and those deliveries join the same loop.
func (rt *Router) dispatch(ds []delivery) {
	for len(ds) > 0 {
		d := ds[0]
		ds = ds[1:]
		id := d.member.ID
		rt.mu.Lock()
		if rt.blockedLocked(d.env, id) {
			rt.inflight[id]--
			rt.held = append(rt.held, pending{env: d.env, to: id})
			rt.mu.Unlock()
			continue
		}
		rd, reads := d.driver.(ReadDriver)
		g := rt.claimLocked(id, d.env.Chain.Root, reads)
		text := render(rt.roster, d.env)
		if len(g.notes) > 0 {
			text = strings.Join(g.notes, "\n\n") + "\n\n" + text
		}
		rt.mu.Unlock()
		var err error
		if reads {
			err = rd.DeliverRead(rt.roster.ID, d.member, text, Receipt{member: id, ref: d.env.ID, chain: g.root})
		} else {
			err = d.driver.Deliver(rt.roster.ID, d.member, text)
		}
		rt.mu.Lock()
		rt.inflight[id]--
		if err != nil {
			g.state = grantFailed
			rt.notes[id] = append(g.notes, rt.notes[id]...)
			rt.settleLocked(id, g)
			rt.record(Line{Type: LineGuard, At: rt.now(), Guard: GuardDelivery, Action: ActionRefused,
				Member: id, Reason: err.Error(), Ref: d.env.ID})
			ds = append(ds, rt.releaseLocked()...)
		} else {
			g.state = grantDelivered
			rt.settleLocked(id, g)
			// The line settles what replay owes the member, and, for a driver
			// that does not report reads, gives the member its chain back
			// after a restart. A lost one fails toward sending again: replay
			// owes the envelope and its notes a second time. That is a
			// repeat, never a loss, so an append error pauses nothing.
			chain := g.root
			if reads {
				chain = ""
			}
			_ = rt.room.Append(Line{Type: LineDelivery, At: rt.now(), Member: id, Chain: chain,
				Ref: d.env.ID, Notes: len(g.notes)})
			// 🔑 A turn that ended while the driver ran was the turn this
			// delivery started or joined. Marking the member busy now would
			// hold a slot that no turn end will ever free.
			if rt.epoch[id] == g.epoch {
				rt.turning[id] = true
			} else {
				// That turn end ran while this delivery still held the slot,
				// so it could not release what waited. The slot is free now.
				ds = append(ds, rt.releaseLocked()...)
			}
		}
		rt.mu.Unlock()
	}
}

// claimLocked gives a member the delivery's waiting notes just before the
// driver runs. For a driver that does not report reads, it gives the member
// the delivery's chain too. The member may send in that chain the moment its
// turn starts, which can be before Deliver returns.
func (rt *Router) claimLocked(id, root string, reads bool) *grant {
	g := &grant{root: root, notes: rt.notes[id], epoch: rt.epoch[id], reads: reads}
	delete(rt.notes, id)
	if reads {
		return g
	}
	gs := rt.grants[id]
	if gs == nil {
		gs = &grants{base: rt.active[id]}
		rt.grants[id] = gs
	}
	gs.list = append(gs.list, g)
	rt.active[id] = root
	return g
}

// settleLocked sets a member's chain from its claims once one returns. The
// member holds the chain of its newest claim that did not fail. With none,
// it holds the chain of the newest delivery that went through, or, with no
// such delivery, the chain it had before them all. A grant to a ReadDriver
// took no chain, so it settles nothing.
//
// 🔑 Two deliveries to one member can overlap. Neither may undo the chain the
// other granted, whichever order they return in.
func (rt *Router) settleLocked(id string, g *grant) {
	if g.reads {
		return
	}
	gs := rt.grants[id]
	for len(gs.list) > 0 && gs.list[0].state != grantPending {
		if gs.list[0].state == grantDelivered {
			gs.base = gs.list[0].root
		}
		gs.list = gs.list[1:]
	}
	active := gs.base
	for _, g := range gs.list {
		if g.state != grantFailed {
			active = g.root
		}
	}
	if active == "" {
		delete(rt.active, id)
	} else {
		rt.active[id] = active
	}
	if len(gs.list) == 0 {
		delete(rt.grants, id)
	}
}

// Read moves a member into the chain of a delivery its turn has just read. A
// [ReadDriver] calls it, once for each delivery that reaches a turn.
//
// The member reads the text whatever holds the chain now, so a pause does not
// stop the move. It stops the member's sends, as it would in any chain. A read
// after Stop is recorded, the same as a turn end.
func (rt *Router) Read(r Receipt) error {
	if _, ok := rt.roster.member(r.member); !ok {
		return fmt.Errorf("talkoot: %q is not a member of %s", r.member, rt.roster.ID)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	// A router that replaced the one that made r knows the chain from the
	// room, so a read that lands after an update still moves the member.
	if _, ok := rt.chains[r.chain]; !ok {
		return fmt.Errorf("talkoot: %s has no chain %q", rt.roster.ID, r.chain)
	}
	rt.active[r.member] = r.chain
	// 🔑 Replay moves the member at this line, so a restart puts it in the
	// chain it last read. A lost line leaves the turn line to name the chain
	// when the turn ends, and the talkoot pauses until a person looks.
	rt.record(Line{Type: LineRead, At: rt.now(), Member: r.member, Chain: r.chain, Ref: r.ref})
	return nil
}

func (rt *Router) blockedLocked(e Envelope, to string) bool {
	return rt.stopped || rt.pausedWhy(to, e.Chain.Root) != ""
}

// Stop holds every delivery from now on, and writes no line for it, so the
// next router over this room still owes it. A turn that ends after Stop is
// still recorded, so its spend survives the restart.
func (rt *Router) Stop() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.stopped = true
}

// pausedWhy names every pause that holds a member in a chain, or returns ""
// when nothing does.
func (rt *Router) pausedWhy(member, root string) string {
	var out []string
	for _, s := range []struct{ label, scope string }{
		{"the talkoot", talkootScope}, {member, memberScope(member)}, {"this chain", chainScope(root)},
	} {
		if r := rt.pauses.reason(s.scope); r != "" {
			out = append(out, s.label+": "+r)
		}
	}
	return strings.Join(out, "; ")
}

// record appends a line whose loss would leave the room short of the truth.
// When it cannot be written, the talkoot pauses in memory until a person
// looks, as an unrecorded turn does.
func (rt *Router) record(l Line) {
	if err := rt.room.Append(l); err != nil && !rt.pauses.has(talkootScope, pauseRoom) {
		what := l.Guard
		if what == "" {
			what = l.Type
		}
		rt.pauses.set(talkootScope, pauseRoom, "the room could not record a "+what+" line: "+err.Error())
	}
}

// TurnEnded records a member's finished turn and its cost, applies the spend
// and turn caps, and releases any delivery that waited for a working slot.
// The workspace calls it from each driver's turn-end signal. A member that
// left the roster while its turn ran still counts toward the talkoot's caps.
func (rt *Router) TurnEnded(member string, costUSD float64) error {
	m, err := rt.turnMember(member)
	if err != nil {
		return err
	}
	// 🚨 NaN fails every comparison and a negative cost lowers the total, so
	// either one would switch the spend caps off. The turn counts as free,
	// and the member pauses until a person looks.
	var badCost string
	if math.IsNaN(costUSD) || math.IsInf(costUSD, 0) || costUSD < 0 {
		badCost = fmt.Sprintf("the driver reported a turn cost of %v", costUSD)
		costUSD = 0
	}
	return rt.turnEnded(m, costUSD, badCost)
}

// TurnUnreported records a member turn whose cost will never arrive, such as
// one still running when the daemon stops. The turn counts as free, and the
// member pauses until a person looks, the same as for a bad cost. The turn
// line carries why, so the pause comes back after a restart.
func (rt *Router) TurnUnreported(member, why string) error {
	m, err := rt.turnMember(member)
	if err != nil {
		return err
	}
	if why == "" {
		why = "the turn's cost was never reported"
	}
	return rt.turnEnded(m, 0, why)
}

// turnMember names the member a turn belongs to. A member that left the
// roster, by an update while its turn ran, still spent the team's money.
// Its turn counts toward the talkoot's caps, and no member cap applies.
func (rt *Router) turnMember(member string) (Member, error) {
	if m, ok := rt.roster.member(member); ok {
		return m, nil
	}
	if !idPattern.MatchString(member) {
		return Member{}, fmt.Errorf("talkoot: %q is not a member id", member)
	}
	return Member{ID: member}, nil
}

func (rt *Router) turnEnded(m Member, costUSD float64, badCost string) error {
	member := m.ID
	rt.mu.Lock()
	now := rt.now()
	rt.roll(now)
	rt.turning[member] = false
	rt.epoch[member]++
	rt.spend[member] += costUSD
	rt.turns[member]++
	rt.teamSpend += costUSD
	err := rt.room.Append(Line{Type: LineTurn, At: now, Member: member, Chain: rt.active[member], CostUSD: costUSD, Reason: badCost})
	if err != nil {
		// 🚨 A turn the room did not record is spend a restart forgets. The
		// talkoot pauses, in memory, until a person resumes it.
		rt.pauses.set(talkootScope, pauseRoom, "the room could not record a turn: "+err.Error())
		rt.mu.Unlock()
		return err
	}

	for _, c := range rt.capTrips(m, badCost) {
		if appendErr := rt.room.Append(Line{Type: LineGuard, At: now, Guard: c.guard, Action: ActionPaused,
			Member: c.member, Reason: c.reason, SpendUSD: rt.teamSpend}); appendErr != nil && err == nil {
			err = appendErr
		}
	}
	ds := rt.releaseLocked()
	rt.mu.Unlock()
	rt.dispatch(ds)
	return err
}

type capTrip struct{ guard, member, reason string }

// capTrips applies the caps after one of m's turns and returns each pause it
// set. The live router and replay both call it.
//
// 🔑 The pause follows from the turn lines alone, so a cap pause whose guard
// line never reached the room still comes back after a restart. The guard
// line is the explanation, not the source of the pause.
//
// 🔑 A cap trips whatever other pause holds the scope. Pauses stack, so a
// person's pause cannot hide a cap or a bad cost.
func (rt *Router) capTrips(m Member, badCost string) []capTrip {
	var out []capTrip
	trip := func(guard, member, kind, reason string) {
		scope := scopeOf(member, "")
		if rt.pauses.has(scope, kind) {
			return
		}
		rt.pauses.set(scope, kind, reason)
		out = append(out, capTrip{guard, member, reason})
	}
	if badCost != "" {
		trip(GuardCost, m.ID, pauseCost, badCost)
	}
	if limit := m.BudgetUSDPerDay; limit > 0 && rt.spend[m.ID] >= limit {
		trip(GuardSpend, m.ID, pauseSpend, fmt.Sprintf("spent $%.2f of the member's $%.2f a day", rt.spend[m.ID], limit))
	}
	if limit := m.TurnsPerDay; limit > 0 && rt.turns[m.ID] >= limit {
		trip(GuardTurns, m.ID, pauseTurns, fmt.Sprintf("used %d of the member's %d turns a day", rt.turns[m.ID], limit))
	}
	if limit := rt.roster.BudgetUSDPerDay; rt.teamSpend >= limit {
		trip(GuardTeamSpend, "", pauseTeam, fmt.Sprintf("spent $%.2f of the talkoot's $%.2f a day", rt.teamSpend, limit))
	}
	return out
}

// releaseLocked retries every held delivery. What is still blocked stays held,
// in order.
func (rt *Router) releaseLocked() []delivery {
	held := rt.held
	rt.held = nil
	var ds []delivery
	for _, p := range held {
		ds = append(ds, rt.routeLocked(p.env, p.to, true)...)
	}
	return ds
}

// Pause stops deliveries to a member, a chain, or, with both empty, the whole
// talkoot. It asks nothing of a working member: the driver's own pause does
// that. Pause and Resume are for a person, and by names them.
//
// The pause takes effect before it is written. If the room cannot record it,
// the pause still holds in this process, and the error says it will not
// survive a restart.
func (rt *Router) Pause(by, member, chain, reason string) error {
	if len(reason) > maxReasonBytes || strings.ContainsFunc(reason, func(r rune) bool { return unicode.IsControl(r) || lineBreak(r) }) {
		return fmt.Errorf("talkoot: a pause reason must be one line of at most %d bytes", maxReasonBytes)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if err := rt.scopeCheck(by, member, chain); err != nil {
		return err
	}
	rt.pauses.set(scopeOf(member, chain), pausePerson, "paused by "+by+": "+reason)
	if err := rt.room.Append(Line{Type: LineGuard, At: rt.now(), Guard: GuardPerson, Action: ActionPaused,
		Member: member, Chain: chain, By: by, Reason: "paused by " + by + ": " + reason}); err != nil {
		return fmt.Errorf("talkoot: paused until the daemon restarts, because the room could not record the pause: %w", err)
	}
	return nil
}

// Resume lifts a pause by the same scope as Pause and delivers what waited.
// It lifts a person's pause first. A cap or a guard under it needs a second
// resume, after the status has named it.
//
// 🚨 The resume is recorded before anything is released. A resume the room
// lost would come back as a pause after a restart, with its deliveries
// already sent.
func (rt *Router) Resume(by, member, chain string) error {
	rt.mu.Lock()
	if err := rt.scopeCheck(by, member, chain); err != nil {
		rt.mu.Unlock()
		return err
	}
	personOnly := rt.pauses.has(scopeOf(member, chain), pausePerson)
	l := Line{Type: LineResume, At: rt.now(), Member: member, Chain: chain, By: by}
	if personOnly {
		l.Guard = GuardPerson
	}
	if err := rt.room.Append(l); err != nil {
		rt.mu.Unlock()
		return err
	}
	rt.resumeLocked(member, chain, personOnly)
	ds := rt.releaseLocked()
	rt.mu.Unlock()
	rt.dispatch(ds)
	return nil
}

// maxReasonBytes bounds a person's pause reason, which the room and every
// status carry.
const maxReasonBytes = 1024

// scopeCheck bounds what Pause and Resume write to the room: a person's name,
// and a scope that names a member of the roster, a known chain, or neither.
func (rt *Router) scopeCheck(by, member, chain string) error {
	if name, ok := strings.CutPrefix(by, HumanPrefix); !ok || !tokenPattern.MatchString(name) {
		return fmt.Errorf("talkoot: %q is not human: and a name of 1 to 64 letters, digits, and . _ @ -", by)
	}
	switch {
	case member != "" && chain != "":
		return errors.New("talkoot: name a member or a chain, not both")
	case member != "":
		if _, ok := rt.roster.member(member); !ok {
			return fmt.Errorf("talkoot: %q is not a member of %s", member, rt.roster.ID)
		}
	case chain != "":
		if _, ok := rt.chains[chain]; !ok {
			return fmt.Errorf("talkoot: %q is not a chain in %s", chain, rt.roster.ID)
		}
	}
	return nil
}

// Status is a member's state as the router sees it.
type Status struct {
	Member   string
	Working  bool
	Paused   string
	SpendUSD float64
	Turns    int
}

// Members returns the roster's members, in roster order.
func (rt *Router) Members() []Member {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return slices.Clone(rt.roster.Members)
}

// Statuses reports every member, in roster order.
func (rt *Router) Statuses() []Status {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.roll(rt.now())
	out := make([]Status, 0, len(rt.roster.Members))
	for _, m := range rt.roster.Members {
		var why []string
		for _, s := range []string{talkootScope, memberScope(m.ID)} {
			if r := rt.pauses.reason(s); r != "" {
				why = append(why, r)
			}
		}
		out = append(out, Status{Member: m.ID, Working: rt.busy(m.ID), Paused: strings.Join(why, "; "),
			SpendUSD: rt.spend[m.ID], Turns: rt.turns[m.ID]})
	}
	return out
}

// Held lists the member ids with a delivery waiting, sorted, for a surface to
// show why a member has not answered.
func (rt *Router) Held() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, p := range rt.held {
		if !seen[p.to] {
			seen[p.to] = true
			out = append(out, p.to)
		}
	}
	sort.Strings(out)
	return out
}
