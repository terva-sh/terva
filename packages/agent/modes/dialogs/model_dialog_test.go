package dialogs

import (
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// Ctrl+D opens a "set as default" scope prompt; p/g return a Select+Promote
// action with the scope; esc cancels the prompt without switching or closing.
func TestModelDialogPromoteFlow(t *testing.T) {
	newDialog := func() *ModelDialog {
		d := NewModelDialog()
		d.p.setCatalog([]provider.Model{{Provider: "anthropic", ID: "opus"}}, "opus", 14)
		d.active = true
		return d
	}

	// Ctrl+D enters promote mode without emitting an action.
	d := newDialog()
	if act := d.HandleKey(tui.Key{Kind: tui.KeyCtrlD}); act.Select || act.Promote {
		t.Fatalf("Ctrl+D should open the prompt, not act: %+v", act)
	}
	if !d.promoting {
		t.Fatal("Ctrl+D should enter promote mode")
	}
	// 'g' promotes globally AND switches, then closes.
	act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'g'})
	if !act.Select || !act.Promote || act.Scope != "global" || act.Model != "opus" {
		t.Fatalf("'g' should promote global + select opus: %+v", act)
	}
	if d.active || d.promoting {
		t.Fatal("dialog should close after a promote")
	}

	// 'p' promotes to project scope.
	d = newDialog()
	d.HandleKey(tui.Key{Kind: tui.KeyCtrlD})
	act = d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'p'})
	if !act.Select || !act.Promote || act.Scope != "project" {
		t.Fatalf("'p' should promote to project: %+v", act)
	}

	// esc cancels the prompt: no action, dialog stays open.
	d = newDialog()
	d.HandleKey(tui.Key{Kind: tui.KeyCtrlD})
	act = d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if act.Promote || act.Select || act.Close {
		t.Fatalf("esc in promote mode should be a quiet cancel: %+v", act)
	}
	if d.promoting || !d.active {
		t.Fatalf("esc should leave promote mode but keep the picker open (promoting=%v active=%v)", d.promoting, d.active)
	}

	// A plain Enter (no Ctrl+D) is a session-only switch: Select, not Promote.
	d = newDialog()
	act = d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if !act.Select || act.Promote {
		t.Fatalf("Enter should be a session-only switch (Select, not Promote): %+v", act)
	}
}

// The /model picker's ":token" query words filter by capability while
// the remaining words keep fuzzy-matching ids. Unrecognized tokens
// (including ones still being typed) have no effect.
func TestModelDialogCapabilityFilter(t *testing.T) {
	d := NewModelDialog()
	d.p.all = sortedModels([]provider.Model{
		{Provider: "p", ID: "seer", Reasoning: false},
		{Provider: "p", ID: "blind", Reasoning: true,
			Caps: map[provider.Capability]bool{provider.CapImageInput: false}},
		{Provider: "p", ID: "painter",
			Caps: map[provider.Capability]bool{provider.CapImageOutput: true}},
	})
	d.active = true

	ids := func() []string {
		var out []string
		for _, m := range d.p.view {
			out = append(out, m.ID)
		}
		return out
	}
	set := func(q string) {
		d.p.query = q
		d.p.refilter()
	}

	set(":img")
	if got := ids(); len(got) != 2 || got[0] != "painter" || got[1] != "seer" {
		t.Errorf(":img view = %v, want [painter seer] (blind excluded)", got)
	}

	set(":reasoning")
	if got := ids(); len(got) != 1 || got[0] != "blind" {
		t.Errorf(":reasoning view = %v, want [blind]", got)
	}

	set(":imggen")
	if got := ids(); len(got) != 1 || got[0] != "painter" {
		t.Errorf(":imggen view = %v, want [painter]", got)
	}

	// Capability token + text needle compose.
	set(":img seer")
	if got := ids(); len(got) != 1 || got[0] != "seer" {
		t.Errorf(":img seer view = %v, want [seer]", got)
	}

	// A token mid-typing (unrecognized) must not hide anything.
	set(":i")
	if got := ids(); len(got) != 3 {
		t.Errorf(":i view = %v, want all 3 (unrecognized token ignored)", got)
	}
}

