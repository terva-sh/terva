package tui

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// Editor is a simple multi-line text editor for the input area.
//
// Users can type, paste, move the cursor, and submit. The editor
// exposes its rendered height and the current cursor row/col for the
// outer renderer. It does NOT draw itself directly; instead, Render()
// returns the visible lines.
type Editor struct {
	// Lines is the current buffer, one entry per line.
	Lines    []string
	CursorR  int // row index into Lines
	CursorC  int // rune index into Lines[CursorR]
	Prompt   string
	MaxWidth int

	// Mask, when non-zero, replaces every rune of the buffer with it at RENDER
	// time. The buffer itself is untouched, so Value and SubmitValue return the
	// real text and nothing else has to know.
	//
	// It exists because the login dialog had no way to hide an API key. The web
	// client rendered the SAME AuthField descriptor as <input type="password">
	// and the TUI echoed it in the clear, on the surface most likely to be on a
	// shared screen or in a recording.
	//
	// One mask rune per real rune, so cursor arithmetic — which is rune-indexed
	// — is unchanged. A buffer of wide runes would render narrower than it
	// measures; an API key is not that, and hiding it matters more.
	Mask rune

	// lastRenderWidth is the column count passed to the most recent
	// Render() call. Up/Down key handling needs this to walk the
	// same visual layout the user sees: a logical line that wraps to
	// two rows should respond to Up by moving to the previous visual
	// row, not do nothing because CursorR is already 0.
	lastRenderWidth int

	// pastes stores the full content of every multi-line paste,
	// keyed by the id embedded in the visible placeholder token.
	// Pasted text is collapsed to "[paste #N +L lines]" in the
	// editor so a 500-line drop doesn't explode the input area;
	// SubmitValue() expands placeholders back to their real bodies
	// right before the prompt goes to the agent. The map is reset
	// on Clear() so stale pastes never leak into a follow-up turn.
	pastes   map[int]string
	pasteSeq int

	// files and dirs store full paths of drag-dropped items, keyed
	// by separate sequence ids. The editor shows compact chips like
	// [file:1:name] and [dir:1:name/] and SubmitValue() expands
	// them back to the full path.
	files   map[int]string
	fileSeq int
	dirs    map[int]string
	dirSeq  int

	// ghost is an offered next line the user has not accepted: rendered
	// where their text would go, never part of the buffer. Stage 2 of
	// docs/proposals/idle-suggestions.md.
	//
	// It is NOT a line in Lines, and that is the whole design. Value(),
	// SubmitValue(), IsEmpty() and every cursor computation are untouched by
	// it, so a suggestion the user ignores cannot be submitted, cannot be
	// walked into with the arrow keys, and cannot change where the caret
	// sits. Accepting it is the only thing that turns it into text.
	//
	// Shown only while the buffer is empty, which is what gives the proposal's
	// hold-and-restore behaviour for free: type and it is hidden behind your
	// own words, delete back to empty and it is there again. No timer, no
	// second slot, and nothing to re-fetch — a stored ghost that is merely not
	// currently drawn.
	ghost string

	// GhostStyle renders the ghost for display, dimming it so it reads as an
	// offer rather than as text already typed. nil renders it plain.
	//
	// A hook rather than a stored styled string because the ghost has to stay
	// PLAIN: accepting it inserts it into the buffer, and escape sequences in
	// the buffer would be submitted to the model and counted by every width
	// computation in this file.
	GhostStyle func(string) string
}

// NewEditor returns an empty editor with the given prompt.
func NewEditor(prompt string) *Editor {
	return &Editor{
		Lines:  []string{""},
		Prompt: prompt,
	}
}

// Value returns the buffer as a single string, WITHOUT expanding
// paste placeholders. Used for anything that should reflect what's
// visible on screen (history, slash-command detection, editor
// state). For the string that actually goes to the agent, use
// SubmitValue(), which expands each [paste #N +L lines] token
// back into the full pasted body.
func (e *Editor) Value() string { return strings.Join(e.Lines, "\n") }

// SubmitValue returns the buffer with every paste placeholder
// expanded to its stored body. Call once at submit time; the
// expansion is lossless (placeholders are only injected in
// HandleKey for KeyPaste with multi-line content).
//
// Expansion is non-destructive: the internal paste map isn't
// touched. Clear() is what resets both the placeholder text and
// the map, and the caller already calls Clear() right after
// reading SubmitValue() as part of the submit flow.
func (e *Editor) SubmitValue() string {
	raw := e.Value()
	if len(e.pastes) > 0 && strings.Contains(raw, "[pasted text #") {
		raw = expandPastePlaceholders(raw, e.pastes)
	}
	if (len(e.files) > 0 || len(e.dirs) > 0) && (strings.Contains(raw, "[file:") || strings.Contains(raw, "[dir:")) {
		raw = expandFilePlaceholders(raw, e.files, e.dirs)
	}
	return raw
}

// EditorState is a snapshot of the editor's complete buffer state: the
// visible lines, the cursor, and the hidden bodies behind paste and
// file/dir placeholders. State()/Restore() exist for callers that park
// a draft aside and bring it back later — the one round trip SetValue
// cannot make, because SetValue drops the placeholder maps (correctly:
// the placeholders they back are gone from the text it installs).
// Both directions deep-copy, so a snapshot never aliases live edits.
type EditorState struct {
	lines            []string
	cursorR, cursorC int
	pastes           map[int]string
	pasteSeq         int
	files            map[int]string
	fileSeq          int
	dirs             map[int]string
	dirSeq           int
}

// Value returns the snapshot's buffer as a single string, placeholder
// form — what was visible on screen when the snapshot was taken.
func (s EditorState) Value() string { return strings.Join(s.lines, "\n") }

// State captures the editor's current buffer, cursor, and placeholder
// bodies. The editor itself is untouched.
func (e *Editor) State() EditorState {
	return EditorState{
		lines:    append([]string(nil), e.Lines...),
		cursorR:  e.CursorR,
		cursorC:  e.CursorC,
		pastes:   copyIntMap(e.pastes),
		pasteSeq: e.pasteSeq,
		files:    copyIntMap(e.files),
		fileSeq:  e.fileSeq,
		dirs:     copyIntMap(e.dirs),
		dirSeq:   e.dirSeq,
	}
}

