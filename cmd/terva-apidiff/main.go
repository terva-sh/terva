// Command terva-apidiff reports how the exported API of a set of packages
// changed between two git refs, so the patch-or-minor call at a release cut
// rests on a census rather than on a reading of the commit log.
//
// The rule it serves lives in docs/plans/release-process.md: the minor tracks
// how stable the CORE pieces are, not how much shipped. Features, behaviour
// changes and frontend work stay patches. So the question at a cut is narrow
// and mechanical — did the exported surface of the core packages lose or
// change anything, and did those packages change shape — and that is what this
// answers. It does not decide the version. It supplies the evidence the
// decision needs, and says plainly when it has none.
//
// Usage:
//
//	terva-apidiff -base pub/v0.132.5              # against the working tree
//	terva-apidiff -base v0.130.1 -head v0.131.0   # between two refs
//	terva-apidiff -base X -head Y -pkgs packages/core,packages/tui
//	terva-apidiff -base X -fail-on-break          # non-zero if anything broke
//
// It parses syntactically rather than type-checking, so build-tag-gated files
// (terva_acp, connector variants) contribute their symbols too: the census is
// the union across builds, which is the surface a consumer can reach with some
// build. Same reason terva-i18n-lint parses that way.
//
// # On empty answers
//
// A detector of this shape has one failure mode that matters, and it is not a
// wrong answer — it is a CLEAN one. Every hand-rolled version of this check
// has at some point reported "no removals" because it was looking at nothing
// at all: a mis-split path list, a package that moved, a ref that resolved to
// an empty tree. A clean report and a vacuous one read identically.
//
// So an empty census is a hard error here, per package and per side. If a
// package yields no exported symbols at either ref, this command fails and
// says so rather than reporting that nothing changed. That is the whole
// defence, and it is automatic — it does not depend on anyone remembering to
// validate the detector against a known range first.
//
// This release mode counts every change alike. It reads neither the snapshot
// manifest's classes nor the Unstable: marker; snapshot mode (snapshot.go)
// splits its count by class.
//
// It is release tooling, not part of the shipped binary.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// defaultPkgs is the surface the versioning rule actually asks about: the
// engine, the provider layer, the tool layer, and the SDK other people build
// against. The TUI and the web client are deliberately absent — frontend work
// never earns a minor, so their churn is not evidence for this decision and
// would only bury the packages that are.
var defaultPkgs = []string{
	"packages/core",
	"packages/provider",
	"packages/agent/tools",
	"packages/agent/sdk",
	// packages/session (the JSONL session store, the engine's until
	// TKT-01M35WK0T) belongs here once a published release contains it. Before
	// that the base has no source for it and comparePkg refuses, which would
	// fail the whole run; its API left packages/core, and this list already
	// reports that removal.
}

type symbol struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Sig  string `json:"sig,omitempty"`
	// Unstable is set by an "Unstable:" paragraph in the symbol's doc, or in
	// the doc of the type that holds it. The JSON leaves it out, so release
	// mode's -json output keeps its shape.
	Unstable bool `json:"-"`
	// Refs are the exported names the signature mentions. The snapshot
	// check's closure rule reads them.
	Refs []ref `json:"-"`
}

// ref is one exported name a signature mentions. Qual is the package
// qualifier as written, "" for a name in the symbol's own package. Path is
// the import path Qual resolves to, "" when no import of the file matches.
// Via is the unexported type the name was reached through, "" when the
// signature names it directly.
type ref struct {
	Qual, Path, Name, Via string
}

// unstableMarker opens the doc paragraph that takes a symbol out of its
// package's promise, the way Go's "Deprecated:" marks a symbol for removal.
const unstableMarker = "Unstable:"

type change struct {
	Symbol  symbol `json:"symbol"`
	Was     string `json:"was,omitempty"`
	WasKind string `json:"was_kind,omitempty"`
	// WasUnstable is the marker the symbol had before, which decides the
	// class a break counts in.
	WasUnstable bool `json:"-"`
}

type pkgReport struct {
	Pkg      string   `json:"pkg"`
	Added    []symbol `json:"added"`
	Removed  []symbol `json:"removed"`
	Changed  []change `json:"changed"`
	NewFiles []string `json:"new_files"`
	BaseSyms int      `json:"base_symbols"`
	HeadSyms int      `json:"head_symbols"`
}

