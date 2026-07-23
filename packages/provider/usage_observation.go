package provider

import "sync"

// usageObservation is the shared, mutex-guarded store for a client that
// captures its usage PASSIVELY from response headers. It is the header-side twin
// of pollingUsageClient (the poll-side shared machinery): concrete header
// clients (openai-compat, codex) embed it and call record() after each
// successful response, getting UsageSnapshot()/seeding for free instead of
// hand-rolling the same mutex+snapshot+seed boilerplate in each client.
//
// Only the store/read/seed plumbing is shared here — the PARSE step stays
// per-client, because each wire format names its usage headers differently
// (openai's x-ratelimit-*, codex's x-codex-*). A header client is then just its
// pure parse func plus `c.usage.record(parse(h))`.
type usageObservation struct {
	mu   sync.Mutex
	last UsageSnapshot
	have bool
}

// record stores snap as the latest observation when ok. A false ok (this
// response carried no usable usage headers) leaves the previous observation
// intact, so an occasional header-less response does not blank a good snapshot.
func (o *usageObservation) record(snap UsageSnapshot, ok bool) {
	if !ok {
		return
	}
	o.mu.Lock()
	o.last, o.have = snap, true
	o.mu.Unlock()
}

// snapshot returns the latest observation; ok=false until the first record.
// This is the UsageReporter half, shared verbatim across header clients.
func (o *usageObservation) snapshot() (UsageSnapshot, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.last, o.have
}

// seed primes the store from a predecessor client's snapshot across a rebuild
// (re-login, endpoint change) — the UsageSeeder half. It is a no-op when the
// snapshot belongs to another provider (its windows say nothing about this
// subscription) or when a live observation already exists (a seed is by
// definition at least one rebuild stale and must never displace fresher data).
//
// Seeding is opt-in: only a client that WANTS continuity across rebuilds wires a
// SeedUsage method to this. An ephemeral rate-limit reporter (openai-compat)
// simply doesn't, which makes "is this provider seedable?" an explicit one-line
// decision per client rather than a structural difference between them.
func (o *usageObservation) seed(snap UsageSnapshot, wantProvider string) {
	if snap.Provider != wantProvider {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.have {
		return
	}
	o.last, o.have = snap, true
}
