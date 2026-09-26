package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// snapRepo writes files (paths relative to the root) into a fresh directory,
// with a go.mod for module example.com/m unless files brings its own. The
// snapshot check reads the module path for its closure rule.
func snapRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := testsupport.TempDir(t)
	if _, ok := files["go.mod"]; !ok {
		writeFiles(t, dir, map[string]string{"go.mod": "module example.com/m\n"})
	}
	writeFiles(t, dir, files)
	return dir
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A package and its subpackage are two namespaces. Keyed by name alone, a New
// in the subpackage could stand in for a New removed from the parent; the
// release census once did exactly that (TestReleaseCensusKeepsSubpackagesApart).
func TestSnapshotCensusKeepsSubpackagesApart(t *testing.T) {
	repo := snapRepo(t, map[string]string{
		"packages/sample/a.go":     "package sample\n\nfunc New() {}\n",
		"packages/sample/sub/b.go": "package sub\n\nfunc New(x int) {}\n\nfunc OnlySub() {}\n",
	})
	parent, err := censusPkg(repo, "packages/sample")
	if err != nil {
		t.Fatal(err)
	}
	if got := parent["New"].Sig; got != "func()" {
		t.Errorf("parent New = %q, want the parent's own func()", got)
	}
	if _, ok := parent["OnlySub"]; ok {
		t.Error("the parent's census holds a subpackage symbol")
	}
	sub, err := censusPkg(repo, "packages/sample/sub")
	if err != nil {
		t.Fatal(err)
	}
	if got := sub["New"].Sig; got != "func(x int)" {
		t.Errorf("sub New = %q, want func(x int)", got)
	}
}

const sampleManifest = "# a comment\nstable packages/sample\n"

// Write, then check: clean. Change the code, and check names the change and
// fails. Write again: clean.
func TestSnapshotCheckFailsUntilRewritten(t *testing.T) {
	repo := snapRepo(t, map[string]string{
		"packages/sample/a.go": "package sample\n\nconst Limit = 3\n\nfunc New() {}\n\nfunc Old() {}\n",
		".api/packages.txt":    sampleManifest,
	})
	dir := filepath.Join(repo, ".api")
	var out bytes.Buffer
	if err := writeSnapshots(repo, dir, &out); err != nil {
		t.Fatal(err)
	}
	snap, err := os.ReadFile(filepath.Join(dir, "packages", "sample.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(snap), "\n") {
		if strings.HasSuffix(line, "\t") || strings.HasSuffix(line, " ") {
			t.Errorf("snapshot line ends in whitespace: %q", line)
		}
	}
	out.Reset()
	if ok, err := checkSnapshots(repo, dir, &out); err != nil || !ok {
		t.Fatalf("check right after write: ok=%v err=%v\n%s", ok, err, out.String())
	}
	if !strings.Contains(out.String(), "1 packages match their snapshots") {
		t.Errorf("a clean check printed nothing to prove it ran:\n%s", out.String())
	}

	writeFiles(t, repo, map[string]string{
		"packages/sample/a.go": "package sample\n\nconst Limit = 3\n\nfunc New(n int) {}\n\nfunc Added() {}\n",
	})
	out.Reset()
	ok, err := checkSnapshots(repo, dir, &out)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("check passed on a changed API")
	}
	for _, want := range []string{"- Old (func)", "~ New: func() -> func(n int)", "+ Added (func)", "just api-snapshot"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("check output lacks %q:\n%s", want, out.String())
		}
	}

	if err := writeSnapshots(repo, dir, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if ok, err := checkSnapshots(repo, dir, &out); err != nil || !ok {
		t.Fatalf("check after rewrite: ok=%v err=%v\n%s", ok, err, out.String())
	}
}

// Moving a package between classes changes its snapshot's header, so the
// check fails even though no symbol moved.
func TestSnapshotCheckCatchesAClassChange(t *testing.T) {
	repo := snapRepo(t, map[string]string{
		"packages/sample/a.go": "package sample\n\nfunc New() {}\n",
		".api/packages.txt":    sampleManifest,
	})
	dir := filepath.Join(repo, ".api")
	var out bytes.Buffer
	if err := writeSnapshots(repo, dir, &out); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, repo, map[string]string{".api/packages.txt": "unstable packages/sample\n"})
	out.Reset()
	ok, err := checkSnapshots(repo, dir, &out)
	if err != nil {
		t.Fatal(err)
	}
	if ok || !strings.Contains(out.String(), "the header or the format differs") {
		t.Errorf("a class change went unnoticed (ok=%v):\n%s", ok, out.String())
	}
}

