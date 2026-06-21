package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// extUpgrade for a missing name is an error. (The git-pull mechanics are
// covered by updateOneExtension's tests in extupdate_test.go; here we
// exercise the name-resolution wrapper.)
func TestExtUpgradeNotFound(t *testing.T) {
	home := t.TempDir()
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
	home := t.TempDir()
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
	home := t.TempDir()
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
