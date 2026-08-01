// Package filelock is the advisory cross-process lock terva takes around a
// file two instances can both write.
//
// It was written for the worktree registry and lifted here when auth.json
// needed the same guarantee: several terva instances share one $TERVA_HOME, and
// a read-modify-write of a whole-file JSON document is a lost update waiting to
// happen — silently, because the rename that makes each write atomic is exactly
// what stops anyone noticing the one it replaced.
//
// The OS releases these locks when the holding process dies, so an instance
// killed mid-write cannot wedge every other one. That is the property that made
// flock/LockFileEx the right primitive over a lockfile with a heartbeat: no
// staleness timeout to tune, and no window where a crashed holder has to be
// waited out before anything can proceed.
package filelock

import (
	"os"
	"path/filepath"
)

// openLockFile creates the lockfile's directory and opens (creating if absent)
// the file both platform implementations lock against.
//
// The directory is 0700, not 0755: every caller's lockfile lives under
// $TERVA_HOME, which privfs already pins to owner-only, and a lock beside a
// credential file has no business being more open than the thing it guards.
func openLockFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}
