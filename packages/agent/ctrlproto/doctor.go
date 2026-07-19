package ctrlproto

import "context"

// DoctorController is OPTIONAL (like CardsController): the LLM "card doctor" that
// reads a stored card plus its deterministic lint and proposes concrete,
// per-field edits. Served only by the real Workspace (it needs a model and the
// card library); a carrier without one answers "unsupported" rather than
// failing deeper. Stage-only today, so it rides ctrlclient's notForwarded
// allow-list rather than the Go forwarder.
type DoctorController interface {
	// CardsDoctor runs the card doctor over a stored card and returns structured
	// per-field edit proposals. Decisions carries the user's accept/decline
	// verdicts on a prior round (empty on the first pass) so the doctor can revise
	// or withdraw a proposal in light of a decline reason — the negotiation.
	CardsDoctor(ctx context.Context, p DoctorParams) (DoctorResult, error)
}

// DoctorParams names the card to examine and, on a follow-up round, the user's
// verdicts on the previous proposals.
type DoctorParams struct {
	ID        string           `json:"id"`
	Decisions []DoctorDecision `json:"decisions,omitempty"`
}

// DoctorDecision is the user's verdict on one prior proposal: accepted, or
// declined with a reason the doctor should weigh when it revises. Field/Rationale
// echo the proposal so the follow-up prompt is self-contained.
type DoctorDecision struct {
	ProposalID string `json:"proposal_id"`
	Field      string `json:"field,omitempty"`
	Rationale  string `json:"rationale,omitempty"`
	Accepted   bool   `json:"accepted"`
	Reason     string `json:"reason,omitempty"`
}

// DoctorProposal is one suggested edit: replace Field's current value (Before)
// with After, for the stated Rationale. Severity mirrors the lint vocabulary
// ("warn"/"info") plus "suggestion" for an improvement the deterministic lint
// didn't flag. Field is one of the editable card text fields.
type DoctorProposal struct {
	ID        string `json:"id"`
	Field     string `json:"field"`
	Severity  string `json:"severity"`
	Rationale string `json:"rationale"`
	Before    string `json:"before"`
	After     string `json:"after"`
}

// DoctorResult is the payload of cards.doctor.
type DoctorResult struct {
	Proposals []DoctorProposal `json:"proposals"`
	// Note is the doctor's optional overall remark (e.g. "the card is already in
	// good shape" when it proposes nothing), shown above the proposals.
	Note string `json:"note,omitempty"`
}
