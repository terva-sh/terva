package dialogs

import (
	"fmt"
	"strings"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// MemoryDialog shows the agent's durable memory — the facts it has decided are
// worth carrying into a future session — and lets the user prune them. Opened
// with /memory.
//
// The two scopes render as one scrollable list under section headers rather than
// as two panes, because the operations are identical and the interesting
// question ("what does terva think it knows?") is answered by reading straight
// down. Each row knows which scope it belongs to, so a delete or a clear lands
// in the right file without the user having to select a pane first.
//
// The user is not an author here. The model curates; this surface prunes,
// resets, and re-reads. Adding a fact by hand is what editing memory.md is for,
// and `r` picks that up.
type MemoryDialog struct {
	active bool
	rows   []MemoryRow
	// scopes carries each scope's budget for the footer, in render order.
	scopes []MemoryScopeInfo
	cursor int
	vp     Viewport
	// MaxRows is the body height the host budgets from the terminal
	// (dialogs.BodyBudget). 0 falls back to memoryFallbackRows.
	MaxRows int
	status  string
	// confirmClear holds the scope a pending `c` is waiting to confirm. Clearing
	// is the one irreversible action on this surface — the entries are gone from
	// the file, and the model spent real turns deciding they were worth keeping —
	// so it takes two keystrokes, unlike delete, which removes exactly the one
	// row the user is looking at.
	confirmClear string
}

// MemoryRow is one entry, tagged with the scope that owns it.
//
// One row type for both tiers, because the reading experience is the point: the
// question this pane answers is "what does terva think it knows", and splitting
// that across two lists makes the reader hold the split. What differs is what
// each row can SAY about itself, which is what the archived fields carry.
type MemoryRow struct {
	Scope string // "project" | "user"
	Text  string

	// Archived marks the keyed tier: not in the model's context right now, and
	// deleted by a different verb (forget, addressed by Ref) than an active
	// entry (remove, addressed by its full text).
	Archived bool
	// Ref is the scope-qualified id ("project:the-id"), for an archived row.
	Ref string
	// Keys are the entry's triggers. Shown because the archive's failure mode is
	// SILENCE — a spec keyed on the answer's vocabulary produces no output to
	// notice — and reading the triggers beside the entry is the only cheap way a
	// human catches it.
	Keys []string
	// Fired reports that this entry matched on the last turn; Dropped, that it
	// matched and the tail budget cut it. The two look identical from outside
	// (neither reaches the model) and need opposite fixes, so they render
	// differently.
	Fired   bool
	Dropped bool
}

// MemoryScopeInfo is a scope's header line: its label, how many entries it
// holds, and how close it is to its byte cap.
type MemoryScopeInfo struct {
	Scope    string
	Label    string
	Count    int
	Bytes    int
	MaxBytes int
	// Bound is false when the scope has nowhere to persist (no resolvable
	// project). Shown, because entries in an unbound scope are accepted and then
	// lost, and a list that silently will not survive is worse than an empty one.
	Bound bool

	// ArchivedCount / ArchivedBytes describe the keyed tier. Reported separately
	// rather than folded into the totals above, because they are measured against
	// a different cap and the whole point of the tier is that its size does not
	// count against the one the model keeps hitting.
	ArchivedCount int
	ArchivedBytes int
	// Problems are unreadable archive files: present, occupying the budget, and
	// unable to fire. Surfaced here because nothing else can.
	Problems []string
}

func NewMemoryDialog() *MemoryDialog { return &MemoryDialog{} }

// Open shows the dialog over the given scopes and rows.
func (d *MemoryDialog) Open(scopes []MemoryScopeInfo, rows []MemoryRow) {
	d.active = true
	d.scopes = scopes
	d.rows = rows
	d.cursor = 0
	d.status = ""
	d.confirmClear = ""
}

// SetItems refreshes in place after a mutation, keeping the cursor near where it
// was — a delete should leave the selection on the neighbouring row, not jump
// the user back to the top of a list they were working down.
func (d *MemoryDialog) SetItems(scopes []MemoryScopeInfo, rows []MemoryRow) {
	d.scopes = scopes
	d.rows = rows
	if d.cursor >= len(rows) {
		d.cursor = len(rows) - 1
	}
	if d.cursor < 0 {
		d.cursor = 0
	}
	d.confirmClear = ""
}

func (d *MemoryDialog) Active() bool { return d != nil && d.active }
func (d *MemoryDialog) Close()       { d.active = false }

// SetStatus shows a one-line result (an error from a refused mutation, say).
func (d *MemoryDialog) SetStatus(s string) { d.status = s }

func (d *MemoryDialog) current() (MemoryRow, bool) {
	if d.cursor < 0 || d.cursor >= len(d.rows) {
		return MemoryRow{}, false
	}
	return d.rows[d.cursor], true
}

// MemoryAction is returned by HandleKey for the overlay host to apply.
type MemoryAction struct {
	Remove bool
	// Forget deletes an ARCHIVED entry. A separate verb from Remove because the
	// two tiers address entries differently — Remove sends the full text and the
	// store matches a substring; Forget sends the Ref, which is an id.
	Forget bool
	Clear  bool
	Reload bool
	Close  bool
	Scope  string
	// Entry is what identifies the row: for Remove, its FULL text (full, not a
	// prefix — the store matches by substring and a truncated one could resolve
	// to a different entry than the row the user was looking at); for Forget, the
	// archived entry's Ref.
	Entry string
}

// HandleKey advances the dialog and returns an action to apply, if any.
func (d *MemoryDialog) HandleKey(k tui.Key) MemoryAction {
	if !d.Active() {
		return MemoryAction{}
	}
	// Any key that is not the confirming `c` cancels a pending clear. A
	// confirmation that survives an arrow key is a confirmation of nothing.
	pending := d.confirmClear
	if !(k.Kind == tui.KeyRune && (k.Rune == 'c' || k.Rune == 'C')) {
		d.confirmClear = ""
	}

	switch k.Kind {
	case tui.KeyUp:
		if d.cursor > 0 {
			d.cursor--
		}
	case tui.KeyDown:
		if d.cursor < len(d.rows)-1 {
			d.cursor++
		}
	case tui.KeyEsc:
		d.Close()
		return MemoryAction{Close: true}
	case tui.KeyRune:
		switch k.Rune {
		case 'd', 'D':
			row, ok := d.current()
			if !ok {
				return MemoryAction{}
			}
			if row.Archived {
				return MemoryAction{Forget: true, Scope: row.Scope, Entry: row.Ref}
			}
			return MemoryAction{Remove: true, Scope: row.Scope, Entry: row.Text}
		case 'c', 'C':
			row, ok := d.current()
			if !ok {
				d.status = i18n.T("nothing to clear")
				return MemoryAction{}
			}
			if pending == row.Scope {
				d.confirmClear = ""
				return MemoryAction{Clear: true, Scope: row.Scope}
			}
			d.confirmClear = row.Scope
			// Says "active" out loud. Clear leaves the archive alone, and a
			// confirmation that does not name its own scope is how someone
			// confirms more than they meant to.
			d.status = i18n.T("press c again to clear the active entries in %s (archived entries are kept)",
				scopeLabel(d.scopes, row.Scope))
		case 'r', 'R':
			// Pull writes another instance (or a hand-edit) made since this
			// session loaded. The file is a supported interface, not a
			// workaround, so re-reading it is a first-class action.
			return MemoryAction{Reload: true}
		}
	}
	return MemoryAction{}
}

func scopeLabel(scopes []MemoryScopeInfo, scope string) string {
	for _, s := range scopes {
		if s.Scope == scope {
			return s.Label
		}
	}
	return scope
}

// Render returns the dialog lines.
const memoryFallbackRows = 12

// ChromeRows is the non-body rows Render emits at their worst case.
// Verified by TestEveryDialogFitsItsOwnBudget rather than counted by eye.
func (d *MemoryDialog) ChromeRows() int { return 6 }

func (d *MemoryDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var lines []string
	lines = append(lines, FrameHeader(th, i18n.T("memory"), width))

	if len(d.rows) == 0 {
		lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("nothing saved yet — the agent adds facts worth keeping as it works")))
		lines = append(lines, FrameRule(th, width))
		return lines
	}

	// Clipped like everything else. The key hints are the longest fixed string
	// here, so on a narrow pane they were what ran past the border and wrapped —
	// a broken frame reads as a rendering bug, where a shortened hint reads as a
	// narrow window.
	lines = append(lines, th.FG256(th.Muted, clipLine(
		i18n.T("↑/↓ · d delete · c clear active · r reload from disk · esc"), width)))

	maxRows := d.MaxRows
	if maxRows <= 0 {
		maxRows = memoryFallbackRows
	}
	// Centred: this list is filtered and rebuilt under the cursor, so
	// holding the cursor still and moving the content reads better than
	// scrolling only at the edges. Named here rather than implied by
	// whichever windowing helper was reached for.
	d.vp.Fit(len(d.rows), maxRows)
	d.vp.Center(d.cursor)
	start, end := d.vp.Window()
	lastScope := ""
	// A header for the scope the window OPENS in, so a scrolled view never shows
	// rows whose scope is off-screen above them.
	if start < len(d.rows) {
		lastScope = d.rows[start].Scope
		lines = append(lines, d.scopeLines(th, lastScope, width)...)
	}
	for i := start; i < end; i++ {
		row := d.rows[i]
		if row.Scope != lastScope {
			lastScope = row.Scope
			lines = append(lines, d.scopeLines(th, lastScope, width)...)
		}
		plain := "    " + clipLine(rowLead(row)+row.Text, width-6)
		if i == d.cursor {
			lines = append(lines, th.PadHighlight(plain, width))
		} else {
			lines = append(lines, plain)
		}
		// The triggers, under the archived row the cursor is on. Only there:
		// printing them on every row would double the list's height and bury the
		// entries, and the question they answer ("would this ever fire?") is one
		// you ask about a specific entry.
		if i == d.cursor && row.Archived {
			lines = append(lines, th.FG256(th.Muted, "      "+clipLine(triggerLine(row), width-8)))
		}
	}
	if start > 0 {
		lines = append(lines, WindowMoreAbove(th, start))
	}
	if end < len(d.rows) {
		lines = append(lines, WindowMoreBelow(th, len(d.rows), end))
	}
	if d.status != "" {
		lines = append(lines, th.FG256(th.Muted, "  "+d.status))
	}
	lines = append(lines, FrameRule(th, width))
	return lines
}

