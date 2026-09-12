package modes

import (
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/tui"
)

// The default must render NO row. A line reading "terva calls itself terva" on
// every /status trains the operator to skip the row, and the row exists to be
// read on the one occasion it says something else.
func TestStatusShowsNoIdentityRowByDefault(t *testing.T) {
	for _, providers := range []map[string]config.ProviderSettings{
		nil,
		{},
		{"openai-codex": {}},
		{"openai-codex": {ClientIdentity: ""}},
	} {
		if got := nonDefaultClientIdentities(providers); len(got) != 0 {
			t.Errorf("nonDefaultClientIdentities(%#v) = %#v, want empty", providers, got)
		}
	}
}

// While it is on, it must be visible. This is the working agreement's
// requirement for a feature that changes how terva presents itself, and the row
// is the whole of terva's compliance with it.
func TestStatusNamesAnImpersonatedIdentity(t *testing.T) {
	got := nonDefaultClientIdentities(map[string]config.ProviderSettings{
		"openai-codex": {ClientIdentity: "native"},
	})
	want := []string{"openai-codex=native"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nonDefaultClientIdentities = %#v, want %#v", got, want)
	}
}

// Sorted, because Go randomises map iteration and a row that reorders between
// two /status calls reads as a setting that changed underneath the operator.
// Run enough times that an unsorted implementation cannot pass by luck.
func TestStatusIdentityRowIsStablyOrdered(t *testing.T) {
	providers := map[string]config.ProviderSettings{
		"openai-codex": {ClientIdentity: "native"},
		"another":      {ClientIdentity: "native"},
		"zebra":        {ClientIdentity: "native"},
		"middle":       {ClientIdentity: "native"},
	}
	want := []string{"another=native", "middle=native", "openai-codex=native", "zebra=native"}
	for i := 0; i < 50; i++ {
		if got := nonDefaultClientIdentities(providers); !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: got %#v, want %#v", i, got, want)
		}
	}
}

// A provider left on the default must not be listed alongside one that is not.
// Listing both would bury the fact the row exists to report.
func TestStatusIdentityRowListsOnlyTheSwitchedProviders(t *testing.T) {
	got := nonDefaultClientIdentities(map[string]config.ProviderSettings{
		"openai-codex": {ClientIdentity: "native"},
		"anthropic":    {ClientIdentity: ""},
	})
	want := []string{"openai-codex=native"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nonDefaultClientIdentities = %#v, want %#v", got, want)
	}
}

// The rendered block, not just the helper. statusRows is what the operator
// reads, and a field that never reaches a row is a field nobody sees.
func TestStatusRowsRenderTheIdentityWhenSet(t *testing.T) {
	th := tui.Theme{}

	off := strings.Join(statusRows(th, statusFacts{}), "\n")
	if strings.Contains(off, "openai-codex=native") {
		t.Errorf("the default block names an impersonated identity:\n%s", off)
	}

	on := strings.Join(statusRows(th, statusFacts{
		ClientIdentities: []string{"openai-codex=native"},
	}), "\n")
	if !strings.Contains(on, "openai-codex=native") {
		t.Errorf("the identity is switched on and the /status block does not say so:\n%s", on)
	}
}