// A snapshot no manifest entry owns fails the check, and write removes it.
func TestSnapshotOrphansFailTheCheck(t *testing.T) {
	repo := snapRepo(t, map[string]string{
		"packages/sample/a.go": "package sample\n\nfunc New() {}\n",
		".api/packages.txt":    sampleManifest,
	})
	dir := filepath.Join(repo, ".api")
	var out bytes.Buffer
	if err := writeSnapshots(repo, dir, &out); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(dir, "packages", "gone.txt")
	writeFiles(t, repo, map[string]string{".api/packages/gone.txt": "# gone\n"})
	out.Reset()
	ok, err := checkSnapshots(repo, dir, &out)
	if err != nil {
		t.Fatal(err)
	}
	if ok || !strings.Contains(out.String(), "no manifest entry owns") {
		t.Errorf("an orphan snapshot went unnoticed (ok=%v):\n%s", ok, out.String())
	}
	if err := writeSnapshots(repo, dir, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("write left the orphan in place: %v", err)
	}
}

// A package under a listed one must be listed too, or it joins the API with
// nothing measuring it. exp, internal and testdata stay out.
func TestSnapshotCheckWantsEveryPackageUnderAListedOne(t *testing.T) {
	repo := snapRepo(t, map[string]string{
		"packages/sample/a.go":                "package sample\n\nfunc New() {}\n",
		"packages/sample/exp/e.go":            "package exp\n\nfunc Try() {}\n",
		"packages/sample/internal/i.go":       "package internal\n\nfunc Hidden() {}\n",
		"packages/sample/testdata/t.go":       "package testdata\n\nfunc Fixture() {}\n",
		"packages/sample/tested/only_test.go": "package tested\n",
		".api/packages.txt":                   sampleManifest,
	})
	dir := filepath.Join(repo, ".api")
	var out bytes.Buffer
	if err := writeSnapshots(repo, dir, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if ok, err := checkSnapshots(repo, dir, &out); err != nil || !ok {
		t.Fatalf("exp, internal, testdata or a test-only directory counted as a package: ok=%v err=%v\n%s", ok, err, out.String())
	}

	writeFiles(t, repo, map[string]string{"packages/sample/fresh/f.go": "package fresh\n\nfunc Born() {}\n"})
	out.Reset()
	ok, err := checkSnapshots(repo, dir, &out)
	if err != nil {
		t.Fatal(err)
	}
	if ok || !strings.Contains(out.String(), "packages/sample/fresh: not in packages.txt") {
		t.Errorf("an unlisted package went unnoticed (ok=%v):\n%s", ok, out.String())
	}
}

// -since counts per class against a ref, and reports a package the ref did
// not have as new.
func TestSinceCountsPerClass(t *testing.T) {
	repo := apiFixture(t,
		map[string]string{"a.go": "package sample\n\nfunc Keep() {}\n\nfunc Gone() {}\n\nfunc Moves(a int) {}\n"},
		map[string]string{
			"a.go":     "package sample\n\nfunc Keep() {}\n\nfunc Moves(a string) {}\n\nfunc Fresh() {}\n",
			"sub/b.go": "package sub\n\nfunc Born() {}\n",
		},
	)
	writeFiles(t, repo, map[string]string{
		".api/packages.txt": "stable packages/sample\nunstable packages/sample/sub\n",
	})
	var out bytes.Buffer
	if err := sinceReport(repo, filepath.Join(repo, ".api"), "base", &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"packages/sample (stable): 1 removed, 1 changed, 1 added",
		"packages/sample/sub (unstable, new since base): 0 removed, 0 changed, 1 added",
		"stable: 1 package(s), 2 break(s) (1 removed, 1 changed), 1 added",
		"unstable: 1 package(s), 0 break(s) (0 removed, 0 changed), 1 added",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("since report lacks %q:\n%s", want, out.String())
		}
	}
}

func TestManifestRefusesBadLines(t *testing.T) {
	for name, body := range map[string]string{
		"an unknown class": "frozen packages/sample\n",
		"a duplicate":      "stable packages/sample\nunstable packages/sample\n",
		"nothing listed":   "# only a comment\n",
		"a missing field":  "stable\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := snapRepo(t, map[string]string{"packages.txt": body})
			if _, err := readManifest(dir); err == nil {
				t.Errorf("readManifest accepted %q", body)
			}
		})
	}
}

