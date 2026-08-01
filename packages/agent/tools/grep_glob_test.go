package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// seedTree writes a small fixture tree under a temp dir and returns it.
func seedTree(t *testing.T) string {
	t.Helper()
	dir := testsupport.TempDir(t)
	files := map[string]string{
		"main.go":            "package main\nfunc main() { hello() }\n",
		"pkg/util.go":        "package pkg\nfunc hello() {}\n// TODO: tidy\n",
		"pkg/util_test.go":   "package pkg\nfunc TestHello() {}\n",
		"docs/readme.md":     "# Title\nhello world\n",
		"build/generated.go": "package build\nfunc hello() {}\n",
		".gitignore":         "build/\n*.log\n",
		"noise.log":          "hello from a log\n",
	}
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func grepText(t *testing.T, dir string, args map[string]any) (string, map[string]any) {
	t.Helper()
	tool := &GrepTool{CWD: dir}
	res, err := tool.Execute(context.Background(), mustJSON(t, args), nil)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	return res.Content[0].(provider.TextBlock).Text, res.Details.(map[string]any)
}

func globText(t *testing.T, dir string, args map[string]any) (string, map[string]any) {
	t.Helper()
	tool := &GlobTool{CWD: dir}
	res, err := tool.Execute(context.Background(), mustJSON(t, args), nil)
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return res.Content[0].(provider.TextBlock).Text, res.Details.(map[string]any)
}

func TestGlobRecursiveAndTopLevel(t *testing.T) {
	dir := seedTree(t)

	out, _ := globText(t, dir, map[string]any{"pattern": "**/*.go"})
	for _, want := range []string{"main.go", "pkg/util.go", "pkg/util_test.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("recursive glob missing %q in:\n%s", want, out)
		}
	}
	// build/ is gitignored -> excluded by default.
	if strings.Contains(out, "build/generated.go") {
		t.Errorf("gitignored build/ should be excluded:\n%s", out)
	}

	// Top-level pattern matches only the root file.
	out, _ = globText(t, dir, map[string]any{"pattern": "*.go"})
	if !strings.Contains(out, "main.go") || strings.Contains(out, "pkg/util.go") {
		t.Errorf("top-level *.go should match only main.go:\n%s", out)
	}
}

func TestGlobIncludeIgnored(t *testing.T) {
	dir := seedTree(t)
	out, _ := globText(t, dir, map[string]any{"pattern": "**/*.go", "include_ignored": true})
	if !strings.Contains(out, "build/generated.go") {
		t.Errorf("include_ignored should surface build/generated.go:\n%s", out)
	}
}

func TestGlobDeterministicOrder(t *testing.T) {
	dir := seedTree(t)
	out1, _ := globText(t, dir, map[string]any{"pattern": "**/*.go"})
	out2, _ := globText(t, dir, map[string]any{"pattern": "**/*.go"})
	if out1 != out2 {
		t.Errorf("glob ordering not deterministic:\n%s\n---\n%s", out1, out2)
	}
}

func TestGlobPaging(t *testing.T) {
	dir := seedTree(t)
	out, det := globText(t, dir, map[string]any{"pattern": "**/*.go", "max_results": 1})
	if det["truncated"] != true {
		t.Errorf("expected truncated with max_results=1, details=%v", det)
	}
	if strings.Count(strings.TrimSpace(out), "\n") < 0 { // sanity
		t.Fatal("unexpected")
	}
	next, ok := det["next_offset"].(int)
	if !ok || next != 1 {
		t.Errorf("next_offset want 1, got %v", det["next_offset"])
	}
	// Second page differs from first.
	out2, _ := globText(t, dir, map[string]any{"pattern": "**/*.go", "max_results": 1, "offset": next})
	if firstLine(out) == firstLine(out2) {
		t.Errorf("paging returned same first line: %q", firstLine(out))
	}
}

func TestGrepBasic(t *testing.T) {
	dir := seedTree(t)
	out, det := grepText(t, dir, map[string]any{"pattern": "func hello"})
	if !strings.Contains(out, "pkg/util.go:2:") {
		t.Errorf("grep missing pkg/util.go match:\n%s", out)
	}
	// build/ is gitignored; its hello() must not appear.
	if strings.Contains(out, "build/generated.go") {
		t.Errorf("gitignored file matched:\n%s", out)
	}
	if det["files_scanned"].(int) == 0 {
		t.Error("files_scanned should be > 0")
	}
}

