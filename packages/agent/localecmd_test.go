package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/i18n"
)

func TestLocaleInitScaffoldsAndValidates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERVA_HOME", home)

	// A language with no embedded catalog scaffolds every source string blank.
	if err := localeInit([]string{"de"}); err != nil {
		t.Fatalf("localeInit(de): %v", err)
	}
	path := filepath.Join(home, "locales", "de.json")
	raw := loadRawLocale(path)
	if len(raw) != len(i18n.ReferenceKeys()) {
		t.Fatalf("scaffold has %d keys, want %d (all reference keys)", len(raw), len(i18n.ReferenceKeys()))
	}
	for k, v := range raw {
		// Blank means unfilled: an empty string for a singular, or an
		// all-empty-forms object for a plural (e.g. the swarm count).
		if isFilled(v) {
			t.Fatalf("key %q should scaffold blank, got %s", k, v)
		}
	}
	// A blank file is valid (blank values are skipped, keys are all known).
	if err := localeValidate([]string{path}); err != nil {
		t.Fatalf("validate blank scaffold: %v", err)
	}

	// init is idempotent: a second run adds nothing.
	if err := localeInit([]string{"de"}); err != nil {
		t.Fatalf("localeInit(de) second: %v", err)
	}
	if got := loadRawLocale(path); len(got) != len(raw) {
		t.Fatalf("second init changed key count: %d -> %d", len(raw), len(got))
	}
}

// `init en` is now the customize-in-place case: it scaffolds an English
// overlay seeded with the current English (UI + prompts) so an operator can
// reword strings or override prompts without switching language. Other "en*"
// tags don't map to a loaded overlay and are steered to `en`.
func TestLocaleInitEnglishScaffoldsOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERVA_HOME", home)
	if err := localeInit([]string{"en"}); err != nil {
		t.Fatalf("localeInit(en): %v", err)
	}
	ui := loadRawLocale(filepath.Join(home, "locales", "en.json"))
	if len(ui) != len(i18n.ReferenceKeys()) {
		t.Fatalf("en overlay has %d keys, want %d", len(ui), len(i18n.ReferenceKeys()))
	}
	// Seeded with the current English (editable in place), not blank.
	if v, ok := ui["interactive tui"]; !ok || string(v) != `"interactive tui"` {
		t.Errorf("en UI should seed the English source, got %s", v)
	}
	prompts := loadRawLocale(filepath.Join(home, "locales", "prompts", "en.json"))
	if len(prompts) == 0 {
		t.Error("en prompt overlay should be scaffolded")
	}
	// A non-en "en*" tag is steered to `en`, not silently accepted.
	if err := localeInit([]string{"en-US"}); err == nil {
		t.Error("localeInit(en-US) should steer to `en`")
	}
}

func TestLocaleValidateCatchesArgMismatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERVA_HOME", home)
	dir := filepath.Join(home, "locales")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A key that carries %d translated to a value that drops it.
	bad := `{"saved %d files": "tiedostot tallennettu"}`
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := localeValidate([]string{path}); err == nil {
		t.Error("validate should fail when a translation drops a format arg")
	}
}

func TestLocaleMergeRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERVA_HOME", home)
	dir := filepath.Join(home, "locales")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	todo := `{"a string": "käännös", "blank one": ""}`
	if err := os.WriteFile(filepath.Join(dir, "fi.todo.json"), []byte(todo), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := localeMerge([]string{"fi"}); err != nil {
		t.Fatalf("localeMerge: %v", err)
	}
	dest := loadRawLocale(filepath.Join(dir, "fi.json"))
	if got := dest["a string"]; string(got) != `"käännös"` {
		t.Errorf("filled entry not merged: %s", got)
	}
	if _, ok := dest["blank one"]; ok {
		t.Error("blank entry should not merge into the locale file")
	}
	remaining := loadRawLocale(filepath.Join(dir, "fi.todo.json"))
	if _, ok := remaining["blank one"]; !ok {
		t.Error("blank entry should remain pending in the todo file")
	}
	if _, ok := remaining["a string"]; ok {
		t.Error("merged entry should be removed from the todo file")
	}
}

func TestCoverageCounts(t *testing.T) {
	ref := i18n.Doc{
		Singular: map[string]string{"a": "a", "b": "b"},
		Plural:   map[string]map[string]string{"x|y": {"one": "x", "other": "y"}},
	}
	merged := i18n.Doc{
		Singular: map[string]string{"a": "AA", "b": ""},
		Plural:   map[string]map[string]string{"x|y": {"other": "Y"}},
	}
	done, total := coverage(merged, ref)
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if done != 2 { // "a" translated, "b" blank, plural "other" filled
		t.Fatalf("done = %d, want 2", done)
	}
}

func TestIsFilled(t *testing.T) {
	cases := map[string]bool{
		`""`:                     false,
		`"x"`:                    true,
		`{"one":"","other":""}`:  false,
		`{"one":"a","other":""}`: true,
	}
	for in, want := range cases {
		if got := isFilled(json.RawMessage(in)); got != want {
			t.Errorf("isFilled(%s) = %v, want %v", in, got, want)
		}
	}
}

func TestSameVerbs(t *testing.T) {
	if !sameVerbs([]string{"%d", "%s"}, []string{"%s", "%d"}) {
		t.Error("same multiset, different order should match")
	}
	if sameVerbs([]string{"%d"}, []string{"%d", "%d"}) {
		t.Error("differing counts should not match")
	}
	if sameVerbs([]string{"%d"}, nil) {
		t.Error("missing verb should not match")
	}
}

func TestNearestFindsRewordedKey(t *testing.T) {
	refs := []string{
		"start a fresh session",
		"resume a previous session for this directory",
		"clear the chat transcript",
	}
	// A slightly reworded orphan should point back at its source.
	got := nearest("start a brand new session", refs)
	if got != "start a fresh session" {
		t.Errorf("nearest = %q, want the fresh-session key", got)
	}
	// Something unrelated should not match.
	if got := nearest("completely unrelated phrase xyzzy", refs); got != "" {
		t.Errorf("nearest for unrelated = %q, want empty", got)
	}
}
