package persona

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

// writeUserPersona drops a persona into $TERVA_HOME/personas so the tier
// machinery (and therefore Lookup) can see it. stem may name a team
// subdirectory ("review-crew/vartija") — that is what `terva persona init`
// writes, and it is the layout the store had to learn to resolve.
//
// Through Dir() rather than the TERVA_HOME env var, so a test that forgot to
// set a home writes nowhere near the developer's own library.
func writeUserPersona(t *testing.T, stem, body string) string {
	t.Helper()
	p := filepath.Join(Dir(), filepath.FromSlash(stem)+".md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestComposeCharterInheritsTheDefault(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	path := writeUserPersona(t, "assistant", "---\nname: Assistant\nextends: mieli\n---\nOperate as a calm administrative partner.")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(string(raw), path)
	if err != nil {
		t.Fatal(err)
	}
	// Parsing does not compose: the roster shows what a file says.
	if p.Inherited != "" {
		t.Errorf("Parse resolved extends; composition belongs at selection time")
	}

	got, err := ComposeCharter(p)
	if err != nil {
		t.Fatalf("ComposeCharter: %v", err)
	}
	if !strings.Contains(got.Inherited, "calm, practical coding collaborator") {
		t.Errorf("inherited charter is not Mieli's: %q", got.Inherited)
	}
	if got.InheritedSource != "embedded:mieli.md" {
		t.Errorf("InheritedSource = %q, want embedded:mieli.md", got.InheritedSource)
	}
	if got.Charter != "Operate as a calm administrative partner." {
		t.Errorf("own charter was modified: %q", got.Charter)
	}

	// Order is load-bearing: the base states the general contract, the
	// extending persona qualifies it. Reversed, the general contract reads as
	// the qualification.
	composed := got.ComposedCharter()
	baseAt := strings.Index(composed, "calm, practical coding collaborator")
	ownAt := strings.Index(composed, "calm administrative partner")
	if baseAt < 0 || ownAt < 0 {
		t.Fatalf("composed charter is missing a half: %q", composed)
	}
	if baseAt > ownAt {
		t.Errorf("inherited charter must come FIRST; got own at %d, base at %d", ownAt, baseAt)
	}
}

// The behaviour this whole feature exists for: the orientation contract that
// ships in the default charter reaching a custom persona that asked for it.
func TestExtendingPersonaGetsTheOrientationContract(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	writeUserPersona(t, "assistant", "---\nname: Assistant\nextends: mieli\n---\nTrack what is in flight.")

	p, err := Resolve("assistant")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.Contains(p.ComposedCharter(), "before your first tool call") {
		t.Errorf("an extending persona did not inherit the orientation contract:\n%s", p.ComposedCharter())
	}
}

// A persona that extends nothing is byte-identical to today — the acceptance
// criterion that keeps this feature from being a behaviour change for the
// entire shipped crew.
func TestNonExtendingPersonaIsUnchanged(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	writeUserPersona(t, "plain", "---\nname: Plain\n---\nJust this.")

	p, err := Resolve("plain")
	if err != nil {
		t.Fatal(err)
	}
	if p.Inherited != "" || p.InheritedSource != "" {
		t.Errorf("a persona with no extends inherited something: %q from %q", p.Inherited, p.InheritedSource)
	}
	if p.ComposedCharter() != "Just this." {
		t.Errorf("ComposedCharter = %q, want the charter verbatim", p.ComposedCharter())
	}
}

// Every built-in must stay self-contained: TW-017 settled that specialists do
// not inherit the default's collaboration style, and a stray `extends` in the
// shipped crew would reverse that decision silently.
func TestBuiltinCrewDoesNotExtend(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	for _, p := range listEmbedded() {
		if p.Extends != "" {
			t.Errorf("built-in %s declares extends: %q — specialists must stay self-contained", p.Source, p.Extends)
		}
	}
}

func TestComposeCharterRejections(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		want string
	}{
		{
			name: "immersive",
			file: "---\nname: Data\nextends: mieli\nimmersive: true\n---\nYou are Data.",
			want: "immersive",
		},
		{
			name: "unsupported base",
			file: "---\nname: Custom\nextends: vartija\n---\nReview things.",
			want: "not supported",
		},
		{
			name: "namespaced base",
			file: "---\nname: Custom\nextends: review-crew:vartija\n---\nReview things.",
			want: "not supported",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TERVA_HOME", testsupport.TempDir(t))
			p, err := Parse(tc.file, "test.md")
			if err != nil {
				t.Fatal(err)
			}
			got, cerr := ComposeCharter(p)
			if cerr == nil {
				t.Fatalf("ComposeCharter accepted it; inherited %q", got.Inherited)
			}
			if !strings.Contains(cerr.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", cerr, tc.want)
			}
			if got.Inherited != "" {
				t.Errorf("a rejected composition still inherited %q", got.Inherited)
			}
		})
	}
}

