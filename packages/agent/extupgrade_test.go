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
