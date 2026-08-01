package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// THE regression this whole field has to survive. models.json `name` moved
// into the ScalarParams registry, and the merge that carries it used to be a
// hand-written special case guarded by `um.DisplayName != um.ID` — a proxy for
// "did the operator actually write a name", forced by the loader backfilling
// DisplayName = ID when they didn't.
//
// Get that guard wrong and every model with an entry for ANY OTHER reason (a
// context window, a price) loses its catalog display name and starts rendering
// its raw id. That is a silent, repo-wide cosmetic regression, so it is
// asserted on the merge directly rather than through a picker.
func TestOverrideWithoutNameKeepsCatalogDisplayName(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "models.json")
	body := `{"providers":{"anthropic":{"models":[{"id":"claude-x","contextWindow":123000}]}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	overrides, warnings := LoadUserModelsWithWarnings(path)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	base := []Model{{Provider: "anthropic", ID: "claude-x", DisplayName: "Claude X (latest)"}}
	merged := applyUserOverrides(base, overrides)

	if merged[0].DisplayName != "Claude X (latest)" {
		t.Errorf("a name-less override clobbered the catalog display name: %q", merged[0].DisplayName)
	}
	if merged[0].DisplayNameSet {
		t.Error("DisplayNameSet must stay false when the operator wrote no name")
	}
	if merged[0].Label() != "claude-x" {
		t.Errorf("Label should stay the id for a catalog name, got %q", merged[0].Label())
	}
	if merged[0].ContextWindow != 123000 {
		t.Errorf("the override the operator DID write was dropped: %d", merged[0].ContextWindow)
	}
}

// A name the operator did write wins over the catalog's, and marks itself as
// theirs so the status bar and picker know they may substitute it for the id.
func TestNameOverrideWinsAndMarksItself(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "models.json")
	body := `{"providers":{"ollama":{"models":[{"id":"hf.co/unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q4_K_XL","name":"Qwen Coder"}]}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	overrides, _ := LoadUserModelsWithWarnings(path)

	base := []Model{{Provider: "ollama", ID: "hf.co/unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q4_K_XL", DisplayName: "hf.co/unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q4_K_XL", Source: "live"}}
	merged := applyUserOverrides(base, overrides)

	if merged[0].DisplayName != "Qwen Coder" || !merged[0].DisplayNameSet {
		t.Fatalf("name override not applied: %q set=%v", merged[0].DisplayName, merged[0].DisplayNameSet)
	}
	if merged[0].Label() != "Qwen Coder" {
		t.Errorf("Label should be the operator's name, got %q", merged[0].Label())
	}
	if merged[0].ID != "hf.co/unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q4_K_XL" {
		t.Error("a rename must not touch the id — it is the identity on the wire")
	}
}

// A name that happens to EQUAL the id used to be silently ignored, because the
// old merge guard could not tell it from the loader's own backfill. The flag
// can, so the operator gets what they asked for.
func TestNameEqualToIDIsHonored(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "models.json")
	body := `{"providers":{"anthropic":{"models":[{"id":"claude-x","name":"claude-x"}]}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	overrides, _ := LoadUserModelsWithWarnings(path)

	base := []Model{{Provider: "anthropic", ID: "claude-x", DisplayName: "Claude X (latest)"}}
	merged := applyUserOverrides(base, overrides)

	if merged[0].DisplayName != "claude-x" || !merged[0].DisplayNameSet {
		t.Errorf("an explicit name equal to the id should win, got %q set=%v", merged[0].DisplayName, merged[0].DisplayNameSet)
	}
}

// A model the catalog has never heard of (hand-listed local) appends whole,
// carrying the flag with it — the append path skips the merge functions, so
// the flag has to be set by the loader, not by Merge.
func TestNewModelFromFileCarriesTheFlag(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "models.json")
	body := `{"providers":{"openai-compatible":{"models":[
		{"id":"some-long-local-id","name":"Local Big"},
		{"id":"another-local-id"}
	]}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	overrides, _ := LoadUserModelsWithWarnings(path)
	merged := applyUserOverrides(nil, overrides)

	byID := map[string]Model{}
	for _, m := range merged {
		byID[m.ID] = m
	}
	if got := byID["some-long-local-id"]; !got.DisplayNameSet || got.Label() != "Local Big" {
		t.Errorf("named new model: set=%v label=%q", got.DisplayNameSet, got.Label())
	}
	// The loader backfills DisplayName = ID for a name-less entry so the model
	// still renders; that backfill must NOT read as a rename.
	if got := byID["another-local-id"]; got.DisplayNameSet {
		t.Error("the loader's DisplayName=ID backfill must not count as a rename")
	}
}

// A name is the one override rendered raw into a terminal status bar, so an
// ESC in it repaints the frame rather than just looking wrong. Both doors
// sanitize; this is the hand-edited one.
func TestLoaderSanitizesName(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "models.json")
	body := `{"providers":{"ollama":{"models":[{"id":"m","name":"\u001b[31mred\u001b[0m\nsecond line"}]}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	overrides, warnings := LoadUserModelsWithWarnings(path)
	if len(overrides) != 1 {
		t.Fatalf("want 1 override, got %d", len(overrides))
	}
	got := overrides[0].Model.DisplayName
	if got != "red second line" {
		t.Errorf("escape sequence / newline survived sanitizing: %q", got)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "adjusted") {
			found = true
		}
	}
	if !found {
		t.Errorf("a silently-rewritten name needs a warning, got %v", warnings)
	}
}

func TestSanitizeDisplayName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Qwen Coder", "Qwen Coder"},
		{"  padded  ", "padded"},
		{"a\tb", "a b"},
		{"a\r\nb", "a b"},
		{"\x1b[1;31mboom\x1b[0m", "boom"},
		{"bel\x07end", "bel end"},            // a BARE BEL is just a control, not an escape
		{"nul\x00del\x7f", "nul del"},        // C0 and DEL become spaces, then collapse
		{"\x9bcsi", "csi"},                   // raw C1 byte: invalid UTF-8, arrives as RuneError
		{"csi", "csi"},                      // the same C1 properly encoded
		{"\x1b]0;title\x07after", "after"},   // OSC runs to BEL
		{"\x1b]0;title\x1b\\after", "after"}, // …or to ST
		{"trailing\x1b[", "trailing"},        // an unterminated escape eats the rest, by design
		{strings.Repeat("x", 200), strings.Repeat("x", MaxDisplayNameRunes)},
		{"", ""},
	}
	for _, c := range cases {
		if got := SanitizeDisplayName(c.in); got != c.want {
			t.Errorf("SanitizeDisplayName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The editor writes through the same sanitizer as the loader, so a pasted
// escape cannot reach models.json from the /model form either.
func TestNameScalarParamSanitizesOnSet(t *testing.T) {
	var p ScalarParam
	for _, sp := range ScalarParams() {
		if sp.Key == "name" {
			p = sp
		}
	}
	if p.Key == "" {
		t.Fatal("no name entry in the scalar registry")
	}
	var um UserModel
	if err := p.SetOverride(&um, "\x1b[31mQwen\x1b[0m Coder"); err != nil {
		t.Fatal(err)
	}
	if um.Name != "Qwen Coder" {
		t.Errorf("editor value not sanitized: %q", um.Name)
	}
	if got := p.Override(um); got != "Qwen Coder" {
		t.Errorf("Override should read back the stored name, got %q", got)
	}
	// Blank clears, like every other scalar.
	if err := p.SetOverride(&um, ""); err != nil || um.Name != "" {
		t.Errorf("blank should clear the name: %q err=%v", um.Name, err)
	}
}

// The editor's "inherit (…)" hint must not offer the operator their OWN name
// as the thing clearing the field would fall back to.
func TestNameScalarDefaultHint(t *testing.T) {
	var p ScalarParam
	for _, sp := range ScalarParams() {
		if sp.Key == "name" {
			p = sp
		}
	}
	renamed := Model{ID: "long-id", DisplayName: "Short", DisplayNameSet: true}
	if got := p.Default(renamed); got != "long-id" {
		t.Errorf("renamed model should hint the id, got %q", got)
	}
	catalog := Model{ID: "claude-x", DisplayName: "Claude X (latest)"}
	if got := p.Default(catalog); got != "Claude X (latest)" {
		t.Errorf("catalog model should hint its display name, got %q", got)
	}
	bare := Model{ID: "bare-id"}
	if got := p.Default(bare); got != "bare-id" {
		t.Errorf("nameless model should hint the id, got %q", got)
	}
}
