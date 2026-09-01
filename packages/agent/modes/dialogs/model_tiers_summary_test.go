package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

func summaryRung(name, source string) ctrlproto.ModelTierRung {
	// Model is set on every rung a source claims, because that is the pairing
	// the wire guarantees: swarmTierRow only names a source when something
	// resolved. A test that left it empty would be asserting on a shape the
	// daemon never sends.
	m := "some-model"
	if source == "" {
		m = ""
	}
	return ctrlproto.ModelTierRung{Rung: name, Source: source, Model: m}
}

func summaryView(prov string, sources ...string) ctrlproto.ModelTiersView {
	names := []string{"weak", "medium", "strong", "cheap"}
	v := ctrlproto.ModelTiersView{Provider: prov}
	for i, src := range sources {
		v.Rungs = append(v.Rungs, summaryRung(names[i], src))
	}
	return v
}

// providerListDialog builds a stage-1 list directly. Open() reads the live
// catalog, which would make these assertions depend on whichever providers the
// developer running them happens to be logged into.
func providerListDialog(rows ...providerRow) *ModelDialog {
	d := NewModelDialog()
	d.active = true
	d.stage = stageProvider
	d.providers = rows
	// Render re-reads the catalog when the revision moved, which would rebuild
	// the rows just installed from a catalog these tests deliberately do not
	// populate. Pinning the revision is what keeps the fixture in place.
	d.catalogRev = provider.CatalogRevision()
	return d
}