// rowLead marks what a row IS, in the two characters before its text. An
// archived entry is not in the model's context, and a list that renders it
// identically to one that is answers the pane's central question wrongly.
//
//	(blank) active — in the model's context on every request
//	·       archived, did not match the last turn
//	▸       archived, matched and was injected
//	✗       archived, matched but the tail budget cut it
func rowLead(row MemoryRow) string {
	switch {
	case !row.Archived:
		return ""
	case row.Dropped:
		return "✗ "
	case row.Fired:
		return "▸ "
	default:
		return "· "
	}
}

// triggerLine is the detail under the selected archived row: what would make it
// fire, and what happened last turn.
func triggerLine(row MemoryRow) string {
	keys := i18n.T("no keys — this entry can never fire")
	if len(row.Keys) > 0 {
		keys = i18n.T("keys: %s", strings.Join(row.Keys, ", "))
	}
	switch {
	case row.Dropped:
		// The distinction worth spending a line on: fired-and-cut needs less
		// competition, never-fired needs different keys, and they look the same.
		return keys + i18n.T(" · matched last turn but was cut to fit the budget")
	case row.Fired:
		return keys + i18n.T(" · matched last turn")
	}
	return keys
}

// scopeHeader is one scope's section line: its name, its count, and how full it
// is. The fill is shown because the cap is what refuses the model's next write,
// and finding that out by hitting it is what cost turns in the reviewed session.
//
// The archived count sits outside that fraction on purpose. It is measured
// against a different, far larger cap, and folding it in would suggest archiving
// something brings the scope closer to refusing the next write when the whole
// point is that it does the opposite.
func scopeHeader(scopes []MemoryScopeInfo, scope string) string {
	for _, s := range scopes {
		if s.Scope != scope {
			continue
		}
		head := fmt.Sprintf("%s — %d/%s", s.Label, s.Count, fmtKiB(s.MaxBytes))
		if s.MaxBytes > 0 {
			head = fmt.Sprintf("%s — %d entries, %s of %s", s.Label, s.Count, fmtKiB(s.Bytes), fmtKiB(s.MaxBytes))
		}
		if s.ArchivedCount > 0 {
			head += i18n.T(" · %d archived (%s, out of context)", s.ArchivedCount, fmtKiB(s.ArchivedBytes))
		}
		if !s.Bound {
			head += i18n.T(" (not saved — no project for this session)")
		}
		return head
	}
	return scope
}

