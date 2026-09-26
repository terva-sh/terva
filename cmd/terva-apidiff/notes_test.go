package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCover(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want cover
	}{
		{"packages/core", cover{Pkg: "packages/core"}},
		{"packages/core.Agent", cover{Pkg: "packages/core", Name: "Agent"}},
		{"packages/core.Agent.Run", cover{Pkg: "packages/core", Name: "Agent.Run"}},
		{"packages/provider/...", cover{Pkg: "packages/provider", Tree: true}},
		{"packages/core/permission.ConfirmGate", cover{Pkg: "packages/core/permission", Name: "ConfirmGate"}},
	} {
		got, err := parseCover(tc.in)
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		tc.want.Text = tc.in
		if got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"", "two words", "packages/core.", "packages/core..Run", "packages/core/", "/packages/core",
		"packages//core", "packages/core.Agent/...", ".Agent"} {
		if c, err := parseCover(bad); err == nil {
			t.Errorf("%q parsed as %+v", bad, c)
		}
	}
}

func TestCoverMatches(t *testing.T) {
	b := func(pkg, name string) breakage { return breakage{Pkg: pkg, Name: name} }
	for _, tc := range []struct {
		cover string
		brk   breakage
		want  bool
	}{
		{"packages/core.Agent.Run", b("packages/core", "Agent.Run"), true},
		{"packages/core.Agent", b("packages/core", "Agent.Run"), true},
		{"packages/core.Agent", b("packages/core", "Agent"), true},
		// A type covers its members, not every name that starts the same.
		{"packages/core.Agent", b("packages/core", "AgentEvent"), false},
		{"packages/core", b("packages/core", "Agent.Run"), true},
		// A package is one directory; its subpackages need /... .
		{"packages/core", b("packages/core/stall", "New"), false},
		{"packages/core/...", b("packages/core/stall", "New"), true},
		{"packages/core/...", b("packages/core", "New"), true},
		{"packages/core/...", b("packages/corex", "New"), false},
		{"packages/core.Agent", b("packages/session", "Agent"), false},
	} {
		c, err := parseCover(tc.cover)
		if err != nil {
			t.Fatal(err)
		}
		if got := c.matches(tc.brk); got != tc.want {
			t.Errorf("%s matches %s.%s = %v, want %v", tc.cover, tc.brk.Pkg, tc.brk.Name, got, tc.want)
		}
	}
}

const notesPage = "# Migrating\n\n" +
	"The format, which the parser must skip:\n\n" +
	"```markdown\n## Unreleased\n\nCovers: `packages/example.Fake`\n```\n\n" +
	"## Unreleased\n\n" +
	"### The sample API\n\n" +
	"Text that names `packages/sample.Prose` outside a Covers paragraph.\n\n" +
	"Covers: `packages/sample.Gone`,\n" +
	"`packages/sample.Moves`.\n\n" +
	"### Unstable changes (no promise)\n\n" +
	"#### Flaky\n\n" +
	"Its argument is a string now.\n\n" +
	"Covers: `packages/sample.Flaky`\n\n" +
	"## v0.1.0\n\n" +
	"Covers: `packages/sample.Old`\n"

func TestParseNotesReadsTheUnreleasedSection(t *testing.T) {
	doc, err := parseNotes([]byte(notesPage), "")
	if err != nil {
		t.Fatal(err)
	}
	got := doc.Covers
	var names []string
	for _, c := range got {
		names = append(names, c.Text+" "+c.Class)
	}
	want := "packages/sample.Gone stable|packages/sample.Moves stable|packages/sample.Flaky unstable"
	if strings.Join(names, "|") != want {
		t.Fatalf("covers = %q, want %q", strings.Join(names, "|"), want)
	}
	if got[1].Line != 18 {
		t.Errorf("the second name's line = %d, want 18, the line it is on", got[1].Line)
	}
}

// noteHead opens a note: a heading that names the change, and what to do.
const noteHead = "### A change\n\nDo this.\n\n"

