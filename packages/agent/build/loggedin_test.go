package build

import (
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

func seedEndpoints(t *testing.T, eps map[string]config.EndpointConfig) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if err := config.MutateConfig(func(c *config.Config) { c.Endpoints = eps }); err != nil {
		t.Fatal(err)
	}
}

// The bug that made this function exist. Most local OpenAI-compatible servers
// want no key, so a named endpoint has NO credential to resolve — and a predicate
// that asks only the credential store reports it as a provider the user is signed
// out of, when it is in fact the one backend they are certain to reach.
//
// Two of the three copies of this predicate did exactly that.
func TestAKeylessNamedEndpointCountsAsLoggedIn(t *testing.T) {
	seedEndpoints(t, map[string]config.EndpointConfig{
		"workshop": {BaseURL: "http://127.0.0.1:1234/v1"},
	})
	if !LoggedInProviderSet()["workshop"] {
		t.Fatal("a keyless endpoint with a base URL is reachable, but it was not offered")
	}
}

// A half-written entry is not a backend: there is nothing to reach, and offering
// it would only produce a provider whose every turn fails.
func TestAnEndpointWithNoBaseURLIsNotOffered(t *testing.T) {
	seedEndpoints(t, map[string]config.EndpointConfig{
		"halfway": {BaseURL: "   "},
	})
	if LoggedInProviderSet()["halfway"] {
		t.Fatal("an endpoint with no base URL was offered as reachable")
	}
}

// ollama needs no credential at all, and a picker that hides it because the
// credential store is empty is hiding the provider that always works.
func TestOllamaIsAlwaysOffered(t *testing.T) {
	seedEndpoints(t, nil)
	if !LoggedInProviderSet()["ollama"] {
		t.Fatal("ollama needs no auth and must always be offered")
	}
}

// A map has no order. A picker that reshuffles its providers between two reads is
// a bug, so the list this returns must not depend on Go's map iteration.
func TestTheProviderOrderIsStable(t *testing.T) {
	seedEndpoints(t, map[string]config.EndpointConfig{
		"zeta":  {BaseURL: "http://127.0.0.1:1/v1"},
		"alpha": {BaseURL: "http://127.0.0.1:2/v1"},
		"mid":   {BaseURL: "http://127.0.0.1:3/v1"},
	})
	first := LoggedInProviders()
	for i := 0; i < 8; i++ {
		next := LoggedInProviders()
		if len(next) != len(first) {
			t.Fatalf("the list changed length between reads: %d then %d", len(first), len(next))
		}
		for j := range first {
			if first[j] != next[j] {
				t.Fatalf("the order changed between reads (%q then %q at %d)", first[j], next[j], j)
			}
		}
	}
}
