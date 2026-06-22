package provider

import (
	"testing"
	"time"
)

// IsCurrent gates re-discovery on BOTH freshness and the discovery-set version,
// so a cache written by an older binary (or before versioning) forces a refresh
// even within CacheTTL — that's how a newly-added provider (opencode-go) gets
// picked up instead of waiting out the 6h TTL.
func TestModelCacheIsCurrent(t *testing.T) {
	now := time.Now()

	if c := (ModelCache{FetchedAt: now, Version: ModelCacheVersion}); !c.IsCurrent() {
		t.Error("fresh + current version should be current")
	}

	// Fresh, but an older discovery set: must re-discover.
	old := ModelCache{FetchedAt: now, Version: ModelCacheVersion - 1}
	if !old.IsFresh() {
		t.Fatal("precondition: an older-version cache should still be time-fresh")
	}
	if old.IsCurrent() {
		t.Error("an older discovery version should force re-discovery even when fresh")
	}

	// A pre-versioning cache (Version 0) — exactly the case that left opencode-go
	// frozen — must be treated as stale.
	if legacy := (ModelCache{FetchedAt: now}); legacy.IsCurrent() {
		t.Error("a pre-versioning cache (version 0) should not be current")
	}

	// Expired is never current, regardless of version.
	if exp := (ModelCache{FetchedAt: now.Add(-7 * time.Hour), Version: ModelCacheVersion}); exp.IsCurrent() {
		t.Error("an expired cache should not be current")
	}
}
