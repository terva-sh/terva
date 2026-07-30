package provider

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A poll-family provider reports nothing until somebody calls its usage
// endpoint. Until this, the only caller was an explicit refresh — a user
// opening /usage — so every passive read in between returned the same frozen
// numbers, and the status bar's meters simply never moved.
//
// The passive read warms its own cache now. These pin the three properties
// that make that safe to do on a read: it warms, it single-flights, and a
// failing endpoint backs off instead of being retried by every reader.

// warmClient builds a polling client over a counted fetcher. gate, when
// non-nil, holds each fetch until it is closed.
func warmClient(t *testing.T, ttl time.Duration, gate <-chan struct{}, err error) (*pollingUsageClient, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	fetch := func(ctx context.Context) (UsageSnapshot, error) {
		calls.Add(1)
		if gate != nil {
			select {
			case <-gate:
			case <-ctx.Done():
				return UsageSnapshot{}, ctx.Err()
			}
		}
		if err != nil {
			return UsageSnapshot{}, err
		}
		return UsageSnapshot{Provider: "kimi", CapturedAt: time.Now()}, nil
	}
	return newPollingUsageClient(nil, ttl, fetch), &calls
}

// waitFor polls cond until it holds or the deadline passes. The warm is
// asynchronous by design, so a test cannot assert on it synchronously.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The headline: a passive read on a cold client fetches, so the READ AFTER it
// has numbers. Before this the answer stayed empty until something called
// RefreshUsage explicitly.
func TestAPassiveReadWarmsAColdPollCache(t *testing.T) {
	c, calls := warmClient(t, usagePollTTL, nil, nil)

	if _, ok := c.UsageSnapshot(); ok {
		t.Fatal("precondition: a cold client should report nothing yet")
	}
	waitFor(t, "the background warm to fetch", func() bool { return calls.Load() == 1 })
	waitFor(t, "the warmed snapshot to be readable", func() bool {
		_, ok := c.UsageSnapshot()
		return ok
	})

	snap, _ := c.UsageSnapshot()
	if snap.Provider != "kimi" {
		t.Errorf("warmed snapshot Provider = %q, want kimi", snap.Provider)
	}
}

// A warm still in flight must not be joined by every reader that arrives while
// it runs. The status bar, the panel and the context breakdown can all read
// within the same instant.
func TestConcurrentPassiveReadsWarmOnce(t *testing.T) {
	gate := make(chan struct{})
	c, calls := warmClient(t, usagePollTTL, gate, nil)

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.UsageSnapshot() }()
	}
	wg.Wait()

	if got := calls.Load(); got > 1 {
		t.Errorf("%d fetches in flight for 24 concurrent reads, want 1 — the warm is not single-flighted", got)
	}
	close(gate)
	waitFor(t, "the warm to finish", func() bool {
		_, ok := c.UsageSnapshot()
		return ok
	})
	if got := calls.Load(); got != 1 {
		t.Errorf("total fetches = %d, want 1", got)
	}
}

// The hazard this design had to answer. fetchCredits deliberately does not
// advance `fetched` on error, so an explicit /usage retries immediately — but
// that means a warm keyed on the SAME clock would re-fetch on every read for as
// long as the endpoint stayed down. The warm has its own clock for this.
func TestAFailingEndpointIsNotRetriedByEveryReader(t *testing.T) {
	c, calls := warmClient(t, time.Hour, nil, errors.New("502 bad gateway"))

	for i := 0; i < 30; i++ {
		c.UsageSnapshot()
	}
	waitFor(t, "the first warm to fail", func() bool { return calls.Load() >= 1 })
	// Give any further warms a chance to be spawned before counting.
	time.Sleep(50 * time.Millisecond)
	for i := 0; i < 30; i++ {
		c.UsageSnapshot()
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("%d fetches for 60 reads against a failing endpoint, want 1 — "+
			"every passive read is retrying a down endpoint", got)
	}
}

// The other half of that split: an explicit refresh is a user asking to try
// again NOW, and must not be silenced by the warm's backoff.
func TestAnExplicitRefreshStillRetriesAfterAFailure(t *testing.T) {
	c, calls := warmClient(t, time.Hour, nil, errors.New("502 bad gateway"))

	c.UsageSnapshot() // arms the warm's backoff
	waitFor(t, "the warm to fail", func() bool { return calls.Load() >= 1 })
	before := calls.Load()

	c.RefreshUsage(context.Background())
	if got := calls.Load(); got != before+1 {
		t.Errorf("an explicit refresh after a failure made %d fetches, want 1 — "+
			"the background backoff is suppressing the user's own retry", got-before)
	}
}

// A warm must not re-fetch behind a cache an explicit refresh just filled.
func TestAFreshExplicitRefreshSuppressesTheNextWarm(t *testing.T) {
	c, calls := warmClient(t, time.Hour, nil, nil)

	if _, ok := c.RefreshUsage(context.Background()); !ok {
		t.Fatal("precondition: the explicit refresh did not land")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("precondition: %d fetches, want 1", got)
	}
	for i := 0; i < 10; i++ {
		c.UsageSnapshot()
	}
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Errorf("%d fetches after a fresh explicit refresh, want 1 — a warm is "+
			"re-fetching numbers that are seconds old", got)
	}
}
