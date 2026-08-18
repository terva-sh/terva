package agent

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/agent/persona"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/testsupport"
)

// plantGatingExt writes an extension under root that ships one skill, one
// persona and one lore entry, so a single fixture exercises every surface that
// loads extension content.
func plantGatingExt(t *testing.T, root, name string) {
	t.Helper()
	ext := filepath.Join(root, "extensions", name)
	for _, d := range []string{
		filepath.Join(ext, "skills", name+"-skill"),
		filepath.Join(ext, "personas"),
		filepath.Join(ext, "lore"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(ext, "extension.json"), `{"name":"`+name+`","version":"1.0.0"}`)
	write(filepath.Join(ext, "skills", name+"-skill", "SKILL.md"),
		"---\nname: "+name+"-skill\ndescription: shipped by the "+name+" extension.\n---\n\nbody\n")
	write(filepath.Join(ext, "personas", name+"-persona.md"),
		"---\nname: "+name+"Persona\nsummary: shipped by the "+name+" extension\n---\n\ncharter\n")
	write(filepath.Join(ext, "lore", name+"-lore.md"),
		"---\nname: "+name+"Lore\nkeys:\n  - "+name+"trigger\n---\n\nlore body\n")
}

// surfaced reports which loaders currently carry ext's contributions.
type surfaced struct{ Skill, Persona, Lore bool }

func gatingSurfaces(t *testing.T, cwd string, trusted bool, ext string) surfaced {
	t.Helper()
	gate := skills.Gate{
		TrustProject: trusted,
		Disabled:     config.ResolveConfig(cwd, trusted).Config.DisableExtensions,
	}
	var got surfaced
	found, _ := skills.Discover(config.TervaHome(), cwd, testsupport.TempDir(t), true, false, gate)
	for _, s := range found {
		if s.Name == ext+"-skill" {
			got.Skill = true
		}
	}
	for _, p := range persona.All() {
		if strings.EqualFold(p.Name, ext+"Persona") {
			got.Persona = true
		}
	}
	entries, _, _ := lore.Discover(config.TervaHome(), cwd, gate)
	for _, e := range entries {
		if strings.EqualFold(e.Name, ext+"Lore") {
			got.Lore = true
		}
	}
	return got
}

// 🔑 Switching an extension off has to stop it steering the model.
//
// It did not. `disable_extensions` reached the extension manager (so the tools
// went) and the persona library's USER-layer read (so the personas went), while
// skill discovery honoured no disable list at all — so the extension kept
// injecting its SKILL.md instructions into the system prompt with every visible
// sign of being off. A skill is text the model reads whether or not anyone
// asked for it, which is what makes this the half worth closing first.
func TestADisabledExtensionStopsInjectingIntoThePrompt(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	plantGatingExt(t, home, "gatedext")

	// Baseline, or the rest of this test proves nothing: nothing is disabled,
	// so every surface carries it.
	if got := gatingSurfaces(t, cwd, true, "gatedext"); !got.Skill || !got.Persona || !got.Lore {
		t.Fatalf("baseline: %+v, want all three present", got)
	}

	for _, tc := range []struct {
		what        string
		writeConfig func()
		want        surfaced
		why         string
	}{
		{
			what: "the USER config disables it",
			writeConfig: func() {
				writeGatingFile(t, filepath.Join(home, "config.json"), `{"disable_extensions":["gatedext"]}`)
				writeGatingFile(t, filepath.Join(cwd, ".terva", "config.json"), `{}`)
			},
			want: surfaced{},
			why:  "an extension the user switched off contributes nothing",
		},
		{
			what: "the PROJECT config disables it",
			writeConfig: func() {
				writeGatingFile(t, filepath.Join(home, "config.json"), `{}`)
				writeGatingFile(t, filepath.Join(cwd, ".terva", "config.json"), `{"disable_extensions":["gatedext"]}`)
			},
			// 🪤 DECIDED, not overlooked. The persona library has no cwd and no
			// trust verdict, so it cannot see the project layer without a
			// resolver threaded through every caller of All/Lookup/Resolve —
			// the parameterless API its package doc names as the reason it
			// could leave package build. The exposure differs in kind: a skill
			// is injected, a persona is chosen. Flip this to false the day
			// persona learns the resolved config.
			want: surfaced{Persona: true},
			why:  "a project-layer disable stops both injections; the persona stays listed",
		},
		{
			what: "the MANIFEST disables it",
			writeConfig: func() {
				writeGatingFile(t, filepath.Join(home, "config.json"), `{}`)
				writeGatingFile(t, filepath.Join(cwd, ".terva", "config.json"), `{}`)
				writeGatingFile(t, filepath.Join(home, "extensions", "gatedext", "extension.json"),
					`{"name":"gatedext","version":"1.0.0","enabled":false}`)
			},
			want: surfaced{},
			why:  "the manifest's own flag was always honoured by every surface, and still is",
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Join(cwd, ".terva"), 0o755); err != nil {
				t.Fatal(err)
			}
			tc.writeConfig()
			t.Cleanup(func() {
				writeGatingFile(t, filepath.Join(home, "extensions", "gatedext", "extension.json"),
					`{"name":"gatedext","version":"1.0.0"}`)
			})
			if got := gatingSurfaces(t, cwd, true, "gatedext"); got != tc.want {
				t.Errorf("%s: got %+v, want %+v — %s", tc.what, got, tc.want, tc.why)
			}
		})
	}
}

