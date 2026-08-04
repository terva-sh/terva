package workspace

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
)

// The retract contract: the detector's zero event MUST render to the empty
// string, because an empty message is what the keyed-note surface understands
// as "take it down". A placeholder like "recovered" would pin a stale line.
func TestCacheCliffNoteRetractsOnZeroEvent(t *testing.T) {
	if got := cacheCliffNote(core.CacheCliff{}); got != "" {
		t.Fatalf("zero event rendered %q, want empty (the retract)", got)
	}
}

// The note must carry the two numbers the user acts on — how long this has
// been going on and how much it has wasted — at note precision.
func TestCacheCliffNoteNamesTheRun(t *testing.T) {
	msg := cacheCliffNote(core.CacheCliff{Dispatches: 12, RereadTokens: 1_400_000, Ongoing: true})
	if msg == "" {
		t.Fatal("ongoing event rendered empty — that retracts the note")
	}
	for _, want := range []string{"12", "1.4M", "/compact"} {
		if !strings.Contains(msg, want) {
			t.Errorf("note %q does not mention %q", msg, want)
		}
	}
}

// Compaction shrinks each miss; it does not end the run (a measured session
// took two compactions and stayed pinned at the floor for 120 more
// dispatches). The note may therefore mention /compact, but must not offer it
// as the remedy — someone who reads it that way spends a summarization
// round-trip on a provider-side outage it cannot stop.
func TestCacheCliffNoteDoesNotSellCompactAsTheFix(t *testing.T) {
	msg := cacheCliffNote(core.CacheCliff{Dispatches: 12, RereadTokens: 1_400_000, Ongoing: true})
	if !strings.Contains(msg, "does not end the run") {
		t.Errorf("note %q must say plainly that compaction does not end the run", msg)
	}
	// The escape hatch that does work has to be named, or the note reports a
	// problem with no action attached.
	if !strings.Contains(msg, "new session") && !strings.Contains(msg, "another model") {
		t.Errorf("note %q names no action that actually ends the run", msg)
	}
	// A conditional promise ("cuts the cost IF it keeps up") is the exact
	// wording that read as a fix. Guard the shape, not just the old string.
	for _, banned := range []string{"cuts the cost", "if it keeps up"} {
		if strings.Contains(msg, banned) {
			t.Errorf("note %q still promises %q", msg, banned)
		}
	}
}

func TestRoughTokens(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{999, "999"},
		{45_000, "45K"},
		{312_500, "312K"},
		{1_400_000, "1.4M"},
	} {
		if got := roughTokens(tc.n); got != tc.want {
			t.Errorf("roughTokens(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
