package agent

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

// extUpgrade for a missing name is an error. (The git-pull mechanics are
// covered by updateOneExtension's tests in extupdate_test.go; here we
// exercise the name-resolution wrapper.)
func TestExtUpgradeNotFound(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "extensions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extUpgrade([]string{"does-not-exist"}); err == nil {
		t.Fatal("expected an error upgrading a missing extension")
	}
}

func TestExtUpgradeNoArgs(t *testing.T) {
	if err := extUpgrade(nil); err == nil {
		t.Fatal("expected a usage error with no names")
	}
}

// A non-git install resolves but reports "skipped: not a git checkout",
// which is not a failure — extUpgrade returns nil.
func TestExtUpgradeSkipsNonGit(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	ext := filepath.Join(home, "extensions", "localext")
	if err := os.MkdirAll(ext, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ext, "extension.json"), []byte(`{"name":"localext"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extUpgrade([]string{"localext"}); err != nil {
		t.Fatalf("non-git extension should skip cleanly, got %v", err)
	}
}

// The name shown in `ext list` is the manifest name, which can differ from
// the install-directory basename (e.g. dir "terva-ext-index" for manifest
// name "index" after a pack install). findExtensionDir must resolve BOTH
// so `ext upgrade index` works exactly as the list shows it.
func TestFindExtensionDirByManifestNameOrDir(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	dir := filepath.Join(home, "extensions", "terva-ext-index")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(`{"name":"index"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"index", "terva-ext-index"} {
		got, err := findExtensionDir(name)
		if err != nil || got != dir {
			t.Fatalf("findExtensionDir(%q) = %q, %v; want %q", name, got, err, dir)
		}
	}
	if _, err := findExtensionDir("nope"); err == nil {
		t.Fatal("expected a not-found error for an unknown name")
	}
}

// findExtensionDirIn backs the /extensions config dialog and must resolve by
// manifest name too, not just the install-dir basename — otherwise an
// extension installed under its source repo name (dir "terva-ext-obsidian",
// manifest "obsidian") reports "no configurable settings" though its schema is
// right there. It shares matchExtensionDir with findExtensionDir.
func TestFindExtensionDirInResolvesManifestName(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	gdir := filepath.Join(home, "extensions", "terva-ext-obsidian")
	if err := os.MkdirAll(gdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gdir, "extension.json"), []byte(`{"name":"obsidian"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"obsidian", "terva-ext-obsidian"} {
		got, err := config.FindExtensionDirIn("", name)
		if err != nil || got != gdir {
			t.Fatalf("findExtensionDirIn(%q) = %q, %v; want %q", name, got, err, gdir)
		}
	}

	// Same resolution for a project-scoped install under cwd/.terva/extensions.
	cwd := testsupport.TempDir(t)
	pdir := filepath.Join(cwd, ".terva", "extensions", "terva-tasks")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "extension.json"), []byte(`{"name":"tasks"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := config.FindExtensionDirIn(cwd, "tasks"); err != nil || got != pdir {
		t.Fatalf(`findExtensionDirIn(cwd, "tasks") = %q, %v; want %q`, got, err, pdir)
	}

	if _, err := config.FindExtensionDirIn("", "nope"); err == nil {
		t.Fatal("expected a not-found error for an unknown name")
	}
}
