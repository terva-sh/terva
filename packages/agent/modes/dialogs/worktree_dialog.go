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
	scroll   int

	listFn    func() *worktree.ListResult
	collectFn func() *worktree.CollectResult

	// MaxRows caps the body height; the overlay sets it from the terminal size
	// each frame so a long list stays inside the bottom band. 0 = unlimited.
	MaxRows int
}

func NewWorktreeDialog() *WorktreeDialog { return &WorktreeDialog{} }

func (d *WorktreeDialog) Active() bool { return d != nil && d.active }

// Open shows the panel over the live caches, resetting transient view state
// (selection, scroll) so each open starts clean. collect opens straight into
// the merge-back overview (`/worktree collect`, the extension-era spelling).
func (d *WorktreeDialog) Open(listFn func() *worktree.ListResult, collectFn func() *worktree.CollectResult, collect bool) {
	d.active = true
	d.collect = collect
	d.selected = 0
	d.scroll = 0
	d.listFn = listFn
	d.collectFn = collectFn
}

func (d *WorktreeDialog) Close() {
	d.active = false
	d.listFn = nil
	d.collectFn = nil
	d.selected = 0
	d.scroll = 0
}

// WorktreeAction reports what the host should do after a key: close the panel,
// refresh the cache (r), or cd the session into a worktree (↵ on a row).
type WorktreeAction struct {
	Close   bool
	Refresh bool
	CdPath  string
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
		switch k.Rune {
		case 'c', 'C':
			d.collect = true
			d.scroll = 0
		case 'l', 'L':
			d.collect = false
			d.scroll = 0
		case 'r', 'R':
			return WorktreeAction{Refresh: true}
		}
	case tui.KeyUp, tui.KeyMouseWheelUp:
		if d.collect {
			if d.scroll > 0 {
				d.scroll--
			}
		} else if d.selected > 0 {
			d.selected--
		}
	case tui.KeyDown, tui.KeyMouseWheelDown:
		if d.collect {
			d.scroll++
		} else if n := d.rows(); d.selected < n-1 {
			d.selected++
		}
	case tui.KeyPageUp:
		if d.scroll -= 5; d.scroll < 0 {
			d.scroll = 0
		}
	case tui.KeyPageDown:
		d.scroll += 5
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
	}

	out := []string{FrameHeader(th, title, width)}
	scrollable := d.MaxRows > 0 && len(body) > d.MaxRows
	visible := body
	if scrollable {
		if d.collect {
			if d.scroll > len(body)-d.MaxRows {
				d.scroll = len(body) - d.MaxRows
			}
			if d.scroll < 0 {
				d.scroll = 0
			}
		} else {
			// List view: PanelLines is one row per worktree, so the selection maps
			// 1:1 to a body line. Keep the ▶ cursor inside the window (mirroring
			// SessionDialog) so ↓/↑ scroll the list instead of driving the cursor
			// off-screen and cd-ing into an invisible worktree on ↵.
			d.scroll = clampViewTop(d.scroll, d.selected, d.MaxRows, len(body))
		}
		visible = body[d.scroll : d.scroll+d.MaxRows]
	} else if d.collect {
		d.scroll = 0
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