func TestParseNotesRefusesWhatItCannotRead(t *testing.T) {
	for name, tc := range map[string]struct{ page, want string }{
		"no section":            {"# M\n\n## v0.1.0\n", "no \"## Unreleased\" heading"},
		"only a fenced section": {"# M\n\n```\n## Unreleased\n```\n", "no \"## Unreleased\" heading"},
		"two sections":          {"## Unreleased\n\n## Unreleased\n", "2 \"## Unreleased\" headings"},
		"a note after unstable": {"## Unreleased\n\n### Unstable changes (no promise)\n\n### A stable note\n", "must come last"},
		"two unstable headings": {"## Unreleased\n\n### Unstable changes (no promise)\n\n### Unstable changes (no promise)\n", "a second"},
		"a Covers naming none":  {"## Unreleased\n\n" + noteHead + "Covers: the sample package\n", "names nothing"},
		"a name it cannot read": {"## Unreleased\n\n" + noteHead + "Covers: `packages/sample.`\n", "the symbol after the package is empty"},
		"an unclosed fence":     {"## Unreleased\n\n" + noteHead + "```\nCovers: `packages/sample`\n", "never closes"},
		"a Covers outside a note": {"## Unreleased\n\nCovers: `packages/sample`\n",
			"line 3: Covers: outside a note; put it under a ### heading"},
		"an unstable Covers outside a note": {"## Unreleased\n\n### Unstable changes (no promise)\n\nCovers: `packages/sample`\n",
			"outside a note; put it under a #### heading"},
		"a note that says nothing": {"## Unreleased\n\n### A change\n\nCovers: `packages/sample`\n",
			"the note at line 3 says nothing before its Covers:"},
		"a note with an empty code example": {"## Unreleased\n\n### A change\n\n```go\n\n```\n\nCovers: `packages/sample`\n",
			"the note at line 3 says nothing"},
		// The text of one note does not guide the next.
		"guidance in the note before": {"## Unreleased\n\n" + noteHead + "### Another\n\nCovers: `packages/sample`\n",
			"the note at line 7 says nothing"},
	} {
		c, err := parseNotes([]byte(tc.page), "")
		if err == nil {
			t.Errorf("%s: parsed as %+v", name, c)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q, want it to say %q", name, err, tc.want)
		}
	}
}