// scopeLines is a scope's section: its header, then a warning line if any of its
// archive files could not be read.
//
// The warning gets its own line rather than a suffix on the header, and the
// header is CLIPPED like every other row here. Appending it made a header that
// ran past the frame and wrapped mid-word at ordinary widths — and clipping that
// combined line would have dropped the warning first, which is precisely the
// thing with no other symptom: an unreadable archive file is present, counted
// against the budget, and unable to fire.
func (d *MemoryDialog) scopeLines(th tui.Theme, scope string, width int) []string {
	out := []string{th.FG256(th.Muted, "  "+clipLine(scopeHeader(d.scopes, scope), width-4))}
	for _, s := range d.scopes {
		if s.Scope == scope && len(s.Problems) > 0 {
			out = append(out, th.FG256(th.Muted, "  "+clipLine(
				i18n.T("⚠ %d archive file(s) could not be read — they can never fire", len(s.Problems)),
				width-4)))
		}
	}
	return out
}

func fmtKiB(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.1fK", float64(n)/1024)
}

// clipLine trims an entry to the available width. Entries are single lines by
// construction (the store sanitizes them), so this only has to bound length.
func clipLine(s string, max int) string {
	if max < 8 {
		max = 8
	}
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return strings.TrimSpace(string(r[:max-1])) + "…"
}
