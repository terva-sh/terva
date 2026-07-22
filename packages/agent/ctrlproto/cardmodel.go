package ctrlproto

import "context"

// Per-card default model on the wire. A card's default is terva-owned metadata,
// kept outside the card like a group (build.CardModelStore) — a seed for a
// session started from the card, resolved through Card → World → Workspace.
//
// CardModelController is OPTIONAL, like CardGroupsController: a carrier that
// serves no card library simply does not implement it and both verbs answer
// "unsupported". Its only client consumers are card-context pickers (the card
// doctor's model, the CardSheet default row), which exist only where the library
// does — so gating the resolver here costs nothing. sessions.create resolves its
// seed through the same Go authority (Workspace.effectiveDefaultModel) directly,
// off the wire.
type CardModelController interface {
	// ModelDefaultFor resolves the effective default provider+model for a
	// context, walking Card → World → Workspace. It is the single authority every
	// "what's the default here?" surface consults, so a card's default propagates
	// to the card doctor, the session seed, and any picker's fallback row
	// identically. Source names the rung that won.
	ModelDefaultFor(ctx context.Context, p DefaultForParams) (DefaultForResult, error)
	// CardModelSet writes a card's default (provider+model), or clears it when
	// both are empty — the "fall back to the workspace default" choice. Clearing a
	// card that had none is not an error.
	CardModelSet(ctx context.Context, p CardModelSetParams) error
}

// DefaultForParams names the context to resolve a default for. Both fields are
// optional: with neither, the result is just the workspace default. World is
// accepted now but has no rung yet (no world-level default model exists) —
// reserved so the day one is added, callers need no change.
type DefaultForParams struct {
	Card  string `json:"card,omitempty"`
	World string `json:"world,omitempty"`
}

// DefaultSource names which rung of Card → World → Workspace supplied the
// resolved default — so a picker's fallback row can say what it inherits, and a
// card-scoped surface can tell "this card has its own" from "inheriting".
type DefaultSource string

const (
	DefaultSourceCard      DefaultSource = "card"
	DefaultSourceWorld     DefaultSource = "world"
	DefaultSourceWorkspace DefaultSource = "workspace"
)

// DefaultForResult is the resolved effective default. Provider/Model are empty
// only when the workspace itself names no default and no catalog fallback
// exists; Source is always set.
type DefaultForResult struct {
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	Source   DefaultSource `json:"source"`
}

// CardModelSetParams sets or clears a card's default model. Empty Provider AND
// Model clears it (drops the sidecar entry → back to the workspace default).
type CardModelSetParams struct {
	Card     string `json:"card"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}
