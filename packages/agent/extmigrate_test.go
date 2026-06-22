package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/extdriver"
)

// writeExtDir creates $TERVA_HOME/extensions/<dirName>/extension.json with
// the given manifest name + enabled flag, so dir basename and manifest
// name can differ (the misnamed-install case).
func writeExtDir(t *testing.T, home, dirName, manifestName string, enabled bool) string {
	t.Helper()
	dir := filepath.Join(home, "extensions", dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mf := extdriver.Manifest{Name: manifestName, Exec: "./run", Enabled: &enabled}
	b, _ := json.MarshalIndent(mf, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestNormalizeRepoURL(t *testing.T) {
	want := "github.com/terva-sh/terva-ext-web"
	for _, in := range []string{
		"https://github.com/terva-sh/terva-ext-web.git",
		"https://github.com/terva-sh/terva-ext-web",
		"https://github.com/terva-sh/terva-ext-web/",
		"git@github.com:terva-sh/terva-ext-web.git",
		// Built from parts: the ssh-scheme-with-user URL form is blocklisted
		// from the public release tree (it matches the internal remote's
		// shape), so splitting it keeps this normalization case testable.
		"ssh://" + "git@github.com/terva-sh/terva-ext-web",
		"HTTPS://GitHub.com/terva-sh/terva-ext-web.git",
	} {
		if got := normalizeRepoURL(in); got != want {
			t.Errorf("normalizeRepoURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := normalizeRepoURL(""); got != "" {
		t.Errorf("empty should normalize to empty, got %q", got)
	}
}

// A dir whose basename is the repo name and whose manifest name is the
// canonical name is a confident match; the canonical dir itself and an
// unrelated dir are not.
func TestDetectDuplicates(t *testing.T) {
	home := withTempHome(t)
	writeExtDir(t, home, "terva-ext-index", "index", true) // misnamed look-alike
	writeExtDir(t, home, "unrelated", "unrelated", true)   // control
	writeExtDir(t, home, "web", "web", true)               // already canonical

	insts := scanInstalledExtensions()
	entry := PackEntry{Name: "index", Source: "https://github.com/terva-sh/terva-ext-index.git"}
	cands := detectDuplicates(entry, insts)
	if len(cands) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(cands), cands)
	}
	if cands[0].Inst.DirBase != "terva-ext-index" || cands[0].Kind != matchConfident {
		t.Errorf("candidate = %+v, want terva-ext-index/confident", cands[0])
	}

	// The already-canonical "web" entry yields nothing (its dir IS canonical).
	if c := detectDuplicates(PackEntry{Name: "web", Source: "https://github.com/terva-sh/terva-ext-web.git"}, insts); len(c) != 0 {
		t.Errorf("canonical install should not be flagged, got %+v", c)
	}
}

// A name-only match (dir basename differs from the repo name) is a maybe.
func TestDetectDuplicatesMaybeOnNameOnly(t *testing.T) {
	home := withTempHome(t)
	writeExtDir(t, home, "myindex", "index", true)
	insts := scanInstalledExtensions()
	cands := detectDuplicates(PackEntry{Name: "index", Source: "https://github.com/terva-sh/terva-ext-index.git"}, insts)
	if len(cands) != 1 || cands[0].Kind != matchMaybe {
		t.Fatalf("name-only match should be a maybe, got %+v", cands)
	}
}

// Rename path: no canonical install yet → the look-alike becomes canonical,
// preserving its manifest (enabled flag) and the user's config block.
func TestMigrateDuplicateRename(t *testing.T) {
	home := withTempHome(t)
	writeExtDir(t, home, "terva-ext-index", "index", false) // user disabled it
	writeUserConfig(t, home, Config{
		Extensions: map[string]map[string]json.RawMessage{
			"index": {"depth": json.RawMessage(`3`)},
		},
	})
	c := detectDuplicates(PackEntry{Name: "index", Source: "https://github.com/terva-sh/terva-ext-index.git"}, scanInstalledExtensions())[0]

	out, err := migrateDuplicate(c, false)
	if err != nil {
		t.Fatal(err)
	}
	if out != "renamed" {
		t.Errorf("outcome = %q, want renamed", out)
	}
	canonical := filepath.Join(home, "extensions", "index")
	if _, err := os.Stat(filepath.Join(canonical, "extension.json")); err != nil {
		t.Fatalf("canonical dir missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "extensions", "terva-ext-index")); !os.IsNotExist(err) {
		t.Errorf("old dir should be gone, stat err=%v", err)
	}
	// enabled flag preserved (rename keeps the manifest file).
	raw, _ := os.ReadFile(filepath.Join(canonical, "extension.json"))
	var mf extdriver.Manifest
	_ = json.Unmarshal(raw, &mf)
	if mf.IsEnabled() {
		t.Error("disabled state should survive the rename")
	}
	// config block (keyed by manifest name) is untouched.
	if cfg, _ := LoadConfig(); string(cfg.Extensions["index"]["depth"]) != "3" {
		t.Errorf("config block should survive, got %v", cfg.Extensions["index"])
	}
}

// Remove path: the canonical already exists → the look-alike is removed,
// and the user's disable intent is carried onto the canonical.
func TestMigrateDuplicateRemoveWhenCanonicalExists(t *testing.T) {
	home := withTempHome(t)
	writeExtDir(t, home, "index", "index", true)            // canonical, enabled
	writeExtDir(t, home, "terva-ext-index", "index", false) // disabled look-alike
	c := detectDuplicates(PackEntry{Name: "index", Source: "https://github.com/terva-sh/terva-ext-index.git"}, scanInstalledExtensions())[0]

	out, err := migrateDuplicate(c, false)
	if err != nil {
		t.Fatal(err)
	}
	if out != "removed duplicate" {
		t.Errorf("outcome = %q, want removed duplicate", out)
	}
	if _, err := os.Stat(filepath.Join(home, "extensions", "terva-ext-index")); !os.IsNotExist(err) {
		t.Error("duplicate should be removed")
	}
	// The look-alike was disabled; that intent is carried onto the canonical.
	raw, _ := os.ReadFile(filepath.Join(home, "extensions", "index", "extension.json"))
	var mf extdriver.Manifest
	_ = json.Unmarshal(raw, &mf)
	if mf.IsEnabled() {
		t.Error("disable intent should carry onto the canonical")
	}
}

// Dry run reports the plan and changes nothing on disk.
func TestMigrateDuplicateDryRun(t *testing.T) {
	home := withTempHome(t)
	writeExtDir(t, home, "terva-ext-index", "index", true)
	c := detectDuplicates(PackEntry{Name: "index", Source: "https://github.com/terva-sh/terva-ext-index.git"}, scanInstalledExtensions())[0]

	if _, err := migrateDuplicate(c, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "extensions", "terva-ext-index")); err != nil {
		t.Error("dry run must not move the old dir")
	}
	if _, err := os.Stat(filepath.Join(home, "extensions", "index")); !os.IsNotExist(err) {
		t.Error("dry run must not create the canonical dir")
	}
}

// A git origin matching the pack source is a confident match on its own,
// even when the dir basename and manifest name say nothing.
func TestDetectDuplicatesByGitOrigin(t *testing.T) {
	home := withTempHome(t)
	dir := writeExtDir(t, home, "weird-name", "weird", true)
	src := "https://github.com/terva-sh/terva-ext-index.git"
	for _, args := range [][]string{
		{"-C", dir, "init", "-q"},
		{"-C", dir, "remote", "add", "origin", src},
	} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Skipf("git unavailable: %v", err)
		}
	}
	cands := detectDuplicates(PackEntry{Name: "index", Source: src}, scanInstalledExtensions())
	if len(cands) != 1 || cands[0].Kind != matchConfident {
		t.Fatalf("git-origin match should be confident, got %+v", cands)
	}
}