// A fence ends a Covers: paragraph, and what it holds stays unread, even with
// no blank line between them.
func TestParseNotesEndsACoversParagraphAtAFence(t *testing.T) {
	page := "## Unreleased\n\n" + noteHead +
		"Covers: `packages/sample.Gone`\n```go\n// `not a name`\nCovers: `packages/example.Fake`\n```\n"
	doc, err := parseNotes([]byte(page), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Covers; len(got) != 1 || got[0].Text != "packages/sample.Gone" {
		t.Fatalf("covers = %+v, want only packages/sample.Gone", doc.Covers)
	}
}

// A code example is guidance, so a note may show the fix instead of saying it.
func TestParseNotesTakesACodeExampleAsGuidance(t *testing.T) {
	page := "## Unreleased\n\n### A change\n\n```go\nx := permission.NewPolicyGate()\n```\n\nCovers: `packages/sample`\n"
	doc, err := parseNotes([]byte(page), "")
	if err != nil || len(doc.Covers) != 1 {
		t.Fatalf("got %+v, %v", doc, err)
	}
}

// The check end to end: a stable break needs a stable note, an unstable note
// covers only unstable breaks, and a name that covers nothing is a finding.
func TestNotesReport(t *testing.T) {
	repo := twoCommits(t,
		map[string]string{
			"packages/sample/a.go": "package sample\n\n" +
				"func Keep() {}\n\nfunc Gone() {}\n\nfunc Moves(a int) {}\n\nfunc Bare() {}\n\n" +
				"// Unstable: x.\nfunc Flaky(a int) {}\n",
			".api/packages.txt": sampleManifest,
		},
		map[string]string{
			"packages/sample/a.go": "package sample\n\n" +
				"func Keep() {}\n\nfunc Moves(a string) {}\n\n" +
				"// Unstable: x.\nfunc Flaky(a string) {}\n",
		},
	)
	run := func(page string) (bool, string) {
		t.Helper()
		writeFiles(t, repo, map[string]string{"docs/migrating.md": page})
		var out bytes.Buffer
		ok, err := notesReport(repo, filepath.Join(repo, ".api"), filepath.Join(repo, "docs", "migrating.md"), "base", "", &out)
		if err != nil {
			t.Fatal(err)
		}
		return ok, out.String()
	}

	ok, out := run(notesPage)
	if ok {
		t.Fatalf("a missing note passed:\n%s", out)
	}
	for _, want := range []string{
		"stable breaks: 3, 1 without a note",
		"unstable breaks: 1, 1 with an optional note",
		"names that cover no break: 0",
		"packages/sample\n    - Bare (func, removed)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}

	ok, out = run(strings.Replace(notesPage, "`packages/sample.Moves`.", "`packages/sample.Moves`, `packages/sample.Bare`.", 1))
	if !ok || !strings.Contains(out, "every stable break since base has a note") {
		t.Fatalf("full coverage did not pass:\n%s", out)
	}

	// Coverage by package passes too, and the unstable note is optional.
	ok, out = run("## Unreleased\n\n" + noteHead + "Covers: `packages/sample`\n")
	if !ok {
		t.Fatalf("a package-wide note did not cover its breaks:\n%s", out)
	}

	for name, tc := range map[string]struct{ page, want string }{
		"a typo": {
			"## Unreleased\n\n" + noteHead + "Covers: `packages/sample`, `packages/sample.Gonne`\n",
			"migrating.md:7: `packages/sample.Gonne` covers no stable break since base\n",
		},
		"an unstable break noted as stable": {
			"## Unreleased\n\n" + noteHead + "Covers: `packages/sample`, `packages/sample.Flaky`\n",
			"covers no stable break since base; it names an unstable break",
		},
		// An unstable note that names a stable break covers nothing, so the
		// stable break stays uncovered.
		"a stable break noted as unstable": {
			"## Unreleased\n\n" + noteHead + "Covers: `packages/sample.Gone`, `packages/sample.Moves`\n\n" +
				"### Unstable changes (no promise)\n\n#### Bare\n\nA courtesy.\n\nCovers: `packages/sample.Bare`\n",
			"covers no unstable break since base; it names a stable break",
		},
	} {
		ok, out := run(tc.page)
		if ok || !strings.Contains(out, tc.want) {
			t.Errorf("%s: ok=%v, report lacks %q:\n%s", name, ok, tc.want, out)
		}
		if name == "a stable break noted as unstable" && !strings.Contains(out, "- Bare (func, removed)") {
			t.Errorf("%s: the unstable note covered a stable break:\n%s", name, out)
		}
	}
}

// Advisory by default: a finding exits zero. -require makes it fail, and a
// page the parser cannot read fails either way.
func TestNotesExitCodes(t *testing.T) {
	repo := twoCommits(t,
		map[string]string{
			"packages/sample/a.go": "package sample\n\nfunc Keep() {}\n\nfunc Gone() {}\n",
			".api/packages.txt":    sampleManifest,
		},
		map[string]string{"packages/sample/a.go": "package sample\n\nfunc Keep() {}\n"},
	)
	dir := filepath.Join(repo, ".api")
	page := filepath.Join(repo, "migrating.md")
	writeFiles(t, repo, map[string]string{"migrating.md": "## Unreleased\n"})
	if got := snapshotMain(repo, dir, false, false, "base", notesFlags{File: page}); got != 0 {
		t.Errorf("advisory: exit %d, want 0", got)
	}
	if got := snapshotMain(repo, dir, false, false, "base", notesFlags{File: page, Require: true}); got != 1 {
		t.Errorf("-require: exit %d, want 1", got)
	}
	writeFiles(t, repo, map[string]string{"migrating.md": "## Unreleased\n\n" + noteHead + "Covers: `packages/sample.Gone`\n"})
	if got := snapshotMain(repo, dir, false, false, "base", notesFlags{File: page, Require: true}); got != 0 {
		t.Errorf("-require with every break noted: exit %d, want 0", got)
	}
	writeFiles(t, repo, map[string]string{"migrating.md": "# no section\n"})
	if got := snapshotMain(repo, dir, false, false, "base", notesFlags{File: page}); got != 2 {
		t.Errorf("an unreadable page: exit %d, want 2", got)
	}
	if got := snapshotMain(repo, dir, false, true, "", notesFlags{File: page}); got != 2 {
		t.Errorf("-notes without -since: exit %d, want 2", got)
	}
	if got := snapshotMain(repo, dir, false, false, "base", notesFlags{Require: true}); got != 2 {
		t.Errorf("-require without -notes: exit %d, want 2", got)
	}
}

// The check reads Unreleased and every version section newer than the ref,
// since those notes have not published yet. Older sections are history.
func TestParseNotesReadsSealedSectionsNewerThanTheRef(t *testing.T) {
	page := "# M\n\n## Unreleased\n\n" + noteHead + "Covers: `packages/sample.A`\n\n" +
		"## v0.139.0\n\n" + noteHead + "Covers: `packages/sample.B`\n\n" +
		"### Unstable changes (no promise)\n\n#### C\n\nA courtesy.\n\nCovers: `packages/sample.C`\n\n" +
		"## v0.138.2\n\n" + noteHead + "Covers: `packages/sample.Old`\n"
	for ref, want := range map[string]string{
		"pub/v0.138.2": "packages/sample.A stable|packages/sample.B stable|packages/sample.C unstable",
		"pub/v0.139.0": "packages/sample.A stable",
		// A ref that names no version reads Unreleased alone.
		"base": "packages/sample.A stable",
	} {
		doc, err := parseNotes([]byte(page), ref)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		var names []string
		for _, c := range doc.Covers {
			names = append(names, c.Text+" "+c.Class)
		}
		if got := strings.Join(names, "|"); got != want {
			t.Errorf("%s: covers = %q, want %q", ref, got, want)
		}
		if doc.Pending != 1 {
			t.Errorf("%s: pending = %d, want the one note under Unreleased", ref, doc.Pending)
		}
	}
	doc, _ := parseNotes([]byte(page), "pub/v0.138.2")
	if strings.Join(doc.Sealed, ",") != "v0.139.0" {
		t.Errorf("sealed = %q, want v0.139.0", doc.Sealed)
	}
	if _, err := parseNotes([]byte(page+"\n## v0.139.0\n"), "pub/v0.138.2"); err == nil || !strings.Contains(err.Error(), "a second \"## v0.139.0\"") {
		t.Errorf("a repeated unpublished section: err = %v", err)
	}
}

const unsealedPage = "# Migrating\n\nIntro.\n\n```markdown\n## Unreleased\n\nNo notes yet.\n```\n\n" +
	"## Unreleased\n\nNo notes yet.\n\n### First\n\nDo this.\n\nCovers: `packages/sample.Gone`\n\n" +
	"### Second\n\nDo that.\n\nCovers: `packages/sample.Moves`\n\n" +
	"### Unstable changes (no promise)\n\nNo notes yet.\n\n" +
	"## v0.1.0\n\n### Older\n\nText.\n"

const sealedPage = "# Migrating\n\nIntro.\n\n```markdown\n## Unreleased\n\nNo notes yet.\n```\n\n" +
	"## Unreleased\n\nNo notes yet.\n\n### Unstable changes (no promise)\n\nNo notes yet.\n\n" +
	"## v0.2.0\n\n### First\n\nDo this.\n\nCovers: `packages/sample.Gone`\n\n" +
	"### Second\n\nDo that.\n\nCovers: `packages/sample.Moves`\n\n" +
	"## v0.1.0\n\n### Older\n\nText.\n"

// The seal moves the notes under a version heading, drops the placeholders and
// an empty unstable subsection, and opens a fresh Unreleased section. The
// fenced example is left alone.
func TestSealNotes(t *testing.T) {
	got, n, err := sealNotes([]byte(unsealedPage), "v0.2.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || string(got) != sealedPage {
		t.Fatalf("sealed %d note(s):\n%s\nwant 2:\n%s", n, got, sealedPage)
	}
	// Nothing left to seal: the page comes back unchanged, whether the seal
	// repeats for the same version or names another.
	for _, v := range []string{"v0.2.0", "v0.3.0"} {
		again, n, err := sealNotes(got, v, "")
		if err != nil || n != 0 || string(again) != string(got) {
			t.Errorf("a second seal for %s: %d note(s), err %v, changed=%v", v, n, err, string(again) != string(got))
		}
	}
	// An unstable note keeps its subsection.
	withCourtesy := strings.Replace(unsealedPage, "### Unstable changes (no promise)\n\nNo notes yet.\n",
		"### Unstable changes (no promise)\n\n#### Flaky\n\nA courtesy.\n\nCovers: `packages/sample.Flaky`\n", 1)
	got, _, err = sealNotes([]byte(withCourtesy), "v0.2.0", "")
	if err != nil || !strings.Contains(string(got), "Covers: `packages/sample.Moves`\n\n### Unstable changes (no promise)\n\n#### Flaky\n") {
		t.Errorf("the courtesy note lost its subsection (err %v):\n%s", err, got)
	}
	for name, tc := range map[string]struct{ page, version, want string }{
		"new notes for a sealed version": {unsealedPage, "v0.1.0", "already has a \"## v0.1.0\" section, and more notes arrived"},
		"not a version":                  {unsealedPage, "0.2.0", "not a version"},
		"a page it cannot read":          {"# no section\n", "v0.2.0", "no \"## Unreleased\" heading"},
	} {
		if _, _, err := sealNotes([]byte(tc.page), tc.version, ""); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, tc.want)
		}
	}
}

