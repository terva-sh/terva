package provider

import "testing"

func TestUsageObservationRecordAndSnapshot(t *testing.T) {
	var o usageObservation

	if _, ok := o.snapshot(); ok {
		t.Fatal("fresh observation: ok=true, want false")
	}

	// A failed parse (ok=false) must not create or clobber a snapshot.
	o.record(UsageSnapshot{Provider: "x"}, false)
	if _, ok := o.snapshot(); ok {
		t.Fatal("record(ok=false) on empty: ok=true, want false")
	}

	o.record(UsageSnapshot{Provider: "openai", Windows: []UsageWindow{{Label: "tokens"}}}, true)
	got, ok := o.snapshot()
	if !ok || got.Provider != "openai" || len(got.Windows) != 1 {
		t.Fatalf("after record: ok=%v snap=%+v", ok, got)
	}

	// A later ok=false leaves the good snapshot intact.
	o.record(UsageSnapshot{}, false)
	if got, ok := o.snapshot(); !ok || got.Provider != "openai" {
		t.Fatalf("record(ok=false) clobbered a good snapshot: ok=%v snap=%+v", ok, got)
	}
}

func TestUsageObservationSeed(t *testing.T) {
	// Foreign provider is rejected.
	var o usageObservation
	o.seed(UsageSnapshot{Provider: "deepseek"}, "openai-codex")
	if _, ok := o.snapshot(); ok {
		t.Error("seed with foreign provider took effect, want no-op")
	}

	// Matching provider primes an empty store.
	o.seed(UsageSnapshot{Provider: "openai-codex", Windows: []UsageWindow{{Label: "5h"}}}, "openai-codex")
	got, ok := o.snapshot()
	if !ok || len(got.Windows) != 1 {
		t.Fatalf("seed on empty store: ok=%v snap=%+v", ok, got)
	}

	// A live observation always wins: a later seed is a no-op.
	o = usageObservation{}
	o.record(UsageSnapshot{Provider: "openai-codex", Windows: []UsageWindow{{Label: "live"}}}, true)
	o.seed(UsageSnapshot{Provider: "openai-codex", Windows: []UsageWindow{{Label: "stale"}}}, "openai-codex")
	if got, _ := o.snapshot(); got.Windows[0].Label != "live" {
		t.Errorf("seed displaced a live observation: got %q, want live", got.Windows[0].Label)
	}
}
