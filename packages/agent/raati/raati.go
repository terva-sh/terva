// Package raati convenes a deliberation panel over the swarm dispatch
// engine: N role-bound units receive one question, deliberate BLIND
// (no unit sees another in round one), then cross-examine the revealed
// provisional ballots and cast finals, which packages/vote tallies
// under the invocation's decision class. The recorded dissent — not
// the verdict — is the primary artifact; see
// docs/proposals/raati-deliberation.md.
//
// This is the third coordination front-end over the engine, after the
// coding skin's fire-and-forget recap and the play skin's
// director-pull: a PHASED BARRIER. The coordinator holds until every
// unit finishes the round (or times out — the engine imposes no turn
// timeout of its own, so the deadline lives here, and a unit that
// misses it ABSTAINS as an absent ballot rather than blocking the
// panel). Units are spawned tool-less (Experience "chat"): panelists
// are evaluators, not actors — evidence is assembled by the convener
// and travels in the prompt.
package raati

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/vote"
)

// Unit is one panelist seat: a display name, the trusted persona the
// unit boots as (a name resolved against the persona library, never a
// path — the same trust rule as swarm_spawn), and optionally an exact
// model binding for this seat. A unit binding overrides the panel-wide
// Config binding; both are exact pins (provider and model together, or
// neither). Per-seat bindings are what the rigor ladder is made of:
// level 1 seats one provider's tier ladder, level 2 seats three
// providers — see docs/proposals/raati-deliberation.md.
type Unit struct {
	Name     string `json:"name"`
	Persona  string `json:"persona"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	// Reasoning is the thinking effort this seat runs at (off | minimum |
	// low | medium | high | maximum). Empty leaves it to the unit's own
	// default. It is part of the BINDING, not a detail of it: a reasoning
	// model with thinking off is a materially different judge from the same
	// weights at high, which is what lets one model span a ladder.
	Reasoning string `json:"reasoning,omitempty"`
}

// BindingLabel renders the seat's effective binding for records and
// boards ("provider/model", plus the effort when the seat pins one), or
// "" when the seat inherits.
//
// The effort belongs in the label for the same reason the model does: the
// seats line is how a reader of a verdict knows what actually judged their
// question. On a one-model ladder it is the ONLY thing distinguishing the
// seats, and a label that omitted it would render three identical panelists.
func (u Unit) BindingLabel() string {
	if u.Provider == "" && u.Model == "" {
		return ""
	}
	label := u.Provider + "/" + u.Model
	if u.Reasoning != "" {
		label += " @" + u.Reasoning
	}
	return label
}

// Binding is an exact provider+model pin for one seat — what a rigor
// level resolves to before the panel is convened.
type Binding struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Reasoning is the thinking effort for this seat; empty inherits.
	// Two bindings that differ ONLY here are still two different judges.
	Reasoning string `json:"reasoning,omitempty"`
}

// SameWeights reports whether every binding in a pool is the same
// provider+model — a pool that decorrelates by thinking effort alone, or
// not at all.
//
// This is the question the honesty rules turn on, so it is asked once here
// rather than re-derived at each level. An empty or single-seat pool is
// trivially "same": there is nothing for a second opinion to disagree with.
func SameWeights(pool []Binding) bool {
	if len(pool) < 2 {
		return true
	}
	for _, b := range pool[1:] {
		if b.Provider != pool[0].Provider || b.Model != pool[0].Model {
			return false
		}
	}
	return true
}

// SeatOrder is how the binding pool maps onto panel seats.
type SeatOrder string

const (
	// SeatOrderFixed assigns pool[i] to seat i (or through SeatMap when
	// set). The panel develops a stable character: the same model holds
	// the same prior every convening — comparable across runs, but a
	// model's idiosyncrasies fuse with its seat.
	SeatOrderFixed SeatOrder = "fixed"
	// SeatOrderConvene (the default) shuffles the assignment once per
	// convening; seats keep their binding across both rounds, so
	// per-unit prompt cache still works, but no model owns a prior
	// across deliberations.
	SeatOrderConvene SeatOrder = "convene"
	// SeatOrderTurn reshuffles per voting round: the seat persists, the
	// weights behind it rotate mid-deliberation (round two respawns
	// each present seat cold on its new binding, carrying the seat's
	// provisional ballot). Strongest defense against fixed model↔prior
	// bias, at a real price: no prompt-cache reuse across rounds and
	// the question + evidence re-read per seat per round.
	SeatOrderTurn SeatOrder = "turn"
)

// SameEffort reports whether every binding runs at the same thinking
// effort. Together with SameWeights it separates "three copies of one
// judge" from "one model deliberately spanning a ladder".
func SameEffort(pool []Binding) bool {
	if len(pool) < 2 {
		return true
	}
	for _, b := range pool[1:] {
		if b.Reasoning != pool[0].Reasoning {
			return false
		}
	}
	return true
}

// SeatOrderFor parses a configured seat order AND applies the one default
// that depends on the panel: a pool whose seats share weights and differ
// only in thinking effort reshuffles PER TURN rather than once per
// convening.
//
// The reason is what such a ladder is made of. With three different models,
// "no model owns a prior across deliberations" is enough — the weights
// disagree on their own. With one model at three efforts, the only thing
// distinguishing the seats IS the effort, so holding it fixed for a whole
// deliberation fuses "the benevolence seat" with "the one that wasn't
// thinking" for every round of it. Rotating per turn is what keeps the
// effort a property of the round instead of a property of the prior.
//
// Only the DEFAULT is upgraded. An operator who wrote "convene" gets
// convene: turn costs real prompt cache (round two respawns every seat cold)
// and that trade is theirs to refuse.
func SeatOrderFor(configured string, pool []Binding) (SeatOrder, error) {
	order, err := ParseSeatOrder(configured)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(configured) == "" && len(pool) > 1 && SameWeights(pool) && !SameEffort(pool) {
		return SeatOrderTurn, nil
	}
	return order, nil
}

// ParseSeatOrder maps a user-supplied seat order onto a SeatOrder;
// empty means the SeatOrderConvene default.
func ParseSeatOrder(s string) (SeatOrder, error) {
	switch SeatOrder(strings.ToLower(strings.TrimSpace(s))) {
	case SeatOrderConvene, "":
		return SeatOrderConvene, nil
	case SeatOrderFixed:
		return SeatOrderFixed, nil
	case SeatOrderTurn:
		return SeatOrderTurn, nil
	}
	return "", i18n.Errorf("unknown seat order %q (fixed, convene, or turn)", s)
}

// DefaultPanel is the shipped three-unit cast — the Imperial Regalia
// (see personas/builtin/raati-crew/): truth, decisiveness,
// benevolence. Returned fresh so callers may edit their copy.
func DefaultPanel() []Unit {
	return []Unit{
		{Name: "YATA-1", Persona: "raati-crew:yata"},
		{Name: "KUSANAGI-2", Persona: "raati-crew:kusanagi"},
		{Name: "MAGATAMA-3", Persona: "raati-crew:magatama"},
	}
}

// DefaultVetoHolder is the seat that holds the veto when a veto-class
// deliberation doesn't name one: the benevolence unit, on the theory
// that the last word on "should we?" belongs to the panelist whose
// prior is the people it lands on.
const DefaultVetoHolder = "MAGATAMA-3"

// Class is the decision class of a deliberation; it selects the vote
// rule and, for gates, the failure posture (fail closed).
type Class string

const (
	// ClassAdvisory decides by majority with a 2-of-panel quorum;
	// dissent rides along as the minority report.
	ClassAdvisory Class = "advisory"
	// ClassGate requires unanimity to pass and fails closed: any
	// dissent, abstention, or absent unit means the gate does not open.
	ClassGate Class = "gate"
	// ClassVeto lets one designated unit block regardless of the tally;
	// otherwise it decides like an advisory majority.
	ClassVeto Class = "veto"
)

// ParseClass maps a user-supplied class string onto a Class.
func ParseClass(s string) (Class, error) {
	switch Class(strings.ToLower(strings.TrimSpace(s))) {
	case ClassAdvisory, "":
		return ClassAdvisory, nil
	case ClassGate:
		return ClassGate, nil
	case ClassVeto:
		return ClassVeto, nil
	}
	return "", i18n.Errorf("unknown decision class %q (advisory, gate, or veto)", s)
}

func (c Class) rule(vetoHolder string) vote.Rule {
	switch c {
	case ClassGate:
		return vote.Unanimity{}
	case ClassVeto:
		return vote.Veto{Holder: vetoHolder, Base: vote.Majority{}}
	default:
		return vote.Majority{}
	}
}

// UnitHandle is the slice of *swarm.Agent the barrier needs, extracted
// (like actor_spawn's actorHandle) so coordination is unit-testable
// without live subprocesses.
type UnitHandle interface {
	AgentID() string
	SetOnTurnEnd(fn func(step int, errMsg string))
	Wait()
	Err() error
	// Findings returns the unit's current answer text (the source the
	// ballot block is parsed from).
	Findings() string
}

// Engine is the slice of *swarm.Swarm a convening needs.
type Engine interface {
	SpawnUnit(ctx context.Context, req swarm.SpawnRequest) (UnitHandle, error)
	SendUserTurn(id, text string) error
	Stop(id string) error
}

// SwarmEngine adapts the real dispatch engine to Engine.
type SwarmEngine struct{ Swarm *swarm.Swarm }

func (e SwarmEngine) SpawnUnit(ctx context.Context, req swarm.SpawnRequest) (UnitHandle, error) {
	a, err := e.Swarm.SpawnReq(ctx, req)
	if err != nil {
		return nil, err
	}
	return swarmUnit{a}, nil
}
func (e SwarmEngine) SendUserTurn(id, text string) error { return e.Swarm.SendUserTurn(id, text) }
func (e SwarmEngine) Stop(id string) error               { return e.Swarm.Stop(id) }

type swarmUnit struct{ a *swarm.Agent }

func (u swarmUnit) AgentID() string                               { return u.a.ID }
func (u swarmUnit) SetOnTurnEnd(fn func(step int, errMsg string)) { u.a.SetOnTurnEnd(fn) }
func (u swarmUnit) Wait()                                         { u.a.Wait() }
func (u swarmUnit) Err() error                                    { return u.a.Err() }
func (u swarmUnit) Findings() string                              { return u.a.Snapshot().Findings() }

// EventKind names one structured deliberation state change.
type EventKind string

const (
	// EventSeated fires once per unit when its agent is spawned — and
	// again when the per-turn seat order reseats a unit for the final
	// round (same unit name, new agent id and binding), so consumers
	// upsert by unit name.
	EventSeated EventKind = "seated"
	// EventRound fires when a round opens: 1 = blind, 2 = cross-exam.
	EventRound EventKind = "round"
	// EventVoted fires when a unit's ballot for the current round is
	// parsed and counted.
	EventVoted EventKind = "voted"
	// EventAbsent fires once per unit, on the transition to absent
	// (timeout, crash, unparseable ballot, undeliverable round).
	EventAbsent EventKind = "absent"
	// EventDecided fires last, carrying the tallied outcome.
	EventDecided EventKind = "decided"
	// EventInquiry fires once per posed question after the inquiry gap
	// resolves it: Unit asked Why; Answer + Source say what came back
	// ("record" from the clerk, "convener" from a callback, or
	// "unanswered" — which is signal, not noise).
	EventInquiry EventKind = "inquiry"
)

// Event is one entry in the deliberation's structured state feed —
// what a live board renders. The narration in Config.Progress is the
// human-prose shadow of this feed.
type Event struct {
	Kind    EventKind     `json:"kind"`
	Unit    string        `json:"unit,omitempty"`
	AgentID string        `json:"agent_id,omitempty"`
	Binding string        `json:"binding,omitempty"` // seated: the seat's "provider/model"
	Round   int           `json:"round,omitempty"`
	Ballot  *vote.Ballot  `json:"ballot,omitempty"`
	Why     string        `json:"why,omitempty"`    // absent: cause; inquiry: the question
	Answer  string        `json:"answer,omitempty"` // inquiry: what came back
	Source  string        `json:"source,omitempty"` // inquiry: record | convener | unanswered
	Outcome *vote.Outcome `json:"outcome,omitempty"`
}

// Inquiry is one between-round question of the inquiry protocol: a
// unit poses it inside its ballot, the inquiry gap answers it (or
// records that it couldn't), and the pooled digest travels to every
// seat in the next round's prompt. An unanswered question is part of
// the record — a decision made with open questions is a different
// artifact than an informed one.
type Inquiry struct {
	Unit     string `json:"unit"`
	Question string `json:"question"`
	Answer   string `json:"answer,omitempty"`
	Source   string `json:"source,omitempty"` // record | convener | unanswered
	Round    int    `json:"round"`            // the round the question was posed in
}

// Inquiry sources.
const (
	SourceRecord     = "record"
	SourceConvener   = "convener"
	SourceUnanswered = "unanswered"
)

// Config parameterizes one convening.
type Config struct {
	Engine Engine
	// Units is the panel; nil means DefaultPanel(). A panel needs at
	// least two seats — a lone voice is an opinion, not a tally.
	Units []Unit
	Class Class
	// VetoHolder names the blocking seat for ClassVeto; empty means
	// DefaultVetoHolder when the default panel is used, otherwise it
	// must name a unit on the panel.
	VetoHolder string
	// Model and Provider, when set (both or neither — the engine
	// rejects a lone half), pin every unit to an exact binding. Level 0
	// is "all units share one binding"; per-unit bindings are the later
	// rigor levels.
	Model    string
	Provider string
	// Bindings, when set, is the seat pool (one entry per seat) and
	// switches assignment to SeatOrder policy; unit-level and
	// panel-level bindings above are ignored in pool mode.
	Bindings []Binding
	// SeatOrder maps the pool onto seats; empty means SeatOrderConvene.
	SeatOrder SeatOrder
	// SeatMap, for SeatOrderFixed only, remaps pool indices onto seats:
	// seat i draws Bindings[SeatMap[i]]. Must be a permutation of the
	// seat indices. Validated whenever set so typos surface; unused by
	// the shuffling orders.
	SeatMap []int
	// Perm, when set, replaces math/rand's permutation source for the
	// shuffling seat orders (tests inject determinism). Nil uses
	// rand.Perm.
	Perm func(n int) []int
	// RoundTimeout bounds each round per panel; a unit that misses it
	// abstains as absent and is stopped. 0 means 5 minutes.
	RoundTimeout time.Duration
	// SingleRound skips the cross-examination round: blind ballots are
	// final. The cheap eight-ball mode.
	SingleRound bool
	// MaxRounds caps the deliberation's voting rounds: 0 or 2 is the
	// two-round default; 3 permits ONE convergence round, run only when
	// a cross-examination ballot actually flipped (everyone revised
	// against stale positions — the extra round stabilizes the real
	// configuration; it is a fixed-point pass, not more debate).
	// Escalations stay escalated either way.
	MaxRounds int
	// AnswerInquiries, when set, enables the inquiry protocol: units
	// may pose up to two questions inside each ballot, and this
	// callback resolves the batch between rounds — filling Answer and
	// Source on each entry (an empty Source is recorded as unanswered).
	// The convener controls what it consults: the record, a human, its
	// own context. Nil disables solicitation; questions a unit volunteers
	// anyway are recorded unanswered.
	AnswerInquiries func(ctx context.Context, qs []Inquiry) []Inquiry
	// Progress, when set, receives one-line narration as the
	// deliberation advances. Never called after Convene returns.
	Progress func(msg string)
	// OnEvent, when set, receives the structured state feed (the
	// board's source of truth). Called synchronously from the
	// convening goroutine, in order — keep it fast and never block.
	// Never called after Convene returns.
	OnEvent func(ev Event)
}

// UnitRecord ties a seat to the spawned agent behind it (and the
// binding it deliberated on), so the persisted record can point back
// at transcripts and says which model held which prior. The flat
// fields are the FINAL-round seating; the Blind* fields are set only
// when the per-turn seat order reseated the unit, and point at the
// blind round's agent (its transcript lives there) and binding.
type UnitRecord struct {
	Name          string `json:"name"`
	Persona       string `json:"persona"`
	AgentID       string `json:"agent_id,omitempty"`
	Model         string `json:"model,omitempty"`
	Provider      string `json:"provider,omitempty"`
	BlindAgentID  string `json:"blind_agent_id,omitempty"`
	BlindModel    string `json:"blind_model,omitempty"`
	BlindProvider string `json:"blind_provider,omitempty"`
}

// Result is the full deliberation record: every round's ballots and
// the tallied outcome. It marshals cleanly for persistence. Middle is
// set only when a convergence round ran (Blind = round 1, Middle =
// round 2, Final = round 3) so the full revision path stays auditable;
// otherwise Final is round 2 (or round 1 when single-round).
type Result struct {
	Question  string        `json:"question"`
	Class     Class         `json:"class"`
	Units     []UnitRecord  `json:"units"`
	Blind     []vote.Ballot `json:"blind"`
	Middle    []vote.Ballot `json:"middle,omitempty"`
	Final     []vote.Ballot `json:"final"`
	Outcome   vote.Outcome  `json:"outcome"`
	Inquiries []Inquiry     `json:"inquiries,omitempty"`
}

// seat is one unit's live state across the rounds.
type seat struct {
	unit       Unit
	handle     UnitHandle
	terminated chan struct{}
	turnDone   <-chan string
	// absent, once set, is sticky: a unit that misses a round never
	// rejoins the deliberation (its remaining rounds are skipped).
	absent    bool
	absentWhy string
	// absentEventSent gates the EventAbsent transition so the feed
	// carries it exactly once even though absent ballots are recorded
	// again in every later round.
	absentEventSent bool
	// blind* remember the blind-round seating when the per-turn seat
	// order reseated this unit for the final round.
	blindAgentID  string
	blindModel    string
	blindProvider string
}

// watchTermination closes a fresh terminated channel when the handle's
// process exits. The handle and channel are captured BY VALUE: after a
// per-turn reseat replaces both on the seat, the old goroutine must
// close the OLD channel — closing through the seat pointer would let a
// dismissed blind-round child falsely "terminate" the new seat.
func (s *seat) watchTermination() {
	s.terminated = make(chan struct{})
	go func(h UnitHandle, term chan struct{}) {
		h.Wait()
		close(term)
	}(s.handle, s.terminated)
}

const defaultRoundTimeout = 5 * time.Minute

// Convene runs one full deliberation and returns its record. It
// returns an error only for operational failure (a seat that cannot
// be spawned, or the caller's ctx cancelled); units that fail DURING
// deliberation become absent ballots in the record instead — the
// panel degrades, the record says so, and fail-closed classes react
// through the vote rule, not through an error.
func Convene(ctx context.Context, cfg Config, question, evidence string) (*Result, error) {
	if cfg.Engine == nil {
		return nil, fmt.Errorf("raati: no engine")
	}
	// Own a copy: the binding merge below writes into the slice, and the
	// caller's panel must not change under them.
	units := append([]Unit(nil), cfg.Units...)
	if len(units) == 0 {
		units = DefaultPanel()
	}
	if len(units) < 2 {
		return nil, i18n.Errorf("a raati needs at least two units; got %d", len(units))
	}
	class := cfg.Class
	if class == "" {
		class = ClassAdvisory
	}
	holder := cfg.VetoHolder
	if class == ClassVeto {
		if holder == "" && cfg.Units == nil {
			holder = DefaultVetoHolder
		}
		if !panelHas(units, holder) {
			return nil, i18n.Errorf("veto holder %q is not on the panel", holder)
		}
	}
	timeout := cfg.RoundTimeout
	if timeout <= 0 {
		timeout = defaultRoundTimeout
	}
	maxRounds := cfg.MaxRounds
	if maxRounds == 0 {
		maxRounds = 2
	}
	if maxRounds != 2 && maxRounds != 3 {
		return nil, i18n.Errorf("max_rounds must be 2 or 3; got %d", cfg.MaxRounds)
	}
	if strings.TrimSpace(question) == "" {
		return nil, i18n.Errorf("a raati needs a question")
	}
	// Seat the panel. In pool mode the SeatOrder policy maps the pool
	// onto seats; otherwise each seat's explicit binding (or the
	// panel-wide one) applies. Either way a half-pinned seat fails the
	// whole convening up front, not one unit's spawn mid-panel.
	seatOrder, err := ParseSeatOrder(string(cfg.SeatOrder))
	if err != nil {
		return nil, err
	}
	if len(cfg.SeatMap) > 0 {
		if err := validSeatMap(cfg.SeatMap, len(units)); err != nil {
			return nil, err
		}
	}
	if len(cfg.Bindings) > 0 {
		if len(cfg.Bindings) != len(units) {
			return nil, i18n.Errorf("the binding pool has %d entries for %d seats", len(cfg.Bindings), len(units))
		}
		for i, b := range cfg.Bindings {
			if b.Provider == "" || b.Model == "" {
				return nil, i18n.Errorf("binding pool entry %d needs both provider and model", i)
			}
		}
		assignSeatBindings(units, cfg.Bindings, seatPerm(cfg, seatOrder, len(units)))
	} else {
		for i := range units {
			if units[i].Model == "" && units[i].Provider == "" {
				units[i].Model, units[i].Provider = cfg.Model, cfg.Provider
			}
			if (units[i].Model == "") != (units[i].Provider == "") {
				return nil, i18n.Errorf("%s: set model and provider together, or neither", units[i].Name)
			}
		}
	}

	// Convene: spawn every seat. A seat that cannot even be spawned is
	// an operational failure — stop whoever already sat down and bail.
	seats := make([]*seat, 0, len(units))
	stopAll := func() {
		for _, s := range seats {
			if s.handle != nil {
				_ = cfg.Engine.Stop(s.handle.AgentID())
			}
		}
	}
	inquire := cfg.AnswerInquiries != nil
	for _, u := range units {
		narrate(cfg, i18n.T("%s takes the seat…", u.Name))
		h, err := cfg.Engine.SpawnUnit(ctx, swarm.SpawnRequest{
			Task:       round1Prompt(u, len(units), class, question, evidence, inquire),
			Persona:    u.Persona,
			Model:      u.Model,
			Provider:   u.Provider,
			Reasoning:  u.Reasoning,
			Experience: "chat", // tool-less: panelists evaluate, they don't act
			// ...and an agent that cannot act cannot use a private checkout.
			// Under --swarm-worktrees every panelist was leasing a git worktree
			// it never wrote to, then releasing it — release keeps the tree for
			// review, which is right for a coding sub-agent and pointless for a
			// ballot. One convening left one worktree per seat, forever.
			SharedTree: true,
		})
		if err != nil {
			stopAll()
			return nil, fmt.Errorf("raati: spawning %s: %w", u.Name, err)
		}
		s := &seat{unit: u, handle: h}
		// Watcher after spawn: the spawn itself starts round one, so a
		// very fast turn could end before we listen. actor_spawn accepts
		// the same window (child startup + a model round trip vs
		// microseconds); the terminated channel and the round deadline
		// both backstop it.
		s.turnDone = installWatcher(h)
		s.watchTermination()
		seats = append(seats, s)
		emitEvent(cfg, Event{Kind: EventSeated, Unit: u.Name, AgentID: h.AgentID(), Binding: u.BindingLabel()})
	}
	defer stopAll() // units are daemons; nobody else will dismiss them

	// Round one: blind deliberation. Posed inquiries are collected per
	// seat, to be resolved in the inquiry gap AFTER the blind ballots
	// are cast — the blind round stays blind.
	emitEvent(cfg, Event{Kind: EventRound, Round: 1})
	posed := make([][]string, len(seats))
	blind, err := awaitRound(ctx, cfg, seats, 1, timeout, func(i int, s *seat, text string) (vote.Ballot, error) {
		b, qs, perr := parseBallot(s.unit.Name, text)
		posed[i] = qs
		return b, perr
	})
	if err != nil {
		return nil, err
	}

	var record []Inquiry // every inquiry posed, resolved or not

	// Round two: reveal + cross-examination + the round-1 inquiry
	// digest. Skipped when configured single-round or when fewer than
	// two seats are left to argue — a cross-examination needs someone
	// to cross.
	final := blind
	var middle []vote.Ballot
	if !cfg.SingleRound && presentSeats(seats) >= 2 {
		digest := resolveInquiries(ctx, cfg, 1, collectInquiries(seats, posed, 1), &record)
		// Round two may pose inquiries only when a convergence round
		// could still consume the answers.
		posed2 := make([][]string, len(seats))
		solicit2 := inquire && maxRounds >= 3
		narrate(cfg, i18n.T("blind round complete — the panel sees each other's ballots"))
		emitEvent(cfg, Event{Kind: EventRound, Round: 2})
		reseat := len(cfg.Bindings) > 0 && seatOrder == SeatOrderTurn
		var perm2 []int
		if reseat {
			perm2 = seatPerm(cfg, seatOrder, len(units))
		}
		for i, s := range seats {
			if s.absent {
				continue
			}
			if reseat {
				// The seat persists; the weights behind it rotate. The
				// blind child is done either way — dismiss it, remember
				// where its transcript lives, and spawn the final round
				// cold on the new binding, carrying the seat's
				// provisional ballot. A failed reseat degrades this seat,
				// not the whole panel: round one already happened.
				b := cfg.Bindings[perm2[i]]
				s.blindAgentID, s.blindProvider, s.blindModel = s.handle.AgentID(), s.unit.Provider, s.unit.Model
				_ = cfg.Engine.Stop(s.blindAgentID)
				s.unit.Provider, s.unit.Model, s.unit.Reasoning = b.Provider, b.Model, b.Reasoning
				h, rerr := cfg.Engine.SpawnUnit(ctx, swarm.SpawnRequest{
					Task:       round2ColdPrompt(s.unit, len(units), class, question, evidence, blind[i], others(blind, i), digest, solicit2),
					Persona:    s.unit.Persona,
					Model:      b.Model,
					Provider:   b.Provider,
					Reasoning:  b.Reasoning,
					Experience: "chat",
					SharedTree: true, // same as round one: a ballot needs no checkout
				})
				if rerr != nil {
					s.markAbsent(i18n.T("could not reseat for the final round: %s", rerr.Error()))
					continue
				}
				s.handle = h
				s.turnDone = installWatcher(h)
				s.watchTermination()
				narrate(cfg, i18n.T("%s reseats on %s for the final round…", s.unit.Name, s.unit.BindingLabel()))
				emitEvent(cfg, Event{Kind: EventSeated, Unit: s.unit.Name, AgentID: h.AgentID(), Binding: s.unit.BindingLabel()})
				continue
			}
			// Install before triggering so a fast reply can't slip past
			// the watcher (the reuse-path ordering from actor_spawn).
			s.turnDone = installWatcher(s.handle)
			if err := cfg.Engine.SendUserTurn(s.handle.AgentID(), round2Prompt(s.unit, others(blind, i), digest, solicit2)); err != nil {
				s.markAbsent(i18n.T("cross-examination round could not be delivered: %s", err.Error()))
			}
		}
		final, err = awaitRound(ctx, cfg, seats, 2, timeout, func(i int, s *seat, text string) (vote.Ballot, error) {
			b, qs, perr := parseBallot(s.unit.Name, text)
			posed2[i] = qs
			return b, perr
		})
		if err != nil {
			return nil, err
		}
		// A seat that voted blind but missed the final round abstains,
		// with its provisional position on the record rather than
		// silently promoted — for a gate that must fail closed.
		for i, s := range seats {
			if s.absent && !blind[i].Absent && final[i].Absent {
				final[i].Rationale = i18n.T("%s; blind ballot was %s", final[i].Rationale, string(blind[i].Verdict))
			}
		}

		// Convergence round: run ONLY when cross-examination flipped a
		// verdict — everyone revised against stale positions, and one
		// more reveal lets the panel respond to the configuration that
		// actually exists. A fixed-point pass, never more debate: no
		// reseat even under the per-turn order (new weights would add a
		// new prior, not stabilize this one), hard-capped at one.
		if maxRounds >= 3 && anyFlip(blind, final) && presentSeats(seats) >= 2 {
			digest2 := resolveInquiries(ctx, cfg, 2, collectInquiries(seats, posed2, 2), &record)
			narrate(cfg, i18n.T("cross-examination changed positions — the panel converges"))
			emitEvent(cfg, Event{Kind: EventRound, Round: 3})
			for i, s := range seats {
				if s.absent {
					continue
				}
				s.turnDone = installWatcher(s.handle)
				if err := cfg.Engine.SendUserTurn(s.handle.AgentID(), round3Prompt(others(final, i), digest2)); err != nil {
					s.markAbsent(i18n.T("convergence round could not be delivered: %s", err.Error()))
				}
			}
			conv, cerr := awaitRound(ctx, cfg, seats, 3, timeout, func(i int, s *seat, text string) (vote.Ballot, error) {
				b, _, perr := parseBallot(s.unit.Name, text)
				return b, perr
			})
			if cerr != nil {
				return nil, cerr
			}
			for i, s := range seats {
				if s.absent && !final[i].Absent && conv[i].Absent {
					conv[i].Rationale = i18n.T("%s; blind ballot was %s", conv[i].Rationale, string(final[i].Verdict))
				}
			}
			middle, final = final, conv
		} else {
			// Round-2 questions with no round left to inform still enter
			// the record: the panel decided with these open.
			for _, q := range collectInquiries(seats, posed2, 2) {
				q.Source = SourceUnanswered
				record = append(record, q)
				emitEvent(cfg, Event{Kind: EventInquiry, Unit: q.Unit, Round: q.Round, Why: q.Question, Source: q.Source})
			}
		}
	} else {
		// No further round will consume answers; record blind-round
		// questions as open.
		for _, q := range collectInquiries(seats, posed, 1) {
			q.Source = SourceUnanswered
			record = append(record, q)
			emitEvent(cfg, Event{Kind: EventInquiry, Unit: q.Unit, Round: q.Round, Why: q.Question, Source: q.Source})
		}
	}

	outcome := vote.Tally(final, class.rule(holder))
	narrate(cfg, i18n.T("the raati has decided: %s", string(outcome.Decision)))
	emitEvent(cfg, Event{Kind: EventDecided, Outcome: &outcome})

	res := &Result{Question: question, Class: class, Blind: blind, Middle: middle, Final: final, Outcome: outcome, Inquiries: record}
	for _, s := range seats {
		res.Units = append(res.Units, UnitRecord{
			Name: s.unit.Name, Persona: s.unit.Persona, AgentID: s.handle.AgentID(),
			Model: s.unit.Model, Provider: s.unit.Provider,
			BlindAgentID: s.blindAgentID, BlindModel: s.blindModel, BlindProvider: s.blindProvider,
		})
	}
	return res, nil
}

// awaitRound is the phased barrier: it holds until every non-absent
// seat's turn ends (or the shared round deadline passes), then
// extracts one ballot per seat. Already-absent seats yield absent
// ballots without being waited on. It returns an error only when the
// caller's ctx is cancelled — a timeout is a deliberation outcome
// (absent seats), not a failure.
func awaitRound(ctx context.Context, cfg Config, seats []*seat, round int, timeout time.Duration, extract func(int, *seat, string) (vote.Ballot, error)) ([]vote.Ballot, error) {
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ballots := make([]vote.Ballot, len(seats))
	for i, s := range seats {
		if !s.absent {
			s.awaitTurn(deadline, cfg)
		}
		if !s.absent {
			b, err := extract(i, s, s.handle.Findings())
			if err != nil {
				s.markAbsent(i18n.T("unparseable ballot: %s", err.Error()))
			} else {
				narrate(cfg, i18n.T("%s has cast its ballot: %s (confidence %.2f)", s.unit.Name, string(b.Verdict), b.Confidence))
				ballots[i] = b
				bb := b
				emitEvent(cfg, Event{Kind: EventVoted, Unit: s.unit.Name, Round: round, Ballot: &bb})
				continue
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err // the convening itself was cancelled
		}
		narrate(cfg, i18n.T("%s is absent: %s", s.unit.Name, s.absentWhy))
		ballots[i] = vote.AbsentBallot(s.unit.Name, s.absentWhy)
		if !s.absentEventSent {
			s.absentEventSent = true
			emitEvent(cfg, Event{Kind: EventAbsent, Unit: s.unit.Name, Round: round, Why: s.absentWhy})
		}
	}
	return ballots, nil
}

func emitEvent(cfg Config, ev Event) {
	if cfg.OnEvent != nil {
		cfg.OnEvent(ev)
	}
}

// awaitTurn blocks until this seat's turn ends, the unit terminates,
// or the round deadline passes; the two failure legs mark the seat
// absent (and a deadline miss also stops the wedged unit — the engine
// has no turn timeout of its own).
func (s *seat) awaitTurn(round context.Context, cfg Config) {
	// A turn that already ended counts even when the round deadline
	// expired while the barrier waited on an earlier seat: the deadline
	// disqualifies units that hadn't voted, never ones that had (a
	// blocking select would pick randomly between the two ready
	// channels).
	select {
	case errMsg := <-s.turnDone:
		s.noteTurn(errMsg)
		return
	default:
	}
	select {
	case errMsg := <-s.turnDone:
		s.noteTurn(errMsg)
	case <-s.terminated:
		why := i18n.T("the unit exited before voting")
		if err := s.handle.Err(); err != nil {
			why = i18n.T("the unit exited before voting: %s", err.Error())
		}
		s.markAbsent(why)
	case <-round.Done():
		s.markAbsent(i18n.T("timed out after %s", cfg.roundTimeoutLabel()))
		_ = cfg.Engine.Stop(s.handle.AgentID())
	}
}

func (s *seat) noteTurn(errMsg string) {
	if errMsg != "" {
		s.markAbsent(i18n.T("the unit could not deliberate: %s", errMsg))
	}
}

func (s *seat) markAbsent(why string) {
	if s.absent {
		return // first cause wins; later rounds don't rewrite history
	}
	s.absent = true
	s.absentWhy = why
}

func (c Config) roundTimeoutLabel() string {
	t := c.RoundTimeout
	if t <= 0 {
		t = defaultRoundTimeout
	}
	return t.String()
}

// installWatcher registers a one-shot listener for the unit's next
// task-level turn end on a buffered channel (a turn that finishes
// before the barrier looks still delivers).
func installWatcher(h UnitHandle) <-chan string {
	turnDone := make(chan string, 1)
	h.SetOnTurnEnd(func(step int, errMsg string) {
		select {
		case turnDone <- errMsg:
		default:
		}
	})
	return turnDone
}

// assignSeatBindings maps the pool onto seats through a permutation:
// seat i draws pool[perm[i]].
func assignSeatBindings(units []Unit, pool []Binding, perm []int) {
	for i := range units {
		b := pool[perm[i]]
		units[i].Provider, units[i].Model, units[i].Reasoning = b.Provider, b.Model, b.Reasoning
	}
}

// seatPerm draws the seat permutation for one assignment: identity (or
// the user's SeatMap) for the fixed order, a shuffle for the others.
// SeatOrderConvene callers draw once and keep seats stable across
// rounds; SeatOrderTurn callers draw again for the final round.
func seatPerm(cfg Config, order SeatOrder, n int) []int {
	if order == SeatOrderFixed {
		if len(cfg.SeatMap) > 0 {
			return cfg.SeatMap
		}
		perm := make([]int, n)
		for i := range perm {
			perm[i] = i
		}
		return perm
	}
	if cfg.Perm != nil {
		return cfg.Perm(n)
	}
	return rand.Perm(n)
}

// validSeatMap requires a permutation of the seat indices — every seat
// draws exactly one pool entry, every pool entry is drawn.
func validSeatMap(m []int, seats int) error {
	if len(m) != seats {
		return i18n.Errorf("seat_map has %d entries for %d seats", len(m), seats)
	}
	seen := make([]bool, seats)
	for _, idx := range m {
		if idx < 0 || idx >= seats || seen[idx] {
			return i18n.Errorf("seat_map must be a permutation of 0–%d; got %v", seats-1, m)
		}
		seen[idx] = true
	}
	return nil
}

func presentSeats(seats []*seat) int {
	n := 0
	for _, s := range seats {
		if !s.absent {
			n++
		}
	}
	return n
}

func panelHas(units []Unit, name string) bool {
	for _, u := range units {
		if u.Name == name {
			return true
		}
	}
	return false
}

// collectInquiries flattens the per-seat questions of one round into
// the docket the inquiry gap resolves. Absent seats' questions are
// dropped: their ballots didn't count either.
func collectInquiries(seats []*seat, posed [][]string, round int) []Inquiry {
	var out []Inquiry
	for i, s := range seats {
		if s.absent {
			continue
		}
		for _, q := range posed[i] {
			out = append(out, Inquiry{Unit: s.unit.Name, Question: q, Round: round})
		}
	}
	return out
}

// resolveInquiries runs the inquiry gap for one round's docket: the
// convener's callback fills answers, every entry lands on the record
// (empty or unknown sources normalize to unanswered — the clerk never
// gets to be vague), events fire, and the pooled digest for the next
// round's prompts comes back. Empty docket or no callback: no gap.
func resolveInquiries(ctx context.Context, cfg Config, round int, docket []Inquiry, record *[]Inquiry) string {
	if len(docket) == 0 {
		return ""
	}
	if cfg.AnswerInquiries == nil {
		for i := range docket {
			docket[i].Source = SourceUnanswered
		}
	} else {
		narrate(cfg, i18n.T("the clerk takes the panel's questions…"))
		docket = cfg.AnswerInquiries(ctx, docket)
		for i := range docket {
			if docket[i].Source != SourceRecord && docket[i].Source != SourceConvener {
				docket[i].Source = SourceUnanswered
				docket[i].Answer = ""
			}
		}
	}
	for _, q := range docket {
		*record = append(*record, q)
		emitEvent(cfg, Event{Kind: EventInquiry, Unit: q.Unit, Round: q.Round, Why: q.Question, Answer: q.Answer, Source: q.Source})
	}
	return inquiryDigest(docket)
}

// anyFlip reports whether any seat present in both rounds changed its
// verdict — the convergence round's trigger.
func anyFlip(prev, cur []vote.Ballot) bool {
	for i := range prev {
		if !prev[i].Absent && !cur[i].Absent && prev[i].Verdict != cur[i].Verdict {
			return true
		}
	}
	return false
}

// others returns every ballot except index i — the reveal a panelist
// receives in the cross-examination round.
func others(ballots []vote.Ballot, i int) []vote.Ballot {
	out := make([]vote.Ballot, 0, len(ballots)-1)
	for j, b := range ballots {
		if j != i {
			out = append(out, b)
		}
	}
	return out
}

func narrate(cfg Config, msg string) {
	if cfg.Progress != nil {
		cfg.Progress(msg)
	}
}

// MarshalRecord renders the result as the persisted JSON record.
func (r *Result) MarshalRecord() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}
