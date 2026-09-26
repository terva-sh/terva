package modes

// Leaving the model config editor lands back on the picker it was opened from,
// not at the prompt.
//
// 🪤 The editor used to be the only dialog opened from another dialog whose
// parent closed on the way in (ctrl+e called modelDialog.Close()). So `s`,
// `esc` and a finished reset all dropped the user out of /model entirely,
// losing the filter, the scope and the place in a list that can be hundreds of
// models long — for what is a one-level-deep sub-form.
//
// The registry order is half the fix and cannot be checked from either dialog
// alone: with both open, whichever entry comes first takes every key, so an
// editor registered BELOW the picker would be unreachable. These drive the real
// registry through the real key entry point for exactly that reason.

import (
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
	"terva.sh/terva/packages/tui"
)

// modelEditorFixture opens /model on a one-provider catalog (so the picker
// lands straight on the model list) with the real overlay wiring built.
func modelEditorFixture(t *testing.T) *Interactive {
	t.Helper()
	modelreg.ResetCatalogLayers()
	t.Cleanup(modelreg.ResetCatalogLayers)
	modelreg.RegisterExtraModel(provider.Model{
		Provider: "acme", ID: "shipped", ContextWindow: 4000, Source: "catalog",
	})

	i := newCtrlprotoTestInteractive()
	i.cfg.UserModelsPath = filepath.Join(testsupport.TempDir(t), "models.json")
	i.modelDialog = dialogs.NewModelDialog()
	i.modelEditDialog = dialogs.NewModelEditDialog()
	i.keymap = i.buildGlobalKeymap()
	i.overlays = i.buildOverlays()

	i.modelDialog.Open("", []string{"acme"}, nil, nil)
	return i
}

// openEditor presses ctrl+e on the highlighted model and checks the editor has
// the keyboard.
func openEditor(t *testing.T, i *Interactive) {
	t.Helper()
	i.handleKey(t.Context(), tui.Key{Kind: tui.KeyCtrlE})
	if !i.modelEditDialog.Active() {
		t.Fatal("ctrl+e did not open the config editor")
	}
	if e := i.activeOverlay(); e == nil || !e.active() || !i.modelEditDialog.Active() {
		t.Fatal("no overlay is active after ctrl+e")
	}
}

// The reported bug: `s` saves and leaves the model interface completely.
func TestSavingAnEditReturnsToThePicker(t *testing.T) {
	i := modelEditorFixture(t)
	openEditor(t, i)

	i.handleKey(t.Context(), tui.Key{Kind: tui.KeyRune, Rune: 's'})

	if i.modelEditDialog.Active() {
		t.Error("the editor stayed open after a save")
	}
	if !i.modelDialog.Active() {
		t.Error("a save exited /model altogether; it should step back to the model list")
	}
	ok, errText := statusOf(t, i)
	if errText != "" {
		t.Errorf("statusErr = %q, want a clean save", errText)
	}
	if !strings.Contains(ok, "saved settings") {
		t.Errorf("statusOK = %q, want the save to have gone through as well", ok)
	}
}

// Esc is the same step, without the write. It is the key the picker itself
// already defines as "back one level".
func TestCancellingAnEditReturnsToThePicker(t *testing.T) {
	i := modelEditorFixture(t)
	openEditor(t, i)

	i.handleKey(t.Context(), tui.Key{Kind: tui.KeyEsc})

	if i.modelEditDialog.Active() {
		t.Error("esc left the editor open")
	}
	if !i.modelDialog.Active() {
		t.Error("esc exited /model altogether; it should step back to the model list")
	}
}

// The keys have to reach the editor while it is up. If the picker outranked it
// in the registry, `s` would be swallowed by the type-to-filter instead and the
// two tests above would pass for the wrong reason — the editor would never have
// opened at all.
func TestTheEditorOutranksThePickerWhileBothAreOpen(t *testing.T) {
	i := modelEditorFixture(t)
	openEditor(t, i)

	if !i.modelDialog.Active() {
		t.Fatal("the picker closed when the editor opened; there is nothing to return to")
	}
	// A rune the editor claims (save) and the picker would otherwise eat as
	// filter input.
	i.handleKey(t.Context(), tui.Key{Kind: tui.KeyRune, Rune: 's'})
	if ok, _ := statusOf(t, i); !strings.Contains(ok, "saved settings") {
		t.Errorf("the picker's filter swallowed the editor's save key (statusOK = %q)", ok)
	}
}
