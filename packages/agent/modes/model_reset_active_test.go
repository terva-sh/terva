package modes

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// seedModelsJSON writes a one-entry models.json and loads it into the user
// layer the way reapplyUserModels does, so the catalog matches what the file
// says before the reset runs.
func seedModelsJSON(t *testing.T, prov, id string) string {
	t.Helper()
	path := filepath.Join(testsupport.TempDir(t), "models.json")
	body := fmt.Sprintf(`{"providers":{%q:{"models":[{"id":%q,"contextWindow":1000}]}}}`, prov, id)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	overrides, _ := provider.LoadUserModelsWithWarnings(path)
	provider.SetUserOverrides(overrides)
	return path
}

// resetHarness builds an Interactive sitting on prov/model with a recording
// carrier.
func resetHarness(t *testing.T, path, prov, model string) (*Interactive, *fakeCarrier) {
	t.Helper()
	i := &Interactive{}
	i.cfg.UserModelsPath = path
	i.cfg.Provider = prov
	i.cfg.Model = model
	fc := newFakeCarrier()
	i.cfg.Carrier = fc
	return i, fc
}

// switched reports whether a SwitchModel call was recorded, without blocking.
func switched(fc *fakeCarrier) bool {
	select {
	case <-fc.switches:
		return true
	default:
		return false
	}
}

// 🪤 Deleting the model the session is running on used to report a clean
// success over a failure.
//
// The entry goes, the model leaves the catalog, and refreshActiveModel then
// asked switchModel to re-resolve it. FindModel fails there, so it returned
// "unknown model" without touching the session, and that error reached the
// status line. setStatusOK ran a moment later and clears statusErr, so the
// user was told the delete worked and never told the swap had not.
//
// There is nothing to re-resolve TO, so the swap can only fail. Skip it, and
// say what actually happened instead.
func TestDeletingTheActiveCustomModelSkipsTheDoomedSwap(t *testing.T) {
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	path := seedModelsJSON(t, "acme", "invented")

	i, fc := resetHarness(t, path, "acme", "invented")
	i.applyModelReset("acme", "invented")

	if switched(fc) {
		t.Error("a swap was attempted for a model that had just left the catalog; " +
			"it can only fail, and its error is what the success message then hid")
	}
	ok, errMsg := statusOf(t, i)
	if errMsg != "" {
		t.Errorf("status error = %q, want none: the delete itself succeeded", errMsg)
	}
	if !strings.Contains(ok, "keeps using it") {
		t.Errorf("the status line does not say the session is still on the deleted model: %q", ok)
	}
}

// The same delete on a model the session is NOT using needs none of that
// wording, because nothing is left pointing at it.
func TestDeletingANonActiveCustomModelKeepsThePlainMessage(t *testing.T) {
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	path := seedModelsJSON(t, "acme", "invented")

	// The session is on something else entirely.
	i, fc := resetHarness(t, path, "acme", "in-use")
	i.applyModelReset("acme", "invented")

	if switched(fc) {
		t.Error("a swap was attempted for a model the session was not using")
	}
	ok, _ := statusOf(t, i)
	if !strings.Contains(ok, "existed only in models.json") {
		t.Errorf("wrong message for a delete that orphaned nothing: %q", ok)
	}
	if strings.Contains(ok, "keeps using it") {
		t.Errorf("the orphan warning fired for a model the session was not on: %q", ok)
	}
}

// The half a careless guard breaks. Resetting the active model when it has a
// catalog row underneath it MUST still re-resolve: that is how the restored
// context window and base URL reach the running session.
func TestResettingTheActiveCatalogRowStillReResolves(t *testing.T) {
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	provider.RegisterExtraModel(provider.Model{
		Provider: "acme", ID: "shipped", ContextWindow: 4000, Source: "catalog",
	})
	path := seedModelsJSON(t, "acme", "shipped")

	i, fc := resetHarness(t, path, "acme", "shipped")
	i.applyModelReset("acme", "shipped")

	if !switched(fc) {
		t.Error("no swap was attempted after resetting the active model; the session " +
			"keeps the overridden context window instead of the restored one")
	}
	ok, _ := statusOf(t, i)
	if !strings.Contains(ok, "to defaults") {
		t.Errorf("a tweaked catalog row should reset to defaults, got: %q", ok)
	}
}
