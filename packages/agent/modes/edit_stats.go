package modes

// editStats sizes what the agent's file-mutating tools changed, for
// the status bar's edits segment. It reads the tools' structured
// Details (never the LLM-facing text): the edit tool ships its context
// diff, the write tool the written line count. Everything else — reads,
// bash, MCP tools — counts zero; if a future tool wants to contribute,
// it ships the same Details keys.

import (
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
)

// swarmAgentCount is the live (running or pending) background-agent
// count for the status bar's swarm segment. Swarm keeps its own
// locking; called per frame outside i.mu like statusUsageWindows. On
// the carrier path the count reads the cached tasks surface, which
// only re-fetches when the daemon signalled a change.
func (i *Interactive) swarmAgentCount() int {
	var rows []swarm.AgentSnapshot
	switch {
	case i.cfg.Carrier != nil && i.cfg.CarrierTasks:
		rows = i.carrierTaskSnapshot()
	default:
		return 0
	}
	n := 0
	for _, a := range rows {
		if a.Status == swarm.StatusRunning || a.Status == swarm.StatusPending {
			n++
		}
	}
	return n
}

// sessionShortName is the live session file's base name (extension
// trimmed) for the status bar's session segment.
func (i *Interactive) sessionShortName() string {
	if i.cfg.CurrentSessionPath == nil {
		return ""
	}
	p := i.cfg.CurrentSessionPath()
	if p == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
}

func editStats(tool string, res core.ToolResult) (added, removed int) {
	if res.IsError {
		return 0, 0
	}
	// First-class counts (set by the in-tree edit/write tools; they also ride
	// the event wire). The Details parse below stays as the fallback for
	// tools that only ship the map.
	if res.LinesAdded > 0 || res.LinesRemoved > 0 {
		return res.LinesAdded, res.LinesRemoved
	}
	details, ok := res.Details.(map[string]any)
	if !ok {
		return 0, 0
	}
	switch strings.ToLower(tool) {
	case "edit":
		diff, _ := details["diff"].(string)
		for _, l := range strings.Split(diff, "\n") {
			switch {
			case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
				// file headers, not content
			case strings.HasPrefix(l, "+"):
				added++
			case strings.HasPrefix(l, "-"):
				removed++
			}
		}
	case "write":
		// A whole-file write counts as added lines, matching how
		// Claude Code's total_lines_added treats it. Overwrites don't
		// subtract the old content — we don't know it here, and "the
		// agent wrote N lines" is the honest claim either way.
		if n, ok := details["total_lines"].(int); ok && n > 0 {
			added = n
		}
	}
	return added, removed
}
