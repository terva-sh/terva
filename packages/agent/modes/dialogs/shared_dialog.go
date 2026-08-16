package dialogs

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// SharedDialog is the /shared panel: everything the agent handed to the user in
// this session, and the things a person actually wants to do with a file.
//
// The transcript cards say what was shared and are the right surface for that —
// they sit where it happened. What they cannot be is a place to ACT: a card is
// scrollback, and the file you want is usually several turns up. This panel is
// the session's deliverables in one list, with the three verbs that make a
// listing useful rather than merely informative: put the path on the clipboard,
// open it in whatever the system opens it with, and save a copy into the
// working directory.
//
// It holds no data of its own. listFn pulls the host's cache every render, the
// TasksDialog/WorktreeDialog pattern, so a refresh has one place to land.
type SharedDialog struct {
	active   bool
	selected int
	vp       Viewport

	listFn func() []ctrlproto.SharedFileEntry

	// mu guards notice and noticeErr, and nothing else.
	//
	// Those two are the only fields that cross a goroutine boundary: the host
	// dispatches `go i.saveSharedFile(id)` so the panel stays painted while the
	// fetch and the write run, and that goroutine ends in Notice() while the
	// render goroutine is reading the same pair every frame. Everything else
	// here — active, selected, vp, listFn, MaxRows, Now — is touched only by the
	// main goroutine's key handling and render, so locking it would buy nothing
	// and hide which state is actually shared.
	//
	// NEVER hold this across listFn(). That callback reaches back into the host
	// and takes the host's own lock, while the host calls into this dialog while
	// holding it — so a lock held across the callback closes an ABBA cycle and
	// wedges the TUI with no panic and no error to read.
	// TestDialogLocksAreNotHeldAcrossHostCallbacks fails if that ever changes.
	mu sync.Mutex
	// notice is the result of the last action ("copied", "saved to …", or why
	// one was refused), shown under the list. Cleared by the next key, so it
	// belongs to the action that raised it and not to the row you moved to.
	notice string
	// noticeErr marks the notice as a refusal, which colours it.
	noticeErr bool

	// Now is the clock the expiry column reads. Nil means time.Now; a test pins
	// it so the rendered "6d left" is an assertion rather than a race against
	// the store's TTL.
	Now func() time.Time

	// MaxRows caps the body height; the overlay sets it from the terminal size
	// each frame so a long list stays inside the bottom band. 0 = unlimited.
	MaxRows int
}

// SharedAction reports what the host should do after a key.
//
// Each action carries the ID rather than a resolved file, because the host is
// the only side that can act: a share is a handle the DAEMON resolves, and the
// path in a listing names the daemon's disk. On a remote carrier that path is
// not the client's to open, which is a distinction the host makes and this
// panel deliberately does not.
type SharedAction struct {
	Close   bool
	Refresh bool

	// CopyID puts the file's path on the clipboard; OpenID hands it to the
	// system viewer; SaveID writes a copy into the working directory.
	CopyID string
	OpenID string
	SaveID string
}

func NewSharedDialog() *SharedDialog { return &SharedDialog{} }

func (d *SharedDialog) Active() bool { return d != nil && d.active }

// ChromeRows is the non-body rows Render emits: the header, the hint, the
// footer, and the closing rule.
func (d *SharedDialog) ChromeRows() int { return 4 }

// Open shows the panel over the host's live cache.
func (d *SharedDialog) Open(listFn func() []ctrlproto.SharedFileEntry) {
	d.active = true
	d.selected = 0
	d.setNotice("", false)
	d.vp.Reset()
	d.listFn = listFn
}

func (d *SharedDialog) Close() {
	d.active = false
	d.listFn = nil
	d.selected = 0
	d.setNotice("", false)
	d.vp.Reset()
}

// Notice reports the outcome of an action the host performed. The host owns
// every action (clipboard, viewer, filesystem), so it is also the only side
// that knows whether one worked — and it may say so from a background
// goroutine, which is why this is the guarded pair.
func (d *SharedDialog) Notice(text string, isErr bool) {
	d.setNotice(text, isErr)
}

