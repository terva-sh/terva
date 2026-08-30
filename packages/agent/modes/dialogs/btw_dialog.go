package dialogs

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// SideChatExchange is one completed question/answer pair the dialog has
// accumulated, replayed to the asker so the side chat remembers its own thread.
type SideChatExchange struct {
	User      string
	Assistant string
}

// SideChatAsker runs the side chat's completions. It owns the frozen snapshot
// (opened before the dialog, closed by Close); the dialog owns the visible
// conversation and hands back its prior exchanges on every ask. Ask blocks on
// the model and honours ctx cancellation — the dialog runs it on a goroutine
// and cancels on esc. This keeps the dialog free of the carrier and the wire:
// it drives an ephemeral completion without knowing a *core.Agent ever existed.
type SideChatAsker interface {
	Ask(ctx context.Context, prior []SideChatExchange, question string) (string, error)
	Close()
}

// btwTurn is one user/assistant pair within a side chat. Kept
// separate from the main transcript so closing the dialog leaves
// the main session untouched.
type btwTurn struct {
	User      string
	Assistant string
	Err       string
}

// BtwDialog is the side-chat overlay opened by /btw. It shows the
// user's question, runs a one-off model call against the live
// snapshot of the main session plus any prior side-chat turns,
// renders the assistant reply through the markdown pipeline, and
// keeps the main transcript completely untouched.
//
// Cancellation: esc cancels an in-flight call when one is running,
// otherwise closes the dialog.
type BtwDialog struct {
	mu      sync.Mutex
	active  bool
	turns   []btwTurn
	editor  *tui.Editor
	loading bool
	cancel  context.CancelFunc

	// spin drives the same braille animation + rotating funny-line
	// shown in the main status bar. Owned by the dialog so its clock
	// is independent of the main spinner (so re-opening the dialog
	// always starts fresh and the message doesn't carry over from a
	// completed main turn).
	spin *widgets.Spinner

	// asker runs the completions against a snapshot frozen daemon-side at open
	// time, so a turn landing on the session while the dialog is up cannot shift
	// the ground under it. The dialog never sees the transcript or the client.
	asker SideChatAsker

	// Theme cached so render() doesn't need it threaded through.
	theme tui.Theme

	// cwd is the working directory used to resolve relative paths
	// when the user presses Tab on a path-like token in the side-
	// chat editor. Set by Open() from the host's cwd so the same
	// path-completion that works in the main editor also works
	// here.
	cwd string
}

func NewBtwDialog() *BtwDialog {
	return &BtwDialog{}
}

// Active reports whether the dialog is visible and consuming keys.
func (d *BtwDialog) Active() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.active
}

// Loading reports whether the dialog is currently awaiting a
// model response (and therefore rendering an animated spinner).
// Used by the host to decide whether a periodic redraw is worth
// triggering; when false and the user is just typing, we can
// skip the tick and let the terminal drive the cursor blink.
func (d *BtwDialog) Loading() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.active && d.loading
}

// Open enters the side chat. asker runs the completions against a snapshot the
// caller has already frozen daemon-side; cwd resolves path-completion in the
// editor; seed, if non-empty, is auto-submitted so `/btw <text>` starts a
// conversation right away. invalidate, if non-nil, is called after each state
// change so the host redraw loop picks up the update without polling.
//
// The dialog holds an asker, not a client or a transcript: it builds no
// requests and never reaches for a *core.Agent. That was the last thing the TUI
// read one for.
func (d *BtwDialog) Open(th tui.Theme, asker SideChatAsker, cwd, seed string, invalidate func()) {
	d.mu.Lock()
	d.active = true
	d.theme = th
	d.turns = nil
	d.loading = false
	d.cancel = nil
	d.editor = tui.NewEditor(th.AccentBar(th.Accent))
	d.asker = asker
	d.cwd = cwd
	d.mu.Unlock()

	if seed = strings.TrimSpace(seed); seed != "" {
		d.editor.SetValue(seed)
		d.submit(invalidate)
	}
}

// Close hides the dialog, cancels any in-flight request, and releases the
// asker's frozen snapshot.
func (d *BtwDialog) Close() {
	d.mu.Lock()
	d.active = false
	d.turns = nil
	d.editor = nil
	d.loading = false
	cancel := d.cancel
	d.cancel = nil
	asker := d.asker
	d.asker = nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if asker != nil {
		asker.Close()
	}
}

