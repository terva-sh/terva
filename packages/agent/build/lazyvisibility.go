package build

// lazyVisibilityEngages reports whether this build may hide tool groups:
// the lazy_tools flag is on AND activate_tools exists as the reveal path.
// The two are decided in different places — the registry at Resolve (which
// registers activate_tools only for base-workspace sessions), the agent at
// NewAgent — and their agreement is the flip-safety contract: hiding groups
// in a session that never registered activate_tools (chat, play, --no-tools,
// --no-workspace-tools) would bury extension and world tools with no way for
// the model to bring them back. Play acts ONLY through world-extension
// tools, so that is a hard break, not a degradation.
// TestLazyVisibilityEngages pins the rule.
func lazyVisibilityEngages(r *Resolved) bool {
	return r.LazyTools && r.ToolRegistry["activate_tools"] != nil
}