func plainLines(d *ModelDialog, width int) []string {
	var out []string
	for _, l := range d.Render(tui.Dark, width) {
		out = append(out, stripANSI(l))
	}
	return out
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++ // the 'm' itself
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// The route into the ladder was unreachable by discovery: ctrl+t is bound on
// the provider list and nothing on that screen said so.
func TestProviderListNamesCtrlTAsTheRouteToTiers(t *testing.T) {
	d := providerListDialog(providerRow{name: "google", label: "google", count: 3})
	got := strings.Join(plainLines(d, 100), "\n")
	if !strings.Contains(got, "ctrl+t") {
		t.Fatalf("provider list hint never names ctrl+t:\n%s", got)
	}
}

// The three states have to be told apart on sight — "filled from config",
// "filled by a built-in rule" and "nothing resolves" are three different
// answers, and the third is the one that costs a user a silently-inherited
// sub-agent model.
func TestProviderGlyphsSeparateOverrideBuiltinAndEmpty(t *testing.T) {
	d := providerListDialog(
		providerRow{name: "anthropic", label: "anthropic", count: 3},
		providerRow{name: "google", label: "google", count: 3},
		providerRow{name: "openrouter", label: "openrouter", count: 3},
	)
	d.SetTierSummaries(map[string]ctrlproto.ModelTiersView{
		"anthropic":  summaryView("anthropic", "override", "override", "override", "built-in"),
		"google":     summaryView("google", "built-in", "built-in", "built-in", "built-in"),
		"openrouter": summaryView("openrouter", "", "", "", ""),
	})
	rows := map[string]string{}
	for _, l := range plainLines(d, 100) {
		for _, p := range []string{"anthropic", "google", "openrouter"} {
			if strings.HasPrefix(strings.TrimSpace(l), p) {
				rows[p] = l
			}
		}
	}
	for prov, want := range map[string]string{
		"anthropic":  "●●●○",
		"google":     "○○○○",
		"openrouter": "----",
	} {
		if !strings.Contains(rows[prov], want) {
			t.Errorf("%s row missing %q:\n%s", prov, want, rows[prov])
		}
	}
	if rows["anthropic"] == "" || rows["google"] == "" {
		t.Fatal("rows not found")
	}
}

// A column that moves with the row above it cannot be read down, and the
// hidden-model note is the thing on this list whose width varies.
func TestProviderGlyphColumnHoldsItsPlaceAcrossAHiddenCount(t *testing.T) {
	d := providerListDialog(
		providerRow{name: "anthropic", label: "anthropic", count: 3},
		providerRow{name: "google", label: "google", count: 12, hidden: 3},
		providerRow{name: "openai", label: "openai", count: 5},
	)
	d.SetTierSummaries(map[string]ctrlproto.ModelTiersView{
		"anthropic": summaryView("anthropic", "override", "override", "override", "built-in"),
		"google":    summaryView("google", "built-in", "built-in", "built-in", "built-in"),
		"openai":    summaryView("openai", "built-in", "", "", ""),
	})
	col := -1
	for _, l := range plainLines(d, 100) {
		// Provider rows only. The legend line carries the same glyphs, and it
		// is not in the column.
		if !strings.HasPrefix(l, "  anthropic") && !strings.HasPrefix(l, "  google") && !strings.HasPrefix(l, "  openai") {
			continue
		}
		i := strings.IndexAny(l, "●○-")
		if i < 0 {
			continue
		}
		at := len([]rune(stripANSI(l)[:i])) // rune offset, glyphs are multi-byte
		if col == -1 {
			col = at
			continue
		}
		if at != col {
			t.Fatalf("glyph column moved: %d vs %d in %q", at, col, l)
		}
	}
	if col == -1 {
		t.Fatal("no glyphs rendered at all")
	}
}

// The glyphs are meaningless without knowing which rung each stands for, and
// the legend has to read the names off the ladder it is describing: a ladder
// that grows a rung must not leave a legend still naming the old three.
func TestTierLegendNamesTheRungsInLadderOrder(t *testing.T) {
	d := providerListDialog(providerRow{name: "anthropic", label: "anthropic", count: 3})
	d.SetTierSummaries(map[string]ctrlproto.ModelTiersView{
		"anthropic": summaryView("anthropic", "override", "override", "override", "built-in"),
	})
	got := strings.Join(plainLines(d, 100), "\n")
	if !strings.Contains(got, "weak/medium/strong/cheap") {
		t.Fatalf("legend does not name the rungs in order:\n%s", got)
	}
}

// The column is an aid. A session whose host cannot read ladders — no
// ModelTiers callback, or every fetch failing — still has to open the picker,
// and a half-drawn column would read as "these providers have no ladder".
func TestProviderListWithoutSummariesDrawsNoColumn(t *testing.T) {
	d := providerListDialog(providerRow{name: "google", label: "google", count: 3})
	lines := plainLines(d, 100)
	got := strings.Join(lines, "\n")

	// The row must end at the count. Checking only for ●/○ would miss the
	// failure that actually matters: rendering "not loaded" as a row of empty
	// glyphs, which tells the user every ladder is unfilled when the truth is
	// that nothing was read. An assertion on the whole row catches any cell
	// appended after the count, whatever it is drawn with.
	var row string
	for _, l := range lines {
		if strings.HasPrefix(l, "  google") {
			row = l
		}
	}
	if row == "" {
		t.Fatal("google row not rendered")
	}
	if trimmed := strings.TrimRight(row, " "); !strings.HasSuffix(trimmed, "3") {
		t.Fatalf("something is drawn after the count with no summary loaded: %q", trimmed)
	}
	if strings.Contains(got, "built-in") {
		t.Fatalf("legend drawn with no summary loaded:\n%s", got)
	}
}

// Tiers belong to a provider; the ★ row spans every provider, so there is no
// ladder it could be describing.
func TestFavoritesRowCarriesNoGlyphs(t *testing.T) {
	d := providerListDialog(
		providerRow{label: "★ favorites", count: 2, fav: true},
		providerRow{name: "google", label: "google", count: 3},
	)
	// The blank-keyed entry is the point. A ★ row carries no provider name, so
	// the map lookup misses and the row comes out bare whether or not anything
	// checks fav — which would make this guard unfallible. Seeding a view under
	// "" is what a host returning a ladder for a blank provider would do, and
	// it is the only fixture in which the fav check is the thing being tested.
	d.SetTierSummaries(map[string]ctrlproto.ModelTiersView{
		"":       summaryView("", "override", "override", "override", "override"),
		"google": summaryView("google", "built-in", "built-in", "built-in", "built-in"),
	})
	for _, l := range plainLines(d, 100) {
		if strings.Contains(l, "★") && strings.ContainsAny(l, "●○") {
			t.Fatalf("favorites row carries tier glyphs: %q", l)
		}
	}
}

// Backing out of the ladder is the ordinary way to see the column, so a rung
// just edited has to be current by the time the list is on screen again.
func TestARungEditUpdatesTheGlyphColumn(t *testing.T) {
	d := providerListDialog(providerRow{name: "google", label: "google", count: 3})
	d.SetTierSummaries(map[string]ctrlproto.ModelTiersView{
		"google": summaryView("google", "built-in", "built-in", "built-in", "built-in"),
	})
	d.UpdateTierSummary("google", summaryView("google", "override", "built-in", "built-in", "built-in"))
	got := strings.Join(plainLines(d, 100), "\n")
	if !strings.Contains(got, "●○○○") {
		t.Fatalf("edited rung not reflected in the column:\n%s", got)
	}
}

// Seeding the column from a single edit would draw glyphs for one provider and
// blanks for every other, which reads as "the others have no ladder".
func TestARungEditDoesNotSeedAnUnloadedColumn(t *testing.T) {
	d := providerListDialog(providerRow{name: "google", label: "google", count: 3})
	d.UpdateTierSummary("google", summaryView("google", "override", "built-in", "built-in", "built-in"))
	got := strings.Join(plainLines(d, 100), "\n")
	if strings.ContainsAny(got, "●○") {
		t.Fatalf("column seeded from one edit:\n%s", got)
	}
}
