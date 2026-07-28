package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/persona"
	"terva.sh/terva/packages/testsupport"
)

// The fold exists for one load-bearing reason: persona shadowing is by file
// stem, and the built-in crew's stems are folded spellings (seppa.md for
// Seppä). A slug that drops the letter instead of folding it mints a stem no
// built-in uses, so a copy-to-edit silently never shadows — and the card
// doctor, which resolves its persona by the literal stem "seppa", never sees
// the edit.
//
// The string handling itself is pinned in slug/slug_test.go. What stays here is
// the end-to-end half: that the stems actually reach disk, shadow, migrate, and
// leave a distinct neighbour's file alone.

// A user copy of built-in Seppä must land on seppa.md and shadow the embedded
// crew member — persona.Resolve("seppa") is the exact lookup the card doctor
// performs (workspace_doctor.go's doctorPersona), so this pins that the edit
// actually takes effect there.
func TestWritePersonaFoldedSlugShadowsBuiltin(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	dest, err := persona.Write(persona.Persona{Name: "Seppä", Charter: "custom charter for the smith"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(dest, "seppa.md") {
		t.Fatalf("copy-to-edit landed on %q, want .../seppa.md — this stem is what shadows the built-in", dest)
	}
	got, err := persona.Resolve("seppa")
	if err != nil {
		t.Fatal(err)
	}
	if got.Charter != "custom charter for the smith" {
		t.Errorf("persona.Resolve(seppa) returned charter %q — the user copy does not shadow the built-in", got.Charter)
	}
	n := 0
	for _, p := range persona.All() {
		if p.Key() == got.Key() {
			n++
		}
	}
	if n != 1 {
		t.Errorf("roster holds %d personas for key %q, want 1 (shadow, not duplicate)", n, got.Key())
	}
}

// A persona saved before the fold sits under the old spelling. An edit must
// find it (UserPersonaPath), and a save must move it to the folded stem
// rather than leaving a duplicate that still fails to shadow.
func TestWritePersonaMigratesPreFoldFile(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	raw, err := persona.Marshal(persona.Persona{Name: "Seppä", Charter: "old edit"})
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(persona.Dir(), "sepp.md")
	if err := os.MkdirAll(persona.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	p, exists := persona.UserPath("Seppä")
	if !exists || p != legacy {
		t.Fatalf("persona.UserPath(Seppä) = (%q, %v), want the pre-fold file %q to be found", p, exists, legacy)
	}

	dest, err := persona.Write(persona.Persona{Name: "Seppä", Charter: "new edit"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(dest, "seppa.md") {
		t.Fatalf("migrated save landed on %q, want .../seppa.md", dest)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("pre-fold sepp.md should be removed by the migrating save (stat err %v)", err)
	}
	got, err := persona.Resolve("seppa")
	if err != nil {
		t.Fatal(err)
	}
	if got.Charter != "new edit" {
		t.Errorf("persona.Resolve(seppa) charter = %q, want the migrated edit", got.Charter)
	}
}

// "Sepp" legitimately owns sepp.md. A lookup or save of "Seppä" — whose
// legacy slug is the same stem — must neither claim nor remove it.
func TestWritePersonaLeavesDistinctLegacyNameAlone(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	raw, err := persona.Marshal(persona.Persona{Name: "Sepp", Charter: "a different persona entirely"})
	if err != nil {
		t.Fatal(err)
	}
	seppFile := filepath.Join(persona.Dir(), "sepp.md")
	if err := os.MkdirAll(persona.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seppFile, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	if p, exists := persona.UserPath("Seppä"); exists {
		t.Fatalf("persona.UserPath(Seppä) claimed %q, which belongs to Sepp", p)
	}
	if _, err := persona.Write(persona.Persona{Name: "Seppä", Charter: "the smith"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(seppFile); err != nil {
		t.Errorf("saving Seppä must not remove Sepp's file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(persona.Dir(), "seppa.md")); err != nil {
		t.Errorf("Seppä's own file missing: %v", err)
	}
}

func TestUserPersonaStoreFoldFallbackAndMigration(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := NewUserPersonaStore()

	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "zo.json"), []byte(`{"name":"Zoë","ref":"zo"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get("Zoë")
	if err != nil || got.Name != "Zoë" {
		t.Fatalf("Get(Zoë) = (%+v, %v), want the pre-fold record", got, err)
	}

	if _, err := s.Save(UserPersona{Name: "Zoë", Description: "updated"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "zoe.json")); err != nil {
		t.Errorf("save should land on the folded slug: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "zo.json")); !os.IsNotExist(err) {
		t.Errorf("pre-fold zo.json should be removed by the migrating save (stat err %v)", err)
	}

	// A distinct "Zo" owning zo.json is out of reach for Zoë's fallbacks.
	if err := os.WriteFile(filepath.Join(s.dir, "zo.json"), []byte(`{"name":"Zo","ref":"zo"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("Zoë"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "zo.json")); err != nil {
		t.Errorf("deleting Zoë must not remove Zo's file: %v", err)
	}
}

// Import filed "Zoë" under zo-<hash> before the fold. The content hash half of
// the id is unchanged, so a re-import must update that entry in place, not
// mint zoe-<hash> beside it — the idempotency ImportBytes promises.
func TestCardImportReusesPreFoldID(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	src := []byte(`{"spec":"chara_card_v2","spec_version":"2.0","data":{"name":"Zoë","first_mes":"salut"}}`)
	first, err := st.ImportBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.ID, "zoe-") {
		t.Fatalf("fresh import id = %q, want a zoe- prefix", first.ID)
	}
	// Refile the entry the way a pre-fold import would have named it.
	legacyID := "zo-" + strings.TrimPrefix(first.ID, "zoe-")
	if err := os.Rename(filepath.Join(st.dir, first.ID), filepath.Join(st.dir, legacyID)); err != nil {
		t.Fatal(err)
	}

	again, err := st.ImportBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != legacyID {
		t.Errorf("re-import minted %q, want it to reuse the pre-fold entry %q", again.ID, legacyID)
	}
	entries, err := os.ReadDir(st.dir)
	if err != nil {
		t.Fatal(err)
	}
	dirs := 0
	for _, e := range entries {
		if e.IsDir() {
			dirs++
		}
	}
	if dirs != 1 {
		t.Errorf("library holds %d entries after re-import, want 1", dirs)
	}
}
