package workspace

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/testsupport"
)

// auth.providers must report the switch, because nothing else the pane shows
// can imply it.
//
// The store's own view of a lapsed subscription is "expired" — the same word it
// uses for a token a refresh would quietly fix. Whether the refresh was actually
// REFUSED is a verdict only boot holds, since only boot attempted it. Without
// this field the panel renders a lapsed provider as one row among seven, every
// row accurate, and the fact that turns are landing on a different account
// appears nowhere at all.
func TestAuthProvidersReportsTheSwitch(t *testing.T) {
	lapsedPinWithWorkingAlternative(t)

	w, err := NewWorkspace(build.Args{CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	view, err := w.AuthProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sw := view.Switch
	if sw == nil {
		t.Fatal("ProvidersView.Switch is nil; the pane cannot say which account turns will bill")
	}
	if sw.From != "anthropic" {
		t.Errorf("From = %q, want anthropic", sw.From)
	}
	if sw.To == "" || sw.To == sw.From {
		t.Errorf("To = %q; the pane would offer the provider that just failed", sw.To)
	}
	if !sw.Lapsed {
		t.Error("Lapsed = false for an expired subscription; the pane would not offer a re-login as the remedy")
	}
	if sw.Reason == "" {
		t.Error("Reason is empty; the pane has nothing to explain the switch with")
	}
	// Wire-safe: this view goes to a browser, and the pane's own doc promises
	// nothing on it is a secret.
	if strings.Contains(sw.Reason, "expired-access-token") {
		t.Errorf("Reason leaks the token: %q", sw.Reason)
	}
}

// No switch, no field. A pane that renders a switch banner on an ordinary boot
// would train the reader to ignore it.
func TestAuthProvidersOmitsTheSwitchWhenThePinWorks(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	view, err := w.AuthProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Switch != nil {
		t.Errorf("Switch = %+v on a healthy boot", view.Switch)
	}
}
