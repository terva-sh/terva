package worktree

import "terva.sh/terva/packages/filelock"

// The registry lock and the liveness probe were written here and moved to
// packages/filelock when auth.json needed the same cross-process guarantee.
// Copying them would have left a twin to fix twice; these are the seams that
// keep this package's call sites (and its tests, which drive pidAlive directly)
// reading the way they always did.

func acquireLock(path string) (*filelock.Lock, error) { return filelock.Acquire(path) }

func pidAlive(pid int) bool { return filelock.PIDAlive(pid) }
