package testsupport

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoRawTempDirInTests enforces that test files use testsupport.TempDir
// instead of the standard-library t.TempDir helper.
//
// On Windows a file that still has an open handle cannot be deleted, so the
// standard helper's automatic RemoveAll cleanup fails the test whenever a
// just-written file under the dir (a session .jsonl, a connector .log, an
// extension file) is held at the cleanup instant — a racing async close, or
// Defender / the search indexer scanning a fresh file. That is the chronic
// source of red Windows CI runs, and the internal CI has no Windows matrix, so
// it only ever surfaces after a public release. testsupport.TempDir is a
// drop-in that rides out that window and never fails the run on a residual
// handle (see tmpdir.go).
//
// This gate keeps the fix from regressing. Because it is an ordinary test it
// runs inside `go test ./...`, so a new raw call is caught pre-merge on any
// platform — no Windows runner required. The one legitimate raw call lives
// inside testsupport.TempDir itself, which is not a _test.go file and so is
// never scanned.
func TestNoRawTempDirInTests(t *testing.T) {
	// Concatenated so this file's own text does not trip the scan. Any
	// receiver.TempDir with EMPTY parens is a raw testing.T/B call —
	// testsupport.TempDir always takes an argument, so it never appears with
	// empty parens.
	needle := ".TempDir" + "()"

	root := filepath.Join("..", "..")
	var offenders []string
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
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(data), "\n") {
			// Drop any line comment so a doc comment may reference the banned
			// call by name without tripping the gate (this file does so above).
			code := line
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx]
			}
			if strings.Contains(code, needle) {
				offenders = append(offenders, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("raw standard-library TempDir in test files — use testsupport.TempDir(t) for Windows-tolerant cleanup:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
