//go:build terva_gosh

package gosh

import (
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"time"

	gobashfs "github.com/mark3labs/go-bash/fs"

	"terva.sh/terva/packages/agent/tools"
)

// guardFS wraps the workspace mount and applies the Sandbox to every call.
// mountfs strips the mount point before it calls a child, so guardFS adds
// the host root back to get the path the Sandbox knows.
//
// 🔑 Reads go through CheckPathRead, the deny list the read tool uses, so
// cat cannot open what read refuses. Writes go through CheckPath (the
// write containment) and CheckPathRead (so nothing writes over a secret).
// Stat and ReadDir stay open, as they are for the read tool's neighbours:
// a listing names a file and does not leak its bytes.
type guardFS struct {
	gobashfs.FileSystem
	root string
	sb   *tools.Sandbox
}

func (g *guardFS) host(name string) string {
	return filepath.Join(g.root, filepath.FromSlash(gobashfs.Clean("/"+name)))
}

func (g *guardFS) read(name string) error { return g.sb.CheckPathRead(g.host(name)) }
func (g *guardFS) write(name string) error {
	h := g.host(name)
	if err := g.sb.CheckPath(h); err != nil {
		return err
	}
	return g.sb.CheckPathRead(h)
}

func (g *guardFS) Open(name string) (iofs.File, error) {
	if err := g.read(name); err != nil {
		return nil, err
	}
	return g.FileSystem.Open(name)
}

func (g *guardFS) ReadFile(name string) ([]byte, error) {
	if err := g.read(name); err != nil {
		return nil, err
	}
	return g.FileSystem.ReadFile(name)
}

func (g *guardFS) Readlink(name string) (string, error) {
	if err := g.read(name); err != nil {
		return "", err
	}
	return g.FileSystem.Readlink(name)
}

func (g *guardFS) OpenFile(name string, flag int, perm os.FileMode) (gobashfs.File, error) {
	check := g.read
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		check = g.write
	}
	if err := check(name); err != nil {
		return nil, err
	}
	return g.FileSystem.OpenFile(name, flag, perm)
}

func (g *guardFS) Create(name string) (gobashfs.File, error) {
	if err := g.write(name); err != nil {
		return nil, err
	}
	return g.FileSystem.Create(name)
}

func (g *guardFS) Mkdir(name string, perm os.FileMode) error {
	if err := g.write(name); err != nil {
		return err
	}
	return g.FileSystem.Mkdir(name, perm)
}

func (g *guardFS) MkdirAll(name string, perm os.FileMode) error {
	if err := g.write(name); err != nil {
		return err
	}
	return g.FileSystem.MkdirAll(name, perm)
}

func (g *guardFS) Remove(name string) error {
	if err := g.write(name); err != nil {
		return err
	}
	return g.FileSystem.Remove(name)
}

func (g *guardFS) RemoveAll(name string) error {
	if err := g.write(name); err != nil {
		return err
	}
	if err := g.holdsSecrets("remove", name); err != nil {
		return err
	}
	return g.FileSystem.RemoveAll(name)
}

func (g *guardFS) Rename(oldpath, newpath string) error {
	if err := g.write(oldpath); err != nil {
		return err
	}
	if err := g.holdsSecrets("rename", oldpath); err != nil {
		return err
	}
	if err := g.write(newpath); err != nil {
		return err
	}
	return g.FileSystem.Rename(oldpath, newpath)
}

// holdsSecrets refuses to move or delete a directory that holds a secret.
// The deny list matches by path, so `mv cfg c2` would carry cfg/terva out
// from under its root, and `cat c2/terva/auth.json` would then be allowed.
func (g *guardFS) holdsSecrets(op, name string) error {
	if g.sb.HoldsSecrets(g.host(name)) {
		return &iofs.PathError{Op: op, Path: name, Err: errors.New("it holds files that tools may never read")}
	}
	return nil
}

// Symlink always refuses. rwfs refuses too while its AllowSymlinks is off,
// and it refuses a link in any component of a path, but the jail should not
// rest on one option of another package. A link would also need its target
// checked, and the target is resolved only when something follows it.
func (g *guardFS) Symlink(target, linkpath string) error {
	return &iofs.PathError{Op: "symlink", Path: linkpath, Err: errors.New("symlinks are off in this shell")}
}

func (g *guardFS) Link(oldpath, newpath string) error {
	if err := g.read(oldpath); err != nil {
		return err
	}
	if err := g.write(newpath); err != nil {
		return err
	}
	return g.FileSystem.Link(oldpath, newpath)
}

func (g *guardFS) Chmod(name string, mode os.FileMode) error {
	if err := g.write(name); err != nil {
		return err
	}
	return g.FileSystem.Chmod(name, mode)
}

func (g *guardFS) Chtimes(name string, atime, mtime time.Time) error {
	if err := g.write(name); err != nil {
		return err
	}
	return g.FileSystem.Chtimes(name, atime, mtime)
}

func (g *guardFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	if err := g.write(name); err != nil {
		return err
	}
	return g.FileSystem.WriteFile(name, data, perm)
}

func (g *guardFS) AppendFile(name string, data []byte, perm os.FileMode) error {
	if err := g.write(name); err != nil {
		return err
	}
	return g.FileSystem.AppendFile(name, data, perm)
}
