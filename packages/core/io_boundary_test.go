package core

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestIOBoundary enforces rule 1 of decision 0021: the engine
// (packages/core) and the wire (packages/provider) perform no file,
// environment, or process I/O.
//
// The check denies by default. Every exported package-level identifier of
// os, os/exec, os/signal, os/user, syscall, io/ioutil, and plugin is
// denied, and so are the path/filepath functions that touch the file
// system (Abs, EvalSymlinks, Glob, Walk, WalkDir). A reference passes only
// when ioBoundaryAllowlist names the identifier as pure and says why. A
// list of banned calls would pass the one call nobody listed, and a new Go
// release that adds an I/O function to os is denied here without an edit.
//
// Identifiers resolve through go/types, not text, so a renamed import
// (import o "os") and a function value (rm := os.Remove) are both caught.
// Types count too: exec.Cmd needs no denied function to run a process. A
// method on a value the host passed in, such as Write on an io.Writer, is
// not a package-level identifier and passes. That is how an injected sink
// works.
//
// 🔑 The import allowlist (TKT-01M35WJYM) cannot do this job, because os
// and os/exec are in the standard library, and a pure use such as
// os.ErrClosed imports the same package as os.Remove.
//
// Network I/O is out of scope. The wire's job is HTTP to providers, so net
// and net/http are allowed in it. Whether the engine may import them is the
// import allowlist's question. I/O inside an imported terva package, such
// as i18n reading catalog overlays from $TERVA_HOME, is that test's
// question as well: this one reads only the two packages' own non-test
// files.
//
// Today's offenders live in testdata/io_boundary_baseline.txt, one line
// per file and identifier with its reference count. The baseline can only
// shrink: a count above it fails, and so does an entry that no longer
// fires, so a change that removes an I/O call deletes or lowers its line in
// the same commit.
func TestIOBoundary(t *testing.T) {
	for id, reason := range ioBoundaryAllowlist {
		if strings.TrimSpace(reason) == "" || strings.Contains(reason, "\n") {
			t.Errorf("allowlist entry %s needs a one-line reason", id)
		}
		if !ioBoundaryDenied(ioBoundarySplit(id)) {
			t.Errorf("allowlist entry %s names an identifier the check does not deny; delete it", id)
		}
	}

	uses := ioBoundaryScan(t)

	counts := map[string]int{}         // "file\tident" -> references
	positions := map[string][]string{} // "file\tident" -> file:line:col
	allowed := map[string]bool{}
	for _, u := range uses {
		if _, ok := ioBoundaryAllowlist[u.ident]; ok {
			allowed[u.ident] = true
			continue
		}
		k := u.file + "\t" + u.ident
		counts[k]++
		positions[k] = append(positions[k], u.pos)
	}
	for id := range ioBoundaryAllowlist {
		if !allowed[id] {
			t.Errorf("allowlist entry %s is referenced nowhere in the engine or the wire; delete it", id)
		}
	}

	baseline := ioBoundaryReadBaseline(t)
	var keys []string
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		file, ident, _ := strings.Cut(k, "\t")
		got, want := counts[k], baseline[k]
		switch {
		case want == 0:
			for _, pos := range positions[k] {
				t.Errorf("%s: %s is file, environment, or process I/O, which decision 0021 rule 1 bans in the engine and the wire; take it through an interface the host implements, or, if it is pure, add it to ioBoundaryAllowlist with a reason", pos, ident)
			}
		case got > want:
			t.Errorf("%s has %d references to %s, above the baseline of %d, which only shrinks; one of these is new: %s", file, got, ident, want, strings.Join(positions[k], ", "))
		case got < want:
			t.Errorf("baseline entry %s %s %d is stale: %d references remain; lower it to %d in testdata/io_boundary_baseline.txt", file, ident, want, got, got)
		}
	}
	for k, want := range baseline {
		if counts[k] == 0 {
			file, ident, _ := strings.Cut(k, "\t")
			t.Errorf("baseline entry %s %s %d no longer fires; delete it from testdata/io_boundary_baseline.txt", file, ident, want)
		}
	}
	if t.Failed() {
		t.Logf("offenders as they stand, in baseline form:\n%s", ioBoundaryFormatBaseline(uses))
	}
}

