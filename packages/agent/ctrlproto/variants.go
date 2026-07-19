package ctrlproto

import "context"

// VariantsController is the OPTIONAL cleanup surface for message-scoped variants
// (docs/proposals/stage-inline-editing.md §9): prune-to-latest and per-take
// removal, so accumulated edit alternatives do not pile up. Like the other Stage
// controllers it is served only by the real workspace; a carrier without it (a
// replay session, a test fake) answers "unsupported" rather than failing deeper.
type VariantsController interface {
	// PruneVariants collapses the message-scoped variant at index to its currently
	// active take and closes the position (prune-to-latest): the swipe marker goes
	// away and the other takes stop being switchable. epoch guards staleness like
	// the other revision verbs ([CodeConflict] on mismatch); [CodeBadRequest] when
	// index has no variants.
	PruneVariants(ctx context.Context, sess string, epoch uint64, index int) error
	// DropVariant removes one take (variant) from the message-scoped variant at
	// index, keeping the rest swipeable; closes the position when a single take
	// remains. Same epoch semantics; [CodeBadRequest] when index has no variants or
	// variant is out of range.
	DropVariant(ctx context.Context, sess string, epoch uint64, index, variant int) error
}

// VariantsPruneParams is the payload of [MethodVariantsPrune].
type VariantsPruneParams struct {
	Epoch uint64 `json:"epoch"`
	Index int    `json:"index"`
}

// VariantsDropParams is the payload of [MethodVariantsDrop].
type VariantsDropParams struct {
	Epoch   uint64 `json:"epoch"`
	Index   int    `json:"index"`
	Variant int    `json:"variant"`
}
