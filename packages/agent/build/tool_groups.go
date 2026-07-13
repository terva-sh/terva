package build

import "terva.sh/terva/packages/core"

// CoreToolGroup is the always-visible group of first-party built-in tools
// (read/write/edit/bash/grep/glob/status/ask/skill/task_*, …) — the coding
// tools that ARE the task, never an optional capability (retro H2·b principle 2).
// It re-exports core.CoreToolGroup so the classifier has a single home.
const CoreToolGroup = core.CoreToolGroup

// ToolGroup classifies a registered tool into its capability group for lazy
// tool visibility (retro H2·b). Extension and MCP tools report their source
// through an Extension() accessor — the extension name, or "mcp:<server>" — set
// where they merge into the registry (build/wiring.go); everything else is a
// built-in and belongs to the always-visible CoreToolGroup.
//
// This derives the group from the LIVE tool rather than a stored map because
// extensions and MCP servers merge per session, after Resolve: the group of a
// name is only knowable from the registry that actually carries it. It
// delegates to core.ToolGroup, which the agent loop uses directly.
func ToolGroup(t core.Tool) string { return core.ToolGroup(t) }

// ToolGroups derives the name→group map for a whole registry (see ToolGroup).
func ToolGroups(reg core.Registry) map[string]string {
	groups := make(map[string]string, len(reg))
	for name, t := range reg {
		groups[name] = ToolGroup(t)
	}
	return groups
}
