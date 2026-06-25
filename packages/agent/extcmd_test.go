package agent

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// TestExtInstallDotSource verifies that `terva ext install .` derives the
// extension name from the resolved directory name rather than collapsing
// to the extensions/ parent directory (the false "already exists" bug).
func TestExtInstallDotSource(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	// Pre-create extensions/ to mimic a normal first run.
	if err := os.MkdirAll(filepath.Join(home, "extensions"), 0o755); err != nil {
		t.Fatal(err)
	}

	srcParent := testsupport.TempDir(t)
	src := filepath.Join(srcParent, "kagi")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "extension.json"), []byte(`{"name":"kagi"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	if err := os.Chdir(src); err != nil {
		t.Fatal(err)
	}

	if err := extInstall([]string{"."}); err != nil {
		t.Fatalf("install with '.' failed: %v", err)
	}

	out := filepath.Join(home, "extensions", "kagi")
	if _, err := os.Stat(filepath.Join(out, "extension.json")); err != nil {
		t.Fatalf("expected installed extension at %s: %v", out, err)
	}
}

// TestExtInstallRejectsParentName guards against deriving a name of ".."
// from a source that resolves to a filesystem root edge case. A normal
// directory always yields a real basename, so this just ensures the
// guard logic does not crash for well-formed input.
func TestExtInstallNamedDir(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	src := filepath.Join(testsupport.TempDir(t), "myext")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "extension.json"), []byte(`{"name":"myext"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := extInstall([]string{src}); err != nil {
		t.Fatalf("install failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "extensions", "myext", "extension.json")); err != nil {
		t.Fatalf("expected installed extension: %v", err)
	}
}

// TestCopyDirRespectsGitignore verifies that non-portable directories
// listed in the source .gitignore (e.g. .venv, node_modules) are not
// copied during install, while tracked files are.
func TestCopyDirRespectsGitignore(t *testing.T) {
	src := testsupport.TempDir(t)
	dst := filepath.Join(testsupport.TempDir(t), "out")

	mustWrite := func(rel, content string) {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite("extension.json", `{"name":"x"}`)
	mustWrite("main.py", "print('hi')")
	mustWrite(".gitignore", ".venv/\nnode_modules/\n*.log\n")
	mustWrite(".venv/bin/python", "binary")
	mustWrite("node_modules/pkg/index.js", "module")
	mustWrite("debug.log", "noise")
	mustWrite("src/app.py", "code")
	mustWrite(".git/config", "gitdir")

	if err := copyDir(src, dst); err != nil {
		t.Fatal(err)
	}

	wantPresent := []string{"extension.json", "main.py", "src/app.py", ".gitignore"}
	for _, rel := range wantPresent {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("expected %s to be copied: %v", rel, err)
		}
	}

	wantAbsent := []string{".venv", "node_modules", "debug.log", ".git"}
	for _, rel := range wantAbsent {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(rel))); err == nil {
			t.Fatalf("expected %s to be skipped, but it was copied", rel)
		}
	}
}

func TestGitignoreNegation(t *testing.T) {
	g := loadGitignoreFromString("build/\n!build/keep.txt\n")
	if !g.Match("build", true) {
		t.Fatal("expected build/ dir to be ignored")
	}
	if g.Match("build/keep.txt", false) {
		t.Fatal("expected build/keep.txt to be re-included by negation")
	}
}
