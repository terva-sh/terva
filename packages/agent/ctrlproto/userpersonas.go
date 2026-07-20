package ctrlproto

import "context"

// Saved user personas on the wire — the reusable "who I am in the story"
// identities (name + description), distinct from the agent personas that shape a
// CHARACTER (personas.*). One may be the DEFAULT a new immersive session
// pre-fills, so a chat no longer opens as the literal "User". A saved user
// persona is benign data (no charter, no authority), so these verbs are ungated
// beyond the workspace's own auth — unlike personas.*, which are trusted-tier.
//
// UserPersonasController is OPTIONAL, like the other Stage controllers, so the
// verbs do not ripple to the base WorkspaceService implementers.
type UserPersonasController interface {
	// UserPersonasList returns every saved user persona (name-sorted), one marked
	// default if the user set one.
	UserPersonasList(ctx context.Context) (UserPersonasListResult, error)
	// UserPersonaSave upserts a saved persona by a slug of its name, preserving its
	// default status (changed only via UserPersonaSetDefault).
	UserPersonaSave(ctx context.Context, p UserPersonaView) (UserPersonaView, error)
	// UserPersonaDelete removes a saved persona; a missing one is a no-op.
	UserPersonaDelete(ctx context.Context, p UserPersonaRef) error
	// UserPersonaSetDefault marks one persona the default (clearing the others); an
	// empty ref clears the default entirely.
	UserPersonaSetDefault(ctx context.Context, p UserPersonaRef) error
}

// UserPersonaView is one saved user persona (mirrors build.UserPersona).
type UserPersonaView struct {
	Ref         string `json:"ref,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Gender      string `json:"gender,omitempty"`
	Pronouns    string `json:"pronouns,omitempty"`
	Default     bool   `json:"default,omitempty"`
}

// UserPersonaRef names a saved persona by ref; an empty ref on set_default clears
// the default.
type UserPersonaRef struct {
	Ref string `json:"ref"`
}

// UserPersonasListResult is the payload of userpersonas.list.
type UserPersonasListResult struct {
	Personas []UserPersonaView `json:"personas"`
}