func main() {
	var (
		base    = flag.String("base", "", "git ref to compare from (required)")
		head    = flag.String("head", "", "git ref to compare to (default: the working tree)")
		repo    = flag.String("C", ".", "repository to read")
		pkgList = flag.String("pkgs", strings.Join(defaultPkgs, ","), "comma-separated package directories")
		asJSON  = flag.Bool("json", false, "emit the census as JSON")
		strict  = flag.Bool("fail-on-break", false, "exit non-zero when a symbol is removed or changed")

		snapDir = flag.String("snapshot-dir", "", "snapshot mode: the directory holding packages.txt and the snapshots (see snapshot.go)")
		write   = flag.Bool("write", false, "snapshot mode: rewrite every snapshot from the code")
		check   = flag.Bool("check", false, "snapshot mode: exit non-zero when a snapshot differs from the code")
		since   = flag.String("since", "", "snapshot mode: count the changes per class since this git ref; never fails")
		notes   = flag.String("notes", "", "snapshot mode, with -since: report each stable break since the ref that this migration document does not cover (see notes.go)")
		require = flag.Bool("require", false, "with -notes: exit non-zero on a finding; without it the report is advisory")
		release = flag.String("release", "", "with -notes: the version being cut, whose section must hold every unpublished note")
		seal    = flag.String("seal", "", "with -notes, -snapshot-dir and -since: move the Unreleased notes to a section for this version, in place, headed by the count of breaks since the ref")
	)
	flag.Parse()

	if *seal != "" {
		if *snapDir == "" || *since == "" || *notes == "" || *write || *check || *require || *release != "" {
			fail("-seal VERSION takes -notes FILE, -snapshot-dir DIR and -since REF, and nothing else")
		}
		os.Exit(sealMain(*repo, *snapDir, *since, *notes, *seal))
	}
	if *snapDir != "" {
		os.Exit(snapshotMain(*repo, *snapDir, *write, *check, *since, notesFlags{*notes, *release, *require}))
	}
	if *notes != "" || *require || *release != "" {
		fail("-notes, -require and -release need -snapshot-dir")
	}

	if *base == "" {
		fail("-base is required (the ref this release is measured against, e.g. pub/v0.132.5)")
	}
	pkgs := strings.Split(*pkgList, ",")

	var reports []pkgReport
	for _, pkg := range pkgs {
		pkg = strings.TrimSpace(strings.TrimSuffix(pkg, "/"))
		if pkg == "" {
			continue
		}
		report, err := comparePkg(*repo, pkg, *base, *head)
		if err != nil {
			fail("%s: %v", pkg, err)
		}
		reports = append(reports, report)
	}

	if *asJSON {
		out, err := json.MarshalIndent(reports, "", "  ")
		if err != nil {
			fail("%v", err)
		}
		fmt.Println(string(out))
	} else {
		printReport(reports, *base, *head)
	}

	if *strict && broke(reports) {
		os.Exit(1)
	}
}

// notesFlags are the flags of the notes check.
type notesFlags struct {
	File, Release string
	Require       bool
}

// snapshotMain runs exactly one snapshot mode and returns the exit code: 0
// clean, 1 a stale snapshot under -check or a finding under -notes -require,
// 2 a usage or read error.
func snapshotMain(repo, dir string, write, check bool, since string, n notesFlags) int {
	modes := 0
	for _, on := range []bool{write, check, since != ""} {
		if on {
			modes++
		}
	}
	if modes != 1 {
		fmt.Fprintln(os.Stderr, "terva-apidiff: -snapshot-dir takes exactly one of -write, -check or -since REF")
		return 2
	}
	if n.File != "" && since == "" {
		fmt.Fprintln(os.Stderr, "terva-apidiff: -notes needs -since REF, the release the notes run from")
		return 2
	}
	if (n.Require || n.Release != "") && n.File == "" {
		fmt.Fprintln(os.Stderr, "terva-apidiff: -require and -release apply to -notes only")
		return 2
	}
	var err error
	switch {
	case n.File != "":
		var ok bool
		ok, err = notesReport(repo, dir, n.File, since, n.Release, os.Stdout)
		if err == nil && !ok && n.Require {
			return 1
		}
	case write:
		err = writeSnapshots(repo, dir, os.Stdout)
	case check:
		var ok bool
		ok, err = checkSnapshots(repo, dir, os.Stdout)
		if err == nil && !ok {
			return 1
		}
	default:
		err = sinceReport(repo, dir, since, os.Stdout)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "terva-apidiff: %v\n", err)
		return 2
	}
	return 0
}

