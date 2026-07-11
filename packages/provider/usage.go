package provider

import (
	"context"
	"sort"
	"time"
)

// WindowKind classifies a usage window so consumers can treat the kinds
// differently: the /usage dialog shows every kind, but the compact status-bar
// hint shows only WindowPlan/WindowCredit — ephemeral rate-limit windows would
// churn the always-visible bar. See docs/plans/usage-merged-view.md.
type WindowKind int

const (
	// WindowPlan is a subscription budget window (Codex 5h / weekly). It is the
	// ZERO VALUE, so windows that predate this field keep their meaning.
	WindowPlan WindowKind = iota
	// WindowCredit is a window derived from a credit/spend budget — reserved
	// for future credit-period windows (usage-merged-view.md §8).
	WindowCredit
	// WindowRateLimit is an ephemeral throughput window (RPM/TPM) parsed from
	// x-ratelimit-* response headers.
	WindowRateLimit
)

// UsageWindow is one provider-defined usage budget the user is spending
// against: a rolling 5-hour window, a weekly cap, a monthly quota —
// whatever the provider reports. terva renders the windows a provider
// hands it; it does not assume a fixed set of hourly/weekly/monthly
// slots, because different subscriptions expose different ones.
type UsageWindow struct {
	// Label is the provider's name for the window ("5h", "weekly", …),
	// shown to the user verbatim.
	Label string
	// UsedPercent is how much of the window is consumed, 0..100. A
	// negative value means "the provider reports this window but not a
	// usable percentage" (render as unknown, not 0%).
	UsedPercent float64
	// WindowMinutes is the window length in minutes; 0 when unknown.
	WindowMinutes int
	// ResetsAt is when the window rolls over; zero when unknown.
	ResetsAt time.Time
	// Kind classifies the window for display/filtering. Zero value is
	// WindowPlan, so existing producers need no change.
	Kind WindowKind
}

// Credits is the optional pay-as-you-go balance some plans expose
// alongside (or instead of) windowed limits.
type Credits struct {
	Unlimited  bool
	HasCredits bool
	// Balance is in provider-defined units; only meaningful when the
	// provider reports it (Unlimited and HasCredits give the context).
	Balance float64
	// Used is the amount spent so far in provider-defined units, when the
	// provider reports it (e.g. OpenRouter's lifetime key usage). 0 when
	// unknown. This is the "usage" half of "usage and limits"; Balance is
	// the "remaining" half — some providers expose one, some the other.
	Used float64
}

// UsageSnapshot is the most recent usage picture a client observed. It
// is a value type: callers read it and render, they never mutate it.
type UsageSnapshot struct {
	Provider   string
	Windows    []UsageWindow
	Credits    *Credits
	CapturedAt time.Time
}

// UsageReporter is implemented by the (few) concrete clients that can
// report subscription usage — today only openai-codex, whose response
// headers carry it. A client that cannot report usage simply does not
// implement this; ClientUsage then returns ok=false and the harness
// shows nothing for that provider. This is intentionally an optional
// reporter interface rather than a ClientCapabilities field: usage is
// dynamic per-turn state to be pulled, not a static wire-format flag.
type UsageReporter interface {
	// UsageSnapshot returns the latest snapshot the client has seen,
	// with ok=false when it has nothing to report (no usage data yet,
	// or this provider doesn't expose any).
	UsageSnapshot() (UsageSnapshot, bool)
}

// ClientUsage returns c's latest usage snapshot, looking through any
// wrapper layers (renamedClient, pollingUsageClient) via the shared
// clientAs walk — openai-responses ships wrapped in a renamedClient, so
// a direct assertion on the outer client would miss it.
func ClientUsage(c Client) (UsageSnapshot, bool) {
	if r, ok := clientAs[UsageReporter](c); ok {
		return r.UsageSnapshot()
	}
	return UsageSnapshot{}, false
}

// UsageSeeder is implemented by clients whose passively-observed snapshot can
// be primed from a predecessor client's. A client rebuild (re-login, endpoint
// change, a cross-then-back provider hop) starts with an empty snapshot that
// nothing refills until the next turn's response headers arrive — so the
// meters a user was just looking at vanish. Seeding carries the last
// observation across the swap; implementations must ignore snapshots that
// are not theirs (wrong Provider) and never let a seed displace a fresher
// live observation.
type UsageSeeder interface {
	SeedUsage(UsageSnapshot)
}

// SeedClientUsage primes c with a predecessor's snapshot, looking through
// wrapper layers for a UsageSeeder. A client that cannot be seeded (or a
// snapshot the seeder rejects) is a silent no-op — seeding is best-effort
// continuity, not state transfer.
func SeedClientUsage(c Client, snap UsageSnapshot) {
	if s, ok := clientAs[UsageSeeder](c); ok {
		s.SeedUsage(snap)
	}
}

// UsageRefresher is implemented by clients whose usage must be PULLED from a
// dedicated endpoint (a balance/usage GET) rather than observed passively from
// response headers. RefreshUsage performs that fetch and returns the latest
// snapshot; it BLOCKS on network I/O, so callers must run it off any UI
// goroutine. Implementations cache with a TTL so repeated calls don't hammer
// the endpoint.
type UsageRefresher interface {
	RefreshUsage(ctx context.Context) (UsageSnapshot, bool)
}

// ClientRefreshUsage pulls a fresh usage snapshot, looking through wrapper
// layers for a UsageRefresher. Falls back to ClientUsage (the passively
// observed snapshot) when no refresher is present, so a header-based reporter
// (codex) and a poll-based one return through the same call.
func ClientRefreshUsage(ctx context.Context, c Client) (UsageSnapshot, bool) {
	if r, ok := clientAs[UsageRefresher](c); ok {
		return r.RefreshUsage(ctx)
	}
	return ClientUsage(c)
}

// ClientNeedsUsageFetch reports whether c pulls its usage from an endpoint
// (a UsageRefresher) — the case where /usage should fetch in the background
// and show a loading state instead of rendering instantly from headers.
func ClientNeedsUsageFetch(c Client) bool {
	_, ok := clientAs[UsageRefresher](c)
	return ok
}

// mergeUsage combines two snapshots into one merged view, for a provider that
// exposes more than one signal at once (e.g. OpenRouter: credits + rate-limit
// windows). Windows are unioned and ordered by Kind (plan/credit before
// rate-limit); the first non-nil Credits wins; the newest CapturedAt and the
// first non-empty Provider are kept. ok is false only when neither input has
// data. Pass the primary snapshot (credits / subscription) as a.
func mergeUsage(a UsageSnapshot, aOK bool, b UsageSnapshot, bOK bool) (UsageSnapshot, bool) {
	switch {
	case !aOK && !bOK:
		return UsageSnapshot{}, false
	case aOK && !bOK:
		return a, true
	case !aOK && bOK:
		return b, true
	}
	out := UsageSnapshot{
		Provider: firstNonEmptyString(a.Provider, b.Provider),
		Windows:  append(append([]UsageWindow(nil), a.Windows...), b.Windows...),
	}
	if a.Credits != nil {
		out.Credits = a.Credits
	} else {
		out.Credits = b.Credits
	}
	out.CapturedAt = a.CapturedAt
	if b.CapturedAt.After(out.CapturedAt) {
		out.CapturedAt = b.CapturedAt
	}
	sort.SliceStable(out.Windows, func(i, j int) bool { return out.Windows[i].Kind < out.Windows[j].Kind })
	return out, true
}
