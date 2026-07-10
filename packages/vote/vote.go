// Package vote is the pure tally primitive under RAATI deliberations
// (docs/proposals/raati-deliberation.md): ballots in, a decision rule
// applied, an outcome with a minority report out.
//
// The package deliberately knows nothing about agents, models, or
// deliberation protocols — a Ballot could come from an LLM unit, an
// extension, or a test table. This is a leaf package: it must import
// nothing from packages/agent so that future consumers (recap
// verification, extension-defined panels) can depend on it freely.
//
// Two design commitments worth naming:
//
//   - The minority report is a first-class output, not a log line. A
//     unanimous outcome is cheap to accept; a split is a flag that a
//     differently-biased evaluator found something. Callers are
//     expected to surface Outcome.Minority, not just Decision.
//   - Absence is not abstention. A unit that deliberately abstains is
//     present (counts toward quorum, appears on the record); a unit
//     that timed out or errored is Absent — its synthesized ballot
//     abstains AND degrades the outcome. Rules that fail closed treat
//     absence as grounds to escalate.
package vote

import "fmt"

// Verdict is a single unit's position on the question.
type Verdict string

const (
	Approve Verdict = "approve"
	Reject  Verdict = "reject"
	Abstain Verdict = "abstain"
)

// ParseVerdict maps a unit's reported verdict string onto a Verdict.
// It is deliberately forgiving about case but NOT about vocabulary:
// anything outside the three canonical words is an error, so a
// coordinator can distinguish "the unit abstained" from "the unit
// produced something unparseable" (which should become an Absent
// ballot, not a silent abstention).
func ParseVerdict(s string) (Verdict, error) {
	switch Verdict(lower(s)) {
	case Approve:
		return Approve, nil
	case Reject:
		return Reject, nil
	case Abstain:
		return Abstain, nil
	}
	return "", fmt.Errorf("vote: unknown verdict %q", s)
}

// lower is a dependency-free ASCII ToLower (the verdict vocabulary is
// ASCII by construction).
func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// Ballot is one unit's cast position. Unit is the unit's display name
// ("YATA-1"); Confidence is the unit's self-reported 0–1 confidence
// and is recorded, not weighed — rules count heads, they do not
// multiply feelings. Absent marks a ballot synthesized by the
// coordinator for a unit that timed out or errored: its Verdict is
// Abstain by construction and it does not count toward quorum.
type Ballot struct {
	Unit       string  `json:"unit"`
	Verdict    Verdict `json:"verdict"`
	Confidence float64 `json:"confidence"`
	Rationale  string  `json:"rationale,omitempty"`
	Absent     bool    `json:"absent,omitempty"`
}

// AbsentBallot builds the synthesized ballot for a unit that never
// answered (timeout, error, unparseable output). rationale should say
// which ("timed out after 120s", "unparseable ballot"), because the
// record is the product.
func AbsentBallot(unit, rationale string) Ballot {
	return Ballot{Unit: unit, Verdict: Abstain, Rationale: rationale, Absent: true}
}

// Decision is the tallied result of a deliberation.
//
// Escalated is a real outcome, not an error: the rule could not
// produce a definite verdict (tie, quorum failure, a fail-closed rule
// missing a unit) and a human should look. Gate-style callers treat
// Escalated exactly like Rejected for pass/fail purposes — fails
// closed — but the record distinguishes "a unit said no" from "we
// don't know".
type Decision string

const (
	Approved  Decision = "approved"
	Rejected  Decision = "rejected"
	Escalated Decision = "escalated"
)

// Counts is the head count behind an outcome. Absent units appear in
// Absent only — their synthesized abstentions are not double-counted
// in Abstain.
type Counts struct {
	Approve int `json:"approve"`
	Reject  int `json:"reject"`
	Abstain int `json:"abstain"`
	Absent  int `json:"absent"`
}

func count(ballots []Ballot) Counts {
	var c Counts
	for _, b := range ballots {
		if b.Absent {
			c.Absent++
			continue
		}
		switch b.Verdict {
		case Approve:
			c.Approve++
		case Reject:
			c.Reject++
		default:
			c.Abstain++
		}
	}
	return c
}