// TestIOBoundaryDeniesUnlisted type-checks in-memory fixtures and proves
// that the check denies what no list in this file names, and that it
// resolves identifiers rather than text.
func TestIOBoundaryDeniesUnlisted(t *testing.T) {
	for _, id := range []string{"os.Create", "os.Remove", "os.StartProcess"} {
		if _, ok := ioBoundaryAllowlist[id]; ok {
			t.Fatalf("fixture assumes %s is not on the allowlist", id)
		}
	}

	cases := []struct {
		name string
		src  string
		want []string // "line ident"
	}{
		{
			name: "unlisted calls",
			src: `package fixture

import "os"

func f() {
	fh, _ := os.Create("x")
	_ = fh
	_ = os.Remove("x")
	_, _ = os.StartProcess("/bin/true", nil, nil)
}
`,
			want: []string{"6 os.Create", "8 os.Remove", "9 os.StartProcess"},
		},
		{
			name: "renamed import and function value",
			src: `package fixture

import o "os"

var rm = o.Remove

func g() { _ = rm("x") }
`,
			want: []string{"5 os.Remove"},
		},
		{
			name: "dot import",
			src: `package fixture

import . "os"

func h() { _ = Getenv("HOME") }
`,
			want: []string{"5 os.Getenv"},
		},
		{
			name: "type with no denied function",
			src: `package fixture

import "os/exec"

func r() error { return (&exec.Cmd{Path: "/bin/true"}).Run() }
`,
			want: []string{"5 os/exec.Cmd"},
		},
		{
			name: "file-system filepath functions only",
			src: `package fixture

import "path/filepath"

func p() string {
	a, _ := filepath.Abs("x")
	return filepath.Join(a, filepath.Base("y"))
}
`,
			want: []string{"6 path/filepath.Abs"},
		},
		{
			name: "injected sink and pure identifiers pass",
			src: `package fixture

import (
	"errors"
	"io"
	"syscall"
)

func w(sink io.Writer, err error) bool {
	_, _ = sink.Write([]byte("x"))
	return errors.Is(err, syscall.EPIPE)
}
`,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset, imp := ioBoundaryImporter()
			f, err := parser.ParseFile(fset, "fixture.go", tc.src, 0)
			if err != nil {
				t.Fatal(err)
			}
			uses, err := ioBoundaryCheck(fset, imp, "fixture", []*ast.File{f})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, u := range uses {
				if _, ok := ioBoundaryAllowlist[u.ident]; ok {
					continue
				}
				got = append(got, strconv.Itoa(u.line)+" "+u.ident)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("violations = %q, want %q", got, tc.want)
			}
			for _, u := range uses {
				if !strings.HasPrefix(u.pos, "fixture.go:"+strconv.Itoa(u.line)+":") {
					t.Errorf("position %q does not name the file and line", u.pos)
				}
			}
		})
	}
}

// ioBoundaryDeniedWhole lists the packages denied in full.
var ioBoundaryDeniedWhole = map[string]bool{
	"os":        true,
	"os/exec":   true,
	"os/signal": true,
	"os/user":   true,
	"syscall":   true,
	"io/ioutil": true,
	"plugin":    true,
}

// ioBoundaryDeniedFilepath lists the path/filepath functions that touch the
// file system. The rest of path/filepath is string work.
var ioBoundaryDeniedFilepath = map[string]bool{
	"Abs":          true, // reads the working directory
	"EvalSymlinks": true,
	"Glob":         true,
	"Walk":         true,
	"WalkDir":      true,
}

