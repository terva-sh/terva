package modes

import (
	"regexp"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/tui"
)

// A label that fills or overflows the column must still be separated from its
// value. `%-10s` pads a short label and does nothing to a long one, which
// rendered "storespersonal /home/me/tickets" in a real /status block.
//
// Every label goes through i18n.T, so this is not only about the labels chosen
// here: a translation of any row may be longer than the column.
func TestStatusRowAlwaysSeparatesLabelFromValue(t *testing.T) {
	th := tui.Theme{Muted: 8, Accent: 4}

	for _, label := range []string{
		"cwd",           // short, padded as before
		"123456789",     // one under the column
		"1234567890",    // exactly the column width
		"ticket stores", // over it, the case that shipped broken
		"a very long label if some locale needs one",
	} {
		got := stripANSI([]string{statusRow(th, label, "VALUE")})
		if !strings.Contains(got, label) {
			t.Fatalf("label %q missing from %q", label, got)
		}
		rest := got[strings.Index(got, label)+len(label):]
		if !strings.HasPrefix(rest, " ") {
			t.Errorf("label %q runs straight into its value: %q", label, got)
		}
	}
}

// A configured ticket store names a directory outside the jail that the ticket
// tools may write, so /status shows it rather than leaving it to sit unread in
// a config file. No configured store is the default and renders nothing,
// because a row that is always present stops being read.
func TestStatusShowsTicketStoresOnlyWhenSet(t *testing.T) {
	th := tui.Theme{Muted: 8, Accent: 4}

	empty := stripANSI(statusRows(th, statusFacts{}))
	if strings.Contains(empty, "personal") {
		t.Errorf("no configured store should render no stores row:\n%s", empty)
	}

	set := stripANSI(statusRows(th, statusFacts{
		TicketStores: []config.TicketStore{
			{Name: "personal", Path: "/home/someone/tickets"},
			{Name: "ops", Path: "/home/someone/ops-tickets"},
		},
	}))
	// Match the label and the value TOGETHER. Checking each on its own is what
	// let "storespersonal" pass review: both substrings are present when the
	// two are glued into one word.
	if !regexp.MustCompile(`stores\s+personal /home/someone/tickets`).MatchString(set) {
		t.Errorf("the stores row is missing or glued to its label:\n%s", set)
	}
	if !strings.Contains(set, "ops /home/someone/ops-tickets") {
		t.Errorf("a second configured store must be visible too:\n%s", set)
	}
}

// A refused entry is the one a user most needs to see: they configured a store
// and it is not there. Dropping it silently leaves them with a store that
// never appears and no reason why.
func TestStatusShowsRefusedTicketStores(t *testing.T) {
	th := tui.Theme{Muted: 8, Accent: 4}

	empty := stripANSI(statusRows(th, statusFacts{}))
	if strings.Contains(empty, "refused") {
		t.Errorf("no refusals should render no row:\n%s", empty)
	}

	set := stripANSI(statusRows(th, statusFacts{
		TicketStoresRefused: []config.TicketStoreRefusal{
			{Name: "ops", Reason: `"ops-tickets" is relative`},
		},
	}))
	if !regexp.MustCompile(`refused\s+ops: `).MatchString(set) {
		t.Errorf("the refused row is missing or glued to its label:\n%s", set)
	}
	if !strings.Contains(set, "relative") {
		t.Errorf("a refused entry must name what to fix:\n%s", set)
	}
}