// HandleKey routes a keypress to the dialog. Returns true if the
// dialog wants the event consumed (always true while active, except
// for the special closing case where the caller may want to signal
// the parent).
func (d *BtwDialog) HandleKey(k tui.Key, invalidate func()) (closed bool) {
	if !d.Active() {
		return false
	}
	switch k.Kind {
	case tui.KeyEsc:
		// First esc: cancel an in-flight call. Subsequent esc closes.
		d.mu.Lock()
		busy := d.loading
		cancel := d.cancel
		d.mu.Unlock()
		if busy && cancel != nil {
			cancel()
			return false
		}
		d.Close()
		invalidate()
		return true
	}

	d.mu.Lock()
	editor := d.editor
	loading := d.loading
	cwd := d.cwd
	d.mu.Unlock()
	if editor == nil {
		return false
	}
	// Tab-complete a path-like token before the editor sees the key,
	// matching the main editor's behaviour.
	if k.Kind == tui.KeyTab {
		if widgets.TryPathTabCompleteEditor(editor, cwd) {
			invalidate()
			return false
		}
	}
	// Don't accept new submissions while a call is in flight; arrow
	// keys / scrolling still flow through to the editor for caret
	// movement and history.
	submitted := editor.HandleKey(k)
	invalidate()
	if submitted && !loading {
		d.submit(invalidate)
	}
	return false
}

