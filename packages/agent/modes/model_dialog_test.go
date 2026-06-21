package modes

import (
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// Ctrl+D opens a "set as default" scope prompt; p/g return a Select+Promote
// action with the scope; esc cancels the prompt without switching or closing.
func TestModelDialogPromoteFlow(t *testing.T) {
	newDialog := func() *modelDialog {
		d := newModelDialog()
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
	d := newModelDialog()
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