// sealMain moves the Unreleased notes of the page at path to a section for
// version, in place, headed by the count of breaks since ref. 0 when it
// sealed or found nothing to seal, 1 when the notes are not complete, 2 on an
// error.
//
// It seals only notes the check passes, because the count it writes tells a
// host that every stable break has a note.
func sealMain(repo, dir, ref, path, version string) int {
	var report bytes.Buffer
	ok, err := notesReport(repo, dir, path, ref, "", &report)
	if err == nil && !ok {
		os.Stdout.Write(report.Bytes())
		fmt.Fprintf(os.Stderr, "terva-apidiff: %s: not sealed, because the notes are not complete (report above)\n", path)
		return 1
	}
	var data []byte
	if err == nil {
		data, err = os.ReadFile(path)
	}
	var pkgs []sincePkg
	if err == nil {
		pkgs, err = diffSince(repo, dir, ref)
	}
	if err == nil {
		var out []byte
		var n int
		if out, n, err = sealNotes(data, version, breakCount(breaksSince(pkgs), ref)); err == nil {
			if n == 0 {
				if bytes.Contains(data, []byte("\n## "+version+"\n")) {
					fmt.Printf("%s: nothing under %q; %s is sealed already\n", path, unreleasedHeading, version)
				} else {
					fmt.Printf("%s: nothing under %q, so %s gets no section\n", path, unreleasedHeading, version)
				}
				return 0
			}
			if err = os.WriteFile(path, out, 0o644); err == nil {
				fmt.Printf("%s: sealed %d note(s) under \"## %s\"; commit the page before the cut\n", path, n, version)
				return 0
			}
		}
	}
	fmt.Fprintf(os.Stderr, "terva-apidiff: %s: %v\n", path, err)
	return 2
}

func fail(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "terva-apidiff: "+format+"\n", args...)
	os.Exit(2)
}

func broke(reports []pkgReport) bool {
	for _, r := range reports {
		if len(r.Removed) > 0 || len(r.Changed) > 0 {
			return true
		}
	}
	return false
}

// comparePkg censuses one package at both refs and diffs them.
func comparePkg(repo, pkg, base, head string) (pkgReport, error) {
	report := pkgReport{Pkg: pkg}

	baseFiles, err := filesAtRef(repo, base, pkg)
	if err != nil {
		return report, fmt.Errorf("reading %s at %s: %w", pkg, base, err)
	}
	headFiles, err := filesAt(repo, head, pkg)
	if err != nil {
		return report, fmt.Errorf("reading %s at %s: %w", pkg, describeHead(head), err)
	}

	baseSyms, err := censusTree(pkg, baseFiles)
	if err != nil {
		return report, fmt.Errorf("parsing %s at %s: %w", pkg, base, err)
	}
	headSyms, err := censusTree(pkg, headFiles)
	if err != nil {
		return report, fmt.Errorf("parsing %s at %s: %w", pkg, describeHead(head), err)
	}

	// The vacuity guards. See the package comment: a census that found nothing
	// cannot be reported as "nothing changed", because those two look the same
	// on the way out and only one of them is an answer.
	//
	// Split by cause, because the two have different remedies and a single
	// message would have to hedge between them.
	if len(baseFiles) == 0 {
		return report, fmt.Errorf("no Go source at %s — the package does not exist there. "+
			"A package added since then has no published surface to compare against; census it "+
			"separately or drop it from -pkgs. A census of nothing is not a clean report", base)
	}
	if len(headFiles) == 0 {
		return report, fmt.Errorf("no Go source at %s — the package was moved or removed. "+
			"A census of nothing is not a clean report", describeHead(head))
	}
	if len(baseSyms) == 0 {
		return report, fmt.Errorf("%d file(s) at %s and not one exported symbol — the parse found "+
			"nothing a consumer could name. A census of nothing is not a clean report",
			len(baseFiles), base)
	}
	if len(headSyms) == 0 {
		return report, fmt.Errorf("%d file(s) at %s and not one exported symbol — the parse found "+
			"nothing a consumer could name. A census of nothing is not a clean report",
			len(headFiles), describeHead(head))
	}
	// 🔑 The same diff the snapshot mode's -since uses, so the release
	// census and the snapshot report cannot disagree about a package.
	report = diffSymbols(pkg, baseSyms, headSyms)
	report.Changed = knownBefore(report.Changed)

	// A new file in one of these packages is the signal the versioning rule
	// calls "the core packages changed shape" — the escalation clause that
	// turned v0.132.0 into a minor. Added symbols alone do not show it: a new
	// method on an existing type is routine, a whole new file is a new piece.
	for path := range headFiles {
		if _, ok := baseFiles[path]; !ok {
			report.NewFiles = append(report.NewFiles, path)
		}
	}

	sort.Strings(report.NewFiles)
	return report, nil
}

