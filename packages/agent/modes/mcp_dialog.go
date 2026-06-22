package modes

import (
	"fmt"

	"terva.sh/terva/packages/tui"
)

// MCPInfo is one configured MCP server's display + state for the /mcp
// dialog. The host (cli) scans the user + trusted-project mcp.servers
// config, overlays the live Manager's connection/tool state, and resolves
// the disable flags; this package only renders it and emits toggle
// actions. Unlike extensions there is no per-server manifest — "enabled"
// means the name is absent from disable_mcp.
//
// Two independent controls, mirroring /extensions:
//
//   - 'g' (global): adds/removes the server from the USER config's
//     disable_mcp. Only meaningful for a user-defined server (Scope
//     "global"); a project-defined server has nothing to toggle here.
//   - 'p' (project): adds/removes the server from THIS project's
//     disable_mcp. Restrict-only — it can disable a user server here, but
//     can never enable one the user disabled.
type MCPInfo struct {
	Name        string
	Scope       string // "global" (user-defined) | "project"
	Description string // serverInfo name or the command, for the detail line

	UserDisabled    bool
	ProjectDisabled bool
	ProjectGated    bool // project-defined server in an untrusted workspace
	Effective       bool // config says it should run

	// Connected and Tools are ground truth from the live Manager.
	// Connected can be false while Effective is true — a server that
	// failed to spawn (see StartupError).
	Connected bool
	Tools     int

	StartupError string // from Manager.Warnings(), if it failed to start
	HasLog       bool
}

// mcpDialog lists configured MCP servers and toggles them on/off globally
// (user config) or per-project (project config). Opened with /mcp.
type mcpDialog struct {
	active bool
	items  []MCPInfo
	cursor int
	status string
}

func newMCPDialog() *mcpDialog { return &mcpDialog{} }

// Open shows the dialog over the given configured-server list.
func (d *mcpDialog) Open(items []MCPInfo) {
	d.active = true
	d.items = items
	d.cursor = 0
	d.status = ""
}

// SetItems refreshes the list in place (after a toggle + reload), keeping
// the cursor on the same row when possible.
func (d *mcpDialog) SetItems(items []MCPInfo) {
	d.items = items
	if d.cursor >= len(items) {
		d.cursor = len(items) - 1
	}
	if d.cursor < 0 {
		d.cursor = 0
	}
}

func (d *mcpDialog) Active() bool { return d != nil && d.active }
func (d *mcpDialog) Close()       { d.active = false }

func (d *mcpDialog) current() (MCPInfo, bool) {
	if d.cursor < 0 || d.cursor >= len(d.items) {
		return MCPInfo{}, false
	}
	return d.items[d.cursor], true
}

// mcpAction is returned by HandleKey for the overlay host to apply. On
// carries the desired new enabled state for a toggle.
type mcpAction struct {
	ToggleGlobal  bool
	ToggleProject bool
	OpenLog       bool
	Close         bool
	Name          string
	Scope         string
	On            bool
}

// HandleKey advances the dialog and returns an action to apply, if any.
func (d *mcpDialog) HandleKey(k tui.Key) mcpAction {
	if !d.Active() {
		return mcpAction{}
	}
	switch k.Kind {
	case tui.KeyUp:
		if d.cursor > 0 {
			d.cursor--
		}
	case tui.KeyDown:
		if d.cursor < len(d.items)-1 {
			d.cursor++
		}
	case tui.KeyEsc:
		d.Close()
		return mcpAction{Close: true}
	case tui.KeyRune:
		it, ok := d.current()
		if !ok {
			return mcpAction{}
		}
		switch k.Rune {
		case 'g':
			// Toggle the user-config disable. UserDisabled==true means
			// currently off, so toggling enables it (On=true). The host
			// guards the project-defined case (no user-scope toggle).
			return mcpAction{ToggleGlobal: true, Name: it.Name, Scope: it.Scope, On: it.UserDisabled}
		case 'p':
			// Toggle this project's disable. ProjectDisabled==true means
			// currently off-here, so toggling enables it (On=true).
			return mcpAction{ToggleProject: true, Name: it.Name, Scope: it.Scope, On: it.ProjectDisabled}
		case 'l':
			// View the server's log (its startup/runtime stderr).
			if it.HasLog {
				return mcpAction{OpenLog: true, Name: it.Name}
			}
			d.status = it.Name + " has no log yet"
		}
	}
	return mcpAction{}
}

// mcpStateLabel summarizes why a server is on or off, in precedence order
// (the first reason that turns it off wins).
func mcpStateLabel(it MCPInfo) string {
	switch {
	case it.UserDisabled:
		return "off (user cfg)"
	case it.ProjectDisabled:
		return "off (project)"
	case it.ProjectGated:
		return "off (untrusted)"
	case it.StartupError != "" && !it.Connected:
		return "failed (see log)"
	case it.Effective && !it.Connected:
		return "off (not running)"
	default:
		return "on"
	}
}

// Render returns the dialog lines.
func (d *mcpDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var lines []string
	lines = append(lines, frameHeader(th, "mcp servers", width))

	if len(d.items) == 0 {
		lines = append(lines, th.FG256(th.Muted, "  no MCP servers configured — add them under \"mcp\" in config.json (docs/mcp.md)"))
		lines = append(lines, frameRule(th, width))
		return lines
	}

	lines = append(lines, th.FG256(th.Muted, "↑/↓ · g enable/disable (global) · p project on/off · l log · esc"))

	const maxRows = 12
	start, end := cursorWindow(d.cursor, len(d.items), maxRows)
	for i := start; i < end; i++ {
		it := d.items[i]
		tools := "-"
		if it.Connected {
			tools = fmt.Sprintf("%d tools", it.Tools)
		}
		plain := fmt.Sprintf("  %-8s %-22s %-9s %s",
			it.Scope, padRight(it.Name, 22), padRight(tools, 9), mcpStateLabel(it))
		if i == d.cursor {
			lines = append(lines, th.PadHighlight(plain, width))
		} else {
			lines = append(lines, th.FG256(th.Muted, plain))
		}
	}
	if start > 0 {
		lines = append(lines, windowMoreAbove(th, start))
	}
	if end < len(d.items) {
		lines = append(lines, windowMoreBelow(th, len(d.items), end))
	}

	if it, ok := d.current(); ok {
		if it.Description != "" {
			lines = append(lines, th.FG256(th.Muted, "  "+truncate(it.Description, width-4)))
		}
		// When the server is off, surface the startup error as the reason and
		// point at the full log ('l').
		if it.StartupError != "" {
			reason := it.StartupError
			if it.HasLog {
				reason += "  (l for log)"
			}
			lines = append(lines, th.FG256(th.Warning, "  "+truncate(reason, width-4)))
		}
	}
	if d.status != "" {
		lines = append(lines, th.FG256(th.Warning, "  "+d.status))
	}
	lines = append(lines, frameRule(th, width))
	return lines
}
