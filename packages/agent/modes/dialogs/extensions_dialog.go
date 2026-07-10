package dialogs

import (
	"fmt"
	"strings"

	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// ExtensionsDialog lists installed extensions and toggles them on/off
// globally (manifest) or per-project (project config). Opened with
// /extensions (alias /ext).
type ExtensionsDialog struct {
	active bool
	items  []ExtInfo
	cursor int
	status string
}

func NewExtensionsDialog() *ExtensionsDialog { return &ExtensionsDialog{} }

// Open shows the dialog over the given installed-extension list.
func (d *ExtensionsDialog) Open(items []ExtInfo) {
	d.active = true
	d.items = items
	d.cursor = 0
	d.status = ""
}

// SetItems refreshes the list in place (after a toggle + reload),
// keeping the cursor on the same row when possible.
func (d *ExtensionsDialog) SetItems(items []ExtInfo) {
	d.items = items
	if d.cursor >= len(items) {
		d.cursor = len(items) - 1
	}
	if d.cursor < 0 {
		d.cursor = 0
	}
}

func (d *ExtensionsDialog) Active() bool { return d != nil && d.active }
func (d *ExtensionsDialog) Close()       { d.active = false }

func (d *ExtensionsDialog) current() (ExtInfo, bool) {
	if d.cursor < 0 || d.cursor >= len(d.items) {
		return ExtInfo{}, false
	}
	return d.items[d.cursor], true
}

// ExtensionsAction is returned by HandleKey for the overlay host to
// apply. On carries the desired new enabled state for a toggle.
type ExtensionsAction struct {
	ToggleGlobal  bool
	ToggleProject bool
	OpenConfig    bool
	OpenLog       bool
	Close         bool
	Name          string
	Scope         string
	On            bool
}

// HandleKey advances the dialog and returns an action to apply, if any.
func (d *ExtensionsDialog) HandleKey(k tui.Key) ExtensionsAction {
	if !d.Active() {
		return ExtensionsAction{}
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
		return ExtensionsAction{Close: true}
	case tui.KeyRune:
		it, ok := d.current()
		if !ok {
			return ExtensionsAction{}
		}
		switch k.Rune {
		case 'g':
			// Flip the manifest enabled flag.
			return ExtensionsAction{ToggleGlobal: true, Name: it.Name, Scope: it.Scope, On: !it.GlobalEnabled}
		case 'p':
			// Flip this project's disable. ProjectDisabled==true means
			// currently off-here, so toggling enables it (On=true).
			return ExtensionsAction{ToggleProject: true, Name: it.Name, Scope: it.Scope, On: it.ProjectDisabled}
		case 'c':
			// Open the per-extension config form, if it declares a schema.
			if it.HasConfig {
				return ExtensionsAction{OpenConfig: true, Name: it.Name}
			}
			d.status = it.Name + " has no configurable settings"
		case 'l':
			// View the extension's log (the crash reason lives there).
			if it.HasLog {
				return ExtensionsAction{OpenLog: true, Name: it.Name}
			}
			d.status = it.Name + " has no log yet"
		}
	}
	return ExtensionsAction{}
}

// stateLabel summarizes why an extension is on or off, in precedence
// order (the first reason that turns it off wins).
func stateLabel(it ExtInfo) string {
	switch {
	case !it.GlobalEnabled:
		return "off (disabled)"
	case it.UserConfigDisabled:
		return "off (user cfg)"
	case it.ProjectDisabled:
		return "off (project)"
	case it.ProjectGated:
		return "off (untrusted)"
	case !it.Running:
		// Should be loaded but isn't — failed spawn / crash. Worth
		// flagging distinctly so the agent/user knows to check logs.
		return "off (not running)"
	default:
		return "on"
	}
}

// Render returns the dialog lines.
func (d *ExtensionsDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var lines []string
	lines = append(lines, FrameHeader(th, i18n.T("extensions"), width))

	if len(d.items) == 0 {
		lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("no extensions installed — `terva ext install <path|url>`")))
		lines = append(lines, FrameRule(th, width))
		return lines
	}

	lines = append(lines, th.FG256(th.Muted, i18n.T("↑/↓ · g enable/disable (global) · p project on/off · c config · l log · esc")))

	const maxRows = 12
	start, end := CursorWindow(d.cursor, len(d.items), maxRows)
	for i := start; i < end; i++ {
		it := d.items[i]
		plain := fmt.Sprintf("  %-8s %-22s %-8s %s",
			it.Scope, padRight(it.Name, 22), padRight(verLang(it), 8), stateLabel(it))
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
		var parts []string
		if it.Language != "" {
			parts = append(parts, it.Language)
		}
		if it.Running {
			parts = append(parts, i18n.T("%d cmds · %d tools", it.Commands, it.Tools))
		}
		detail := strings.Join(parts, " · ")
		if it.Description != "" {
			if detail != "" {
				detail += " — "
			}
			detail += it.Description
		}
		if detail != "" {
			lines = append(lines, th.FG256(th.Muted, "  "+truncate(detail, width-4)))
		}
		// When the extension is off, surface the last log line as the reason,
		// and point at the full log ('l').
		if it.LastLog != "" {
			lines = append(lines, th.FG256(th.Warning, "  "+truncate(it.LastLog+"  (l for log)", width-4)))
		}
	}
	if d.status != "" {
		lines = append(lines, th.FG256(th.Warning, "  "+d.status))
	}
	lines = append(lines, FrameRule(th, width))
	return lines
}

func verLang(it ExtInfo) string {
	v := it.Version
	if v == "" {
		v = "-"
	}
	return v
}

func truncate(s string, max int) string {
	if max <= 1 || len(s) <= max {
		return s
	}
	if max < 2 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

// ExtInfo is extensions.Info. The type moved down so the ctrlproto server and
// the build layer can name it without importing the TUI; the alias keeps every
// modes caller unchanged.
type ExtInfo = extensions.Info