// Restore reinstates a snapshot taken by State, cursor position and
// placeholder bodies included.
//
// A snapshot carries no ghost, and restoring one drops any offer standing:
// State/Restore park the user's OWN writing and bring it back, and the user's
// writing outranks a suggestion every time it is a contest. The offer is
// discarded rather than parked, because it costs nothing to ask for another
// and it was never the user's text to keep.
func (e *Editor) Restore(s EditorState) {
	e.ghost = ""
	e.Lines = append([]string(nil), s.lines...)
	if len(e.Lines) == 0 {
		e.Lines = []string{""}
	}
	e.CursorR = clamp(s.cursorR, 0, len(e.Lines)-1)
	e.CursorC = clamp(s.cursorC, 0, runeLen(e.Lines[e.CursorR]))
	e.pastes = copyIntMap(s.pastes)
	e.pasteSeq = s.pasteSeq
	e.files = copyIntMap(s.files)
	e.fileSeq = s.fileSeq
	e.dirs = copyIntMap(s.dirs)
	e.dirSeq = s.dirSeq
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func copyIntMap(m map[int]string) map[int]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[int]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// SetValue replaces the buffer and places the cursor at the end.
// Also drops any stored pastes because the placeholders they back
// are now gone from the visible text.
func (e *Editor) SetValue(s string) {
	s = normalizeEditorText(s)
	e.Lines = strings.Split(s, "\n")
	if len(e.Lines) == 0 {
		e.Lines = []string{""}
	}
	e.CursorR = len(e.Lines) - 1
	e.CursorC = runeLen(e.Lines[e.CursorR])
	e.pastes = nil
	e.pasteSeq = 0
	e.files = nil
	e.fileSeq = 0
	e.dirs = nil
	e.dirSeq = 0
	// Whoever installs a buffer owns the composer, so the offer is over. This
	// covers submit and Clear as well, both of which come through here: a
	// suggestion must not outlive the message that was actually sent, because a
	// line proposed against a conversation that has since moved on is worse
	// than no line at all — it still looks current.
	e.ghost = ""
}

// Clear resets the buffer.
func (e *Editor) Clear() { e.SetValue("") }

// SetGhost offers text as the next line, or clears the offer when empty.
//
// Stored plain and flattened to a single line: it is drawn on one row, and
// accepting it must insert exactly what was shown.
func (e *Editor) SetGhost(text string) {
	text = strings.TrimSpace(strings.ReplaceAll(normalizeEditorText(text), "\n", " "))
	e.ghost = text
}

// Ghost returns the offered line, or "" when there is none. It reports what is
// HELD, not what is currently drawn — the two differ while the user is typing
// over it.
func (e *Editor) Ghost() string { return e.ghost }

// ghostVisible reports whether the offer is currently on screen. Held is not
// shown: an offer waits behind whatever the user is writing.
func (e *Editor) ghostVisible() bool { return e.ghost != "" && e.IsEmpty() }

// GhostVisible reports whether an offer is on screen right now, as opposed to
// merely held.
//
// Exported because the host intercepts some keys before the editor ever sees
// them, and one of those keys accepts the offer. An interceptor that has to
// decide whether to yield must ask the SAME question Render asks — not a
// lookalike built from Ghost() and IsEmpty(), which is the version that drifts
// the first time either rule changes.
func (e *Editor) GhostVisible() bool { return e.ghostVisible() }

// AcceptGhost turns a visible offer into ordinary text and reports whether it
// did. The offer is consumed either way it goes from here: accepted it becomes
// the user's own line to edit or send, and there is nothing left to re-offer.
//
// Refuses while the buffer is non-empty, which is the same rule as drawing it.
// Accepting an offer the user cannot see would insert text they did not know
// was on hand.
func (e *Editor) AcceptGhost() bool {
	if !e.ghostVisible() {
		return false
	}
	text := e.ghost
	e.ghost = ""
	e.insert(text)
	return true
}

// IsEmpty reports whether the buffer has no visible content.
func (e *Editor) IsEmpty() bool {
	return len(e.Lines) == 1 && e.Lines[0] == ""
}

// HandleKey applies k to the editor. It returns submit=true when
// the user pressed enter and there is content to send; the caller
// should read SubmitValue() and then Clear().
func (e *Editor) HandleKey(k Key) (submit bool) {
	switch k.Kind {
	case KeyTab:
		// Tab accepts a visible offer, and Tab is where the decision was made
		// to put it: Enter on an empty composer is a reflex, so an offer that
		// arrived in the gap between the reflex starting and finishing would
		// send the model's idea as the user's message. One extra keystroke
		// buys no accidental sends, and a chance to edit the wording first.
		//
		// With no offer showing this stays the no-op Tab has always been in
		// the editor, so nothing that reaches here today changes.
		e.AcceptGhost()
	case KeyRune:
		if k.Alt && (k.Rune == '\r' || k.Rune == '\n') {
			e.newline()
			return false
		}
		e.insert(string(k.Rune))
	case KeyEnter:
		// Any modified Enter inserts a newline; a bare Enter submits.
		// Shift+Enter and Alt+Enter only reach us on terminals that
		// speak the kitty keyboard protocol or xterm modifyOtherKeys —
		// Ctrl covers ctrl+j, which is a plain 0x0a byte and therefore
		// works everywhere (see the Reader's LF case).
		if k.Shift || k.Alt || k.Ctrl {
			e.newline()
			return false
		}
		// Last-resort chord for terminals that report none of the
		// above: a lone backslash immediately before the cursor turns
		// Enter into a newline and is consumed, shell-style. An escaped
		// backslash ("\\") is left alone and submits, so text that
		// genuinely ends in a backslash is still reachable.
		if e.trailingLineContinuation() {
			e.backspace()
			e.newline()
			return false
		}
		return true
	case KeyBackspace:
		if k.Alt {
			// Alt+Backspace (Option+Delete on macOS) — delete previous word.
			e.deleteWord()
		} else {
			e.backspace()
		}
	case KeyDelete:
		e.delete()
	case KeyLeft:
		if k.Alt {
			e.moveWordLeft()
		} else {
			e.moveLeft()
		}
	case KeyRight:
		if k.Alt {
			// Alt+Right stays word navigation. Accepting is the PLAIN gesture,
			// which is what shell autosuggestion trained everyone to reach for.
			e.moveWordRight()
			break
		}
		// Right accepts a visible offer, the same as Tab. It costs nothing to
		// give it away: an offer is only ever drawn while the buffer is EMPTY,
		// and on an empty buffer moveRight has nothing to move toward — the
		// caret is at rune 0 of the only line, so both of its branches decline.
		// So this takes no movement away from the user; it fills in a keystroke
		// that was previously inert at exactly the moment an offer is showing.
		//
		// AcceptGhost self-guards on the same visibility rule that draws it,
		// so with nothing offered this falls straight through to the movement
		// it has always done.
		if e.AcceptGhost() {
			break
		}
		e.moveRight()
	case KeyUp:
		// Visual-row navigation. When a single logical line wraps
		// to several visual rows, Up needs to climb one visual
		// row — which may mean moving within the same
		// e.Lines[CursorR] back toward an earlier rune index,
		// not jumping to CursorR-1. Buffer-line navigation is
		// subsumed: a visual row above may also live in the
		// previous logical line when a short line precedes a
		// wrapped one.
		e.MoveVertical(-1)
	case KeyDown:
		e.MoveVertical(+1)
	case KeyHome, KeyCtrlA:
		e.CursorC = 0
	case KeyEnd, KeyCtrlE:
		e.CursorC = runeLen(e.Lines[e.CursorR])
	case KeyCtrlU:
		e.Lines[e.CursorR] = substringAfter(e.Lines[e.CursorR], e.CursorC)
		e.CursorC = 0
	case KeyCtrlK:
		e.Lines[e.CursorR] = substringBefore(e.Lines[e.CursorR], e.CursorC)
	case KeyCtrlW:
		e.deleteWord()
	case KeyPaste:
		paste := normalizeEditorText(k.Paste)
		// Large multi-line pastes are collapsed to a short
		// placeholder token so the editor doesn't balloon to
		// hundreds of rows. The full body is stashed in e.pastes,
		// keyed by a monotonically increasing id, and swapped
		// back in at submit time via SubmitValue. Threshold:
		// two or more newlines triggers collapse; one-liners and
		// drag-dropped file paths fall through to the original
		// insert path (including file-path quoting).
		if pasteShouldCollapse(paste) {
			if e.pastes == nil {
				e.pastes = map[int]string{}
			}
			e.pasteSeq++
			id := e.pasteSeq
			e.pastes[id] = paste
			e.insert(formatPastePlaceholder(id, paste))
		} else {
			// macOS Terminal / iTerm / Ghostty deliver drag-dropped
			// files as bracketed-paste text. Detect that pattern
			// and collapse long paths to a [file:basename] chip.
			inserted := e.collapseOrQuoteFilePaths(paste)
			e.insert(inserted)
		}
	case KeyEsc:
		e.Clear()
	}
	return false
}

// quotePastedFilePaths returns paste with any drag-dropped file
// paths wrapped in single quotes. Heuristics:
//
//   - one line only; multi-line text is returned unchanged so we
//     never touch a real paste of code.
//   - whitespace-separated tokens are inspected one by one. A token
//     that looks like a filesystem path (absolute, ~/relative, or
//     file:// URL) is normalised and re-quoted; everything else is
//     left exactly as the user dropped it.
//   - terminal-style backslash escapes ("foo\ bar.png") are folded
//     back to literal characters before quoting, since wrapping in
//     single quotes makes them unnecessary.
//   - file:// URLs are URL-decoded and stripped of the scheme so
//     the agent receives a plain filesystem path.
//   - if a path contains a literal single quote, it's escaped using
//     the standard '\” splice.
//
// Pastes that don't contain any path-shaped tokens are returned
// unchanged.
func quotePastedFilePaths(paste string) string {
	if paste == "" || strings.ContainsRune(paste, '\n') {
		return paste
	}
	trimmed := strings.TrimSpace(paste)
	if trimmed == "" {
		return paste
	}

	// Split on runs of whitespace, preserving the original separators
	// so multi-file drops keep their spacing on rebuild. Backslash
	// before a space is treated as part of the preceding token, since
	// macOS Terminal escapes spaces in dropped paths that way.
	tokens := splitPreservingSeparators(paste)
	// Bound the os.Stat calls normalisePathToken makes: a drag-drop
	// is a handful of paths, while prose or data can be ~100 tokens —
	// and each stat can block the UI on a slow network mount. More
	// path-shaped candidates than a plausible drop means this isn't
	// one; insert verbatim.
	const maxPathStatChecks = 16
	candidates := 0
	for _, tk := range tokens {
		if !tk.isSpace && pathShapedToken(tk.text) {
			candidates++
		}
	}
	if candidates > maxPathStatChecks {
		return paste
	}
	changed := false
	for i, tk := range tokens {
		if tk.isSpace {
			continue
		}
		if p, ok := normalisePathToken(tk.text); ok {
			tokens[i].text = singleQuote(p)
			changed = true
		}
	}
	if !changed {
		return paste
	}
	var out strings.Builder
	out.Grow(len(paste) + 8)
	for _, tk := range tokens {
		out.WriteString(tk.text)
	}
	return out.String()
}

type pasteToken struct {
	text    string
	isSpace bool
}

// splitPreservingSeparators splits s into runs of whitespace and
// runs of non-whitespace, keeping the separator runs intact so a
// rebuild via concatenation reproduces the original string exactly.
// A backslash immediately followed by a space ("\ ") is treated as
// part of the preceding non-whitespace run, since that's how macOS
// Terminal escapes spaces in drag-dropped file paths.
func splitPreservingSeparators(s string) []pasteToken {
	runes := []rune(s)
	var out []pasteToken
	var buf strings.Builder
	inSpace := false
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		out = append(out, pasteToken{text: buf.String(), isSpace: inSpace})
		buf.Reset()
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		isEscapedSpace := r == '\\' && i+1 < len(runes) && (runes[i+1] == ' ' || runes[i+1] == '\t')
		rSpace := !isEscapedSpace && (r == ' ' || r == '\t')
		if buf.Len() == 0 {
			inSpace = rSpace
		} else if rSpace != inSpace {
			flush()
			inSpace = rSpace
		}
		if isEscapedSpace {
			// Skip the backslash; emit the literal space as part of
			// this non-whitespace token. normalisePathToken's
			// unescapeBackslashes is now redundant for this path but
			// stays in case some other source escapes other chars.
			buf.WriteRune(runes[i+1])
			i++
			continue
		}
		buf.WriteRune(r)
	}
	flush()
	return out
}

