package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

// A non-empty pack_registries is a standing exemption from the egress guard's
// address policy, so /status shows it rather than leaving it to sit unread in
// a config file. Empty is the default and renders nothing, because a row that
// is always present stops being read.
func TestStatusShowsPackRegistriesOnlyWhenSet(t *testing.T) {
	th := tui.Theme{Muted: 8, Accent: 4}

	empty := stripANSI(statusRows(th, statusFacts{}))
	if strings.Contains(empty, "packs") {
		t.Errorf("an empty allowlist should render no packs row:\n%s", empty)
	}

	set := stripANSI(statusRows(th, statusFacts{
		PackRegistries: []string{"https://packs.internal.corp", "https://packs.example.test"},
	}))
	// Both entries, because showing only the first would hide an exemption.
	for _, want := range []string{"packs.internal.corp", "packs.example.test"} {
		if !strings.Contains(set, want) {
			t.Errorf("a configured registry must be visible in /status, missing %q:\n%s", want, set)
		}
	}
}
