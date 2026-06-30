// Package envcompat resolves the product's environment variables and
// user data directory across the zot → terva rename
// (docs/plans/rename-terva.md). It is the ONE place the compat
// policy lives: new spelling first, old spelling honored as a
// fallback, with a one-time deprecation warning per variable once
// the rename ships (dormant until then).
//
// This is a leaf package: tui, provider, agent, and the connector
// SDK all read product env vars, so it must import nothing of
// theirs.
package envcompat

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

const (
	newPrefix = "TERVA_"
	oldPrefix = "ZOT_"

	newDirName = "terva"
	oldDirName = "zot"
)

// warnDeprecated gates the one-time per-variable warning for
// old-prefix spellings. Dormant through phase 1 (the binary still
// shipped as zot); ON since the phase-2 rename cut, which started
// the ≥2-tagged-release deprecation window for ZOT_* spellings.
var (
	warnMu         sync.Mutex
	warnDeprecated = true
	warned         = map[string]bool{}
)

// Lookup returns the value of the product env var with the given
// suffix ("HOME", "INLINE_IMAGES", …): TERVA_<suffix> first, then
// ZOT_<suffix>. The bool reports whether either spelling was set.
func Lookup(suffix string) (string, bool) {
	if v, ok := os.LookupEnv(newPrefix + suffix); ok {
		return v, true
	}
	if v, ok := os.LookupEnv(oldPrefix + suffix); ok {
		warnOld(suffix)
		return v, true
	}
	return "", false
}

// Get is Lookup without the presence bool — "" means unset, matching
// the os.Getenv idiom every call site already used.
func Get(suffix string) string {
	v, _ := Lookup(suffix)
	return v
}

func warnOld(suffix string) {
	warnMu.Lock()
	defer warnMu.Unlock()
	if !warnDeprecated || warned[suffix] {
		return
	}
	warned[suffix] = true
	fmt.Fprintf(os.Stderr, "warning: %s%s is deprecated; set %s%s instead\n",
		oldPrefix, suffix, newPrefix, suffix)
}

// Home returns the user data directory (config.json, auth.json,
// sessions/, logs/, connectors/, …):
//
//  1. $TERVA_HOME, then $ZOT_HOME — an explicit env var always wins.
//  2. The OS-default terva directory, when it already exists.
//  3. The OS-default zot directory, when it exists — an install that
//     predates the rename keeps its data where it is, forever
//     ("read old where it's cheap"; see HomeMigrationNote) — unless
//     `terva migrate` has run (see ZotFallbackDisabled).
//  4. The OS-default terva directory — what a fresh install creates
//     since the phase-2 rename cut.
func Home() string {
	if v, ok := Lookup("HOME"); ok && v != "" {
		return v
	}
	newDir := osDefaultDir(newDirName)
	if newDir != "" {
		if st, err := os.Stat(newDir); err == nil && st.IsDir() {
			return newDir
		}
	}
	if !ZotFallbackDisabled() {
		if old := osDefaultDir(oldDirName); old != "" {
			if st, err := os.Stat(old); err == nil && st.IsDir() {
				return old
			}
		}
	}
	if newDir != "" {
		return newDir
	}
	return "." + newDirName
}

// SetHome overrides the data home for THIS process (and any children it
// spawns) by setting the TERVA_HOME env var — the same var Home() reads first.
// Used by project-scoped mode to redirect all terva data into a project-local
// dir. It does not touch the old ZOT_HOME spelling.
func SetHome(dir string) error { return os.Setenv(newPrefix+"HOME", dir) }

// DefaultHome is the OS-default terva data dir ("" when the platform
// gives us nothing to go on). Exported for the migration engine so it
// shares this resolver instead of duplicating it.
func DefaultHome() string { return osDefaultDir(newDirName) }

// LegacyDefaultHome is the OS-default pre-rename zot data dir.
func LegacyDefaultHome() string { return osDefaultDir(oldDirName) }

