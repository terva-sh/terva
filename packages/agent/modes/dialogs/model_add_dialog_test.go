package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// The add form reserves two field keys of its own, "id" and "provider", and
// save assembles the entry by looking every field's key up in the registry.
// A registry param that ever took one of those keys would be handed the model
// id or the provider name as its value, and fieldValue would answer with
// whichever row came first.
//
// Self-enrolling on purpose: it reads the registry rather than listing keys,
// so a param added later is covered without anyone remembering this file.
func TestNoRegistryParamCollidesWithTheAddFormsOwnKeys(t *testing.T) {
	reserved := map[string]bool{"id": true, "provider": true}
	params := provider.ModelParams()
	if len(params) == 0 {
		t.Fatal("the registry declares no params, so this guard would pass vacuously")
	}
	for _, p := range params {
		if reserved[p.Key] {
			t.Errorf("registry param %q collides with the add form's own row: "+
				"saveAdd would write the %s into it, and fieldValue would answer "+
				"with whichever row is listed first", p.Key, p.Key)
		}
	}
}

func addField(t *testing.T, d *ModelEditDialog, key string) editField {
	t.Helper()
	for _, f := range d.fields {
		if f.key == key {
			return f
		}
	}
	t.Fatalf("the add form has no %q row", key)
	return editField{}
}

// 🪤 The clone seeds from each param's DEFAULT, never its override.
//
// Default is the effective value resolved off the source model. Override is
// only what models.json pins, and most clone sources pin nothing at all. Seed
// from Override and the common case hands back a form of empty boxes, which
// creates a model with no context window: no gauge, and auto-condensing never
// fires. That is the exact failure clone-from was chosen to avoid.
func TestAddFormSeedsFromTheEffectiveValueNotTheOverride(t *testing.T) {
	d := NewModelEditDialog()
	// A catalog row with real numbers and NO models.json entry, which is what
	// almost every clone source looks like.
	src := provider.Model{Provider: "anthropic", ID: "claude-x", ContextWindow: 200000, MaxOutput: 64000}

	d.OpenAdd(src, []string{"anthropic", "workshop"}, "")

	if got := addField(t, d, "contextWindow").value; got != "200000" {
		t.Errorf("context window seeded as %q, want the source's effective 200000: "+
			"an empty seed here creates a model that never auto-condenses", got)
	}
}

// The id starts empty and is the one thing a clone cannot supply.
func TestAddFormRefusesAnEmptyID(t *testing.T) {
	d := NewModelEditDialog()
	d.OpenAdd(provider.Model{Provider: "anthropic", ID: "claude-x", ContextWindow: 200000}, []string{"anthropic"}, "")

	act := d.saveAdd()
	if act.Add {
		t.Fatal("an add with no id was accepted")
	}
	if !strings.Contains(d.status, "id") {
		t.Errorf("status %q does not say the id is what is missing", d.status)
	}
	if !d.Active() {
		t.Error("the form closed on a refusal, so the operator would retype everything")
	}
}

// A blank window is not "inherit" on a model with nothing underneath it. It is
// zero, which is safe but inert.
func TestAddFormRefusesABlankContextWindow(t *testing.T) {
	d := NewModelEditDialog()
	d.OpenAdd(provider.Model{Provider: "anthropic", ID: "claude-x", ContextWindow: 200000}, []string{"anthropic"}, "")

	for i := range d.fields {
		switch d.fields[i].key {
		case "id":
			d.fields[i].value = "invented-local"
		case "contextWindow":
			d.fields[i].value = ""
		}
	}

	act := d.saveAdd()
	if act.Add {
		t.Fatal("an add with no context window was accepted")
	}
	if !strings.Contains(d.status, "context window") {
		t.Errorf("status %q does not name the context window", d.status)
	}
}

