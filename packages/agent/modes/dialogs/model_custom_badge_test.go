package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// renderPickerRowsFor opens a picker scoped to one provider and returns its
// rendered rows as one string. A single provider with no favorites skips the
// provider stage, but the guard keeps this honest if that ever changes.
//
// The zero Theme is deliberate: colour codes are not what these cases assert,
// and the tags are plain text inside them either way.
func renderPickerRowsFor(t *testing.T, prov string) string {
	t.Helper()
	d := NewModelDialog()
	d.Open("", []string{prov}, nil, nil)
	if d.stage == stageProvider {
		d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	}
	return strings.Join(d.p.renderRows(tui.Theme{}, 120), "\n")
}

// The two models.json cases have to read differently in the list, because the
// key that acts on them does two different things. [edited] on a catalog row
// means "reset restores the shipped values". The same word on a model that
// exists only in models.json invites the user to reset it and find out, and
// finding out costs them the row.
func TestPickerTagsACustomModelApartFromAnEditedOne(t *testing.T) {
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()

	// Nothing underneath it: the append branch stamps Synthetic.
	provider.SetUserOverrides([]provider.UserOverride{
		{Model: provider.Model{Provider: "acme", ID: "invented", Source: "user"}},
	})

	out := renderPickerRowsFor(t, "acme")
	if !strings.Contains(out, "[custom]") {
		t.Errorf("a models.json-only model is not tagged [custom]:\n%s", out)
	}
	if strings.Contains(out, "[edited]") {
		t.Errorf("a models.json-only model was tagged [edited]; that word promises that "+
			"a reset restores defaults, and this model has none:\n%s", out)
	}
}

// The other half. A tweak on a row some lower layer supplied keeps [edited],
// because resetting it really does restore the catalog values.
func TestPickerTagsATweakedCatalogRowAsEdited(t *testing.T) {
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()

	provider.RegisterExtraModel(provider.Model{
		Provider: "acme", ID: "shipped", ContextWindow: 1000, Source: "catalog",
	})
	provider.SetUserOverrides([]provider.UserOverride{
		{Model: provider.Model{Provider: "acme", ID: "shipped", ContextWindow: 2000, Source: "user"}},
	})

	out := renderPickerRowsFor(t, "acme")
	if !strings.Contains(out, "[edited]") {
		t.Errorf("a tweaked catalog row lost its [edited] tag:\n%s", out)
	}
	if strings.Contains(out, "[custom]") {
		t.Errorf("a tweaked catalog row was tagged [custom]; that word promises that "+
			"removing the entry deletes the model, and here it restores defaults:\n%s", out)
	}
}

// The confirm prompt is the last thing the user reads before the row goes away,
// so it is the one place the difference has to be unambiguous. Same key, same
// models.json write, two outcomes.
func TestResetConfirmSaysDeleteForASyntheticModel(t *testing.T) {
	d := NewModelEditDialog()
	d.Open(
		provider.Model{Provider: "acme", ID: "invented", Synthetic: true},
		provider.UserModel{ID: "invented"}, true, "",
	)
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'r'})

	out := strings.Join(d.Render(tui.Theme{}, 120), "\n")
	if !strings.Contains(out, "delete") {
		t.Errorf("the confirm prompt for a models.json-only model does not say delete:\n%s", out)
	}
	if strings.Contains(out, "reset all custom settings") {
		t.Errorf("the confirm prompt offers to restore defaults that do not exist:\n%s", out)
	}
}

// And the catalog-row wording is unchanged, which is the half a careless branch
// breaks while the synthetic case looks right.
func TestResetConfirmStillSaysResetForACatalogRow(t *testing.T) {
	d := NewModelEditDialog()
	d.Open(
		provider.Model{Provider: "acme", ID: "shipped", Source: "user"},
		provider.UserModel{ID: "shipped"}, true, "",
	)
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'r'})

	out := strings.Join(d.Render(tui.Theme{}, 120), "\n")
	if !strings.Contains(out, "reset all custom settings") {
		t.Errorf("the confirm prompt for a tweaked catalog row lost its reset wording:\n%s", out)
	}
	if strings.Contains(out, "delete") {
		t.Errorf("the confirm prompt threatens to delete a model that has a catalog row:\n%s", out)
	}
}
