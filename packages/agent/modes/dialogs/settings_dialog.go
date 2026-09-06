package dialogs

import (
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// SettingsDialog is a two-level pane: a category picker, then one category's
// items, then (for an enum) that item's options.
//
// It went two-level because the flat list outgrew the terminal. The daemon
// emits ~34 items and the TUI appends ~11 more, each with a wrapped
// description, so the body ran past 110 rows and a 65-row terminal opened on
// "37 more below". Each level now fits a screen: seven categories, then three
// to ten items.
//
// A single category means the picker would show one row and cost a keypress
// for nothing, so the dialog opens straight into the items and esc closes from
// there. That is also what a pre-groups daemon produces, where every item
// falls into the trailing bucket.
type SettingsDialog struct {
	active bool

	// groups is the display order after partitioning: every declared group
	// that has at least one item, then the trailing bucket if anything was
	// left over. member[g] holds the indices into items for groups[g], so a
	// toggle writes back through one flat slice and the wire order survives.
	groups []SettingsGroup
	items  []SettingsItem
	member [][]int

	// inGroup is level 2. single records that there is nothing to go back to.
	inGroup     bool
	single      bool
	groupCursor int
	cursor      int

	selecting    bool
	optionCursor int

	// MaxRows caps the rendered body (item rows + wrapped
	// descriptions); the window scrolls to keep the cursor item
	// visible. Set by the overlay registry from the live terminal
	// height — without the cap the full settings list is taller than
	// a 24-row terminal and the header clips off the top. 0 = no cap.
	MaxRows int
	vp      Viewport
	// optVP windows the option list. It had none, and the theme picker's
	// options already outrun a short terminal.
	optVP Viewport
}

// SettingGroup names one category. The daemon declares the order; see
// settingGroups in packages/agent/workspace/workspace_settings.go.
type SettingsGroup struct {
	ID    string
	Label string
	Desc  string
}

type SettingsItem struct {
	Key      string
	Label    string
	Desc     string
	Value    bool
	Options  []SettingsOption
	Choice   int
	Disabled bool
	Hint     string
	// Group is a SettingsGroup.ID. An item naming no group, or a group the
	// view does not declare, lands in the trailing bucket rather than
	// vanishing: a forgotten assignment has to be visible.
	Group string
}

type SettingsOption struct {
	Value string
	Label string
	Desc  string
}

type SettingsAction struct {
	Toggle bool
	Key    string
	Value  bool
	// Enum is true when this action came from an option picker (the chosen
	// value is in StringValue); false for a bool toggle (state in Value). It
	// disambiguates an enum selecting the empty value (e.g. reasoning "off")
	// from a false toggle, so a dispatcher never mistakes one for the other.
	Enum        bool
	StringValue string
	Close       bool
}

// ChromeRows is the non-body rows Render emits at their worst case: header,
// hint line, BOTH scroll indicators and the closing rule. A fixture showing only
// one indicator measures 4, which is exactly the trap this must not fall into.
func (d *SettingsDialog) ChromeRows() int { return 5 }

func NewSettingsDialog() *SettingsDialog { return &SettingsDialog{} }

func (d *SettingsDialog) Open(groups []SettingsGroup, items []SettingsItem) bool {
	if len(items) == 0 {
		return false
	}
	d.load(groups, items)
	d.groupCursor = 0
	d.cursor = 0
	d.inGroup = d.single
	d.selecting = false
	d.optionCursor = 0
	d.vp.Reset()
	d.optVP.Reset()
	d.active = true
	return true
}

// Reopen replaces the content in place and puts the cursor back on the same
// (group id, item key) it was on.
//
// Raw indices cannot do that job: flipping a parent adds conditional rows
// (auto_swarm on adds two), so the index that meant "swarm worktrees" before
// the toggle means something else after it. When the key is gone — the toggle
// removed the row the cursor sat on — the cursor clamps inside the group it
// was in.
func (d *SettingsDialog) Reopen(groups []SettingsGroup, items []SettingsItem) {
	if !d.Active() || len(items) == 0 {
		return
	}
	groupID, key := d.position()
	inGroup, selecting, optionCursor := d.inGroup, d.selecting, d.optionCursor
	d.load(groups, items)
	d.groupCursor = 0
	for gi, g := range d.groups {
		if g.ID == groupID {
			d.groupCursor = gi
			break
		}
	}
	d.cursor = 0
	for ci, idx := range d.member[d.groupCursor] {
		if d.items[idx].Key == key {
			d.cursor = ci
			break
		}
	}
	d.inGroup = inGroup || d.single
	// An option sub-view survives only while the item under it is still an
	// enum; anything else drops back to the item list rather than rendering a
	// picker over a checkbox.
	d.selecting = false
	if selecting {
		if it, ok := d.currentItem(); ok && len(it.Options) > 0 {
			d.selecting = true
			d.optionCursor = optionCursor
			if d.optionCursor >= len(it.Options) {
				d.optionCursor = it.Choice
			}
		}
	}
}

// load partitions items by group. Order comes from the declared groups; an
// undeclared or empty group id collects into one trailing bucket.
func (d *SettingsDialog) load(groups []SettingsGroup, items []SettingsItem) {
	d.items = items
	d.groups = nil
	d.member = nil
	byID := map[string][]int{}
	for i, it := range items {
		byID[it.Group] = append(byID[it.Group], i)
	}
	kept := map[string]bool{}
	for _, g := range groups {
		if kept[g.ID] || len(byID[g.ID]) == 0 {
			continue // a group with no items this session is not a category
		}
		kept[g.ID] = true
		d.groups = append(d.groups, g)
		d.member = append(d.member, byID[g.ID])
	}
	var orphans []int
	for i, it := range items {
		if !kept[it.Group] {
			orphans = append(orphans, i)
		}
	}
	if len(orphans) > 0 {
		d.groups = append(d.groups, SettingsGroup{Label: i18n.T("other settings")})
		d.member = append(d.member, orphans)
	}
	d.single = len(d.groups) == 1
}

func (d *SettingsDialog) Close() {
	d.active = false
	d.selecting = false
}
func (d *SettingsDialog) Active() bool { return d != nil && d.active }

// position is the (group id, item key) pair Reopen restores.
func (d *SettingsDialog) position() (groupID, key string) {
	if d.groupCursor >= 0 && d.groupCursor < len(d.groups) {
		groupID = d.groups[d.groupCursor].ID
	}
	if it, ok := d.currentItem(); ok {
		key = it.Key
	}
	return groupID, key
}

// currentIndex maps the group-local cursor onto the flat items slice.
func (d *SettingsDialog) currentIndex() (int, bool) {
	if d.groupCursor < 0 || d.groupCursor >= len(d.member) {
		return 0, false
	}
	idx := d.member[d.groupCursor]
	if d.cursor < 0 || d.cursor >= len(idx) {
		return 0, false
	}
	return idx[d.cursor], true
}

func (d *SettingsDialog) currentItem() (SettingsItem, bool) {
	i, ok := d.currentIndex()
	if !ok {
		return SettingsItem{}, false
	}
	return d.items[i], true
}

// groupItems is the current group's items in wire order.
func (d *SettingsDialog) groupItems() []SettingsItem {
	if d.groupCursor < 0 || d.groupCursor >= len(d.member) {
		return nil
	}
	idx := d.member[d.groupCursor]
	out := make([]SettingsItem, 0, len(idx))
	for _, i := range idx {
		out = append(out, d.items[i])
	}
	return out
}

func (d *SettingsDialog) HandleKey(k tui.Key) SettingsAction {
	if d.selecting {
		return d.handleOptionKey(k)
	}
	if !d.inGroup {
		return d.handleGroupKey(k)
	}
	// The cursor drives the viewport (Render reveals the cursor item's
	// block), so scroll intent maps onto cursor movement rather than routing
	// to vp.HandleKey — a free-scrolled offset would be yanked back to the
	// cursor on the next render anyway. Same shape as the worktree list view.
	n := len(d.member[d.groupCursor])
	switch k.Kind {
	case tui.KeyUp, tui.KeyMouseWheelUp:
		if d.cursor > 0 {
			d.cursor--
		}
	case tui.KeyDown, tui.KeyMouseWheelDown:
		if d.cursor < n-1 {
			d.cursor++
		}
	case tui.KeyHome:
		d.cursor = 0
	case tui.KeyEnd:
		if n > 0 {
			d.cursor = n - 1
		}
	case tui.KeyEsc:
		// Esc unwinds one level. With a single category there is no level to
		// unwind to, so it closes.
		if d.single {
			d.Close()
			return SettingsAction{Close: true}
		}
		d.inGroup = false
		d.vp.Reset()
	case tui.KeyEnter:
		return d.toggleCurrent()
	case tui.KeyRune:
		if k.Rune == ' ' {
			return d.toggleCurrent()
		}
	}
	return SettingsAction{}
}

func (d *SettingsDialog) handleGroupKey(k tui.Key) SettingsAction {
	switch k.Kind {
	case tui.KeyUp, tui.KeyMouseWheelUp:
		if d.groupCursor > 0 {
			d.groupCursor--
		}
	case tui.KeyDown, tui.KeyMouseWheelDown:
		if d.groupCursor < len(d.groups)-1 {
			d.groupCursor++
		}
	case tui.KeyHome:
		d.groupCursor = 0
	case tui.KeyEnd:
		if len(d.groups) > 0 {
			d.groupCursor = len(d.groups) - 1
		}
	case tui.KeyEsc:
		d.Close()
		return SettingsAction{Close: true}
	case tui.KeyEnter:
		d.descend()
	case tui.KeyRune:
		if k.Rune == ' ' {
			d.descend()
		}
	}
	return SettingsAction{}
}

func (d *SettingsDialog) descend() {
	if d.groupCursor < 0 || d.groupCursor >= len(d.groups) {
		return
	}
	d.inGroup = true
	d.cursor = 0
	d.vp.Reset()
}

func (d *SettingsDialog) handleOptionKey(k tui.Key) SettingsAction {
	it, ok := d.currentItem()
	if !ok {
		d.selecting = false
		return SettingsAction{}
	}
	switch k.Kind {
	case tui.KeyUp, tui.KeyMouseWheelUp:
		if d.optionCursor > 0 {
			d.optionCursor--
		}
	case tui.KeyDown, tui.KeyMouseWheelDown:
		if d.optionCursor < len(it.Options)-1 {
			d.optionCursor++
		}
	case tui.KeyHome:
		d.optionCursor = 0
	case tui.KeyEnd:
		if len(it.Options) > 0 {
			d.optionCursor = len(it.Options) - 1
		}
	case tui.KeyEsc:
		d.selecting = false
	case tui.KeyEnter:
		return d.selectCurrentOption()
	case tui.KeyRune:
		if k.Rune == ' ' {
			return d.selectCurrentOption()
		}
	}
	return SettingsAction{}
}

func (d *SettingsDialog) toggleCurrent() SettingsAction {
	flat, ok := d.currentIndex()
	if !ok {
		d.Close()
		return SettingsAction{Close: true}
	}
	it := d.items[flat]
	if it.Disabled {
		return SettingsAction{}
	}
	if len(it.Options) > 0 {
		d.optionCursor = it.Choice
		if d.optionCursor < 0 || d.optionCursor >= len(it.Options) {
			d.optionCursor = 0
		}
		d.selecting = true
		d.optVP.Reset()
		return SettingsAction{}
	}
	it.Value = !it.Value
	d.items[flat] = it
	return SettingsAction{Toggle: true, Key: it.Key, Value: it.Value}
}

func (d *SettingsDialog) selectCurrentOption() SettingsAction {
	flat, ok := d.currentIndex()
	if !ok {
		d.Close()
		return SettingsAction{Close: true}
	}
	it := d.items[flat]
	if len(it.Options) == 0 {
		d.selecting = false
		return SettingsAction{}
	}
	if d.optionCursor < 0 || d.optionCursor >= len(it.Options) {
		d.optionCursor = 0
	}
	it.Choice = d.optionCursor
	d.items[flat] = it
	d.selecting = false
	return SettingsAction{Toggle: true, Enum: true, Key: it.Key, StringValue: it.Options[it.Choice].Value}
}

func (d *SettingsDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	if d.selecting {
		return d.renderOptions(th, width)
	}
	if !d.inGroup {
		return d.renderGroups(th, width)
	}
	return d.renderItems(th, width)
}

// renderGroups is level 1: one row per category with its item count, and the
// category's one-line description beneath. No checkboxes — nothing here is
// togglable, enter descends.
func (d *SettingsDialog) renderGroups(th tui.Theme, width int) []string {
	var body []string
	cursorLine, cursorEnd := 0, 0
	for gi, g := range d.groups {
		if gi == d.groupCursor {
			cursorLine = len(body)
		}
		plain := truncate("  "+g.Label, width)
		count := "(" + strconv.Itoa(len(d.member[gi])) + ")"
		if runewidth.StringWidth(plain)+2+runewidth.StringWidth(count) <= width {
			plain += "  " + th.FG256(th.Muted, count)
		}
		if gi == d.groupCursor {
			body = append(body, th.PadHighlight(plain, width))
		} else {
			body = append(body, plain)
		}
		for _, desc := range wrapSettingDescription(g.Desc, width, 6) {
			body = append(body, th.FG256(th.Muted, desc))
		}
		if gi == d.groupCursor {
			cursorEnd = len(body)
		}
	}

	maxRows := d.MaxRows
	if maxRows <= 0 || maxRows > len(body) {
		maxRows = len(body)
	}
	d.vp.Fit(len(body), maxRows)
	if cursorEnd > 0 {
		d.vp.Reveal(cursorEnd - 1)
	}
	d.vp.Reveal(cursorLine)

	lines := []string{FrameHeader(th, i18n.T("settings"), width)}
	lines = append(lines, th.FG256(th.Muted, i18n.T("open with enter, esc to close:")))
	lines = append(lines, d.vp.Rows(th, body)...)
	lines = append(lines, FrameRule(th, width))
	return lines
}

// renderItems is level 2: one category's items.
//
// Only the focused item shows its whole description. The long ones
// (cache_aware_compaction, provider_compaction, transport_recording) earn
// their length when you are reading them and cost three rows each when you are
// not, so an unfocused row keeps the first wrapped line and an ellipsis. The
// wire still carries the full text; this is a rendering economy, not a second
// summary field to keep in step.
func (d *SettingsDialog) renderItems(th tui.Theme, width int) []string {
	items := d.groupItems()
	var body []string
	cursorLine, cursorEnd := 0, 0
	for i, it := range items {
		focused := i == d.cursor
		if focused {
			cursorLine = len(body)
		}
		box := "[ ]"
		if it.Value {
			box = "[✓]"
		}
		plain := "  " + box + " " + it.Label
		if len(it.Options) > 0 {
			box = "[→]"
			if it.Choice < 0 || it.Choice >= len(it.Options) {
				it.Choice = 0
			}
			plain = "  " + box + " " + it.Label + ": " + it.Options[it.Choice].Label
		}
		// The row is the one line that cannot wrap — it is the selectable
		// target, and the cursor highlight pads it to width. Clamp it here, on
		// the still-uncoloured text, so the frame is never overrun.
		plain = truncate(plain, width)
		// A hint rides inline only when the whole row still fits; otherwise it
		// moves to its own wrapped line, the same treatment Desc gets.
		// Appending it unconditionally is what pushed rows past the frame — the
		// approval row alone paints 106 cells and overflowed every terminal
		// narrower than that.
		var hintLines []string
		if it.Hint != "" {
			hint := "(" + it.Hint + ")"
			if runewidth.StringWidth(plain)+2+runewidth.StringWidth(hint) <= width {
				plain += "  " + th.FG256(th.Muted, hint)
			} else if focused {
				// An unfocused row drops the overflowing hint with its
				// description: both are detail for the row you are reading.
				hintLines = wrapSettingDescription(hint, width, 6)
			}
		}
		if it.Disabled {
			body = append(body, th.FG256(th.Muted, plain))
		} else if focused {
			body = append(body, th.PadHighlight(plain, width))
		} else {
			body = append(body, plain)
		}
		for _, hint := range hintLines {
			body = append(body, th.FG256(th.Muted, hint))
		}
		if it.Desc != "" {
			if focused {
				for _, desc := range wrapSettingDescription(it.Desc, width, 6) {
					body = append(body, th.FG256(th.Muted, desc))
				}
			} else {
				body = append(body, th.FG256(th.Muted, firstDescLine(it.Desc, width, 6)))
			}
		}
		if focused {
			cursorEnd = len(body)
		}
	}

	// Window the body so the dialog fits short terminals, keeping the
	// cursor item's first row in view.
	maxRows := d.MaxRows
	if maxRows <= 0 || maxRows > len(body) {
		maxRows = len(body)
	}
	d.vp.Fit(len(body), maxRows)
	// Reveal the cursor item's WHOLE block — row plus wrapped hint and
	// description lines — not just its first row. Revealing only the row left
	// the last item's description permanently below the window: the cursor
	// could go no further down, so "1 more below" could never be reached.
	// Bottom first, then top, so when a block is taller than the pane the
	// item row (the selectable line) wins the tie-break and stays visible.
	if cursorEnd > 0 {
		d.vp.Reveal(cursorEnd - 1)
	}
	d.vp.Reveal(cursorLine)

	title := i18n.T("settings")
	if d.groupCursor >= 0 && d.groupCursor < len(d.groups) && !d.single {
		title = i18n.T("settings: %s", d.groups[d.groupCursor].Label)
	}
	hint := i18n.T("change with enter/space, esc to go back:")
	if d.single {
		hint = i18n.T("change with enter/space, esc to close:")
	}
	lines := []string{FrameHeader(th, title, width)}
	lines = append(lines, th.FG256(th.Muted, hint))
	lines = append(lines, d.vp.Rows(th, body)...)
	lines = append(lines, FrameRule(th, width))
	return lines
}

func (d *SettingsDialog) renderOptions(th tui.Theme, width int) []string {
	it, ok := d.currentItem()
	if !ok {
		d.selecting = false
		return d.Render(th, width)
	}
	lines := []string{FrameHeader(th, i18n.T("settings: %s", it.Label), width)}
	var head []string
	if it.Desc != "" {
		// Wrapped, like the main view wraps the same string — emitting it raw
		// here let any description longer than the terminal overrun the frame.
		for _, desc := range wrapSettingDescription(it.Desc, width, 0) {
			head = append(head, th.FG256(th.Muted, desc))
		}
	}
	head = append(head, th.FG256(th.Muted, i18n.T("select with enter/space, esc to go back:")))
	lines = append(lines, head...)

	var body []string
	cursorLine, cursorEnd := 0, 0
	for idx, opt := range it.Options {
		if idx == d.optionCursor {
			cursorLine = len(body)
		}
		marker := "  "
		if idx == it.Choice {
			marker = "✓ "
		}
		plain := truncate("  "+marker+opt.Label, width)
		if idx == d.optionCursor {
			body = append(body, th.PadHighlight(plain, width))
		} else {
			body = append(body, plain)
		}
		if opt.Desc != "" {
			for _, desc := range wrapSettingDescription(opt.Desc, width, 6) {
				body = append(body, th.FG256(th.Muted, desc))
			}
		}
		if idx == d.optionCursor {
			cursorEnd = len(body)
		}
	}

	// The option list gets the same window the item list has. It had none, and
	// the theme picker already lists more options than a short terminal shows.
	// The budget is what the item list would have had, minus the description
	// this view prints above the options.
	maxRows := d.MaxRows - len(head) + 1 // +1: the hint row is chrome in both views
	if d.MaxRows <= 0 || maxRows > len(body) {
		maxRows = len(body)
	}
	if maxRows < 3 && len(body) >= 3 {
		maxRows = 3 // a one-row pane cannot be navigated
	}
	d.optVP.Fit(len(body), maxRows)
	if cursorEnd > 0 {
		d.optVP.Reveal(cursorEnd - 1)
	}
	d.optVP.Reveal(cursorLine)

	lines = append(lines, d.optVP.Rows(th, body)...)
	lines = append(lines, FrameRule(th, width))
	return lines
}

// firstDescLine is the unfocused form of a description: the first line of the
// same wrap the focused row uses, with an ellipsis when there is more behind
// it. Reusing the wrap keeps the two forms from drifting.
func firstDescLine(desc string, width, indent int) string {
	lines := wrapSettingDescription(desc, width, indent)
	switch len(lines) {
	case 0:
		return ""
	case 1:
		return lines[0]
	}
	return truncate(lines[0], width-1) + "…"
}

func wrapSettingDescription(desc string, width, indent int) []string {
	prefix := strings.Repeat(" ", indent)
	limit := width - indent
	if limit < 20 {
		limit = 20
	}
	words := strings.Fields(desc)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	line := words[0]
	for _, word := range words[1:] {
		candidate := line + " " + word
		if runewidth.StringWidth(candidate) <= limit {
			line = candidate
			continue
		}
		lines = append(lines, prefix+line)
		line = word
	}
	lines = append(lines, prefix+line)
	return lines
}