// normalisePathToken decides whether tk is a drag-dropped path and,
// if so, returns the cleaned-up path string.
//
// To avoid mistaking URL path segments (e.g. "/de/downloads/foo"
// pasted from a browser address bar) for filesystem paths, the
// candidate must point to an entry that actually exists on disk.
// Drag-and-drop from a file manager always satisfies this; a
// hand-typed URL fragment doesn't, so it falls through to the
// regular insert path untouched.
func normalisePathToken(tk string) (string, bool) {
	// Strip pre-existing surrounding quotes; we'll re-quote consistently.
	if n := len(tk); n >= 2 {
		if (tk[0] == '\'' && tk[n-1] == '\'') || (tk[0] == '"' && tk[n-1] == '"') {
			tk = tk[1 : n-1]
		}
	}

	// file:// URL form: decode and strip the scheme.
	if strings.HasPrefix(tk, "file://") {
		decoded, err := url.PathUnescape(strings.TrimPrefix(tk, "file://"))
		if err != nil {
			return "", false
		}
		if decoded == "" || decoded[0] != '/' {
			return "", false
		}
		if !pathExists(decoded) {
			return "", false
		}
		return decoded, true
	}

	// Looks-like-a-path heuristic: starts with /, ~, ~/. Must contain
	// at least one path separator after the prefix (otherwise a bare
	// "/" or "~" gets quoted, which is never what the user meant).
	if !strings.HasPrefix(tk, "/") && !strings.HasPrefix(tk, "~") {
		return "", false
	}
	if !strings.ContainsAny(tk[1:], "/.") {
		return "", false
	}
	unescaped := unescapeBackslashes(tk)
	if strings.ContainsAny(unescaped, "|;&$`<>") {
		return "", false
	}
	if !pathExists(unescaped) {
		return "", false
	}
	return unescaped, true
}

// pathExists reports whether p resolves to an existing filesystem
// entry (file, directory, or symlink target). "~" / "~/" prefixes
// are expanded relative to the current user's home directory before
// the check, mirroring how the agent will later read the path.
func pathExists(p string) bool {
	if p == "" {
		return false
	}
	expanded := p
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return false
		}
		if p == "~" {
			expanded = home
		} else {
			expanded = filepath.Join(home, p[2:])
		}
	}
	_, err := os.Stat(expanded)
	return err == nil
}