// The disable list is hand-written, so it matches either spelling of the
// extension: the name in its manifest or the directory it sits in.
func TestADisableMatchesEitherSpellingOfTheName(t *testing.T) {
	for _, spelling := range []string{"manifestname", "dirname"} {
		t.Run(spelling, func(t *testing.T) {
			home := testsupport.TempDir(t)
			cwd := testsupport.TempDir(t)
			t.Setenv("TERVA_HOME", home)
			plantGatingExt(t, home, "dirname")
			writeGatingFile(t, filepath.Join(home, "extensions", "dirname", "extension.json"),
				`{"name":"manifestname","version":"1.0.0"}`)
			writeGatingFile(t, filepath.Join(home, "config.json"), `{"disable_extensions":["`+spelling+`"]}`)

			if got := gatingSurfaces(t, cwd, true, "dirname"); got != (surfaced{}) {
				t.Errorf("disabling by %q left %+v loaded", spelling, got)
			}
		})
	}
}

// Every production caller of a content loader passes the resolved disable set.
//
// 🪤 The behaviour gates in this file build their own Gate, so they prove the
// LIBRARIES honour a disable set and say nothing about whether the wiring hands
// them one. Dropping `Disabled:` from build.Resolve fired none of them — the
// exact shape of the original bug, one layer up, and it would ship with a green
// suite. This scans the call sites instead.
func TestEveryContentLoaderCallPassesTheDisableSet(t *testing.T) {
	loaders := []string{"skills.Discover(", "lore.Discover("}
	root := repoRoot
	scanned, calls := 0, 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if testsupport.SkipScanDir(root, path, d) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		scanned++
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		text := string(src)
		for _, loader := range loaders {
			for i := 0; ; {
				j := strings.Index(text[i:], loader)
				if j < 0 {
					break
				}
				open := i + j + len(loader) - 1
				call, ok := balanced(text, open)
				i = open + 1
				if !ok {
					continue
				}
				calls++
				if strings.Contains(call, "Disabled:") {
					continue
				}
				line := 1 + strings.Count(text[:open], "\n")
				t.Errorf("%s:%d — %s is called without a Disabled set.\n"+
					"  Pass the RESOLVED (user ∪ project) disable_extensions, from eff.Config.DisableExtensions "+
					"where a resolved config is in hand or config.ResolveConfig(cwd, trusted) where it is not. "+
					"A loader handed no disable set injects a switched-off extension's content into the prompt, "+
					"which is the bug this seam exists to close — and the behaviour tests here cannot see it, "+
					"because they build their own gate.", rel, line, strings.TrimSuffix(loader, "("))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Vacuity floors: a walk that read nothing, or matched no call, reports a
	// clean tree either way.
	if scanned < 200 {
		t.Fatalf("only %d Go files walked; the scan is broken", scanned)
	}
	if calls < 4 {
		t.Fatalf("found %d content-loader calls; there are more than that, so the matcher is broken", calls)
	}
}

// balanced returns the text of the parenthesised group starting at src[open],
// which must be '('.
func balanced(src string, open int) (string, bool) {
	if open >= len(src) || src[open] != '(' {
		return "", false
	}
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return src[open : i+1], true
			}
		}
	}
	return "", false
}

