package worktree

import (
	"os"
	"path/filepath"
)

// openLockFile creates the lockfile's directory and opens (creating if absent)
// the file both platform lock implementations lock against.
func openLockFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
}
