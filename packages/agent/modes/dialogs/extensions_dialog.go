package dialogs

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"

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
	vp     Viewport
	// MaxRows is the body height the host budgets from the terminal
	// (dialogs.BodyBudget). 0 falls back to extensionsFallbackRows.
	MaxRows int
	status  string
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
			d.status = i18n.T("%s has no configurable settings", it.Name)
		case 'l':
			// View the extension's log (the crash reason lives there).
			if it.HasLog {
				return ExtensionsAction{OpenLog: true, Name: it.Name}
			}
			d.status = i18n.T("%s has no log yet", it.Name)
		}
	}
	return ExtensionsAction{}
}

// stateLabel summarizes why an extension is on or off, in precedence
// order (the first reason that turns it off wins).
func stateLabel(it ExtInfo) string {
	switch {
	case !it.GlobalEnabled:
		return i18n.T("off (disabled)")
	case it.UserConfigDisabled:
		return i18n.T("off (user cfg)")
	case it.ProjectDisabled:
		return i18n.T("off (project)")
	case it.ProjectGated:
		return i18n.T("off (untrusted)")
	case !it.Running:
		// Should be loaded but isn't — failed spawn / crash. Worth
		// flagging distinctly so the agent/user knows to check logs.
		return i18n.T("off (not running)")
	default:
		// TC, not T: a bare "on" is the one key in this column ambiguous
		// enough to need its sense recorded for a translator.
		return i18n.TC("service state", "on")
	}
}

// Render returns the dialog lines.
const extensionsFallbackRows = 12

// ChromeRows is the non-body rows Render emits at their worst case.
// Verified by TestEveryDialogFitsItsOwnBudget rather than counted by eye.
func (d *ExtensionsDialog) ChromeRows() int { return 5 }

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

	maxRows := d.MaxRows
	if maxRows <= 0 {
		maxRows = extensionsFallbackRows
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
			lines = append(lines, th.FG256(th.Warning, "  "+truncate(i18n.T("%s  (l for log)", it.LastLog), width-4)))
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

// truncate shortens s to at most max painted cells, marking the cut with an
// ellipsis. Both the measurement and the cut are in runes-and-cells, not
// bytes: a byte-length comparison overstates the width of anything non-ASCII
// (an em dash bills 3 bytes for 1 cell) and a byte-index cut lands inside a
// multi-byte rune, emitting invalid UTF-8 that the terminal paints as a
// replacement glyph. s must carry no ANSI escapes — every caller in this
// package truncates before colouring.
func truncate(s string, max int) string {
	if max <= 1 || runewidth.StringWidth(s) <= max {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if used+w > max-1 { // reserve the last cell for the ellipsis
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
}

// ExtInfo is extensions.Info. The type moved down so the ctrlproto server and
// the build layer can name it without importing the TUI; the alias keeps every
// modes caller unchanged.
type ExtInfo = extensions.Info
