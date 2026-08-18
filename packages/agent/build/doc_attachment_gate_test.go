package build

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// A doc comment must document the thing it sits on.
//
// Go's convention — open with the declared name — makes the failure detectable:
// a comment on Y that opens "X ..." where X is declared LATER in the same file
// is X's contract filed under Y, and X reads as undocumented. It happens by
// insertion: someone adds a function between an existing doc comment and its
// function, the two comment blocks merge into one group, and nothing complains
// because comments have no compiler.
//
// It had happened 29 times. MergeToolsForMode's contract sat on
// ExtToolReadOnly; OpenSession's and Prompt's one-line summaries were stranded
// as duplicates on unrelated types. One of the 29 was mine, added six commits
// earlier in this same review by putting maskRunes between Render's doc and
// Render.
//
// Non-test files only. In a test file the heuristic is genuinely ambiguous —
// "// Close releases the snapshot" over TestCloseReleasesSnapshot is correct
// style, not a mistake — and a gate that fires on correct code gets suppressed.
//
// A grouped declaration is skipped for the same reason: a shared doc above
// `type ( A; B; C )` legitimately opens with the first member's name.
func TestEveryDocCommentSitsOnWhatItDocuments(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	scanned := 0
	var offenders []string

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

		// Every name this file declares, and where.
		declLine := map[string]int{}
		for _, dec := range f.Decls {
			switch v := dec.(type) {
			case *ast.FuncDecl:
				declLine[v.Name.Name] = fset.Position(v.Pos()).Line
			case *ast.GenDecl:
				for _, s := range v.Specs {
					if ts, ok := s.(*ast.TypeSpec); ok {
						declLine[ts.Name.Name] = fset.Position(ts.Pos()).Line
					}
				}
			}
		}

		check := func(doc *ast.CommentGroup, own string, pos token.Pos) {
			if doc == nil {
				return
			}
			fields := strings.Fields(strings.TrimSpace(doc.Text()))
			if len(fields) < 2 || fields[0] == own {
				return
			}
			at, declared := declLine[fields[0]]
			if !declared || at <= fset.Position(pos).Line {
				return
			}
			offenders = append(offenders, rel+":"+itoa(fset.Position(doc.Pos()).Line)+
				": the doc on "+own+" opens \""+fields[0]+" "+fields[1]+" ...\" and "+
				fields[0]+" is declared below at line "+itoa(at)+". Move that block down to it — "+
				"as written, "+fields[0]+" is undocumented and "+own+" carries a contract that is "+
				"not its own.")
		}
		for _, dec := range f.Decls {
			switch v := dec.(type) {
			case *ast.FuncDecl:
				check(v.Doc, v.Name.Name, v.Pos())
			case *ast.GenDecl:
				if len(v.Specs) != 1 {
					continue // grouped decl: a shared doc names its first member
				}
				if ts, ok := v.Specs[0].(*ast.TypeSpec); ok {
					doc := ts.Doc
					if doc == nil {
						doc = v.Doc
					}
					check(doc, ts.Name.Name, ts.Pos())
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 500 {
		t.Fatalf("scanned only %d non-test Go files; the walk is broken and this proves nothing", scanned)
	}
	for _, o := range offenders {
		t.Error(o)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