// censusTree censuses a root package and every package below it, one
// directory at a time. A symbol of the root keeps its name, and a
// subpackage's carries its path under the root: packages/core/stall's New is
// "stall.New". Keyed by name alone, the two would be one symbol, and a New
// removed from the root would hide behind the subpackage's. The map's keys
// are internal: only diffSymbols reads them, and it reports the names.
func censusTree(root string, files map[string][]byte) (map[string]symbol, error) {
	byDir := map[string]map[string][]byte{}
	for path, body := range files {
		dir := pathpkg.Dir(path)
		if hiddenPkg(dir) {
			continue
		}
		if byDir[dir] == nil {
			byDir[dir] = map[string][]byte{}
		}
		byDir[dir][path] = body
	}
	out := map[string]symbol{}
	for dir, group := range byDir {
		syms, err := census(group)
		if err != nil {
			return nil, err
		}
		rel := ""
		if dir != root {
			rel = strings.TrimPrefix(dir, root+"/")
		}
		for key, sym := range syms {
			if rel != "" {
				sym.Name = rel + "." + sym.Name
			}
			// ⚠️ Key on the directory and the symbol apart, never on the
			// dotted name: a directory a.B exporting New and a method New
			// on type B in directory a both print as a.B.New, and one key
			// would make them one symbol.
			out[rel+"\x00"+key] = sym
		}
	}
	return out, nil
}

func sortSymbols(syms []symbol) {
	sort.Slice(syms, func(i, j int) bool { return syms[i].Name < syms[j].Name })
}

func describeHead(head string) string {
	if head == "" {
		return "the working tree"
	}
	return head
}

// filesAtRef reads a package's non-test Go sources out of a git ref.
//
// Read from the object store rather than checked out: the release worktree is
// busy holding a candidate tree, and a census must never need to move it.
func filesAtRef(repo, ref, pkg string) (map[string][]byte, error) {
	out, err := run(repo, "ls-tree", "-r", "-z", ref, "--", pkg)
	if err != nil {
		return nil, err
	}
	oids := map[string]string{}
	var order []string
	for _, entry := range strings.Split(out, "\x00") {
		if entry == "" {
			continue
		}
		meta, path, found := strings.Cut(entry, "\t")
		if !found || !isSource(path) {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) < 3 {
			continue
		}
		oids[path] = fields[2]
		order = append(order, path)
	}
	files := map[string][]byte{}
	for _, path := range order {
		blob, err := runBytes(repo, "cat-file", "blob", oids[path])
		if err != nil {
			return nil, err
		}
		files[path] = blob
	}
	return files, nil
}

// filesAt reads from a ref, or from the working tree when ref is empty.
func filesAt(repo, ref, pkg string) (map[string][]byte, error) {
	if ref != "" {
		return filesAtRef(repo, ref, pkg)
	}
	files := map[string][]byte{}
	dir := filepath.Join(repo, filepath.FromSlash(pkg))
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !isSource(rel) {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[rel] = body
		return nil
	})
	return files, err
}

// expDir is the directory name of an experiments package (decision 0021, rule
// 7). An exp package promises nothing, so its changes are not evidence for the
// version and are left out of every census, wherever one sits.
const expDir = "exp"

// isSource keeps Go sources that are part of the package's surface. Tests are
// out: they compile into no consumer's build. So is anything under an exp
// directory: packages/core/exp is imported by hosts but promises nothing.
func isSource(path string) bool {
	if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
		return false
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == expDir {
			return false
		}
	}
	return true
}

// census extracts every exported symbol a consumer could name.
//
// Nested one level deep on purpose: an exported struct field and an interface
// method are as breakable as the type that holds them, and a removal there
// would otherwise show up as an unchanged type. The type itself carries only
// "struct" or "interface" as its signature so a field edit is reported once,
// against the field, and not a second time as a whole-type rewrite.
func census(files map[string][]byte) (map[string]symbol, error) {
	fset := token.NewFileSet()
	syms := map[string]symbol{}
	hidden := map[string][]ref{}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		// Comments are parsed for the Unstable: marker. render keeps them
		// out of every signature.
		file, err := parser.ParseFile(fset, path, files[path], parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		c := &censusFile{fset: fset, syms: syms, hidden: hidden, imports: importNames(file)}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				c.addFunc(d)
			case *ast.GenDecl:
				c.addGenDecl(d)
			}
		}
	}
	// A type's marker covers its fields and its methods, wherever the
	// methods are declared. Every member's name is "Type.Member".
	for name, s := range syms {
		if owner, _, ok := strings.Cut(name, "."); ok && syms[owner].Unstable {
			s.Unstable = true
			syms[name] = s
		}
	}
	// A host cannot name an unexported type, but it reaches the exported
	// fields and methods of one that a signature returns, holds or embeds.
	// Those members' references become the symbol's own.
	for name, s := range syms {
		s.Refs = throughHidden(s.Refs, hidden)
		syms[name] = s
	}
	return syms, nil
}

