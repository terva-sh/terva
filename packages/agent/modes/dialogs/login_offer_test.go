package dialogs

import (
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/testsupport"
	"terva.sh/terva/packages/tui"
)

func openDialog(t *testing.T) *LoginDialog {
	t.Helper()
	d := NewLoginDialog()
	d.Open(auth.NewStore(filepath.Join(testsupport.TempDir(t), "auth.json")))
	return d
}

func pressLogin(d *LoginDialog, kinds ...tui.KeyKind) loginDialogAction {
	var act loginDialogAction
	for _, k := range kinds {
		act = d.HandleKey(tui.Key{Kind: k})
	}
	return act
}

// The offer row is reachable and yields the pair it names.
//
// Reachability is the half worth asserting: the cursor bound used to be a
// literal `max := 2` sitting a hundred lines from the literal list it was
// bounding, so a third row would render and simply refuse to take the cursor —
// visible, unselectable, and not obviously a bug from either side.
func TestOfferedProviderRowIsSelectable(t *testing.T) {
	d := openDialog(t)
	d.OfferSession("openai", "gpt-5")

	act := pressLogin(d, tui.KeyDown, tui.KeyDown, tui.KeyEnter)
	if !act.UseOffer {
		t.Fatalf("enter on the offer row did not take the offer: %+v", act)
	}
	if act.Provider != "openai" || act.Model != "gpt-5" {
		t.Errorf("offer = %q/%q, want openai/gpt-5", act.Provider, act.Model)
	}
	if act.StartLogin {
		t.Error("the offer row started a login; it is not one")
	}
	// It closes rather than advancing to the provider picker — there is no
	// provider to pick, the pair is already decided.
	if d.Active() {
		t.Error("dialog stayed open after taking the offer")
	}
}

// Without an offer the third row does not exist, and the cursor must not be
// able to walk onto it. Otherwise enter lands in the default branch of the
// method switch and returns UseOffer with an empty provider.
func TestNoOfferMeansNoThirdRow(t *testing.T) {
	d := openDialog(t)

	act := pressLogin(d, tui.KeyDown, tui.KeyDown, tui.KeyDown, tui.KeyEnter)
	if act.UseOffer {
		t.Fatalf("took an offer that was never made: %+v", act)
	}
	// Cursor pinned to the last real row (subscription) → a normal login.
	if d.method != "oauth" {
		t.Errorf("method = %q, want oauth — the cursor should stop on the last real row", d.method)
	}
}

// The offer names the pair in the row, so the user can see what they are
// agreeing to before pressing enter rather than after.
func TestOfferRowNamesTheProviderAndModel(t *testing.T) {
	d := openDialog(t)
	d.OfferSession("openai", "gpt-5")

	var found string
	for _, l := range d.Render(tui.Theme{}, 100) {
		if plain := widgets.StripANSIBytes(l); strings.Contains(plain, "continue on") {
			found = plain
		}
	}
	if found == "" {
		t.Fatal("no offer row rendered")
	}
	if !strings.Contains(found, "gpt-5") {
		t.Errorf("offer row does not name the model: %q", found)
	}
	if !strings.Contains(strings.ToLower(found), "session") {
		t.Errorf("offer row does not say the choice is session-scoped: %q", found)
	}
}

// Reopening clears the offer. A stale one would invite "continue on openai"
// after the user had just logged anthropic back in — the exact provider they
// fixed, offered away again.
func TestReopeningClearsTheOffer(t *testing.T) {
	d := openDialog(t)
	d.OfferSession("openai", "gpt-5")
	d.Open(auth.NewStore(filepath.Join(testsupport.TempDir(t), "auth.json")))

	for _, l := range d.Render(tui.Theme{}, 100) {
		if strings.Contains(widgets.StripANSIBytes(l), "continue on") {
			t.Fatal("offer survived a reopen")
		}
	}
	if act := pressLogin(d, tui.KeyDown, tui.KeyDown, tui.KeyEnter); act.UseOffer {
		t.Error("a cleared offer was still selectable")
	}
}
