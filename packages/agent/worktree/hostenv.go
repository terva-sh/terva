package worktree

import "path/filepath"

// HostRoots returns the managed-state pair for a $TERVA_HOME: the first-class
// root, and the retired terva-git-worktree extension's data dir that a repo is
// migrated off on first touch.
//
// The two must be named together. Root is where the registry lives and
// LegacyRoot is what triggers the one-time migration, so a caller that gets one
// of them wrong — or omits LegacyRoot — is addressing a DIFFERENT registry for
// the same repo, and resolveRepo succeeds against an empty one. There is no
// error; the worktrees simply are not there.
//
// Three production call sites built this pair from the same two literals with
// no shared constructor: the tool registry (build.BuildToolRegistry), the
// swarm's worktree carrier, and the web Worktrees panel's session env. They
// agreed, which is the only reason nothing had broken yet. packages/agent/config
// owns a whole family of $TERVA_HOME-relative helpers for exactly this reason,
// and UserModelsPath's comment states the rule: "One helper, so the two callers
// cannot drift onto different files."
//
// tervaHome is a parameter rather than a config.TervaHome() call because this
// package deliberately imports nothing but privfs and filelock. Keeping it that
// way is worth more than saving the argument.
func HostRoots(tervaHome string) (root, legacyRoot string) {
	return filepath.Join(tervaHome, "worktrees"),
		filepath.Join(tervaHome, "ext-data", "git-worktree")
}

// HostEnv is the per-call environment for a host that keeps its worktree state
// under tervaHome. The three production callers differ only in cwd and the
// owning session.
func HostEnv(tervaHome, cwd, sessionID string) Env {
	root, legacy := HostRoots(tervaHome)
	return Env{Root: root, LegacyRoot: legacy, CWD: cwd, SessionID: sessionID}
}
