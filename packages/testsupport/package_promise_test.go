package testsupport

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A package that .api/packages.txt lists as unstable says so where a host reads
// it: its package doc carries a paragraph that opens with "No promise:".
// A stable package must not, because the two would contradict each other.
//
// Decision 0026 draws the line in the manifest. A host reads godoc, not the
// manifest, so without this the manifest could move and the docs would not.
// Unlisted packages are out of scope. The SDK's package doc says that a
// package the manifest does not list promises nothing.
func TestPackageDocsMatchTheManifestPromise(t *testing.T) {
	f, err := os.Open("../../.api/packages.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	seen := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("packages.txt: want \"<class> <package>\", got %q", line)
		}
		class, pkg := fields[0], fields[1]
		doc := packageDoc(t, filepath.Join("../..", pkg))
		says := hasParagraph(doc, "No promise:")
		switch class {
		case "unstable":
			if !says {
				t.Errorf("%s is listed unstable, but its package doc has no \"No promise:\" paragraph", pkg)
			}
		case "stable":
			if says {
				t.Errorf("%s is listed stable, but its package doc says \"No promise:\"", pkg)
			}
		default:
			t.Fatalf("packages.txt: unknown class %q for %s", class, pkg)
		}
		seen++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	// 🔑 A manifest that parsed to nothing would pass every check above.
	if seen < 10 {
		t.Fatalf("read %d packages from packages.txt; the manifest lists more than that", seen)
	}
}

// packageDoc returns the text of the package doc in dir, the comment directly
// above a non-test file's package clause.
func packageDoc(t *testing.T, dir string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var docs []*ast.CommentGroup
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.PackageClauseOnly|parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		if file.Doc != nil {
			docs = append(docs, file.Doc)
		}
	}
	if len(docs) == 0 {
		t.Fatalf("%s: no package doc", dir)
	}
	var b strings.Builder
	for _, d := range docs {
		b.WriteString(d.Text())
		b.WriteString("\n")
	}
	return b.String()
}

// hasParagraph reports whether doc has a paragraph that opens with prefix.
func hasParagraph(doc, prefix string) bool {
	start := true
	for _, line := range strings.Split(doc, "\n") {
		if start && strings.HasPrefix(line, prefix) {
			return true
		}
		start = strings.TrimSpace(line) == ""
	}
	return false
}
