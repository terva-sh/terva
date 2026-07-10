package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/envcompat"
)

// Extension and MCP configuration: the project-config path, the enable/disable
// mutators, and extension-directory resolution. These write config and manifest
// files and nothing else, so they belong beside config rather than in the TUI
// dialog files where they accumulated.

// ProjectConfigPath returns the preferred project config path for cwd
// ($CWD/.terva/json).
func ProjectConfigPath(cwd string) string {
	dir := ".terva"
	if names := envcompat.ProjectDirNames(); len(names) > 0 {
		dir = names[0]
	}
	return filepath.Join(cwd, dir, "config.json")
}

// SetManifestEnabled flips the enabled flag in the extension manifest at
// dir, preserving every other field (generic round-trip — same shape as
// `terva ext enable/disable`).
func SetManifestEnabled(dir string, enabled bool) error {
	mfPath := filepath.Join(dir, "extension.json")
	raw, err := os.ReadFile(mfPath)
	if err != nil {
		return err
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	generic["enabled"] = enabled
	out, err := json.MarshalIndent(generic, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(mfPath, append(out, '\n'), 0o644)
}

// SetProjectExtensionDisabled adds or removes name from the project
// config's disable_extensions list (preserving other fields) and writes
// it atomically. disabled=true disables the extension for this project.
func SetProjectExtensionDisabled(cwd, name string, disabled bool) error {
	path := ProjectConfigPath(cwd)
	generic := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &generic); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// Rebuild the list without name, then re-add it iff disabling.
	var list []string
	if arr, ok := generic["disable_extensions"].([]any); ok {
		for _, e := range arr {
			if s, ok := e.(string); ok && s != name {
				list = append(list, s)
			}
		}
	}
	if disabled {
		list = append(list, name)
	}
	if len(list) == 0 {
		delete(generic, "disable_extensions")
	} else {
		generic["disable_extensions"] = list
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(generic, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// FindExtensionDirIn locates the named extension's directory under the
// global root or the given project root.
func FindExtensionDirIn(cwd, name string) (string, error) {
	roots := []string{filepath.Join(TervaHome(), "extensions")}
	if cwd != "" {
		roots = append(roots, filepath.Join(cwd, ".terva", "extensions"))
	}
	// Resolve by manifest name as well as dir basename (shared with
	// findExtensionDir). Without the manifest-name fallback an extension
	// installed under its source repo name (dir "terva-ext-obsidian", manifest
	// "obsidian") is invisible here, and the /extensions config dialog reports
	// "no configurable settings" even though the schema is right there.
	if dir, ok := MatchExtensionDir(roots, name); ok {
		return dir, nil
	}
	return "", fmt.Errorf("extension %q not found", name)
}

// SetUserMCPDisabled adds or removes name from the user config's
// disable_mcp list and writes json. The user layer is fully
// modeled by Config, so the structured round-trip (LoadConfig /
// toggleStringMember / SaveConfig) is correct and matches the other
// user-level toggles (favorites, etc.). "Enable" is just removal.
func SetUserMCPDisabled(name string, disabled bool) error {
	c, err := LoadConfig()
	if err != nil {
		return err
	}
	c.DisableMCP = ToggleStringMember(c.DisableMCP, name, disabled)
	return SaveConfig(c)
}

// SetProjectMCPDisabled adds or removes name from the project config's
// disable_mcp list (preserving other fields) and writes it atomically.
// The project layer isn't a fully writable struct, so this uses the same
// generic-map round-trip as SetProjectExtensionDisabled.
func SetProjectMCPDisabled(cwd, name string, disabled bool) error {
	path := ProjectConfigPath(cwd)
	generic := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &generic); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// Rebuild the list without name, then re-add it iff disabling.
	var list []string
	if arr, ok := generic["disable_mcp"].([]any); ok {
		for _, e := range arr {
			if s, ok := e.(string); ok && s != name {
				list = append(list, s)
			}
		}
	}
	if disabled {
		list = append(list, name)
	}
	if len(list) == 0 {
		delete(generic, "disable_mcp")
	} else {
		generic["disable_mcp"] = list
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(generic, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LocalesDir is $TERVA_HOME/locales, home to operator translation overlays
// (<lang>.json), gap-capture todo files (<lang>.todo.json), and exports.
func LocalesDir() string { return filepath.Join(TervaHome(), "locales") }

// MatchExtensionDir resolves an extension to its install directory within the
// given roots (searched in order), preferring a directory whose basename is
// name, then a directory whose manifest declares that name. The two differ
// when the install dir keeps the source repo name (dir "terva-ext-index" for
// manifest name "index"); `ext list` shows the manifest name, so every
// name-keyed lookup must accept both. findExtensionDir and findExtensionDirIn
// both route through here so their resolution can't drift apart — the drift
// that once let the /extensions config dialog miss a manifest-named install.
func MatchExtensionDir(roots []string, name string) (string, bool) {
	// 1. Direct install-directory match (fast path, back-compat).
	for _, d := range roots {
		cand := filepath.Join(d, name)
		if _, err := os.Stat(filepath.Join(cand, "extension.json")); err == nil {
			return cand, true
		}
	}
	// 2. Manifest-name match (what `ext list` displays).
	for _, d := range roots {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(d, e.Name(), "extension.json"))
			if err != nil {
				continue
			}
			var m struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(raw, &m) == nil && m.Name == name {
				return filepath.Join(d, e.Name()), true
			}
		}
	}
	return "", false
}

// UnionDisabledExtensions is the resolved disable list (user ∪ project)
// read fresh from disk — what the manager's load policy must honor after
// a toggle.
func UnionDisabledExtensions(cwd string) []string {
	disabled := map[string]bool{}
	if c, err := LoadConfig(); err == nil {
		for _, n := range c.DisableExtensions {
			disabled[n] = true
		}
	}
	if pc, err := LoadProjectConfig(cwd); err == nil && pc != nil {
		for _, n := range pc.DisableExtensions {
			disabled[n] = true
		}
	}
	names := make([]string, 0, len(disabled))
	for n := range disabled {
		names = append(names, n)
	}
	return names
}

// MCPServerShouldRun reports whether the named server should be running
// under the current on-disk config: it must be a defined server (user, or
// a trusted project's) and not disabled (user ∪ project). Mirrors
// ExtensionShouldRun so a live toggle can't drift from what the row shows.
func MCPServerShouldRun(cwd string, trusted bool, name string) bool {
	user, _ := LoadConfig()
	merged := MergeMCPConfigs(user.MCP, TrustedProjectMCP(cwd, trusted))
	if merged == nil {
		return false
	}
	if _, ok := merged.Servers[name]; !ok {
		return false // not defined here (or an untrusted project's server)
	}
	return !ResolvedDisableMCP(cwd, trusted)[name]
}

// ServerConfigFor resolves the live ServerConfig for one server name from
// the freshly-merged user+project sets (user wins). Returns ok=false when
// the name isn't a defined server. Used by a live enable to spawn it.
func ServerConfigFor(cwd string, trusted bool, name string) (mcp.ServerConfig, bool) {
	user, _ := LoadConfig()
	merged := MergeMCPConfigs(user.MCP, TrustedProjectMCP(cwd, trusted))
	if merged == nil {
		return mcp.ServerConfig{}, false
	}
	sc, ok := merged.Servers[name]
	return sc, ok
}

// ResolvedDisableMCP is the resolved disable set (user ∪ restrict-only
// project) read fresh from disk — what SetupMCP, the ACP MCP block, and a
// live toggle must honor. Project entries are honored even when untrusted
// (refusing to spawn is always safe), matching ResolveConfig.
func ResolvedDisableMCP(cwd string, trusted bool) map[string]bool {
	eff := ResolveConfig(cwd, trusted)
	set := make(map[string]bool, len(eff.Config.DisableMCP))
	for _, n := range eff.Config.DisableMCP {
		set[n] = true
	}
	return set
}

// SetProjectModel writes the project's default provider/model into
// .terva/json, preserving unrelated fields; an empty value clears its
// key. The write is unconditional, but the value is honored at launch only in
// a trusted workspace (see ResolveConfig) — the trust gate lives at read time.
func SetProjectModel(cwd, provider, model string) error {
	path := ProjectConfigPath(cwd)
	generic := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &generic); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if provider != "" {
		generic["provider"] = provider
	} else {
		delete(generic, "provider")
	}
	if model != "" {
		generic["model"] = model
	} else {
		delete(generic, "model")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(generic, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
