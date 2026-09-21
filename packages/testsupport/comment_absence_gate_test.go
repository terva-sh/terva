package testsupport

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A comment that says a symbol does not exist, while the tree declares it,
// tells a reader not to look, and so suppresses the search that would correct
// it. Two such comments about the ctrlproto auth verbs stood for six weeks;
// TKT-01M1RRT6J carries the chronology and the argument for this gate.
//
// The honest scope is narrow, and the ticket says why: "does not exist" is one
// phrasing among many, and the interesting version has to tell "this symbol is
// absent" from "this path may be absent". So the gate fires only when a
// comment names an identifier as the SUBJECT of an absence verb, and that
// identifier resolves in the package the comment sits in (or, for a qualified
// name, in the package it qualifies). "AddSecretRoot skips paths that do not
// exist yet" names AddSecretRoot but denies the paths, and stays quiet. A
// comment that denies a capability without naming it also stays quiet; the two
// motivating comments were in fact of that shape, and no gate over prose alone
// would have caught them.
//
// Comments are read through go/ast, as the pointer gate beside this one does,
// because a claim inside a string literal is data the program uses and not
// prose a reader believes.
func TestCommentAbsenceClaimsResolve(t *testing.T) {
	root := filepath.Join("..", "..")

	// Pass one: parse every file and index what each package declares.
	type parsed struct {
		rel  string
		dir  string
		file *ast.File
		fset *token.FileSet
	}
	var files []parsed
	byDir := map[string]*pkgIndex{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if SkipScanDir(root, path, d) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			// A file the parser rejects is a compile failure, which another
			// gate reports better than this one would.
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		dir := filepath.Dir(rel)
		idx := byDir[dir]
		if idx == nil {
			idx = newPkgIndex()
			byDir[dir] = idx
		}
		idx.add(f)
		files = append(files, parsed{rel, dir, f, fset})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	byPkgName := map[string][]*pkgIndex{}
	for _, idx := range byDir {
		byPkgName[idx.name] = append(byPkgName[idx.name], idx)
	}

	// Pass two: find the claims and ask each one whether it is still true.
	var offenders []absenceClaim
	for _, p := range files {
		for _, c := range absenceClaims(p.fset, p.file, byDir[p.dir], byPkgName) {
			c.file = p.rel
			offenders = append(offenders, c)
		}
	}

	sort.Slice(offenders, func(i, j int) bool {
		if offenders[i].file != offenders[j].file {
			return offenders[i].file < offenders[j].file
		}
		return offenders[i].line < offenders[j].line
	})
	for _, o := range offenders {
		t.Errorf("%s:%d: comment says %s is absent (%q), but %s declares it",
			o.file, o.line, o.name, o.claim, o.declaredIn)
	}
	if len(offenders) > 0 {
		t.Logf("The prose was probably true when written. Say what the symbol " +
			"does now rather than deleting the sentence, and if the comment " +
			"is about something the symbol still lacks, name that thing as " +
			"the subject instead of the symbol.")
	}
}

// absenceClaim is one comment sentence that names an identifier as the subject
// of an absence verb, where the identifier resolves.
type absenceClaim struct {
	file       string
	line       int
	name       string // the identifier as the comment wrote it, backticks removed
	claim      string // the matched clause, for the failure message
	declaredIn string // where the identifier resolved
}

// pkgIndex is what one package directory declares, as far as a comment could
// name it: top-level names, Type.Member for methods and fields, and the string
// values of constants and variables, because a wire verb such as
// "auth.login.start" is named in prose by its value, not by the Go constant
// that holds it.
type pkgIndex struct {
	name  string
	names map[string]bool
	strs  map[string]bool
}

func newPkgIndex() *pkgIndex {
	return &pkgIndex{names: map[string]bool{}, strs: map[string]bool{}}
}

func (x *pkgIndex) add(f *ast.File) {
	// A directory holding both foo and foo_test is indexed under foo, so a
	// claim in an external test resolves against the code it tests.
	if n := f.Name.Name; x.name == "" ||
		(strings.HasSuffix(x.name, "_test") && !strings.HasSuffix(n, "_test")) {
		x.name = n
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			x.names[d.Name.Name] = true
			if d.Recv != nil && len(d.Recv.List) == 1 {
				if r := receiverTypeName(d.Recv.List[0].Type); r != "" {
					x.names[r+"."+d.Name.Name] = true
				}
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					x.names[s.Name.Name] = true
					if st, ok := s.Type.(*ast.StructType); ok && st.Fields != nil {
						for _, fld := range st.Fields.List {
							for _, n := range fld.Names {
								x.names[s.Name.Name+"."+n.Name] = true
							}
						}
					}
					if it, ok := s.Type.(*ast.InterfaceType); ok && it.Methods != nil {
						for _, m := range it.Methods.List {
							for _, n := range m.Names {
								x.names[s.Name.Name+"."+n.Name] = true
							}
						}
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						x.names[n.Name] = true
					}
					for _, v := range s.Values {
						if lit, ok := v.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							x.strs[strings.Trim(lit.Value, "`\"")] = true
						}
					}
				}
			}
		}
	}
}

func receiverTypeName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.StarExpr:
		return receiverTypeName(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return receiverTypeName(e.X)
	case *ast.IndexListExpr:
		return receiverTypeName(e.X)
	}
	return ""
}