// A package deleted since the ref still counts: its symbols are removals, in
// the class of the listed package that claimed it.
func TestSinceCountsAPackageGoneSinceTheRef(t *testing.T) {
	repo := apiFixture(t,
		map[string]string{
			"a.go":      "package sample\n\nfunc Keep() {}\n",
			"gone/g.go": "package gone\n\nfunc One() {}\n\nfunc Two() {}\n",
		},
		map[string]string{"a.go": "package sample\n\nfunc Keep() {}\n"},
	)
	writeFiles(t, repo, map[string]string{".api/packages.txt": sampleManifest})
	var out bytes.Buffer
	if err := sinceReport(repo, filepath.Join(repo, ".api"), "base", &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"packages/sample/gone (stable, gone since base): 2 removed",
		"- One (func)",
		"stable: 2 package(s), 2 break(s) (2 removed, 0 changed), 0 added",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("since report lacks %q:\n%s", want, out.String())
		}
	}
}

// A var or const records its declared type where the syntax shows it, so a
// type change reaches the snapshot. The value never does.
func TestCensusRecordsDeclaredValueTypes(t *testing.T) {
	syms, err := census(map[string][]byte{"p/a.go": []byte(`package p

type T struct{}
type Mode int

var Explicit []T
var Literal = T{}
var Pointer = &T{}
var Hook = func(n int) error { return nil }
var Called = make([]T, 0)
var ErrGone = errors.New("gone")

const (
	First Mode = iota
	Second
)

const Untyped = 3
const Typed Mode = 7
const Named = Untyped

const (
	Bit = 1 << iota
	NextBit
)

const Left, Right = 1, "r"

const (
	Num, Text = iota, "t"
	NumNext, TextNext
)

const (
	Ratio = 0.5
	Word  = "w"
	On    = !false
	Typed2 = !bool(true)
	Both  = true && false
	Less  = Ratio < 1
	Mixed = 2 * 1.5
)

type Box[T any] struct{ V T }
type Pair[K comparable, V any] map[K]V

func Map[T any](x T) T { return x }
`)})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"Explicit": "[]T",
		"Literal":  "T",
		"Pointer":  "*T",
		"Hook":     "func(n int) error",
		"Called":   "",
		"ErrGone":  "error",
		"First":    "Mode",
		"Second":   "Mode",
		"Untyped":  "untyped int",
		"Typed":    "Mode",
		"Named":    "",
		"Bit":      "untyped int",
		"NextBit":  "untyped int",
		"Ratio":    "untyped float",
		"Word":     "untyped string",
		"On":       "untyped bool",
		"Typed2":   "",
		"Both":     "untyped bool",
		"Less":     "untyped bool",
		"Mixed":    "untyped float",
		"Left":     "untyped int",
		"Right":    "untyped string",
		"Num":      "untyped int",
		"Text":     "untyped string",
		"NumNext":  "untyped int",
		"TextNext": "untyped string",
		"Box":      "[T any] struct",
		"Pair":     "[K comparable, V any] map[K]V",
		"Map":      "func[T any](x T) T",
	} {
		if got := syms[name].Sig; got != want {
			t.Errorf("%s: sig %q, want %q", name, got, want)
		}
	}
}

// twoCommits commits base and tags it "base", then applies head on top, where
// an empty body deletes the file, and commits again.
func twoCommits(t *testing.T, base, head map[string]string) string {
	t.Helper()
	repo := snapRepo(t, base)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	git("tag", "base")
	for name, body := range head {
		if body == "" {
			if err := os.RemoveAll(filepath.Join(repo, filepath.FromSlash(name))); err != nil {
				t.Fatal(err)
			}
			continue
		}
		writeFiles(t, repo, map[string]string{name: body})
	}
	git("add", "-A")
	git("commit", "-q", "-m", "head")
	return repo
}

// When the ref has a manifest, a deleted package keeps the class it had there:
// a top-level entry is still found once no current entry claims it, and an
// unstable package under a stable one does not count as a stable break.
func TestSinceReadsTheManifestAtTheRef(t *testing.T) {
	repo := twoCommits(t,
		map[string]string{
			"packages/keep/k.go":       "package keep\n\nfunc Keep() {}\n",
			"packages/keep/flaky/f.go": "package flaky\n\nfunc Flaky() {}\n",
			"packages/wire/w.go":       "package wire\n\nfunc Dial() {}\n\nfunc Close() {}\n",
			".api/packages.txt":        "stable packages/keep\nunstable packages/keep/flaky\nstable packages/wire\n",
		},
		map[string]string{
			"packages/keep/flaky": "",
			"packages/wire":       "",
			".api/packages.txt":   "stable packages/keep\n",
		},
	)
	var out bytes.Buffer
	if err := sinceReport(repo, filepath.Join(repo, ".api"), "base", &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"packages/wire (stable, gone since base): 2 removed",
		"packages/keep/flaky (unstable, gone since base): 1 removed",
		"stable: 2 package(s), 2 break(s) (2 removed, 0 changed), 0 added",
		"unstable: 1 package(s), 1 break(s) (1 removed, 0 changed), 0 added",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("since report lacks %q:\n%s", want, out.String())
		}
	}
}

