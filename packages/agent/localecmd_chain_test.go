package agent

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/testsupport"
)

// localeInit scaffolds three things: the root UI catalog, the keyed catalogs
// (prompts, help, tools) and the web panel's catalogs. The last two run as
// chained calls at the end of the function — after an early return that fired
// whenever the ROOT catalog needed nothing.
//
// So once a translator's root file had settled, `terva locale init <lang>`
// printed "nothing to add" and did exactly that. A prompt, tool, help entry or
// panel string added after that point could not be picked up at all without
// deleting the locale file and starting over. The root catalog being complete
// says nothing about the other two.
func TestLocaleInitScaffoldsTheChainedCatalogsOnEveryRun(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	if err := localeInit([]string{"de"}); err != nil {
		t.Fatalf("first init: %v", err)
	}

	// Everything the chained calls produced, so the assertion covers whichever
	// catalogs are enrolled today rather than a list written here.
	chained := chainedCatalogPaths(t, home, "de")
	if len(chained) < 2 {
		t.Fatalf("only %d chained catalog(s) scaffolded; the fixture cannot see the defect", len(chained))
	}

	// The state a translator reaches: the root is complete, and a chained
	// catalog is missing entries. Deleting the files is the strongest form of
	// "missing" and needs no knowledge of which key was added.
	for _, p := range chained {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}

	if err := localeInit([]string{"de"}); err != nil {
		t.Fatalf("second init: %v", err)
	}
	for _, p := range chained {
		if _, err := os.Stat(p); err != nil {
			rel, _ := filepath.Rel(home, p)
			t.Errorf("%s was not re-scaffolded: the root catalog needed nothing, so init returned "+
				"before reaching the keyed and panel catalogs", rel)
		}
	}
}

// The complement: the root catalog must still be left alone when it is
// complete. Without this, "always rewrite everything" would pass the test
// above and would clobber a translator's file on every run.
func TestASettledRootCatalogIsNotRewritten(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	if err := localeInit([]string{"de"}); err != nil {
		t.Fatalf("first init: %v", err)
	}
	root := filepath.Join(home, "locales", "de.json")

	// A translation the operator has done by hand.
	before := loadRawLocale(root)
	if len(before) == 0 {
		t.Fatal("the root catalog scaffolded empty")
	}
	stat, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}

	if err := localeInit([]string{"de"}); err != nil {
		t.Fatalf("second init: %v", err)
	}

	after := loadRawLocale(root)
	if len(after) != len(before) {
		t.Errorf("the root catalog changed key count on a no-op run: %d -> %d", len(before), len(after))
	}
	if stat2, err := os.Stat(root); err == nil && !stat2.ModTime().Equal(stat.ModTime()) {
		t.Error("the root catalog was rewritten though it needed nothing — a translator's file " +
			"must not be touched on a run that adds no keys")
	}
}

// chainedCatalogPaths returns the per-language files the two chained scaffolds
// write, for whichever catalogs are enrolled.
func chainedCatalogPaths(t *testing.T, home, lang string) []string {
	t.Helper()
	var out []string
	for _, cat := range append(i18n.KeyedCatalogs(), i18n.UICatalogs()...) {
		p := filepath.Join(home, "locales", cat, lang+".json")
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}