// throughHidden replaces each reference to an unexported local type with the
// references of that type's exported members, followed transitively. A
// reference reached that way records the first unexported type in Via. An
// unexported name that is no type of the package stays as it is, so the
// closure check reports it as it reports an exported name it cannot find.
func throughHidden(refs []ref, hidden map[string][]ref) []ref {
	var out []ref
	for _, r := range refs {
		if _, known := hidden[r.Name]; r.Qual != "" || ast.IsExported(r.Name) || !known {
			out = append(out, r)
			continue
		}
		seen := map[string]bool{}
		var follow func(name string)
		follow = func(name string) {
			if seen[name] {
				return
			}
			seen[name] = true
			for _, h := range hidden[name] {
				if _, known := hidden[h.Name]; known && h.Qual == "" && !ast.IsExported(h.Name) {
					follow(h.Name)
					continue
				}
				h.Via = r.Name
				out = append(out, h)
			}
		}
		follow(r.Name)
	}
	return out
}

// censusFile carries what one file's declarations need: the shared census,
// and the file's imports to resolve a qualified name in a signature.
type censusFile struct {
	fset *token.FileSet
	syms map[string]symbol
	// hidden holds, per unexported type, the references of its exported
	// members: what a host reaches through a value of it.
	hidden  map[string][]ref
	imports map[string]string
}

// predeclared are the lowercase names a type expression can hold that no
// package declares.
var predeclared = map[string]bool{
	"any": true, "bool": true, "byte": true, "comparable": true, "complex64": true,
	"complex128": true, "error": true, "float32": true, "float64": true, "int": true,
	"int8": true, "int16": true, "int32": true, "int64": true, "rune": true,
	"string": true, "uint": true, "uint8": true, "uint16": true, "uint32": true,
	"uint64": true, "uintptr": true, "nil": true, "true": true, "false": true, "iota": true,
}

// importNames maps each import's name in the file to its path. An import
// without an alias takes the name the path's last element suggests. The
// census reads syntax only, so it cannot see a package clause that differs.
// A qualifier that resolves to nothing then fails the closure check, which
// asks for an alias. Dot and blank imports name nothing.
func importNames(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, "`\"")
		name := ""
		if spec.Name != nil {
			name = spec.Name.Name
		} else {
			name = defaultImportName(path)
		}
		if name == "." || name == "_" {
			continue
		}
		out[name] = path
	}
	return out
}

// defaultImportName guesses a package's name from its import path: the last
// element, less a major-version element (math/rand/v2 is rand) or a gopkg.in
// version suffix (yaml.v3 is yaml).
func defaultImportName(path string) string {
	elems := strings.Split(path, "/")
	name := elems[len(elems)-1]
	if len(elems) > 1 && isMajorVersion(name) {
		name = elems[len(elems)-2]
	}
	if i := strings.LastIndex(name, ".v"); i > 0 && isMajorVersion(name[i+1:]) {
		name = name[:i]
	}
	return name
}

func isMajorVersion(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// marked reports whether any of the doc comments holds a paragraph that opens
// with the Unstable: marker. An indented line is a code block in a doc, so a
// marker quoted in an example does not count.
func marked(docs ...*ast.CommentGroup) bool {
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		start := true
		for _, line := range strings.Split(doc.Text(), "\n") {
			if start && strings.HasPrefix(line, unstableMarker) {
				return true
			}
			start = strings.TrimSpace(line) == ""
		}
	}
	return false
}

// refsOf collects the names the type expressions mention. A field's or a
// parameter's own name is not a reference, and neither is a type parameter
// or a predeclared type. An unexported local name is kept, and census
// replaces it with what the type exposes (throughHidden).
func (c *censusFile) refsOf(tparams map[string]bool, nodes ...ast.Node) []ref {
	var out []ref
	var visit func(ast.Node) bool
	visit = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Field:
			ast.Inspect(x.Type, visit)
			return false
		case *ast.ArrayType:
			// An array's length is a constant, not a type: [maxN]T names
			// only T.
			ast.Inspect(x.Elt, visit)
			return false
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok {
				if x.Sel.IsExported() {
					out = append(out, ref{Qual: id.Name, Path: c.imports[id.Name], Name: x.Sel.Name})
				}
				return false
			}
		case *ast.Ident:
			if !tparams[x.Name] && (x.IsExported() || !predeclared[x.Name]) {
				out = append(out, ref{Name: x.Name})
			}
		}
		return true
	}
	for _, n := range nodes {
		if n != nil && !reflect.ValueOf(n).IsNil() {
			ast.Inspect(n, visit)
		}
	}
	return out
}