// unescapeBackslashes turns "foo\ bar" into "foo bar". Terminal
// drag-and-drop on macOS uses backslash escaping by default; quoting
// makes that unnecessary.
func unescapeBackslashes(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var out strings.Builder
	out.Grow(len(s))
	prev := false
	for _, r := range s {
		if prev {
			out.WriteRune(r)
			prev = false
			continue
		}
		if r == '\\' {
			prev = true
			continue
		}
		out.WriteRune(r)
	}
	if prev {
		out.WriteRune('\\')
	}
	return out.String()
}

// singleQuote wraps s in single quotes, escaping any embedded single
// quote using the splice idiom: 'foo'\”bar' for foo'bar.
func singleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// Insert places s into the editor at the cursor, splitting on
// newlines so multi-line pastes preserve their structure.
func (e *Editor) Insert(s string) { e.insert(s) }

func (e *Editor) insert(s string) {
	s = normalizeEditorText(s)
	line := e.Lines[e.CursorR]
	pre := substringBefore(line, e.CursorC)
	post := substringAfter(line, e.CursorC)
	// Split pasted text on newlines.
	parts := strings.Split(s, "\n")
	if len(parts) == 1 {
		e.Lines[e.CursorR] = pre + s + post
		e.CursorC += runeLen(s)
		return
	}
	newLines := make([]string, 0, len(e.Lines)+len(parts)-1)
	newLines = append(newLines, e.Lines[:e.CursorR]...)
	newLines = append(newLines, pre+parts[0])
	for i := 1; i < len(parts)-1; i++ {
		newLines = append(newLines, parts[i])
	}
	last := parts[len(parts)-1]
	newLines = append(newLines, last+post)
	newLines = append(newLines, e.Lines[e.CursorR+1:]...)
	e.Lines = newLines
	e.CursorR += len(parts) - 1
	e.CursorC = runeLen(last)
}

