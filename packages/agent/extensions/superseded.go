package extensions

// supersededExtensions names extensions whose capability moved into terva
// itself: an installed copy is skipped at load with a pointer, because running
// it would register a second, colliding implementation of the same tool names
// against a state home the built-in has migrated away from. The message shows
// in the startup log and explains what to do; `terva ext list` still shows the
// install.
var supersededExtensions = map[string]string{
	// Stage 1 of the fold-in (docs/proposals — worktree fold-in): the five
	// worktree_* tools are built in, and the swarm's --swarm-worktrees lease
	// no longer needs the extension either. State migrates on first touch;
	// existing checkouts stay valid at their extension-era paths.
	"git-worktree": "the worktree tools are built into terva now; uninstall with `terva ext remove git-worktree`",
}
