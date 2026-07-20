package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

func freshHome(t *testing.T) string {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("TERVA_PERSONA_NAME", "")
	return home
}

// The embedded crew is the 14 personas (Mieli + 7 review specialists +
// Kertoja the play director + Seppä the card doctor + Toimittaja the
// character editor + the 3 raati panelists); the team READMEs are not
// personas.
func TestEmbeddedCrew(t *testing.T) {
	freshHome(t)
	got := listEmbeddedPersonas()
	if len(got) != 14 {
		names := make([]string, len(got))
		for i, p := range got {
			names[i] = p.Name
		}
		t.Fatalf("embedded crew: got %d personas %v, want 14", len(got), names)
	}
	by := map[string]Persona{}
	for _, p := range got {
		by[p.Name] = p
		if p.Charter == "" {
			t.Errorf("%s: empty charter", p.Name)
		}
		if !p.Builtin() {
			t.Errorf("%s: Builtin()=false, want true (source %q)", p.Name, p.Source)
		}
	}
	if v, ok := by["Vartija"]; !ok {
		t.Error("Vartija missing from embedded crew")
	} else if v.Specialty == "" || v.Pronunciation == "" {
		t.Errorf("Vartija missing metadata: %+v", v)
	}
	if k, ok := by["Kertoja"]; !ok {
		t.Error("Kertoja (the play director) missing from embedded crew")
	} else if !k.Immersive {
		t.Error("Kertoja should be immersive")
	}
	if y, ok := by["YATA-1"]; !ok {
		t.Error("YATA-1 (the raati truth panelist) missing from embedded crew")
	} else if y.Namespace != "raati-crew" {
		t.Errorf("YATA-1 namespace = %q, want raati-crew", y.Namespace)
	}
	if s, ok := by["Seppä"]; !ok {
		t.Error("Seppä (the card doctor) missing from embedded crew")
	} else if s.Immersive {
		t.Error("Seppä should not be immersive (it is an additive card-craft persona)")
	}
}

func TestParsePersona_RequiresName(t *testing.T) {
	if _, err := ParsePersona("---\nsummary: x\n---\nbody", "t.md"); err == nil {
		t.Fatal("expected error for missing name")
	}
	p, err := ParsePersona("---\nname: Foo\ngood_for: [a, b]\n---\nThe charter.", "t.md")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Foo" || p.Charter != "The charter." {
		t.Fatalf("parsed wrong: %+v", p)
	}
	if len(p.GoodFor) != 2 || p.GoodFor[0] != "a" {
		t.Fatalf("flow-sequence list not parsed: %v", p.GoodFor)
	}
}

// Default resolution lands on the embedded Mieli, charter included.
func TestResolvePersona_DefaultIsMieli(t *testing.T) {
	freshHome(t)
	p, err := ResolvePersona("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Mieli" || !p.Builtin() || p.Charter == "" {
		t.Fatalf("default persona: %+v", p)
	}
}

// A bare name resolves against the embedded crew.
func TestResolvePersona_ByName(t *testing.T) {
	freshHome(t)
	for _, name := range []string{"vartija", "Vartija", "koestaja"} {
		p, err := ResolvePersona(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.EqualFold(p.Name, name) {
			t.Fatalf("%s: got %q", name, p.Name)
		}
		if strings.Contains(p.Charter, "{{") || strings.Contains(p.Charter, "<START>") {
			t.Errorf("%s charter has leftover macros", name)
		}
	}
	if _, err := ResolvePersona("nope-not-real"); err == nil {
		t.Error("expected error for unknown persona name")
	}
}

func TestResolvePersona_ByPath(t *testing.T) {
	home := freshHome(t)
	path := filepath.Join(home, "custom.md")
	if err := os.WriteFile(path, []byte("---\nname: Custom\n---\nBespoke charter."), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := ResolvePersona(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Custom" || p.Charter != "Bespoke charter." || p.Builtin() {
		t.Fatalf("path persona: %+v", p)
	}
}

// $TERVA_HOME/Persona.md overrides the default; and when default_persona is
// also set the file wins.
func TestResolvePersona_RootFileWinsOverConfig(t *testing.T) {
	home := freshHome(t)
	if err := os.WriteFile(filepath.Join(home, "persona.md"), []byte("---\nname: Root\n---\nRoot charter."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveConfig(config.Config{DefaultPersona: "koestaja"}); err != nil {
		t.Fatal(err)
	}
	p, err := ResolvePersona("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Root" {
		t.Fatalf("persona.md should win over default_persona: got %q", p.Name)
	}
}

func TestResolvePersona_DefaultPersonaPointer(t *testing.T) {
	freshHome(t)
	if err := config.SaveConfig(config.Config{DefaultPersona: "luotsi"}); err != nil {
		t.Fatal(err)
	}
	p, err := ResolvePersona("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Luotsi" {
		t.Fatalf("default_persona pointer: got %q", p.Name)
	}
}

// On-disk personas shadow embedded ones of the same name.
func TestResolvePersona_OnDiskShadowsEmbedded(t *testing.T) {
	home := freshHome(t)
	dir := filepath.Join(home, "personas")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vartija.md"), []byte("---\nname: Vartija\nsummary: ONDISK\n---\nForked charter."), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := ResolvePersona("vartija")
	if err != nil {
		t.Fatal(err)
	}
	if p.Builtin() || p.Summary != "ONDISK" {
		t.Fatalf("on-disk fork should shadow embedded: %+v", p)
	}
}

// Legacy name-only override: persona_name with no Persona file swaps the name
// and carries no charter (back-compat).
func TestResolvePersona_LegacyNameOnly(t *testing.T) {
	freshHome(t)
	if err := config.SaveConfig(config.Config{PersonaName: "Aria"}); err != nil {
		t.Fatal(err)
	}
	p, err := ResolvePersona("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Aria" || p.Charter != "" {
		t.Fatalf("legacy name-only: %+v", p)
	}
}

// The charter lands between the identity intro and the harness conventions,
// and is dropped when a Custom prompt replaces the identity.
func TestBuildSystemPrompt_CharterPlacement(t *testing.T) {
	got := BuildSystemPrompt(SystemPromptOpts{PersonaName: "Vartija", Charter: "REVIEW-AS-SECURITY"})
	intro := strings.Index(got, "You are Vartija,")
	charter := strings.Index(got, "REVIEW-AS-SECURITY")
	// Anchor on the surface-independent half of the conventions: the opening
	// sentence now varies with where the output lands (see Surface), so keying
	// the ordering check on it would make this test a hostage to the audience.
	conv := strings.Index(got, "Act first, then summarise")
	if intro < 0 || charter < 0 || conv < 0 {
		t.Fatalf("missing a section: intro=%d charter=%d conv=%d\n%s", intro, charter, conv, got)
	}
	if !(intro < charter && charter < conv) {
		t.Fatalf("order wrong: intro=%d charter=%d conv=%d", intro, charter, conv)
	}

	custom := BuildSystemPrompt(SystemPromptOpts{Custom: "BESPOKE", PersonaName: "Vartija", Charter: "REVIEW-AS-SECURITY"})
	if strings.Contains(custom, "REVIEW-AS-SECURITY") {
		t.Errorf("charter must be ignored under Custom:\n%s", custom)
	}
}