// present reports how many units cast a ballot themselves (including
// deliberate abstentions).
func (c Counts) present() int { return c.Approve + c.Reject + c.Abstain }

// Rule turns counted ballots into a decision. Rules are tiny and
// pluggable on purpose — the bookkeeping every rule shares (counts,
// minority report, degraded flag) lives in Tally, not in rules.
type Rule interface {
	// Name identifies the rule on the persisted record ("majority",
	// "unanimity", "veto").
	Name() string
	// Decide returns the decision for the given ballots. Implementations
	// may assume Tally has not filtered anything: absent ballots are
	// included so fail-closed rules can see them.
	Decide(ballots []Ballot) Decision
}

// Majority decides by simple head count among present, definite
// (approve/reject) ballots. Quorum is the minimum number of present
// units required to decide at all; below it, or on a tie, the outcome
// escalates. The zero value (Quorum 0) is normalized to a quorum of 2,
// the sensible floor for any panel — a lone voice is an opinion, not
// a tally.
type Majority struct {
	Quorum int `json:"quorum"`
}

func (Majority) Name() string { return "majority" }

func (m Majority) Decide(ballots []Ballot) Decision {
	q := m.Quorum
	if q <= 0 {
		q = 2
	}
	c := count(ballots)
	if c.present() < q {
		return Escalated
	}
	switch {
	case c.Approve > c.Reject:
		return Approved
	case c.Reject > c.Approve:
		return Rejected
	default:
		return Escalated
	}
}

// Unanimity is the gate rule: every unit must be present and approve.
// Any reject is a Rejected (a unit said no); any absence or
// abstention without a reject is an Escalated (we don't know). Both
// fail a gate — the distinction is for the record and the humans
// reading it.
type Unanimity struct{}

func (Unanimity) Name() string { return "unanimity" }

func (Unanimity) Decide(ballots []Ballot) Decision {
	if len(ballots) == 0 {
		return Escalated
	}
	c := count(ballots)
	switch {
	case c.Reject > 0:
		return Rejected
	case c.Absent > 0 || c.Abstain > 0:
		return Escalated
	default:
		return Approved
	}
}

// Veto wraps a base rule with one designated unit's power to block: if
// the holder casts a reject, the outcome is Rejected regardless of the
// tally. A holder who is absent has not exercised the veto (absence is
// not a no), but the base rule still sees the absence and may
// escalate on it. The zero-value Base is normalized to Majority{}.
type Veto struct {
	Holder string `json:"holder"`
	Base   Rule   `json:"-"`
}

func (Veto) Name() string { return "veto" }

func (v Veto) Decide(ballots []Ballot) Decision {
	for _, b := range ballots {
		if b.Unit == v.Holder && !b.Absent && b.Verdict == Reject {
			return Rejected
		}
	}
	base := v.Base
	if base == nil {
		base = Majority{}
	}
	return base.Decide(ballots)
}

// Outcome is the full result of a tally: what was decided, under which
// rule, who counted how, who disagreed, and whether the panel was
// whole. Minority holds the present, definite ballots whose verdict
// differs from the decision — under a veto that can be the outvoted
// majority, which is exactly when the record matters most. For an
// Escalated decision Minority is empty: nothing prevailed, so nothing
// dissents; callers show all ballots.
type Outcome struct {
	Decision Decision `json:"decision"`
	Rule     string   `json:"rule"`
	Tally    Counts   `json:"tally"`
	Minority []Ballot `json:"minority,omitempty"`
	Degraded bool     `json:"degraded,omitempty"`
}

// Tally applies rule to ballots and assembles the outcome record.
func Tally(ballots []Ballot, rule Rule) Outcome {
	c := count(ballots)
	out := Outcome{
		Decision: rule.Decide(ballots),
		Rule:     rule.Name(),
		Tally:    c,
		Degraded: c.Absent > 0,
	}
	if out.Decision == Escalated {
		return out
	}
	against := Reject
	if out.Decision == Rejected {
		against = Approve
	}
	for _, b := range ballots {
		if !b.Absent && b.Verdict == against {
			out.Minority = append(out.Minority, b)
		}
	}
	return out
}
