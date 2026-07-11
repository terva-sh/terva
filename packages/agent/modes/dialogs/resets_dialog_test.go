package dialogs

import (
	"errors"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/tui"
)

func availableList() ctrlproto.ResetsListResult {
	return ctrlproto.ResetsListResult{
		Supported: true,
		Resets: []ctrlproto.ResetInfo{
			{ID: "cr_spent", Status: "redeemed", Title: "Full reset", RedeemedAt: "2026-06-02T00:00:01Z"},
			{ID: "cr_ok", Status: "available", Title: "Full reset", ExpiresAt: "2026-07-18T00:00:00Z"},
		},
	}
}

func rune_(r rune) tui.Key { return tui.Key{Kind: tui.KeyRune, Rune: r} }

// The core safety property: a consume action is emitted ONLY after enter-to-arm
// followed by 'y', and only for an available credit.
func TestResetsDialogConfirmGate(t *testing.T) {
	d := NewResetsDialog()
	d.Open("openai-codex")
	d.SetList(availableList(), nil)

	// clampCursor should land on the available credit (index 1), not the spent one.
	if r, ok := d.selectedCredit(); !ok || r.ID != "cr_ok" {
		t.Fatalf("cursor landed on %+v, want cr_ok", r)
	}

	// Enter arms the confirm; it must NOT itself consume.
	if act := d.HandleKey(tui.Key{Kind: tui.KeyEnter}); act.Consume {
		t.Fatal("enter alone emitted a consume action")
	}
	if d.phase != resetsConfirm {
		t.Fatalf("phase = %v, want resetsConfirm", d.phase)
	}

	// 'y' at the confirm emits exactly one consume for the selected credit.
	act := d.HandleKey(rune_('y'))
	if !act.Consume || act.CreditID != "cr_ok" {
		t.Fatalf("confirm 'y' = %+v, want consume cr_ok", act)
	}
}

func TestResetsDialogConfirmCancelDoesNotConsume(t *testing.T) {
	for _, k := range []tui.Key{{Kind: tui.KeyEsc}, rune_('n'), rune_('x')} {
		d := NewResetsDialog()
		d.Open("openai-codex")
		d.SetList(availableList(), nil)
		d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // arm
		if act := d.HandleKey(k); act.Consume {
			t.Errorf("key %v at confirm consumed; must cancel", k)
		}
		if d.phase != resetsList {
			t.Errorf("key %v did not return to list", k)
		}
	}
}

// Enter on a non-available (spent) credit must not arm the confirm at all.
func TestResetsDialogEnterOnSpentIsInert(t *testing.T) {
	d := NewResetsDialog()
	d.Open("openai-codex")
	d.SetList(availableList(), nil)
	d.cursor = 0 // the redeemed credit
	if act := d.HandleKey(tui.Key{Kind: tui.KeyEnter}); act.Consume {
		t.Fatal("enter on a spent credit emitted consume")
	}
	if d.phase != resetsList {
		t.Errorf("enter on a spent credit armed confirm (phase %v)", d.phase)
	}
}

func TestResetsDialogUnsupportedRenders(t *testing.T) {
	d := NewResetsDialog()
	d.Open("anthropic")
	d.SetList(ctrlproto.ResetsListResult{Supported: false}, nil)
	out := strings.Join(d.Render(tui.Theme{}, 60), "\n")
	if !strings.Contains(out, "doesn't offer usage resets") {
		t.Errorf("unsupported provider should say so; got %q", out)
	}
}

func TestResetsDialogListErrorShown(t *testing.T) {
	d := NewResetsDialog()
	d.Open("openai-codex")
	d.SetList(ctrlproto.ResetsListResult{}, errors.New("boom"))
	out := strings.Join(d.Render(tui.Theme{}, 60), "\n")
	if !strings.Contains(out, "boom") {
		t.Errorf("list error should render; got %q", out)
	}
}
