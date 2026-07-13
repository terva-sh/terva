package tasks

import "os"

// LayeredFS is a read-through FileStore: reads try the writable upper dir first
// and fall through to a read-only lower dir; writes and Path always target
// upper. This is the built-in equivalent of the SDK's ext.DataFS layering — it
// lets the in-core tasks tool adopt boards the old terva-tasks extension wrote
// to $TERVA_HOME/ext-data/tasks (the lower dir): a legacy board loads via
// fall-through and migrates forward into the built-in's own dir (upper) on its
// next write, without ever mutating the lower copy.
type LayeredFS struct {
	upper DirFS
	lower DirFS
}

// NewLayeredFS returns a read-through store rooted at upperDir, falling through
// to lowerDir on reads. Writes land in upperDir.
func NewLayeredFS(upperDir, lowerDir string) LayeredFS {
	return LayeredFS{upper: NewDirFS(upperDir), lower: NewDirFS(lowerDir)}
}

// ReadFile reads name from upper, falling through to lower only when upper has
// no such file. A non-not-exist upper error (e.g. a permission problem) is
// returned as-is rather than masked by the fall-through. When neither has the
// file, upper's not-exist error is returned so the store treats it as a fresh,
// empty board.
func (l LayeredFS) ReadFile(name string) ([]byte, error) {
	data, err := l.upper.ReadFile(name)
	if err == nil || !os.IsNotExist(err) {
		return data, err
	}
	if lo, lerr := l.lower.ReadFile(name); lerr == nil {
		return lo, nil
	}
	return data, err
}

// WriteFile always writes to upper (copy-on-write forward from lower).
func (l LayeredFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	return l.upper.WriteFile(name, data, perm)
}

// Path returns the upper path a WriteFile(name) would land at.
func (l LayeredFS) Path(name string) (string, error) { return l.upper.Path(name) }