// setNotice and noticeState are the only readers and writers of the guarded
// pair. Every other site goes through them, so the lock cannot be forgotten at
// one of the seven places a key clears the notice.
func (d *SharedDialog) setNotice(text string, isErr bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.notice, d.noticeErr = text, isErr
}

func (d *SharedDialog) noticeState() (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.notice, d.noticeErr
}

// clearNotice is setNotice's common case: a key moved the cursor, so the
// previous action's message no longer belongs to the row under it.
func (d *SharedDialog) clearNotice() { d.setNotice("", false) }

func (d *SharedDialog) rows() []ctrlproto.SharedFileEntry {
	if d.listFn == nil {
		return nil
	}
	return d.listFn()
}

// current is the selected row, or nil when the list is empty or the selection
// is out of range — which it can be, because the list is refetched every render
// and a sweep can shorten it under the cursor.
func (d *SharedDialog) current() *ctrlproto.SharedFileEntry {
	rows := d.rows()
	if d.selected < 0 || d.selected >= len(rows) {
		return nil
	}
	return &rows[d.selected]
}

func (d *SharedDialog) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// act builds the action for a row-scoped key, refusing when there is no row or
// when the row's bytes are already gone.
//
// The expiry check is the point: an entry whose deadline has passed is one the
// sweeper may already have taken, and every one of these verbs would fail on
// it. Saying so here is better than three different filesystem errors.
func (d *SharedDialog) act(fn func(id string) SharedAction) SharedAction {
	it := d.current()
	if it == nil {
		d.setNotice(i18n.T("nothing to act on"), true)
		return SharedAction{}
	}
	if shareExpired(it.ExpiresAt, d.now()) {
		// The name is interpolated into a sentence, so an escape in it would
		// repaint the notice line rather than merely garble the name.
		d.setNotice(i18n.T("%s has expired — its bytes are gone", tui.SanitizeLabel(it.Name)), true)
		return SharedAction{}
	}
	d.setNotice("", false)
	return fn(it.ID)
}

func (d *SharedDialog) HandleKey(k tui.Key) SharedAction {
	// Any key clears the previous action's notice: it described THAT act, and
	// leaving it up while the selection moves would attach it to another row.
	switch k.Kind {
	case tui.KeyEsc:
		return SharedAction{Close: true}
	case tui.KeyUp:
		d.clearNotice()
		if d.selected > 0 {
			d.selected--
		}
	case tui.KeyDown:
		d.clearNotice()
		if d.selected < len(d.rows())-1 {
			d.selected++
		}
	case tui.KeyHome:
		d.clearNotice()
		d.selected = 0
	case tui.KeyEnd:
		d.clearNotice()
		if n := len(d.rows()); n > 0 {
			d.selected = n - 1
		}
	case tui.KeyPageUp:
		d.clearNotice()
		d.selected -= d.pageSize()
		if d.selected < 0 {
			d.selected = 0
		}
	case tui.KeyPageDown:
		d.clearNotice()
		d.selected += d.pageSize()
		if n := len(d.rows()); d.selected >= n {
			d.selected = n - 1
		}
		if d.selected < 0 {
			d.selected = 0
		}
	case tui.KeyEnter:
		// Enter is open: the obvious thing to do with a file you selected.
		return d.act(func(id string) SharedAction { return SharedAction{OpenID: id} })
	case tui.KeyRune:
		switch k.Rune {
		case 'c', 'C':
			return d.act(func(id string) SharedAction { return SharedAction{CopyID: id} })
		case 'o', 'O':
			return d.act(func(id string) SharedAction { return SharedAction{OpenID: id} })
		case 's', 'S':
			return d.act(func(id string) SharedAction { return SharedAction{SaveID: id} })
		case 'r', 'R':
			d.clearNotice()
			return SharedAction{Refresh: true}
		}
	}
	return SharedAction{}
}

