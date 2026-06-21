package agent

// Backs the interactive /extensions dialog (modes.extensionsDialog): a
// dir scan for the full installed list, the two persistence surfaces it
// toggles (manifest `enabled` and project config disable_extensions),
// and a live reload. Plumbed into modes as InteractiveConfig callbacks
// so that package never imports this one.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/modes"
	"terva.sh/terva/packages/envcompat"
)

// projectConfigPath returns the preferred project config path for cwd
// ($CWD/.terva/config.json).
func projectConfigPath(cwd string) string {
	dir := ".terva"
	if names := envcompat.ProjectDirNames(); len(names) > 0 {
		dir = names[0]
	}
	return filepath.Join(cwd, dir, "config.json")
}

// setManifestEnabled flips the enabled flag in the extension manifest at
// dir, preserving every other field (generic round-trip — same shape as
// `terva ext enable/disable`).
func setManifestEnabled(dir string, enabled bool) error {
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

// setProjectExtensionDisabled adds or removes name from the project
// config's disable_extensions list (preserving other fields) and writes
// it atomically. disabled=true disables the extension for this project.
func setProjectExtensionDisabled(cwd, name string, disabled bool) error {
	path := projectConfigPath(cwd)
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

// findExtensionDirIn locates the named extension's directory under the
// global root or the given project root.
func findExtensionDirIn(cwd, name string) (string, error) {
	dirs := []string{filepath.Join(TervaHome(), "extensions")}
	if cwd != "" {
		dirs = append(dirs, filepath.Join(cwd, ".terva", "extensions"))
	}
	for _, d := range dirs {
		cand := filepath.Join(d, name)
		if _, err := os.Stat(filepath.Join(cand, "extension.json")); err == nil {
			return cand, nil
		}
	}
	return "", fmt.Errorf("extension %q not found", name)
}

// listInstalledExtensions scans the global and project extension dirs and
// returns each installed extension with enable/disable + live state.
// cwd scopes the project dir + project config; trusted gates whether
// project extensions can load; mgr (may be nil) supplies ground-truth
// running state and provide-counts from the live driver.
func listInstalledExtensions(cwd string, trusted bool, mgr *extensions.Manager) []modes.ExtInfo {
	userDisabled := map[string]bool{}
	if c, err := LoadConfig(); err == nil {
		for _, n := range c.DisableExtensions {
			userDisabled[n] = true
		}
	}
	projDisabled := map[string]bool{}
	if pc, err := LoadProjectConfig(cwd); err == nil && pc != nil {
		for _, n := range pc.DisableExtensions {
			projDisabled[n] = true
		}
	}

	// Ground truth from the live manager (embedded extdriver.Driver):
	// which names are ready, and how many commands/tools each registered.
	ready := map[string]bool{}
	cmdCount := map[string]int{}
	toolCount := map[string]int{}
	if mgr != nil {
		for _, e := range mgr.All() {
			ready[e.Manifest.Name] = e.Ready()
		}
		for _, c := range mgr.Commands() {
			cmdCount[c.Extension]++
		}
		for _, t := range mgr.Tools() {
			toolCount[t.Extension]++
		}
	}

	type scoped struct{ scope, dir string }
	roots := []scoped{{"global", filepath.Join(TervaHome(), "extensions")}}
	if cwd != "" {
		roots = append(roots, scoped{"project", filepath.Join(cwd, ".terva", "extensions")})
	}

	var out []modes.ExtInfo
	seen := map[string]bool{}
	for _, r := range roots {
		entries, err := os.ReadDir(r.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(r.dir, e.Name(), "extension.json"))
			if err != nil {
				continue
			}
			var m struct {
				Name        string `json:"name"`
				Version     string `json:"version"`
				Language    string `json:"language"`
				Description string `json:"description"`
				Enabled     *bool  `json:"enabled"`
			}
			if err := json.Unmarshal(raw, &m); err != nil || m.Name == "" {
				continue
			}
			globalEnabled := m.Enabled == nil || *m.Enabled
			projectGated := r.scope == "project" && !trusted
			info := modes.ExtInfo{
				Name:               m.Name,
				Version:            m.Version,
				Language:           m.Language,
				Description:        m.Description,
				Scope:              r.scope,
				GlobalEnabled:      globalEnabled,
				ProjectDisabled:    projDisabled[m.Name],
				UserConfigDisabled: userDisabled[m.Name],
				ProjectGated:       projectGated,
				Effective:          globalEnabled && !projDisabled[m.Name] && !userDisabled[m.Name] && !projectGated,
				Running:            ready[m.Name],
				Commands:           cmdCount[m.Name],
				Tools:              toolCount[m.Name],
			}
			if _, err := os.Stat(filepath.Join(TervaHome(), "logs", "ext-"+m.Name+".log")); err == nil {
				info.HasLog = true
			}
			seen[m.Name] = true
			out = append(out, info)
		}
	}

	// Explicit --ext loads live outside the install roots, so the scan above
	// never emits them. Surface any extension the manager is actually running
	// that the roots didn't account for, so a `terva --ext .` dev extension
	// shows up in /extensions alongside the installed ones.
	if mgr != nil {
		live := make([]sessionExt, 0)
		for _, e := range mgr.All() {
			live = append(live, sessionExt{
				Name:        e.Manifest.Name,
				Version:     e.Manifest.Version,
				Language:    e.Manifest.Language,
				Description: e.Manifest.Description,
				LogPath:     e.LogPath,
				Ready:       e.Ready(),
			})
		}
		out = appendUnrootedExtensions(out, seen, live, cmdCount, toolCount)
	}
	return out
}

// sessionExt is the minimal view of a live extension that
// appendUnrootedExtensions needs — decoupled from extdriver.Extension (whose
// readiness lives behind unexported state) so the dedup logic is unit-testable.
type sessionExt struct {
	Name, Version, Language, Description, LogPath string
	Ready                                         bool
}

// appendUnrootedExtensions adds a "session"-scoped row for each live extension
// whose name the install-root scan didn't already emit (tracked in seen). These
// are the explicit --ext loads: loaded by path for this run only, not installed,
// so they carry no persistent enable/disable state to toggle.
func appendUnrootedExtensions(out []modes.ExtInfo, seen map[string]bool, live []sessionExt, cmdCount, toolCount map[string]int) []modes.ExtInfo {
	for _, e := range live {
		if e.Name == "" || seen[e.Name] {
			continue
		}
		seen[e.Name] = true
		info := modes.ExtInfo{
			Name:          e.Name,
			Version:       e.Version,
			Language:      e.Language,
			Description:   e.Description,
			Scope:         "session",
			GlobalEnabled: true, // loaded explicitly for this run
			Effective:     e.Ready,
			Running:       e.Ready,
			Commands:      cmdCount[e.Name],
			Tools:         toolCount[e.Name],
		}
		if e.LogPath != "" {
			if _, err := os.Stat(e.LogPath); err == nil {
				info.HasLog = true
			}
		}
		out = append(out, info)
	}
	return out
}

// unionDisabledExtensions is the resolved disable list (user ∪ project)
// read fresh from disk — what the manager's load policy must honor after
// a toggle.
func unionDisabledExtensions(cwd string) []string {
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

// extensionShouldRun reports whether the named extension should be loaded
// under the current on-disk config (manifest enabled, not user/project
// disabled, project trust satisfied). Derived from the same scan the
// dialog lists, so it can't drift from what the row shows.
func extensionShouldRun(cwd string, trusted bool, name string) bool {
	for _, info := range listInstalledExtensions(cwd, trusted, nil) {
		if info.Name == name {
			return info.Effective
		}
	}
	return false
}

// applyExtensionChangeLive applies a just-persisted enable/disable for ONE
// extension: refresh the disabled set, then surgically start or stop only
// that extension (graceful + silent). Every other running extension is
// left untouched — so a single toggle no longer reloads the whole set or
// spams "exited unexpectedly" for its neighbours.
func applyExtensionChangeLive(mgr *extensions.Manager, cwd string, trusted bool, name string) {
	if mgr == nil {
		return
	}
	mgr.SetDisabledExtensions(unionDisabledExtensions(cwd))
	want := extensionShouldRun(cwd, trusted, name)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	mgr.ApplyOne(ctx, name, want, 2*time.Second)
}
