package extensions

import "terva.sh/terva/packages/i18n"

// supersededExtensions names extensions whose capability moved into terva
// itself: an installed copy is skipped at load with a pointer, because running
// it would register a second, colliding implementation of the same tool names
// against a state home the built-in has migrated away from. The message shows
// in the startup log and explains what to do; `terva ext list` still shows the
// install.
// Superseded returns the explanation for a superseded extension, or "" if the
// name is not one. Exported so surfaces outside this package can agree with the
// loader about what it will refuse — `ext doctor` needs to say why an installed
// extension never starts, and the first-run pack must not offer one at all.
//
// The returned string is UNTRANSLATED: the table below marks each explanation
// with i18n.M because a var initializer runs before i18n.Configure, so calling
// T there would freeze whichever language was active at init. Every caller that
// DISPLAYS the result must pass it through i18n.T. Callers that only test it
// against "" need nothing, and that is most of them.
func Superseded(name string) string { return supersededExtensions[name] }

var supersededExtensions = map[string]string{
	// Stage 1 of the fold-in (docs/proposals — worktree fold-in): the five
	// worktree_* tools are built in, and the swarm's --swarm-worktrees lease
	// no longer needs the extension either. State migrates on first touch;
	// existing checkouts stay valid at their extension-era paths.
	"git-worktree": i18n.M("the worktree tools are built into terva now; uninstall with `terva ext remove git-worktree`"),
	// Durable memory moved into core across #511/#513 (docs/proposals/
	// memory-in-core.md): the tool, the injected block, /memory and the status
	// glance all have built-in equivalents, and memory.Adopt copies
	// ext-data/memory/ forward on first touch, so nothing is lost by not
	// loading this.
	//
	// Retiring it here rather than leaning on core's stand-down removes a whole
	// class of failure instead of one instance. #526 was the instance: the
	// stand-down deleted core's tool while the merge had already skipped the
	// extension's, leaving a session with no memory tool at all. The stand-down
	// is a two-step dance between two implementations, and the reliable way to
	// win it is to have one implementation.
	"memory": i18n.M("durable memory is built into terva now; uninstall with `terva ext remove memory`"),
	// Cross-session search moved into core as the session_search tool. Retired
	// here rather than left to win a tool-name collision, for the reason the
	// entry above spells out: one implementation beats a two-step dance between
	// two.
	//
	// Nothing is lost by not loading it. The extension's state is an FTS index
	// DERIVED from the transcripts, and core does not use an index at all — a
	// full-fidelity scan of the largest project measured 456ms — so there is no
	// Adopt step here as there is for memory. The transcripts were always the
	// source of truth; the index was a copy, and the copy is what stops being
	// maintained.
	//
	// It is also strictly a capability gain: the bridge it read through
	// (list_sessions / read_session, protocol 3) flattens transcripts to
	// role+text, which drops tool calls, their arguments, and their results —
	// 98% of the searchable bytes on a measured session — and cannot see swarm
	// sub-agents at all.
	"session-search": i18n.M("cross-session search is built into terva now (the session_search tool, which also searches sub-agents); uninstall with `terva ext remove session-search`"),
}
