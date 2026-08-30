package dialogs

import (
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// The TUI half of model visibility. The picker's job is to make a 300-model
// provider navigable, so these pin the two halves of that: hidden models are
// out of the way by default, and there is always a way back to them.

func hiddenCatalog(t *testing.T) {
	t.Helper()
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)
	provider.SetUserModels([]provider.Model{
		{Provider: "openrouter", ID: "anthropic/claude-sonnet-4.5"},
		{Provider: "openrouter", ID: "deepseek/deepseek-r1"},
		{Provider: "openrouter", ID: "meta/llama-3.1"},
	})
}

func viewIDs(d *ModelDialog) []string {
	out := make([]string, 0, len(d.p.view))
	for _, m := range d.p.view {
		out = append(out, m.ID)
	}
	return out
}

func TestHiddenModelsAreOutOfTheListByDefault(t *testing.T) {
	hiddenCatalog(t)
	d := NewModelDialog()
	d.Open("", []string{"openrouter"}, nil, []string{"openrouter/deepseek/deepseek-r1"})

	got := viewIDs(d)
	if len(got) != 2 {
		t.Fatalf("view = %v, want the 2 unhidden models", got)
	}
	for _, id := range got {
		if id == "deepseek/deepseek-r1" {
			t.Error("a hidden model must not appear in the normal list")
		}
	}
	// It stays in `all` regardless: the reveal token and the un-hide both need
	// a row to act on.
	if len(d.p.all) != 3 {
		t.Errorf("all = %d models, want 3 — hidden models are filtered, not dropped", len(d.p.all))
	}
}

// ":hidden" filters TO the hidden models, matching ":img" rather than acting as
// a "show everything" switch. This is the only route back to a model swallowed
// by a broad pattern, so it is the load-bearing half of the feature.
func TestHiddenTokenShowsOnlyTheHiddenModels(t *testing.T) {
	hiddenCatalog(t)
	d := NewModelDialog()
	d.Open("", []string{"openrouter"}, nil, []string{"openrouter/deepseek/deepseek-r1"})

	d.p.query = ":hidden"
	d.p.refilter()

	got := viewIDs(d)
	if len(got) != 1 || got[0] != "deepseek/deepseek-r1" {
		t.Fatalf(":hidden view = %v, want only the hidden model", got)
	}
}

func TestHiddenTokenComposesWithATextNeedle(t *testing.T) {
	hiddenCatalog(t)
	d := NewModelDialog()
	d.Open("", []string{"openrouter"}, nil, []string{
		"openrouter/deepseek/deepseek-r1",
		"openrouter/meta/llama-3.1",
	})

	d.p.query = ":hidden llama"
	d.p.refilter()

	got := viewIDs(d)
	if len(got) != 1 || got[0] != "meta/llama-3.1" {
		t.Fatalf("view = %v, want the one hidden model matching the needle", got)
	}
}

// Ctrl+K hides the highlighted model, and Ctrl+H is deliberately NOT the
// binding: a terminal sends Ctrl+H as 0x08, which this TUI reads as Backspace,
// so binding hide there would fire it on every backspace in the filter.
func TestCtrlKTogglesHiddenAndReportsIt(t *testing.T) {
	hiddenCatalog(t)
	d := NewModelDialog()
	d.Open("", []string{"openrouter"}, nil, nil)

	first := d.p.view[0].ID
	act := d.HandleKey(tui.Key{Kind: tui.KeyCtrlK})
	if !act.Hide || !act.HideOn {
		t.Fatalf("ctrl+k should hide the highlighted model: %+v", act)
	}
	if act.Model != first || act.Provider != "openrouter" {
		t.Errorf("action named %s/%s, want openrouter/%s", act.Provider, act.Model, first)
	}
	if !d.p.hidden["openrouter/"+first] {
		t.Error("the picker's own set must reflect the toggle immediately")
	}
	// It leaves the visible list at once, which is the whole point.
	for _, id := range viewIDs(d) {
		if id == first {
			t.Error("a just-hidden model should drop out of the visible list")
		}
	}
}

// The round trip: hide a model, find it again under :hidden, restore it.
// Without this a hidden model would be a one-way door.
func TestAHiddenModelCanBeRestoredThroughTheToken(t *testing.T) {
	hiddenCatalog(t)
	d := NewModelDialog()
	d.Open("", []string{"openrouter"}, nil, []string{"openrouter/deepseek/deepseek-r1"})

	d.p.query = ":hidden"
	d.p.refilter()
	if len(d.p.view) != 1 {
		t.Fatalf("expected the hidden model in view, got %v", viewIDs(d))
	}

	act := d.HandleKey(tui.Key{Kind: tui.KeyCtrlK})
	if !act.Hide || act.HideOn {
		t.Fatalf("ctrl+k on a hidden model should restore it: %+v", act)
	}
	if d.p.hidden["openrouter/deepseek/deepseek-r1"] {
		t.Error("the model should no longer be hidden")
	}
	// Clearing the token shows it among the normal rows again.
	d.p.query = ""
	d.p.refilter()
	if len(viewIDs(d)) != 3 {
		t.Errorf("after restoring, all 3 models should be visible, got %v", viewIDs(d))
	}
}

// The provider list counts what you can actually pick, and advertises what it
// is holding back — otherwise a shrinking number is unexplained.
func TestProviderRowCountsExcludeHiddenButAdvertiseThem(t *testing.T) {
	hiddenCatalog(t)
	provider.SetUserModels(append(provider.Active(), provider.Model{Provider: "other", ID: "solo"}))
	d := NewModelDialog()
	d.Open("", []string{"openrouter", "other"}, nil, []string{"openrouter/deepseek/deepseek-r1"})

	var row providerRow
	for _, r := range d.providers {
		if r.name == "openrouter" {
			row = r
		}
	}
	if row.count != 2 {
		t.Errorf("openrouter count = %d, want 2 visible models", row.count)
	}
	if row.hidden != 1 {
		t.Errorf("openrouter hidden = %d, want 1", row.hidden)
	}
}

// The trap this guards: the ONLY route to ":hidden" is through a provider's
// model list. If hiding a provider's last model dropped its row, every model it
// serves would become unreachable — unhideable except by hand-editing
// config.json, which is exactly the corner a UI feature must not paint into.
func TestAProviderWhoseModelsAreAllHiddenKeepsItsRow(t *testing.T) {
	hiddenCatalog(t)
	d := NewModelDialog()
	d.Open("", []string{"openrouter"}, nil, []string{
		"openrouter/anthropic/claude-sonnet-4.5",
		"openrouter/deepseek/deepseek-r1",
		"openrouter/meta/llama-3.1",
	})

	var found bool
	for _, r := range d.providers {
		if r.name == "openrouter" {
			found = true
			if r.count != 0 || r.hidden != 3 {
				t.Errorf("row = %d visible / %d hidden, want 0/3", r.count, r.hidden)
			}
		}
	}
	if !found {
		t.Fatal("a provider with every model hidden must KEEP its row, or the\n" +
			"models it serves can never be un-hidden from the picker again")
	}
}
