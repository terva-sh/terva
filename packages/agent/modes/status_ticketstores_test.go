package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/tui"
)

// A configured ticket store names a directory outside the jail that the ticket
// tools may write, so /status shows it rather than leaving it to sit unread in
// a config file. No configured store is the default and renders nothing,
// because a row that is always present stops being read.
func TestStatusShowsTicketStoresOnlyWhenSet(t *testing.T) {
	th := tui.Theme{Muted: 8, Accent: 4}

	empty := stripANSI(statusRows(th, statusFacts{}))
	if strings.Contains(empty, "ticket stores") {
		t.Errorf("no configured store should render no ticket stores row:\n%s", empty)
	}

	set := stripANSI(statusRows(th, statusFacts{
		TicketStores: []config.TicketStore{
			{Name: "personal", Path: "/home/someone/tickets"},
			{Name: "ops", Path: "/home/someone/ops-tickets"},
		},
	}))
	// Every entry, name and path. Showing only the first would hide a
	// directory the ticket tools may write.
	for _, want := range []string{"personal", "/home/someone/tickets", "ops", "/home/someone/ops-tickets"} {
		if !strings.Contains(set, want) {
			t.Errorf("a configured store must be visible in /status, missing %q:\n%s", want, set)
		}
	}
}

// A refused entry is the one a user most needs to see: they configured a store
// and it is not there. Dropping it silently leaves them with a store that
// never appears and no reason why.
func TestStatusShowsRefusedTicketStores(t *testing.T) {
	th := tui.Theme{Muted: 8, Accent: 4}

	empty := stripANSI(statusRows(th, statusFacts{}))
	if strings.Contains(empty, "stores refused") {
		t.Errorf("no refusals should render no row:\n%s", empty)
	}

	set := stripANSI(statusRows(th, statusFacts{
		TicketStoresRefused: []config.TicketStoreRefusal{
			{Name: "ops", Reason: `"ops-tickets" is relative`},
		},
	}))
	for _, want := range []string{"ops", "relative"} {
		if !strings.Contains(set, want) {
			t.Errorf("a refused entry must name what to fix, missing %q:\n%s", want, set)
		}
	}
}
