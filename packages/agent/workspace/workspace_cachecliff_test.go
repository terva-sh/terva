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
