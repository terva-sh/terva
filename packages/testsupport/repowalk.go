package testsupport

import (
	"io/fs"
	"os"
	"path/filepath"
)

// Directories a repo-wide source scan never descends into, by name: version
// control, agent scratch space, build output, and third-party trees. None of
// them hold first-party source, and several hold enough files to dominate a
// walk's runtime on their own.
var skipByName = map[string]bool{
	".git":         true,
	".claude":      true,
	"bin":          true,
	"dist":         true,
	"node_modules": true,
	"vendor":       true,
}

// SkipScanDir reports whether a repository-wide source scan should skip this
// directory. Every guard test that walks the tree looking for a banned pattern
// must consult it, because the interesting failure mode is not what such a scan
// finds — it is what it walks into.
//
// Four of these guards existed with four hand-written skip lists, and the fourth
// was written from scratch rather than copied. It omitted .claude, so it
// descended into the git worktrees kept under .claude/worktrees and reported
// every file in them as an offender: a whole class of `just ci` failures that
// named real paths, in branches predating the rule being enforced, on a checkout
// whose own sources were clean. A red gate that is right about the file and
// wrong about the repository is worse than no gate — it teaches everyone to
// disbelieve the next red run. One predicate, so the lists cannot drift again.
//
// The nested-checkout rule is the general form of that bug. Any directory
// holding a .git entry is a separate checkout — a worktree, a clone dropped in
// the tree, a fixture repo — and its contents are somebody else's source, held
// to somebody else's conventions. The name list only covers where this project
// puts them by convention; this covers wherever they actually are.
//
// root is the walk's starting directory. Pass the same value given to
// filepath.WalkDir, so the root's own .git does not skip the entire scan.
func SkipScanDir(root, path string, d fs.DirEntry) bool {
	if !d.IsDir() {
		return false
	}
	if skipByName[d.Name()] {
		return true
	}
	return isNestedCheckout(root, path)
}

// isNestedCheckout reports whether path is a git checkout other than the walk's
// own root. A worktree's .git is a FILE (a gitdir pointer) rather than a
// directory, so this tests for the entry's presence, not its kind.
func isNestedCheckout(root, path string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	if absPath == absRoot {
		return false // the repository being scanned
	}
	_, err = os.Stat(filepath.Join(path, ".git"))
	return err == nil
}