// Two providers + no favorites: the dialog opens on the provider stage;
// selecting a provider scopes the model list; esc walks back then closes.
func TestModelDialogTwoLevel(t *testing.T) {
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	provider.SetUserModels([]provider.Model{
		{Provider: "alpha", ID: "a-1"}, {Provider: "alpha", ID: "a-2"},
		{Provider: "beta", ID: "b-1"},
	})
	d := NewModelDialog()
	d.Open("", []string{"alpha", "beta"}, nil)

	if d.stage != stageProvider {
		t.Fatalf("want provider stage, got %d", d.stage)
	}
	if len(d.providers) != 2 { // alpha, beta; no ★ favorites entry
		t.Fatalf("want 2 provider rows, got %+v", d.providers)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // select alpha (cursor 0)
	if d.stage != stageModel || d.scopeLabel != "alpha" {
		t.Fatalf("expected model stage scoped to alpha (stage=%d scope=%q)", d.stage, d.scopeLabel)
	}
	if len(d.p.all) != 2 {
		t.Fatalf("alpha should scope to 2 models, got %d", len(d.p.all))
	}
	if act := d.HandleKey(tui.Key{Kind: tui.KeyEsc}); act.Close {
		t.Fatal("esc in model stage should go back to providers, not close")
	}
	if d.stage != stageProvider {
		t.Fatalf("esc should return to provider stage, got %d", d.stage)
	}
	if act := d.HandleKey(tui.Key{Kind: tui.KeyEsc}); !act.Close || d.Active() {
		t.Fatal("esc at provider stage should close the dialog")
	}
}

// A single provider with no favorites skips straight into its models; esc
// then closes (there's no provider list to return to).
func TestModelDialogSingleProviderSkips(t *testing.T) {
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	provider.SetUserModels([]provider.Model{
		{Provider: "solo", ID: "s-1"}, {Provider: "solo", ID: "s-2"},
	})
	d := NewModelDialog()
	d.Open("", []string{"solo"}, nil)
	if d.stage != stageModel || !d.single {
		t.Fatalf("single provider should skip to model stage (stage=%d single=%v)", d.stage, d.single)
	}
	if act := d.HandleKey(tui.Key{Kind: tui.KeyEsc}); !act.Close {
		t.Fatal("esc with a single provider should close")
	}
}

// Favorites add a ★ entry at the top of the provider list that scopes to the
// starred models across providers.
func TestModelDialogFavoritesView(t *testing.T) {
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	provider.SetUserModels([]provider.Model{
		{Provider: "alpha", ID: "a-1"}, {Provider: "beta", ID: "b-1"},
	})
	d := NewModelDialog()
	d.Open("", []string{"alpha", "beta"}, []string{"beta/b-1"})

	if len(d.providers) != 3 || !d.providers[0].fav {
		t.Fatalf("expected ★ favorites first + 2 providers, got %+v", d.providers)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // enter ★ favorites
	if d.scopeLabel != "★ favorites" || len(d.p.all) != 1 || d.p.all[0].ID != "b-1" {
		t.Fatalf("favorites view wrong: scope=%q models=%+v", d.scopeLabel, d.p.all)
	}
}

// Ctrl+F toggles a favorite, floats it to the top, keeps the cursor on it,
// and emits a Favorite action for the host to persist.
func TestModelDialogFavoriteToggle(t *testing.T) {
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	provider.SetUserModels([]provider.Model{
		{Provider: "solo", ID: "s-1"}, {Provider: "solo", ID: "s-2"},
	})
	d := NewModelDialog()
	d.Open("", []string{"solo"}, nil) // single -> model stage

	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // cursor -> s-2
	if sel, _ := d.p.selected(); sel.ID != "s-2" {
		t.Fatalf("cursor should be on s-2, got %q", sel.ID)
	}
	act := d.HandleKey(tui.Key{Kind: tui.KeyCtrlF})
	if !act.Favorite || !act.FavOn || act.Model != "s-2" {
		t.Fatalf("Ctrl+F should favorite s-2: %+v", act)
	}
	if !d.p.favorites["solo/s-2"] {
		t.Error("favorite not recorded in the picker set")
	}
	if d.p.view[0].ID != "s-2" {
		t.Errorf("favorited s-2 should float to the top, got %q", d.p.view[0].ID)
	}
	if cur, _ := d.p.selected(); cur.ID != "s-2" {
		t.Errorf("cursor should follow the favorited model, got %q", cur.ID)
	}
	// Toggling again unfavorites it.
	act = d.HandleKey(tui.Key{Kind: tui.KeyCtrlF})
	if !act.Favorite || act.FavOn {
		t.Fatalf("second Ctrl+F should unfavorite: %+v", act)
	}
	if d.p.favorites["solo/s-2"] {
		t.Error("favorite should be cleared after the second toggle")
	}
}

// Favoriting rebuilds the provider list so the ★ favorites entry and its count
// appear without reopening (regression: they were built once at Open).
func TestModelDialogFavoriteUpdatesProviderList(t *testing.T) {
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	provider.SetUserModels([]provider.Model{
		{Provider: "solo", ID: "s-1"}, {Provider: "solo", ID: "s-2"},
	})
	d := NewModelDialog()
	d.Open("", []string{"solo"}, nil) // single provider -> model stage, no ★ yet

	for _, r := range d.providers {
		if r.fav {
			t.Fatal("no favorites entry should exist before any favorite")
		}
	}
	d.HandleKey(tui.Key{Kind: tui.KeyCtrlF}) // favorite the highlighted model
	if len(d.providers) == 0 || !d.providers[0].fav || d.providers[0].count != 1 {
		t.Fatalf("favoriting should add a ★ entry with count 1: %+v", d.providers)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyDown})  // move to the other model
	d.HandleKey(tui.Key{Kind: tui.KeyCtrlF}) // favorite it too
	if d.providers[0].count != 2 {
		t.Errorf("favorites count should update live to 2, got %d", d.providers[0].count)
	}
}

// An open picker picks up models that arrive via background /v1/models
// discovery after it opened (the OpenRouter "only 1 model until restart" case).
func TestModelDialogReloadsOnCatalogGrowth(t *testing.T) {
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	provider.SetLiveModels([]provider.Model{{Provider: "grow", ID: "g-1"}})

	d := NewModelDialog()
	d.Open("", []string{"grow"}, nil) // single provider, 1 model -> model stage
	_ = d.Render(tui.Theme{}, 80)
	if len(d.p.all) != 1 {
		t.Fatalf("should start with 1 model, got %d", len(d.p.all))
	}

	// Discovery completes and grows the catalog.
	provider.SetLiveModels([]provider.Model{
		{Provider: "grow", ID: "g-1"}, {Provider: "grow", ID: "g-2"}, {Provider: "grow", ID: "g-3"},
	})
	_ = d.Render(tui.Theme{}, 80) // re-render polls the revision and re-scopes
	if len(d.p.all) != 3 {
		t.Errorf("open picker should pick up the 3 grown models, got %d", len(d.p.all))
	}
}