// ioBoundaryAllowlist names the denied-package identifiers that are pure.
// Each value is the one-line reason. An entry nothing references fails
// TestIOBoundary.
var ioBoundaryAllowlist = map[string]string{
	"syscall.ECONNRESET":   "an errno constant the wire matches against a network error it was handed",
	"syscall.ECONNREFUSED": "an errno constant the wire matches against a network error it was handed",
	"syscall.EPIPE":        "an errno constant the wire matches against a network error it was handed",
}

// 🔑 The os.O_* flags and the os.File type are pure in themselves, but
// nothing uses them except to open and hold a file. They sit in the
// baseline instead, so they leave with the call they serve rather than stay
// behind as allowlist entries the engine has no use for.

func ioBoundaryDenied(pkgPath, name string) bool {
	if ioBoundaryDeniedWhole[pkgPath] {
		return true
	}
	return pkgPath == "path/filepath" && ioBoundaryDeniedFilepath[name]
}

// ioBoundarySplit splits "os/exec.Command" into its package path and name.
func ioBoundarySplit(id string) (string, string) {
	i := strings.LastIndex(id, ".")
	return id[:i], id[i+1:]
}

type ioBoundaryUse struct {
	file  string // path as parsed, relative to the repository root
	line  int
	pos   string // file:line:col
	ident string // package path + "." + name
}

var (
	ioBoundaryOnce sync.Once
	ioBoundaryFset *token.FileSet
	ioBoundaryImp  types.ImporterFrom
)

// ioBoundaryImporter returns one source importer for the whole test binary,
// so the standard library is type-checked once.
func ioBoundaryImporter() (*token.FileSet, types.ImporterFrom) {
	ioBoundaryOnce.Do(func() {
		// ⚠️ The source importer runs cgo on a package such as net when
		// cgo is on, and that needs a C compiler the Docs Gate image does
		// not have. The pure-Go files type-check the same identifiers.
		build.Default.CgoEnabled = false
		ioBoundaryFset = token.NewFileSet()
		ioBoundaryImp = importer.ForCompiler(ioBoundaryFset, "source", nil).(types.ImporterFrom)
	})
	return ioBoundaryFset, ioBoundaryImp
}

// ioBoundaryCheck type-checks one package and returns every reference to a
// denied package-level identifier, allowlisted or not, in source order.
func ioBoundaryCheck(fset *token.FileSet, imp types.ImporterFrom, path string, files []*ast.File) ([]ioBoundaryUse, error) {
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: imp}
	if _, err := conf.Check(path, fset, files, info); err != nil {
		return nil, err
	}
	return ioBoundaryUses(fset, info), nil
}

