package persona

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func personaHome(t *testing.T) string {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	return home
}

// The shipped crew and the write path must agree on where a persona's file
// goes, because they are two implementations of the same decision: `terva
// persona init` copies each built-in to Dir()/<its embedded rel path>, and
// every other write goes through Path. A user who inits and a user who edits
// through the library must end up with the same file, or one of them is
// shadowing nothing.
//
// Derived from the embed rather than listed, so this cannot pass by omission: a
// crew member added under a new team directory, or one whose frontmatter name
// stops folding to its filename, fails here on the commit that adds it.
func TestEveryBuiltinFilesWhereTheWritePathWouldPutIt(t *testing.T) {
	personaHome(t)

	checked, namespaced := 0, 0
	err := fs.WalkDir(BuiltinFS, BuiltinRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(p, ".md") || strings.EqualFold(path.Base(p), "README.md") {
			return nil
		}
		rel := strings.TrimPrefix(p, BuiltinRoot+"/")
		raw, rerr := fs.ReadFile(BuiltinFS, p)
		if rerr != nil {
			return rerr
		}
		parsed, perr := Parse(string(raw), "embedded:"+rel)
		if perr != nil {
			t.Errorf("%s does not parse: %v", rel, perr)
			return nil
		}
		parsed.Namespace = nsFromRel(rel)
		checked++
		if parsed.Namespace != "" {
			namespaced++
		}

		got, gerr := Path(parsed)
		if gerr != nil {
			t.Errorf("Path(%s): %v", rel, gerr)
			return nil
		}
		want := filepath.Join(Dir(), filepath.FromSlash(rel))
		if got != want {
			t.Errorf("%s: a user copy would be written to\n  %s\nbut `terva persona init` puts it at\n  %s\n"+
				"The two must agree: the init copy and the library edit are the same act, and a copy filed "+
				"anywhere but the built-in's own path shares neither its Key nor its ref, so it shadows nothing.",
				rel, got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Vacuity floors. A walk that found nothing, or found only top-level files,
	// would pass every assertion above while asserting nothing about the case
	// this exists for.
	if checked < 10 {
		t.Fatalf("only %d built-in personas walked; the embed walk is broken and a pass here proves nothing", checked)
	}
	if namespaced == 0 {
		t.Fatal("no NAMESPACED built-in was walked, so the namespace half of the path was never exercised")
	}
	t.Logf("checked %d built-in personas, %d of them namespaced", checked, namespaced)
}

// Path is the only place a namespace becomes a directory, so it is the only
// place that has to distrust one. An extension's namespace is its self-declared
// manifest name — a string from a bundle the user downloaded — and it reaches
// here whenever someone copies that extension's persona to edit it.
func TestPathRefusesANamespaceThatIsNotADirectoryName(t *testing.T) {
	home := personaHome(t)

	for _, ns := range []string{"..", "../..", "../../.ssh", "a/b", `a\b`, "team/../.."} {
		p, err := Path(Persona{Name: "Vartija", Namespace: ns})
		if err == nil {
			t.Errorf("Path with namespace %q returned %q, want a refusal — that path escapes the library", ns, p)
		}
	}

	// And the write refuses too, rather than refusing to compute a path and
	// then writing somewhere else.
	if _, err := Write(Persona{Name: "Vartija", Namespace: "../../evil", Charter: "x"}); err == nil {
		t.Error("Write accepted a traversing namespace")
	}
	outside := filepath.Join(filepath.Dir(filepath.Dir(home)), "evil")
	if _, err := os.Stat(outside); err == nil {
		t.Errorf("a refused write left a file at %s", outside)
	}

	// The legitimate shape still works, or the fence is just a ban.
	got, err := Path(Persona{Name: "Vartija", Namespace: "review-crew"})
	if err != nil {
		t.Fatalf("Path with a plain namespace: %v", err)
	}
	if want := filepath.Join(Dir(), "review-crew", "vartija.md"); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

// UserPath answers "which file", and every caller that asks it means one of
// three questions. A qualified ref names one file; a bare name may name a
// persona filed under a team directory; and a pre-fold spelling is still the
// user's file. Getting any of them wrong picks the wrong file to overwrite or
// remove.
func TestUserPathResolvesRefsAndTeamDirectories(t *testing.T) {
	personaHome(t)
	nested := writeUserPersona(t, "review-crew/vartija", "---\nname: Vartija\n---\n\nMine.\n")

	// By qualified ref — what the library sends for get and delete.
	if got, ok := UserPath("review-crew:vartija"); !ok || got != nested {
		t.Errorf("UserPath(review-crew:vartija) = (%q, %v), want (%q, true)", got, ok, nested)
	}
	// By bare name — what the editor sends, and the name the user knows.
	if got, ok := UserPath("Vartija"); !ok || got != nested {
		t.Errorf("UserPath(Vartija) = (%q, %v), want (%q, true)", got, ok, nested)
	}
	// A ref naming a namespace this persona is not in must not claim its file.
	if got, ok := UserPath("raati-crew:vartija"); ok {
		t.Errorf("UserPath(raati-crew:vartija) claimed %q, which belongs to review-crew", got)
	}

	// A top-level file of the same stem is a DIFFERENT persona, and it is the
	// one a bare name resolves to in the roster — so it is the one UserPath
	// must answer with, not the nested file it was falling back to.
	flat := writeUserPersona(t, "vartija", "---\nname: Vartija\n---\n\nFlat.\n")
	if got, ok := UserPath("Vartija"); !ok || got != flat {
		t.Errorf("with both files present, UserPath(Vartija) = (%q, %v), want the top-level %q", got, ok, flat)
	}
	if got, ok := UserPath("review-crew:vartija"); !ok || got != nested {
		t.Errorf("the qualified ref must still name the nested file: got (%q, %v), want %q", got, ok, nested)
	}
}

// Overwrite carries identity; a rename does not get to inherit it. Pinned
// because Overwrite reads as if it should always reuse the file — and because
// the half that is not obvious is the other one: a renamed crew member keeps
// its shelf, so it stays with the crew rather than falling to the top level.
func TestOverwriteKeepsTheFileButARenameKeepsOnlyTheShelf(t *testing.T) {
	personaHome(t)
	into := Persona{Name: "YATA-1", Namespace: "raati-crew", Source: "embedded:raati-crew/yata.md"}

	same := Overwrite(Persona{Name: "YATA-1", Charter: "mine"}, into)
	got, err := Path(same)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(Dir(), "raati-crew", "yata.md"); got != want {
		t.Errorf("an edit landed at %q, want the file it is editing, %q.\n"+
			"  YATA-1 slugs to \"yata-1\"; a copy written there is keyed raati-crew:yata-1 while the "+
			"panel convenes raati-crew:yata, so the edit reaches no seat.", got, want)
	}

	renamed := Overwrite(Persona{Name: "Watchman", Charter: "mine"}, into)
	if renamed.Source != "" {
		t.Errorf("a rename reused the old file: source = %q", renamed.Source)
	}
	if renamed.Namespace != "raati-crew" {
		t.Errorf("a renamed crew member left its shelf: namespace = %q", renamed.Namespace)
	}
	got, err = Path(renamed)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(Dir(), "raati-crew", "watchman.md"); got != want {
		t.Errorf("renamed path = %q, want %q", got, want)
	}
}

// The pre-fold rescue has to reach into team directories too. It looked only at
// the top level, so a persona whose name carries diacritics and lives in a team
// directory would have had its old file left behind as a second roster entry —
// the exact leftover the fold rescue exists to clear.
func TestLegacySpellingIsFoundInsideATeamDirectory(t *testing.T) {
	personaHome(t)
	legacy := writeUserPersona(t, "review-crew/sepp", "---\nname: Seppä\n---\n\nOld.\n")

	if got, ok := UserPath("review-crew:Seppä"); !ok || got != legacy {
		t.Errorf("UserPath(review-crew:Seppä) = (%q, %v), want the pre-fold file %q", got, ok, legacy)
	}

	dest, err := Write(Persona{Name: "Seppä", Namespace: "review-crew", Charter: "New."})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(Dir(), "review-crew", "seppa.md"); dest != want {
		t.Fatalf("Write landed at %q, want %q", dest, want)
	}
	if _, err := os.Stat(legacy); err == nil {
		t.Error("the pre-fold file survived the write, so the roster now carries two Seppä")
	}
}
