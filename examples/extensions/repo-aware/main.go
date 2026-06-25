// repo-aware — demonstrates the responsible way to make an extension's
// CONTEXT and TOOLS adapt to the workspace, per session.
//
// It registers one read-only tool (repo_root) and a standing context
// block. Both are only meaningful inside a git repository, so on every
// session boundary — OnSession, which also re-fires on a /cd — it decides:
//
//   - inside a repo:  publish the context block + restore the tool
//   - outside a repo: clear the context block  + withdraw the tool
//
// This is the pattern from docs/extensions.md "Responsible use: context &
// tools". The context block and the tool set live in the model's CACHED
// prompt prefix, so they are changed ONLY at a session boundary, via the
// Session handle (the cache-safe call site — the same methods on *Extension
// warn off a boundary). The tool half is gated on host protocol >= 4, so an
// older host simply keeps the tool visible (the pre-4 behavior) instead of
// breaking. The host pins the prefix per turn and no-ops an unchanged set, so
// re-asserting the same decision on the next session / a /cd is free.
//
// Build:
//
//	cd examples/extensions/repo-aware
//	go build -o repo-aware .
//
// Then `terva ext install ./repo-aware` (or `terva --ext .`). cd into a git
// repo and ask "what's the repo root?"; cd somewhere without a repo and the
// tool quietly disappears from the model's view.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"terva.sh/terva/packages/agent/ext"
)

func main() {
	e := ext.New("repo-aware", "1.0.0")

	// Correctness floor (not mere convenience): we key off the session's
	// CWD, which rides session_start and refreshes on /cd from protocol 2.
	// Without it we couldn't tell a repo from a non-repo, so we'd withdraw
	// the tool everywhere — wrong, not just degraded. So require it.
	e.RequireProtocol(2)

	// Read-only: it only reports a path. Declaring this lets the host admit
	// it in plan / read-only modes without a prompt — declare it honestly.
	e.Tool("repo_root", "Report the git repository root for the current workspace.",
		json.RawMessage(`{"type":"object","properties":{}}`),
		func(json.RawMessage) ext.ToolResult {
			if root := gitRoot(e.Host().CWD); root != "" {
				return ext.TextResult(root)
			}
			// Belt-and-suspenders: outside a repo this tool is withdrawn, but
			// a stale call from earlier context (e.g. across a compaction)
			// still refuses gracefully rather than returning nonsense.
			return ext.TextErrorResult("not inside a git repository")
		}, ext.ReadOnly())

	// Decide once per session, from the boundary, on the Session handle.
	e.OnSession(func(s ext.Session) {
		if root := gitRoot(s.CWD); root != "" {
			s.RefreshContext("The workspace is a git repository rooted at " + root +
				". Use the repo_root tool if you need that path again.")
			if s.ProtocolVersion() >= 4 {
				s.RestoreAllTools()
			}
		} else {
			s.RefreshContext("") // nothing repo-specific to add here
			if s.ProtocolVersion() >= 4 {
				s.WithdrawAllTools() // the tool can't do anything useful
			}
		}
	})

	if err := e.Run(); err != nil {
		e.Logf("fatal: %v", err)
	}
}

// gitRoot walks up from dir looking for a .git entry and returns the
// containing directory, or "" if none is found.
func gitRoot(dir string) string {
	for dir != "" {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir { // reached the filesystem root
			return ""
		}
		dir = parent
	}
	return ""
}
