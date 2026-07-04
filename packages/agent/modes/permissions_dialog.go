package modes

import (
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// permGrant is one revocable session "always allow" entry in the
// /permissions inspector. allowAll marks the blanket "yes, always"
// grant (tool is empty then); otherwise tool is the granted tool name.
type permGrant struct {
	tool     string
	allowAll bool
}

// permissionsAction is what the dialog asks the parent to do after a
// keypress. At most one of the bools is set.
type permissionsAction struct {
	Revoke   bool      // revoke the single grant in Grant
	Grant    permGrant // valid when Revoke is set
	ClearAll bool      // drop every session grant (gate.Reset)
	Close    bool
}

// permissionsDialog is the /permissions inspector. It shows the active
// approval mode and the permission rules (read-only — rule editing
// stays in config, by design) and lets you revoke this session's
// "always allow" grants: the cursor moves over the grant list, r/del
// takes one back, R clears them all. info holds the pre-formatted,
// already-themed static lines (mode + rules + the "this session"
// header); grants is the selectable list, rebuilt from the live gate
// after every revoke.
type permissionsDialog struct {
	active bool
	info   []string
	grants []permGrant
	cursor int
	scroll int
}

func newPermissionsDialog() *permissionsDialog { return &permissionsDialog{} }

// Open shows the dialog with static info lines and the selectable grant
// list.
func (d *permissionsDialog) Open(info []string, grants []permGrant) {
	d.active = true
	d.info = info
	d.grants = grants
	d.cursor = 0
	d.scroll = 0
}

// Refresh swaps in freshly-derived content after a revoke while keeping
// the dialog open, clamping the cursor to the (now shorter) grant list.
func (d *permissionsDialog) Refresh(info []string, grants []permGrant) {
	d.info = info
	d.grants = grants
	if d.cursor >= len(d.grants) {
		d.cursor = len(d.grants) - 1
	}
	if d.cursor < 0 {
		d.cursor = 0
	}
}

func (d *permissionsDialog) Close()       { d.active = false }
func (d *permissionsDialog) Active() bool { return d != nil && d.active }

// HandleKey moves the cursor, scrolls the static area, or asks the
// parent to revoke a grant. esc/q close; r or del revoke the selected
// grant; R clears every session grant.
func (d *permissionsDialog) HandleKey(k tui.Key) permissionsAction {
	if !d.Active() {
		return permissionsAction{}
	}
	switch k.Kind {
	case tui.KeyUp:
		if d.cursor > 0 {
			d.cursor--
		}
	case tui.KeyDown:
		if d.cursor < len(d.grants)-1 {
			d.cursor++
		}
	case tui.KeyPageUp:
		d.scroll -= 8
		if d.scroll < 0 {
			d.scroll = 0
		}
	case tui.KeyPageDown:
		d.scroll += 8
	case tui.KeyEsc:
		d.Close()
		return permissionsAction{Close: true}
	case tui.KeyDelete, tui.KeyBackspace:
		return d.revokeCurrent()
	case tui.KeyRune:
		switch k.Rune {
		case 'r':
			return d.revokeCurrent()
		case 'R':
			if len(d.grants) > 0 {
				return permissionsAction{ClearAll: true}
			}
		case 'q':
			d.Close()
			return permissionsAction{Close: true}
		}
	}
	return permissionsAction{}
}

func (d *permissionsDialog) revokeCurrent() permissionsAction {
	if d.cursor < 0 || d.cursor >= len(d.grants) {
		return permissionsAction{}
	}
	return permissionsAction{Revoke: true, Grant: d.grants[d.cursor]}
}

func grantLabel(g permGrant) string {
	if g.allowAll {
		return `yes, always — every tool runs unprompted this session`
	}
	return g.tool
}

// Render returns the dialog lines: the static info, then the selectable
// grant list with the cursor highlighted, scrolled so the cursor stays
// in view.
func (d *permissionsDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}

	// Compose the body, baking the 2-space indent into each line so the
	// selection highlight can span the full row.
	var body []string
	for _, line := range d.info {
		body = append(body, "  "+line)
	}
	grantStart := len(body)
	if len(d.grants) > 0 {
		body = append(body, "  "+th.FG256(th.Muted, i18n.T("↑/↓ select · r/del revoke · R clear all")))
		grantStart = len(body)
		for i, g := range d.grants {
			text := "  " + grantLabel(g)
			if i == d.cursor {
				body = append(body, th.PadHighlight(text, width))
			} else {
				body = append(body, th.FG256(th.Muted, text))
			}
		}
	}

	title := "permissions (esc to close)"
	if len(d.grants) > 0 {
		title = i18n.T("permissions (r/del revoke · R clear all · esc close)")
	}

	// Keep the cursor's line inside the scroll window.
	const maxRows = 18
	cursorLine := grantStart + d.cursor
	if cursorLine >= d.scroll+maxRows {
		d.scroll = cursorLine - maxRows + 1
	}
	if cursorLine < d.scroll {
		d.scroll = cursorLine
	}
	if d.scroll > len(body)-1 {
		d.scroll = len(body) - 1
	}
	if d.scroll < 0 {
		d.scroll = 0
	}
	end := d.scroll + maxRows
	if end > len(body) {
		end = len(body)
	}

	out := []string{frameHeaderColor(th, title, width, th.Accent)}
	out = append(out, body[d.scroll:end]...)
	if end < len(body) {
		out = append(out, "  "+th.FG256(th.Muted, i18n.T("↓ %d more (down/pgdn)", len(body)-end)))
	}
	out = append(out, frameRuleColor(th, width, th.Accent))
	return out
}
