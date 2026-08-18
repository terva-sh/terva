package provider

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// writeModelsJSON writes a models.json with one model under one provider key.
func writeModelsJSON(t *testing.T, providerKey, id string, window int) string {
	t.Helper()
	path := filepath.Join(testsupport.TempDir(t), "models.json")
	body := UserModelsFile{Providers: map[string]UserProvider{
		providerKey: {Models: []UserModel{{ID: id, ContextWindow: window}}},
	}}
	b, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Every legacy spelling in the table, driven off the table itself.
//
// The load door normalized the key and the write door did not, so an override
// filed under a legacy name was LIVE — the picker and status bar honored it —
// while the settings form, looking under the canonical name, reported no
// override at all. Reset then removed the entry that was not there, reported
// success, broadcast a models refresh, and left the override in force.
//
// Ranging over LegacyUserModelProviderAliases rather than listing cases is the
// point: an alias added to that table enrolls here by being added.
func TestALegacyKeyedOverrideIsVisibleToTheEditor(t *testing.T) {
	if len(LegacyUserModelProviderAliases) == 0 {
		t.Fatal("the alias table is empty; this test would pass by having nothing to check")
	}
	for legacy, canonical := range LegacyUserModelProviderAliases {
		t.Run(legacy, func(t *testing.T) {
			const id = "legacy-keyed-model"
			path := writeModelsJSON(t, legacy, id, 500000)

			// The loader treats it as live under the canonical provider...
			overrides, warnings := LoadUserModelsWithWarnings(path)
			if len(overrides) != 1 || overrides[0].Model.Provider != canonical {
				t.Fatalf("loader produced %+v (warnings %v); expected one override under %q",
					overrides, warnings, canonical)
			}

			// ...so the editor, which only ever holds the canonical provider,
			// must see it too.
			um, ok, err := FindUserModel(path, canonical, id)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatalf("FindUserModel(%q) found nothing while a %q block holds a live override; "+
					"the settings form shows no override and its Reset clears nothing", canonical, legacy)
			}
			if um.ContextWindow != 500000 {
				t.Errorf("contextWindow = %d, want 500000", um.ContextWindow)
			}

			// And Reset must actually reset.
			removed, err := RemoveUserModel(path, canonical, id)
			if err != nil {
				t.Fatal(err)
			}
			if !removed {
				t.Fatalf("RemoveUserModel(%q) reported nothing to remove", canonical)
			}
			if left, _ := LoadUserModelsWithWarnings(path); len(left) != 0 {
				t.Fatalf("the override survived Reset: %+v", left)
			}
		})
	}
}

// Saving folds the legacy block into the canonical one instead of leaving two
// entries for one model. Two blocks are not merely untidy: the loader applies
// both, so any field set in each resolves by ordering rather than by intent.
func TestSavingFoldsALegacyBlockIntoTheCanonicalOne(t *testing.T) {
	for legacy, canonical := range LegacyUserModelProviderAliases {
		t.Run(legacy, func(t *testing.T) {
			const id = "folded-model"
			path := writeModelsJSON(t, legacy, id, 500000)

			if err := UpsertUserModel(path, canonical, UserModel{ID: id, ContextWindow: 250000}); err != nil {
				t.Fatal(err)
			}

			f, err := ReadUserModelsFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, still := f.Providers[legacy]; still {
				t.Errorf("the %q block survived the save; the model is now configured twice", legacy)
			}
			models := f.Providers[canonical].Models
			if len(models) != 1 || models[0].ID != id || models[0].ContextWindow != 250000 {
				t.Fatalf("the %q block holds %+v, want one entry at 250000", canonical, models)
			}
		})
	}
}

// A file holding BOTH spellings must resolve the same way every run. Map
// iteration decided it before, so a field set in both flipped between runs of
// the same binary against the same file. Canonical wins, because that is the
// only spelling the editor writes.
func TestBothSpellingsResolveDeterministicallyToTheEditorsBlock(t *testing.T) {
	const id = "contested-model"
	legacy, canonical := "kimi-code", "kimi"
	if NormalizeUserModelProviderKey(legacy) != canonical {
		t.Skipf("%q no longer normalizes to %q", legacy, canonical)
	}
	path := filepath.Join(testsupport.TempDir(t), "models.json")
	body := UserModelsFile{Providers: map[string]UserProvider{
		legacy:    {Models: []UserModel{{ID: id, ContextWindow: 500000}}},
		canonical: {Models: []UserModel{{ID: id, ContextWindow: 250000}}},
	}}
	b, _ := json.MarshalIndent(body, "", "  ")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	// Run it enough times that map-order roulette would have shown itself.
	for i := 0; i < 50; i++ {
		overrides, warnings := LoadUserModelsWithWarnings(path)
		if len(overrides) != 2 {
			t.Fatalf("run %d: got %d overrides, want both entries loaded", i, len(overrides))
		}
		// The LAST override for a key is the one applyUserOverrides leaves in
		// place, and it must be the canonical block's every time.
		last := overrides[len(overrides)-1].Model
		if last.ContextWindow != 250000 {
			t.Fatalf("run %d: the %q block won; the winner must be the one the editor writes",
				i, legacy)
		}
		if len(warnings) == 0 {
			t.Fatalf("run %d: a model configured under two provider keys produced no warning, so "+
				"the operator has no way to find out", i)
		}
	}
}

// Enrollment. Every door that takes a models.json provider key must resolve it.
//
// The normalizer already existed when this was written and had exactly one
// production caller — the loader. A fourth door added next to the other three
// would inherit the same silence, so the census enrolls by SHAPE: a function
// with a providerKey parameter must mention the resolution.
func TestEveryModelsJSONDoorResolvesItsProviderKey(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "usermodels_write.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("usermodels_write.go")
	if err != nil {
		t.Fatal(err)
	}

	doors := 0
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Type.Params == nil {
			continue
		}
		takesKey := false
		for _, p := range fd.Type.Params.List {
			for _, n := range p.Names {
				if n.Name == "providerKey" {
					takesKey = true
				}
			}
		}
		if !takesKey {
			continue
		}
		doors++
		body := string(src[fset.Position(fd.Body.Pos()).Offset:fset.Position(fd.Body.End()).Offset])
		if !strings.Contains(body, "userModelsProviderKeysFor") &&
			!strings.Contains(body, "NormalizeUserModelProviderKey") {
			t.Errorf("%s takes a providerKey and indexes models.json with it raw. The loader "+
				"normalizes legacy spellings, so a raw index reads and writes a DIFFERENT entry "+
				"than the one that is live.", fd.Name.Name)
		}
	}
	if doors < 3 {
		t.Fatalf("found %d functions taking a providerKey; expected at least the three editor "+
			"doors, so the scan is broken and this proves nothing", doors)
	}
}
