package ctrlproto

// realize (creator C3 — docs/plans/creator-realize.md) is the exit from a
// cartographer conversation: Kartoittaja re-reads the converged planning chat
// and PROPOSES the finished structure — a World, the protagonist, the NPC
// roster, the lore, and a cold open — which the author edits and then COMMITS
// into a playable session. Two phases, one verb, the sessions.next_scene shape:
// propose creates nothing, commit spends nothing. It rides DoctorController
// because its propose half is a persona call with the doctors' posture.

// RealizeParams drives sessions.realize. The session (the creator conversation)
// rides the frame.
type RealizeParams struct {
	// Commit false (the default) PROPOSES: one Kartoittaja call over the
	// conversation returns a RealizeProposal and creates nothing. Commit true
	// CREATES a play session from Proposal — the structure as the author edited
	// it — and spends nothing.
	Commit bool `json:"commit,omitempty"`
	// Proposal is the author-edited structure, required on a commit and ignored
	// on a propose.
	Proposal *RealizeProposal `json:"proposal,omitempty"`
	// Provider/Model optionally propose on a specific model (Phase 7), like the
	// doctors; the default is the session's own. Ignored on a commit — no model
	// runs.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// RealizeProposal is the structure Kartoittaja extracts from the conversation —
// the result of a propose, and the (edited) input of a commit.
type RealizeProposal struct {
	World RealizeWorld `json:"world"`
	// Protagonist is who the AUTHOR plays; it becomes the session's bound user
	// persona, NOT a roster card (docs/plans/creator-realize.md DR-2).
	Protagonist RealizeCharacter `json:"protagonist"`
	// Roster is the NPCs the model voices; each becomes an imported card + a
	// cast member on commit.
	Roster []RealizeCharacter `json:"roster,omitempty"`
	Lore   []RealizeLore      `json:"lore,omitempty"`
	// ColdOpen is the scene the play session opens on; ColdOpenActor names the
	// roster character who delivers it ("" = the narrator).
	ColdOpen      string `json:"cold_open,omitempty"`
	ColdOpenActor string `json:"cold_open_actor,omitempty"`
	// Coordination is the meta-narrator mode: "" (auto), "off", or "focus:<name>".
	Coordination string `json:"coordination,omitempty"`
	// Given/Invented is the cartographer's attribution ledger, shown so the
	// author sees what to overrule. Propose only.
	Given    []string `json:"given_by_author,omitempty"`
	Invented []string `json:"invented_by_you,omitempty"`
	// Note is Kartoittaja's optional remark (propose only) — e.g. that the
	// conversation has not converged on anything to realize yet.
	Note string `json:"note,omitempty"`
}

// RealizeWorld is the World's identity in a proposal. realize does NOT persist a
// library World on commit — that stays the explicit worlds.save act (DR-5) — so
// this carries only what a session needs to be titled and, later, saved.
type RealizeWorld struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// RealizeCharacter is one proposed character — a roster NPC or the protagonist.
// The two share fields; Role/FirstMes are roster-shaped, Notes is the
// protagonist's play constraint (e.g. a language or ability quirk).
type RealizeCharacter struct {
	Name        string `json:"name"`
	Role        string `json:"role,omitempty"`
	Description string `json:"description,omitempty"`
	Personality string `json:"personality,omitempty"`
	FirstMes    string `json:"first_mes,omitempty"`
	Notes       string `json:"notes,omitempty"`
}

// RealizeLore is one proposed lore entry: a setting fact as a discrete keyed
// entry so the per-turn tail can inject it when its keys appear. AlwaysOn marks
// a fact relevant every turn (it becomes a constant entry).
type RealizeLore struct {
	Name     string   `json:"name"`
	Keys     []string `json:"keys,omitempty"`
	AlwaysOn bool     `json:"always_on,omitempty"`
	Content  string   `json:"content"`
}

// RealizeResult carries the proposal (propose) or the created session (commit).
type RealizeResult struct {
	// Proposal is the extracted structure; nil on a commit.
	Proposal *RealizeProposal `json:"proposal,omitempty"`
	// Session is the newly created play session; nil on a propose.
	Session *SessionInfo `json:"session,omitempty"`
}
