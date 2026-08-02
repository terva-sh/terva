package dialogs

import (
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// PermGrant is one revocable session "always allow" entry in the
// /permissions inspector. allowAll marks the blanket "yes, always"
// grant (tool is empty then); otherwise tool is the granted tool name.
type PermGrant struct {
	Tool     string
	AllowAll bool
}

// permissionsAction is what the dialog asks the parent to do after a
// keypress. At most one of the bools is set.
type permissionsAction struct {
	Revoke   bool      // revoke the single grant in Grant
	Grant    PermGrant // valid when Revoke is set
	ClearAll bool      // drop every session grant (gate.Reset)
	Close    bool
}

// PermissionsDialog is the /permissions inspector. It shows the active
// approval mode and the permission rules (read-only — rule editing
// stays in config, by design) and lets you revoke this session's
// "always allow" grants: the cursor moves over the grant list, r/del
// takes one back, R clears them all. info holds the pre-formatted,
// already-themed static lines (mode + rules + the "this session"
// header); grants is the selectable list, rebuilt from the live gate
// after every revoke.
type PermissionsDialog struct {
	active bool
	info   []string
	grants []PermGrant
	cursor int
	vp     Viewport
	// MaxRows is the body height the host budgets from the terminal
	// (dialogs.BodyBudget). 0 falls back to permissionsFallbackRows.
	MaxRows int
}

const permissionsFallbackRows = 18

// ChromeRows is the non-body rows Render emits: header, the more-below
// indicator and the closing rule.
func (d *PermissionsDialog) ChromeRows() int { return 3 }

func NewPermissionsDialog() *PermissionsDialog { return &PermissionsDialog{} }

// Open shows the dialog with static info lines and the selectable grant
// list.
func (d *PermissionsDialog) Open(info []string, grants []PermGrant) {
	d.active = true
	d.info = info
	d.grants = grants
	d.cursor = 0
	d.vp.Reset()
}

// Refresh swaps in freshly-derived content after a revoke while keeping
// the dialog open, clamping the cursor to the (now shorter) grant list.
func (d *PermissionsDialog) Refresh(info []string, grants []PermGrant) {
	d.info = info
	d.grants = grants
	if d.cursor >= len(d.grants) {
		d.cursor = len(d.grants) - 1
	}
	if d.cursor < 0 {
		d.cursor = 0
	}
}

func (d *PermissionsDialog) Close()       { d.active = false }
func (d *PermissionsDialog) Active() bool { return d != nil && d.active }

// HandleKey moves the cursor, scrolls the static area, or asks the
// parent to revoke a grant. esc/q close; r or del revoke the selected
// grant; R clears every session grant.
func (d *PermissionsDialog) HandleKey(k tui.Key) permissionsAction {
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
	case tui.KeyPageUp, tui.KeyPageDown, tui.KeyHome, tui.KeyEnd:
		// Paging moves the body under a cursor that stays put, which is what it
		// did before; the cursor is re-revealed at render if paging left it
		// off-screen. Home/End are new here and come free with the shared handler.
		d.vp.HandleKey(k)
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

func (d *PermissionsDialog) revokeCurrent() permissionsAction {
	if d.cursor < 0 || d.cursor >= len(d.grants) {
		return permissionsAction{}
	}
	return permissionsAction{Revoke: true, Grant: d.grants[d.cursor]}
}

func grantLabel(g PermGrant) string {
	if g.AllowAll {
		return `yes, always — every tool runs unprompted this session`
	}
	return g.Tool
}

// Render returns the dialog lines: the static info, then the selectable
// grant list with the cursor highlighted, scrolled so the cursor stays
// in view.
func (d *PermissionsDialog) Render(th tui.Theme, width int) []string {
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
	maxRows := d.MaxRows
	if maxRows <= 0 {
		maxRows = permissionsFallbackRows
	}
	d.vp.Fit(len(body), maxRows)
	d.vp.Reveal(grantStart + d.cursor)
	start, end := d.vp.Window()

	out := []string{FrameHeaderColor(th, title, width, th.Accent)}
	out = append(out, body[start:end]...)
	if end < len(body) {
		out = append(out, "  "+th.FG256(th.Muted, i18n.T("↓ %d more (down/pgdn)", len(body)-end)))
	}
	out = append(out, FrameRuleColor(th, width, th.Accent))
	return out
}
