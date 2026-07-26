package agent

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/testsupport"
)

func TestValidateOnePersona(t *testing.T) {
	dir := testsupport.TempDir(t)
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"valid.md", "---\nname: Ok\naccent_color: \"#7aa2f7\"\n---\nA real charter.", true},
		{"noname.md", "---\nsummary: x\n---\nBody.", false},
		{"macro.md", "---\nname: Ok\n---\nHello {{user}}.", false},
		{"empty.md", "---\nname: Ok\n---\n", false},
		{"badcolor.md", "---\nname: Ok\naccent_color: blue\n---\nCharter.", false},
	}
	for _, c := range cases {
		p := filepath.Join(dir, c.name)
		if err := os.WriteFile(p, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := validateOnePersona(p); got != c.want {
			t.Errorf("%s: validateOnePersona=%v, want %v", c.name, got, c.want)
		}
	}
}

// The note is the fix for the bug that a persona author cannot see what their
// charter leaves OUT. Pin the two things it must say: the size, and that the
// default persona's charter does not come along.
func TestCharterScopeNote(t *testing.T) {
	additive := charterScopeNote(build.Persona{Name: "Assistant", Charter: strings.Repeat("x", 1747)})
	for _, want := range []string{"1747", "ONLY charter", "Mieli"} {
		if !strings.Contains(additive, want) {
			t.Errorf("additive note %q is missing %q", additive, want)
		}
	}

	immersive := charterScopeNote(build.Persona{Name: "Data", Charter: "You are Data.", Immersive: true})
	if !strings.Contains(immersive, "WHOLE prompt") {
		t.Errorf("immersive note %q should say the charter owns the whole prompt", immersive)
	}
	// An immersive charter replaces terva's conventions too, so telling its
	// author about Mieli would point at the wrong absence.
	if strings.Contains(immersive, "Mieli") {
		t.Errorf("immersive note %q should not talk about the default persona", immersive)
	}
}

// captureValidate runs validateOnePersona and returns what it printed. Reads
// concurrently so a verdict longer than the pipe buffer can't deadlock it.
func captureValidate(t *testing.T, path string) (string, bool) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	ok := validateOnePersona(path)
	_ = w.Close()
	os.Stdout = old
	return <-done, ok
}

// Computing the note is not the feature — PRINTING it is. Without this, deleting
// the call site is a silent no-op that TestCharterScopeNote happily survives.
func TestValidatePrintsTheCharterScopeNote(t *testing.T) {
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "assistant.md")
	if err := os.WriteFile(p, []byte("---\nname: Assistant\n---\nOperate as a calm administrative partner."), 0o644); err != nil {
		t.Fatal(err)
	}
	out, ok := captureValidate(t, p)
	if !ok {
		t.Fatalf("valid persona reported invalid:\n%s", out)
	}
	if !strings.Contains(out, "ONLY charter") || !strings.Contains(out, "Mieli") {
		t.Errorf("validate output does not say the default persona's charter is absent:\n%s", out)
	}
}

// A file that failed validation prints problems and stops. Telling someone what
// their charter will replace, when it will not load at all, is noise on top of
// an error.
func TestValidateSkipsTheNoteOnAnInvalidPersona(t *testing.T) {
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "broken.md")
	// A REAL charter body with a fatal frontmatter problem. An empty-charter
	// fixture cannot test this: it produces no note to suppress, so it passes
	// whether the suppression works or not.
	if err := os.WriteFile(p, []byte("---\nname: Broken\naccent_color: blue\n---\nA real charter body."), 0o644); err != nil {
		t.Fatal(err)
	}
	out, ok := captureValidate(t, p)
	if ok {
		t.Fatalf("a bad accent_color should be invalid:\n%s", out)
	}
	if strings.Contains(out, "ONLY charter") {
		t.Errorf("invalid persona should not get the scope note:\n%s", out)
	}
}

func TestPersonaClip(t *testing.T) {
	if got := personaClip("hello", 10); got != "hello" {
		t.Errorf("no-clip: %q", got)
	}
	if got := personaClip("hello world", 5); got != "hell…" {
		t.Errorf("clip: %q", got)
	}
}

