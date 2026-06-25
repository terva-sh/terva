package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func writeCtxFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadStartupContextFilesOrderAndDedup(t *testing.T) {
	dir := testsupport.TempDir(t)
	writeCtxFile(t, filepath.Join(dir, "a.md"), "AAA")
	writeCtxFile(t, filepath.Join(dir, "b.md"), "BBB")

	// config provides a.md; flags provide b.md then a.md again (a dup).
	out, err := readStartupContextFiles(dir,
		[]string{filepath.Join(dir, "a.md")},
		[]string{"b.md", "a.md"})
	if err != nil {
		t.Fatal(err)
	}
	ia, ib := strings.Index(out, "AAA"), strings.Index(out, "BBB")
	if ia < 0 || ib < 0 {
		t.Fatalf("missing content:\n%s", out)
	}
	if ia > ib {
		t.Fatalf("config file should precede flag file:\n%s", out)
	}
	if n := strings.Count(out, "AAA"); n != 1 {
		t.Fatalf("duplicate not deduped: AAA appears %d times", n)
	}
}

func TestReadStartupContextFilesRelativeFlagResolvesAgainstCwd(t *testing.T) {
	dir := testsupport.TempDir(t)
	writeCtxFile(t, filepath.Join(dir, "sub", "c.md"), "CCC")
	out, err := readStartupContextFiles(dir, nil, []string{"sub/c.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "CCC") {
		t.Fatalf("relative flag not loaded:\n%s", out)
	}
	if !strings.Contains(out, filepath.Join(dir, "sub", "c.md")) {
		t.Fatalf("expected absolute path heading:\n%s", out)
	}
}

func TestReadStartupContextFilesMissingErrors(t *testing.T) {
	dir := testsupport.TempDir(t)
	_, err := readStartupContextFiles(dir, nil, []string{"nope.md"})
	if err == nil {
		t.Fatal("expected error for missing context file")
	}
}

func TestReadStartupContextFilesDirectoryErrors(t *testing.T) {
	dir := testsupport.TempDir(t)
	sub := filepath.Join(dir, "adir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := readStartupContextFiles(dir, []string{sub}, nil)
	if err == nil {
		t.Fatal("expected error when a context file is a directory")
	}
}

func TestReadStartupContextFilesEmptySkipped(t *testing.T) {
	dir := testsupport.TempDir(t)
	writeCtxFile(t, filepath.Join(dir, "blank.md"), "   \n\t  ")
	out, err := readStartupContextFiles(dir, []string{filepath.Join(dir, "blank.md")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("whitespace-only file should be skipped, got:\n%s", out)
	}
}

func TestReadStartupContextFilesOversizeErrors(t *testing.T) {
	dir := testsupport.TempDir(t)
	big := filepath.Join(dir, "big.md")
	writeCtxFile(t, big, strings.Repeat("x", maxContextFileBytes+1))
	_, err := readStartupContextFiles(dir, []string{big}, nil)
	if err == nil {
		t.Fatal("expected error for oversize context file")
	}
}

// TestReadStartupContextFilesFlagAbsolutePathAccepted: the --context-file flag
// is user-supplied and TRUSTED, so an absolute path passed via flagFiles is
// loaded normally — the project-layer containment check does not touch it.
func TestReadStartupContextFilesFlagAbsolutePathAccepted(t *testing.T) {
	cwd := testsupport.TempDir(t)
	outside := filepath.Join(testsupport.TempDir(t), "team-rules.md")
	writeCtxFile(t, outside, "RULES")

	out, err := readStartupContextFiles(cwd, nil, []string{outside})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "RULES") {
		t.Fatalf("absolute --context-file not loaded:\n%s", out)
	}
	if !strings.Contains(out, outside) {
		t.Fatalf("expected absolute path heading for flag file:\n%s", out)
	}
}

func TestReadStartupContextFilesNoneReturnsEmpty(t *testing.T) {
	out, err := readStartupContextFiles(testsupport.TempDir(t), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("expected empty, got:\n%s", out)
	}
}
