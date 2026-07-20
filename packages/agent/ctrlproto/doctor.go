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

	// SessionsDoctor runs the session doctor (the Dramaturgi persona) over a live
	// immersive session and returns typed proposals — lore entries, open threads,
	// cast promotions, scene-state updates — each applying through a verb that
	// already exists. The same Decisions negotiation loop as the card doctor. The
	// session rides the frame, like suggest.
	SessionsDoctor(ctx context.Context, sess string, p SessionDoctorParams) (SessionDoctorResult, error)

	// SessionsNextScene starts the next scene of a played session (SD5): PROPOSE
	// drafts a title, a story-so-far recap, and a cold-open beat with one bounded
	// Dramaturgi call; COMMIT creates a fresh session carrying this one's live
	// World state (roster, lore including the pinned scene-state card,
	// coordination, backdrop, user persona) with the recap as always-on lore and
	// the cold open standing in for the card greeting. It rides this controller
	// because its propose half is a Dramaturgi call with the same posture as the
	// doctors — optional, and needs a model.
	SessionsNextScene(ctx context.Context, sess string, p NextSceneParams) (NextSceneResult, error)
}

// DoctorParams names the card to examine and, on a follow-up round, the user's
// verdicts on the previous proposals.
type DoctorParams struct {
	ID        string           `json:"id"`
	Decisions []DoctorDecision `json:"decisions,omitempty"`
	// Provider/Model optionally run the doctor on a specific model instead of the
	// workspace default (Phase 7 per-generation routing). Empty = the default.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// Session switches the doctor into EDITOR mode (Worlds W4): the proposals
	// come from the Toimittaja persona, grounded in the named immersive
	// session's scene and the character's World lore — promotion from play —
	// rather than from card-craft lint alone. The same proposal shape and
	// negotiation loop apply.
	Session string `json:"session,omitempty"`
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

// SessionDoctorParams drives sessions.doctor. The session rides the frame.
// Decisions and the per-generation Provider/Model override work exactly as
// they do for the card doctor; the default model is the session's own.
//
// Focus and Promote are the doctor's small hands (SD2/SD3), mutually
// exclusive with each other: each narrows the run to one purpose instead of
// the whole-session sweep.
type SessionDoctorParams struct {
	Decisions []DoctorDecision `json:"decisions,omitempty"`
	Provider  string           `json:"provider,omitempty"`
	Model     string           `json:"model,omitempty"`
	// Focus marks ONE message (its index in the effective transcript): draft
	// lore/thread entries capturing what that moment establishes — mark-as-lore
	// (SD2). Promotions are filtered from a focused run; its button promised
	// lore. A pointer so index 0 is distinguishable from unset.
	Focus *int `json:"focus,omitempty"`
	// Promote names a walk-on: draft exactly one cast_promotion for them from
	// their played lines (SD3). Refused when they are already on stage.
	Promote string `json:"promote,omitempty"`
}

// SessionProposal is one typed session-doctor proposal. Kind decides which
// payload fields are meaningful and which verb an acceptance applies through:
//
//	lore_entry     — Name/Content/Keys/Audience → world.lore.put
//	open_thread    — Name/Content/Keys (a promised callback) → world.lore.put
//	cast_promotion — Character + card-seed fields → cards.import + cast.add,
//	                 surfaced as a prefilled editor, never a silent import
//	scene_state    — Content only (SD4): the FULL replacement text of the
//	                 pinned scene-state card; Name is forced to the reserved
//	                 pin name server-side → world.lore.put (an update, so the
//	                 collision rule does not apply); at most one per round
//	lore_retire    — Name only (SD6): a RECORDED entry the scene has played
//	                 past, retired → world.lore.delete. The one destructive
//	                 kind, and the counterweight to append-only: lore written
//	                 to set a scene up ("the lieutenant is expected at first
//	                 light and should be asked her team size") keeps firing
//	                 after she has arrived and been asked, and neither the
//	                 model (world_note is append-only) nor the doctor could
//	                 retire it. Never the reserved names — the pin is updated
//	                 via scene_state, the recap replaced at a scene break
//	scene_break    — Name (a title for the next scene) + Content (why the
//	                 scene ends here) (SD5): the only kind that applies to no
//	                 verb of its own — accepting it opens the next-scene flow
//	                 (sessions.next_scene), which drafts and commits under the
//	                 author's eye; at most one per round
type SessionProposal struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Rationale string `json:"rationale"`

	// lore_entry / open_thread.
	Name     string   `json:"name,omitempty"`
	Content  string   `json:"content,omitempty"`
	Keys     []string `json:"keys,omitempty"`
	Audience []string `json:"audience,omitempty"`

	// cast_promotion: the seed for a new card, drawn from the played scene.
	Character   string `json:"character,omitempty"`
	Description string `json:"description,omitempty"`
	Personality string `json:"personality,omitempty"`
	FirstMes    string `json:"first_mes,omitempty"`
}