// The provider row defaults to the clone source and stays editable. Pinning it
// would make one case unreachable: a provider the user is logged into that has
// no models yet gets no picker row, so there is nothing there to clone from.
func TestAddFormFilesTheModelUnderTheChosenProvider(t *testing.T) {
	d := NewModelEditDialog()
	d.OpenAdd(provider.Model{Provider: "anthropic", ID: "claude-x", ContextWindow: 200000}, []string{"anthropic", "workshop"}, "")

	if got := addField(t, d, "provider").value; got != "anthropic" {
		t.Errorf("provider seeded as %q, want the clone source's anthropic", got)
	}
	for i := range d.fields {
		switch d.fields[i].key {
		case "id":
			d.fields[i].value = "invented-local"
		case "provider":
			d.fields[i].value = "workshop"
		}
	}

	act := d.saveAdd()
	if !act.Add {
		t.Fatalf("a complete form was refused: %q", d.status)
	}
	if act.Provider != "workshop" {
		t.Errorf("filed under %q, want the workshop the operator chose", act.Provider)
	}
	if act.ModelID != "invented-local" || act.Entry.ID != "invented-local" {
		t.Errorf("action names %q / entry names %q, want invented-local", act.ModelID, act.Entry.ID)
	}
	if act.Entry.ContextWindow != 200000 {
		t.Errorf("entry context window = %d, want the seeded 200000", act.Entry.ContextWindow)
	}
	if act.Save {
		t.Error("an add emitted Save too; the host commits the two through different verbs")
	}
}

// A required enum has no inherit state to pass through, so cycling it wraps
// straight back to the first option. Landing on "" would offer a provider of
// nothing, which saveAdd would then have to refuse.
func TestTheProviderRowNeverCyclesThroughEmpty(t *testing.T) {
	d := NewModelEditDialog()
	d.OpenAdd(provider.Model{Provider: "alpha", ID: "m", ContextWindow: 1000}, []string{"alpha", "beta"}, "")

	f := addField(t, d, "provider")
	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		cycleField(&f)
		seen[f.value] = true
	}
	if seen[""] {
		t.Error("the provider row cycled through the empty state")
	}
	if !seen["alpha"] || !seen["beta"] {
		t.Errorf("cycling did not reach both providers: %v", seen)
	}
}

// After an add the picker reopens scoped to the new model, with it under the
// cursor, and does NOT switch to it. Enter is then the whole cost for someone
// who added a model in order to use it, and nobody who was only registering one
// gets moved off the model they were on.
func TestOpenAtPutsTheNamedModelUnderTheCursorWithoutSwitching(t *testing.T) {
	shipped := provider.ModelsForProvider("anthropic")
	if len(shipped) < 2 {
		t.Skip("need two anthropic models to tell selection from the default landing")
	}
	// Not shipped[0]: landing on the first row is what the picker does anyway,
	// so it would pass with no cursor move at all.
	target := shipped[len(shipped)-1]
	current := shipped[0].ID

	d := NewModelDialog()
	d.OpenAt(current, []string{"anthropic"}, nil, nil, "anthropic", target.ID)

	sel, ok := d.p.selected()
	if !ok {
		t.Fatal("the picker reopened with nothing selected")
	}
	if sel.ID != target.ID {
		t.Errorf("cursor is on %q, want the just-added %q", sel.ID, target.ID)
	}
	// current is the ● marker, which is what "did we switch?" is stored in.
	if d.current != current {
		t.Errorf("active model moved to %q; an add must not switch the session", d.current)
	}
}

// A model the list does not hold must not leave the picker somewhere that does
// not contain what the status line just named.
func TestOpenAtFallsBackWhenTheModelIsNotThere(t *testing.T) {
	if len(provider.ModelsForProvider("anthropic")) == 0 {
		t.Skip("no anthropic models in the catalog")
	}
	d := NewModelDialog()
	d.OpenAt("", []string{"anthropic"}, nil, nil, "nonesuch", "not-a-model")

	if !d.Active() {
		t.Error("the picker did not open at all")
	}
}

// Ctrl+N emits Add for the SELECTED model, which is the clone source rather
// than the model being created, and closes the picker for the form.
func TestModelDialogCtrlNOpensTheAddForm(t *testing.T) {
	d := NewModelDialog()
	d.active = true
	d.p.setCatalog([]provider.Model{{Provider: "anthropic", ID: "claude-x"}}, "", 14)

	act := d.HandleKey(kind(tui.KeyCtrlN))
	if !act.Add || act.Provider != "anthropic" || act.Model != "claude-x" {
		t.Errorf("ctrl+n should emit Add naming the clone source, got %+v", act)
	}
	if act.Edit {
		t.Error("ctrl+n emitted Edit as well, which would open the wrong form")
	}
	if d.Active() {
		t.Error("the picker should close when handing off to the add form")
	}
}
