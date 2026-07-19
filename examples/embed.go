// Package examples embeds terva's deployment and setup examples so an installed
// binary can lay them down without a source checkout present.
//
// Only examples/deploy/ and examples/workflows/ are embedded — the systemd
// units, reverse-proxy configs, and container recipes an operator needs to
// bootstrap a terva host, and the workflow scripts a `terva workflow run`
// user (human or sandboxed agent) needs as working references; all of it
// text. The rest of examples/ (extension source with compiled binaries,
// character-card PNGs, RPC/SDK samples) is developer material read from a
// checkout, and is deliberately not carried in the binary.
package examples

import (
	"bytes"
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

// embeddedExamples carries examples/deploy/ and examples/workflows/. Naming
// a directory embeds it recursively, and the `all:` prefix keeps any
// dot/underscore-prefixed file too — so the embed is the whole subtree, not
// a curated list that can drift (the failure docs.go's EnsureInstalled was
// rewritten to avoid).
//
//go:embed all:deploy all:workflows
var embeddedExamples embed.FS

// embeddedRoots names the embedded subtrees; the install walk and the
// everything-reaches-disk test both range over it.
var embeddedRoots = []string{"deploy", "workflows"}

// EnsureInstalled mirrors the embedded example trees into
// $TERVA_HOME/examples and returns that directory. A file whose content
// already matches is left alone, so it is cheap to call on every startup
// (which it is).
//
// Each tree lands under $TERVA_HOME/examples/<tree>/… — the same shape it
// has in the repo — so the relative links in the installed docs
// ($TERVA_HOME/docs/web.md → ../examples/deploy/haproxy/, docs/workflows.md
// → ../examples/workflows/) resolve on an install-only host exactly as they
// do in a checkout. An agent that is jail-restricted to $TERVA_HOME still
// reads them: build.go registers this directory as a read-only root.
func EnsureInstalled(tervaHome string) (string, error) {
	root := filepath.Join(tervaHome, "examples")
	for _, tree := range embeddedRoots {
		err := fs.WalkDir(embeddedExamples, tree, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			data, rerr := embeddedExamples.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			// embed paths are always slash-separated; make them native under $HOME.
			dst := filepath.Join(root, filepath.FromSlash(p))
			if existing, err := os.ReadFile(dst); err == nil && bytes.Equal(existing, data) {
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			return os.WriteFile(dst, data, 0o644)
		})
		if err != nil {
			return root, err
		}
	}
	return root, nil
}
