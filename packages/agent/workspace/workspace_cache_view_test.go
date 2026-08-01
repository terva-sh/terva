package workspace

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

// The first request of a session writes the cache and reads nothing. Deciding
// "does this provider cache?" from that one request would label every fresh
// Anthropic session as uncached, and the panel would tell people to stop
// worrying about a cache that is about to save them most of their money.
func TestSupportedIsReadFromTheSessionNotTheNewestRequest(t *testing.T) {
	first := provider.Usage{InputTokens: 50_000, CacheWriteTokens: 40_000}
	c := cacheView(first, first, []provider.Usage{first})
	if !c.Supported {
		t.Error("a cache write alone did not count as cache support")
	}

	// Now a later request that touched neither: small, under the cacheable
	// minimum. The session still supports caching.
	tiny := provider.Usage{InputTokens: 200}
	c = cacheView(tiny, first.Add(tiny), []provider.Usage{first, tiny})
	if !c.Supported {
		t.Error("a session with earlier cache activity was reported as uncached")
	}

	// And a provider that has never reported either, across the whole session.
	none := provider.Usage{InputTokens: 90_000, OutputTokens: 2_000}
	if cacheView(none, none, []provider.Usage{none}).Supported {
		t.Error("a provider with no cache activity was reported as supporting one")
	}
}

// A response the provider reported no prompt tokens for is not a miss; it is an
// absence. Drawing it as a zero-height bar puts a notch in the strip that reads
// as "the cache broke here" when nothing happened at all.
func TestAResponseWithNoPromptTokensIsNotDrawnAsAMiss(t *testing.T) {
	real1 := provider.Usage{InputTokens: 1_000, CacheReadTokens: 99_000}
	empty := provider.Usage{OutputTokens: 20} // e.g. a stream that reported output only
	real2 := provider.Usage{InputTokens: 1_000, CacheReadTokens: 99_000}

	c := cacheView(real2, real1.Add(empty).Add(real2), []provider.Usage{real1, empty, real2})
	if len(c.Recent) != 2 {
		t.Fatalf("strip has %d samples; want 2 — the empty response must not draw", len(c.Recent))
	}
	for i, s := range c.Recent {
		if s.HitRate < 0.98 {
			t.Errorf("sample %d hit rate = %v; want ~0.99", i, s.HitRate)
		}
	}
}

// The savings figure crosses the wire already computed, because the client has
// no price table and the session may have spanned several models.
func TestSavingsRideTheWireAlreadyPriced(t *testing.T) {
	u := provider.Usage{InputTokens: 1_000, CacheReadTokens: 99_000}
	provider.ApplyCost(provider.Model{
		PriceInput: 3, PriceCacheRead: 0.3, PriceCacheWrite: 3.75,
	}, &u)
	if u.CacheSavedUSD <= 0 {
		t.Fatalf("ApplyCost left no saving: %+v", u)
	}
	c := cacheView(u, u, []provider.Usage{u})
	if c.Session.CacheSavedUSD != u.CacheSavedUSD {
		t.Errorf("session saving = %v; want %v", c.Session.CacheSavedUSD, u.CacheSavedUSD)
	}
	if c.Recent[0].SavedUSD != u.CacheSavedUSD {
		t.Errorf("sample saving = %v; want %v", c.Recent[0].SavedUSD, u.CacheSavedUSD)
	}
}
