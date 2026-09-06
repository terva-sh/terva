package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
)

// models.add is the way in. Everything else about a custom model already worked:
// a hand-written models.json entry reached both pickers, was editable through
// models.params.set, and could be dropped through models.params.reset. Only
// creating one required leaving terva and opening the file.

// modelAddWorkspace gives a workspace on a throwaway TERVA_HOME with one
// reachable provider that needs no credential: a keyless named endpoint.
//
// A named endpoint on purpose, rather than ollama. It is the case a bare
// ResolveCredential sweep misses, and the case only the TUI's copy of this
// predicate ever learned about, so testing through one exercises the reason
// build.LoggedInProviders exists at all.
func modelAddWorkspace(t *testing.T, authJSON string) *Workspace {
	t.Helper()
	seedCreds(t, authJSON)
	if err := config.MutateConfig(func(c *config.Config) {
		c.Endpoints = map[string]config.EndpointConfig{
			"workshop": {BaseURL: "http://127.0.0.1:1234/v1"},
		}
	}); err != nil {
		t.Fatal(err)
	}
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)
	return &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
}

// wireCode returns the code a client would branch on. A refusal that carries no
// code is a string a frontend can only show, not act on.
func wireCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("wanted a refusal, got nil")
	}
	var ce *ctrlproto.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a *ctrlproto.Error, so nothing can branch on it", err)
	}
	return ce.Code
}

// The whole point: a model terva has no catalog row for becomes usable, and it
// arrives marked as one the entry INVENTED rather than one it tweaked. Without
// Synthetic the picker would offer "reset to defaults" on a model that has none.
func TestModelAddCreatesAModelTheCatalogDoesNotShip(t *testing.T) {
	w := modelAddWorkspace(t, "")
	ctx := context.Background()

	err := w.ModelAdd(ctx, ctrlproto.ModelAddParams{
		Provider: "workshop", Model: "invented-local",
		Values: map[string]string{"contextWindow": "262144", "maxTokens": "8192"},
	})
	if err != nil {
		t.Fatalf("ModelAdd: %v", err)
	}

	// It resolves, which is what makes it selectable at all.
	m, err := provider.FindModel("workshop", "invented-local")
	if err != nil {
		t.Fatalf("the model does not resolve after being added: %v", err)
	}
	if !m.Synthetic {
		t.Error("Synthetic is false: the picker would offer to reset a model that has no defaults to reset to")
	}
	if m.ContextWindow != 262144 {
		t.Errorf("context window = %d, want the 262144 that was sent", m.ContextWindow)
	}

	// And it is on disk, under the provider key it was filed against.
	if _, ok, err := provider.FindUserModel(config.UserModelsPath(), "workshop", "invented-local"); err != nil || !ok {
		t.Fatalf("models.json holds no entry for it (ok=%v, err=%v)", ok, err)
	}

	// The view a client renders has to agree, or the delete control lies.
	v, err := w.ModelParams(ctx, ctrlproto.ModelParamsParams{Provider: "workshop", Model: "invented-local"})
	if err != nil {
		t.Fatalf("ModelParams: %v", err)
	}
	if !v.Custom {
		t.Error("the view reports Custom false, so a client would say 'reset to defaults' over a delete")
	}
}

// An id is the one thing a clone cannot supply.
func TestModelAddRefusesAnEmptyID(t *testing.T) {
	w := modelAddWorkspace(t, "")

	err := w.ModelAdd(context.Background(), ctrlproto.ModelAddParams{
		Provider: "workshop", Model: "   ",
	})
	if got := wireCode(t, err); got != ctrlproto.CodeBadRequest {
		t.Errorf("code = %q, want %q", got, ctrlproto.CodeBadRequest)
	}
}

// The guard that makes this a separate method rather than a flag. An id that
// already answers is an edit, and letting a create through here would overwrite
// an existing entry's settings without the caller ever naming it.
func TestModelAddRefusesAnIDThatAlreadyResolves(t *testing.T) {
	w := modelAddWorkspace(t, "")
	ctx := context.Background()
	add := ctrlproto.ModelAddParams{
		Provider: "workshop", Model: "invented-local",
		Values: map[string]string{"contextWindow": "262144"},
	}

	if err := w.ModelAdd(ctx, add); err != nil {
		t.Fatalf("first add: %v", err)
	}
	err := w.ModelAdd(ctx, add)
	if got := wireCode(t, err); got != ctrlproto.CodeBadRequest {
		t.Errorf("code = %q, want %q", got, ctrlproto.CodeBadRequest)
	}
	// The remedy is a different verb, so the message has to point at it.
	if !strings.Contains(err.Error(), "edit") {
		t.Errorf("refusal %q does not tell the caller to edit the model instead", err)
	}
}

// The same guard against the catalog rather than against models.json. This is
// the one that matters: without it a create could shadow a shipped row, and the
// user would silently be talking to their own half-specified copy of it.
func TestModelAddRefusesAnIDTheCatalogAlreadyShips(t *testing.T) {
	w := modelAddWorkspace(t, `{"anthropic":{"api_key":"sk-a"}}`)

	shipped := provider.ModelsForProvider("anthropic")
	if len(shipped) == 0 {
		t.Skip("no anthropic models in the catalog")
	}

	err := w.ModelAdd(context.Background(), ctrlproto.ModelAddParams{
		Provider: "anthropic", Model: shipped[0].ID,
		Values: map[string]string{"contextWindow": "1000"},
	})
	if got := wireCode(t, err); got != ctrlproto.CodeBadRequest {
		t.Errorf("code = %q, want %q: a create over a shipped model id was accepted", got, ctrlproto.CodeBadRequest)
	}
}

// Reachable, not registered. An entry under a provider with no credential writes
// correctly and then never appears, because both pickers filter on this same
// predicate. That reads as a failed save with nothing to explain it.
//
// The code is no_credential rather than bad_request because the remedy is a
// login, and a host with a login flow keys on exactly that code to offer one.
func TestModelAddRefusesAProviderWithNoCredential(t *testing.T) {
	w := modelAddWorkspace(t, "")

	// No auth.json at all, so anthropic has nothing to resolve.
	err := w.ModelAdd(context.Background(), ctrlproto.ModelAddParams{
		Provider: "anthropic", Model: "invented-anthropic",
		Values: map[string]string{"contextWindow": "200000"},
	})
	if got := wireCode(t, err); got != ctrlproto.CodeNoCredential {
		t.Errorf("code = %q, want %q", got, ctrlproto.CodeNoCredential)
	}

	// And nothing was written, or a later login would surface a model the user
	// never successfully added.
	if _, ok, _ := provider.FindUserModel(config.UserModelsPath(), "anthropic", "invented-anthropic"); ok {
		t.Error("the refused model was written to models.json anyway")
	}
}

// Validation is the edit path's validation, by the same registry code, so a
// refusal names the box the operator typed into rather than the request.
func TestModelAddRefusesABadValueAndNamesTheSetting(t *testing.T) {
	w := modelAddWorkspace(t, "")

	err := w.ModelAdd(context.Background(), ctrlproto.ModelAddParams{
		Provider: "workshop", Model: "invented-local",
		Values: map[string]string{"contextWindow": "not-a-number"},
	})
	if got := wireCode(t, err); got != ctrlproto.CodeBadRequest {
		t.Errorf("code = %q, want %q", got, ctrlproto.CodeBadRequest)
	}
	if !strings.Contains(err.Error(), "context window") {
		t.Errorf("refusal %q does not name the setting that was wrong", err)
	}
}
