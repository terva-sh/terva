package core

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

func TestRecentUsageKeepsTheTailOldestFirst(t *testing.T) {
	var c CostTracker
	for i := 1; i <= 5; i++ {
		c.Add(provider.Usage{InputTokens: i})
	}
	got := c.RecentUsage()
	if len(got) != 5 {
		t.Fatalf("len = %d; want 5", len(got))
	}
	for i, u := range got {
		if u.InputTokens != i+1 {
			t.Fatalf("recent[%d].InputTokens = %d; want %d (oldest first)", i, u.InputTokens, i+1)
		}
	}
}

func TestRecentUsageEvictsTheOldest(t *testing.T) {
	var c CostTracker
	for i := 1; i <= recentCap+10; i++ {
		c.Add(provider.Usage{InputTokens: i})
	}
	got := c.RecentUsage()
	if len(got) != recentCap {
		t.Fatalf("len = %d; want %d", len(got), recentCap)
	}
	if got[0].InputTokens != 11 {
		t.Errorf("oldest kept = %d; want 11", got[0].InputTokens)
	}
	if got[len(got)-1].InputTokens != recentCap+10 {
		t.Errorf("newest kept = %d; want %d", got[len(got)-1].InputTokens, recentCap+10)
	}
}

// The strip must show what this session's own prompts did with the cache.
// Compaction runs a transcript-sized request whose usage is real spend but is
// NOT a turn's prompt — the same distinction AddTotalOnly already draws for the
// context gauge. Let it into the strip and every auto-compact plants a fake
// cache miss in the middle of the picture.
func TestCompactionAndDelegationStaySpendNotStrip(t *testing.T) {
	var c CostTracker
	c.Add(provider.Usage{InputTokens: 1, CacheReadTokens: 90_000})
	c.AddTotalOnly(provider.Usage{InputTokens: 120_000}) // a compaction
	c.AddDelegated(provider.Usage{InputTokens: 40_000})  // a sub-agent
	c.Add(provider.Usage{InputTokens: 2, CacheReadTokens: 95_000})

	got := c.RecentUsage()
	if len(got) != 2 {
		t.Fatalf("strip has %d entries; want 2 — only the session's own requests belong", len(got))
	}
	for _, u := range got {
		if u.CacheReadTokens == 0 {
			t.Errorf("a non-turn request reached the strip: %+v", u)
		}
	}
	// The money still counts, though: both were charged to the session.
	if want := 1 + 120_000 + 40_000 + 2; c.CumulativeTotal().InputTokens != want {
		t.Errorf("total input = %d; want %d — the spend is real even when the prompt is not ours",
			c.CumulativeTotal().InputTokens, want)
	}
}

// RecentUsage hands out a copy. A caller that sorts or truncates the slice it
// got back must not be able to reach into the tracker's own tail.
func TestRecentUsageHandsOutACopy(t *testing.T) {
	var c CostTracker
	c.Add(provider.Usage{InputTokens: 7})
	got := c.RecentUsage()
	got[0].InputTokens = 999
	if again := c.RecentUsage(); again[0].InputTokens != 7 {
		t.Errorf("mutating the returned slice changed the tracker: %d", again[0].InputTokens)
	}
}
