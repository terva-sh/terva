package dialogs

import (
	"fmt"

	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// MCPDialog lists configured MCP servers and toggles them on/off globally
// (user config) or per-project (project config). Opened with /mcp.
type MCPDialog struct {
	active bool
	items  []MCPInfo
	cursor int
	vp     Viewport
	// MaxRows is the body height the host budgets from the terminal
	// (dialogs.BodyBudget). 0 falls back to mcpFallbackRows.
	MaxRows int
	status  string
}

func NewMCPDialog() *MCPDialog { return &MCPDialog{} }

// Open shows the dialog over the given configured-server list.
func (d *MCPDialog) Open(items []MCPInfo) {
	d.active = true
	d.items = items
	d.cursor = 0
	d.status = ""
}

// SetItems refreshes the list in place (after a toggle + reload), keeping
// the cursor on the same row when possible.
func (d *MCPDialog) SetItems(items []MCPInfo) {
	d.items = items
	if d.cursor >= len(items) {
		d.cursor = len(items) - 1
	}
	if d.cursor < 0 {
		d.cursor = 0
	}
}

func (d *MCPDialog) Active() bool { return d != nil && d.active }
func (d *MCPDialog) Close()       { d.active = false }

func (d *MCPDialog) current() (MCPInfo, bool) {
	if d.cursor < 0 || d.cursor >= len(d.items) {
		return MCPInfo{}, false
	}
	return d.items[d.cursor], true
}

// MCPAction is returned by HandleKey for the overlay host to apply. On
// carries the desired new enabled state for a toggle.
type MCPAction struct {
	ToggleGlobal  bool
	ToggleProject bool
	OpenLog       bool
	Close         bool
	Name          string
	Scope         string
	On            bool
}

// HandleKey advances the dialog and returns an action to apply, if any.
func (d *MCPDialog) HandleKey(k tui.Key) MCPAction {
	if !d.Active() {
		return MCPAction{}
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
		return MCPAction{Close: true}
	case tui.KeyRune:
		it, ok := d.current()
		if !ok {
			return MCPAction{}
		}
		switch k.Rune {
		case 'g':
			// Toggle the user-config disable. UserDisabled==true means
			// currently off, so toggling enables it (On=true). The host
			// guards the project-defined case (no user-scope toggle).
			return MCPAction{ToggleGlobal: true, Name: it.Name, Scope: it.Scope, On: it.UserDisabled}
		case 'p':
			// Toggle this project's disable. ProjectDisabled==true means
			// currently off-here, so toggling enables it (On=true).
			return MCPAction{ToggleProject: true, Name: it.Name, Scope: it.Scope, On: it.ProjectDisabled}
		case 'l':
			// View the server's log (its startup/runtime stderr).
			if it.HasLog {
				return MCPAction{OpenLog: true, Name: it.Name}
			}
			d.status = it.Name + " has no log yet"
		}
	}
	return MCPAction{}
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
const mcpFallbackRows = 12

// ChromeRows is the non-body rows Render emits at their worst case.
// Verified by TestEveryDialogFitsItsOwnBudget rather than counted by eye.
func (d *MCPDialog) ChromeRows() int { return 5 }

func (d *MCPDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var lines []string
	lines = append(lines, FrameHeader(th, i18n.T("mcp servers"), width))

	if len(d.items) == 0 {
		lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("no MCP servers configured — add them under \"mcp\" in config.json (docs/mcp.md)")))
		lines = append(lines, FrameRule(th, width))
		return lines
	}

	lines = append(lines, th.FG256(th.Muted, i18n.T("↑/↓ · g enable/disable (global) · p project on/off · l log · esc")))

	maxRows := d.MaxRows
	if maxRows <= 0 {
		maxRows = mcpFallbackRows
	}
	// Centred: this list is filtered and rebuilt under the cursor, so
	// holding the cursor still and moving the content reads better than
	// scrolling only at the edges. Named here rather than implied by
	// whichever windowing helper was reached for.
	d.vp.Fit(len(d.items), maxRows)
	d.vp.Center(d.cursor)
	start, end := d.vp.Window()
	for i := start; i < end; i++ {
		it := d.items[i]
		tools := "-"
		if it.Connected {
			tools = i18n.T("%d tools", it.Tools)
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
		lines = append(lines, WindowMoreAbove(th, start))
	}
	if end < len(d.items) {
		lines = append(lines, WindowMoreBelow(th, len(d.items), end))
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
	lines = append(lines, FrameRule(th, width))
	return lines
}

// MCPInfo is mcp.Info. See ExtInfo.
type MCPInfo = mcp.Info
