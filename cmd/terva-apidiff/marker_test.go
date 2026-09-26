package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An "Unstable:" paragraph marks the symbol whose doc holds it. A type's
// marker covers its fields and methods, a group's doc covers its specs, and
// the marker counts only where a paragraph opens.
func TestCensusReadsTheUnstableMarker(t *testing.T) {
	syms, err := census(map[string][]byte{
		"p/a.go": []byte(`package p

// Loose is not promised.
//
// Unstable: a per-vendor constructor.
func Loose() {}

// Firm mentions the word. See the Unstable: rule, which it does not follow.
func Firm() {}

// Quoted shows the marker in an indented block:
//
//	Unstable: an example, in a code block.
func Quoted() {}

// Wrapped mentions the word where a line wraps, and a wrapped line is not a
// Unstable: paragraph of its own.
func Wrapped() {}

// Rough is a type out of the promise.
//
// Unstable: the shape still moves.
type Rough struct {
	Field int
}

type Iface interface {
	// Unstable: a new hook.
	Hook()
	Kept()
}

type Solid struct {
	// Unstable: a knob.
	Knob int
	Size int
}

// Unstable: this method alone.
func (Solid) Tune() {}

func (Solid) Plain() {}

// Unstable: the group's doc covers every spec in it.
const (
	GroupA = 1
	GroupB = 2
)

const (
	// Unstable: this spec only.
	SpecA = 1
	SpecB = 2
)

// Unstable: a single spec's decl doc.
var Knobbed int

// Unstable: godoc shows each type of a group apart, without this doc.
type (
	// TypeOwn has its own doc.
	TypeOwn int
	TypeBare int
)

// Unstable: a group of one type is that type's doc.
type (
	TypeSolo int
)
`),
		"p/b.go": []byte("package p\n\nfunc (Rough) Method() {}\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"Loose": true, "Firm": false, "Wrapped": false, "Quoted": false,
		"Rough": true, "Rough.Field": true, "Rough.Method": true,
		"Iface": false, "Iface.Hook": true, "Iface.Kept": false,
		"Solid": false, "Solid.Knob": true, "Solid.Size": false, "Solid.Tune": true, "Solid.Plain": false,
		"GroupA": true, "GroupB": true, "SpecA": true, "SpecB": false,
		"Knobbed": true,
		"TypeOwn": false, "TypeBare": false, "TypeSolo": true,
	} {
		s, ok := syms[name]
		if !ok {
			t.Errorf("%s: not in the census", name)
			continue
		}
		if s.Unstable != want {
			t.Errorf("%s: unstable = %v, want %v", name, s.Unstable, want)
		}
	}
}

// A comment inside an anonymous struct or interface is no part of the
// signature. The census parses comments for the marker, and the printer
// would print these ones, so render sets them aside.
func TestSignaturesCarryNoComments(t *testing.T) {
	syms, err := census(map[string][]byte{"p/a.go": []byte(`package p

type T struct {
	F struct {
		// Doc on A.
		A int // trailing
	}
}

func G(o interface {
	// M does.
	M()
}) {
}
`)})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"T.F": "struct { A int }",
		"G":   "func(o interface { M() })",
	} {
		if got := syms[name].Sig; got != want {
			t.Errorf("%s: sig %q, want %q", name, got, want)
		}
	}
}

// render leaves the tree as it found it: a later read of a field's doc, for
// the marker, still finds it.
func TestRenderPutsTheCommentsBack(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "a.go", "package p\n\nvar V struct {\n\t// Unstable: x.\n\tA int // y\n}\n", parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	typ := file.Decls[0].(*ast.GenDecl).Specs[0].(*ast.ValueSpec).Type
	if got := render(fset, typ); got != "struct { A int }" {
		t.Errorf("render = %q", got)
	}
	field := typ.(*ast.StructType).Fields.List[0]
	if !marked(field.Doc) || field.Comment == nil {
		t.Errorf("render lost the field's comments: doc %v, comment %v", field.Doc, field.Comment)
	}
}

// The snapshot writes the marker after the kind, reads it back, and names a
// marker that moved when the check fails.
func TestSnapshotRecordsTheMarkerInTheKind(t *testing.T) {
	repo := snapRepo(t, map[string]string{
		"packages/sample/a.go": "package sample\n\n// Unstable: not yet.\nfunc New() {}\n\nconst Limit = 3\n\nfunc Old() {}\n",
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
	for _, want := range []string{"New\tfunc unstable\tfunc()\n", "Old\tfunc\tfunc()\n"} {
		if !strings.Contains(string(snap), want) {
			t.Errorf("snapshot lacks %q:\n%s", want, snap)
		}
	}
	parsed, err := parseSnapshot(snap)
	if err != nil {
		t.Fatal(err)
	}
	if s := parsed["New"]; s.Kind != "func" || !s.Unstable {
		t.Errorf("parsed New = %+v, want kind func and unstable", s)
	}
	if s := parsed["Old"]; s.Kind != "func" || s.Unstable {
		t.Errorf("parsed Old = %+v, want kind func and stable", s)
	}

	writeFiles(t, repo, map[string]string{
		"packages/sample/a.go": "package sample\n\nfunc New() {}\n\nconst Limit = 3\n\n// Unstable: now.\nfunc Old() {}\n",
	})
	out.Reset()
	ok, err := checkSnapshots(repo, dir, &out)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("check passed after two markers moved")
	}
	for _, want := range []string{"! New: no longer marked Unstable:", "! Old: marked Unstable:"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("check output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "the symbols match") {
		t.Errorf("a marker change read as a format change:\n%s", out.String())
	}
}

// Each symbol counts in the class it had at the ref. A marker added since
// then does not hide a break, and a symbol marked at the ref breaks in the
// unstable count.
func TestSinceCountsEachSymbolInItsClassAtTheRef(t *testing.T) {
	repo := twoCommits(t,
		map[string]string{
			"packages/sample/a.go": "package sample\n\n" +
				"// Unstable: x.\nfunc Flaky(a int) {}\n\n" +
				"func Solid(a int) {}\n\n" +
				"// Unstable: x.\nfunc Dropped() {}\n\n" +
				"func Marked(a int) {}\n",
			".api/packages.txt": sampleManifest,
		},
		map[string]string{
			"packages/sample/a.go": "package sample\n\n" +
				"// Unstable: x.\nfunc Flaky(a string) {}\n\n" +
				"func Solid(a string) {}\n\n" +
				"// Unstable: x.\nfunc Marked(a string) {}\n\n" +
				"// Unstable: x.\nfunc Fresh() {}\n",
		},
	)
	var out bytes.Buffer
	if err := sinceReport(repo, filepath.Join(repo, ".api"), "base", &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"packages/sample (stable): 1 removed, 3 changed, 1 added (4 symbols, 3 marked unstable, 2 at base; 2 of the breaks to marked symbols)",
		"~ Solid: func(a int) -> func(a string)",
		"~ Marked: func(a int) -> func(a string)",
		"stable: 1 package(s), 2 break(s) (0 removed, 2 changed), 0 added",
		"unstable: 1 package(s), 2 break(s) (1 removed, 1 changed), 1 added",
		"marked unstable in the stable packages: 3 symbol(s), 2 at base",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("since report lacks %q:\n%s", want, got)
		}
	}
	// Only the stable breaks are listed one by one.
	for _, gone := range []string{"Flaky", "Dropped"} {
		if strings.Contains(got, gone) {
			t.Errorf("since report lists %s, whose break is unstable:\n%s", gone, got)
		}
	}
}

// The marker totals count the packages that were stable when each was
// taken. A package that became stable adds to the count now and not to the
// count at the ref, and one that left the stable class the other way round.
func TestSinceCountsMarkersInThePackagesStableAtEachEnd(t *testing.T) {
	files := map[string]string{
		"packages/up/u.go":   "package up\n\n// Unstable: x.\nfunc A() {}\n\nfunc B() {}\n",
		"packages/down/d.go": "package down\n\n// Unstable: x.\nfunc C() {}\n\n// Unstable: x.\nfunc D() {}\n",
	}
	base := map[string]string{".api/packages.txt": "unstable packages/up\nstable packages/down\n"}
	for k, v := range files {
		base[k] = v
	}
	repo := twoCommits(t, base, map[string]string{
		".api/packages.txt": "stable packages/up\nunstable packages/down\n",
		"packages/up/u.go":  "package up\n\n// Unstable: x.\nfunc A() {}\n\nfunc B(n int) {}\n",
	})
	var out bytes.Buffer
	if err := sinceReport(repo, filepath.Join(repo, ".api"), "base", &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"marked unstable in the stable packages: 1 symbol(s), 2 at base",
		// B broke while up was unstable, and no marker had a hand in it.
		"packages/up (stable, unstable at base and counted so): 0 removed, 1 changed, 0 added (2 symbols, 1 marked unstable, 1 at base)\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("since report lacks %q:\n%s", want, out.String())
		}
	}
}

// A stable symbol that names an unstable one fails the check: across
// packages, by the class the manifest gives the package or by a marker, and
// inside one package. A marked symbol, a type parameter and a name outside
// the module pass.
func TestSnapshotCheckRefusesAStableSymbolThatNamesAnUnstableOne(t *testing.T) {
	repo := snapRepo(t, map[string]string{
		"packages/a/a.go": `package a

import (
	"strings"

	"example.com/m/packages/b"
	cc "example.com/m/packages/c"
	"example.com/m/packages/d"
)

// Unstable: its shape still moves.
type Shaky struct{ N int }

func (s Shaky) Method() {}

func UsesB(x b.T)              {}
func UsesMarked(x cc.Rough)    {}
func UsesSolid(x cc.Solid)     {}
func UsesD() d.T               { return 0 }
func UsesShaky(s *Shaky)       {}
func UsesMissing() cc.Gone     { return 0 }
func UsesNowhere(x Nowhere)    {}
func UsesBuilder(x strings.Builder) {}

// Unstable: it takes an unstable type, and says so.
func Exempt(x b.T) {}

func Map[Shaky any](x Shaky) Shaky { return x }

type Holder struct{ Field b.T }

type Wrap struct{ b.T }

type inner struct{ X b.T }

type Hides struct{ *inner }

type impl struct{}

func (impl) Get() b.T { return 0 }

func NewImpl() *impl { return nil }

type opaque struct{ n b.T }

func Opaque() opaque { return opaque{} }

type loop struct{ Next *loop }

func Loop() loop { return loop{} }

const maxN = 4

func Sized() [maxN]b.T { return [maxN]b.T{} }

func Mistyped() missing { return nil }

type Box[K comparable, V any] struct{}

func (x *Box[K, V]) Get(k K) V { var v V; return v }

var Bad bogus.T
`,
		"packages/b/b.go":   "package b\n\ntype T int\n",
		"packages/c/c.go":   "package c\n\n// Unstable: x.\ntype Rough int\n\ntype Solid int\n",
		"packages/d/d.go":   "package d\n\ntype T int\n",
		".api/packages.txt": "stable packages/a\nunstable packages/b\nstable packages/c\n",
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
	got := out.String()
	if ok {
		t.Fatalf("the check passed a stable symbol that names an unstable one:\n%s", got)
	}
	for _, want := range []string{
		"packages/a: UsesB (func) names b.T, and packages/b is listed unstable",
		"packages/a: UsesMarked (func) names cc.Rough, which is marked Unstable:",
		"packages/a: UsesD (func) names d.T, and packages/d is not in packages.txt",
		"packages/a: UsesShaky (func) names Shaky, which is marked Unstable:",
		"packages/a: UsesMissing (func) names cc.Gone, which packages/c does not export",
		"packages/a: UsesNowhere (func) names Nowhere, which packages/a does not export",
		"packages/a: Holder.Field (field) names b.T, and packages/b is listed unstable",
		"packages/a: Wrap (type) names b.T, and packages/b is listed unstable",
		"packages/a: Bad (var) names bogus.T, and no import of its file is called bogus",
		"packages/a: Hides (type) names b.T through inner, and packages/b is listed unstable",
		"packages/a: NewImpl (func) names b.T through impl, and packages/b is listed unstable",
		"packages/a: Sized (func) names b.T, and packages/b is listed unstable",
		"packages/a: Mistyped (func) names missing, which packages/a does not export",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("check output lacks %q:\n%s", want, got)
		}
	}
	for _, clean := range []string{"UsesSolid", "UsesBuilder", "Exempt", "Map", "Box.Get", "Shaky.Method", "Opaque", "Loop"} {
		if strings.Contains(got, "packages/a: "+clean+" ") {
			t.Errorf("%s keeps the promise, and the check refused it:\n%s", clean, got)
		}
	}
	if strings.Contains(got, "names maxN") {
		t.Errorf("an array length read as a type:\n%s", got)
	}
	if strings.Contains(got, "differs from its committed record") {
		t.Errorf("the snapshots match, and the check said they differ:\n%s", got)
	}
}

// A clean check counts the references it followed, so a pass over nothing
// reads differently from a pass.
func TestSnapshotCheckCountsTheReferencesItFollowed(t *testing.T) {
	repo := snapRepo(t, map[string]string{
		"packages/sample/a.go": "package sample\n\ntype T int\n\nfunc New(t T) T { return t }\n",
		".api/packages.txt":    sampleManifest,
	})
	dir := filepath.Join(repo, ".api")
	var out bytes.Buffer
	if err := writeSnapshots(repo, dir, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if ok, err := checkSnapshots(repo, dir, &out); err != nil || !ok {
		t.Fatalf("check: ok=%v err=%v\n%s", ok, err, out.String())
	}
	if !strings.Contains(out.String(), "no stable signature names an unstable symbol (2 references followed)") {
		t.Errorf("a clean check did not count what it followed:\n%s", out.String())
	}
}

// A comment after the module directive is not part of the path. Kept, it
// would match no import, and the check would pass on nothing.
func TestSnapshotCheckReadsAModuleLineWithAComment(t *testing.T) {
	repo := snapRepo(t, map[string]string{
		"go.mod":            "// the module\nmodule example.com/m // a note\n",
		"packages/a/a.go":   "package a\n\nimport \"example.com/m/packages/b\"\n\nfunc UsesB(x b.T) {}\n",
		"packages/b/b.go":   "package b\n\ntype T int\n",
		".api/packages.txt": "stable packages/a\nunstable packages/b\n",
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
	if ok || !strings.Contains(out.String(), "UsesB (func) names b.T, and packages/b is listed unstable") {
		t.Errorf("the module line's comment hid a leak (ok=%v):\n%s", ok, out.String())
	}
}

// The module's root package is inside the module. An import of it is
// followed like any other, and the root is unlisted, so it promises nothing.
func TestSnapshotCheckFollowsTheModuleRoot(t *testing.T) {
	repo := snapRepo(t, map[string]string{
		"root.go":           "package m\n\ntype T int\n",
		"packages/a/a.go":   "package a\n\nimport \"example.com/m\"\n\nfunc UsesRoot(x m.T) {}\n",
		".api/packages.txt": "stable packages/a\n",
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
	if want := "packages/a: UsesRoot (func) names m.T, and the module's root package is not in packages.txt"; ok || !strings.Contains(out.String(), want) {
		t.Errorf("an import of the module root passed (ok=%v); want %q:\n%s", ok, want, out.String())
	}
}

// Without a go.mod no import resolves to the module, and the closure rule
// would pass on nothing. The check refuses to run.
func TestSnapshotCheckNeedsTheModulePath(t *testing.T) {
	repo := snapRepo(t, map[string]string{
		"go.mod":               "// no module line\n",
		"packages/sample/a.go": "package sample\n\nfunc New() {}\n",
		".api/packages.txt":    sampleManifest,
	})
	dir := filepath.Join(repo, ".api")
	var out bytes.Buffer
	if err := writeSnapshots(repo, dir, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := checkSnapshots(repo, dir, &out); err == nil || !strings.Contains(err.Error(), "declares no module") {
		t.Errorf("check ran without a module path: err=%v", err)
	}
}

func TestDefaultImportName(t *testing.T) {
	for path, want := range map[string]string{
		"strings":                         "strings",
		"terva.sh/terva/packages/core":    "core",
		"math/rand/v2":                    "rand",
		"gopkg.in/yaml.v3":                "yaml",
		"example.com/v2":                  "example.com",
		"example.com/tools/vet":           "vet",
		"example.com/tools/version.vnext": "version.vnext",
	} {
		if got := defaultImportName(path); got != want {
			t.Errorf("defaultImportName(%q) = %q, want %q", path, got, want)
		}
	}
}
