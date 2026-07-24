package dialogs

import (
	"testing"

	"terva.sh/terva/packages/tui"
)

// The picker resolves to two different verbs, and the caller must be able to
// tell them apart. Clearing a credential leaves the provider standing to sign
// back into; removing an endpoint forgets which machine, which port and which
// context window, and nothing in terva remembers those but that entry. Handing
// a bare id back would make the two indistinguishable.
func TestLogoutDialogDistinguishesEndpointRows(t *testing.T) {
	d := NewLogoutDialog()
	if !d.Open([]LogoutItem{
		{Label: "Anthropic", Target: "anthropic", Method: "api key"},
		{Label: "workshop-3090", Target: "workshop-3090", Method: "endpoint - remove", Endpoint: true},
	}) {
		t.Fatal("dialog did not open")
	}

	act := d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if !act.Select {
		t.Fatal("enter did not select")
	}
	if act.Target != "anthropic" {
		t.Errorf("target = %q, want anthropic", act.Target)
	}
	if act.Endpoint {
		t.Error("a credential row came back marked as an endpoint; picking it would DELETE the provider")
	}

	d = NewLogoutDialog()
	d.Open([]LogoutItem{
		{Label: "Anthropic", Target: "anthropic", Method: "api key"},
		{Label: "workshop-3090", Target: "workshop-3090", Method: "endpoint - remove", Endpoint: true},
	})
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	act = d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if !act.Select {
		t.Fatal("enter did not select the endpoint row")
	}
	if !act.Endpoint {
		t.Error("the endpoint row did not come back marked; picking it would only clear a key it does not have")
	}
	// The tag must not survive into the id: it addresses config.json and auth.json.
	if act.Target != "workshop-3090" {
		t.Errorf("target = %q, want the bare endpoint id", act.Target)
	}
}

// An id that merely starts with the tag's text is still a plain target. The tag
// is an encoding, and an encoding that leaks into ordinary values is a bug that
// only shows up on someone's oddly-named provider.
func TestLogoutDialogDoesNotTagOrdinaryTargets(t *testing.T) {
	d := NewLogoutDialog()
	d.Open([]LogoutItem{{Label: "all", Target: "all"}})
	act := d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if act.Endpoint || act.Target != "all" {
		t.Errorf("target=%q endpoint=%v, want all/false", act.Target, act.Endpoint)
	}
}
