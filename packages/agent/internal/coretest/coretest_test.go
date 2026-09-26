package coretest

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

// This package hands out agents a test does not need to gate. A production
// file that imported it would get the same agent without ever writing a gate
// choice, which is the silent ungated host the constructor argument exists to
// prevent. So only test files may import it, and this walk holds that true.
func TestOnlyTestFilesImportCoretest(t *testing.T) {
	const self = "terva.sh/terva/packages/agent/internal/coretest"
	root := filepath.Join("..", "..", "..", "..")
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
			t.Errorf("%s imports coretest outside a test. Build the agent through build.Resolved.NewAgent, "+
				"or core.New with a gate you chose", filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Positive control: the migration put about sixty test files on this
	// package. A walk that found none proves nothing about the rule above.
	if testImporters < 20 {
		t.Fatalf("found %d test files importing coretest, want at least 20; the walk is broken", testImporters)
	}
}
