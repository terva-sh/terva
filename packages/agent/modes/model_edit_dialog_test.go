package modes

import (
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

func rn(r rune) tui.Key          { return tui.Key{Kind: tui.KeyRune, Rune: r} }
func kind(k tui.KeyKind) tui.Key { return tui.Key{Kind: k} }

// fieldIdx finds a form row by key (robust to the registry's row order).
func fieldIdx(d *modelEditDialog, key string) int {
	for i, f := range d.fields {
		if f.key == key {
			return i
		}
	}
	return -1
}

// moveTo walks the cursor to the named field.
func moveTo(d *modelEditDialog, key string) {
	for d.cursor < fieldIdx(d, key) {
		d.HandleKey(kind(tui.KeyDown))
	}
	for d.cursor > fieldIdx(d, key) {
		d.HandleKey(kind(tui.KeyUp))
	}
}

// A bare save (no edits, no existing override) must write a minimal
// entry — just the id — so every field keeps inheriting its default.
func TestModelEditSaveNoEdits(t *testing.T) {
	d := newModelEditDialog()
	d.Open(provider.Model{Provider: "anthropic", ID: "claude-x", ContextWindow: 200000, MaxOutput: 64000}, provider.UserModel{}, false)

	act := d.HandleKey(rn('s'))
	if !act.Save {
		t.Fatalf("expected Save action, got %+v", act)
	}
	e := act.Entry
	if e.ID != "claude-x" || e.BaseURL != "" || e.ContextWindow != 0 || e.MaxTokens != 0 || e.Reasoning != nil || e.Capabilities != nil {
		t.Errorf("no-edit save should be a bare entry, got %+v", e)
	}
}

// Editing a numeric field: enter to edit, type digits, enter to commit,
// then save carries the value. Non-digit input is ignored.
func TestModelEditNumericField(t *testing.T) {
	d := newModelEditDialog()
	d.Open(provider.Model{Provider: "anthropic", ID: "claude-x", ContextWindow: 200000}, provider.UserModel{}, false)

	d.HandleKey(kind(tui.KeyDown)) // -> context window (index 1)
	d.HandleKey(kind(tui.KeyEnter))
	d.HandleKey(rn('1'))
	d.HandleKey(rn('a')) // ignored: digits only
	d.HandleKey(rn('5'))
	d.HandleKey(kind(tui.KeyEnter)) // commit

	act := d.HandleKey(rn('s'))
	if !act.Save || act.Entry.ContextWindow != 15 {
		t.Errorf("want contextWindow=15, got %+v", act.Entry)
	}
}

// reasoning cycles inherit -> on -> off -> inherit, and save writes a
// tri-state pointer (nil when inheriting, &false when explicitly off).
func TestModelEditReasoningTriState(t *testing.T) {
	d := newModelEditDialog()
	d.Open(provider.Model{Provider: "anthropic", ID: "claude-x"}, provider.UserModel{}, false)
	moveTo(d, "reasoning")
	rf := func() editField { return d.fields[fieldIdx(d, "reasoning")] }

	if rf().set {
		t.Fatal("reasoning should start inheriting")
	}
	d.HandleKey(kind(tui.KeyEnter)) // -> on
	if !rf().set || !rf().on {
		t.Errorf("inherit->on failed: %+v", rf())
	}
	d.HandleKey(kind(tui.KeyEnter)) // -> off
	if !rf().set || rf().on {
		t.Errorf("on->off failed: %+v", rf())
	}
	act := d.HandleKey(rn('s'))
	if act.Entry.Reasoning == nil || *act.Entry.Reasoning {
		t.Errorf("explicit-off must write &false, got %+v", act.Entry.Reasoning)
	}
}

// Opening with an existing override pre-fills the fields, and saving
// unchanged round-trips them (base url + an explicit image-input:false).
func TestModelEditPrefillPreserves(t *testing.T) {
	existing := provider.UserModel{
		ID:           "claude-x",
		BaseURL:      "http://local:1234",
		Capabilities: map[string]bool{"image-input": false},
	}
	d := newModelEditDialog()
	d.Open(provider.Model{Provider: "anthropic", ID: "claude-x"}, existing, true)

	if d.fields[fieldIdx(d, "baseUrl")].value != "http://local:1234" {
		t.Errorf("base url not pre-filled: %q", d.fields[fieldIdx(d, "baseUrl")].value)
	}
	if ii := d.fields[fieldIdx(d, "imageInput")]; !ii.set || ii.on { // pre-filled explicit-off
		t.Errorf("image-input prefill wrong: %+v", ii)
	}
	act := d.HandleKey(rn('s'))
	if act.Entry.BaseURL != "http://local:1234" {
		t.Errorf("base url lost on save: %q", act.Entry.BaseURL)
	}
	if v, ok := act.Entry.Capabilities["image-input"]; !ok || v {
		t.Errorf("image-input not preserved: present=%v value=%v", ok, v)
	}
}

// Editing a managed field must NOT drop fields the editor doesn't touch
// — hand-set prices and api carry through an entry-replacing save.
func TestModelEditPreservesUnmanagedFields(t *testing.T) {
	existing := provider.UserModel{
		ID:          "claude-x",
		PriceInput:  3.5,
		PriceOutput: 17.0,
		API:         "anthropic-messages",
	}
	d := newModelEditDialog()
	d.Open(provider.Model{Provider: "anthropic", ID: "claude-x"}, existing, true)

	d.HandleKey(kind(tui.KeyDown)) // -> context window
	d.HandleKey(kind(tui.KeyEnter))
	for _, r := range "123000" {
		d.HandleKey(rn(r))
	}
	d.HandleKey(kind(tui.KeyEnter))

	act := d.HandleKey(rn('s'))
	if act.Entry.ContextWindow != 123000 {
		t.Errorf("context window not applied: %+v", act.Entry)
	}
	if act.Entry.PriceInput != 3.5 || act.Entry.PriceOutput != 17.0 || act.Entry.API != "anthropic-messages" {
		t.Errorf("unmanaged fields dropped on edit: %+v", act.Entry)
	}
}

// Cycling a previously-set field back to inherit clears it in the saved
// entry, so the model returns to the default for that field.
func TestModelEditClearsInheritedField(t *testing.T) {
	on := true
	existing := provider.UserModel{ID: "claude-x", Reasoning: &on, BaseURL: "http://x"}
	d := newModelEditDialog()
	d.Open(provider.Model{Provider: "anthropic", ID: "claude-x"}, existing, true)

	moveTo(d, "reasoning")          // pre-filled set+on
	d.HandleKey(kind(tui.KeyEnter)) // on -> off
	d.HandleKey(kind(tui.KeyEnter)) // off -> inherit

	act := d.HandleKey(rn('s'))
	if act.Entry.Reasoning != nil {
		t.Errorf("reasoning should clear to inherit (nil), got %v", *act.Entry.Reasoning)
	}
	if act.Entry.BaseURL != "http://x" {
		t.Errorf("untouched base url should survive: %q", act.Entry.BaseURL)
	}
}

// temperature is a registry-driven float field: it accepts a decimal in
// [0,2], filters out letters, canonicalizes on commit, and rejects out-of-range.
func TestModelEditTemperatureField(t *testing.T) {
	d := newModelEditDialog()
	d.Open(provider.Model{Provider: "anthropic", ID: "claude-x"}, provider.UserModel{}, false)
	moveTo(d, "temperature")
	d.HandleKey(kind(tui.KeyEnter)) // edit
	for _, r := range "0.7" {
		d.HandleKey(rn(r))
	}
	d.HandleKey(rn('a'))            // ignored: float allows digits + '.'
	d.HandleKey(kind(tui.KeyEnter)) // commit
	act := d.HandleKey(rn('s'))
	if act.Entry.Temperature == nil || *act.Entry.Temperature != float32(0.7) {
		t.Fatalf("want temperature=0.7, got %v", act.Entry.Temperature)
	}

	// Out-of-range (>2) is rejected: stays in edit mode with a status.
	d2 := newModelEditDialog()
	d2.Open(provider.Model{Provider: "anthropic", ID: "claude-x"}, provider.UserModel{}, false)
	moveTo(d2, "temperature")
	d2.HandleKey(kind(tui.KeyEnter))
	d2.HandleKey(rn('3'))
	d2.HandleKey(kind(tui.KeyEnter)) // try to commit 3
	if !d2.editing || d2.status == "" {
		t.Errorf("temperature 3 should be rejected (editing=%v status=%q)", d2.editing, d2.status)
	}
}

// An AdaptiveThinking model shows temperature as inapplicable rather than a
// blank inherit, so the user isn't misled into setting a value it ignores.
func TestModelEditTemperatureAdaptiveThinking(t *testing.T) {
	d := newModelEditDialog()
	d.Open(provider.Model{Provider: "anthropic", ID: "opus", AdaptiveThinking: true}, provider.UserModel{}, false)
	f := d.fields[fieldIdx(d, "temperature")]
	if f.inherit != "n/a (adaptive thinking)" {
		t.Errorf("adaptive-thinking temperature inherit hint = %q; want the n/a sentinel", f.inherit)
	}
}

// Reset is a confirm-then-act flow, gated on there being an override.
func TestModelEditResetConfirm(t *testing.T) {
	d := newModelEditDialog()
	d.Open(provider.Model{Provider: "anthropic", ID: "claude-x"}, provider.UserModel{ID: "claude-x", BaseURL: "http://x"}, true)

	d.HandleKey(rn('r'))
	if !d.confirmingReset {
		t.Fatal("r should open the reset confirmation")
	}
	if act := d.HandleKey(rn('n')); act.Reset || d.confirmingReset {
		t.Errorf("n should cancel the confirmation, got act=%+v confirming=%v", act, d.confirmingReset)
	}

	d.HandleKey(rn('r'))
	act := d.HandleKey(rn('y'))
	if !act.Reset || act.Provider != "anthropic" || act.ModelID != "claude-x" {
		t.Errorf("y should emit Reset for the model, got %+v", act)
	}
}

func TestModelEditResetGatedWithoutOverride(t *testing.T) {
	d := newModelEditDialog()
	d.Open(provider.Model{Provider: "anthropic", ID: "claude-x"}, provider.UserModel{}, false)
	act := d.HandleKey(rn('r'))
	if act.Reset || d.confirmingReset {
		t.Error("reset must be gated when the model has no override")
	}
	if d.status == "" {
		t.Error("expected a 'nothing to reset' hint")
	}
}

// Ctrl+E in the picker emits an Edit action for the selected model and
// closes the picker so the editor overlay takes over.
func TestModelDialogCtrlEOpensEditor(t *testing.T) {
	d := newModelDialog()
	d.active = true
	d.p.setCatalog([]provider.Model{{Provider: "anthropic", ID: "claude-x"}}, "", 14)

	act := d.HandleKey(kind(tui.KeyCtrlE))
	if !act.Edit || act.Provider != "anthropic" || act.Model != "claude-x" {
		t.Errorf("ctrl+e should emit Edit for the selected model, got %+v", act)
	}
	if d.Active() {
		t.Error("picker should close when handing off to the editor")
	}
}
