package dialogs

import (
	"fmt"
	"strings"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// SessionDialog is the inline picker shown when the user runs /sessions.
type SessionDialog struct {
	active   bool
	sessions []core.SessionSummary
	cursor   int
	renaming bool
	rename   string

	// MaxRows is the maximum number of session rows the dialog
	// will render in a single frame. Set by the host right before
	// Render based on the available chat space; if 0, the dialog
	// falls back to rendering every row (original behaviour).
	// When the list is longer than MaxRows the dialog scrolls so
	// the cursor stays visible and tags the first/last visible
	// entry with a muted "↑ N more" / "↓ N more" row so the user
	// knows there's offscreen content.
	MaxRows int

	// viewTop is the index of the first session currently drawn.
	// Adjusted to follow the cursor on up/down moves.
	viewTop int

	// Rename, when set, persists a rename instead of the default direct
	// core.RenameSession write. The ctrlproto path routes it through the
	// service so a live session's in-memory title stays in sync and other
	// clients get the session_updated broadcast.
	Rename func(path, title string) error

	// List, when set, supplies the summaries instead of the default disk
	// scan. The ctrlproto path routes it through the service's session
	// group, which overlays live state (current model, settled title,
	// usage) the file's meta line can lag behind.
	List func() []core.SessionSummary

	// ListArchived supplies the archive when the user asks to see it. Nil
	// means this frontend cannot browse an archive (a replay carrier), and
	// the picker then does not offer the key at all rather than offering one
	// that opens an empty list.
	ListArchived func() []core.ArchivedSession

	// archived is the view mode: false lists live sessions, true lists the
	// archive. One dialog rather than two, because everything but the row
	// bodies and the verbs is identical — the same viewport, the same
	// scrolling, the same relative-time column.
	archived     bool
	archivedRows []core.ArchivedSession

	// confirming holds a delete under the cursor awaiting a y/n. Delete is the
	// one verb here that destroys a transcript, and it sits one key away from
	// archive, which does not — so it asks.
	confirming bool
}

// rowCount is the number of rows in whichever list is showing.
func (d *SessionDialog) rowCount() int {
	if d.archived {
		return len(d.archivedRows)
	}
	return len(d.sessions)
}

// rowPlain renders row i of the showing list, unstyled.
func (d *SessionDialog) rowPlain(i, width int) string {
	if d.archived {
		a := d.archivedRows[i]
		return FormatSessionRowPlain(a.SessionSummary, width-len([]rune(archivedSizeTag(a)))) + archivedSizeTag(a)
	}
	return FormatSessionRowPlain(d.sessions[i], width)
}

// archivedSizeTag is what archiving bought, shown on the row that bought it.
func archivedSizeTag(a core.ArchivedSession) string {
	if a.Bytes <= 0 {
		return ""
	}
	return fmt.Sprintf("  [%s]", humanBytes(a.Bytes))
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/float64(int64(1)<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(n)/float64(int64(1)<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// cursorRowTitle names the row under the cursor for a confirm prompt.
func (d *SessionDialog) cursorRowTitle() string {
	if d.cursor < 0 || d.cursor >= d.rowCount() {
		return ""
	}
	var s core.SessionSummary
	if d.archived {
		s = d.archivedRows[d.cursor].SessionSummary
	} else {
		s = d.sessions[d.cursor]
	}
	if t := strings.TrimSpace(s.Title); t != "" {
		return t
	}
	if t := strings.TrimSpace(s.FirstUserText); t != "" {
		return t
	}
	return i18n.T("(empty)")
}

// sessionDialogAction is returned by HandleKey.
type sessionDialogAction struct {
	Select  bool
	Path    string
	Close   bool
	Renamed bool
	// GenerateTitle asks the host to run on-demand title generation for
	// Path (a model call — the host handles it asynchronously and reports
	// back via SetTitleFor / the status line).
	GenerateTitle bool

	// The three lifecycle verbs. Archive and Delete name a LIVE row and carry
	// its Path; Restore names an archived row and carries its ID, because an
	// archived transcript's path points at a .jsonl.gz that is not a session
	// file and must never be handed to something that opens sessions.
	Archive bool
	Delete  bool
	Restore bool
	ID      string
}

func NewSessionDialog() *SessionDialog { return &SessionDialog{} }

// Open populates the dialog from root + cwd and shows it. Empty
// sessions (zero messages) are filtered out so the currently-running
// session, a freshly-opened one that hasn't received a prompt yet,
// and any stale empties that haven't been pruned yet all stay out
// of the picker. Resuming an empty session is a no-op anyway.
func (d *SessionDialog) Open(root, cwd string) {
	var all []core.SessionSummary
	if d.List != nil {
		all = d.List()
	} else {
		all = core.DescribeSessions(root, cwd)
	}
	filtered := make([]core.SessionSummary, 0, len(all))
	for _, s := range all {
		if s.MessageCount == 0 {
			continue
		}
		filtered = append(filtered, s)
	}
	d.sessions = filtered
	d.cursor = 0
	d.viewTop = 0
	d.archived = false
	d.confirming = false
	d.active = true
}

// CursorPos returns the row/col for the terminal cursor when in
// rename mode. Returns -1, -1 otherwise.
func (d *SessionDialog) CursorPos() (row, col int) {
	if !d.renaming {
		return -1, -1
	}
	// Row: frameHeader(1) + rename hint(1) = row 2 (0-indexed)
	// Col: 2 spaces indent + text length
	return 2, 2 + len([]rune(d.rename))
}

// Close hides the dialog.
func (d *SessionDialog) Close() { d.active = false }

// Active reports whether the dialog is visible and consumes input.
func (d *SessionDialog) Active() bool { return d != nil && d.active }

// Render returns the dialog lines.
func (d *SessionDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var lines []string
	title := i18n.T("sessions")
	if d.archived {
		title = i18n.T("sessions - archive")
	}
	lines = append(lines, FrameHeader(th, title, width))
	if d.rowCount() == 0 {
		if d.archived {
			lines = append(lines, th.FG256(th.Muted, i18n.T("nothing archived for this directory")))
			lines = append(lines, th.FG256(th.Muted, i18n.T("A back to sessions, esc to close")))
		} else {
			lines = append(lines, th.FG256(th.Muted, i18n.T("no previous sessions for this directory")))
			lines = append(lines, th.FG256(th.Muted, i18n.T("press esc to close")))
		}
		lines = append(lines, FrameRule(th, width))
		return lines
	}
	if d.renaming {
		lines = append(lines, th.FG256(th.Muted, i18n.T("rename session (enter to save, esc to cancel):")))
		lines = append(lines, "  "+th.FG256(th.FG, d.rename))
		lines = append(lines, FrameRule(th, width))
		return lines
	}
	if d.confirming {
		// Named, not just "this session": the picker is a list of near-identical
		// rows, and a confirm that does not say WHICH one is not a confirm.
		lines = append(lines, th.FG256(th.Error, i18n.T("delete %q? this cannot be undone (y to delete, any other key cancels)", d.cursorRowTitle())))
		lines = append(lines, th.FG256(th.Muted, i18n.T("a archives it instead — same list, nothing lost")))
		lines = append(lines, FrameRule(th, width))
		return lines
	}
	switch {
	case d.archived:
		lines = append(lines, th.FG256(th.Muted, i18n.T("archived sessions (↑/↓, enter or r restores, A back, esc cancel)")))
	case d.ListArchived != nil:
		lines = append(lines, th.FG256(th.Muted, i18n.T("pick a session (↑/↓, enter resume, r rename, g title, a archive, d delete, A archived, esc cancel)")))
	default:
		lines = append(lines, th.FG256(th.Muted, i18n.T("pick a session (↑/↓, pgup/pgdn, enter resume, r rename, g generate title, esc cancel)")))
	}

	// Viewport: windowed slice of d.sessions around d.cursor so a
	// list taller than the terminal still scrolls. Caller sets
	// MaxRows to the number of rows available for session entries
	// (i.e. excluding the header, hint, chrome). When it's zero or
	// bigger than the list, we draw everything.
	total := d.rowCount()
	window := d.MaxRows
	if window <= 0 || window >= total {
		window = total
	}
	d.viewTop = clampViewTop(d.viewTop, d.cursor, window, total)
	viewBot := d.viewTop + window
	if viewBot > total {
		viewBot = total
	}

	// Top indicator: how many rows are above the viewport.
	if d.viewTop > 0 {
		lines = append(lines, WindowMoreAbove(th, d.viewTop))
	}
	for i := d.viewTop; i < viewBot; i++ {
		plain := "  " + d.rowPlain(i, width-2)
		if i == d.cursor {
			lines = append(lines, th.PadHighlight(plain, width))
		} else {
			lines = append(lines, th.FG256(th.Muted, plain))
		}
	}
	// Bottom indicator: how many rows are below the viewport.
	if viewBot < total {
		lines = append(lines, WindowMoreBelow(th, total, viewBot))
	}
	lines = append(lines, FrameRule(th, width))
	return lines
}

// clampViewTop returns a viewTop that keeps cursor visible in a
// window of the given size over a list of `total` rows. Leaves one
// row of padding above/below where possible so moving the cursor
// doesn't land right on the top/bottom edge — easier to see what
// direction you're moving.
func clampViewTop(viewTop, cursor, window, total int) int {
	if window <= 0 || total <= 0 {
		return 0
	}
	if window >= total {
		return 0
	}
	pad := 2
	if window < 6 {
		pad = 0
	}
	if cursor < viewTop+pad {
		viewTop = cursor - pad
	}
	if cursor >= viewTop+window-pad {
		viewTop = cursor - window + pad + 1
	}
	if viewTop < 0 {
		viewTop = 0
	}
	if viewTop+window > total {
		viewTop = total - window
	}
	return viewTop
}

// FormatSessionRowPlain returns the session row body without any ANSI
// styling so the caller can wrap it in either a plain mute color or a
// full-row selection highlight. The returned string is guaranteed to
// fit within maxWidth visible characters so the terminal never soft-
// wraps it into the next row. Exported because the pre-TUI --resume
// fallback picker (non-TTY boots) renders the same rows.
func FormatSessionRowPlain(s core.SessionSummary, maxWidth int) string {
	when := formatRelative(s.Started)
	summary := strings.TrimSpace(s.Title)
	if summary == "" {
		summary = strings.TrimSpace(s.FirstUserText)
	}
	if summary == "" {
		summary = i18n.T("(empty)")
	}
	summary = strings.ReplaceAll(summary, "\n", " ")
	left := fmt.Sprintf("%s%-14s  %s/%s  %d msgs  $%.4f  ",
		sessionStatusGlyph(s), when, s.Provider, s.Model, s.MessageCount, s.TotalCost)
	room := maxWidth - len([]rune(left))
	if room < 4 {
		room = 4
	}
	runes := []rune(summary)
	if len(runes) > room {
		summary = string(runes[:room-3]) + "..."
	}
	row := left + summary
	// Hard clamp: ensure the full row never exceeds maxWidth.
	rowRunes := []rune(row)
	if len(rowRunes) > maxWidth {
		if maxWidth <= 3 {
			row = strings.Repeat(".", maxWidth)
		} else {
			row = string(rowRunes[:maxWidth-3]) + "..."
		}
	}
	return row
}

// sessionStatusGlyph is a fixed-width (2-column) prefix conveying a session's
// live state: ● a turn is in flight, ○ materialized but idle, or two spaces for
// a cold on-disk session (the common case for a local picker or an old daemon).
// Monochrome on purpose — a row is styled as a whole (the selection highlight or
// a flat mute), so the state has to read from the glyph shape, not colour. The
// filled/hollow/blank gradient mirrors the web board's busy/idle/cold pills.
func sessionStatusGlyph(s core.SessionSummary) string {
	switch {
	case s.Busy:
		return "● "
	case s.Live:
		return "○ "
	default:
		return "  "
	}
}

func formatRelative(t time.Time) string {
	if t.IsZero() {
		return i18n.T("unknown")
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return i18n.T("just now")
	case d < time.Hour:
		return i18n.T("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return i18n.T("%d h ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return i18n.T("%d d ago", int(d.Hours()/24))
	default:
		return t.Local().Format("2006-01-02")
	}
}

// HandleKey advances the dialog and returns an action to apply, if any.
func (d *SessionDialog) HandleKey(k tui.Key) sessionDialogAction {
	// Rename mode: type the new name.
	if d.renaming {
		switch k.Kind {
		case tui.KeyEnter:
			title := strings.TrimSpace(d.rename)
			if title != "" && d.cursor < len(d.sessions) {
				path := d.sessions[d.cursor].Path
				rename := d.Rename
				if rename == nil {
					rename = core.RenameSession
				}
				if rename(path, title) == nil {
					d.sessions[d.cursor].Title = title
				}
			}
			d.renaming = false
			d.rename = ""
			return sessionDialogAction{Renamed: true}
		case tui.KeyEsc:
			d.renaming = false
			d.rename = ""
			return sessionDialogAction{}
		case tui.KeyBackspace:
			if len(d.rename) > 0 {
				r := []rune(d.rename)
				d.rename = string(r[:len(r)-1])
			}
			return sessionDialogAction{}
		case tui.KeyPaste:
			d.rename += k.Paste
			return sessionDialogAction{}
		case tui.KeyRune:
			if k.Rune != 0 {
				d.rename += string(k.Rune)
			}
			return sessionDialogAction{}
		}
		return sessionDialogAction{}
	}

	page := d.MaxRows
	if page <= 0 {
		page = 10
	}
	if page > 1 {
		page--
	}
	// A pending delete owns the keyboard: every key either confirms it or
	// cancels it, so no navigation key can be mistaken for an answer.
	if d.confirming {
		d.confirming = false
		if k.Kind == tui.KeyRune && (k.Rune == 'y' || k.Rune == 'Y') {
			if d.cursor >= 0 && d.cursor < len(d.sessions) {
				return sessionDialogAction{Delete: true, Path: d.sessions[d.cursor].Path}
			}
		}
		return sessionDialogAction{}
	}

	switch k.Kind {
	case tui.KeyUp:
		if d.cursor > 0 {
			d.cursor--
		}
	case tui.KeyDown:
		if d.cursor < d.rowCount()-1 {
			d.cursor++
		}
	case tui.KeyPageUp:
		d.cursor -= page
		if d.cursor < 0 {
			d.cursor = 0
		}
	case tui.KeyPageDown:
		d.cursor += page
		if d.cursor >= d.rowCount() {
			d.cursor = d.rowCount() - 1
			if d.cursor < 0 {
				d.cursor = 0
			}
		}
	case tui.KeyHome:
		d.cursor = 0
	case tui.KeyEnd:
		if d.rowCount() > 0 {
			d.cursor = d.rowCount() - 1
		}
	case tui.KeyEsc:
		d.Close()
		return sessionDialogAction{Close: true}
	case tui.KeyEnter:
		if d.rowCount() == 0 {
			d.Close()
			return sessionDialogAction{Close: true}
		}
		if d.archived {
			// Enter on an archived row RESTORES it rather than resuming it:
			// there is nothing to resume until it is back in the sessions
			// directory, and a picker whose enter key does nothing is worse
			// than one whose enter key does the only available thing.
			return sessionDialogAction{Restore: true, ID: d.archivedRows[d.cursor].ID}
		}
		s := d.sessions[d.cursor]
		d.Close()
		return sessionDialogAction{Select: true, Path: s.Path}
	case tui.KeyRune:
		if k.Rune == 'A' && d.ListArchived != nil {
			d.ShowArchived(!d.archived)
			return sessionDialogAction{}
		}
		if d.rowCount() == 0 {
			return sessionDialogAction{}
		}
		if d.archived {
			if k.Rune == 'r' {
				return sessionDialogAction{Restore: true, ID: d.archivedRows[d.cursor].ID}
			}
			return sessionDialogAction{}
		}
		switch k.Rune {
		case 'r':
			s := d.sessions[d.cursor]
			d.renaming = true
			if s.Title != "" {
				d.rename = s.Title
			} else {
				d.rename = ""
			}
		case 'g':
			return sessionDialogAction{GenerateTitle: true, Path: d.sessions[d.cursor].Path}
		case 'a':
			if d.ListArchived != nil {
				return sessionDialogAction{Archive: true, Path: d.sessions[d.cursor].Path}
			}
		case 'd':
			// Ask first. This is the only key in the picker that destroys a
			// transcript, and it is adjacent to the one that does not.
			d.confirming = true
		}
	}
	return sessionDialogAction{}
}

// ShowArchived switches the picker between the live list and the archive,
// re-listing whichever it lands on. A dialog with no ListArchived hook stays on
// the live list: a frontend that cannot browse an archive must not appear to.
func (d *SessionDialog) ShowArchived(on bool) {
	if on && d.ListArchived == nil {
		return
	}
	d.archived = on
	d.cursor = 0
	d.viewTop = 0
	d.confirming = false
	if on {
		d.archivedRows = d.ListArchived()
	}
}

// ShowingArchived reports which list the picker is on, so the host knows whether
// a refresh should re-list the archive or the sessions.
func (d *SessionDialog) ShowingArchived() bool { return d != nil && d.archived }

// Refresh re-runs List (if set) so an OPEN picker picks up sessions another
// client added, renamed, or deleted, keeping the cursor on the same session
// where it survives. A no-op while closed or mid-rename (a re-list under the
// user's fingers would fight a rename in progress). The attached TUI calls it
// on a sessions_changed broadcast so the picker never shows a stale set.
func (d *SessionDialog) Refresh(root, cwd string) {
	if !d.Active() || d.renaming || d.confirming {
		return
	}
	// The archive is its own list with its own source; re-running the session
	// scan under it would silently drop the user back onto the live sessions.
	if d.archived {
		cur := ""
		if d.cursor >= 0 && d.cursor < len(d.archivedRows) {
			cur = d.archivedRows[d.cursor].ID
		}
		d.archivedRows = d.ListArchived()
		d.cursor, d.viewTop = 0, 0
		for i := range d.archivedRows {
			if d.archivedRows[i].ID == cur {
				d.cursor = i
				break
			}
		}
		return
	}
	var curPath string
	if d.cursor >= 0 && d.cursor < len(d.sessions) {
		curPath = d.sessions[d.cursor].Path
	}
	d.Open(root, cwd) // re-lists via List(); resets cursor to 0
	for i := range d.sessions {
		if d.sessions[i].Path == curPath {
			d.cursor = i
			break
		}
	}
}

// SetTitleFor updates the displayed title of the row for path, if it is
// still listed — the host calls it when an async title generation lands.
// A dialog that was closed or re-listed in the meantime simply no-ops.
func (d *SessionDialog) SetTitleFor(path, title string) {
	for i := range d.sessions {
		if d.sessions[i].Path == path {
			d.sessions[i].Title = title
			return
		}
	}
}