func TestGrepCaseInsensitiveByDefault(t *testing.T) {
	dir := seedTree(t)
	out, _ := grepText(t, dir, map[string]any{"pattern": "HELLO"})
	if !strings.Contains(out, "hello") {
		t.Errorf("default search should be case-insensitive:\n%s", out)
	}
	// Case-sensitive should not match lowercase hello via uppercase pattern.
	out, _ = grepText(t, dir, map[string]any{"pattern": "HELLO", "case_sensitive": true})
	if strings.Contains(out, "hello") {
		t.Errorf("case_sensitive should not match lowercase:\n%s", out)
	}
}

func TestGrepGlobFilter(t *testing.T) {
	dir := seedTree(t)
	out, _ := grepText(t, dir, map[string]any{"pattern": "hello", "glob": "**/*_test.go"})
	if strings.Contains(out, "main.go") || strings.Contains(out, "util.go:") {
		t.Errorf("glob filter should restrict to *_test.go:\n%s", out)
	}
}

func TestGrepContext(t *testing.T) {
	dir := seedTree(t)
	out, _ := grepText(t, dir, map[string]any{"pattern": "TODO", "context": 1})
	// Context line above the match (dash separator) should be present.
	if !strings.Contains(out, "pkg/util.go-2-") || !strings.Contains(out, "pkg/util.go:3:") {
		t.Errorf("context render unexpected:\n%s", out)
	}
}

func TestGrepSingleFile(t *testing.T) {
	dir := seedTree(t)
	out, _ := grepText(t, dir, map[string]any{"pattern": "hello", "path": "noise.log"})
	// A file named explicitly is searched even though *.log is gitignored.
	if !strings.Contains(out, "noise.log:1:") {
		t.Errorf("named gitignored file should still be searched:\n%s", out)
	}
}

func TestGrepBinarySkipped(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "bin"), []byte("hel\x00lo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "txt.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := grepText(t, dir, map[string]any{"pattern": "hel"})
	if strings.Contains(out, "bin:") {
		t.Errorf("binary file should be skipped:\n%s", out)
	}
	if !strings.Contains(out, "txt.txt:1:") {
		t.Errorf("text file should match:\n%s", out)
	}
}

func TestGrepInvalidRegex(t *testing.T) {
	dir := seedTree(t)
	tool := &GrepTool{CWD: dir}
	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"pattern": "("}), nil)
	if err == nil {
		t.Error("expected error for invalid regex")
	}
}

// grep and glob are read tools, so a locked sandbox no longer confines them to
// the root — bash was never confined either, and refusing them only cost turns
// (B1, docs/reviews/2026-07-30-session-harness-friction-review.md). What they
// must still refuse is a registered secret root.
func TestGrepGlobReadOutsideJailButNotSecrets(t *testing.T) {
	dir := seedTree(t)
	sub := filepath.Join(dir, "pkg")
	secret := filepath.Join(dir, "docs")
	sb := NewSandbox(sub)
	sb.AddSecretRoot(secret)
	sb.Lock()

	g := &GrepTool{CWD: sub, Sandbox: sb}
	if _, err := g.Execute(context.Background(), mustJSON(t, map[string]any{"pattern": "hello", "path": ".."}), nil); err != nil {
		t.Errorf("grep outside the jail root should be allowed: %v", err)
	}
	gl := &GlobTool{CWD: sub, Sandbox: sb}
	if _, err := gl.Execute(context.Background(), mustJSON(t, map[string]any{"pattern": "*", "path": ".."}), nil); err != nil {
		t.Errorf("glob outside the jail root should be allowed: %v", err)
	}

	// The secret root is still refused, to both.
	if _, err := g.Execute(context.Background(), mustJSON(t, map[string]any{"pattern": "hello", "path": "../docs"}), nil); err == nil {
		t.Error("grep should refuse a secret root")
	}
	if _, err := gl.Execute(context.Background(), mustJSON(t, map[string]any{"pattern": "*", "path": "../docs"}), nil); err == nil {
		t.Error("glob should refuse a secret root")
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "pkg/util.go", false},
		{"**/*.go", "pkg/util.go", true},
		{"**/*.go", "main.go", true},
		{"pkg/**", "pkg/a/b.go", true},
		{"src/**/test_*.py", "src/a/b/test_x.py", true},
		{"src/**/test_*.py", "src/test_x.py", true},
		{"**/foo", "a/b/foo", true},
		{"a/*/c", "a/b/c", true},
		{"a/*/c", "a/b/d/c", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.name); got != c.want {
			t.Errorf("matchGlob(%q,%q)=%v want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
