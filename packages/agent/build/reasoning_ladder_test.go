package build

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// Every level the ladder ADVERTISES has to be one the flag ACCEPTS.
//
// The bug this guards against already shipped in the other direction: `max` was
// accepted by the switch while `--help` and the parse error both listed only up
// to `maximum`, so the tier that unlocks gpt-5.6's native ceiling was enforced
// and never advertised. Rendering those two from provider.ReasoningLevels fixed
// that half. The switch is still a hand-written list, so this enrolls a newly
// added rung rather than leaving it advertised-but-rejected.
//
// Self-enrolling on purpose: it reads the ladder rather than restating it, so
// adding a rung audits itself on the first run.
func TestEveryAdvertisedReasoningLevelIsAccepted(t *testing.T) {
	if len(provider.ReasoningLevels) == 0 {
		t.Fatal("the ladder is empty — this guard would pass vacuously")
	}
	for _, lv := range provider.ReasoningLevels {
		a, err := ParseArgs([]string{"--reasoning", lv})
		if err != nil {
			t.Errorf("--reasoning %s is advertised but rejected: %v", lv, err)
			continue
		}
		// "off" is stored verbatim and normalized downstream; what matters here
		// is that the flag round-trips the spelling the ladder taught.
		if a.Reasoning != lv {
			t.Errorf("--reasoning %s stored as %q", lv, a.Reasoning)
		}
	}

	// Teeth. Without this the loop above cannot fail if the flag ever stops
	// validating at all, and a guard that cannot fail is not a guard.
	_, err := ParseArgs([]string{"--reasoning", "bogus"})
	if err == nil {
		t.Fatal("an unknown level was accepted — the loop above proves nothing")
	}
	// And the refusal has to name the ladder, since that message is the only
	// place a user finds out what the valid rungs are.
	//
	// 🪤 Compared as TOKENS, not substrings. "maximum" contains "max", so a
	// strings.Contains check passes even when the message drops the top rung —
	// verified by neutering the message and watching a Contains-based version
	// of this test stay green. That is the precise shape of the bug being
	// guarded, so the guard must not share it.
	fields := strings.Fields(err.Error())
	if len(fields) == 0 {
		t.Fatalf("empty parse error: %v", err)
	}
	named := map[string]bool{}
	for _, tok := range strings.Split(fields[len(fields)-1], "|") {
		named[tok] = true
	}
	for _, lv := range provider.ReasoningLevels {
		if !named[lv] {
			t.Errorf("the parse error names %v, omitting %q — a user who typos is told a working value is invalid",
				fields[len(fields)-1], lv)
		}
	}
}