// typeParams names the type parameters in scope for a signature.
func typeParams(lists ...*ast.FieldList) map[string]bool {
	out := map[string]bool{}
	for _, l := range lists {
		if l == nil {
			continue
		}
		for _, f := range l.List {
			for _, n := range f.Names {
				out[n.Name] = true
			}
		}
	}
	return out
}

// receiverParams names the type parameters a generic receiver binds: the K
// and V of func (m *Map[K, V]) Get.
func receiverParams(expr ast.Expr) map[string]bool {
	out := map[string]bool{}
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	var idx []ast.Expr
	switch t := expr.(type) {
	case *ast.IndexExpr:
		idx = []ast.Expr{t.Index}
	case *ast.IndexListExpr:
		idx = t.Indices
	}
	for _, e := range idx {
		if id, ok := e.(*ast.Ident); ok {
			out[id.Name] = true
		}
	}
	return out
}

func (c *censusFile) addFunc(decl *ast.FuncDecl) {
	if !decl.Name.IsExported() {
		return
	}
	name := decl.Name.Name
	kind := "func"
	tparams := typeParams(decl.Type.TypeParams)
	if decl.Recv != nil && len(decl.Recv.List) == 1 {
		recv := receiverName(decl.Recv.List[0].Type)
		if recv == "" {
			return
		}
		if !ast.IsExported(recv) {
			// A method on an unexported type is no symbol, but a host
			// calls it on a value a stable signature hands out.
			c.hidden[recv] = append(c.hidden[recv], c.refsOf(receiverParams(decl.Recv.List[0].Type), decl.Type)...)
			return
		}
		name = recv + "." + name
		kind = "method"
		tparams = receiverParams(decl.Recv.List[0].Type)
	}
	c.syms[name] = symbol{Name: name, Kind: kind, Sig: render(c.fset, decl.Type),
		Unstable: marked(decl.Doc), Refs: c.refsOf(tparams, decl.Type)}
}

func (c *censusFile) addGenDecl(decl *ast.GenDecl) {
	fset, syms := c.fset, c.syms
	// In a const group, a spec with no values repeats the one above it, type
	// and expressions included: `A T = iota` then a bare `B` makes B a T too.
	var carriedType string
	var carriedTypeExpr ast.Expr
	var carriedVals []ast.Expr
	for _, spec := range decl.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if !s.Name.IsExported() {
				c.hidden[s.Name.Name] = append(c.hidden[s.Name.Name], c.exposedRefs(s)...)
				continue
			}
			// godoc shows each type in a group apart, with its own doc, so a
			// group's doc speaks for a type only when the type stands alone.
			// A const or var group is shown whole, and its doc covers all.
			doc := []*ast.CommentGroup{s.Doc}
			if len(decl.Specs) == 1 {
				doc = append(doc, decl.Doc)
			}
			syms[s.Name.Name] = symbol{Name: s.Name.Name, Kind: "type", Sig: typeSig(fset, s),
				Unstable: marked(doc...), Refs: c.typeRefs(s)}
			c.addMembers(s)
		case *ast.ValueSpec:
			kind := "var"
			if decl.Tok == token.CONST {
				kind = "const"
				if len(s.Values) > 0 {
					carriedType, carriedTypeExpr, carriedVals = "", s.Type, s.Values
					if s.Type != nil {
						carriedType = render(fset, s.Type)
					}
				}
			}
			for i, name := range s.Names {
				if !name.IsExported() {
					continue
				}
				// The signature is the declared type, never the value: a
				// const's value changing is not an API break, and its type
				// changing is. Without type checking some types stay unseen
				// (`var X = f()`, an untyped const), and those record presence
				// only.
				var sig string
				var typ ast.Expr
				switch {
				case kind == "var":
					typ, sig = varType(fset, s, i)
				case carriedType != "":
					typ, sig = carriedTypeExpr, carriedType
				case i < len(carriedVals):
					// Each name takes the kind of its own value:
					// `A, B = 1, "x"` is an int and a string.
					sig = untypedKind(carriedVals[i])
				}
				syms[name.Name] = symbol{Name: name.Name, Kind: kind, Sig: sig,
					Unstable: marked(decl.Doc, s.Doc), Refs: c.refsOf(nil, typ)}
			}
		}
	}
}

