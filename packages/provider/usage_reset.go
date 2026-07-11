package provider

import (
	"context"
	"errors"
	"time"
)

// ErrResetsUnsupported is returned by ClientConsumeReset when the target client
// exposes no reset capability — a routing bug, surfaced loudly because the call
// is meant to spend a credit.
var ErrResetsUnsupported = errors.New("provider does not support usage resets")

// A usage RESET is a consumable credit that clears a provider's usage windows
// when redeemed — OpenAI's "banked rate-limit resets" for Codex subscriptions
// are the first instance (a granted credit that zeroes the 5h + weekly
// windows). The concept is deliberately provider-agnostic: terva models "a
// list of consumable resets, each redeemable once" and lets each provider map
// its own API onto it, exactly as UsageWindow abstracts over differing budget
// windows. See docs/proposals/usage-resets.md.

// ResetStatus is the lifecycle of a single reset credit.
type ResetStatus string

const (
	// ResetAvailable is a credit that can be redeemed now.
	ResetAvailable ResetStatus = "available"
	// ResetPending is a credit whose redemption has started but not completed
	// (the provider reports an in-flight redeem). Rare; shown as not-yet-usable.
	ResetPending ResetStatus = "pending"
	// ResetRedeemed is a spent credit.
	ResetRedeemed ResetStatus = "redeemed"
	// ResetExpired is a credit past its expiry that was never redeemed.
	ResetExpired ResetStatus = "expired"
)

// UsageReset is one consumable reset credit. Fields mirror what a provider
// exposes; a provider fills what it has and leaves the rest zero. Times are
// UTC; a zero time means the provider did not report that timestamp.
type UsageReset struct {
	// ID is the provider's opaque credit identifier, passed back to redeem.
	ID string
	// Kind is the provider's own reset-type tag (codex: "codex_rate_limits"),
	// carried verbatim for display/telemetry; terva does not switch on it.
	Kind string
	// Title/Description are the provider's human labels ("Full reset (Weekly +
	// 5 hr)"), shown to the user verbatim.
	Title       string
	Description string
	// Status classifies the credit for display and gating (only ResetAvailable
	// credits may be consumed).
	Status ResetStatus
	// GrantedAt / ExpiresAt bound the credit's life; RedeemedAt is set once
	// spent (zero otherwise).
	GrantedAt  time.Time
	ExpiresAt  time.Time
	RedeemedAt time.Time
}

// Available reports whether the credit can be redeemed right now.
func (r UsageReset) Available() bool { return r.Status == ResetAvailable }

// UsageResetResult is the outcome of redeeming one credit.
type UsageResetResult struct {
	// Reset is the credit in its post-redemption state (Status ResetRedeemed,
	// RedeemedAt set).
	Reset UsageReset
	// WindowsReset is how many usage windows the redemption cleared, when the
	// provider reports it (codex returns this); 0 when unknown.
	WindowsReset int
}

// UsageResetProvider is implemented by the (few) clients that expose
// consumable usage resets — today only openai-codex. Optional interface, like
// UsageReporter: a client that offers no resets simply does not implement it,
// and ClientSupportsResets then reports false so the harness hides the feature.
//
// ConsumeReset is IRREVERSIBLE and spends a scarce, provider-granted credit, so
// callers must gate it behind explicit user confirmation (never auto-redeem).
// Implementations make redemption idempotent per credit so a retried or
// timed-out call cannot double-spend (see the codex client's deterministic
// request id).
type UsageResetProvider interface {
	// ListResets returns the account's reset credits — available and spent —
	// newest grant first. A nil slice with nil error means "none".
	ListResets(ctx context.Context) ([]UsageReset, error)
	// ConsumeReset redeems the credit named by id. It returns the updated
	// credit and how many windows were cleared. Redeeming a non-available
	// credit is the provider's error to return.
	ConsumeReset(ctx context.Context, id string) (UsageResetResult, error)
}

// ClientSupportsResets reports whether c (through any wrapper layer) exposes
// consumable usage resets — the gate for showing a /resets affordance at all.
func ClientSupportsResets(c Client) bool {
	_, ok := clientAs[UsageResetProvider](c)
	return ok
}

// ClientListResets lists c's reset credits, looking through wrapper layers for
// a UsageResetProvider. Returns (nil, nil) when the client offers no resets, so
// a caller can treat "no support" and "supported but empty" the same way.
func ClientListResets(ctx context.Context, c Client) ([]UsageReset, error) {
	if r, ok := clientAs[UsageResetProvider](c); ok {
		return r.ListResets(ctx)
	}
	return nil, nil
}

// ClientConsumeReset redeems a credit on c, looking through wrapper layers.
// Returns ErrResetsUnsupported when the client offers no resets, so a
// mis-routed consume fails loudly rather than silently no-opping (this call
// spends a credit — a silent success would be a lie).
func ClientConsumeReset(ctx context.Context, c Client, id string) (UsageResetResult, error) {
	if r, ok := clientAs[UsageResetProvider](c); ok {
		return r.ConsumeReset(ctx, id)
	}
	return UsageResetResult{}, ErrResetsUnsupported
}
