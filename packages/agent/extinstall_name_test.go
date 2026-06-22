package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeSource makes a local extension source dir <parent>/<dirName> with the
// given extension.json body and returns its path.
func writeSource(t *testing.T, dirName, manifestBody string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), dirName)
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "extension.json"), []byte(manifestBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

func TestSafeInstallName(t *testing.T) {
	for name, want := range map[string]bool{
		"foo": true, "terva-ext-foo": true, "a.b": true, "v1.2": true,
		"": false, ".": false, "..": false, "a/b": false, "../x": false, `a\b`: false, "/abs": false,
	} {
		if got := safeInstallName(name); got != want {
			t.Errorf("safeInstallName(%q) = %v, want %v", name, got, want)
		}
	}
}

// A local install names the dir from the manifest, not the source basename.
func TestInstallOneNamesFromManifest(t *testing.T) {
	home := withTempHome(t)
	src := writeSource(t, "terva-ext-foo", `{"name":"foo"}`)

	out, err := installOne(src, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if out != filepath.Join(home, "extensions", "foo") {
		t.Fatalf("out = %s, want extensions/foo (manifest name)", out)
	}
	if _, err := os.Stat(filepath.Join(out, "extension.json")); err != nil {
		t.Fatalf("installed extension missing: %v", err)
	}
}

// No manifest name → fall back to the source basename (legacy behavior).
func TestInstallOneFallsBackToBasename(t *testing.T) {
	home := withTempHome(t)
	src := writeSource(t, "bar", `{"version":"1.0.0"}`) // no name
	out, err := installOne(src, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if out != filepath.Join(home, "extensions", "bar") {
		t.Fatalf("out = %s, want extensions/bar (basename fallback)", out)
	}
}

// An explicit override still wins over the manifest name.
func TestInstallOneNameOverrideWins(t *testing.T) {
	home := withTempHome(t)
	src := writeSource(t, "terva-ext-foo", `{"name":"foo"}`)
	out, err := installOne(src, "", "pinned")
	if err != nil {
		t.Fatal(err)
	}
	if out != filepath.Join(home, "extensions", "pinned") {
		t.Fatalf("out = %s, want extensions/pinned (override)", out)
	}
}

// SECURITY: a manifest name that would escape the extensions dir is rejected
// and the install falls back to the (safe) source basename — nothing is
// written outside extensions/.
func TestInstallOneRejectsTraversalManifestName(t *testing.T) {
	home := withTempHome(t)
	src := writeSource(t, "evilsrc", `{"name":"../pwned"}`)

	out, err := installOne(src, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if out != filepath.Join(home, "extensions", "evilsrc") {
		t.Fatalf("out = %s, want extensions/evilsrc (traversal name rejected)", out)
	}
	if _, err := os.Stat(filepath.Join(home, "pwned")); !os.IsNotExist(err) {
		t.Fatalf("traversal escaped the extensions dir: %s/pwned exists", home)
	}
}

// SECURITY: an explicit override that would escape is rejected outright
// (closes the pre-existing pack-entry-name vector too).
func TestInstallOneRejectsTraversalOverride(t *testing.T) {
	home := withTempHome(t)
	src := writeSource(t, "ok", `{"name":"ok"}`)
	if _, err := installOne(src, "", "../pwned"); err == nil {
		t.Fatal("expected an error for a traversal override")
	}
	if _, err := os.Stat(filepath.Join(home, "pwned")); !os.IsNotExist(err) {
		t.Fatal("traversal override escaped the extensions dir")
	}
}

// The git branch canonicalizes the cloned dir to the manifest name.
func TestInstallOneGitCanonicalizes(t *testing.T) {
	home := withTempHome(t)
	// A local repo dir ending in .git triggers installOne's git branch.
	repo := writeSource(t, "terva-ext-foo.git", `{"name":"foo"}`)
	for _, args := range [][]string{
		{"-C", repo, "init", "-q"},
		{"-C", repo, "add", "-A"},
		{"-C", repo, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-q", "-m", "x"},
	} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Skipf("git unavailable: %v", err)
		}
	}
	out, err := installOne(repo, "", "")
	if err != nil {
		t.Fatalf("git install: %v", err)
	}
	if out != filepath.Join(home, "extensions", "foo") {
		t.Fatalf("out = %s, want extensions/foo (canonicalized from terva-ext-foo)", out)
	}
	if _, err := os.Stat(filepath.Join(home, "extensions", "terva-ext-foo")); !os.IsNotExist(err) {
		t.Fatal("the non-canonical clone dir should have been renamed away")
	}
}