// An exported var whose type the census cannot see fails the check, because
// a change to its type would reach no snapshot.
func TestSnapshotCheckRefusesAnUntypedVar(t *testing.T) {
	repo := snapRepo(t, map[string]string{
		"packages/sample/a.go": "package sample\n\nfunc pick() int { return 1 }\n\nvar Picked = pick()\n",
		".api/packages.txt":    sampleManifest,
	})
	dir := filepath.Join(repo, ".api")
	var out bytes.Buffer
	if err := writeSnapshots(repo, dir, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	ok, err := checkSnapshots(repo, dir, &out)
	if err != nil {
		t.Fatal(err)
	}
	if ok || !strings.Contains(out.String(), "var Picked does not spell its type") {
		t.Errorf("an untyped var passed the check (ok=%v):\n%s", ok, out.String())
	}
	writeFiles(t, repo, map[string]string{
		"packages/sample/a.go": "package sample\n\nfunc pick() int { return 1 }\n\nvar Picked int = pick()\n",
	})
	if err := writeSnapshots(repo, dir, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if ok, err := checkSnapshots(repo, dir, &out); err != nil || !ok {
		t.Errorf("a spelled-out type still failed: ok=%v err=%v\n%s", ok, err, out.String())
	}
}

// A var whose type became visible since the ref is not a break. The ref may
// predate the rule that every var spells its type.
func TestSinceDoesNotCountATypeThatBecameVisible(t *testing.T) {
	repo := apiFixture(t,
		map[string]string{"a.go": "package sample\n\nimport \"time\"\n\nfunc pick() int { return 1 }\n\nvar Picked = pick()\n\nvar Moved int\n\nconst Wait = 5 * time.Second\n"},
		map[string]string{"a.go": "package sample\n\nimport \"time\"\n\nfunc pick() int { return 1 }\n\nvar Picked int = pick()\n\nvar Moved string\n\nvar Wait time.Duration = 5 * time.Second\n"},
	)
	writeFiles(t, repo, map[string]string{".api/packages.txt": sampleManifest})
	var out bytes.Buffer
	if err := sinceReport(repo, filepath.Join(repo, ".api"), "base", &out); err != nil {
		t.Fatal(err)
	}
	// Wait went from a const of unknown kind to a var: a break, even though
	// its old signature was unknown too.
	if !strings.Contains(out.String(), "packages/sample (stable): 0 removed, 2 changed, 0 added") ||
		!strings.Contains(out.String(), "~ Moved: int -> string") ||
		!strings.Contains(out.String(), "~ Wait: const  -> var time.Duration") {
		t.Errorf("want Moved and Wait counted as changes, and Picked not:\n%s", out.String())
	}
}

// A package whose Go files are gone is gone, even when its directory keeps a
// README. A package that changed class counts its breaks in the class it had
// at the ref, because that is the class a host relied on. An addition had no
// class there, so it counts in the class it has now.
func TestSinceCountsByTheRefsFacts(t *testing.T) {
	repo := twoCommits(t,
		map[string]string{
			"packages/keep/k.go":          "package keep\n\nfunc Keep() {}\n",
			"packages/keep/doc/d.go":      "package doc\n\nfunc Doc() {}\n",
			"packages/keep/doc/README.md": "notes\n",
			"packages/keep/flip/f.go":     "package flip\n\nfunc Stay() {}\n\nfunc Drop() {}\n",
			".api/packages.txt":           "stable packages/keep\nstable packages/keep/doc\nstable packages/keep/flip\n",
		},
		map[string]string{
			"packages/keep/doc/d.go":  "",
			"packages/keep/flip/f.go": "package flip\n\nfunc Stay() {}\n\nfunc Born() {}\n",
			".api/packages.txt":       "stable packages/keep\nunstable packages/keep/flip\n",
		},
	)
	var out bytes.Buffer
	if err := sinceReport(repo, filepath.Join(repo, ".api"), "base", &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"packages/keep/doc (stable, gone since base): 1 removed",
		"packages/keep/flip (unstable, stable at base and counted so): 1 removed",
		"stable: 3 package(s), 2 break(s) (2 removed, 0 changed), 0 added",
		// Born has no class at base, so it counts in the class it has now.
		"unstable: 1 package(s), 0 break(s) (0 removed, 0 changed), 1 added",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("since report lacks %q:\n%s", want, out.String())
		}
	}
}

// The release census once merged a package with its subpackages by name, so a
// New removed from the root hid behind the subpackage's New.
func TestReleaseCensusKeepsSubpackagesApart(t *testing.T) {
	repo := apiFixture(t,
		map[string]string{
			"a.go":     "package sample\n\nfunc New() {}\n\nfunc Keep() {}\n",
			"sub/b.go": "package sub\n\nfunc New(x int) {}\n",
		},
		map[string]string{
			"a.go":     "package sample\n\nfunc Keep() {}\n",
			"sub/b.go": "package sub\n\nfunc New(x int) {}\n",
		},
	)
	r, err := comparePkg(repo, "packages/sample", "base", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Removed) != 1 || r.Removed[0].Name != "New" {
		t.Errorf("removed = %+v, want the root's New", r.Removed)
	}
	if len(r.Changed) != 0 || len(r.Added) != 0 {
		t.Errorf("changed = %+v, added = %+v, want none: sub.New did not move", r.Changed, r.Added)
	}
}

// The release census of a root and the per-directory counts the snapshot mode
// makes agree, because both use one diff.
func TestReleaseCensusAgreesWithThePerPackageCount(t *testing.T) {
	repo := apiFixture(t,
		map[string]string{
			"a.go":     "package sample\n\nfunc New() {}\n\nfunc Moves(a int) {}\n",
			"sub/b.go": "package sub\n\nfunc New(x int) {}\n\nfunc Gone() {}\n",
		},
		map[string]string{
			"a.go":     "package sample\n\nfunc Moves(a string) {}\n\nfunc Fresh() {}\n",
			"sub/b.go": "package sub\n\nfunc New(x string) {}\n",
			"c/c.go":   "package c\n\nfunc Born() {}\n",
		},
	)
	r, err := comparePkg(repo, "packages/sample", "base", "")
	if err != nil {
		t.Fatal(err)
	}
	var removed, changed, added int
	for _, pkg := range []string{"packages/sample", "packages/sample/sub", "packages/sample/c"} {
		baseFiles, err := dirFilesAtRef(repo, "base", pkg)
		if err != nil {
			t.Fatal(err)
		}
		base, err := census(baseFiles)
		if err != nil {
			t.Fatal(err)
		}
		head, err := censusPkg(repo, pkg)
		if err != nil {
			t.Fatal(err)
		}
		d := diffSymbols(pkg, base, head)
		removed, changed, added = removed+len(d.Removed), changed+len(d.Changed), added+len(d.Added)
	}
	if len(r.Removed) != removed || len(r.Changed) != changed || len(r.Added) != added {
		t.Errorf("release census %d removed, %d changed, %d added; per package %d, %d, %d",
			len(r.Removed), len(r.Changed), len(r.Added), removed, changed, added)
	}
	if removed != 2 || changed != 2 || added != 2 {
		t.Errorf("per-package count %d, %d, %d; want 2, 2, 2 (New and sub.Gone; Moves and sub.New; Fresh and c.Born)", removed, changed, added)
	}
}

// A directory whose name holds a dot must not merge with a method of the same
// printed name: a.B's func New and the method New on type B in a both print
// as a.B.New, and they are two symbols.
func TestReleaseCensusKeysDirectoryAndNameApart(t *testing.T) {
	repo := apiFixture(t,
		map[string]string{
			"a.go":     "package sample\n\nfunc Keep() {}\n",
			"a.B/b.go": "package b\n\nfunc New() {}\n",
		},
		map[string]string{
			"a.go":   "package sample\n\nfunc Keep() {}\n",
			"a/a.go": "package a\n\ntype B struct{}\n\nfunc (B) New() {}\n",
		},
	)
	r, err := comparePkg(repo, "packages/sample", "base", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Removed) != 1 || r.Removed[0].Name != "a.B.New" || r.Removed[0].Kind != "func" {
		t.Errorf("removed = %+v, want a.B's func New", r.Removed)
	}
	if len(r.Changed) != 0 {
		t.Errorf("changed = %+v, want none: a removed func and a new method are not one symbol", r.Changed)
	}
}
