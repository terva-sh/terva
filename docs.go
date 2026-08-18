package docs

import (
	"bytes"
	"embed"
	"io/fs"
	"os"
	"path/filepath"

	"strings"
	"terva.sh/terva/packages/privfs"
)

// embeddedDocs holds terva's user-facing documentation so installed binaries do
// not depend on a source checkout existing on disk.
//
// The globs are the whole contract: every top-level docs/*.md, plus the
// user-facing subdirectories named in shippedSubdirs. Everything else under
// docs/ (plans/, proposals/, reviews/, decisions/, architecture/, vanity/) is
// the internal engineering tier and is deliberately not matched — those trees
// name internal infrastructure and are stripped from the public release too.
//
// "Subdirectory means internal" WAS the whole rule, and it stopped being true
// when design/ and practices/ arrived: both are shipped documentation that
// happens to be more than one file. A directory under docs/ must now be
// classified, and docs_test.go's TestEveryDocsSubdirIsClassified fails on any
// that is not — so the classification cannot be forgotten, only decided.
//
//go:embed docs/*.md docs/design/*.md docs/practices/*.md
var embeddedDocs embed.FS

// shippedSubdirs are the docs/ subdirectories that reach $TERVA_HOME/docs. It
// must agree with the go:embed globs above; the tests assert the agreement in
// both directions, because a directory added to one and not the other installs
// nothing and says nothing.
var shippedSubdirs = []string{"design", "practices"}

// EnsureInstalled mirrors the embedded docs into $TERVA_HOME/docs and returns
// that directory. A file whose content already matches is left alone.
//
// Every embedded doc is installed. The previous version wrote out a
// hand-maintained map of filenames instead, and it drifted: fifteen pages —
// web.md and permissions.md among them — were compiled into the binary and then
// never written, while `terva web --help` and the system prompt went on pointing
// readers at them. An operator who followed those pointers found nothing, so the
// installed directory is now a mirror of docs/ with nothing left to forget to
// update.
//
// The mirror preserves each page's path relative to docs/, which is what makes
// the docs' own relative links resolve: docs/README.md is the index and links to
// its top-level siblings by bare filename and into design/ and practices/ by
// one-segment path, and those tiers link back out with "../".
func EnsureInstalled(tervaHome string) (string, error) {
	dir := filepath.Join(tervaHome, "docs")
	if err := privfs.MkdirAll(dir); err != nil {
		return dir, err
	}

	globs := []string{"docs/*.md"}
	for _, sub := range shippedSubdirs {
		globs = append(globs, "docs/"+sub+"/*.md")
	}

	for _, glob := range globs {
		srcs, err := fs.Glob(embeddedDocs, glob)
		if err != nil {
			return dir, err
		}
		for _, src := range srcs {
			data, err := fs.ReadFile(embeddedDocs, src)
			if err != nil {
				return dir, err
			}
			// embed.FS always uses forward slashes; the destination is the same
			// path relative to docs/, joined with the host separator.
			rel := strings.TrimPrefix(src, "docs/")
			dst := filepath.Join(dir, filepath.FromSlash(rel))
			if existing, err := os.ReadFile(dst); err == nil && bytes.Equal(existing, data) {
				continue
			}
			if err := privfs.MkdirAll(filepath.Dir(dst)); err != nil {
				return dir, err
			}
			if err := privfs.WriteFile(dst, data); err != nil {
				return dir, err
			}
		}
	}
	return dir, nil
}
