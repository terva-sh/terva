package docs

import (
	"bytes"
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

// embeddedDocs holds terva's user-facing documentation so installed binaries do
// not depend on a source checkout existing on disk.
//
// The glob is the whole contract. Every top-level docs/*.md ships; the
// subdirectories (plans/, proposals/, reviews/, decisions/, architecture/) are
// the internal tier and are deliberately not matched. That shape — not a second
// list kept in sync by hand — is what decides which pages are user-facing.
//
//go:embed docs/*.md
var embeddedDocs embed.FS

// EnsureInstalled mirrors the embedded docs into $TERVA_HOME/docs and returns
// that directory. A file whose content already matches is left alone.
//
// Every embedded doc is installed. The previous version wrote out a
// hand-maintained map of filenames instead, and it drifted: fifteen pages —
// web.md and permissions.md among them — were compiled into the binary and then
// never written, while `terva web --help` and the system prompt went on pointing
// readers at them. An operator who followed those pointers found nothing, so the
// installed directory is now a flat mirror of docs/ with nothing left to forget
// to update.
//
// Note that the flat mirror is also what makes the docs' own relative links
// resolve: docs/README.md is the index, and it links to its siblings by bare
// filename.
func EnsureInstalled(tervaHome string) (string, error) {
	dir := filepath.Join(tervaHome, "docs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return dir, err
	}

	srcs, err := fs.Glob(embeddedDocs, "docs/*.md")
	if err != nil {
		return dir, err
	}
	for _, src := range srcs {
		data, err := fs.ReadFile(embeddedDocs, src)
		if err != nil {
			return dir, err
		}
		dst := filepath.Join(dir, filepath.Base(src))
		if existing, err := os.ReadFile(dst); err == nil && bytes.Equal(existing, data) {
			continue
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return dir, err
		}
	}
	return dir, nil
}
