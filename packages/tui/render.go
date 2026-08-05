package tui

import (
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// runewidthRune reports the number of cells a rune occupies, pinned
// here so the renderer does not depend on the editor's helper.
func runewidthRune(r rune) int { return runewidth.RuneWidth(r) }

// Renderer paints terva's main-screen flow: chat lines are emitted
// once into the terminal's scrollback and the live bottom band
// (status + editor + dialogs) is diff-redrawn in place on each
// DrawLog call. Callers pass styled lines already wrapped to width.
type Renderer struct {
	out  io.Writer
	rows int // terminal rows
	cols int // terminal cols

	// Cursor position after last draw (for placing input cursor).
	cursorRow int
	cursorCol int

	// Main-screen flow renderer state. logLines is the full logical
	// buffer (chat + live bottom band) from the previous DrawLog call.
	// logViewportTop/logHardwareRow track where that logical buffer sits
	// in the terminal's visible viewport so we can diff safely, and bail
	// out to clear+replay when the diff would touch rows that are no
	// longer addressable.
	logChat        []string
	logBottom      []string
	logLines       []string
	logViewportTop int
	logHardwareRow int
	logInit        bool

	// keepScrollback is true when we must NOT emit \x1b[3J
	// (erase-in-display 3, "clear scrollback").
	//
	// VS Code's integrated terminal (xterm.js) interprets \x1b[3J
	// as "also snap the viewport to the top of the remaining
	// buffer." Once the user has reopened a terminal with VS
	// Code's persistent-sessions feature on, there is real
	// replayed scrollback above the live cursor, and the snap is
	// visible: the host scrollbar jumps to the top every time we
	// do a full repaint (first paint, Ctrl+L via Renderer.Clear,
	// any writeFull(true) shrink). On every other terminal we
	// tested (iTerm, Ghostty, Kitty, Alacritty, Apple Terminal)
	// \x1b[3J just drops scrollback rows without moving the
	// viewport, which is what we actually want.
	//
	// The trade-off when keepScrollback is true: stale terva frames
	// remain in scrollback above the live view, so scrolling up
	// in VS Code's terminal shows old (already-superseded) terva
	// output. That is strictly less disruptive than the
	// scrollbar yanking to top on every Ctrl+L, and it is a
	// limitation specific to VS Code's terminal that we have no
	// way to work around without breaking other terminals.
	keepScrollback bool

	// theme is optional renderer-level styling applied at the final
	// row-writing boundary. In particular, Theme.Background is painted
	// as a full-width row background without making every View renderer
	// know about terminal padding and reset semantics.
	theme Theme

	// truncCache memoises truncateToWidth output by line, so a redraw that
	// re-emits the (unchanged) transcript doesn't re-run width math on
	// every line — the dominant render cost in long, busy transcripts.
	// Keyed by the line string; truncCols records the width it was built
	// for, so a resize drops the whole cache. Only chat lines are cached
	// (the large, stable set); the small bottom band is truncated directly.
	truncCache map[string]string
	truncCols  int
}

// NewRenderer returns a renderer that writes to out.
//
// Detects VS Code's integrated terminal via $TERM_PROGRAM and, when
// detected, disables emission of \x1b[3J for the reasons documented
// on Renderer.keepScrollback. The env var is set by VS Code itself
// (and by Cursor, which forks VS Code's terminal — same xterm.js,
// same bug), so no user configuration is required.
func NewRenderer(out io.Writer) *Renderer {
	return &Renderer{
		out:            out,
		keepScrollback: os.Getenv("TERM_PROGRAM") == "vscode",
	}
}

// SetTheme updates renderer-level terminal styling. Changing the
// background affects every row, so cached frame state is invalidated.
func (r *Renderer) SetTheme(th Theme) {
	r.theme = th
	r.Invalidate()
}

// Resize tells the renderer the current terminal size.
//
// On a real size change we also issue a clear-screen so the next
// DrawLog starts from a blank slate. Without the clear, characters from the
// old (wider) layout linger past the new right edge and rows from
// before the new bottom hang around as garbage.
func (r *Renderer) ResetScrollRegion() {
	if r.out != nil {
		_, _ = io.WriteString(r.out, SeqResetScrollRegion)
	}
}

// Size returns the terminal dimensions the renderer is currently laid out
// for — the size the last Resize settled on. Callers use it to tell a real
// resize (dimensions changed) from a same-size SIGWINCH (a multiplexer
// reattach), which want different repaint handling.
func (r *Renderer) Size() (cols, rows int) { return r.cols, r.rows }

func (r *Renderer) Resize(cols, rows int) {
	if cols != r.cols || rows != r.rows {
		r.cols = cols
		r.rows = rows
		r.logChat = nil
		r.logBottom = nil
		r.logLines = nil
		r.logViewportTop = 0
		r.logHardwareRow = 0
		r.logInit = false
		if r.out != nil {
			if r.keepScrollback {
				// A resize is a discrete user action, like Ctrl+L: the
				// old-width frame must be fully purged or the in-place
				// viewport clear leaves the scrolled-away rows behind
				// (the user otherwise has to press Ctrl+L to fix it).
				// Emit \x1b[3J to drop the retained scrollback and
				// repaint clean, accepting VS Code's viewport-snap the
				// same way Clear does.
				_, _ = io.WriteString(r.out, SeqDeleteKittyImages+SeqClearScreen+SeqClearScrollback+MoveTo(1, 1))
			} else {
				// Clear both screen and (where safe) scrollback so stale
				// content from the old width doesn't bleed through. Move
				// to (1,1) so the next DrawLog/writeFull starts from a
				// clean slate. Use the no-home variant: the explicit
				// MoveTo below sets the cursor without triggering VS
				// Code's viewport-snap.
				_, _ = io.WriteString(r.out, SeqDeleteKittyImages+SeqClearScreenNoHome+r.clearScrollbackSeq()+MoveTo(1, 1))
			}
		}
	}
}

// Clear forces a full repaint on the next DrawLog and clears the
// screen plus scrollback. In main-screen flow mode this is required whenever
// already-emitted transcript layout changes (for example ctrl+o
// expand/collapse), because terminal scrollback cannot be edited
// reliably once printed.
func (r *Renderer) Clear() {
	r.logChat = nil
	r.logBottom = nil
	r.logLines = nil
	r.logViewportTop = 0
	r.logHardwareRow = 0
	r.logInit = false
	if r.keepScrollback {
		// On VS Code's xterm.js the transcript is taller than the
		// viewport, so an in-place clear (home + erase-to-end) only
		// wipes the visible rows: the part that scrolled above the
		// viewport stays in the retained buffer and the next full
		// repaint stacks a duplicate above the live frame.
		//
		// Clear() is an explicit user refresh (Ctrl+L), so here we do
		// emit \x1b[3J to actually drop that scrollback, then home and
		// repaint. This is the one place we accept VS Code's
		// viewport-snap, because the user asked for a clean screen.
		// (Implicit repaints, e.g. closing a dialog, deliberately
		// avoid this path so they never snap the viewport.)
		if r.out != nil {
			_, _ = io.WriteString(r.out, SeqDeleteKittyImages+SeqClearScreen+SeqClearScrollback+MoveTo(1, 1))
		}
		return
	}
	// nil out = headless (tests, print modes): state reset alone is the
	// whole job, same guard the other emit paths keep.
	if r.out != nil {
		_, _ = io.WriteString(r.out, SeqDeleteKittyImages+SeqClearScreenNoHome+r.clearScrollbackSeq()+MoveTo(1, 1))
	}
}

// clearScrollbackSeq returns the scrollback-clear escape, or the
// empty string when we are running under a terminal where emitting
// it has user-visible side effects (see Renderer.keepScrollback).
// Callers concatenate this into a larger control sequence; an empty
// return value is a no-op there.
func (r *Renderer) clearScrollbackSeq() string {
	if r.keepScrollback {
		return ""
	}
	return SeqClearScrollback
}

// KeepsScrollback reports whether this renderer suppresses the
// scrollback-clear escape (true under VS Code's terminal). Callers
// use it to pick a viewport-safe full repaint (Invalidate) over a
// scrollback-clearing one (Clear) when redrawing overlays.
func (r *Renderer) KeepsScrollback() bool { return r.keepScrollback }

// Invalidate forces a full repaint on the next DrawLog without
// clearing the whole terminal first. Useful when the cached diff is
// unreliable but a visible full-screen flash would be too distracting.
func (r *Renderer) Invalidate() {
	r.logLines = nil
}

// containsImageEscape reports whether the line carries an inline-image
// escape we must repaint rather than diff against the previous frame.
func containsImageEscape(s string) bool {
	return strings.Contains(s, "\x1b]1337;File=") || strings.Contains(s, "\x1b_G")
}

// paintBackgroundRow applies the optional theme background to a single
// already-truncated terminal row. It pads with spaces to cols so the
// background reaches the right edge, and re-applies the background
// after full SGR resets inside the row so local styling does not punch
// transparent holes through the global tint.
func paintBackgroundRow(line string, cols int, th Theme) string {
	bg := th.BackgroundStyle()
	if bg == "" || cols <= 0 || containsImageEscape(line) {
		return line
	}
	line = strings.ReplaceAll(line, reset, reset+bg)
	if w := visibleWidth(line); w < cols {
		line += strings.Repeat(" ", cols-w)
	}
	return bg + line + reset
}

// truncateToWidth clips s so its on-screen width doesn't exceed cols
// cells, preserving ANSI CSI escape sequences (which don't consume
// cells). Lines carrying an inline-image escape are returned as-is
// since we can't measure their painted size.
//
// Fast path: a byte-length <= cols is a conservative upper bound
// guaranteeing the cell width is also <= cols, so we skip all the
// rune-width math. That covers the vast majority of lines in a
// transcript (narrow terminals wrap early; wide ones leave headroom).
func truncateToWidth(s string, cols int) string {
	if cols <= 0 || containsImageEscape(s) {
		return s
	}
	if len(s) <= cols {
		return s
	}
	var out strings.Builder
	out.Grow(len(s))
	seen := 0
	// Walk by byte index with utf8.DecodeRuneInString instead of
	// materialising []rune(s): the rune-slice allocation was a measurable
	// share of redraw CPU, and only the width of each rune is needed, not
	// random access. ANSI escapes are pure ASCII, so they're matched and
	// copied by byte without decoding.
	for i := 0; i < len(s); {
		// CSI escape sequence (ESC [ ... final): zero-width.
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			out.WriteByte(s[i])
			out.WriteByte(s[i+1])
			i += 2
			for i < len(s) {
				c := s[i]
				out.WriteByte(c)
				i++
				if c >= 0x40 && c <= 0x7e {
					break
				}
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runewidthRune(r)
		if seen+rw > cols {
			// Flush any trailing ANSI escapes (resets, erase-to-EOL)
			// so background colors and cleanup sequences survive.
			for i < len(s) {
				if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
					out.WriteByte(s[i])
					out.WriteByte(s[i+1])
					i += 2
					for i < len(s) {
						c := s[i]
						out.WriteByte(c)
						i++
						if c >= 0x40 && c <= 0x7e {
							break
						}
					}
				} else {
					break
				}
			}
			break
		}
		if r == utf8.RuneError && size == 1 {
			// Invalid byte: emit U+FFFD, matching the old []rune(s) path
			// (which mapped each bad byte to the replacement rune).
			out.WriteRune(utf8.RuneError)
		} else {
			out.WriteString(s[i : i+size])
		}
		seen += rw
		i += size
	}
	return out.String()
}

// truncCacheMax bounds truncCache. A transcript can present more distinct
// long lines than we want to retain; past the cap we drop and rebuild,
// which at worst recomputes — the pre-cache behaviour.
const truncCacheMax = 8192

// truncateCached is truncateToWidth with a per-line memo (truncCache).
// The cheap exits mirror truncateToWidth so short lines — which the byte
// fast path already returns for free — never pay a map probe.
func (r *Renderer) truncateCached(line string) string {
	if r.cols <= 0 || len(line) <= r.cols {
		return line
	}
	if r.truncCache == nil || r.truncCols != r.cols {
		r.truncCache = make(map[string]string)
		r.truncCols = r.cols
	}
	if t, ok := r.truncCache[line]; ok {
		return t
	}
	if len(r.truncCache) >= truncCacheMax {
		r.truncCache = make(map[string]string)
	}
	t := truncateToWidth(line, r.cols)
	r.truncCache[line] = t
	return t
}

// DrawLog renders terva in the terminal's main screen as normal terminal
// flow rather than a fixed full-screen frame. Chat lines are emitted once
// into the host terminal scrollback; the current bottom block (dialogs,
// slash popup, status, editor) is erased and redrawn in place at the end.
//
// cursorBottomRow/cursorCol are offsets into bottom, not the full frame.
func (r *Renderer) DrawLog(chat, bottom []string, cursorBottomRow, cursorCol int) {
	if r.cols == 0 || r.rows == 0 {
		return
	}
	if len(bottom) == 0 {
		bottom = []string{""}
	}
	chatFrame := make([]string, len(chat))
	for i, line := range chat {
		chatFrame[i] = paintBackgroundRow(r.truncateCached(line), r.cols, r.theme)
	}
	bottomFrame := make([]string, len(bottom))
	for i, line := range bottom {
		bottomFrame[i] = paintBackgroundRow(truncateToWidth(line, r.cols), r.cols, r.theme)
	}

	// Always reserve one real row below the editor/status band. This is
	// renderer-owned (not a best-effort trailing blank in the caller's
	// bottom block), so the logical-buffer diff keeps it visible and cursor
	// placement remains relative to the editor itself.
	const bottomMarginRows = 1
	lines := make([]string, 0, len(chatFrame)+len(bottomFrame)+bottomMarginRows)
	lines = append(lines, chatFrame...)
	lines = append(lines, bottomFrame...)
	for range bottomMarginRows {
		lines = append(lines, paintBackgroundRow("", r.cols, r.theme))
	}
	// In main-screen flow mode terva normally emits only its logical
	// content rows and leaves the rest of the terminal viewport alone.
	// When a theme background is configured, fill that otherwise-idle
	// space with painted blank rows so the full window is tinted while
	// keeping the scrollback-oriented renderer model unchanged for the
	// default transparent case.
	if r.theme.Background != nil {
		for len(lines) < r.rows {
			lines = append(lines, paintBackgroundRow("", r.cols, r.theme))
		}
	}
	if len(lines) == 0 {
		lines = []string{""}
	}

	cursorTargetRow := -1
	if cursorBottomRow >= 0 && cursorBottomRow < len(bottomFrame) {
		cursorTargetRow = len(chatFrame) + cursorBottomRow
	}

	// Idle no-op fast path. When the buffer AND the cursor position
	// haven't changed since the last DrawLog, emit nothing. The
	// alternative — always writing SeqHideCursor + cursor-position +
	// SeqShowCursor — resets the terminal's cursor blink timer on
	// every tick. At our 120ms animation cadence that means the
	// caret in an idle dialog editor (e.g. an open swarm transcript
	// for an agent that's currently idle) appears as a solid block
	// that never blinks, because we keep "showing" it before the
	// terminal can blink it off. Bailing out here lets the OS run
	// its blink cycle.
	if r.logInit && cursorBottomRow == r.cursorRow && cursorCol == r.cursorCol && sameLines(lines, r.logLines) {
		return
	}

	var w strings.Builder
	w.WriteString(SeqSynchronizedOn)
	w.WriteString(SeqHideCursor)

	var writeFullMode func(clear, keepHistory bool)

	// keepHistory repaints the screen WITHOUT dropping scrollback, on every
	// terminal. It is the VS Code path applied deliberately: the caller is
	// recovering from a state it cannot patch, and wiping the user's history
	// and text selection is a heavier price than leaving a superseded frame
	// above the live view.
	writeFullKeepingHistory := func() { writeFullMode(true, true) }
	writeFull := func(clear bool) { writeFullMode(clear, false) }
	writeFullMode = func(clear, keepHistory bool) {
		if clear {
			w.WriteString(SeqDeleteKittyImages)
			if r.keepScrollback || keepHistory {
				// VS Code's xterm.js scrolls the visible content up into
				// scrollback on \x1b[2J, which duplicates the frame (the
				// old paint stays above the new one). Home to the
				// viewport top and erase-to-end (\x1b[0J) instead: that
				// clears the visible screen in place without pushing the
				// previous frame into scrollback. We still cannot drop
				// existing scrollback (\x1b[3J snaps the viewport there),
				// but a full repaint no longer stacks a fresh copy below
				// the old one.
				w.WriteString(SeqCursorHome)
				w.WriteString(SeqClearToEnd)
			} else {
				w.WriteString(SeqClearScreenNoHome)
				w.WriteString(r.clearScrollbackSeq())
				w.WriteString(MoveTo(1, 1))
			}
		}
		for idx, line := range lines {
			if idx > 0 {
				w.WriteString("\r\n")
			}
			w.WriteString("\x1b[0m")
			w.WriteString(SeqClearLine)
			w.WriteString(line)
		}
		r.logHardwareRow = len(lines) - 1
		r.logViewportTop = len(lines) - r.rows
		if r.logViewportTop < 0 {
			r.logViewportTop = 0
		}
	}

	moveToLogicalRow := func(targetRow int) {
		if targetRow < 0 {
			targetRow = 0
		}
		if targetRow >= len(lines) {
			targetRow = len(lines) - 1
		}
		viewportBottom := r.logViewportTop + r.rows - 1
		if targetRow > viewportBottom {
			currentScreenRow := r.logHardwareRow - r.logViewportTop
			if currentScreenRow < 0 {
				currentScreenRow = 0
			}
			if currentScreenRow >= r.rows {
				currentScreenRow = r.rows - 1
			}
			moveToBottom := r.rows - 1 - currentScreenRow
			if moveToBottom > 0 {
				w.WriteString("\x1b[" + itoa(moveToBottom) + "B")
			}
			scroll := targetRow - viewportBottom
			for s := 0; s < scroll; s++ {
				w.WriteString("\r\n")
			}
			r.logViewportTop += scroll
			r.logHardwareRow = targetRow
			return
		}
		currentScreenRow := r.logHardwareRow - r.logViewportTop
		targetScreenRow := targetRow - r.logViewportTop
		lineDiff := targetScreenRow - currentScreenRow
		if lineDiff > 0 {
			w.WriteString("\x1b[" + itoa(lineDiff) + "B")
		} else if lineDiff < 0 {
			w.WriteString("\x1b[" + itoa(-lineDiff) + "A")
		}
		r.logHardwareRow = targetRow
	}

	positionCursor := func() {
		if cursorTargetRow < 0 || cursorTargetRow >= len(lines) {
			return
		}
		moveToLogicalRow(cursorTargetRow)
		w.WriteString("\r")
		if cursorCol > 0 {
			w.WriteString("\x1b[" + itoa(cursorCol) + "C")
		}
		w.WriteString(SeqShowCursor)
	}

	// Selection-highlight workaround removed: it could mis-invalidate
	// user-bubble padding rows whose colored bg made botHasHL trip,
	// causing the next diff pass to leave those rows visually thinned
	// because the cached entry was the \x00 sentinel rather than the
	// real previous bg-colored row.

	full := !r.logInit || len(r.logLines) == 0
	if full {
		writeFull(true)
		r.logInit = true
	} else {
		firstChanged, lastChanged := diffRows(r.logLines, lines, 0)

		// A change ABOVE the viewport used to force writeFull(true), which is
		// the most expensive answer available and destroys user state: on a
		// normal terminal it emits \x1b[3J, wiping scrollback and any native
		// text selection; on VS Code (keepScrollback) it cannot clear, so the
		// whole transcript is pushed into scrollback a second time.
		//
		// It is also unnecessary. Rows above logViewportTop are unreachable,
		// but the terminal's PHYSICAL rows do not move just because our
		// logical numbering shifted: the row that displayed old index j still
		// displays it. Renumbering the cached model by the height change
		// restores the index-to-physical-row mapping, and the ordinary patch
		// path below can then repaint only what actually differs on screen.
		//
		// Measured on six real recorded sessions: this branch fires 1-8 times
		// per session (0.2-1.4% of draws), every single occurrence a height
		// change — so this is the case worth handling, not a rare corner.
		// The rebase below answers exactly one question: "chat above the
		// viewport changed height, so where did the rows move to?" It is only
		// applicable when the change IS in chat.
		//
		// A change in the bottom band can also land above logViewportTop —
		// typing "/" opens a slash popup listing every command, and when that
		// band is taller than the screen the chat ends above the viewport top.
		// Rebasing then applies a chat delta (often 0) to popup rows and marks
		// them accepted, so rows that were never painted are recorded as
		// painted: the typed text is invisible and the cursor sits a row above
		// the composer until some later keystroke happens to dirty a row below
		// the anchor. Shipped exactly that, and it made slash commands
		// unusable.
		aboveViewportInChat := firstChanged >= 0 &&
			firstChanged < r.logViewportTop &&
			firstChanged < len(chatFrame)
		if aboveViewportInChat {
			if r.rebaseForAboveViewportChange(lines, chatFrame) {
				// Re-diff in the rebased coordinates, over rows that can
				// actually be repainted. firstChanged is now >= logViewportTop
				// by construction.
				firstChanged, lastChanged = diffRows(r.logLines, lines, r.logViewportTop)
			} else {
				// The shift does not preserve the anchor, so the cached model
				// cannot describe the physical screen any more and the frame
				// has to be repainted. Keep the user's scrollback while doing
				// it: they did not ask for history to be dropped, they toggled
				// a display mode. A superseded frame left above the live view
				// is the same trade-off this renderer already accepts on VS
				// Code, and it is far cheaper than losing scrollback and any
				// text selection in it.
				writeFullKeepingHistory()
				firstChanged, lastChanged = -1, -1
			}
		}

		if firstChanged >= 0 && firstChanged < r.logViewportTop {
			// Still above the viewport and NOT a chat height change, so there
			// is nothing to renumber — the rows simply cannot be patched.
			// Repaint, keeping the user's scrollback.
			writeFullKeepingHistory()
			firstChanged, lastChanged = -1, -1
		}

		if firstChanged == -1 {
			// No content changes; the final cursor positioning below may still
			// move the hardware cursor if the editor cursor changed.
		} else {
			prevViewportTop := r.logViewportTop
			viewportTop := prevViewportTop
			hardwareRow := r.logHardwareRow
			prevViewportBottom := prevViewportTop + r.rows - 1
			appendStart := len(lines) > len(r.logLines) && firstChanged == len(r.logLines) && firstChanged > 0
			moveTarget := firstChanged
			if appendStart {
				moveTarget = firstChanged - 1
			}

			if moveTarget > prevViewportBottom {
				currentScreenRow := hardwareRow - prevViewportTop
				if currentScreenRow < 0 {
					currentScreenRow = 0
				}
				if currentScreenRow >= r.rows {
					currentScreenRow = r.rows - 1
				}
				moveToBottom := r.rows - 1 - currentScreenRow
				if moveToBottom > 0 {
					w.WriteString("\x1b[" + itoa(moveToBottom) + "B")
				}
				scroll := moveTarget - prevViewportBottom
				for s := 0; s < scroll; s++ {
					w.WriteString("\r\n")
				}
				prevViewportTop += scroll
				viewportTop += scroll
				hardwareRow = moveTarget
			}

			currentScreenRow := hardwareRow - prevViewportTop
			targetScreenRow := moveTarget - viewportTop
			lineDiff := targetScreenRow - currentScreenRow
			if lineDiff > 0 {
				w.WriteString("\x1b[" + itoa(lineDiff) + "B")
			} else if lineDiff < 0 {
				w.WriteString("\x1b[" + itoa(-lineDiff) + "A")
			}
			if appendStart {
				w.WriteString("\r\n")
			} else {
				w.WriteString("\r")
			}

			renderEnd := lastChanged
			if renderEnd >= len(lines) {
				renderEnd = len(lines) - 1
			}
			for idx := firstChanged; idx <= renderEnd; idx++ {
				if idx > firstChanged {
					w.WriteString("\r\n")
				}
				w.WriteString("\x1b[0m")
				w.WriteString(SeqClearLine)
				w.WriteString(lines[idx])
			}
			finalRow := renderEnd
			if len(r.logLines) > len(lines) {
				extra := len(r.logLines) - len(lines)
				if extra > r.rows {
					writeFull(true)
				} else {
					for e := 0; e < extra; e++ {
						w.WriteString("\x1b[1B")
						w.WriteString("\r")
						w.WriteString("\x1b[0m")
						w.WriteString(SeqClearLine)
						finalRow++
					}
					if extra > 0 {
						w.WriteString("\x1b[" + itoa(extra) + "A")
						finalRow -= extra
					}
				}
			}
			r.logHardwareRow = finalRow
			r.logViewportTop = viewportTop
			if minTop := r.logHardwareRow - r.rows + 1; minTop > r.logViewportTop {
				r.logViewportTop = minTop
			}
			if r.logViewportTop < 0 {
				r.logViewportTop = 0
			}
		}
	}

	positionCursor()
	w.WriteString(SeqSynchronizedOff)
	_, _ = io.WriteString(r.out, w.String())

	r.logChat = append(r.logChat[:0], chatFrame...)
	r.logBottom = append(r.logBottom[:0], bottomFrame...)
	r.logLines = append(r.logLines[:0], lines...)
	r.cursorRow = cursorBottomRow
	r.cursorCol = cursorCol
}

// TeardownLog erases the live bottom band (status bar + input editor +
// reserved margin) that DrawLog paints below the conversation, leaving
// the chat transcript untouched in scrollback and the cursor parked on a
// fresh line right after it.
//
// This is the exit counterpart to DrawLog. terva stays on the terminal's
// main screen and emits chat as ordinary scrollback, so on quit we must
// not clear the screen (that would wipe the conversation the user just
// had) but we also can't leave the transient input/status frame sitting
// under the returning shell prompt. Moving to the first row of the bottom
// band and erasing to the end of the screen drops exactly the live chrome
// and nothing above it.
//
// No-op before the first DrawLog (nothing was painted). Uses only the
// tracked hardware-cursor/viewport state, so it works whether the last
// frame ended via the full-repaint or incremental-diff path.
func (r *Renderer) TeardownLog() {
	if r.out == nil || !r.logInit {
		return
	}
	// First row of the bottom band in the logical buffer. The band is
	// anchored at the bottom of the viewport, so this is at or above the
	// current hardware cursor; clamp into the visible viewport in case a
	// tall (scrolled) chat pushed the chat start above the viewport top.
	target := len(r.logChat)
	if target < r.logViewportTop {
		target = r.logViewportTop
	}
	var w strings.Builder
	w.WriteString(SeqHideCursor)
	if diff := target - r.logHardwareRow; diff > 0 {
		w.WriteString("\x1b[" + itoa(diff) + "B")
	} else if diff < 0 {
		w.WriteString("\x1b[" + itoa(-diff) + "A")
	}
	w.WriteString("\r")
	w.WriteString(SeqEraseToEnd)
	w.WriteString(SeqShowCursor)
	_, _ = io.WriteString(r.out, w.String())
	// Force a clean full repaint if anything draws after teardown (e.g. a
	// late async redraw racing the exit); the band is gone now.
	r.logInit = false
	r.logLines = nil
}

// sameLines reports whether two []string have the exact same
// length and per-row contents. Used by DrawLog's idle no-op fast
// path; cheap enough at our frame rates and far simpler than
// hashing every byte.
func sameLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// diffRows reports the first and last index at or after `from` where the
// cached buffer and the new frame disagree, or (-1, -1) when they match.
//
// Extracted so it can run twice per draw: once over the whole buffer to find
// any change, and again after a rebase over only the addressable rows. A
// second inline copy is how the two would drift.
func diffRows(cached, next []string, from int) (first, last int) {
	first, last = -1, -1
	maxLines := len(next)
	if len(cached) > maxLines {
		maxLines = len(cached)
	}
	if from < 0 {
		from = 0
	}
	for idx := from; idx < maxLines; idx++ {
		cachedLine := ""
		if idx < len(cached) {
			cachedLine = cached[idx]
		}
		nextLine := ""
		if idx < len(next) {
			nextLine = next[idx]
		}
		if cachedLine != nextLine {
			if first == -1 {
				first = idx
			}
			last = idx
		}
	}
	// The buffer grew but the appended rows compare equal to the implicit ""
	// past the old end. The loop cannot flag those, yet the renderer still has
	// to advance its hardware-cursor / viewport tracking past them so the next
	// draw starts from the right place. Treat the extension as changed.
	if len(next) > len(cached) {
		if first == -1 {
			first = len(cached)
			if first < from {
				first = from
			}
		}
		if last < len(next)-1 {
			last = len(next) - 1
		}
	}
	return first, last
}

// rebaseForAboveViewportChange renumbers the cached logical buffer after chat
// above the viewport changed height, so index-based patching keeps addressing
// the physical rows it means to.
//
// The whole argument rests on one fact about terminals: a logical renumbering
// moves nothing. The physical row that displayed cached index j still displays
// exactly that text. So shifting our own indices by the chat delta restores
// the mapping, and no content has to be re-emitted.
//
// The delta comes from CHAT, not from len(lines): the bottom band is anchored
// to the end of chat, and `lines` additionally carries a reserved margin row
// and (with a themed background) padding up to the viewport height, neither of
// which shifts in step with a chat height change.
// It reports false when the shift cannot be applied, and the caller must fall
// back to a full repaint.
//
// The invariant only holds while the viewport anchor survives the shift. A
// shrink big enough to push the anchor above the start of the buffer (or a
// grow that pushes it past the end) means the content no longer covers the
// physical region it used to, and stale rows are left below the new end with
// no sound way to address them. Clamping the anchor instead of refusing is
// exactly the bug this guard exists to prevent: it silently breaks
// `screenRow = logHardwareRow - logViewportTop`, so the cursor lands in the
// wrong place and the new frame paints BELOW the old content rather than over
// it. Dismissing a help block taller than the screen does this, and
// TestInteractiveHelpBlock caught it.
func (r *Renderer) rebaseForAboveViewportChange(lines, chatFrame []string) bool {
	delta := len(chatFrame) - len(r.logChat)

	newTop := r.logViewportTop + delta
	if newTop < 0 || newTop > len(lines) {
		return false
	}
	rebased := make([]string, len(lines))

	for idx := range rebased {
		switch {
		case idx < newTop:
			// Unreachable: the terminal scrolled these into scrollback and we
			// cannot repaint them. Record the NEW text even though the screen
			// still shows the old — deliberately, and it is the only lie in
			// here. Keeping the stale text would make every later draw find a
			// change above the viewport and rebase (or, before this, full
			// repaint) forever, for rows no one can fix. The stale rows remain
			// in scrollback either way; that is the cost this whole branch is
			// choosing over wiping the user's scrollback and selection.
			if idx < len(lines) {
				rebased[idx] = lines[idx]
			}
		case idx < len(chatFrame):
			// Chat: shifted wholesale by delta.
			if old := idx - delta; old >= 0 && old < len(r.logLines) {
				rebased[idx] = r.logLines[old]
			}
		default:
			// Bottom band, margin and background fill: positioned relative to
			// the END of chat, so their offset from that boundary is what is
			// preserved, not their absolute index.
			if old := len(r.logChat) + (idx - len(chatFrame)); old >= 0 && old < len(r.logLines) {
				rebased[idx] = r.logLines[old]
			}
		}
	}

	r.logLines = rebased
	// Shift the anchor and the cursor by the SAME delta, which is what keeps
	// screenRow = logHardwareRow - logViewportTop invariant. That equality is
	// the whole point: it is how every later move is translated into physical
	// cursor motion. Because newTop was not clamped above, this holds exactly.
	r.logViewportTop = newTop
	r.logHardwareRow += delta
	if r.logHardwareRow < 0 {
		r.logHardwareRow = 0
	}
	if maxRow := len(lines) - 1; r.logHardwareRow > maxRow && maxRow >= 0 {
		r.logHardwareRow = maxRow
	}
	return true
}
