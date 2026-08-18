package build

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// A doc comment that says "X implements pkg.Iface" is a claim the compiler can
// check in one line, and the codebase already checks it in fifty places
// (`var _ pkg.Iface = (*T)(nil)`). Where the line is missing, the claim can
// outlive the wiring — silently, because nothing reads a comment.
//
// It had. *Interactive carried "HostHooks implementation for the extension
// manager. The manager holds an interface, not a concrete *Interactive, so
// these methods are the only thing the manager sees." The manager never
// received one, the type was two methods short of the interface, and one
// method's signature had missed a parameter the interface gained. Five of the
// methods under that heading had no caller anywhere, in production or test.
//
// This gate refuses a doc-comment claim with no assertion behind it.
//
// Keyed on DOC POSITION, not on the text appearing anywhere in the file: the
// claim must be the doc comment of a declaration, and must name that
// declaration (Go doc convention, "Foo implements ..."). ext/connector.go says
// "the author implements connsdk.Transport" about the human writing an
// extension, and a text scan fires on it.
var implementsClaim = regexp.MustCompile(`\b(\w+) implements ([a-z][\w]*)\.([A-Z]\w+)`)

func TestEveryImplementsClaimIsHeldByTheCompiler(t *testing.T) {
	root := filepath.Join("..", "..", "..")

	type claim struct {
		file, decl, iface string
		line              int
	}
	var claims []claim
	assertions := map[string]map[string]bool{} // file -> "pkg.Iface" -> present
	scanned := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if testsupport.SkipScanDir(root, path, d) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			return nil
		}
		scanned++

		assertions[rel] = map[string]bool{}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "_" || vs.Type == nil {
					continue
				}
				if sel, ok := vs.Type.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok {
						assertions[rel][pkg.Name+"."+sel.Sel.Name] = true
					}
				}
			}
		}

		record := func(doc *ast.CommentGroup, name string, pos token.Pos) {
			if doc == nil {
				return
			}
			for _, m := range implementsClaim.FindAllStringSubmatch(doc.Text(), -1) {
				// The claim must be ABOUT this declaration: Go doc comments open
				// with the declared name, and a method's claim is about its
				// receiver, whose name the method's own name is not — so accept
				// either the decl name or a trailing match on the receiver.
				if m[1] != name {
					continue
				}
				claims = append(claims, claim{rel, name, m[2] + "." + m[3], fset.Position(pos).Line})
			}
		}
		for _, d := range f.Decls {
			switch v := d.(type) {
			case *ast.FuncDecl:
				record(v.Doc, v.Name.Name, v.Pos())
			case *ast.GenDecl:
				if v.Tok != token.TYPE {
					continue
				}
				for _, spec := range v.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					doc := ts.Doc
					if doc == nil {
						doc = v.Doc
					}
					record(doc, ts.Name.Name, ts.Pos())
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 500 {
		t.Fatalf("scanned only %d Go files; the walk is broken and this proves nothing", scanned)
	}
	if len(claims) < 5 {
		t.Fatalf("found %d \"implements pkg.Iface\" doc claims; the matcher is broken", len(claims))
	}

	for _, c := range claims {
		if assertions[c.file][c.iface] {
			continue
		}
		t.Errorf("%s:%d: %s's doc says it implements %s, and nothing checks that. Add "+
			"`var _ %s = (*%s)(nil)` in this file — the claim is either true, in which case the "+
			"line is free, or it is false, in which case it has been misleading readers.",
			c.file, c.line, c.decl, c.iface, c.iface, receiverFor(c.decl, c.file))
	}
}

// receiverFor is a best-effort hint for the error message only.
func receiverFor(decl, file string) string {
	if strings.Contains(file, "interactive") {
		return "Interactive"
	}
	return decl
}
