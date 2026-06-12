package docs

import (
	"bytes"
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// embeddedDocs holds terva's user-facing documentation so installed
// binaries do not depend on a source checkout existing on disk.
//
//go:embed README.md docs/*.md
var embeddedDocs embed.FS

var docFiles = map[string]string{
	"README.md":     "README.md",
	"cli.md":        "docs/cli.md",
	"connectors.md": "docs/connectors.md",
	"fork.md":       "docs/fork.md",
	"extensions.md": "docs/extensions.md",
	"models.md":     "docs/models.md",
	"providers.md":  "docs/providers.md",
	"rpc.md":        "docs/rpc.md",
	"skills.md":     "docs/skills.md",
	"themes.md":     "docs/themes.md",
	"tui.md":        "docs/tui.md",
}

// EnsureInstalled writes the embedded docs into $TERVA_HOME/docs and
// returns that directory. Existing files are left untouched when their
// content already matches the embedded copy.
func EnsureInstalled(tervaHome string) (string, error) {
	dir := filepath.Join(tervaHome, "docs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return dir, err
	}

	names := make([]string, 0, len(docFiles))
	for name := range docFiles {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		src := docFiles[name]
		data, err := fs.ReadFile(embeddedDocs, src)
		if err != nil {
			return dir, err
		}
		dst := filepath.Join(dir, name)
		if existing, err := os.ReadFile(dst); err == nil && bytes.Equal(existing, data) {
			continue
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return dir, err
		}
	}
	return dir, nil
}
