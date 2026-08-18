package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/filelock"
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

// projectConfigMu orders project-config writers inside this process, the way
// configMu does for the user config. One mutex for every project: the pane and
// the dialog that contend are in one process and one workspace, and a per-path
// map would need its own lifetime rules to buy nothing.
var projectConfigMu sync.Mutex

// MutateProjectConfig applies fn to the project's .terva/config.json and writes
// it back, atomically against every other writer.
//
// The project layer is deliberately NOT a struct. Unlike the user config, this
// file is edited by hand and shared through a repository, so a round-trip
// through map[string]any is what keeps a key this build has never heard of —
// one a newer terva wrote, one a human added — from being dropped on the way
// through. That is also why every caller needs the SAME round-trip, and why
// four of them each grew their own.
//
// The four were byte-for-byte identical, which was the danger rather than the
// consolation: they edit different keys of ONE document whose entire purpose is
// preserving the keys the others own, they take no lock, and they all wrote the
// same fixed "<path>.tmp" — a shared scratch name that provider/auth/store.go
// and terva-mcp-bridge both document having fixed, because two writers O_TRUNC
// the same file and the rename publishes a blend of two documents. Three tests
// separately asserted "preserves unrelated fields" against three of the copies,
// and none of them could see the other two.
//
// Modes stay 0755/0644, unlike the user config's owner-only: this file lives in
// the user's repository and is routinely committed. Tightening it here would be
// a surprise change to a directory terva does not own.
func MutateProjectConfig(cwd string, fn func(doc map[string]any)) error {
	path := ProjectConfigPath(cwd)
	projectConfigMu.Lock()
	defer projectConfigMu.Unlock()
	// Degrade to the mutex when the tree cannot host a lockfile, as
	// MutateConfigAt does: editing a project setting must not depend on it.
	lk, err := filelock.Acquire(path + ".lock")
	if err != nil {
		lk = nil
	}
	defer lk.Release()

	doc := map[string]any{}
	// Read inside the lock; anything read before it describes the file as it
	// was before the writer we just queued behind.
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	fn(doc)
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return publishProjectConfig(path, append(b, '\n'))
}

// publishProjectConfig writes b to path via a UNIQUE temp file. Not
// "<path>.tmp": that name is shared, so two writers truncate and fill the same
// scratch path and the rename publishes whichever blend of the two happened to
// be on disk.
func publishProjectConfig(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // no-op once the rename succeeds
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// CreateTemp makes 0600; the project file is 0644 like the rest of a repo.
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// toggleProjectStringList is the shared body of the disable_extensions and
// disable_mcp toggles: rebuild the list without name, re-add iff disabling, and
// drop the key entirely when nothing is left so an empty list never accumulates
// in a file people read.
func toggleProjectStringList(doc map[string]any, key, name string, on bool) {
	var list []string
	if arr, ok := doc[key].([]any); ok {
		for _, e := range arr {
			if s, ok := e.(string); ok && s != name {
				list = append(list, s)
			}
		}
	}
	if on {
		list = append(list, name)
	}
	if len(list) == 0 {
		delete(doc, key)
		return
	}
	doc[key] = list
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
	return MutateProjectConfig(cwd, func(doc map[string]any) {
		toggleProjectStringList(doc, "disable_extensions", name, disabled)
	})
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
// disable_mcp list. The user layer is fully modeled by Config, so the
// structured round-trip is correct and matches the other user-level toggles
// (favorites, etc.). "Enable" is just removal.
func SetUserMCPDisabled(name string, disabled bool) error {
	return MutateConfig(func(c *Config) {
		c.DisableMCP = ToggleStringMember(c.DisableMCP, name, disabled)
	})
}

// SetProjectMCPDisabled adds or removes name from the project config's
// disable_mcp list (preserving other fields) and writes it atomically.
// The project layer isn't a fully writable struct, so this uses the same
// generic-map round-trip as SetProjectExtensionDisabled.
func SetProjectMCPDisabled(cwd, name string, disabled bool) error {
	return MutateProjectConfig(cwd, func(doc map[string]any) {
		toggleProjectStringList(doc, "disable_mcp", name, disabled)
	})
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
	return MutateProjectConfig(cwd, func(doc map[string]any) {
		setOrDelete(doc, "provider", provider)
		setOrDelete(doc, "model", model)
	})
}

// setOrDelete assigns a string key, or removes it when the value is empty —
// "clear this setting" and "set it to the empty string" must not be the same
// document, because the empty string is a value the resolver would honour.
func setOrDelete(doc map[string]any, key, value string) {
	if value == "" {
		delete(doc, key)
		return
	}
	doc[key] = value
}