// The three shapes a comment uses to deny a named thing. Each captures the
// subject span; subjectRe then pulls the individual identifiers out of it, so
// "`a`, `b` and `c` do not exist yet" yields three claims.
//
// An identifier counts as a subject when the comment marks it as code: in
// backticks, or qualified with a dot, or an exported-looking CamelCase word.
// A capitalised sentence-initial word ("Until", "The") has no second capital
// and is not one. The verbs are the ones that deny existence outright.
// "does not support", "does not have" and "is not available" are left out on
// purpose: their subject exists and the comment is about something it lacks.
var (
	subjectPat = "(?:`[^`]+`|[A-Za-z_][A-Za-z0-9_]*(?:\\.[A-Za-z_][A-Za-z0-9_]*)+(?:\\(\\))?|[A-Z][a-z0-9]*[A-Z_][A-Za-z0-9_]*(?:\\(\\))?)"
	subjectRe  = regexp.MustCompile(subjectPat)

	absenceVerb = `(?:is|are|do|does)\s+(?:still\s+)?not\s+(?:yet\s+)?(?:exist|implemented|wired(?:\s+up)?|defined|declared|added|landed|built|shipped|present)\b` +
		`|(?:is|are)\s+(?:still\s+)?(?:unimplemented|missing|absent|a\s+stub)\b`

	// "`X` does not exist yet", "X and Y are not implemented"
	subjectVerbRe = regexp.MustCompile(
		`(` + subjectPat + `(?:,\s*` + subjectPat + `)*(?:,?\s+(?:and|or)\s+` + subjectPat + `)?)\s+(?:` + absenceVerb + `)`)

	// "there is no `X`", "there is no pkg.Verb yet"
	thereIsNoRe = regexp.MustCompile(
		`\b[Tt]here\s+(?:is|are)\s+(?:still\s+)?no\s+(` + "(?:`[^`]+`|[A-Za-z_][A-Za-z0-9_]*(?:\\.[A-Za-z_][A-Za-z0-9_]*)+)" + `)(?:\s+yet)?(?:[\s.,;:)]|$)`)

	// "no `X` yet", "no pkg.Verb exists"
	noYetRe = regexp.MustCompile(
		`\b[Nn]o\s+(` + "(?:`[^`]+`|[A-Za-z_][A-Za-z0-9_]*(?:\\.[A-Za-z_][A-Za-z0-9_]*)+)" + `)\s+(?:yet|exists)\b`)
)

// absenceClaims returns the claims in f's comments whose subject resolves.
// idx is the package f sits in; byPkgName resolves a qualified name against
// the package it names, wherever that package lives.
func absenceClaims(fset *token.FileSet, f *ast.File, idx *pkgIndex, byPkgName map[string][]*pkgIndex) []absenceClaim {
	owner := docOwners(f)
	var out []absenceClaim
	for _, cg := range f.Comments {
		text := strings.Join(strings.Fields(cg.Text()), " ")
		own := owner[cg]
		seen := map[string]bool{}
		consider := func(clause, raw string) {
			name := strings.TrimSuffix(strings.Trim(raw, "`"), "()")
			if name == "" || seen[name] || own[name] {
				return
			}
			seen[name] = true
			where := resolveName(name, idx, byPkgName)
			if where == "" {
				return
			}
			out = append(out, absenceClaim{
				line:       fset.Position(cg.Pos()).Line,
				name:       name,
				claim:      clause,
				declaredIn: where,
			})
		}
		for _, m := range subjectVerbRe.FindAllStringSubmatch(text, -1) {
			for _, s := range subjectRe.FindAllString(m[1], -1) {
				consider(m[0], s)
			}
		}
		for _, m := range thereIsNoRe.FindAllStringSubmatch(text, -1) {
			consider(m[0], m[1])
		}
		for _, m := range noYetRe.FindAllStringSubmatch(text, -1) {
			consider(m[0], m[1])
		}
	}
	return out
}

// docOwners maps each doc comment to the names it documents. A doc comment
// that says "Foo is not implemented on this platform" sits on a stub called
// Foo, and the existence of Foo is not what it denies.
func docOwners(f *ast.File) map[*ast.CommentGroup]map[string]bool {
	out := map[*ast.CommentGroup]map[string]bool{}
	add := func(cg *ast.CommentGroup, names ...string) {
		if cg == nil {
			return
		}
		if out[cg] == nil {
			out[cg] = map[string]bool{}
		}
		for _, n := range names {
			out[cg][n] = true
		}
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			add(d.Doc, d.Name.Name)
			if d.Recv != nil && len(d.Recv.List) == 1 {
				if r := receiverTypeName(d.Recv.List[0].Type); r != "" {
					add(d.Doc, r+"."+d.Name.Name)
				}
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					add(d.Doc, s.Name.Name)
					add(s.Doc, s.Name.Name)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						add(d.Doc, n.Name)
						add(s.Doc, n.Name)
						add(s.Comment, n.Name)
					}
				}
			}
		}
	}
	return out
}

// resolveName reports where name is declared, or "" when it is not.
//
// A bare name resolves only in its own package. A qualified name resolves
// first against a package called by its qualifier, anywhere in the tree, then
// as Type.Member in its own package. Any name resolves as a string value in
// its own package, which is how a wire verb is found. A bare name declared in
// some OTHER package is not a hit: comments qualify names when they mean
// another package, and a gate that guessed otherwise would fire on prose.
func resolveName(name string, idx *pkgIndex, byPkgName map[string][]*pkgIndex) string {
	if idx == nil {
		return ""
	}
	if i := strings.Index(name, "."); i > 0 {
		q, sym := name[:i], name[i+1:]
		for _, other := range byPkgName[q] {
			if other.names[sym] {
				return "package " + q
			}
		}
	}
	if idx.names[name] {
		return "package " + idx.name
	}
	if idx.strs[name] {
		return "a string value in package " + idx.name
	}
	return ""
}