// The cut's form of the check: every note must sit in the section of the
// version being cut. The sealed page passes both forms of the check, and the
// unsealed one passes only the form without -release.
func TestNotesReportForARelease(t *testing.T) {
	repo := twoCommits(t,
		map[string]string{
			"packages/sample/a.go": "package sample\n\nfunc Keep() {}\n\nfunc Gone() {}\n\nfunc Moves(a int) {}\n",
			".api/packages.txt":    sampleManifest,
		},
		map[string]string{"packages/sample/a.go": "package sample\n\nfunc Keep() {}\n\nfunc Moves(a string) {}\n"},
	)
	gitIn(t, repo, "tag", "pub/v0.1.0", "base")
	run := func(page, release string) (bool, string) {
		t.Helper()
		writeFiles(t, repo, map[string]string{"docs/migrating.md": page})
		var out bytes.Buffer
		ok, err := notesReport(repo, filepath.Join(repo, ".api"), filepath.Join(repo, "docs", "migrating.md"), "pub/v0.1.0", release, &out)
		if err != nil {
			t.Fatal(err)
		}
		return ok, out.String()
	}
	if ok, out := run(unsealedPage, ""); !ok {
		t.Errorf("unsealed notes failed the check without -release:\n%s", out)
	}
	if ok, out := run(unsealedPage, "v0.2.0"); ok || !strings.Contains(out, "2 note(s) still under \"## Unreleased\"") {
		t.Errorf("unsealed notes passed the cut (ok=%v):\n%s", ok, out)
	}
	for _, release := range []string{"", "v0.2.0"} {
		if ok, out := run(sealedPage, release); !ok || !strings.Contains(out, "under ## Unreleased and ## v0.2.0") {
			t.Errorf("-release %q: the sealed page failed (ok=%v):\n%s", release, ok, out)
		}
	}
	if ok, out := run(sealedPage, "v0.2.1"); ok || !strings.Contains(out, "\"## v0.2.0\" has no published release and is not v0.2.1") {
		t.Errorf("a section sealed for another version passed (ok=%v):\n%s", ok, out)
	}
	if _, err := notesReport(repo, filepath.Join(repo, ".api"), filepath.Join(repo, "docs", "migrating.md"), "pub/v0.1.0", "0.2.0", &bytes.Buffer{}); err == nil {
		t.Error("-release took a malformed version")
	}
}