// enabledFlagReaders names the files allowed to read an extension manifest's
// `enabled` flag for themselves, with why. Everything that decides which
// extensions may CONTRIBUTE asks extroots.
//
// Four surfaces answered "is this extension on?" and they answered it four
// ways, because each had grown its own copy of the walk: the manager honoured
// the resolved disable list, personas honoured only its user layer, and skills
// and lore honoured neither — so a disabled extension kept injecting SKILL.md
// instructions and lore entries into the prompt after its tools had gone. A
// fifth copy is a fifth answer, so the rule is scanned rather than asked for
// politely in a comment.
//
// The two entries here READ the flag without gating content on it, which is why
// they are exemptions rather than offenders.
var enabledFlagReaders = map[string]string{
	"packages/agent/extroots/extroots.go": "the one place the flag decides whether an extension contributes",
	"packages/agent/extdoctor.go":         "REPORTS the flag in the doctor's table; gates nothing on it",
	"packages/agent/extupdate.go":         "skips UPDATING a disabled extension, an operation on the extension itself",
}

// Only extroots decides whether an extension may contribute.
func TestOnlyOneScannerDecidesWhichExtensionsAreEnabled(t *testing.T) {
	root := repoRoot
	// The shape of the decision, as all four copies wrote it: read the
	// extension manifest, then branch on its enabled flag. Both halves,
	// because `Enabled *bool` alone is a common config shape and
	// "extension.json" alone is read by a dozen files that install, update,
	// migrate or report on extensions without gating content.
	const manifest, flag = `"extension.json"`, "Enabled *bool"
	var offenders []string
	seen := map[string]bool{}
	scanned := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if testsupport.SkipScanDir(root, path, d) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		scanned++
		if !strings.Contains(string(src), manifest) || !strings.Contains(string(src), flag) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		seen[rel] = true
		if _, ok := enabledFlagReaders[rel]; !ok {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Vacuity floors: a walk that read nothing, or found no reader at all,
	// reports a clean tree either way.
	if scanned < 200 {
		t.Fatalf("only %d Go files walked; the scan is broken and a pass proves nothing", scanned)
	}
	if len(seen) == 0 {
		t.Fatalf("no file reads %s alongside %s; the needle no longer matches how the decision is written, "+
			"so this gate is blind", manifest, flag)
	}

	sort.Strings(offenders)
	for _, o := range offenders {
		t.Errorf("%s decides an extension's enabled-ness for itself.\n"+
			"  Call extroots.Enabled instead. Four surfaces each grew their own copy of this walk and each "+
			"answered differently, which is how a disabled extension went on injecting skills and lore into "+
			"the prompt after its tools and personas were gone. If it reads the flag WITHOUT gating "+
			"contributions on it, add it to enabledFlagReaders with the reason.", o)
	}

	// A licence for a file that no longer reads the flag would silently
	// re-permit a copy at that path later.
	for path, why := range enabledFlagReaders {
		if !seen[path] {
			t.Errorf("enabledFlagReaders licenses %s (%s), but it no longer reads the flag; drop the entry", path, why)
		}
	}
}

func writeGatingFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