// zotFallbackDisabledName marks an install where `terva migrate` has
// run: legacy zot config-file autoloading (the Home() zot-dir fallback
// and .zot project dirs) is off. ZOT_* env vars and .zotsession import
// are deliberately NOT gated by it — an env var is an explicit user
// action, and session import is a documented forever-promise.
const zotFallbackDisabledName = ".zot-fallback-disabled"

// zotFallbackMarkerPath is where the marker lives: $TERVA_HOME when
// set (new spelling only — a ZOT_HOME-only setup must not host a
// post-rename marker), else the OS-default terva dir. By construction
// this is also the migration target, so the writer (`terva migrate`)
// and every reader agree without consulting Home(), which itself
// depends on the marker.
func zotFallbackMarkerPath() string {
	base := os.Getenv(newPrefix + "HOME")
	if base == "" {
		base = osDefaultDir(newDirName)
	}
	if base == "" {
		return ""
	}
	return filepath.Join(base, zotFallbackDisabledName)
}

// ZotFallbackDisabled reports whether `terva migrate` has disabled
// legacy zot config-file autoloading. A plain stat per call — no
// caching, so tests reset it with t.Setenv/t.TempDir alone and a
// hand-deleted marker takes effect immediately.
func ZotFallbackDisabled() bool {
	p := zotFallbackMarkerPath()
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// SetZotFallbackDisabled writes or removes the marker. Exposed for the
// migration engine and tests.
func SetZotFallbackDisabled(disabled bool) error {
	p := zotFallbackMarkerPath()
	if p == "" {
		return fmt.Errorf("envcompat: cannot resolve a terva home for the %s marker", zotFallbackDisabledName)
	}
	if !disabled {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte("written by `terva migrate`; delete to re-enable discovery of the legacy zot data dir and .zot project dirs\n"), 0o644)
}

// HomeMigrationNote returns a one-shot migration hint when the data
// dir resolved to the legacy zot location by discovery (not by an
// explicit env var). The note is shown ONCE ever — a marker file in
// the legacy dir records that it was delivered — because the old
// location keeps working indefinitely and nagging every launch would
// punish a perfectly valid choice.
func HomeMigrationNote() (string, bool) {
	if ZotFallbackDisabled() {
		return "", false // already migrated: nothing left to hint at
	}
	if _, ok := Lookup("HOME"); ok {
		return "", false // explicit env var: the user already decided
	}
	old := osDefaultDir(oldDirName)
	if old == "" || Home() != old {
		return "", false
	}
	marker := filepath.Join(old, ".terva-migration-note-shown")
	if _, err := os.Stat(marker); err == nil {
		return "", false
	}
	_ = os.WriteFile(marker, []byte("delete to see the migration note again\n"), 0o644)
	return fmt.Sprintf("terva (formerly zot) is using your existing data dir %s — it keeps working as-is. To adopt the new location: mv %q %q (or set TERVA_HOME). This note won't repeat.",
		old, old, osDefaultDir(newDirName)), true
}

// ProjectDirNames returns the per-project config dir basenames in
// read order, new first (".terva", then ".zot"). Every project-local
// reader (config, skills, extensions) checks them in this order;
// writers keep using the new name. Once `terva migrate` has run, the
// old name drops out of the read path entirely.
func ProjectDirNames() []string {
	if ZotFallbackDisabled() {
		return []string{"." + newDirName}
	}
	return []string{"." + newDirName, "." + oldDirName}
}

// osDefaultDir is the platform-conventional data dir for the given
// product name. Mirrors the resolver zot has always used: macOS
// Application Support, Windows LOCALAPPDATA, XDG state dir, then
// ~/.local/state.
func osDefaultDir(name string) string {
	switch runtime.GOOS {
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", name)
		}
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, name)
		}
	}
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, name)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", name)
	}
	return ""
}
