package dialogs

import (
	"strings"

	"terva.sh/terva/packages/agent/worktree"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// WorktreeDialog is the /worktree panel over the built-in worktree engine: the
// list view (selection, ↵ cd) and the collect view (the merge-back overview),
// the two modes the retired terva-git-worktree extension's panel had. It holds
// no worktree data of its own: listFn/collectFn pull the TUI's surface cache
// every render, and the layout comes from the shared renderers in
// packages/agent/worktree (one renderer, no drift — the TasksDialog pattern).
type WorktreeDialog struct {
	active   bool
	collect  bool // false = list view, true = merge-back overview
	selected int
	vp       Viewport

	listFn    func() *worktree.ListResult
	collectFn func() *worktree.CollectResult

	// armRemove / armRemoveAll are the first press of x / X: the prompt is up
	// and the SECOND press commits. Disarmed by any other key, by moving the
	// selection, and by a refusal — a prompt that outlives the row it was
	// raised on would delete the wrong worktree.
	armRemove    bool
	armRemoveAll bool
	// refuse is why the last attempt was declined (claimed, unmanaged, nothing
	// available), shown in place of the prompt. Cleared by the next key.
	refuse string

	// MaxRows caps the body height; the overlay sets it from the terminal size
	// each frame so a long list stays inside the bottom band. 0 = unlimited.
	MaxRows int
}

// current is the selected row, or nil when the list is empty or out of range.
func (d *WorktreeDialog) current() *worktree.ListItem {
	if d.listFn == nil {
		return nil
	}
	res := d.listFn()
	if res == nil || d.selected < 0 || d.selected >= len(res.Worktrees) {
		return nil
	}
	return res.Worktrees[d.selected]
}

// availableCount is how many rows X would remove.
func (d *WorktreeDialog) availableCount() int {
	if d.listFn == nil {
		return 0
	}
	res := d.listFn()
	if res == nil {
		return 0
	}
	n := 0
	for _, it := range res.Worktrees {
		if ok, _ := removable(it); ok {
			n++
		}
	}
	return n
}

// ChromeRows is the non-body rows Render emits: header, the blank, the footer
// and the closing rule.
func (d *WorktreeDialog) ChromeRows() int { return 4 }

func NewWorktreeDialog() *WorktreeDialog { return &WorktreeDialog{} }

func (d *WorktreeDialog) Active() bool { return d != nil && d.active }

// Open shows the panel over the live caches, resetting transient view state
// (selection, scroll) so each open starts clean. collect opens straight into
// the merge-back overview (`/worktree collect`, the extension-era spelling).
func (d *WorktreeDialog) Open(listFn func() *worktree.ListResult, collectFn func() *worktree.CollectResult, collect bool) {
	d.active = true
	d.collect = collect
	d.selected = 0
	d.armRemove, d.armRemoveAll, d.refuse = false, false, ""
	d.vp.Reset()
	d.listFn = listFn
	d.collectFn = collectFn
}

func (d *WorktreeDialog) Close() {
	d.active = false
	d.listFn = nil
	d.collectFn = nil
	d.selected = 0
	d.armRemove, d.armRemoveAll, d.refuse = false, false, ""
	d.vp.Reset()
}

// WorktreeAction reports what the host should do after a key: close the panel,
// refresh the cache (r), cd the session into a worktree (↵ on a row), remove
// one (x), or remove every available one at once (X).
type WorktreeAction struct {
	Close   bool
	Refresh bool
	CdPath  string

	// RemoveName is a single managed worktree to remove; RemoveAvailable asks
	// for every available one in the repo. Both are the ARMED second press —
	// the first press only sets the prompt, so a stray keystroke on a list of
	// checkouts cannot delete one.
	RemoveName      string
	RemoveAvailable bool
}

// removable reports whether a row may be removed from here, and why not when
// it may not.
//
// "available" is the engine's own word and it already covers the stale case: a
// claim whose owning process is gone or whose TTL expired is reported available
// with a StaleReason, never silently reclaimed. So this needs no notion of
// staleness of its own — asking for available IS asking for "unclaimed, stale
// ones included".
//
// A LIVE claim is refused. Deleting the checkout out from under a running
// sub-agent is not a thing to offer behind one keypress, and the claim is the
// only evidence terva has that someone is in there.
func removable(it *worktree.ListItem) (bool, string) {
	switch {
	case it == nil:
		return false, ""
	case it.Unmanaged:
		return false, i18n.T("that worktree is not managed by terva — remove it with git")
	case it.Status != "available":
		owner := ""
		if it.ClaimedBy != nil {
			owner = *it.ClaimedBy
		}
		if owner == "" {
			owner = i18n.T("another session")
		}
		return false, i18n.T("%s is claimed by %s — it is still in use", it.Name, owner)
	}
	return true, ""
}

func (d *WorktreeDialog) rows() int {
	if d.listFn == nil {
		return 0
	}
	if res := d.listFn(); res != nil {
		return len(res.Worktrees)
	}
	return 0
}

func (d *WorktreeDialog) HandleKey(k tui.Key) WorktreeAction {
	switch k.Kind {
	case tui.KeyEsc:
		return WorktreeAction{Close: true}
	case tui.KeyEnter:
		if !d.collect && d.listFn != nil {
			if res := d.listFn(); res != nil && d.selected >= 0 && d.selected < len(res.Worktrees) {
				return WorktreeAction{CdPath: res.Worktrees[d.selected].Path}
			}
		}
	case tui.KeyRune:
		// Any key that is not the arming key disarms, so a prompt never
		// outlives the keystroke that raised it.
		armedOne, armedAll := d.armRemove, d.armRemoveAll
		if k.Rune != 'x' && k.Rune != 'X' {
			d.armRemove, d.armRemoveAll = false, false
		}
		switch k.Rune {
		case 'c', 'C':
			d.collect = true
			d.vp.Reset()
		case 'l', 'L':
			d.collect = false
			d.vp.Reset()
		case 'r', 'R':
			return WorktreeAction{Refresh: true}
		case 'x':
			if d.collect {
				break
			}
			it := d.current()
			ok, why := removable(it)
			if !ok {
				d.armRemove, d.armRemoveAll = false, false
				d.refuse = why
				break
			}
			d.refuse = ""
			if armedOne {
				d.armRemove = false
				return WorktreeAction{RemoveName: it.Name}
			}
			d.armRemove, d.armRemoveAll = true, false
		case 'X':
			if d.collect {
				break
			}
			if d.availableCount() == 0 {
				d.armRemove, d.armRemoveAll = false, false
				d.refuse = i18n.T("nothing to remove — no worktree here is available")
				break
			}
			d.refuse = ""
			if armedAll {
				d.armRemoveAll = false
				return WorktreeAction{RemoveAvailable: true}
			}
			d.armRemoveAll, d.armRemove = true, false
		}
	case tui.KeyUp, tui.KeyMouseWheelUp:
		if d.collect {
			d.vp.HandleKey(k)
		} else if d.selected > 0 {
			d.selected--
			d.armRemove, d.armRemoveAll, d.refuse = false, false, ""
		}
	case tui.KeyDown, tui.KeyMouseWheelDown:
		if d.collect {
			d.vp.HandleKey(k)
		} else if n := d.rows(); d.selected < n-1 {
			d.selected++
			d.armRemove, d.armRemoveAll, d.refuse = false, false, ""
		}
	default:
		// The list view's offset follows the selection (clampViewTop, with its
		// two-row padding); only the collect body scrolls on its own.
		if d.collect {
			d.vp.HandleKey(k)
		}
	}
	return WorktreeAction{}
}

func (d *WorktreeDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var title, footer string
	var body []string
	if d.collect {
		var res *worktree.CollectResult
		if d.collectFn != nil {
			res = d.collectFn()
		}
		title = worktree.CollectTitle(res)
		body = worktree.CollectLines(res)
		footer = worktree.CollectFooter()
	} else {
		var res *worktree.ListResult
		if d.listFn != nil {
			res = d.listFn()
		}
		n := 0
		if res != nil {
			n = len(res.Worktrees)
		}
		if d.selected >= n {
			d.selected = n - 1
		}
		if d.selected < 0 {
			d.selected = 0
		}
		title = worktree.PanelTitle(res)
		body = worktree.PanelLines(res, d.selected)
		footer = worktree.PanelFooter()
		// The prompt REPLACES the key hints rather than joining them: an armed
		// delete is the only thing worth reading on that row, and a footer that
		// still lists "↵ cd · r refresh" beside it reads as one more hint.
		switch {
		case d.refuse != "":
			footer = d.refuse
		case d.armRemove:
			name := ""
			if it := d.current(); it != nil {
				name = it.Name
			}
			footer = i18n.T("remove %s? x again to confirm, any other key cancels", name)
		case d.armRemoveAll:
			footer = i18n.T("remove all %d available worktrees? X again to confirm, any other key cancels", d.availableCount())
		}
	}

	out := []string{FrameHeader(th, title, width)}
	d.vp.Fit(len(body), d.MaxRows)
	scrollable := d.MaxRows > 0 && d.vp.Scrollable()
	visible := body
	if scrollable {
		if d.collect {
			// Clamped by the viewport, in one place, for every dialog.
		} else {
			// List view: PanelLines is one row per worktree, so the selection maps
			// 1:1 to a body line. Keep the ▶ cursor inside the window (mirroring
			// SessionDialog) so ↓/↑ scroll the list instead of driving the cursor
			// off-screen and cd-ing into an invisible worktree on ↵.
			d.vp.RevealPadded(d.selected, cursorPadRows)
		}
		start, end := d.vp.Window()
		visible = body[start:end]
	} else if d.collect {
		d.vp.Reset()
	}
	for _, l := range visible {
		out = append(out, colorWorktreeLine(th, l))
	}
	// Only the collect view scrolls with the arrows; the list view's footer already
	// says "↑/↓ select" (which now also scrolls to follow), so labelling it "scroll"
	// there would misdescribe the keys.
	if scrollable && d.collect {
		footer = i18n.T("↑/↓ scroll · %s", footer)
	}
	out = append(out, "", th.FG256(th.Muted, "  "+footer))
	out = append(out, FrameRule(th, width))
	return out
}

// colorWorktreeLine themes one renderer row: the selection cursor row reads in
// the accent colour, dirty rows warn, everything else stays plain — the colour
// lives with the frontend, the layout stays with the engine renderer
// (packages/agent/worktree/render.go's "▶ " cursor and "✱dirty" marker).
func colorWorktreeLine(th tui.Theme, line string) string {
	switch {
	case strings.HasPrefix(line, "▶ "):
		return th.FG256(th.Accent, line)
	case strings.Contains(line, "✱dirty"):
		return th.FG256(th.Warning, line)
	default:
		return th.FG256(th.FG, line)
	}
}
