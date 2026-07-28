package persona

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

func TestNamespaceAndRef(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("TERVA_PERSONA_NAME", "")
	byRef := map[string]Persona{}
	for _, p := range listEmbedded() {
		byRef[p.Ref()] = p
	}
	if m, ok := byRef["mieli"]; !ok || m.Namespace != "" {
		t.Fatalf("mieli should be top-level (ns \"\"): ok=%v %+v", ok, m)
	}
	v, ok := byRef["review-crew:vartija"]
	if !ok {
		t.Fatal("review-crew:vartija missing from embedded refs")
	}
	if v.Namespace != "review-crew" || v.Qualified() != "review-crew:Vartija" || v.Origin() != "built-in" {
		t.Fatalf("vartija ns/qualified/origin wrong: ns=%q qual=%q origin=%q", v.Namespace, v.Qualified(), v.Origin())
	}
	if !v.matches("vartija") || !v.matches("Vartija") || !v.matches("review-crew:vartija") {
		t.Error("vartija should match bare stem/name and the qualified form")
	}
	if v.matches("websearch:vartija") {
		t.Error("vartija must not match a wrong namespace")
	}
}

// writeExtPersona drops a fake extension bundle shipping one Persona.
func writeExtPersona(t *testing.T, home, extDir, manifestName string, enabled bool, file, body string) {
	t.Helper()
	dir := filepath.Join(home, "extensions", extDir)
	if err := os.MkdirAll(filepath.Join(dir, "personas"), 0o755); err != nil {
		t.Fatal(err)
	}
	en := "true"
	if !enabled {
		en = "false"
	}
	name := manifestName
	if name == "" {
		name = extDir
	}
	manifest := `{"name":"` + name + `","exec":"x","enabled":` + en + `}`
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "personas", file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionPersonaDiscovery(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("TERVA_PERSONA_NAME", "")
	writeExtPersona(t, home, "websearch", "websearch", true, "deep-researcher.md",
		"---\nname: Deep Researcher\nspecialty: web research\ngood_for: [web-research]\n---\nResearch the web rigorously; cite and verify.")

	p, err := LoadByName("websearch:deep-researcher")
	if err != nil {
		t.Fatal(err)
	}
	if p.Namespace != "websearch" || !p.FromExtension() || p.Origin() != "ext:websearch" || p.Ref() != "websearch:deep-researcher" {
		t.Fatalf("ext persona provenance wrong: ns=%q source=%q origin=%q ref=%q", p.Namespace, p.Source, p.Origin(), p.Ref())
	}
	if _, err := LoadByName("deep-researcher"); err != nil {
		t.Errorf("bare deep-researcher should resolve to the extension's: %v", err)
	}
	// good_for survives the frontmatter round trip. What that makes the persona
	// eligible FOR is build's question — TestExtensionPersonaIsDispatchable.
	if len(p.GoodFor) != 1 || p.GoodFor[0] != "web-research" {
		t.Errorf("good_for = %v, want [web-research]", p.GoodFor)
	}
}

func TestExtensionPersonaDisabled(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("TERVA_PERSONA_NAME", "")
	// manifest enabled:false → not loaded (follows skills).
	writeExtPersona(t, home, "off-ext", "off-ext", false, "x.md", "---\nname: X\n---\nbody")
	if _, err := LoadByName("off-ext:x"); err == nil {
		t.Error("manifest-disabled extension persona must not load")
	}
	// config disable_extensions → not loaded (closing the skill-path gap).
	writeExtPersona(t, home, "cfg-off", "cfg-off", true, "y.md", "---\nname: Y\n---\nbody")
	if err := config.SaveConfig(config.Config{DisableExtensions: []string{"cfg-off"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadByName("cfg-off:y"); err == nil {
		t.Error("config-disabled extension persona must not load")
	}
}

func TestUserOverridesExtensionPersona(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("TERVA_PERSONA_NAME", "")
	writeExtPersona(t, home, "websearch", "websearch", true, "deep-researcher.md",
		"---\nname: Deep Researcher\nsummary: EXT\ngood_for: [web-research]\n---\next charter")
	// User mirrors the namespace as a subdir to override.
	udir := filepath.Join(home, "personas", "websearch")
	if err := os.MkdirAll(udir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(udir, "deep-researcher.md"),
		[]byte("---\nname: Deep Researcher\nsummary: MINE\ngood_for: [web-research]\n---\nmy charter"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadByName("websearch:deep-researcher")
	if err != nil {
		t.Fatal(err)
	}
	if p.FromExtension() || p.Summary != "MINE" {
		t.Fatalf("user persona should override the extension's: from-ext=%v summary=%q", p.FromExtension(), p.Summary)
	}
	count := 0
	for _, ap := range All() {
		if ap.Ref() == "websearch:deep-researcher" {
			count++
			if ap.Summary != "MINE" {
				t.Error("merged roster kept the wrong tier")
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one websearch:deep-researcher in the merged roster, got %d", count)
	}
}
