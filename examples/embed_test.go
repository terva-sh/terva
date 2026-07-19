package examples

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// Every file under the real embedded trees must reach disk on an install. A
// doc that points a reader at examples/<tree>/<x> — docs/web.md and
// docs/deploy.md do for the systemd units and HAProxy configs,
// docs/workflows.md does for the example scripts — dangles otherwise, on
// exactly the install-only host that has no checkout to fall back on. This
// reads the source tree, not the embed, so it catches a file the glob
// somehow missed rather than restating the glob to itself (the reasoning
// docs.go records).
func TestEveryExampleTreeIsInstalled(t *testing.T) {
	root, err := EnsureInstalled(testsupport.TempDir(t))
	if err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	for _, tree := range embeddedRoots {
		err = filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			dst := filepath.Join(root, filepath.FromSlash(p))
			got, err := os.ReadFile(dst)
			if err != nil {
				t.Errorf("%s is in the source tree but was not installed to %s: %v", p, dst, err)
				return nil
			}
			if !bytes.Equal(src, got) {
				t.Errorf("%s installed with content that differs from the source", p)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s/: %v", tree, err)
		}
	}
}

// EnsureInstalled runs on every startup; a file whose content already matches
// must be left untouched.
func TestInstallIsIdempotent(t *testing.T) {
	home := testsupport.TempDir(t)
	root, err := EnsureInstalled(home)
	if err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(root, "deploy", "systemd", "terva-web.socket")
	before, err := os.Stat(probe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureInstalled(home); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(probe)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("a second install rewrote an already-current example")
	}
}

// A stale example is a wrong example: an upgrade whose content changed must land.
func TestOutdatedExampleIsRefreshed(t *testing.T) {
	home := testsupport.TempDir(t)
	root, err := EnsureInstalled(home)
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(root, "deploy", "systemd", "terva-web.socket")
	if err := os.WriteFile(stale, []byte("# from an older terva\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureInstalled(home); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(stale)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("from an older terva")) {
		t.Error("EnsureInstalled left a stale example in place")
	}
}
