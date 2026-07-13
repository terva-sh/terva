package ignore

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func writeGI(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIgnored(t *testing.T) {
	root := testsupport.TempDir(t)
	writeGI(t, filepath.Join(root, ".gitignore"), "sessions/\n*.log\n/build\n")
	// A nested per-directory .gitignore deeper in the tree.
	writeGI(t, filepath.Join(root, "pkg", ".gitignore"), "vendor/\n")

	cases := []struct {
		name string
		path string
		want bool
	}{
		// The crux: a dir-only rule ("sessions/") hides a file via an ANCESTOR
		// directory, which a single leaf-path Match would miss.
		{"dir-only via ancestor", filepath.Join(root, "src", "features", "sessions", "x.tsx"), true},
		{"sibling of ignored dir", filepath.Join(root, "src", "features", "other", "x.tsx"), false},
		{"basename rule", filepath.Join(root, "a", "b", "debug.log"), true},
		{"anchored rule at root", filepath.Join(root, "build", "out.js"), true},
		{"anchored rule not at root", filepath.Join(root, "sub", "build", "out.js"), false},
		{"nested gitignore", filepath.Join(root, "pkg", "vendor", "dep.go"), true},
		{"ordinary source file", filepath.Join(root, "src", "main.go"), false},
		{"path outside root", filepath.Join(testsupport.TempDir(t), "x.go"), false},
	}
	for _, c := range cases {
		if got := Ignored(root, c.path); got != c.want {
			t.Errorf("%s: Ignored(%s) = %v, want %v", c.name, c.path, got, c.want)
		}
	}

	// No .gitignore anywhere: ignore nothing (non-git safe, no false positives).
	bare := testsupport.TempDir(t)
	if Ignored(bare, filepath.Join(bare, "sessions", "x.tsx")) {
		t.Error("a workspace with no .gitignore should ignore nothing")
	}
}