// Session-proposal kinds. Each kind ships only once its acceptance applies
// through a verb that exists (docs/proposals/session-doctor.md): the SD1
// three landed together; scene_state joined with the pinned card (SD4).
const (
	SessionProposalLore      = "lore_entry"
	SessionProposalThread    = "open_thread"
	SessionProposalPromotion = "cast_promotion"
	SessionProposalState     = "scene_state"
	SessionProposalBreak     = "scene_break"
	SessionProposalRetire    = "lore_retire"
)

// SessionDoctorResult is the payload of sessions.doctor.
type SessionDoctorResult struct {
	Proposals []SessionProposal `json:"proposals"`
	Note      string            `json:"note,omitempty"`
}

// NextSceneParams drives sessions.next_scene (SD5). The session rides the
// frame; the verb has two phases, so one round-trip never both drafts and
// commits — the author always sees the recap and the cold open before a scene
// exists (the doctor family's "nothing applies unless you accept it").
type NextSceneParams struct {
	// Commit false (the default) PROPOSES: one bounded model call returns a
	// draft Title/Summary/Opening and creates nothing. Commit true CREATES the
	// scene from the fields below — the drafts as the author edited them — and
	// spends nothing.
	Commit bool `json:"commit,omitempty"`
	// Title names the new session; Summary becomes its always-on "story so far"
	// lore entry; Opening is the cold-open beat that stands in for the card
	// greeting. All three are required on a commit.
	Title   string `json:"title,omitempty"`
	Summary string `json:"summary,omitempty"`
	Opening string `json:"opening,omitempty"`
	// World names a World to promote this story into, on commit, when the
	// current scene belongs to none. Both scenes are stamped into it — the one
	// ending and the one opening — so the story is grouped from its first
	// boundary rather than only from whenever someone remembered to run
	// worlds.save.
	//
	// Ignored when the scene is ALREADY in a World (it joins that one; there is
	// nothing to name). Empty declines the promotion: the next scene is still
	// created, and still records its lineage, it just is not grouped.
	World string `json:"world,omitempty"`
	// Provider/Model optionally draft on a specific model (Phase 7), like the
	// doctors. Ignored on a commit — no model runs.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// NextSceneResult carries the draft (propose) or the created scene (commit).
type NextSceneResult struct {
	// The three drafted fields. A commit echoes back what it actually applied,
	// so a client need not trust its own copy.
	Title   string `json:"title,omitempty"`
	Summary string `json:"summary,omitempty"`
	Opening string `json:"opening,omitempty"`
	// Note is Dramaturgi's optional remark on the boundary (propose only) —
	// e.g. that the scene has not reached one yet.
	Note string `json:"note,omitempty"`
	// Session is the newly created scene; nil on a propose.
	Session *SessionInfo `json:"session,omitempty"`

	// WorldID is the saved World this story already belongs to, empty when it
	// belongs to none. It is what tells a client whether to OFFER a promotion:
	// set means the next scene simply joins that World and there is nothing to
	// ask; empty means the scenes would be grouped by nothing unless the author
	// names one.
	WorldID string `json:"world_id,omitempty"`
	// WorldName is that World's name — or, when WorldID is empty, a suggested
	// name to prefill the offer with (the bound character, else the session's
	// title). Only a suggestion: the author names it.
	WorldName string `json:"world_name,omitempty"`
}