// A bad extends is a hard error at run time (ResolvePersona composes on every
// path), so validate must call it a problem — a ✓ on a persona that cannot
// start is worse than no check at all.
func TestValidateRejectsABadExtends(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := testsupport.TempDir(t)
	for _, tc := range []struct{ name, body, want string }{
		{"unsupported.md", "---\nname: X\nextends: vartija\n---\nBody.", "not supported"},
		{"immersive.md", "---\nname: X\nextends: mieli\nimmersive: true\n---\nBody.", "immersive"},
	} {
		p := filepath.Join(dir, tc.name)
		if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		out, ok := captureValidate(t, p)
		if ok {
			t.Errorf("%s: validate passed a persona that cannot start:\n%s", tc.name, out)
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("%s: verdict does not explain the problem (%q):\n%s", tc.name, tc.want, out)
		}
	}
}

// The budget guards the cached prefix, so it must measure what lands there. A
// 1,747-char charter inheriting Mieli's 664 is over the 2,000 budget even
// though neither half is.
func TestValidateBudgetsTheAssembledCharter(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "assistant.md")
	body := "---\nname: Assistant\nextends: mieli\n---\n" + strings.Repeat("x", 1747)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, ok := captureValidate(t, p)
	if !ok {
		t.Fatalf("an over-budget charter is a warning, not a failure:\n%s", out)
	}
	if !strings.Contains(out, "over the 2000") {
		t.Errorf("assembled charter is over budget but validate did not say so:\n%s", out)
	}
	// And the note has to show the split, or the warning names a number the
	// author cannot act on: they control only one of the two halves.
	if !strings.Contains(out, "inherited from mieli") || !strings.Contains(out, "1747") {
		t.Errorf("verdict does not attribute the assembled size:\n%s", out)
	}
}

// Nothing ran the validator over the personas terva itself ships, so two of
// them failed it for years: seppa and toimittaja EDIT character cards, so their
// charters discuss {{char}}/{{user}} as a matter of course. `terva persona
// validate` called them invalid, which is the validator being wrong about the
// only charters in the tree guaranteed to be right.
func TestEveryBuiltinPersonaValidates(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := testsupport.TempDir(t)
	seen := 0
	err := fs.WalkDir(build.BuiltinPersonasFS, build.BuiltinPersonasRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") || strings.EqualFold(filepath.Base(p), "README.md") {
			return err
		}
		raw, err := fs.ReadFile(build.BuiltinPersonasFS, p)
		if err != nil {
			return err
		}
		// Validate from a real file: that is the path the CLI takes, and a
		// helper reimplementing it would be a second validator to keep in sync.
		onDisk := filepath.Join(dir, strings.ReplaceAll(p, "/", "_"))
		if err := os.WriteFile(onDisk, raw, 0o644); err != nil {
			return err
		}
		seen++
		if out, ok := captureValidate(t, onDisk); !ok {
			t.Errorf("built-in persona %s does not pass terva persona validate:\n%s", p, out)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < 10 {
		t.Fatalf("walked only %d built-in personas; the crew is larger than that, so the walk is broken", seen)
	}
}

// The macro check still has to fire on what it was written for: a charter
// converted from a character card, with a placeholder left where a name belongs.
func TestMacroCheckDistinguishesUseFromMention(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := testsupport.TempDir(t)
	for _, tc := range []struct {
		name  string
		body  string
		valid bool
	}{
		{"used.md", "---\nname: X\n---\nYou are {{char}}, talking to {{user}}.", false},
		{"start-delim.md", "---\nname: X\n---\nExamples follow.\n<START>\nHello.", false},
		{"mentioned-inline.md", "---\nname: X\n---\nKeep `{{char}}` and `{{user}}` intact when you edit a card.", true},
		{"mentioned-fenced.md", "---\nname: X\n---\nA card looks like:\n\n```\nYou are {{char}}.\n```\n\nRepair it in place.", true},
		{"half-mentioned.md", "---\nname: X\n---\nKeep `{{char}}` intact, and never write {{user}} yourself.", false},
	} {
		p := filepath.Join(dir, tc.name)
		if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		out, ok := captureValidate(t, p)
		if ok != tc.valid {
			t.Errorf("%s: valid=%v, want %v:\n%s", tc.name, ok, tc.valid, out)
		}
	}
}

// Blanking rather than deleting the code regions: splicing the text around a
// removal can join two halves into a macro nobody wrote.
func TestStripCodeRegionsDoesNotManufactureAMacro(t *testing.T) {
	got := stripCodeRegions("{{ch`x`ar}}")
	if personaMacroRe.MatchString(got) {
		t.Errorf("stripping code spans invented a macro: %q", got)
	}
}
