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
	"path/filepath"
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
}

type symbol struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Sig  string `json:"sig,omitempty"`
}

type change struct {
	Symbol symbol `json:"symbol"`
	Was    string `json:"was,omitempty"`
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
	)
	flag.Parse()

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

	baseSyms, err := census(baseFiles)
	if err != nil {
		return report, fmt.Errorf("parsing %s at %s: %w", pkg, base, err)
	}
	headSyms, err := census(headFiles)
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
	report.BaseSyms, report.HeadSyms = len(baseSyms), len(headSyms)

	for name, sym := range headSyms {
		prev, ok := baseSyms[name]
		switch {
		case !ok:
			report.Added = append(report.Added, sym)
		case prev.Sig != sym.Sig:
			report.Changed = append(report.Changed, change{Symbol: sym, Was: prev.Sig})
		}
	}
	for name, sym := range baseSyms {
		if _, ok := headSyms[name]; !ok {
			report.Removed = append(report.Removed, sym)
		}
	}

	// A new file in one of these packages is the signal the versioning rule
	// calls "the core packages changed shape" — the escalation clause that
	// turned v0.132.0 into a minor. Added symbols alone do not show it: a new
	// method on an existing type is routine, a whole new file is a new piece.
	for path := range headFiles {
		if _, ok := baseFiles[path]; !ok {
			report.NewFiles = append(report.NewFiles, path)
		}
	}

	sortSymbols(report.Added)
	sortSymbols(report.Removed)
	sort.Slice(report.Changed, func(i, j int) bool { return report.Changed[i].Symbol.Name < report.Changed[j].Symbol.Name })
	sort.Strings(report.NewFiles)
	return report, nil
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

// isSource keeps Go sources that are part of the package's surface. Tests are
// out: they compile into no consumer's build.
func isSource(path string) bool {
	return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
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
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, files[path], parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				addFunc(fset, syms, d)
			case *ast.GenDecl:
				addGenDecl(fset, syms, d)
			}
		}
	}
	return syms, nil
}

func addFunc(fset *token.FileSet, syms map[string]symbol, decl *ast.FuncDecl) {
	if !decl.Name.IsExported() {
		return
	}
	name := decl.Name.Name
	kind := "func"
	if decl.Recv != nil && len(decl.Recv.List) == 1 {
		recv := receiverName(decl.Recv.List[0].Type)
		if recv == "" || !ast.IsExported(recv) {
			return // a method on an unexported type is not reachable
		}
		name = recv + "." + name
		kind = "method"
	}
	syms[name] = symbol{Name: name, Kind: kind, Sig: render(fset, decl.Type)}
}

func addGenDecl(fset *token.FileSet, syms map[string]symbol, decl *ast.GenDecl) {
	for _, spec := range decl.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if !s.Name.IsExported() {
				continue
			}
			syms[s.Name.Name] = symbol{Name: s.Name.Name, Kind: "type", Sig: typeSig(fset, s)}
			addMembers(fset, syms, s)
		case *ast.ValueSpec:
			kind := "var"
			if decl.Tok == token.CONST {
				kind = "const"
			}
			for _, name := range s.Names {
				if !name.IsExported() {
					continue
				}
				// No signature: a const's VALUE changing is not an API break,
				// and syntactic parsing cannot always see its type. Presence is
				// the property worth tracking.
				syms[name.Name] = symbol{Name: name.Name, Kind: kind}
			}
		}
	}
}

func addMembers(fset *token.FileSet, syms map[string]symbol, spec *ast.TypeSpec) {
	switch t := spec.Type.(type) {
	case *ast.StructType:
		for _, field := range t.Fields.List {
			for _, name := range field.Names {
				if !name.IsExported() {
					continue
				}
				full := spec.Name.Name + "." + name.Name
				syms[full] = symbol{Name: full, Kind: "field", Sig: render(fset, field.Type)}
			}
		}
	case *ast.InterfaceType:
		for _, method := range t.Methods.List {
			for _, name := range method.Names {
				if !name.IsExported() {
					continue
				}
				full := spec.Name.Name + "." + name.Name
				syms[full] = symbol{Name: full, Kind: "method", Sig: render(fset, method.Type)}
			}
		}
	}
}

// typeSig keeps a composite type's own signature coarse, because its members
// are censused separately.
func typeSig(fset *token.FileSet, spec *ast.TypeSpec) string {
	switch spec.Type.(type) {
	case *ast.StructType:
		return "struct"
	case *ast.InterfaceType:
		return "interface"
	}
	return render(fset, spec.Type)
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

func render(fset *token.FileSet, node ast.Node) string {
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
