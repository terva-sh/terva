package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeProjectConfig writes a .terva/config.json under dir with the given JSON body.
func writeProjectConfig(t *testing.T, dir, body string) {
	t.Helper()
	zdir := filepath.Join(dir, ".terva")
	if err := os.MkdirAll(zdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zdir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadProjectConfigNearestWins(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	sub := filepath.Join(repo, "packages", "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// A far ancestor config and a nearer repo config; the nearer one wins.
	writeProjectConfig(t, root, `{"context_files":["far.md"]}`)
	writeProjectConfig(t, repo, `{"context_files":["near.md"]}`)

	pc, err := LoadProjectConfig(sub)
	if err != nil {
		t.Fatal(err)
	}
	if pc == nil {
		t.Fatal("expected a project config, got nil")
	}
	want := filepath.Join(repo, "near.md")
	if len(pc.ContextFiles) != 1 || pc.ContextFiles[0] != want {
		t.Fatalf("nearest-wins failed: got %v, want [%s]", pc.ContextFiles, want)
	}
}

func TestLoadProjectConfigInheritsFromAncestorWhenAbsent(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "repo", "packages", "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Only a far-ancestor config exists; cwd has none, so it inherits.
	writeProjectConfig(t, root, `{"context_files":["shared.md"]}`)

	pc, err := LoadProjectConfig(sub)
	if err != nil {
		t.Fatal(err)
	}
	if pc == nil {
		t.Fatal("expected to inherit ancestor config, got nil")
	}
	want := filepath.Join(root, "shared.md")
	if len(pc.ContextFiles) != 1 || pc.ContextFiles[0] != want {
		t.Fatalf("inherit failed: got %v, want [%s]", pc.ContextFiles, want)
	}
}

func TestLoadProjectConfigNoneReturnsNil(t *testing.T) {
	pc, err := LoadProjectConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if pc != nil {
		t.Fatalf("expected nil project config, got %+v", pc)
	}
}

func TestLoadProjectConfigMalformedErrors(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, `{not valid json`)
	_, err := LoadProjectConfig(dir)
	if err == nil {
		t.Fatal("expected error for malformed project config, got nil")
	}
}

func TestLoadProjectConfigAbsolutizesInRepoRelativeAgainstProjectRoot(t *testing.T) {
	root := t.TempDir()
	// A legit in-repo relative entry resolves against the project root (the
	// dir containing .terva/), not against .terva/ itself, and is retained.
	writeProjectConfig(t, root, `{"context_files":["docs/playbook.md"]}`)

	pc, err := LoadProjectConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "docs", "playbook.md")
	if len(pc.ContextFiles) != 1 || pc.ContextFiles[0] != want {
		t.Fatalf("in-repo relative not resolved against project root: got %v, want [%s]", pc.ContextFiles, want)
	}
}

// TestLoadProjectConfigRejectsAbsolutePath: an absolute path in a (potentially
// cloned-repo) project config is the exfiltration primitive — it must be
// dropped, never injected. The trusted user layer keeps absolute paths; only
// the project layer is restricted.
func TestLoadProjectConfigRejectsAbsolutePath(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "elsewhere", "id_ed25519")
	writeProjectConfig(t, root, `{"context_files":["docs/playbook.md",`+jsonString(secret)+`]}`)

	pc, err := LoadProjectConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "docs", "playbook.md")
	if len(pc.ContextFiles) != 1 || pc.ContextFiles[0] != want {
		t.Fatalf("absolute project path not rejected: got %v, want only [%s]", pc.ContextFiles, want)
	}
	for _, f := range pc.ContextFiles {
		if f == secret {
			t.Fatalf("absolute path %q leaked into project context files", secret)
		}
	}
}

// TestLoadProjectConfigRejectsRootEscape: a ../../etc/passwd-style entry
// resolves outside the project root and must be dropped.
func TestLoadProjectConfigRejectsRootEscape(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	// "../secret.md" escapes repo/ up into root/. "../../etc/passwd" escapes
	// even further. Both must be rejected; the in-repo entry survives.
	writeProjectConfig(t, repo, `{"context_files":["ok.md","../secret.md","../../etc/passwd"]}`)

	pc, err := LoadProjectConfig(repo)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(repo, "ok.md")
	if len(pc.ContextFiles) != 1 || pc.ContextFiles[0] != want {
		t.Fatalf("root-escape not rejected: got %v, want only [%s]", pc.ContextFiles, want)
	}
}

