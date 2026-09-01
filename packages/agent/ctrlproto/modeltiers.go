package ctrlproto

import "context"

// The swarm tier ladder — which model a sub-agent gets for `tier: weak`,
// `medium` or `strong`, per provider, and how hard it thinks.
//
// The ladder existed only as built-in family rules plus a hand-edited
// config.json block, discoverable through one CLI subcommand. That is how
// google's medium and strong rungs came to resolve to image-generation models
// while every guard passed: nothing showed the operator what a rung had
// actually resolved to, so nobody looked.
//
// The RESOLVED pick is therefore the point of this surface, not the override.
// An empty `swarm_tiers` is the normal case and says nothing about whether the
// ladder is right — a client that rendered only what config holds would have
// shown three blank rungs on the day the bug was live.

// ModelTiersController is served by a daemon that can read and edit the tier
// ladder. Optional, like ModelParamsController: a carrier that cannot write
// config.json does not implement it, and the method answers "unsupported"
// rather than failing somewhere deeper.
type ModelTiersController interface {
	// ModelTiers describes one provider's ladder: what each rung resolves to
	// today, and where that came from.
	ModelTiers(ctx context.Context, p ModelTiersParams) (ModelTiersView, error)
	// ModelTiersSet pins one rung. Either field may be empty — a rung that
	// names only a thinking level means "the built-in model for this rung, but
	// think this hard", which is the cheapest way to build a ladder on a
	// provider whose families terva already knows.
	ModelTiersSet(ctx context.Context, p ModelTiersSetParams) error
	// ModelTiersReset drops a pin. With Rung empty it drops the provider's
	// whole entry, returning every rung to the built-in guess.
	ModelTiersReset(ctx context.Context, p ModelTiersResetParams) error
}

// ModelTiersParams names a provider.
type ModelTiersParams struct {
	Provider string `json:"provider"`
}

// ModelTiersSetParams pins one rung of one provider's ladder.
//
// One rung per call, unlike ModelParamsSet's whole-form rule. The ambiguity
// that forces the form rule there does not exist here: a rung is addressed by
// name, so "absent" cannot be confused with "cleared" — clearing is
// ModelTiersReset, which says so.
type ModelTiersSetParams struct {
	Provider string `json:"provider"`
	// Rung is weak | medium | strong.
	Rung string `json:"rung"`
	// Model is a model id in this provider's catalog. Empty keeps the built-in
	// pick for the rung and changes only Reasoning.
	Model string `json:"model,omitempty"`
	// Reasoning is a ladder level (off | minimum | low | medium | high |
	// maximum | max). Empty leaves the effort to the child.
	Reasoning string `json:"reasoning,omitempty"`
}

// ModelTiersResetParams drops one rung's pin, or the provider's whole entry
// when Rung is empty.
type ModelTiersResetParams struct {
	Provider string `json:"provider"`
	Rung     string `json:"rung,omitempty"`
}

// ModelTiersView is what a client renders: three rungs, always all three, in
// ladder order.
type ModelTiersView struct {
	Provider string `json:"provider"`
	// HasOverride is whether config.json holds an entry for this provider —
	// i.e. whether a "reset this provider" would do anything.
	HasOverride bool            `json:"has_override,omitempty"`
	Rungs       []ModelTierRung `json:"rungs"`
}

// ModelTierRung is one rung as it stands.
type ModelTierRung struct {
	// Rung is weak | medium | strong.
	Rung string `json:"rung"`
	// Model is what this rung resolves to TODAY — the pinned id, or the model
	// the built-in family rule found in this provider's catalog. Empty means
	// the rung does not resolve at all, and a sub-agent asking for this tier
	// inherits the host model instead.
	Model string `json:"model,omitempty"`
	// Label is Model's display name, so a client need not re-look-up the
	// catalog to render a row.
	Label string `json:"label,omitempty"`
	// Pinned is the model id the OPERATOR pinned, empty when the rung's model
	// came from a family rule. Model alone cannot answer that, and a client
	// that edited only the thinking level would have to send SOMETHING back
	// for the model: sending the resolved id would silently freeze a rung that
	// had been tracking the family rule, and sending nothing would drop a
	// genuine pin. Carrying the pin separately makes the round trip lossless.
	Pinned string `json:"pinned,omitempty"`
	// Reasoning is the thinking level pinned for this rung, if any.
	Reasoning string `json:"reasoning,omitempty"`
	// Source is "override" when the operator pinned this rung, "built-in" when
	// a family rule found it, and "" when nothing resolved. It is the field
	// that makes the view worth having: a rung can be wrong while config is
	// empty, and only the source says which of the two you are looking at.
	Source string `json:"source,omitempty"`
}
