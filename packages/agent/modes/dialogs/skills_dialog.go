package dialogs

import (
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
//
// The reading view's optional metadata lines (argument hint, shadowed-by note)
// have to be counted here too: undercounting hands the viewport a body budget
// the dialog then overruns, and an oversized dialog squeezes the transcript.
func (d *SkillsDialog) ChromeRows() int {
	if d.viewing == nil {
		return 3
	}
	n := 4
	if d.viewing.ArgumentHint != "" {
		n++
	}
	if d.viewing.ShadowedBy != nil {
		n++
	}
	return n
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
		// Built-ins are listed now, so an empty picker means nothing loaded
		// at all — --no-skill, or --no-builtin-skills with no skills of your
		// own. "no user skills" would send someone looking in the wrong place.
		out = append(out, "  "+th.FG256(th.Muted, i18n.T("no skills loaded")))
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

// shadowSourceLabel names what beat a skill, in the terms a user can act on:
// the winner's namespace, which is also the prefix that would reach IT.
func shadowSourceLabel(winner *skills.Skill) string {
	if winner == nil {
		return ""
	}
	if winner.Namespace != "" {
		return winner.Namespace
	}
	return winner.Source
}

func (d *SkillsDialog) renderBody(th tui.Theme, width int) []string {
	s := d.viewing
	out := []string{
		FrameHeader(th, i18n.T("skill: %s  (esc / enter to go back)", s.Ref()), width),
		"  " + th.FG256(th.Muted, s.Description),
		"  " + th.FG256(th.Muted, i18n.T("source: %s  (%s)", s.Source, s.Path)),
	}
	if s.ArgumentHint != "" {
		out = append(out, "  "+th.FG256(th.Muted, i18n.T("argument: %s", s.ArgumentHint)))
	}
	// The collision is the whole reason this entry looks unusual, so say it
	// where the user landed to find out, not only in the one-line row.
	if s.ShadowedBy != nil {
		out = append(out, "  "+th.FG256(th.Muted, i18n.T("shadowed: %q is taken by the %s skill; load this one as %s",
			s.Name, shadowSourceLabel(s.ShadowedBy), s.Qualified())))
	}
	out = append(out, "")

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
	// Ref, not Name: for a shadowed skill the bare name belongs to somebody
	// else, and this row is where the user reads what to type.
	left := padRunes(truncateLineSafe(s.Ref(), 20), 20) + "  "
	src := "  " + truncateLineSafe(s.Source, 16)
	room := maxWidth - runeLen(left) - runeLen(src)
	if room < 10 {
		room = 10
	}
	desc := s.Description
	// A shadowed row without this reads as a duplicate entry rather than a
	// collision: same description, no clue why the name is spelled oddly.
	if s.ShadowedBy != nil {
		desc = i18n.T("shadowed by %s — %s", shadowSourceLabel(s.ShadowedBy), desc)
	}
	// Truncate AND pad to the same column. Only truncating leaves a short
	// description pulling its source tag left, which reads as a ragged column
	// once the list is long and the descriptions vary in length — the state
	// the picker is in now that it carries the eleven built-ins.
	//
	// Widths are counted in runes throughout: descriptions here routinely
	// carry em dashes, which are three bytes each, so a byte count both
	// over-truncates and mis-pads.
	return left + padRunes(truncateLineSafe(desc, room), room) + src
}

// runeLen is the display width of s in runes rather than bytes.
func runeLen(s string) int { return len([]rune(s)) }

// padRunes right-pads s with spaces to n runes. Shorter is left alone; longer
// is returned unchanged, since truncation is the caller's decision.
func padRunes(s string, n int) string {
	if d := n - runeLen(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
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
