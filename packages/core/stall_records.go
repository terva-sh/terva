package core

// The stuck-loop detector is a component (packages/core/stall), but the records
// it produces are the engine's vocabulary: EvStall and EvEscalation carry them,
// the wire names them, and a session store writes them as rows
// (AttachTranscriptStore). A store must not depend on a component to read its
// own rows, so the types stay here (docs/plans/stall-component.md, decision 3).

// StallRecord is what a stuck-loop detector did about a loop, carried by
// EvStall. Hosts persist it as a "stall" session row, so rung 1 of the
// stuck-loop hatch (the nudge) is visible after the fact the way an escalation
// (rung 3) is via EscalationRecord. Written whenever detection nudges: unlike
// escalation it needs no configured target, so it records for every session that
// leaves the (default-on) detector running and sees a model repeat itself.
type StallRecord struct {
	Axis   string // "spin" (same call repeated) | "churn" (same failure repeated)
	Tool   string // the tool the model looped on
	Detail string // the repeated error/guard slice; empty for spin
	// Rung counts how far the detector went for this loop, not the proposal's
	// hatch-rung number (where 2 is the human ask): 1 = the first nudge, 2 = the
	// firmer hold-off that follows when the loop outlives it and the hatch's later
	// rungs cannot act, 3 = a call refused rather than dispatched, 4 = the turn
	// ended because the refusals were ignored too. Zero on records written before
	// the hold-off existed, so a reader treats absent as 1.
	//
	// 1 and 2 are things terva SAID; 3 and 4 are things it DID. Reading the split
	// out of a session log is the whole point of recording the number: it answers
	// how often talking was enough.
	Rung int
}

// EscalationDisposition is how an escalation decision resolved. It is recorded so
// a reader of the session log can tell one apart from the others.
type EscalationDisposition string

const (
	// EscalationSwitched: the swap landed and the loop continues on the stronger model.
	EscalationSwitched EscalationDisposition = "switched"
	// EscalationDeclined: the user chose to keep trying, or the host was already on
	// the target — no swap, the turn continues on the current model.
	EscalationDeclined EscalationDisposition = "declined"
	// EscalationStopped: the user chose to end the turn at the offer.
	EscalationStopped EscalationDisposition = "stopped"
	// EscalationFailed: the swap was attempted but errored (no credential, unknown
	// model); non-fatal, the turn continues on the current model.
	EscalationFailed EscalationDisposition = "failed"
)

// EscalationRecord is what an escalation decision produced, carried by
// EvEscalation. It is the payload hosts persist as an "escalation"
// session row: the swap the hatch performs writes only a "meta" row (via
// UpdateModel), which is byte-identical to a user /model switch — this record is
// the provenance that meta row cannot carry. Emitted only once a target is
// configured (the inert path records nothing), and for every disposition, not
// just successful swaps: a declined or failed escalation is as worth knowing as
// a completed one.
type EscalationRecord struct {
	Reason      string // the stall prose ("stuck on task_update ×5: <error>")
	Tool        string // the tool the model looped on
	FromModel   string // the model in play when the loop was detected (pre-swap)
	ToProvider  string // the configured escalation target
	ToModel     string
	Auto        bool                  // true = swapped under the auto policy, unasked
	Disposition EscalationDisposition // switched | declined | stopped | failed
	Detail      string                // failure error or host note; optional
}