// untypedKind is the default kind of an untyped constant expression, as far as
// the syntax shows it: "untyped int" for 3, iota or 1 << iota, "untyped
// string" for a string literal. A change of kind breaks a caller that uses
// the constant as the old kind. Anything that names another constant or calls
// a conversion yields "", and the constant records presence only.
func untypedKind(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.BasicLit:
		switch v.Kind {
		case token.INT:
			return "untyped int"
		case token.FLOAT:
			return "untyped float"
		case token.IMAG:
			return "untyped complex"
		case token.CHAR:
			return "untyped rune"
		case token.STRING:
			return "untyped string"
		}
	case *ast.Ident:
		switch v.Name {
		case "iota":
			return "untyped int"
		case "true", "false":
			return "untyped bool"
		}
	case *ast.ParenExpr:
		return untypedKind(v.X)
	case *ast.UnaryExpr:
		// Every unary operator keeps its operand's type, so `!bool(true)` is
		// a typed bool. An operand of unknown kind yields "".
		return untypedKind(v.X)
	case *ast.BinaryExpr:
		switch v.Op {
		case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
			// A comparison always yields an untyped bool, whatever it compares.
			return "untyped bool"
		case token.SHL, token.SHR:
			return untypedKind(v.X)
		}
		x, y := untypedKind(v.X), untypedKind(v.Y)
		if x == y {
			return x
		}
		// Mixed numeric kinds take the later one in Go's order.
		order := map[string]int{"untyped int": 1, "untyped rune": 2, "untyped float": 3, "untyped complex": 4}
		if order[x] > 0 && order[y] > 0 {
			if order[x] > order[y] {
				return x
			}
			return y
		}
	}
	return ""
}

// varType is the type of the i-th name in a var spec, where the syntax shows
// it: a declared type, a composite literal, the address of one, a func
// literal, or errors.New and fmt.Errorf. Anything else needs a type checker,
// and yields "". The expression is the one the signature renders, nil when
// none does.
func varType(fset *token.FileSet, s *ast.ValueSpec, i int) (ast.Expr, string) {
	if s.Type != nil {
		return s.Type, render(fset, s.Type)
	}
	if len(s.Values) != len(s.Names) {
		return nil, ""
	}
	switch v := s.Values[i].(type) {
	case *ast.CompositeLit:
		if v.Type != nil {
			return v.Type, render(fset, v.Type)
		}
	case *ast.UnaryExpr:
		if lit, ok := v.X.(*ast.CompositeLit); ok && v.Op == token.AND && lit.Type != nil {
			return lit.Type, "*" + render(fset, lit.Type)
		}
	case *ast.FuncLit:
		return v.Type, render(fset, v.Type)
	case *ast.CallExpr:
		// A sentinel error is the common case, and these two constructors
		// fix their result type.
		if fn := render(fset, v.Fun); fn == "errors.New" || fn == "fmt.Errorf" {
			return nil, "error"
		}
	}
	return nil, ""
}

// typeRefs are the names a type's own signature mentions: its type
// parameters' constraints, and its whole underlying type unless that is a
// struct or an interface. A struct's or an interface's members carry their
// own references, except an embedded one, whose members the type takes on.
func (c *censusFile) typeRefs(spec *ast.TypeSpec) []ref {
	tparams := typeParams(spec.TypeParams)
	nodes := []ast.Node{spec.TypeParams}
	switch t := spec.Type.(type) {
	case *ast.StructType:
		for _, f := range t.Fields.List {
			if len(f.Names) == 0 {
				nodes = append(nodes, f.Type)
			}
		}
	case *ast.InterfaceType:
		for _, f := range t.Methods.List {
			if len(f.Names) == 0 {
				nodes = append(nodes, f.Type)
			}
		}
	default:
		nodes = append(nodes, spec.Type)
	}
	return c.refsOf(tparams, nodes...)
}

// exposedRefs are the names an unexported type shows a host: what its own
// signature mentions, and the types of its exported fields and interface
// methods. Its methods declared elsewhere join in addFunc.
func (c *censusFile) exposedRefs(spec *ast.TypeSpec) []ref {
	out := c.typeRefs(spec)
	tparams := typeParams(spec.TypeParams)
	var members []*ast.Field
	switch t := spec.Type.(type) {
	case *ast.StructType:
		members = t.Fields.List
	case *ast.InterfaceType:
		members = t.Methods.List
	}
	for _, f := range members {
		for _, name := range f.Names {
			if name.IsExported() {
				out = append(out, c.refsOf(tparams, f.Type)...)
				break
			}
		}
	}
	return out
}

func (c *censusFile) addMembers(spec *ast.TypeSpec) {
	fset, syms := c.fset, c.syms
	tparams := typeParams(spec.TypeParams)
	switch t := spec.Type.(type) {
	case *ast.StructType:
		for _, field := range t.Fields.List {
			for _, name := range field.Names {
				if !name.IsExported() {
					continue
				}
				full := spec.Name.Name + "." + name.Name
				syms[full] = symbol{Name: full, Kind: "field", Sig: render(fset, field.Type),
					Unstable: marked(field.Doc), Refs: c.refsOf(tparams, field.Type)}
			}
		}
	case *ast.InterfaceType:
		for _, method := range t.Methods.List {
			for _, name := range method.Names {
				if !name.IsExported() {
					continue
				}
				full := spec.Name.Name + "." + name.Name
				syms[full] = symbol{Name: full, Kind: "method", Sig: render(fset, method.Type),
					Unstable: marked(method.Doc), Refs: c.refsOf(tparams, method.Type)}
			}
		}
	}
}