// sealMain rewrites the page in place, and heads the section with the count
// of breaks since the ref.
func TestSealMainWritesThePage(t *testing.T) {
	repo := twoCommits(t,
		map[string]string{
			"packages/sample/a.go": "package sample\n\nfunc Keep() {}\n\nfunc Gone() {}\n\nfunc Moves(a int) {}\n\n// Unstable: x.\nfunc Flaky() {}\n",
			".api/packages.txt":    sampleManifest,
		},
		map[string]string{"packages/sample/a.go": "package sample\n\nfunc Keep() {}\n\nfunc Moves(a string) {}\n"},
	)
	gitIn(t, repo, "tag", "pub/v0.1.0", "base")
	dir := filepath.Join(repo, ".api")
	page := filepath.Join(repo, "migrating.md")
	// A missing note: the seal refuses and leaves the page alone.
	writeFiles(t, repo, map[string]string{"migrating.md": strings.Replace(unsealedPage, "\n### Second\n\nDo that.\n\nCovers: `packages/sample.Moves`\n", "", 1)})
	if got := sealMain(repo, dir, "pub/v0.1.0", page, "v0.2.0"); got != 1 {
		t.Errorf("an incomplete page: exit %d, want 1", got)
	}
	if got, _ := os.ReadFile(page); strings.Contains(string(got), "## v0.2.0") {
		t.Errorf("an incomplete page was sealed:\n%s", got)
	}
	writeFiles(t, repo, map[string]string{"migrating.md": unsealedPage})
	if got := sealMain(repo, dir, "pub/v0.1.0", page, "v0.2.0"); got != 0 {
		t.Fatalf("exit %d", got)
	}
	count := "Since v0.1.0: 2 stable break(s) (1 removed, 1 changed), each with a note below, and 1 unstable break(s)."
	want := strings.Replace(sealedPage, "## v0.2.0\n\n", "## v0.2.0\n\n"+count+"\n\n", 1)
	if got, _ := os.ReadFile(page); string(got) != want {
		t.Fatalf("page after the seal:\n%s\nwant:\n%s", got, want)
	}
	if got := sealMain(repo, dir, "pub/v0.1.0", page, "v0.2.0"); got != 0 {
		t.Errorf("a repeated seal: exit %d, want 0", got)
	}
	if got, _ := os.ReadFile(page); string(got) != want {
		t.Errorf("a repeated seal changed the page:\n%s", got)
	}
}

func TestBreakCount(t *testing.T) {
	if got, want := breakCount([]breakage{{Class: classUnstable}}, "base"), "Since base: no stable break, and 1 unstable break(s)."; got != want {
		t.Errorf("count = %q, want %q", got, want)
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
