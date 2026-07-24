// Package privfs centralizes creation of user-private terva state on disk.
//
// $TERVA_HOME holds material meant only for the invoking user: config.json
// (which stores extension secrets in plaintext), conversation transcripts,
// error sidecars, and subprocess logs. Historically terva wrote these as 0644
// files under 0755 directories, so on a multi-user or shared-home system
// another local account could read them. This package writes new state as
// 0600/0700, offers an atomic private write for config, and repairs
// pre-existing permissive state once per install.
package privfs

import (
	"io/fs"
	"os"
	"path/filepath"
)

const (
	// DirMode is the permission for private directories: owner rwx only.
	DirMode os.FileMode = 0o700
	// FileMode is the permission for private files: owner rw only.
	FileMode os.FileMode = 0o600

	// repairMarkerName is written under the home once RepairOnce has run, so
	// the (potentially large) tree walk happens at most once per install.
	// Versioned so a future stricter repair can re-run by bumping the suffix.
	// v2: task boards and swarm state (events.jsonl, meta.json) kept writing
	// permissive modes after the v1 walk shipped; re-walk once to tighten
	// what accumulated in between.
	repairMarkerName = ".perms-repaired-v2"
)

// MkdirAll creates dir and any missing parents with private (0700) permissions.
func MkdirAll(dir string) error { return os.MkdirAll(dir, DirMode) }

// OpenFile opens/creates path with the given flags; a newly created file gets
// 0600. Like os.OpenFile, the mode applies only on creation — an existing file
// keeps its mode (the one-time RepairOnce walk tightens legacy files).
func OpenFile(path string, flag int) (*os.File, error) {
	return os.OpenFile(path, flag, FileMode)
}

// WriteFile writes data to path atomically with 0600 permissions: it writes a
// temp file in the same directory, chmods it 0600 (defeating a permissive
// umask), then renames over path. The parent directory is created 0700 if
// missing. Used for config.json, which carries plaintext secrets.
func WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := MkdirAll(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename; a no-op after success.
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, FileMode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// RepairTree walks root and tightens any file or directory whose group or other
// permission bits are set, clearing those bits while preserving the owner bits
// — including the execute bit, so extension binaries stay runnable. It returns
// the number of entries it changed. Symlinks are neither followed nor chmod'd,
// and unreadable entries are skipped rather than aborting the walk.
func RepairTree(root string) (int, error) {
	var changed int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip entries we can't stat; keep repairing the rest
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		perm := info.Mode().Perm()
		if perm&0o077 == 0 {
			return nil // already private to the owner
		}
		if err := os.Chmod(path, perm&0o700); err == nil {
			changed++
		}
		return nil
	})
	return changed, err
}

// RepairOnce tightens permissions on an existing home exactly once per install.
// It is a no-op when the marker file already exists or when home does not yet
// exist (a fresh install writes private modes from the start). Otherwise it runs
// RepairTree and writes the marker — even on a partial repair — so the walk does
// not repeat every startup. Returns the number of entries changed.
func RepairOnce(home string) (int, error) {
	if home == "" {
		return 0, nil
	}
	marker := filepath.Join(home, repairMarkerName)
	if _, err := os.Stat(marker); err == nil {
		return 0, nil
	}
	if _, err := os.Stat(home); err != nil {
		return 0, nil // nothing on disk yet
	}
	changed, repairErr := RepairTree(home)
	_ = os.WriteFile(marker, []byte("terva tightened home permissions to 0600/0700\n"), FileMode)
	return changed, repairErr
}

// DirPerm is the permission posture of a directory that is expected to hold
// private state (credentials, tokens, plaintext config). It carries only mode
// metadata — never a byte of the directory's contents — so it is safe to print.
type DirPerm struct {
	Path    string      // the directory inspected
	Exists  bool        // false when nothing is on disk yet
	Mode    os.FileMode // the permission bits (Perm()); zero when absent
	TooOpen bool        // true when any group or other bit is set
}

// RepairCommand is the safe, explicit shell command an operator can run to
// tighten a too-open directory to owner-only. Empty when the directory is
// already private or absent. It names only the path, never any contents.
func (d DirPerm) RepairCommand() string {
	if !d.TooOpen {
		return ""
	}
	return "chmod 700 " + d.Path
}

// InspectDir reports whether dir is group/world-accessible, reading only its
// mode via a single stat — it never opens the directory or any file within, so
// it cannot leak a secret. A missing directory is not "too open": a fresh
// install creates it privately (see MkdirAll), so there is nothing to repair.
// Used by `terva doctor` to diagnose a pre-existing permissive config root
// without silently changing it.
func InspectDir(dir string) DirPerm {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return DirPerm{Path: dir}
	}
	perm := info.Mode().Perm()
	return DirPerm{Path: dir, Exists: true, Mode: perm, TooOpen: perm&0o077 != 0}
}
