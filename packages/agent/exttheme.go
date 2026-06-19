package agent

import (
	"path/filepath"

	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/tui"
)

// extensionThemeFiles are the in-extension paths (relative to an
// extension's dir) where a bundled theme may live, highest priority
// first.
var extensionThemeFiles = []string{"theme.json", "themes/theme.json"}

// extensionThemeOptions builds the theme-picker entries for every loaded
// extension that ships a theme file. The wire layer stays theme-agnostic
// — it only exposes each extension's dir + name via Extensions() — so the
// theme-file convention and the tui.ThemeOption construction live here,
// in the tui-aware host. Wired into the interactive mode as the
// ExtensionThemes callback.
func extensionThemeOptions(mgr *extensions.Manager) []tui.ThemeOption {
	if mgr == nil {
		return nil
	}
	var out []tui.ThemeOption
	for _, ext := range mgr.Extensions() {
		for _, file := range extensionThemeFiles {
			path := filepath.Join(ext.Dir, file)
			// value == path so an extension theme loads in place without
			// being copied into $TERVA_HOME/themes; ThemeOptionFromFile
			// returns ok=false when the file is absent.
			if opt, ok := tui.ThemeOptionFromFile(path, path, "extension "+ext.Manifest.Name); ok {
				out = append(out, opt)
			}
		}
	}
	return out
}
