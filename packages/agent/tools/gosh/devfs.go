//go:build terva_gosh

package gosh

import (
	"io"
	iofs "io/fs"
	"os"
	"path"
	"time"

	gobashfs "github.com/mark3labs/go-bash/fs"
)

// devNull is the device, not a file.
//
// 🚨 With a custom filesystem go-bash creates no /dev at all, so every
// `2>/dev/null` failed with "open /dev: file does not exist" and took its
// command down with it. The GNU comparison found 300 scripts whose output
// came back empty this way. Even go-bash's own layout makes /dev/null an
// ordinary file, which keeps what is written to it.
const devNull = "/dev/null"

// devFS answers /dev/null itself and passes every other path through.
type devFS struct {
	gobashfs.FileSystem
}

func isNull(name string) bool { return gobashfs.Clean("/"+name) == devNull }

func (d *devFS) Open(name string) (iofs.File, error) {
	if isNull(name) {
		return nullFile{}, nil
	}
	return d.FileSystem.Open(name)
}

func (d *devFS) OpenFile(name string, flag int, perm os.FileMode) (gobashfs.File, error) {
	if isNull(name) {
		return nullFile{}, nil
	}
	return d.FileSystem.OpenFile(name, flag, perm)
}

func (d *devFS) Create(name string) (gobashfs.File, error) {
	if isNull(name) {
		return nullFile{}, nil
	}
	return d.FileSystem.Create(name)
}

func (d *devFS) Stat(name string) (os.FileInfo, error) {
	if isNull(name) {
		return nullInfo{}, nil
	}
	return d.FileSystem.Stat(name)
}

func (d *devFS) Lstat(name string) (os.FileInfo, error) {
	if isNull(name) {
		return nullInfo{}, nil
	}
	return d.FileSystem.Lstat(name)
}

func (d *devFS) ReadFile(name string) ([]byte, error) {
	if isNull(name) {
		return nil, nil
	}
	return d.FileSystem.ReadFile(name)
}

func (d *devFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	if isNull(name) {
		return nil
	}
	return d.FileSystem.WriteFile(name, data, perm)
}

func (d *devFS) AppendFile(name string, data []byte, perm os.FileMode) error {
	if isNull(name) {
		return nil
	}
	return d.FileSystem.AppendFile(name, data, perm)
}

// skipDirFS hides the directories whose name matches one of globs from
// ReadDir. It gives go-bash's grep --exclude-dir, which it lacks: its
// recursive walk reads the tree only through ReadDir.
type skipDirFS struct {
	gobashfs.FileSystem
	globs []string
}

func (s *skipDirFS) ReadDir(name string) ([]iofs.DirEntry, error) {
	entries, err := s.FileSystem.ReadDir(name)
	if err != nil {
		return entries, err
	}
	kept := entries[:0:0]
	for _, e := range entries {
		if !e.IsDir() || !s.skipped(e.Name()) {
			kept = append(kept, e)
		}
	}
	return kept, nil
}

func (s *skipDirFS) skipped(name string) bool {
	for _, g := range s.globs {
		if ok, _ := path.Match(g, name); ok {
			return true
		}
	}
	return false
}

type nullFile struct{}

func (nullFile) Read([]byte) (int, error)             { return 0, io.EOF }
func (nullFile) Write(p []byte) (int, error)          { return len(p), nil }
func (nullFile) Seek(int64, int) (int64, error)       { return 0, nil }
func (nullFile) Truncate(int64) error                 { return nil }
func (nullFile) Close() error                         { return nil }
func (nullFile) Stat() (iofs.FileInfo, error)         { return nullInfo{}, nil }
func (nullFile) ReadDir(int) ([]iofs.DirEntry, error) { return nil, os.ErrInvalid }

type nullInfo struct{}

func (nullInfo) Name() string       { return "null" }
func (nullInfo) Size() int64        { return 0 }
func (nullInfo) Mode() os.FileMode  { return os.ModeDevice | os.ModeCharDevice | 0o666 }
func (nullInfo) ModTime() time.Time { return time.Time{} }
func (nullInfo) IsDir() bool        { return false }
func (nullInfo) Sys() any           { return nil }
