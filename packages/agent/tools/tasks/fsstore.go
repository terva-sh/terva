package tasks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/privfs"
)

// FileStore is the minimal persistence surface the Store needs: read, write, and
// resolve-to-a-writable-path, all by relative name. It is an interface so the
// store stays SDK-independent and unit-testable.
//
// In production the app injects the terva SDK's ext.DataFS — a read-through view
// that layers the writable DataDir over the read-only install dir — which
// satisfies this interface as-is. That layering is what auto-migrates a board
// written under the old DataDir (which became the install dir when terva split
// the writable data dir out): ReadFile falls through to the old copy, and the
// next WriteFile rewrites it into the new DataDir. Tests inject a DirFS.
type FileStore interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm os.FileMode) error
	// Path returns the writable path a WriteFile(name) would land at, without
	// creating anything — used to quarantine a corrupt file aside.
	Path(name string) (string, error)
}

// DirFS is a single-directory FileStore: every name resolves under root. It is
// the test double for the store, and a usable fallback for any context without
// the SDK's layered DataFS. Names are relative and must not escape root; callers
// pass traversal-safe names (see sessionFileName), and DirFS guards regardless.
type DirFS struct{ root string }

// NewDirFS returns a FileStore rooted at dir.
func NewDirFS(dir string) DirFS { return DirFS{root: dir} }

func (d DirFS) resolve(name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || name == ".." ||
		strings.HasPrefix(name, ".."+string(filepath.Separator)) ||
		strings.Contains(name, string(filepath.Separator)) {
		return "", fmt.Errorf("invalid data file name %q", name)
	}
	return filepath.Join(d.root, name), nil
}

// ReadFile reads name from root.
func (d DirFS) ReadFile(name string) ([]byte, error) {
	p, err := d.resolve(name)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// WriteFile writes name under root, creating root if needed. Boards are
// user-private state, so the write is routed through privfs regardless of
// perm: the directory is created 0700, and the file lands atomically
// (temp + rename) as 0600 — which also repairs the mode of a pre-existing
// permissive board, since the rename replaces the old inode. perm is kept
// only for FileStore interface compatibility (the SDK's ext.DataFS honors
// it; the built-in store does not need to).
func (d DirFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	p, err := d.resolve(name)
	if err != nil {
		return err
	}
	_ = perm
	return privfs.WriteFile(p, data)
}

// Path returns root/name without creating anything.
func (d DirFS) Path(name string) (string, error) { return d.resolve(name) }