// trailingLineContinuation reports whether the cursor sits directly
// after a single, unescaped backslash — the shell's line-continuation
// shape. Only an odd run of backslashes counts: "foo\" continues,
// "foo\\" is a literal backslash and submits.
func (e *Editor) trailingLineContinuation() bool {
	r := []rune(e.Lines[e.CursorR])
	if e.CursorC == 0 || e.CursorC > len(r) {
		return false
	}
	n := 0
	for i := e.CursorC - 1; i >= 0 && r[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

func (e *Editor) newline() {
	line := e.Lines[e.CursorR]
	pre := substringBefore(line, e.CursorC)
	post := substringAfter(line, e.CursorC)
	e.Lines[e.CursorR] = pre
	e.Lines = append(e.Lines, "")
	copy(e.Lines[e.CursorR+2:], e.Lines[e.CursorR+1:])
	e.Lines[e.CursorR+1] = post
	e.CursorR++
	e.CursorC = 0
}

func (e *Editor) backspace() {
	if e.CursorC == 0 {
		if e.CursorR == 0 {
			return
		}
		prev := e.Lines[e.CursorR-1]
		cur := e.Lines[e.CursorR]
		e.Lines = append(e.Lines[:e.CursorR], e.Lines[e.CursorR+1:]...)
		e.CursorR--
		e.CursorC = runeLen(prev)
		e.Lines[e.CursorR] = prev + cur
		return
	}
	line := e.Lines[e.CursorR]
	start := graphemeLeft(line, e.CursorC)
	e.Lines[e.CursorR] = substringBefore(line, start) + substringAfter(line, e.CursorC)
	e.CursorC = start
}

func (e *Editor) delete() {
	line := e.Lines[e.CursorR]
	if e.CursorC == runeLen(line) {
		if e.CursorR == len(e.Lines)-1 {
			return
		}
		next := e.Lines[e.CursorR+1]
		e.Lines = append(e.Lines[:e.CursorR+1], e.Lines[e.CursorR+2:]...)
		e.Lines[e.CursorR] = line + next
		return
	}
	e.Lines[e.CursorR] = substringBefore(line, e.CursorC) + substringAfter(line, graphemeRight(line, e.CursorC))
}

// moveWordLeft jumps the cursor to the start of the previous word,
// using the same word-separator rules as deleteWord. If already at the
// start of the line, wraps to the end of the previous line.
func (e *Editor) moveWordLeft() {
	if e.CursorC == 0 {
		if e.CursorR > 0 {
			e.CursorR--
			e.CursorC = runeLen(e.Lines[e.CursorR])
		}
		return
	}
	r := []rune(e.Lines[e.CursorR])
	i := e.CursorC
	for i > 0 && isWordSep(r[i-1]) {
		i--
	}
	for i > 0 && !isWordSep(r[i-1]) {
		i--
	}
	e.CursorC = i
}

// moveWordRight jumps the cursor to the start of the next word. If
// already at the end of the line, wraps to the start of the next line.
func (e *Editor) moveWordRight() {
	line := e.Lines[e.CursorR]
	if e.CursorC >= runeLen(line) {
		if e.CursorR < len(e.Lines)-1 {
			e.CursorR++
			e.CursorC = 0
		}
		return
	}
	r := []rune(line)
	i := e.CursorC
	for i < len(r) && !isWordSep(r[i]) {
		i++
	}
	for i < len(r) && isWordSep(r[i]) {
		i++
	}
	e.CursorC = i
}

func (e *Editor) moveLeft() {
	if e.CursorC > 0 {
		e.CursorC = graphemeLeft(e.Lines[e.CursorR], e.CursorC)
		return
	}
	if e.CursorR > 0 {
		e.CursorR--
		e.CursorC = runeLen(e.Lines[e.CursorR])
	}
}

func (e *Editor) moveRight() {
	if e.CursorC < runeLen(e.Lines[e.CursorR]) {
		e.CursorC = graphemeRight(e.Lines[e.CursorR], e.CursorC)
		return
	}
	if e.CursorR < len(e.Lines)-1 {
		e.CursorR++
		e.CursorC = 0
	}
}

// moveCursorVisual moves the cursor one visual row in direction
// dir (-1 = up, +1 = down) through the wrapped layout the user
// sees on screen. Handles both multi-line logical inputs and the
// case where a single long line wraps across several visual
// rows.
//
// Algorithm: rebuild the same wrapped layout Render produces,
// tagging each visual row with (logicalRow, runeOffsetStart,
// runeOffsetEnd, leadingWidth). Find the row the cursor sits on,
// then pick (row+dir) and map the cursor's current visual column
// (minus the target row's leading indent) to a rune index inside
// that row's slice of its logical line. No-op at the top/bottom
// edges of the whole buffer.
// MoveVertical moves the cursor one visual row up/down through the
// rendered editor layout. It returns true when the cursor moved, false
// at the top/bottom edge. Callers can use false to fall back to outer
// UI scrolling.
func (e *Editor) MoveVertical(dir int) bool {
	width := e.lastRenderWidth
	if width <= 0 {
		// Fall back to logical-line navigation if Render hasn't
		// been called yet (shouldn't happen in practice; the
		// host always renders once before accepting input).
		return e.moveCursorLogical(dir)
	}

	type vrow struct {
		logical    int    // e.Lines index
		runeStart  int    // rune offset into e.Lines[logical]
		runeEnd    int    // exclusive
		leadWidth  int    // width of prompt / cont indent on this row
		leadPrefix string // the prefix used (prompt on row 0, indent on cont)
	}

	// Match Render's layout exactly: strip ANSI from the prompt before
	// wrapping, otherwise escape bytes count as body runes and Up/Down
	// lands in the wrong visual cell when the prompt is styled.
	plainPrompt := stripANSI(e.Prompt)
	promptLen := runewidth.StringWidth(plainPrompt)
	indent := strings.Repeat(" ", promptLen)

	var rows []vrow
	curVRow, curVCol := 0, 0
	for r, line := range e.Lines {
		prefix := indent
		if r == 0 {
			prefix = plainPrompt
		}
		wrapped := wrapLine(prefix+line, width, indent)
		lineRunes := []rune(line)
		seen := 0
		for wi, w := range wrapped {
			var leadW int
			var leadP string
			body := w
			if wi == 0 {
				if strings.HasPrefix(body, prefix) {
					body = body[len(prefix):]
				}
				leadW = promptLen
				leadP = prefix
			} else {
				if strings.HasPrefix(body, indent) {
					body = body[len(indent):]
				}
				leadW = promptLen
				leadP = indent
			}
			bodyRunes := []rune(body)
			start := seen
			end := seen + len(bodyRunes)
			rows = append(rows, vrow{
				logical: r, runeStart: start, runeEnd: end,
				leadWidth: leadW, leadPrefix: leadP,
			})
			// Record where the cursor currently sits.
			if r == e.CursorR && e.CursorC >= start && e.CursorC <= end {
				curVRow = len(rows) - 1
				inner := e.CursorC - start
				if inner < 0 {
					inner = 0
				}
				if inner > len(bodyRunes) {
					inner = len(bodyRunes)
				}
				curVCol = leadW + runewidth.StringWidth(string(bodyRunes[:inner]))
			}
			seen = end
			// Word-wrap often drops a single space at the boundary.
			for seen < len(lineRunes) && lineRunes[seen] == ' ' {
				seen++
			}
		}
	}

	target := curVRow + dir
	if target < 0 || target >= len(rows) {
		return false
	}
	tr := rows[target]
	line := e.Lines[tr.logical]
	lineRunes := []rune(line)
	bodyRunes := lineRunes[tr.runeStart:tr.runeEnd]

	// Find the rune offset inside bodyRunes whose visible column
	// most closely matches curVCol after accounting for leadWidth.
	want := curVCol - tr.leadWidth
	if want < 0 {
		want = 0
	}
	best := 0
	bestW := 0
	for i := 1; i <= len(bodyRunes); i++ {
		w := runewidth.StringWidth(string(bodyRunes[:i]))
		if w > want {
			break
		}
		best = i
		bestW = w
		if w == want {
			break
		}
	}
	_ = bestW
	e.CursorR = tr.logical
	e.CursorC = tr.runeStart + best
	return true
}

// moveCursorLogical is the pre-visual-navigation fallback used
// when Render hasn't told us the terminal width yet. Walks the
// e.Lines array directly.
func (e *Editor) moveCursorLogical(dir int) bool {
	switch {
	case dir < 0 && e.CursorR > 0:
		e.CursorR--
	case dir > 0 && e.CursorR < len(e.Lines)-1:
		e.CursorR++
	default:
		return false
	}
	if e.CursorC > runeLen(e.Lines[e.CursorR]) {
		e.CursorC = runeLen(e.Lines[e.CursorR])
	}
	return true
}

func (e *Editor) deleteWord() {
	line := e.Lines[e.CursorR]
	if e.CursorC == 0 {
		e.backspace()
		return
	}
	r := []rune(line)
	i := e.CursorC
	// Step 1: walk over any trailing whitespace to the left of the cursor.
	for i > 0 && isWordSep(r[i-1]) {
		i--
	}
	// Step 2: walk over the word itself (non-separator runes).
	for i > 0 && !isWordSep(r[i-1]) {
		i--
	}
	e.Lines[e.CursorR] = string(r[:i]) + string(r[e.CursorC:])
	e.CursorC = i
}

// isWordSep reports whether r is a word separator. Anything that isn't
// a letter, digit, or underscore counts as a separator so delete-word
// feels natural on paths, code, and prose.
func isWordSep(r rune) bool {
	switch {
	case r == '_':
		return false
	case r >= '0' && r <= '9':
		return false
	case r >= 'a' && r <= 'z':
		return false
	case r >= 'A' && r <= 'Z':
		return false
	case r >= 0x80:
		// Treat all non-ASCII as word chars so CJK/äöü don't get chopped.
		return false
	}
	return true
}

// ---- rendering ----

// Render returns the editor's visible lines (wrapped to width).
// visualRow/visualCol describe where the cursor lands within the returned lines.
// maskRunes returns s with every rune replaced by mask, one for one.
func maskRunes(s string, mask rune) string {
	if s == "" {
		return s
	}
	out := make([]rune, 0, len([]rune(s)))
	for range s {
		out = append(out, mask)
	}
	return string(out)
}

func (e *Editor) Render(width int) (lines []string, visualRow, visualCol int) {
	e.lastRenderWidth = width
	// The Prompt may carry ANSI styling (theme-coloured glyph + reset).
	// We must compute wrap geometry using its *visible* width only;
	// otherwise the raw escape bytes leak into wrapLine's per-rune
	// width accounting and the cursor column lands inside the row
	// instead of at the end. See the wrapLine docs for why we keep it
	// ANSI-unaware: the rest of the codebase relies on its simple
	// rune-based behaviour for plain text and would regress if we
	// taught it about escape sequences here. Strip ANSI from the
	// prompt used for layout, then re-apply the original styling to
	// the very first wrapped row before returning.
	plainPrompt := stripANSI(e.Prompt)
	promptLen := runewidth.StringWidth(plainPrompt)
	indent := strings.Repeat(" ", promptLen)

	for r, line := range e.Lines {
		if e.Mask != 0 {
			line = maskRunes(line, e.Mask)
		}
		var prefix string
		if r == 0 {
			prefix = plainPrompt
		} else {
			prefix = indent
		}
		wrapped := wrapLine(prefix+line, width, indent)
		if r == e.CursorR {
			// Compute where CursorC lands inside the wrapped rows by
			// walking the wrapped output character-by-character and
			// tracking which wrapped row + column corresponds to the
			// cursor's rune index in `line`. This is the only reliable
			// answer under word-wrap, where a simple (promptLen+col)/width
			// formula overshoots when wrapLine broke on a space.
			targetRunes := e.CursorC
			row, col := locateCursor(wrapped, prefix, line, targetRunes, indent)
			visualRow = len(lines) + row
			visualCol = col
		}
		// Re-attach the styled prompt to the very first wrapped row of
		// the first logical line. All later rows are indent-only and
		// don't need styling. The replacement is byte-exact because the
		// plain prompt sits at the row's start.
		if r == 0 && len(wrapped) > 0 && strings.HasPrefix(wrapped[0], plainPrompt) {
			wrapped[0] = e.Prompt + wrapped[0][len(plainPrompt):]
		}
		lines = append(lines, wrapped...)
	}
	// The offer is drawn LAST, onto the finished first row. It is not in Lines,
	// so the loop above laid the buffer out without it and the caret is already
	// where the user's own first character goes; painting into the tail of that
	// row cannot move it.
	//
	// What this ordering actually buys is the row count, which is worth being
	// precise about: feeding the offer through wrapLine instead would wrap a
	// long one across three rows, and the composer's height feeds the chat
	// viewport, so the transcript above would resize while the user sat still.
	// (It would NOT move the caret — the caret is at rune 0 and the offer goes
	// after it. The caret only moves under the other implementation of this
	// feature, the one where the suggestion is put in the buffer; that is what
	// the field comment on `ghost` is arguing against, and what the guard in
	// editor_ghost_test.go pins.)
	if e.ghostVisible() && len(lines) > 0 {
		if room := width - promptLen; room > 0 {
			lines[0] += e.styleGhost(fitGhost(e.ghost, room))
		}
	}
	return lines, visualRow, visualCol
}

// styleGhost applies the display styling, if a caller supplied any.
func (e *Editor) styleGhost(s string) string {
	if e.GhostStyle == nil {
		return s
	}
	return e.GhostStyle(s)
}

// fitGhost bounds the offer to one row's worth of columns, marking a cut with
// an ellipsis so a trimmed suggestion cannot be mistaken for a complete one.
//
// Width, not runes: the composer is a terminal row, and a line of CJK or emoji
// occupies two columns per rune. Accepting still inserts the whole line — what
// is trimmed here is the view of it, the same way the buffer's own long lines
// are shown.
func fitGhost(s string, room int) string {
	if runewidth.StringWidth(s) <= room {
		return s
	}
	const ellipsis = "…"
	budget := room - runewidth.RuneWidth('…')
	if budget < 1 {
		// No room for even one column plus the marker: show the marker alone,
		// which still says "there is an offer here" without lying about it.
		return ellipsis
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if used+w > budget {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + ellipsis
}

// locateCursor finds the wrapped row + visible column corresponding to
// `targetRunes` rune positions into the logical `line`, given that the
// wrapped output `wrapped` started with `prefix` on its first row and
// uses `cont` as the continuation indent on subsequent rows.
func locateCursor(wrapped []string, prefix, line string, targetRunes int, cont string) (int, int) {
	prefixW := visibleWidth(prefix)
	contW := visibleWidth(cont)
	lineRunes := []rune(line)
	if targetRunes > len(lineRunes) {
		targetRunes = len(lineRunes)
	}

	seenRunes := 0
	for row, w := range wrapped {
		// strip leading prefix / continuation indent before counting runes
		body := w
		var leadW int
		if row == 0 {
			if strings.HasPrefix(body, prefix) {
				body = body[len(prefix):]
			}
			leadW = prefixW
		} else {
			if strings.HasPrefix(body, cont) {
				body = body[len(cont):]
			}
			leadW = contW
		}
		bodyRunes := []rune(body)
		// Could this wrapped row contain the cursor?
		if targetRunes <= seenRunes+len(bodyRunes) {
			// Column inside body.
			inner := targetRunes - seenRunes
			if inner < 0 {
				inner = 0
			}
			col := leadW + runewidth.StringWidth(string(bodyRunes[:inner]))
			return row, col
		}
		seenRunes += len(bodyRunes)
		// Word-wrap may have dropped whitespace at the boundary; skip the
		// corresponding runes in `line` so counts stay aligned.
		for seenRunes < len(lineRunes) && lineRunes[seenRunes] == ' ' {
			seenRunes++
			if seenRunes >= targetRunes {
				// Cursor landed in the skipped whitespace — place it at
				// the start of the next wrapped row.
				nextRow := row + 1
				if nextRow >= len(wrapped) {
					nextRow = row
				}
				return nextRow, contW
			}
		}
	}
	// Fallback: end of the last wrapped row.
	if len(wrapped) == 0 {
		return 0, prefixW
	}
	last := wrapped[len(wrapped)-1]
	return len(wrapped) - 1, visibleWidth(last)
}

// ---- helpers ----

func substringBefore(s string, col int) string {
	r := []rune(s)
	if col > len(r) {
		col = len(r)
	}
	return string(r[:col])
}

func substringAfter(s string, col int) string {
	r := []rune(s)
	if col > len(r) {
		col = len(r)
	}
	return string(r[col:])
}

func runeLen(s string) int { return len([]rune(s)) }

// normalizeEditorText converts all common line endings to \n before text
// reaches the renderer. A literal carriage return would move the terminal
// cursor back to column 0 and overwrite the left side of the input row.
func normalizeEditorText(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func visibleWidth(s string) int {
	return runewidth.StringWidth(stripANSI(s))
}

func stripANSI(s string) string {
	// Minimal ANSI stripper; handles CSI sequences (ESC [ ... final).
	var out []rune
	i := 0
	runes := []rune(s)
	for i < len(runes) {
		if runes[i] == 0x1b && i+1 < len(runes) && runes[i+1] == '[' {
			i += 2
			for i < len(runes) {
				c := runes[i]
				i++
				if c >= 0x40 && c <= 0x7e {
					break
				}
			}
			continue
		}
		out = append(out, runes[i])
		i++
	}
	return string(out)
}

// WrapANSILine is the exported form of wrapANSILine so other modes /
// dialogs can reuse the same visible-width-aware wrap behavior.
func WrapANSILine(s string, limit int) []string { return wrapANSILine(s, limit) }

// wrapANSILine folds a string that may contain ANSI CSI escapes so that
// the visible width of each line stays within limit. Breaks happen on
// spaces when possible, falling back to mid-token splits for very long
// unbroken runs. Escape sequences are preserved verbatim and never
// counted toward the visible column.
func wrapANSILine(s string, limit int) []string {
	if limit <= 0 {
		return []string{s}
	}
	if visibleWidth(s) <= limit {
		return []string{s}
	}
	runes := []rune(s)
	var out []string
	var line strings.Builder
	var word strings.Builder
	lineW := 0
	wordW := 0

	flushLine := func() {
		out = append(out, line.String())
		line.Reset()
		lineW = 0
	}
	flushWord := func() {
		if word.Len() == 0 {
			return
		}
		if lineW+wordW > limit && lineW > 0 {
			flushLine()
		}
		// If the word alone is bigger than the limit, split it by runes.
		if wordW > limit {
			wr := []rune(word.String())
			for i := 0; i < len(wr); {
				r := wr[i]
				if r == 0x1b && i+1 < len(wr) && wr[i+1] == '[' {
					line.WriteRune(r)
					line.WriteRune(wr[i+1])
					i += 2
					for i < len(wr) {
						c := wr[i]
						line.WriteRune(c)
						i++
						if c >= 0x40 && c <= 0x7e {
							break
						}
					}
					continue
				}
				rw := runewidth.RuneWidth(r)
				if lineW+rw > limit && lineW > 0 {
					flushLine()
				}
				line.WriteRune(r)
				lineW += rw
				i++
			}
			word.Reset()
			wordW = 0
			return
		}
		line.WriteString(word.String())
		lineW += wordW
		word.Reset()
		wordW = 0
	}

	for i := 0; i < len(runes); {
		r := runes[i]
		if r == 0x1b && i+1 < len(runes) && runes[i+1] == '[' {
			word.WriteRune(r)
			word.WriteRune(runes[i+1])
			i += 2
			for i < len(runes) {
				c := runes[i]
				word.WriteRune(c)
				i++
				if c >= 0x40 && c <= 0x7e {
					break
				}
			}
			continue
		}
		if r == ' ' || r == '\t' {
			flushWord()
			rw := runewidth.RuneWidth(r)
			if lineW+rw > limit && lineW > 0 {
				flushLine()
				i++
				continue
			}
			line.WriteRune(r)
			lineW += rw
			i++
			continue
		}
		word.WriteRune(r)
		wordW += runewidth.RuneWidth(r)
		i++
	}
	flushWord()
	if line.Len() > 0 {
		flushLine()
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// WrapANSILineKeepStyle is the exported form of wrapANSILineKeepStyle.
func WrapANSILineKeepStyle(s string, limit int) []string { return wrapANSILineKeepStyle(s, limit) }

// wrapANSILineKeepStyle wraps like wrapANSILine but keeps each physical line a
// self-contained styled unit: the SGR state active at a wrap boundary is
// re-emitted at the start of the next line, and a reset closes any line that
// leaves styling open. Plain wrapANSILine keeps the opening colour escape only
// on the first line, so under the per-row background repaint (paintBackgroundRow,
// which terminates each row with a reset) a wrapped tail falls back to the
// default colour.
//
// Use this when wrapping a PRE-STYLED line you emit directly — a coloured list
// row, an extension-context line, a transcript line. When you instead wrap plain
// text and colour each returned piece yourself (e.g. the login URL, the user
// bubble), plain wrapANSILine is the right tool. Output is byte-identical to
// wrapANSILine unless a styled span actually crosses a wrap boundary, so it's a
// safe drop-in for the direct-emit callers.
func wrapANSILineKeepStyle(s string, limit int) []string {
	pieces := wrapANSILine(s, limit)
	if len(pieces) <= 1 {
		return pieces
	}
	out := make([]string, 0, len(pieces))
	active := "" // SGR sequence in effect at the start of the current piece
	for _, p := range pieces {
		start := active
		active = sgrStateAfter(active, p)
		line := start + p
		// Close the line only when it leaves styling open; a piece that
		// already balances its own escapes stays byte-identical.
		if active != "" && !strings.HasSuffix(line, reset) {
			line += reset
		}
		out = append(out, line)
	}
	return out
}

// sgrStateAfter replays the SGR escape sequences in piece onto a prior state and
// returns the SGR string in effect at the end of piece. A full reset (ESC[0m /
// ESC[m) clears the state; other SGR sequences (CSI … 'm') accumulate, since the
// terminal applies them in order and a later code overrides an earlier one.
// Non-SGR CSI escapes don't affect the state.
func sgrStateAfter(state, piece string) string {
	for i := 0; i < len(piece); {
		if piece[i] != 0x1b || i+1 >= len(piece) || piece[i+1] != '[' {
			i++
			continue
		}
		j := i + 2
		for j < len(piece) && (piece[j] < 0x40 || piece[j] > 0x7e) {
			j++
		}
		if j >= len(piece) {
			break // unterminated escape
		}
		seq := piece[i : j+1]
		switch {
		case seq == reset || seq == "\x1b[m":
			state = ""
		case piece[j] == 'm':
			state += seq
		}
		i = j + 1
	}
	return state
}

func wrapLine(s string, width int, cont string) []string {
	if width <= 0 {
		return []string{s}
	}

	// Tokenize on spaces while preserving their widths. Runs of spaces
	// stay attached to the preceding word so trailing whitespace doesn't
	// create empty wrapped lines.
	type token struct {
		text  string
		width int
		space bool // true if this token is whitespace only
	}
	var tokens []token
	{
		var buf strings.Builder
		var bufW int
		inSpace := false
		flush := func() {
			if buf.Len() == 0 {
				return
			}
			tokens = append(tokens, token{text: buf.String(), width: bufW, space: inSpace})
			buf.Reset()
			bufW = 0
		}
		for _, r := range s {
			rSpace := r == ' ' || r == '\t'
			if buf.Len() == 0 {
				inSpace = rSpace
			} else if rSpace != inSpace {
				flush()
				inSpace = rSpace
			}
			buf.WriteRune(r)
			bufW += runewidth.RuneWidth(r)
		}
		flush()
	}

	var out []string
	var cur strings.Builder
	curW := 0
	firstLine := true
	contW := visibleWidth(cont)

	newLine := func() {
		out = append(out, cur.String())
		cur.Reset()
		curW = 0
		// After flushing, everything we append next is a CONTINUATION
		// row, which must start with the cont indent so the editor's
		// visual cursor alignment stays consistent. The old code only
		// wrote cont when !firstLine BEFORE toggling firstLine, which
		// meant the very first wrap never got its indent. That caused
		// the terminal cursor to land in the wrong column after a
		// multi-line paste.
		firstLine = false
		cur.WriteString(cont)
		curW = contW
	}

	for i := 0; i < len(tokens); i++ {
		tk := tokens[i]

		// Drop leading whitespace after a wrap.
		if tk.space && curW == contW && !firstLine {
			continue
		}

		if curW+tk.width <= width {
			cur.WriteString(tk.text)
			curW += tk.width
			continue
		}

		// Token overflows.
		//
		// If the token on its own is wider than what fits on a
		// continuation line (width - contW), it'll need to be split
		// rune-by-rune no matter what. In that case, don't break
		// first: let the rune-split start right here at the current
		// column and wrap naturally. Breaking first would strand the
		// prompt (on firstLine) or the cont indent (on later lines)
		// on a row by itself, which is the drag-drop-long-path bug.
		if tk.width > width-contW {
			// Rune-by-rune starting from the current column. newLine()
			// inside handles wrapping and re-indents.
			for _, r := range tk.text {
				rw := runewidth.RuneWidth(r)
				if curW+rw > width {
					newLine()
				}
				cur.WriteRune(r)
				curW += rw
			}
			continue
		}

		// Token fits on a fresh continuation line; break first, then
		// emit it whole.
		if cur.Len() > 0 && !(firstLine && curW == 0) {
			trimmed := strings.TrimRight(cur.String(), " \t")
			cur.Reset()
			cur.WriteString(trimmed)
			curW = visibleWidth(trimmed)
			newLine()
			if tk.space {
				continue
			}
		}
		cur.WriteString(tk.text)
		curW += tk.width
	}

	if cur.Len() > 0 || len(out) == 0 {
		out = append(out, cur.String())
	}
	return out
}

// pasteCollapseLineThreshold and pasteCollapseCharThreshold govern
// when a bracketed paste gets collapsed to a [pasted text #N ...]
// placeholder instead of being inserted inline. Either trigger
// alone is enough — a 500-line log dump and a 1200-character
// one-line log entry both bloat the editor in ways the user
// doesn't want to scroll through while composing a prompt.
const (
	pasteCollapseLineThreshold = 10
	pasteCollapseCharThreshold = 1000
)

// pasteShouldCollapse reports whether a pasted chunk is big enough
// to deserve a placeholder token instead of being inserted verbatim.
// Collapse on > 10 lines OR > 1000 characters, whichever fires
// first.
func pasteShouldCollapse(s string) bool {
	return countLines(s) > pasteCollapseLineThreshold || len(s) > pasteCollapseCharThreshold
}

// formatPastePlaceholder builds the visible marker for a
// collapsed paste. Two shapes:
//
//	[pasted text #N +L lines]  — used when the line count is the
//	                             trigger (multi-line dumps)
//	[pasted text #N C chars]   — used when the character count is
//	                             the trigger (long single-line
//	                             or near-single-line pastes)
//
// Line-triggered takes precedence so a 12-line 4000-char paste
// reads as "+12 lines", not "4000 chars".
func formatPastePlaceholder(id int, body string) string {
	if countLines(body) > pasteCollapseLineThreshold {
		return fmt.Sprintf("[pasted text #%d +%d lines]", id, countLines(body))
	}
	return fmt.Sprintf("[pasted text #%d %d chars]", id, len(body))
}

// countLines returns the number of visual lines in s. A trailing
// newline is not counted as an extra empty line so
// "foo\nbar\n" reads as 2 lines (what the user expects in the
// "+N lines" summary) instead of 3.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// pastePlaceholderRE matches both placeholder shapes the editor
// emits for a collapsed paste:
//
//	[pasted text #N +L lines]   — multi-line trigger
//	[pasted text #N C chars]    — long-char trigger
//
// Capture group 1 is the numeric id used to look up the full
// body in e.pastes; the rest of the token is free-form and gets
// discarded during expansion.
var pastePlaceholderRE = regexp.MustCompile(`\[pasted text #(\d+) (?:\+\d+ lines?|\d+ chars?)\]`)
var filePlaceholderRE = regexp.MustCompile(`\[(file|dir):(\d+):[^\]]+\]`)

// expandPastePlaceholders returns raw with every paste token
// swapped for the body stored under its id in pastes. Tokens
// whose id isn't in the map are left as-is (user deleted the
// corresponding entry somehow, or the id is spurious user text
// that happens to match the shape).
func expandPastePlaceholders(raw string, pastes map[int]string) string {
	return pastePlaceholderRE.ReplaceAllStringFunc(raw, func(match string) string {
		groups := pastePlaceholderRE.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		var id int
		if _, err := fmt.Sscanf(groups[1], "%d", &id); err != nil {
			return match
		}
		if body, ok := pastes[id]; ok {
			return body
		}
		return match
	})
}

// expandFilePlaceholders returns raw with every [file:N:name] and
// [dir:N:name/] token swapped for the full path stored under its id.
func expandFilePlaceholders(raw string, files, dirs map[int]string) string {
	return filePlaceholderRE.ReplaceAllStringFunc(raw, func(match string) string {
		groups := filePlaceholderRE.FindStringSubmatch(match)
		if len(groups) < 3 {
			return match
		}
		kind := groups[1] // "file" or "dir"
		var id int
		if _, err := fmt.Sscanf(groups[2], "%d", &id); err != nil {
			return match
		}
		var m map[int]string
		if kind == "dir" {
			m = dirs
		} else {
			m = files
		}
		if path, ok := m[id]; ok {
			return path
		}
		return match
	})
}

// collapseOrQuoteFilePaths checks if the paste is a file path and
// collapses it to a [file:basename] chip. URLs are left as-is.
// Non-path pastes fall through to quotePastedFilePaths.
func (e *Editor) collapseOrQuoteFilePaths(paste string) string {
	if paste == "" || strings.ContainsRune(paste, '\n') {
		return quotePastedFilePaths(paste)
	}
	trimmed := strings.TrimSpace(paste)
	// Don't collapse URLs.
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return paste
	}
	// Check if it's a single file path.
	p, ok := normalisePathToken(trimmed)
	if !ok {
		return quotePastedFilePaths(paste)
	}
	// Only collapse if the path is long enough to benefit.
	base := filepath.Base(p)
	if len(p) <= len(base)+5 {
		// Short path like "./foo.txt" - just quote it.
		return singleQuote(p)
	}
	// Check if it's a directory.
	info, err := os.Stat(p)
	if err == nil && info.IsDir() {
		if e.dirs == nil {
			e.dirs = map[int]string{}
		}
		e.dirSeq++
		e.dirs[e.dirSeq] = singleQuote(p)
		return fmt.Sprintf("[dir:%d:%s/]", e.dirSeq, base)
	}
	if e.files == nil {
		e.files = map[int]string{}
	}
	e.fileSeq++
	e.files[e.fileSeq] = singleQuote(p)
	return fmt.Sprintf("[file:%d:%s]", e.fileSeq, base)
}

// graphemeLeft returns the rune index of the start of the grapheme
// cluster immediately left of runeIdx — the unit Backspace and ←
// operate on. Treating clusters (combining accents, emoji ZWJ
// families, flag pairs) as single units keeps the cursor from
// landing inside one and deletions from leaving broken halves.
func graphemeLeft(line string, runeIdx int) int {
	if runeIdx <= 0 {
		return 0
	}
	count := 0
	rest := line
	for len(rest) > 0 {
		cluster, tail, _, _ := uniseg.FirstGraphemeClusterInString(rest, -1)
		n := runeLen(cluster)
		if count+n >= runeIdx {
			return count
		}
		count += n
		rest = tail
	}
	return count
}

// graphemeRight returns the rune index just past the grapheme
// cluster at runeIdx — the unit Delete and → operate on.
func graphemeRight(line string, runeIdx int) int {
	count := 0
	rest := line
	for len(rest) > 0 {
		cluster, tail, _, _ := uniseg.FirstGraphemeClusterInString(rest, -1)
		n := runeLen(cluster)
		if count+n > runeIdx {
			return count + n
		}
		count += n
		rest = tail
	}
	return count
}

// pathShapedToken is the cheap (no syscall) pre-filter for tokens
// that normalisePathToken would stat: absolute, tilde, or file://
// spellings.
func pathShapedToken(tok string) bool {
	return strings.HasPrefix(tok, "/") || strings.HasPrefix(tok, "~") || strings.HasPrefix(tok, "file://")
}
