package transcripttest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// This package builds an agent with core.AllowAll and a scripted client, which
// is right for testing a store and wrong anywhere else: a production file that
// imported it would get an ungated agent without ever writing a gate choice.
// So only test files may import it, and this walk holds that true. The host
// census in packages/agent relies on it.
func TestOnlyTestFilesImportTranscripttest(t *testing.T) {
	const self = "terva.sh/terva/packages/core/transcripttest"
	root := filepath.Join("..", "..", "..")
	testImporters := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if testsupport.SkipScanDir(root, path, d) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ImportsOnly)
		if err != nil {
			return nil // a file the parser rejects fails the build, which says it better
		}
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p != self {
				continue
			}
			if strings.HasSuffix(path, "_test.go") {
				testImporters++
				continue
			}
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s imports transcripttest outside a test; it is a behavior suite, not a library", filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Positive control: core and build both run the suite. A walk that found
	// neither proves nothing about the rule above.
	if testImporters < 2 {
		t.Fatalf("found %d test files importing transcripttest, want at least 2; the walk is broken", testImporters)
	}
}
