package lore

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func entryByName(entries []Entry, name string) bool {
	for _, e := range entries {
		if e.Name == name {
			return true
		}
	}
	return false
}

func TestDiscover_TiersAndTrustGating(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)

	writeFile(t, filepath.Join(home, "lore", "auth.md"), "---\nname: Auth\nkeys: [auth]\n---\nauth body")
	writeFile(t, filepath.Join(home, "lore", "lore.json"), `{"scan_depth": 5, "token_budget": 999, "recursive_scanning": true}`)

	writeFile(t, filepath.Join(home, "extensions", "extfoo", "extension.json"), `{"enabled": true}`)
	writeFile(t, filepath.Join(home, "extensions", "extfoo", "lore", "ext.md"), "---\nname: Ext\nkeys: [ext]\n---\next body")

	writeFile(t, filepath.Join(home, "extensions", "extoff", "extension.json"), `{"enabled": false}`)
	writeFile(t, filepath.Join(home, "extensions", "extoff", "lore", "off.md"), "---\nname: Off\nkeys: [off]\n---\noff body")

	writeFile(t, filepath.Join(cwd, ".terva", "lore", "proj.md"), "---\nname: Proj\nkeys: [proj]\n---\nproj body")

	// Trusted: personal, extension, and project tiers all contribute.
	entries, cfg, errs := Discover(home, cwd, true)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	for _, want := range []string{"Auth", "Ext", "Proj"} {
		if !entryByName(entries, want) {
			t.Errorf("trusted discover missing %q (got %d entries)", want, len(entries))
		}
	}
	if entryByName(entries, "Off") {
		t.Errorf("disabled extension lore must be excluded")
	}
	if cfg.ScanDepth != 5 || cfg.TokenBudget != 999 || !cfg.RecursiveScanning {
		t.Errorf("config from lore.json = %+v", cfg)
	}

	// Untrusted: project tier withheld, global tiers intact.
	entries, _, _ = Discover(home, cwd, false)
	if entryByName(entries, "Proj") {
		t.Errorf("untrusted workspace must not load project lore")
	}
	if !entryByName(entries, "Auth") {
		t.Errorf("global lore should still load when untrusted")
	}
}

func TestDiscover_ProjectConfigWins(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	writeFile(t, filepath.Join(home, "lore", "lore.json"), `{"token_budget": 100}`)
	writeFile(t, filepath.Join(home, "lore", "a.md"), "---\nname: A\nconstant: true\n---\nx")
	writeFile(t, filepath.Join(cwd, ".terva", "lore", "lore.json"), `{"token_budget": 200}`)
	writeFile(t, filepath.Join(cwd, ".terva", "lore", "b.md"), "---\nname: B\nconstant: true\n---\ny")

	_, cfg, _ := Discover(home, cwd, true)
	if cfg.TokenBudget != 200 {
		t.Errorf("project lore.json should win: got budget %d, want 200", cfg.TokenBudget)
	}
}

func TestDiscover_BadEntryReportedNotFatal(t *testing.T) {
	home := testsupport.TempDir(t)
	writeFile(t, filepath.Join(home, "lore", "good.md"), "---\nname: Good\nkeys: [g]\n---\nok")
	writeFile(t, filepath.Join(home, "lore", "bad.md"), "---\nname: Bad\n---\nno keys, not constant")
	entries, _, errs := Discover(home, "", false)
	if len(errs) == 0 {
		t.Errorf("expected an error for the bad entry")
	}
	if !entryByName(entries, "Good") {
		t.Errorf("good entry should still load despite a sibling error")
	}
}

// TestExtensionLoreIgnoresTheRunAllowlist pins a limitation the docs now state
// rather than a behavior worth relying on: the per-run `--extensions`
// allowlist narrows which extensions the manager LOADS AND SPAWNS, but the
// bundle scanners walk the extension roots straight from disk and consult only
// the manifest `enabled` flag. So an extension excluded from the run still
// contributes its lore to the prompt.
//
// That matters because `--extensions` is documented as the least-privilege
// flag for exposed agents. It restricts processes, not bundled content.
// Consolidating discovery into one resolution result — after which this test
// should be inverted, not deleted — is Phase 1 of
// docs/proposals/managed-extension-catalog.md.
func TestExtensionLoreIgnoresTheRunAllowlist(t *testing.T) {
	home := testsupport.TempDir(t)
	ext := func(dir, name, enabled, entry string) {
		root := filepath.Join(home, "extensions", dir)
		writeFile(t, filepath.Join(root, "extension.json"),
			`{"name":"`+name+`","exec":"./run"`+enabled+`}`)
		writeFile(t, filepath.Join(root, "lore", entry+".md"),
			"---\nname: "+entry+"\nkeys: [k]\n---\nbody\n")
	}
	// Imagine a run of `--extensions calendar`: only `calendar` is allowed.
	ext("calendar", "calendar", "", "calendar-lore")
	ext("mail", "mail", "", "mail-lore")
	// A disabled manifest is the mechanism that DOES exclude a bundle.
	ext("archive", "archive", `,"enabled":false`, "archive-lore")

	dirs := extensionLoreDirs(home, "", false)
	has := func(want string) bool {
		for _, d := range dirs {
			if filepath.Base(filepath.Dir(d)) == want {
				return true
			}
		}
		return false
	}
	if !has("calendar") {
		t.Error("the allowlisted extension's lore should load")
	}
	if !has("mail") {
		t.Error("a NON-allowlisted extension's lore still loads today; if this " +
			"now fails the leak is closed — invert the assertion and update " +
			"docs/extensions.md (Bundle contributions) and docs/cli.md (--extensions)")
	}
	if has("archive") {
		t.Error("a manifest with enabled:false must contribute no lore — that is " +
			"the documented way to exclude a bundle")
	}
}