func (d *SharedDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	rows := d.rows()
	lines := []string{FrameHeader(th, i18n.T("shared files"), width)}

	if len(rows) == 0 {
		// An empty drawer is an ordinary state, not a failure: most sessions
		// never hand anything over. Say what would put something here.
		//
		// This path still falls through to the notice below rather than
		// returning early. The list is refetched every render, so it can empty
		// out UNDER a keypress — a sweep, a session switch — and that is
		// precisely when a refusal has something to explain. Returning here
		// swallowed it, leaving a key that looked like it did nothing.
		lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("this session has not shared any files")))
	} else {
		lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("↑/↓ select · c copy path · o open · s save here · r refresh · esc close")))

		body := make([]string, 0, len(rows))
		for i, it := range rows {
			body = append(body, d.renderRow(th, it, i == d.selected, width))
		}
		d.vp.Fit(len(body), d.bodyRows())
		// Keep the cursor inside the window, so ↓ scrolls the list rather than
		// driving the selection off-screen and acting on a row nobody can see.
		// Same padding rule as the session and worktree panels.
		d.vp.RevealPadded(d.selected, cursorPadRows)
		lines = append(lines, d.vp.Rows(th, body)...)
	}

	// Read once, outside any other lock: the render pass must see one
	// consistent (text, isErr) pair rather than a message with the colour of
	// whatever landed between the two reads.
	if notice, isErr := d.noticeState(); notice != "" {
		colour := th.Muted
		if isErr {
			colour = th.Error
		}
		lines = append(lines, th.FG256(colour, "  "+notice))
	}
	lines = append(lines, FrameRule(th, width))
	return lines
}

// bodyRows is how many list rows fit, given the cap the overlay set. 0 means
// no cap, which the viewport reads as "the pane is the whole body".
func (d *SharedDialog) bodyRows() int {
	if d.MaxRows <= 0 {
		return 0
	}
	rows := d.MaxRows - d.ChromeRows()
	if rows < 1 {
		rows = 1
	}
	return rows
}

// pageSize is how far PgUp/PgDn move the SELECTION.
//
// It moves the selection rather than the offset because this list is navigated,
// not read: the actions are row-scoped, so a page that scrolled past the cursor
// would leave the keys acting on something off-screen. One screenful, or the
// whole list when the host has not capped the height yet.
func (d *SharedDialog) pageSize() int {
	if rows := d.bodyRows(); rows > 0 {
		return rows
	}
	if n := len(d.rows()); n > 0 {
		return n
	}
	return 1
}

func (d *SharedDialog) renderRow(th tui.Theme, it ctrlproto.SharedFileEntry, selected bool, width int) string {
	// A row is padded and colour-filled to a fixed width, so an escape in the
	// name does not just corrupt the name: it survives into the highlight and
	// the rows painted after it. A name that sanitizes away is no name.
	name := tui.SanitizeLabel(it.Name)
	if name == "" {
		name = i18n.T("(unnamed)")
	}
	var details []string
	if it.Kind != "" {
		details = append(details, it.Kind)
	}
	if size := humanShareSize(it.Size); size != "" {
		details = append(details, size)
	}
	expired := shareExpired(it.ExpiresAt, d.now())
	if expired {
		details = append(details, i18n.T("expired"))
	}

	row := "  " + name
	if len(details) > 0 {
		row += "  " + strings.Join(details, " · ")
	}
	switch {
	case selected:
		return th.PadHighlight(row, width)
	case expired:
		// A dead row stays listed — the session did share it — but must not
		// look like something you can still act on.
		return th.FG256(th.Error, row)
	default:
		return th.FG256(th.Muted, row)
	}
}

// shareExpired reports the sweeper being entitled to the bytes.
//
// An expiry the daemon did not send, or one that will not parse, is UNKNOWN and
// therefore not expired: telling someone their file is gone when it is not is
// the worse of the two errors, and the action they try will say so honestly.
func shareExpired(expiresAt string, now time.Time) bool {
	if expiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return false
	}
	return !t.After(now)
}

// humanShareSize renders a byte count for a row.
func humanShareSize(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
}
