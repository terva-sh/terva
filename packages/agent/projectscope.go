package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/envcompat"
)

// Project-scoped mode makes a directory a self-contained agent: all DATA
// (sessions, ext-data, logs, config, personas) lives in a project-local home
// (.terva/home), and ONLY the project's own extensions load — not the global
// system ones. Credentials (auth.json) and the trust store stay GLOBAL, so the
// agent inherits your login and trust verdicts instead of re-authenticating per
// project (and a repo still can't trust itself). It's opt-in: the
// `project_scoped` field in .terva/config.json, or --project / --no-project.
//
// The whole redirect is "set TERVA_HOME to the project home, but pin auth +
// trust to the captured global home" — see EnableProjectScope and
// config.CredentialHome.

// ProjectScoped reports whether this run is project-scoped (the data home was
// redirected and credentials pinned global). True only after EnableProjectScope.
func ProjectScoped() bool { return config.PinnedGlobalHome != "" }

// GlobalExtensionsDir is the user's global extensions directory — the source a
// project-scoped agent adopts extensions FROM. It tracks CredentialHome, so it
// points at the real global install even when data has been redirected.
func GlobalExtensionsDir() string { return filepath.Join(config.CredentialHome(), "extensions") }

// resolveAdoptExtensions returns the global extensions a scoped project re-adopts
// (its .terva/config.json adopt_extensions), or nil.
func resolveAdoptExtensions(cwd string) []string {
	pc, err := config.LoadProjectConfig(cwd)
	if err != nil || pc == nil {
		return nil
	}
	return pc.AdoptExtensions
}

// projectScopedDecision resolves whether project-scoped mode is on. Pure, so
// the precedence is easy to test: --no-project wins, then --project, then the
// project config's project_scoped field (nil = unset), else off.
func projectScopedDecision(flagProject, flagNoProject bool, configScoped *bool) bool {
	switch {
	case flagNoProject:
		return false
	case flagProject:
		return true
	case configScoped != nil:
		return *configScoped
	default:
		return false
	}
}

// resolveProjectScoped combines the flags with the project config's
// project_scoped field (looked up from cwd, walking up like other project
// config). A read error just means "no config opinion".
func resolveProjectScoped(args build.Args, cwd string) bool {
	var configScoped *bool
	if pc, err := config.LoadProjectConfig(cwd); err == nil && pc != nil {
		configScoped = pc.ProjectScoped
	}
	return projectScopedDecision(args.Project, args.NoProject, configScoped)
}

// projectDataHome is where a project-scoped agent keeps its data: the first
// project dir spelling (.terva) plus /home, under cwd.
func projectDataHome(cwd string) string {
	dir := ".terva"
	if names := envcompat.ProjectDirNames(); len(names) > 0 {
		dir = names[0]
	}
	return filepath.Join(cwd, dir, "home")
}

// EnableProjectScope redirects all terva DATA to a project-local home while
// pinning credentials + the trust store to the user's global home. It captures
// the global home FIRST (auth/trust resolve there), creates the project data
// home (with a .gitignore so runtime state is never committed), then sets
// TERVA_HOME so every data-layer path follows. Call once, early, before
// anything reads the home. Returns (dataHome, globalHome).
func EnableProjectScope(cwd string) (dataHome, globalHome string, err error) {
	globalHome = config.TervaHome() // capture BEFORE the redirect — auth + trust home
	dataHome = projectDataHome(cwd)
	if err := os.MkdirAll(dataHome, 0o700); err != nil {
		return "", "", fmt.Errorf("create project home %s: %w", dataHome, err)
	}
	// The data home is pure runtime state (sessions/logs/ext-data/config) — make
	// sure it's never committed. Best-effort; ignore if it already exists.
	gi := filepath.Join(dataHome, ".gitignore")
	if _, statErr := os.Stat(gi); os.IsNotExist(statErr) {
		_ = os.WriteFile(gi, []byte("# terva project-scoped data home — runtime state, do not commit\n*\n"), 0o644)
	}
	if err := envcompat.SetHome(dataHome); err != nil {
		return "", "", fmt.Errorf("redirect TERVA_HOME: %w", err)
	}
	// Set the pin only AFTER the redirect succeeds: otherwise ProjectScoped()
	// would report true while TERVA_HOME was never actually redirected, an
	// inconsistent global state. auth.json + trusted.json resolve here.
	config.PinnedGlobalHome = globalHome
	return dataHome, globalHome, nil
}

// maybeEnableProjectScope turns on project-scoped mode when the flags/config ask
// for it, returning a human-readable note (or "") to surface to the user.
func maybeEnableProjectScope(args build.Args) (string, error) {
	cwd := args.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if !resolveProjectScoped(args, cwd) {
		return "", nil
	}
	// A project-scoped agent runs THIS directory's own config, extensions,
	// hooks, and system prompt as the trusted user layer — the data home is
	// redirected INTO the repo, so .terva/home/{config.json,SYSTEM.md,
	// extensions} would otherwise load ungated. That is exactly the
	// project-supplied code Workspace Trust exists to gate. So refuse to
	// activate scope for an untrusted workspace, BEFORE the redirect and
	// before any in-repo execution surface is read — a cloned repo that ships
	// `project_scoped: true` must not self-activate (the clone-to-RCE this
	// closes). Reading the project config to learn "scope requested?" is safe
	// (a bool, not executed); only EnableProjectScope below acts on the repo.
	// Fail-closed by design: the alternative (redirect, then gate everything
	// off) is a confusing, near-empty half-agent that still moves data into the
	// repo. The user trusts once — `terva trust` (or `terva project trust`) —
	// and re-runs; `--trust` grants it for a single run.
	trustArgs := args
	trustArgs.CWD = cwd
	if !build.ResolveTrustState(trustArgs).IsTrusted() {
		return "", fmt.Errorf(
			"project-scoped agent in %s is not trusted.\n"+
				"A scoped project runs its OWN config, extensions, hooks, and system prompt, so it must be trusted first:\n"+
				"  terva trust            # trust this directory (alias: terva project trust), then re-run\n"+
				"  terva --project --trust  # trust for this run only",
			cwd)
	}
	dataHome, globalHome, err := EnableProjectScope(cwd)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"project-scoped: data → %s · login + trust inherited from %s · only this project's extensions load (still trust-gated)",
		dataHome, globalHome), nil
}