func ioBoundaryUses(fset *token.FileSet, info *types.Info) []ioBoundaryUse {
	var out []ioBoundaryUse
	for id, obj := range info.Uses {
		pkg := obj.Pkg()
		if pkg == nil || !obj.Exported() || obj.Parent() != pkg.Scope() {
			continue // universe, methods, fields, and locals
		}
		if _, isPkgName := obj.(*types.PkgName); isPkgName {
			continue
		}
		if !ioBoundaryDenied(pkg.Path(), obj.Name()) {
			continue
		}
		p := fset.Position(id.Pos())
		out = append(out, ioBoundaryUse{
			file:  p.Filename,
			line:  p.Line,
			pos:   p.String(),
			ident: pkg.Path() + "." + obj.Name(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		if out[i].line != out[j].line {
			return out[i].line < out[j].line
		}
		return out[i].pos < out[j].pos
	})
	return out
}

// ioBoundaryScan type-checks the wire and then the engine from source and
// returns the denied references in both.
func ioBoundaryScan(t *testing.T) []ioBoundaryUse {
	t.Helper()
	fset, src := ioBoundaryImporter()

	const providerPath = "terva.sh/terva/packages/provider"
	load := func(dir, display string) []*ast.File {
		bp, err := build.ImportDir(dir, 0)
		if err != nil {
			t.Fatalf("%s: %v", display, err)
		}
		// ⚠️ A file a build constraint drops is a file this check cannot
		// see on this platform. None exist today, so refuse the first one
		// rather than pass it silently.
		for _, name := range append(bp.IgnoredGoFiles, bp.CgoFiles...) {
			if !strings.HasSuffix(name, "_test.go") {
				t.Errorf("%s/%s is excluded by a build constraint or uses cgo, so the I/O check cannot read it on every platform; extend TestIOBoundary before adding one", display, name)
			}
		}
		var files []*ast.File
		for _, name := range bp.GoFiles {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			f, err := parser.ParseFile(fset, display+"/"+name, data, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
		}
		return files
	}

	providerInfo := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	providerPkg, err := (&types.Config{Importer: src}).Check(providerPath, fset, load("../provider", "packages/provider"), providerInfo)
	if err != nil {
		t.Fatalf("type-check packages/provider: %v", err)
	}
	// transcriptcodec was carved out of the engine (TKT-01M35WK0T) and the
	// engine imports it, so it is held to the engine's rule.
	const codecPath = "terva.sh/terva/packages/core/transcriptcodec"
	codecInfo := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	withProvider := ioBoundaryWithPkg{ImporterFrom: src, pkgs: map[string]*types.Package{providerPath: providerPkg}}
	codecPkg, err := (&types.Config{Importer: withProvider}).Check(codecPath, fset, load("transcriptcodec", "packages/core/transcriptcodec"), codecInfo)
	if err != nil {
		t.Fatalf("type-check packages/core/transcriptcodec: %v", err)
	}
	coreInfo := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	coreImp := ioBoundaryWithPkg{ImporterFrom: src, pkgs: map[string]*types.Package{providerPath: providerPkg, codecPath: codecPkg}}
	if _, err := (&types.Config{Importer: coreImp}).Check("terva.sh/terva/packages/core", fset, load(".", "packages/core"), coreInfo); err != nil {
		t.Fatalf("type-check packages/core: %v", err)
	}
	uses := append(ioBoundaryUses(fset, providerInfo), ioBoundaryUses(fset, codecInfo)...)
	return append(uses, ioBoundaryUses(fset, coreInfo)...)
}

// ioBoundaryWithPkg hands the engine the wire package this test already
// checked, so provider is type-checked once and has one identity.
type ioBoundaryWithPkg struct {
	types.ImporterFrom
	pkgs map[string]*types.Package
}

func (i ioBoundaryWithPkg) ImportFrom(path, dir string, mode types.ImportMode) (*types.Package, error) {
	if pkg, ok := i.pkgs[path]; ok {
		return pkg, nil
	}
	return i.ImporterFrom.ImportFrom(path, dir, mode)
}

// ioBoundaryReadBaseline reads "file<TAB>ident<TAB>count" lines, keyed by
// "file\tident".
func ioBoundaryReadBaseline(t *testing.T) map[string]int {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "io_boundary_baseline.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]int{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("io_boundary_baseline.txt:%d: want \"file ident count\", got %q", n, line)
		}
		c, err := strconv.Atoi(fields[2])
		if err != nil || c <= 0 {
			t.Fatalf("io_boundary_baseline.txt:%d: bad count %q", n, fields[2])
		}
		k := fields[0] + "\t" + fields[1]
		if _, dup := out[k]; dup {
			t.Fatalf("io_boundary_baseline.txt:%d: duplicate entry %s %s", n, fields[0], fields[1])
		}
		out[k] = c
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// ioBoundaryFormatBaseline renders uses in baseline form. TestIOBoundary
// logs it on failure, so a line to lower or delete is easy to find. A new
// line in it is a new offender, not an entry to copy into the baseline.
func ioBoundaryFormatBaseline(uses []ioBoundaryUse) string {
	counts := map[string]int{}
	for _, u := range uses {
		if _, ok := ioBoundaryAllowlist[u.ident]; ok {
			continue
		}
		counts[u.file+"\t"+u.ident]++
	}
	var keys []string
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s\t%d\n", k, counts[k])
	}
	return b.String()
}
