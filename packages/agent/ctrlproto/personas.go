package ctrlproto

import "context"

// The persona library on the wire — the identity half of the content library.
// Personas were CLI-only (terva persona list/validate/init); this brings them
// into the control plane so any controller inspects and authors them.
//
// Trust is the hard line, unlike cards: a persona charter shapes identity in the
// cached prefix, so create/edit is a TRUSTED-tier mutation — the workspace gates
// it (writes land in the user library, $TERVA_HOME/personas; the embedded crew is
// read-only and copy-to-edit). Reads (list/get) are ungated.
//
// PersonasController is OPTIONAL, like CardsController: a carrier without a
// persona library simply does not implement it. Adding these verbs does NOT
// ripple to every WorkspaceService implementer.
type PersonasController interface {
	// PersonasList returns the merged, deduped roster with provenance.
	PersonasList(ctx context.Context) (PersonasListResult, error)
	// PersonasGet returns one persona in full, including its charter.
	PersonasGet(ctx context.Context, p PersonaGetParams) (PersonaView, error)
	// PersonasCreate writes a NEW persona to the user library. Trusted-tier;
	// errors if a user persona of that name already exists (edit it instead).
	PersonasCreate(ctx context.Context, p PersonaWriteParams) (PersonaView, error)
	// PersonasEdit overwrites an existing persona (copy-to-edit for a built-in).
	// Trusted-tier; errors if no persona of that name exists (create it instead).
	PersonasEdit(ctx context.Context, p PersonaWriteParams) (PersonaView, error)
	// PersonasDelete removes a persona from the USER library. Trusted-tier.
	//
	// Only an on-disk user file can be deleted — the embedded crew and extension
	// bundles are not ours to remove, and a request naming one is refused rather
	// than silently doing nothing. Deleting a user file that SHADOWS a built-in
	// is the un-shadow: the built-in becomes visible again, which is the only way
	// back from a copy-to-edit and the reason this is a delete rather than a
	// tombstone.
	PersonasDelete(ctx context.Context, p PersonaDeleteParams) error
}

// PersonaDeleteParams names the persona to remove from the user library.
type PersonaDeleteParams struct {
	Name string `json:"name"`
}

// PersonaGetParams names a persona by a bare name/stem or "namespace:name" ref.
type PersonaGetParams struct {
	Name string `json:"name"`
}

// PersonaWriteParams is the editable persona form. Name is required; Charter is
// the behavioral body. Immersive makes the charter own the whole system prompt
// (roleplay) rather than layering on terva's identity.
type PersonaWriteParams struct {
	Name              string   `json:"name"`
	Pronunciation     string   `json:"pronunciation,omitempty"`
	Specialty         string   `json:"specialty,omitempty"`
	Summary           string   `json:"summary,omitempty"`
	Emoji             string   `json:"emoji,omitempty"`
	AccentColor       string   `json:"accent_color,omitempty"`
	Group             string   `json:"group,omitempty"`
	RecommendedSkills []string `json:"recommended_skills,omitempty"`
	GoodFor           []string `json:"good_for,omitempty"`
	AvoidFor          []string `json:"avoid_for,omitempty"`
	Immersive         bool     `json:"immersive,omitempty"`
	Introduction      string   `json:"introduction,omitempty"`
	Charter           string   `json:"charter,omitempty"`
}

// PersonasListResult is the payload of personas.list.
type PersonasListResult struct {
	Personas []PersonaSummary `json:"personas"`
}

// PersonaSummary is the roster entry: identity + provenance. Origin is
// "built-in" | "extension" | "user"; Editable is false for built-in/extension
// (those copy-to-edit into the user library).
type PersonaSummary struct {
	Name        string `json:"name"`
	Ref         string `json:"ref"`
	Namespace   string `json:"namespace,omitempty"`
	Specialty   string `json:"specialty,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Emoji       string `json:"emoji,omitempty"`
	AccentColor string `json:"accent_color,omitempty"`
	// Group is the shelf a roster files this persona under — its declared
	// `group`, else the namespace it came from. Purely organisational: it is
	// not part of Ref, so a client must never key anything on it.
	Group     string `json:"group,omitempty"`
	Immersive bool   `json:"immersive,omitempty"`
	Origin    string `json:"origin"`
	Editable  bool   `json:"editable,omitempty"`
}

// PersonaView is one persona in full: its summary plus the fields an editor
// renders (charter, pronunciation, the good-for/avoid-for guidance).
type PersonaView struct {
	PersonaSummary
	Pronunciation     string   `json:"pronunciation,omitempty"`
	RecommendedSkills []string `json:"recommended_skills,omitempty"`
	GoodFor           []string `json:"good_for,omitempty"`
	AvoidFor          []string `json:"avoid_for,omitempty"`
	Introduction      string   `json:"introduction,omitempty"`
	Charter           string   `json:"charter,omitempty"`
}
