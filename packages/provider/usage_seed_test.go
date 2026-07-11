package provider

import (
	"context"
	"testing"
	"time"
)

// Tests for SeedClientUsage / UsageSeeder: carrying the passively-observed
// usage snapshot across a client rebuild (re-login, endpoint change) so the
// meters don't blank until the next turn.

func codexSnap(label string) UsageSnapshot {
	return UsageSnapshot{
		Provider:   "openai-codex",
		Windows:    []UsageWindow{{Label: label, UsedPercent: 42, WindowMinutes: 300}},
		CapturedAt: time.Now(),
	}
}

func TestSeedClientUsageCarriesSnapshot(t *testing.T) {
	dst := NewOpenAICodex("tok", "acct", "")
	SeedClientUsage(dst, codexSnap("5h"))
	got, ok := ClientUsage(dst)
	if !ok {
		t.Fatal("seeded client reports no usage")
	}
	if len(got.Windows) != 1 || got.Windows[0].Label != "5h" {
		t.Errorf("seeded snapshot = %+v, want the 5h window", got.Windows)
	}
}

func TestSeedClientUsageRejectsForeignProvider(t *testing.T) {
	dst := NewOpenAICodex("tok", "acct", "")
	snap := codexSnap("5h")
	snap.Provider = "openrouter"
	SeedClientUsage(dst, snap)
	if _, ok := ClientUsage(dst); ok {
		t.Error("a foreign provider's snapshot must not seed the codex client")
	}
}

func TestSeedClientUsageNeverDisplacesLiveObservation(t *testing.T) {
	dst := NewOpenAICodex("tok", "acct", "")
	SeedClientUsage(dst, codexSnap("5h"))
	// A second seed models a stale predecessor arriving after data exists
	// (live headers and seeds race through the same guard): first wins.
	SeedClientUsage(dst, codexSnap("weekly"))
	got, _ := ClientUsage(dst)
	if len(got.Windows) != 1 || got.Windows[0].Label != "5h" {
		t.Errorf("later seed displaced the earlier snapshot: %+v", got.Windows)
	}
}

func TestSeedClientUsageNonSeederIsNoOp(t *testing.T) {
	// A client without the seeder interface (the stub used elsewhere in the
	// usage tests) must be silently skipped, not panic.
	SeedClientUsage(NewOpenAI("tok", ""), codexSnap("5h"))
}

func TestSeedClientUsageThroughWrapper(t *testing.T) {
	// Wrappers hide the concrete client; the seed must reach it through the
	// same clientAs walk every other capability probe uses.
	inner := NewOpenAICodex("tok", "acct", "")
	wrapped := newPollingUsageClient(inner, time.Minute, func(ctx context.Context) (UsageSnapshot, error) {
		return UsageSnapshot{}, nil
	})
	SeedClientUsage(wrapped, codexSnap("5h"))
	got, ok := ClientUsage(inner)
	if !ok || len(got.Windows) != 1 || got.Windows[0].Label != "5h" {
		t.Errorf("seed did not reach the wrapped client: ok=%v %+v", ok, got.Windows)
	}
}