// submit fires the LLM call for the current input and, on success,
// appends a new turn to d.turns. invalidate is called every time
// the turn's visible state changes (text delta, error, complete)
// so the host redraw loop picks up the update without relying on
// a periodic tick.
func (d *BtwDialog) submit(invalidate func()) {
	d.mu.Lock()
	if d.editor == nil || d.loading {
		d.mu.Unlock()
		return
	}
	question := strings.TrimSpace(d.editor.Value())
	if question == "" {
		d.mu.Unlock()
		return
	}
	d.editor.Clear()
	d.loading = true
	if d.spin == nil {
		d.spin = widgets.NewSpinner(d.theme)
	} else {
		d.spin.Configure(d.theme)
	}
	d.spin.Start()
	d.turns = append(d.turns, btwTurn{User: question})
	turnIdx := len(d.turns) - 1

	// The side chat's own prior exchanges (completed turns only; the in-flight
	// one at turnIdx has no reply yet). The frozen main transcript lives with
	// the asker's snapshot — the dialog only carries what happened here.
	prior := make([]SideChatExchange, 0, turnIdx)
	for i := range turnIdx {
		if t := d.turns[i]; t.Assistant != "" {
			prior = append(prior, SideChatExchange{User: t.User, Assistant: t.Assistant})
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	asker := d.asker
	d.mu.Unlock()

	go func() {
		reply, err := asker.Ask(ctx, prior, question)
		errMsg := ""
		if err != nil && ctx.Err() == nil {
			errMsg = err.Error()
		}
		d.completeTurn(turnIdx, reply, errMsg)
		if invalidate != nil {
			invalidate()
		}
	}()
}

// completeTurn fills in the assistant text or error for the turn at
// idx and clears the loading state.
func (d *BtwDialog) completeTurn(idx int, assistant, errMsg string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if idx < 0 || idx >= len(d.turns) {
		return
	}
	d.turns[idx].Assistant = assistant
	d.turns[idx].Err = errMsg
	d.loading = false
	d.cancel = nil
}

// Render returns the side-chat panel lines. Called every frame
// while active.
func (d *BtwDialog) Render(th tui.Theme, width int) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.active {
		return nil
	}

	var out []string
	out = append(out, FrameHeaderColor(th, i18n.T("btw - side chat (esc closes; nothing is added to the main thread)"), width, th.Accent))

	if len(d.turns) == 0 && !d.loading {
		out = append(out, "  "+th.FG256(th.Muted, i18n.T("ask anything; replies stay private to this side chat.")))
	}

	for _, t := range d.turns {
		out = append(out, "")
		out = append(out, btwUserBubbleRows(th, t.User, width-2)...)
		if t.Assistant != "" {
			out = append(out, "")
			md := tui.RenderMarkdown(t.Assistant, th, width-4)
			for _, line := range strings.Split(md, "\n") {
				if len(line) > 0 && line[0] == tui.FlushLeftSentinel {
					line = line[1:]
				}
				out = append(out, "    "+line)
			}
		}
		if t.Err != "" {
			out = append(out, "    "+th.FG256(th.Error, "✖ "+t.Err))
		}
	}

	if d.loading && d.spin != nil {
		out = append(out, "")
		// Match the main chat busy prefix shape: spinner glyph,
		// rotating funny-line, elapsed seconds, then a muted hint
		// that esc cancels.
		prefix := fmt.Sprintf("%s %s - %s",
			th.FG256(th.Assistant, d.spin.Frame()),
			th.FG256(th.Assistant, d.spin.Message()),
			th.FG256(th.Muted, d.spin.Elapsed().String()),
		)
		out = append(out, "  "+prefix+"  "+th.FG256(th.Muted, i18n.T("(esc cancels)")))
	}

	out = append(out, "")
	if d.editor != nil {
		// Render at width-2 to match the two-cell left indent applied
		// below. CursorPos uses the same width so the reported cursor
		// column matches the wrapped layout shown on screen.
		edLines, _, _ := d.editor.Render(width - 2)
		for _, l := range edLines {
			// Indent the editor body so it lines up with the side-chat
			// content column. Editor's prompt already includes a left
			// marker, so just two cells of pad.
			out = append(out, "  "+l)
		}
		out = append(out, "") // breathing room between editor and frame rule
	}
	out = append(out, FrameRuleColor(th, width, th.Accent))
	return out
}

// CursorRow / CursorCol report where the dialog wants the terminal
// cursor placed within its render output, so the parent can position
// the actual terminal cursor on the editor input. Returns (-1, -1)
// when the dialog isn't active or has no editor.
func (d *BtwDialog) CursorPos(width int) (row, col int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.active || d.editor == nil {
		return -1, -1
	}
	// Reproduce render's structure to find where the editor sits.
	// Note: the parent (interactive.go) wraps every dialog with
	// PadDialogFrame. It only injects a blank row after the frame
	// header when Render did not already put one there. With existing
	// turns or a loading spinner, Render's first body row is already
	// blank, so counting an extra pad row would place the cursor one
	// row too low.
	editorOffset := 1 // header
	if len(d.turns) == 0 && !d.loading {
		editorOffset++ // padDialogFrame's post-header blank
		editorOffset++ // muted "ask anything..." line
	}
	for _, t := range d.turns {
		editorOffset++ // blank
		editorOffset += len(btwUserBubbleRows(d.theme, t.User, width-2))
		if t.Assistant != "" {
			editorOffset++ // blank
			editorOffset += len(strings.Split(tui.RenderMarkdown(t.Assistant, d.theme, width-4), "\n"))
		}
		if t.Err != "" {
			editorOffset++
		}
	}
	if d.loading {
		editorOffset++ // blank
		editorOffset++ // spinner line
	}
	editorOffset++ // pre-editor blank
	_, eRow, eCol := d.editor.Render(width - 2)
	return editorOffset + eRow, eCol + 2 /* matches render indent */
}

// btwUserBubbleRows renders a user message inside the /btw dialog
// using the same bubble layout the main chat uses (full-width tinted
// panel, left-edge ▌ bar, padding rows above and below). The frame
// padding is the caller's job; bubbleWidth is the available row
// width inside the frame.
func btwUserBubbleRows(th tui.Theme, text string, bubbleWidth int) []string {
	const leftGutter = 0
	const rightGutter = 2
	innerWidth := bubbleWidth - 2 - leftGutter - rightGutter // 2 = bar's two cells
	if innerWidth < 1 {
		innerWidth = 1
	}
	bar := th.BG(th.UserBubbleBG, th.FG256(th.Accent, "▌ "))
	row := func(content string) string {
		inner := strings.Repeat(" ", leftGutter) + content
		return "  " + bar + th.UserBubble(inner, bubbleWidth-2)
	}
	var bubble []string
	for _, l := range strings.Split(text, "\n") {
		// Plain WrapANSILine (not WrapANSILineKeepStyle) on purpose: each
		// wrapped piece is re-wrapped by row() -> th.UserBubble, which applies
		// the bubble FG+BG to the whole piece. The colour is re-established per
		// row by the bubble, so keep-style's carry-forward would be redundant
		// here — this caller frames each piece rather than emitting it directly.
		for _, w := range tui.WrapANSILine(l, innerWidth) {
			bubble = append(bubble, row(w))
		}
	}
	if len(bubble) == 0 {
		return nil
	}
	out := make([]string, 0, len(bubble)+2)
	out = append(out, row(""))
	out = append(out, bubble...)
	out = append(out, row(""))
	return out
}
