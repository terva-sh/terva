package dialogs

import (
	"fmt"
	"strings"

	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// SkillsDialog lists every discovered skill and lets the user view
// the body of one inline. View is read-only — the model loads skills
// itself via the `skill` tool. This dialog is for inspection.
type SkillsDialog struct {
	active  bool
	skills  []*skills.Skill
	cursor  int
	viewing *skills.Skill // when non-nil, render the body instead of the list
	vp      Viewport      // body view scroll state
	// MaxRows is the body height the host budgets from the terminal
	// (dialogs.BodyBudget). 0 falls back to the standalone constants.
	MaxRows int
}

const (
	skillsListFallbackRows = 12
	skillsBodyFallbackRows = 16
)

// ChromeRows is the non-body rows Render emits. The two views differ — the
// reading view spends a row on the scroll indicator the list does not — so this
// reports whichever is showing rather than one number for both.
func (d *SkillsDialog) ChromeRows() int {
	if d.viewing != nil {
		return 4
	}
	return 3
}

func NewSkillsDialog() *SkillsDialog { return &SkillsDialog{} }

// Open populates and shows the dialog with the given snapshot.
func (d *SkillsDialog) Open(s []*skills.Skill) {
	d.active = true
	d.skills = s
	d.cursor = 0
	d.viewing = nil
	d.vp.Reset()
}

// Close hides the dialog.
func (d *SkillsDialog) Close() { d.active = false }

// Active reports whether the dialog is visible.
func (d *SkillsDialog) Active() bool { return d != nil && d.active }

// InList reports whether the picker (not the body view) is showing, so
// the overlay binds the reload key only there.
func (d *SkillsDialog) InList() bool { return d.Active() && d.viewing == nil }

// HandleKey advances the dialog state.
func (d *SkillsDialog) HandleKey(k tui.Key) (closed bool) {
	if !d.Active() {
		return false
	}

	if d.viewing != nil {
		// Body view keys.
		switch k.Kind {
		case tui.KeyEsc, tui.KeyEnter:
			d.viewing = nil
			d.vp.Reset()
		default:
			d.vp.HandleKey(k)
		}
		return false
	}

	// List view keys.
	switch k.Kind {
	case tui.KeyEsc:
		d.Close()
		return true
	case tui.KeyUp:
		if d.cursor > 0 {
			d.cursor--
		}
	case tui.KeyDown:
		if d.cursor < len(d.skills)-1 {
			d.cursor++
		}
	case tui.KeyEnter:
		if len(d.skills) > 0 {
			d.viewing = d.skills[d.cursor]
			d.vp.Reset()
		}
	}
	return false
}

// Render draws the picker or the body view.
func (d *SkillsDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}

	if d.viewing != nil {
		return d.renderBody(th, width)
	}

	out := []string{FrameHeader(th, i18n.T("skills (enter to view, r to reload, esc to close)"), width)}
	if len(d.skills) == 0 {
		out = append(out, "  "+th.FG256(th.Muted, i18n.T("no user skills loaded")))
		out = append(out, "  "+th.FG256(th.Muted, i18n.T("add SKILL.md under $TERVA_HOME/skills, .terva/skills, .claude/skills, or .agents/skills")))
		out = append(out, FrameRule(th, width))
		return out
	}

	maxRows := d.MaxRows
	if maxRows <= 0 {
		maxRows = skillsListFallbackRows
	}
	start, end := visibleWindow(d.cursor, len(d.skills), maxRows)
	if start > 0 {
		out = append(out, WindowMoreAbove(th, start))
	}
	for i := start; i < end; i++ {
		s := d.skills[i]
		row := formatSkillRow(s, width-2)
		if i == d.cursor {
			out = append(out, th.PadHighlight("  "+row, width))
		} else {
			out = append(out, "  "+th.FG256(th.Muted, row))
		}
	}
	if end < len(d.skills) {
		out = append(out, WindowMoreBelow(th, len(d.skills), end))
	}
	out = append(out, FrameRule(th, width))
	return out
}

func (d *SkillsDialog) renderBody(th tui.Theme, width int) []string {
	s := d.viewing
	out := []string{
		FrameHeader(th, i18n.T("skill: %s  (esc / enter to go back)", s.Name), width),
		"  " + th.FG256(th.Muted, s.Description),
		"  " + th.FG256(th.Muted, i18n.T("source: %s  (%s)", s.Source, s.Path)),
		"",
	}

	rendered := tui.RenderMarkdown(s.Body, th, width-4)
	bodyLines := strings.Split(rendered, "\n")
	for i, l := range bodyLines {
		if len(l) > 0 && l[0] == tui.FlushLeftSentinel {
			bodyLines[i] = l[1:]
		}
	}

	maxRows := d.MaxRows
	if maxRows <= 0 {
		maxRows = skillsBodyFallbackRows
	}
	// Indent before windowing so the shared more-above/below markers keep their
	// own alignment rather than inheriting the body's.
	for i, line := range bodyLines {
		bodyLines[i] = "    " + line
	}
	d.vp.Fit(len(bodyLines), maxRows)
	out = append(out, d.vp.Rows(th, bodyLines)...)
	out = append(out, FrameRule(th, width))
	return out
}

func formatSkillRow(s *skills.Skill, maxWidth int) string {
	left := fmt.Sprintf("%-20s  ", truncateLineSafe(s.Name, 20))
	src := "  " + truncateLineSafe(s.Source, 16)
	room := maxWidth - len(left) - len(src)
	if room < 10 {
		room = 10
	}
	desc := s.Description
	if len(desc) > room {
		if room <= 3 {
			desc = strings.Repeat(".", room)
		} else {
			desc = desc[:room-3] + "..."
		}
	}
	return left + desc + src
}

// truncateLineSafe limits s to n runes (not bytes) so multibyte
// names + sources don't blow past the column.
func truncateLineSafe(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 3 {
		return strings.Repeat(".", n)
	}
	return string(r[:n-3]) + "..."
}

// visibleWindow centers cursor in a window of size n within total
// items. Returns [start, end) bounds.
func visibleWindow(cursor, total, n int) (start, end int) {
	if total <= n {
		return 0, total
	}
	start = cursor - n/2
	if start < 0 {
		start = 0
	}
	end = start + n
	if end > total {
		end = total
		start = end - n
	}
	return
}