// typeSig keeps a composite type's own signature coarse, because its members
// are censused separately.
func typeSig(fset *token.FileSet, spec *ast.TypeSpec) string {
	// A generic type's parameters and constraints are its API as much as its
	// body is: a tighter constraint breaks a caller's instantiation.
	prefix := ""
	if tp := spec.TypeParams; tp != nil && len(tp.List) > 0 {
		var fields []string
		for _, f := range tp.List {
			names := make([]string, len(f.Names))
			for i, n := range f.Names {
				names[i] = n.Name
			}
			fields = append(fields, strings.Join(names, ", ")+" "+render(fset, f.Type))
		}
		prefix = "[" + strings.Join(fields, ", ") + "] "
	}
	switch spec.Type.(type) {
	case *ast.StructType:
		return prefix + "struct"
	case *ast.InterfaceType:
		return prefix + "interface"
	}
	return prefix + render(fset, spec.Type)
}

func receiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr: // generic receiver: T[P]
		return receiverName(t.X)
	case *ast.IndexListExpr:
		return receiverName(t.X)
	}
	return ""
}

// render prints a node as one line. A comment is no part of a signature, but
// the printer prints a field's doc and line comment wherever it prints the
// field, so an anonymous struct or interface in a signature would carry them.
// render sets them aside for the print and puts them back.
func render(fset *token.FileSet, node ast.Node) string {
	type saved struct {
		field        *ast.Field
		doc, comment *ast.CommentGroup
	}
	var stash []saved
	ast.Inspect(node, func(n ast.Node) bool {
		if f, ok := n.(*ast.Field); ok && (f.Doc != nil || f.Comment != nil) {
			stash = append(stash, saved{f, f.Doc, f.Comment})
			f.Doc, f.Comment = nil, nil
		}
		return true
	})
	defer func() {
		for _, s := range stash {
			s.field.Doc, s.field.Comment = s.doc, s.comment
		}
	}()
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, node); err != nil {
		return "?"
	}
	return strings.Join(strings.Fields(buf.String()), " ")
}

func run(repo string, args ...string) (string, error) {
	out, err := runBytes(repo, args...)
	return string(out), err
}

func runBytes(repo string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func printReport(reports []pkgReport, base, head string) {
	fmt.Printf("exported API: %s -> %s\n\n", base, describeHead(head))
	var removed, changed, newFiles int
	for _, r := range reports {
		fmt.Printf("%s: %d added, %d removed, %d changed (%d symbols)\n",
			r.Pkg, len(r.Added), len(r.Removed), len(r.Changed), r.HeadSyms)
		for _, s := range r.Removed {
			fmt.Printf("    - %s (%s)\n", s.Name, s.Kind)
		}
		for _, c := range r.Changed {
			if c.WasKind != "" && c.WasKind != c.Symbol.Kind {
				fmt.Printf("    ~ %s: %s %s -> %s %s\n", c.Symbol.Name, c.WasKind, c.Was, c.Symbol.Kind, c.Symbol.Sig)
				continue
			}
			fmt.Printf("    ~ %s: %s -> %s\n", c.Symbol.Name, c.Was, c.Symbol.Sig)
		}
		for _, s := range r.Added {
			fmt.Printf("    + %s (%s)\n", s.Name, s.Kind)
		}
		if len(r.NewFiles) > 0 {
			fmt.Printf("    new files: %s\n", strings.Join(r.NewFiles, ", "))
		}
		removed += len(r.Removed)
		changed += len(r.Changed)
		newFiles += len(r.NewFiles)
	}

	fmt.Println()
	switch {
	case removed > 0 || changed > 0:
		fmt.Printf("A PATCH IS NOT SUPPORTABLE on this evidence: %d removed, %d changed.\n", removed, changed)
		fmt.Println("Either the change is a documented break, or the surface should be restored.")
	default:
		fmt.Println("PATCH is supportable: nothing removed, no signature changed.")
	}
	if newFiles > 0 {
		fmt.Printf("\n%d new file(s) in the censused packages. The versioning rule asks whether the\n", newFiles)
		fmt.Println("core packages changed SHAPE — a new on-disk piece is what that means, and it is")
		fmt.Println("the clause that made v0.132.0 a minor. Read them before settling on a patch.")
	}
	fmt.Println("\nThis is evidence, not a verdict. docs/plans/release-process.md has the rule.")
}