// A user file at personas/mieli.md shadows the built-in, so `extends: mieli`
// inside it resolves to itself. Left unguarded that is an infinite regress
// dressed up as a config typo.
func TestComposeCharterRejectsSelfExtension(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	path := writeUserPersona(t, "mieli", "---\nname: Mieli\nextends: mieli\n---\nMy own take.")

	raw, _ := os.ReadFile(path)
	p, err := Parse(string(raw), path)
	if err != nil {
		t.Fatal(err)
	}
	_, cerr := ComposeCharter(p)
	if cerr == nil || !strings.Contains(cerr.Error(), "cannot extend itself") {
		t.Fatalf("self-extension error = %v, want one naming the self-reference", cerr)
	}
}

// Shadow the base with a file that itself extends, and v1's one-level promise
// would quietly become two. Refuse instead of resolving a chain nobody
// designed.
func TestComposeCharterRejectsAChain(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	writeUserPersona(t, "mieli", "---\nname: Mieli\nextends: something\n---\nA shadowing base.")
	path := writeUserPersona(t, "assistant", "---\nname: Assistant\nextends: mieli\n---\nMine.")

	raw, _ := os.ReadFile(path)
	p, _ := Parse(string(raw), path)
	_, cerr := ComposeCharter(p)
	if cerr == nil || !strings.Contains(cerr.Error(), "chains are not supported") {
		t.Fatalf("chain error = %v, want one refusing the chain", cerr)
	}
}

// Resolve is the single choke point. If a caller could reach a prompt
// without composing, `extends` would be a field that works only sometimes —
// which is worse than one that does not exist.
func TestResolvePersonaComposesOnEveryPath(t *testing.T) {
	body := "---\nname: Assistant\nextends: mieli\n---\nMine."

	t.Run("by name", func(t *testing.T) {
		t.Setenv("TERVA_HOME", testsupport.TempDir(t))
		writeUserPersona(t, "assistant", body)
		p, err := Resolve("assistant")
		if err != nil || p.Inherited == "" {
			t.Fatalf("name path: inherited %q, err %v", p.Inherited, err)
		}
	})

	t.Run("by path", func(t *testing.T) {
		t.Setenv("TERVA_HOME", testsupport.TempDir(t))
		path := writeUserPersona(t, "assistant", body)
		p, err := Resolve(path)
		if err != nil || p.Inherited == "" {
			t.Fatalf("path path: inherited %q, err %v", p.Inherited, err)
		}
	})

	t.Run("TERVA_HOME/persona.md", func(t *testing.T) {
		home := testsupport.TempDir(t)
		t.Setenv("TERVA_HOME", home)
		if err := os.WriteFile(filepath.Join(home, "persona.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := Resolve("")
		if err != nil || p.Inherited == "" {
			t.Fatalf("root-file path: inherited %q, err %v", p.Inherited, err)
		}
	})

	t.Run("default_persona", func(t *testing.T) {
		t.Setenv("TERVA_HOME", testsupport.TempDir(t))
		writeUserPersona(t, "assistant", body)
		if err := config.SaveConfig(config.Config{DefaultPersona: "assistant"}); err != nil {
			t.Fatal(err)
		}
		p, err := Resolve("")
		if err != nil || p.Inherited == "" {
			t.Fatalf("default_persona path: inherited %q, err %v", p.Inherited, err)
		}
	})
}

// A bad extends must stop the run rather than quietly drop the inheritance: a
// persona that silently loses half its charter is the original bug wearing a
// different hat.
func TestResolvePersonaFailsOnABadExtends(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	writeUserPersona(t, "broken", "---\nname: Broken\nextends: nosuchpersona\n---\nMine.")
	if _, err := Resolve("broken"); err == nil {
		t.Fatal("Resolve accepted an unsupported extends")
	}
}

// The round trip is where a new frontmatter field goes to die: Marshal
// writes an explicit field list, so a field added to the parser and forgotten
// here is DELETED the first time anyone saves the persona from an editor. That
// is the same silent replacement `extends` exists to fix, so it gets a test of
// its own rather than trust.
func TestExtendsSurvivesAWriteReadRoundTrip(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	original := Persona{Name: "Assistant", Extends: "mieli", Charter: "Track what is in flight."}

	raw, err := Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "extends: mieli") {
		t.Fatalf("marshaled persona has no extends:\n%s", raw)
	}
	back, err := Parse(string(raw), "round-trip.md")
	if err != nil {
		t.Fatal(err)
	}
	if back.Extends != "mieli" {
		t.Errorf("Extends = %q after a round trip, want mieli", back.Extends)
	}
}

// Writing the RESOLVED charter back would convert a reference into the
// hand-copied fork this feature replaces — permanently, and on somebody's
// first save.
func TestMarshalDoesNotBakeTheInheritedCharterIn(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	p := Persona{
		Name:            "Assistant",
		Extends:         "mieli",
		Charter:         "Track what is in flight.",
		Inherited:       "Work as a calm, practical coding collaborator.",
		InheritedSource: "embedded:mieli.md",
	}
	raw, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "calm, practical coding collaborator") {
		t.Errorf("the inherited charter was written into the file:\n%s", raw)
	}
}
