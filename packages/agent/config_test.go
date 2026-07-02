package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
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

// A project's default provider/model is behaviour-changing, so ResolveConfig
// overlays it onto the read view only when the workspace is trusted; the
// pristine user layer is never touched.
func TestResolveConfigProjectModelTrustGated(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"),
		[]byte(`{"provider":"anthropic","model":"opus"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd := testsupport.TempDir(t)
	writeProjectConfig(t, cwd, `{"provider":"openai","model":"gpt-5"}`)

	// Untrusted: the project's model/provider are ignored; the user default wins.
	eff := ResolveConfig(cwd, false)
	if eff.Config.Provider != "anthropic" || eff.Config.Model != "opus" {
		t.Errorf("untrusted should keep user default, got %q/%q", eff.Config.Provider, eff.Config.Model)
	}

	// Trusted: the project's model/provider overlay the user default.
	eff = ResolveConfig(cwd, true)
	if eff.Config.Provider != "openai" || eff.Config.Model != "gpt-5" {
		t.Errorf("trusted should apply the project default, got %q/%q", eff.Config.Provider, eff.Config.Model)
	}
	// The writable user layer must stay pristine in both cases.
	if eff.User.Provider != "anthropic" || eff.User.Model != "opus" {
		t.Errorf("user layer must remain pristine, got %q/%q", eff.User.Provider, eff.User.Model)
	}
}

// toggleStringMember backs FavoriteModels: add is idempotent (deduped),
// remove drops the key, and emptying returns nil.
func TestToggleStringMember(t *testing.T) {
	var list []string
	list = toggleStringMember(list, "a", true)
	if len(list) != 1 || list[0] != "a" {
		t.Fatalf("add to empty: %v", list)
	}
	list = toggleStringMember(list, "b", true)
	list = toggleStringMember(list, "a", true) // idempotent add
	if len(list) != 2 {
		t.Fatalf("idempotent add should keep 2: %v", list)
	}
	list = toggleStringMember(list, "a", false) // remove
	if len(list) != 1 || list[0] != "b" {
		t.Fatalf("remove a: %v", list)
	}
	if list = toggleStringMember(list, "b", false); list != nil {
		t.Fatalf("emptying should return nil, got %v", list)
	}
}

func TestLoadProjectConfigNearestWins(t *testing.T) {
	root := testsupport.TempDir(t)
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
	root := testsupport.TempDir(t)
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
	pc, err := LoadProjectConfig(testsupport.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if pc != nil {
		t.Fatalf("expected nil project config, got %+v", pc)
	}
}

func TestLoadProjectConfigMalformedErrors(t *testing.T) {
	dir := testsupport.TempDir(t)
	writeProjectConfig(t, dir, `{not valid json`)
	_, err := LoadProjectConfig(dir)
	if err == nil {
		t.Fatal("expected error for malformed project config, got nil")
	}
}

func TestLoadProjectConfigAbsolutizesInRepoRelativeAgainstProjectRoot(t *testing.T) {
	root := testsupport.TempDir(t)
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
	root := testsupport.TempDir(t)
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
	root := testsupport.TempDir(t)
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
	tervaHome := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", tervaHome)
	abs := filepath.Join(testsupport.TempDir(t), "outside", "notes.md")
	if err := SaveConfig(Config{ContextFiles: []string{abs}}); err != nil {
		t.Fatal(err)
	}
	// cwd has no project config, so the user layer is used verbatim.
	eff := ResolveConfig(testsupport.TempDir(t), true)
	if len(eff.ContextFiles) != 1 || eff.ContextFiles[0] != abs {
		t.Fatalf("user-layer absolute path not preserved: got %v, want [%s]", eff.ContextFiles, abs)
	}
}

func TestResolveConfigProjectOverridesUser(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if err := SaveConfig(Config{Provider: "openai", Model: "gpt-5", ContextFiles: []string{"user.md"}}); err != nil {
		t.Fatal(err)
	}
	repo := testsupport.TempDir(t)
	writeProjectConfig(t, repo, `{"context_files":["project.md"]}`)

	eff := ResolveConfig(repo, true) // trusted: project layer applies
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

// Workspace Trust: an UNTRUSTED project's context_files (system-prompt
// injection from a possibly-cloned repo) are dropped; the user layer
// still injects. The restrict-only disable fields are NOT gated (tested
// separately) — they can only tighten.
func TestResolveConfigUntrustedDropsProjectContextFiles(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if err := SaveConfig(Config{ContextFiles: []string{"user.md"}}); err != nil {
		t.Fatal(err)
	}
	repo := testsupport.TempDir(t)
	writeProjectConfig(t, repo, `{"context_files":["project.md"]}`)

	// Untrusted: the project's context_files must NOT win — only the user
	// layer injects.
	eff := ResolveConfig(repo, false)
	wantUser := filepath.Join(TervaHome(), "user.md")
	if len(eff.ContextFiles) != 1 || eff.ContextFiles[0] != wantUser {
		t.Fatalf("untrusted project context_files leaked: got %v, want user layer [%s]", eff.ContextFiles, wantUser)
	}
	// Trusted: same dir, the project layer now applies.
	effTrusted := ResolveConfig(repo, true)
	wantProject := filepath.Join(repo, "project.md")
	if len(effTrusted.ContextFiles) != 1 || effTrusted.ContextFiles[0] != wantProject {
		t.Fatalf("trusted project context_files not applied: got %v, want [%s]", effTrusted.ContextFiles, wantProject)
	}
}

// disable_context_extensions is restrict-only: the project layer can
// only ADD to the user's disabled set (union), never re-enable.
func TestResolveConfigDisableContextExtensionsUnion(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if err := SaveConfig(Config{DisableContextExtensions: []string{"alpha"}}); err != nil {
		t.Fatal(err)
	}
	repo := testsupport.TempDir(t)
	writeProjectConfig(t, repo, `{"disable_context_extensions":["beta"]}`)

	eff := ResolveConfig(repo, true)
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

// disable_extensions is restrict-only too: the project layer unions
// into the user's, never re-enabling.
func TestResolveConfigDisableExtensionsUnion(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if err := SaveConfig(Config{DisableExtensions: []string{"alpha"}}); err != nil {
		t.Fatal(err)
	}
	repo := testsupport.TempDir(t)
	writeProjectConfig(t, repo, `{"disable_extensions":["beta"]}`)

	eff := ResolveConfig(repo, true)
	got := map[string]bool{}
	for _, n := range eff.DisableExtensions {
		got[n] = true
	}
	if !got["alpha"] || !got["beta"] || len(eff.DisableExtensions) != 2 {
		t.Fatalf("union should be {alpha (user), beta (project)}: %v", eff.DisableExtensions)
	}
}

func TestResolveConfigFallsBackToUserWhenNoProject(t *testing.T) {
	tervaHome := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", tervaHome)
	if err := SaveConfig(Config{ContextFiles: []string{"user.md"}}); err != nil {
		t.Fatal(err)
	}
	// cwd has no project config anywhere up the tree.
	eff := ResolveConfig(testsupport.TempDir(t), true)
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

// TestConfigSwarmWorktreesRoundTrip verifies the swarm_worktrees key
// survives a SaveConfig/LoadConfig round trip and parses from JSON.
func TestConfigSwarmWorktreesRoundTrip(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	// Absent in JSON => nil pointer (off).
	if err := SaveConfig(Config{}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.SwarmWorktrees != nil {
		t.Fatalf("SwarmWorktrees = %v; want nil when absent", *got.SwarmWorktrees)
	}

	// Explicit true survives the round trip.
	on := true
	if err := SaveConfig(Config{SwarmWorktrees: &on}); err != nil {
		t.Fatal(err)
	}
	got, err = LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.SwarmWorktrees == nil || !*got.SwarmWorktrees {
		t.Fatalf("SwarmWorktrees = %v; want a non-nil true", got.SwarmWorktrees)
	}

	// And it parses from a raw JSON config using the snake_case key.
	var c Config
	if err := json.Unmarshal([]byte(`{"swarm_worktrees":true}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.SwarmWorktrees == nil || !*c.SwarmWorktrees {
		t.Fatalf("parsed SwarmWorktrees = %v; want true", c.SwarmWorktrees)
	}
}

// TestGlobalUserName_SurvivesProjectScope: the preferred user name reads from
// and writes to the user's real GLOBAL home even when project-scoped mode has
// redirected TERVA_HOME into a repo — so a character card's {{user}} is stable
// across projects and the answer is never saved into the repo.
func TestGlobalUserName_SurvivesProjectScope(t *testing.T) {
	globalDir := testsupport.TempDir(t)
	scopedDir := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", scopedDir) // the redirected (project-local) home

	// Different names in the scoped home and the real global home.
	if err := SaveConfig(Config{UserName: "ScopedName"}); err != nil { // -> TERVA_HOME (scoped)
		t.Fatal(err)
	}
	if err := saveConfigAt(globalDir, Config{UserName: "GlobalName"}); err != nil {
		t.Fatal(err)
	}

	// Simulate project-scoped mode pinning the real global home.
	prev := pinnedGlobalHome
	pinnedGlobalHome = globalDir
	t.Cleanup(func() { pinnedGlobalHome = prev })

	// Reads come from the global home, not the redirected one.
	if got := GlobalUserPreferences().UserName; got != "GlobalName" {
		t.Errorf("global pref under scoping = %q, want GlobalName", got)
	}
	// Writes go to the global home; the scoped home is left untouched.
	if err := SetGlobalUserName("Updated"); err != nil {
		t.Fatal(err)
	}
	if c, _ := loadConfigAt(globalDir); c.UserName != "Updated" {
		t.Errorf("global config not updated: %q", c.UserName)
	}
	if c, _ := loadConfigAt(scopedDir); c.UserName != "ScopedName" {
		t.Errorf("scoped home should be untouched: %q", c.UserName)
	}
}

// TestResolveConfigUserName_GlobalAndTrustedOverride: user_name resolves from
// the global config, and a TRUSTED project may override it (an untrusted one
// cannot — same gate as provider/model).
func TestResolveConfigUserName_GlobalAndTrustedOverride(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if err := SaveConfig(Config{UserName: "GlobalName"}); err != nil {
		t.Fatal(err)
	}
	repo := testsupport.TempDir(t)
	writeProjectConfig(t, repo, `{"user_name":"ProjectName"}`)

	if got := ResolveConfig(repo, true).UserName; got != "ProjectName" {
		t.Errorf("trusted project should override: got %q, want ProjectName", got)
	}
	if got := ResolveConfig(repo, false).UserName; got != "GlobalName" {
		t.Errorf("untrusted project override should be dropped: got %q, want GlobalName", got)
	}

	// A project that sets no user_name leaves the global preference standing.
	repo2 := testsupport.TempDir(t)
	writeProjectConfig(t, repo2, `{"context_files":["x.md"]}`)
	if got := ResolveConfig(repo2, true).UserName; got != "GlobalName" {
		t.Errorf("no project user_name: global should stand, got %q", got)
	}
}

// The interactive name prompt must honor the same precedence Resolve uses, so it
// never fires (and clobbers) when a trusted project's user_name is set.
func TestUserNameResolved_HonorsTrustedProjectAndGlobal(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t)) // fresh home: no global user_name
	repo := testsupport.TempDir(t)
	writeProjectConfig(t, repo, `{"user_name":"ProjectName"}`)

	// A trusted project's user_name makes the name resolvable (no prompt).
	if !userNameResolved(Args{CWD: repo, Trust: true}) {
		t.Error("trusted project user_name should count as resolved")
	}
	// Untrusted + no global: the project override is dropped, nothing resolves.
	if userNameResolved(Args{CWD: repo, Trust: false}) {
		t.Error("untrusted project user_name must NOT count as resolved")
	}
	// --as always resolves, regardless of trust/config.
	if !userNameResolved(Args{CWD: repo, As: "Ravi"}) {
		t.Error("--as should count as resolved")
	}

	// With a global user_name set, even an untrusted project resolves to it.
	if err := SaveConfig(Config{UserName: "GlobalName"}); err != nil {
		t.Fatal(err)
	}
	if !userNameResolved(Args{CWD: repo, Trust: false}) {
		t.Error("global user_name should count as resolved even when untrusted")
	}

	// No --as, no project user_name, no global → unresolved (would prompt).
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	bare := testsupport.TempDir(t)
	if userNameResolved(Args{CWD: bare, Trust: true}) {
		t.Error("no --as, no project, no global → must be unresolved (prompt)")
	}
}
