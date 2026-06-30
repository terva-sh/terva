package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func boolp(b bool) *bool { return &b }

func TestProjectScopedDecision(t *testing.T) {
	cases := []struct {
		name                 string
		flagProj, flagNoProj bool
		cfg                  *bool
		want                 bool
	}{
		{"default off", false, false, nil, false},
		{"config true", false, false, boolp(true), true},
		{"config false", false, false, boolp(false), false},
		{"--project overrides unset", true, false, nil, true},
		{"--project overrides config-false", true, false, boolp(false), true},
		{"--no-project overrides config-true", false, true, boolp(true), false},
		{"--no-project wins over --project", true, true, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := projectScopedDecision(c.flagProj, c.flagNoProj, c.cfg); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestResolveProjectScopedReadsConfig(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, ".terva", "config.json"), `{"project_scoped": true}`)

	if !resolveProjectScoped(Args{}, cwd) {
		t.Error("project_scoped:true in .terva/config.json should enable it")
	}
	// An explicit --no-project still wins over the config field.
	if resolveProjectScoped(Args{NoProject: true}, cwd) {
		t.Error("--no-project should override the config field")
	}
	// A directory with no config is off.
	if resolveProjectScoped(Args{}, t.TempDir()) {
		t.Error("no config ⇒ project-scoped off")
	}
}

// TestEnableProjectScope is the integration check: data follows the redirect,
// credentials + trust stay pinned to the captured global home.
func TestEnableProjectScope(t *testing.T) {
	global := t.TempDir()
	t.Setenv("TERVA_HOME", global) // fake global home; restored at cleanup
	defer func() { pinnedGlobalHome = "" }()

	// Sanity: before scoping, everything resolves to the global home.
	if TervaHome() != global || AuthPath() != filepath.Join(global, "auth.json") {
		t.Fatalf("precondition: home should be the global temp dir")
	}

	cwd := t.TempDir()
	dataHome, gotGlobal, err := EnableProjectScope(cwd)
	if err != nil {
		t.Fatal(err)
	}

	if gotGlobal != global {
		t.Errorf("captured global home = %q, want %q", gotGlobal, global)
	}
	if want := filepath.Join(cwd, ".terva", "home"); dataHome != want {
		t.Errorf("data home = %q, want %q", dataHome, want)
	}
	if st, err := os.Stat(dataHome); err != nil || !st.IsDir() {
		t.Errorf("data home not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataHome, ".gitignore")); err != nil {
		t.Errorf("data home should get a .gitignore: %v", err)
	}

	// Data follows the redirect…
	if TervaHome() != dataHome {
		t.Errorf("TervaHome() = %q, want the project data home %q", TervaHome(), dataHome)
	}
	if ConfigPath() != filepath.Join(dataHome, "config.json") {
		t.Errorf("config should follow the redirect, got %q", ConfigPath())
	}
	// …but credentials + trust stay pinned to the global home (inherited login).
	if AuthPath() != filepath.Join(global, "auth.json") {
		t.Errorf("auth.json should stay global, got %q", AuthPath())
	}
	if TrustStorePath() != filepath.Join(global, "trusted.json") {
		t.Errorf("trusted.json should stay global, got %q", TrustStorePath())
	}
}

// TestMaybeEnableProjectScopeRequiresTrust is the clone-to-RCE gate: a project
// that declares itself scoped must NOT self-activate on an UNTRUSTED workspace.
// Activation redirects TERVA_HOME into the repo, where .terva/home/{config.json
// (hooks/MCP), SYSTEM.md, extensions/} would otherwise load as the trusted user
// layer. So an untrusted scoped project is refused (an error), with NO redirect
// and NO data home created, until the user trusts it (here: a persisted entry;
// --trust grants it for one run via resolveTrustState).
func TestMaybeEnableProjectScopeRequiresTrust(t *testing.T) {
	global := t.TempDir()
	t.Setenv("TERVA_HOME", global)
	defer func() { pinnedGlobalHome = "" }()

	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, ".terva", "config.json"), `{"project_scoped": true}`)

	// Untrusted (the default): refuse with an error; the redirect must not run.
	if _, err := maybeEnableProjectScope(Args{CWD: cwd}); err == nil {
		t.Fatal("scoped + untrusted must error, not activate")
	}
	if ProjectScoped() {
		t.Fatal("scope must not activate while untrusted")
	}
	if TervaHome() != global {
		t.Fatalf("TERVA_HOME must stay global when refused, got %q", TervaHome())
	}
	if _, err := os.Stat(filepath.Join(cwd, ".terva", "home")); !os.IsNotExist(err) {
		t.Fatalf("a refused activation must not create .terva/home (err=%v)", err)
	}

	// Trusting the workspace opens the gate: now it activates and redirects.
	if err := TrustPath(cwd, false); err != nil {
		t.Fatal(err)
	}
	note, err := maybeEnableProjectScope(Args{CWD: cwd})
	if err != nil {
		t.Fatalf("a trusted scoped project should activate: %v", err)
	}
	if !ProjectScoped() || note == "" {
		t.Fatalf("expected activation (note=%q scoped=%v)", note, ProjectScoped())
	}
	if want := filepath.Join(cwd, ".terva", "home"); TervaHome() != want {
		t.Errorf("data should redirect into the repo once trusted: got %q want %q", TervaHome(), want)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
