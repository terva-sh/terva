package memory

import (
	"os"
	"path/filepath"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/privfs"
)

// Layout under $TERVA_HOME:
//
//	memory/
//	  user.md                           cross-project facts about the user
//	  archive/<id>.md                   archived user facts, retrieved by keyword
//	  projects/<ProjectKey>/memory.md   facts about one repo
//	  projects/<ProjectKey>/archive/    archived project facts
//
// <ProjectKey> is core.ProjectKey(cwd) — the same key the sessions directory
// buckets by, and the same one the memory EXTENSION used for its own per-project
// dirs, which is what makes the copy-forward below a straight file move rather
// than a re-keying.
const (
	dirName         = "memory"
	projectsDirName = "projects"

	// extDataDirName / extName locate the extension's data, the source of the
	// one-time copy-forward. Named here rather than imported from the extension
	// driver because this is a fixed historical path, not a live coupling: when
	// the extension is retired these two constants and Adopt go with it.
	extDataDirName = "ext-data"
	extName        = "memory"
)

// Root is the memory directory under a terva home.
func Root(home string) string { return filepath.Join(home, dirName) }

// UserDir is where user.md lives — the memory root itself, so the cross-project
// scope sits beside (not inside) the per-project buckets.
func UserDir(home string) string { return Root(home) }

// ProjectDir is where a project's memory.md lives. An empty cwd yields "", which
// Rebind reads as "no target": a session with no resolvable project keeps its
// user memory and simply has no project scope, rather than writing project facts
// into a shared bucket where they would surface in an unrelated repo.
func ProjectDir(home, cwd string) string {
	if home == "" || cwd == "" {
		return ""
	}
	return filepath.Join(Root(home), projectsDirName, core.ProjectKey(cwd))
}

// ArchiveDir is where a scope's archived entries live, given that scope's own
// directory — one rule for both scopes, so the two cannot end up on different
// layouts. Empty in, empty out: an unbound scope has no archive either, which is
// what keeps a --no-session run from writing project facts anywhere.
//
// Nothing migrates into here. The extension never had an archived tier, so
// Adopt has nothing to copy forward and deliberately does not look.
func ArchiveDir(scopeDir string) string {
	if scopeDir == "" {
		return ""
	}
	return filepath.Join(scopeDir, archiveDirName)
}

// Adopt copies the memory extension's files into core's layout, once, when core
// has none of its own. It is how a user who has been running the extension keeps
// their memories when it is retired.
//
// Same shape the worktree tools used when they were folded in from
// terva-git-worktree (worktree.Env.LegacyRoot): a first-class root under
// $TERVA_HOME, and the retired extension's ext-data dir migrated on first touch.
//
// COPY, never move. The originals stay exactly where they were, so an install
// that rolls back to the extension finds its data intact and no bug in this
// function can destroy a memory. The cost is a duplicate that goes stale, which
// is the right trade against losing facts a user spent months accumulating.
//
// Skips silently whenever core already has a file: after the first adoption the
// core copy is authoritative and re-copying would revert whatever has been
// written since. Errors are returned for logging, never fatal — a failed
// adoption means starting with an empty memory, not failing to start.
func Adopt(home, cwd string) error {
	if home == "" {
		return nil
	}
	extRoot := filepath.Join(home, extDataDirName, extName)
	if _, err := os.Stat(extRoot); err != nil {
		return nil // extension never ran here; nothing to adopt
	}
	if err := adoptFile(
		filepath.Join(extRoot, userFileName),
		filepath.Join(UserDir(home), userFileName),
	); err != nil {
		return err
	}
	dir := ProjectDir(home, cwd)
	if dir == "" {
		return nil
	}
	// The extension bucketed by the same ProjectKey, so the source is the
	// identically-named directory under its own projects/ tree.
	return adoptFile(
		filepath.Join(extRoot, projectsDirName, core.ProjectKey(cwd), projectFileName),
		filepath.Join(dir, projectFileName),
	)
}

// adoptFile copies src to dst when dst does not exist and src does. Written
// through privfs so the adopted copy lands with owner-only modes even though the
// extension wrote 0644.
func adoptFile(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return nil // core already owns this scope
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return nil // nothing to adopt for this scope
	}
	if err := privfs.MkdirAll(filepath.Dir(dst)); err != nil {
		return err
	}
	return privfs.WriteFile(dst, b)
}