// TestResolveConfigUserLayerKeepsAbsolutePath: the user layer is TRUSTED, so
// an absolute path in $TERVA_HOME/config.json must still be accepted — the
// containment check applies to the project layer only.
func TestResolveConfigUserLayerKeepsAbsolutePath(t *testing.T) {
	tervaHome := t.TempDir()
	t.Setenv("TERVA_HOME", tervaHome)
	abs := filepath.Join(t.TempDir(), "outside", "notes.md")
	if err := SaveConfig(Config{ContextFiles: []string{abs}}); err != nil {
		t.Fatal(err)
	}
	// cwd has no project config, so the user layer is used verbatim.
	eff := ResolveConfig(t.TempDir())
	if len(eff.ContextFiles) != 1 || eff.ContextFiles[0] != abs {
		t.Fatalf("user-layer absolute path not preserved: got %v, want [%s]", eff.ContextFiles, abs)
	}
}

func TestResolveConfigProjectOverridesUser(t *testing.T) {
	t.Setenv("TERVA_HOME", t.TempDir())
	if err := SaveConfig(Config{Provider: "openai", Model: "gpt-5", ContextFiles: []string{"user.md"}}); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	writeProjectConfig(t, repo, `{"context_files":["project.md"]}`)

	eff := ResolveConfig(repo)
	want := filepath.Join(repo, "project.md")
	if len(eff.ContextFiles) != 1 || eff.ContextFiles[0] != want {
		t.Fatalf("project should override user: got %v, want [%s]", eff.ContextFiles, want)
	}
	// Non-allowlisted fields still come from the user layer untouched.
	if eff.Provider != "openai" || eff.Model != "gpt-5" {
		t.Errorf("user fields drifted: provider=%q model=%q", eff.Provider, eff.Model)
	}
	// The writable user layer keeps its own (raw, unresolved) context files.
	if len(eff.User.ContextFiles) != 1 || eff.User.ContextFiles[0] != "user.md" {
		t.Errorf("user layer not preserved: %v", eff.User.ContextFiles)
	}
}

// disable_context_extensions is restrict-only: the project layer can
// only ADD to the user's disabled set (union), never re-enable.
func TestResolveConfigDisableContextExtensionsUnion(t *testing.T) {
	t.Setenv("TERVA_HOME", t.TempDir())
	if err := SaveConfig(Config{DisableContextExtensions: []string{"alpha"}}); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	writeProjectConfig(t, repo, `{"disable_context_extensions":["beta"]}`)

	eff := ResolveConfig(repo)
	got := map[string]bool{}
	for _, n := range eff.DisableContextExtensions {
		got[n] = true
	}
	if !got["alpha"] || !got["beta"] {
		t.Fatalf("union should contain both user (alpha) and project (beta): %v", eff.DisableContextExtensions)
	}
	if len(eff.DisableContextExtensions) != 2 {
		t.Errorf("expected exactly the union of 2, got %v", eff.DisableContextExtensions)
	}
	// The project layer cannot remove the user's entry.
	if !got["alpha"] {
		t.Error("project must not be able to re-enable a user-disabled extension")
	}
}

func TestResolveConfigFallsBackToUserWhenNoProject(t *testing.T) {
	tervaHome := t.TempDir()
	t.Setenv("TERVA_HOME", tervaHome)
	if err := SaveConfig(Config{ContextFiles: []string{"user.md"}}); err != nil {
		t.Fatal(err)
	}
	// cwd has no project config anywhere up the tree.
	eff := ResolveConfig(t.TempDir())
	want := filepath.Join(tervaHome, "user.md")
	if len(eff.ContextFiles) != 1 || eff.ContextFiles[0] != want {
		t.Fatalf("user fallback failed: got %v, want [%s]", eff.ContextFiles, want)
	}
}

// jsonString quotes s as a JSON string literal (handles Windows backslashes).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
