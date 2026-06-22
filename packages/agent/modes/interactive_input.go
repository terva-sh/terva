package modes

// Key handling outside the overlay registry and global keymap: the
// handleKey pipeline, input history, the ctrl+c exit arm, and path
// tab-completion.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// ctrlCExitWindow is how long after a ctrl+c press a *second* press
// will exit instead of just clearing input. Long enough to be
// deliberate (rules out accidental key chord), short enough that the
// hint stays meaningful.
const ctrlCExitWindow = 2 * time.Second

// armCtrlCExit records the timestamp of the current ctrl+c so the next
// one within ctrlCExitWindow exits.
func (i *Interactive) armCtrlCExit() {
	i.mu.Lock()
	i.lastCtrlC = time.Now()
	i.mu.Unlock()
}

// ctrlCExitArmed reports whether a previous ctrl+c was recent enough
// that another press should now exit.
func (i *Interactive) ctrlCExitArmed() bool {
	i.mu.Lock()
	t := i.lastCtrlC
	i.mu.Unlock()
	return !t.IsZero() && time.Since(t) <= ctrlCExitWindow
}

// clearFileSuggestQuery strips the filter the user typed after the
// last "@", leaving the bare "@" so the picker stays open. Called when
// navigating between directory levels (Right/Left): the filter applied
// to the level the user was on, not the one being entered, so carrying
// it forward would wrongly hide the new directory's contents.
func (i *Interactive) clearFileSuggestQuery() {
	val := i.ed.Value()
	if idx := strings.LastIndex(val, "@"); idx >= 0 {
		i.ed.SetValue(val[:idx+1])
	}
}

func (i *Interactive) handleKey(ctx context.Context, k tui.Key) (done bool) {
	// Any key that isn't ctrl+c invalidates an armed ctrl+c-exit, so
	// pressing ctrl+c then typing then ctrl+c much later doesn't quit
	// unexpectedly. The hint message also goes stale; clear it.
	if k.Kind != tui.KeyCtrlC {
		i.mu.Lock()
		if !i.lastCtrlC.IsZero() {
			i.lastCtrlC = time.Time{}
			if strings.HasPrefix(i.statusOK, "input cleared") || strings.HasPrefix(i.statusOK, "press ctrl+c") {
				i.statusOK = ""
			}
		}
		i.mu.Unlock()
	}

	// Dialogs consume keys while open (except ctrl+c, which closes
	// them — see each entry's ctrlC in overlay_registry.go). The
	// registry is priority-ordered; the confirm dialog sits first
	// because the agent goroutine is blocked waiting for its answer,
	// so no key may leak anywhere else while it's up.
	if handled, done := i.dispatchOverlayKey(k); handled {
		return done
	}

	// Global keys: one named binding per chord in keymap.go. A
	// binding may decline (keyPass) — e.g. Esc while a popup is open,
	// Alt+Up with an empty queue — and the key continues to the
	// downstream consumers below.
	if handled, done := i.dispatchGlobalKey(ctx, k); handled {
		return done
	}

	// Note: we intentionally do NOT gate the editor on busy state.
	// Typing while the agent is working is supported — submitted
	// messages are queued and delivered as follow-up turns when the
	// current turn ends. See the submit handler below.

	// Slash suggestions: intercept up/down/tab/enter when the popup is visible.
	if i.suggest.Active(i.ed.Value()) {
		switch k.Kind {
		case tui.KeyUp:
			i.suggest.Up()
			return false
		case tui.KeyDown:
			i.suggest.Down()
			return false
		case tui.KeyPageUp:
			i.suggest.PageUp()
			return false
		case tui.KeyPageDown:
			i.suggest.PageDown()
			return false
		case tui.KeyTab:
			if name := i.suggest.Selection(i.ed.Value()); name != "" {
				i.ed.SetValue(name)
				i.suggest.Reset()
			}
			return false
		case tui.KeyEnter:
			// Enter on an ambiguous or partial slash prefix: complete to the
			// currently highlighted command and run it. That way typing
			// "/lo" + enter picks whichever of /login or /logout is selected
			// in the popup instead of submitting "/lo" as unknown. Also
			// clear the editor so the command doesn't linger after the
			// dialog opens/closes.
			if name := i.suggest.Selection(i.ed.Value()); name != "" {
				i.ed.Clear()
				i.suggest.Reset()
				return i.runSlash(ctx, name)
			}
		case tui.KeyEsc:
			i.ed.Clear()
			i.suggest.Reset()
			return false
		}
	}

	// File suggestions: intercept up/down/tab/enter when the @-popup is visible.
	if i.fileSuggest.Active(i.ed.Value()) {
		switch k.Kind {
		case tui.KeyUp:
			i.fileSuggest.Up()
			return false
		case tui.KeyDown:
			i.fileSuggest.Down()
			return false
		case tui.KeyRight:
			// Open selected directory. The filter the user typed picked
			// that directory at the current level; once we descend it no
			// longer applies to the directory's contents, so clear it.
			// Otherwise typing "@eda" then right would re-filter inside
			// eda/ by "eda" and show nothing.
			if i.fileSuggest.Right() {
				i.clearFileSuggestQuery()
			}
			return false
		case tui.KeyLeft:
			// Go back to parent directory. Clear the filter for the same
			// reason as Right: it was scoped to the level we just left.
			if i.fileSuggest.Left() {
				i.clearFileSuggestQuery()
			}
			return false
		case tui.KeyEnter:
			if entry, ok := i.fileSuggest.SelectedEntry(i.ed.Value()); ok {
				var chip string
				if entry.isDir {
					chip = "[dir:" + entry.rel + "/]"
				} else {
					chip = "[file:" + entry.rel + "]"
				}
				val := i.ed.Value()
				if idx := strings.LastIndex(val, "@"); idx >= 0 {
					val = val[:idx]
				}
				i.ed.SetValue(val + chip + " ")
				i.fileSuggest.Reset()
			}
			return false
		case tui.KeyEsc:
			val := i.ed.Value()
			if idx := strings.LastIndex(val, "@"); idx >= 0 {
				i.ed.SetValue(val[:idx])
			}
			i.fileSuggest.Reset()
			return false
		}
	}

	// Tab-complete a path token in the editor when no popup is open.
	// Recognises tokens that look like paths (start with ~, /, ./, ../
	// or contain a slash); shell-style completion expands ~, lists the
	// parent dir, and completes the basename to the longest common
	// prefix. Single match: full replace and trailing / for dirs.
	// No match: no-op. Plain bare words (no slash, no tilde) fall
	// through so Tab keeps its current no-op behaviour outside paths.
	if k.Kind == tui.KeyTab && !i.suggest.Active(i.ed.Value()) && !i.fileSuggest.Active(i.ed.Value()) {
		if i.tryPathTabComplete() {
			return false
		}
	}

	if i.handleInputHistoryKey(k) {
		return false
	}
	if i.inputHistoryIndex >= 0 && k.Kind != tui.KeyLeft && k.Kind != tui.KeyRight {
		i.inputHistoryIndex = -1
	}

	if submit := i.ed.HandleKey(k); submit {
		// SubmitValue() expands any [pasted text #N +L lines]
		// placeholders back into their bodies; the raw Value()
		// is only what the user sees on screen.
		text := strings.TrimRight(i.ed.SubmitValue(), "\n")
		// Expand [file:name] and [dir:name/] chips to full paths.
		text = expandFileChips(text, i.cfg.CWD)
		// Reconcile any clipboard images pasted this turn: markers still in
		// the text attach their image, deleted markers drop theirs. Only
		// rewrites the text when something was actually pasted.
		var clipImages []provider.ImageBlock
		if len(i.clipboardImages) > 0 {
			text, clipImages = preparePromptWithClipboardImages(text, i.clipboardImages)
			i.clipboardImages = nil
		}
		if text == "" && len(clipImages) == 0 {
			return false
		}
		i.ed.Clear()
		i.inputHistoryIndex = -1
		i.suggest.Reset()
		i.fileSuggest.Reset()

		if cmd, ok := shellEscapeCommand(text); ok {
			i.startShellEscape(ctx, cmd)
			return false
		}

		if looksLikeSlashCommand(text) {
			head := text
			rest := ""
			if idx := strings.IndexAny(text, " \t"); idx >= 0 {
				head = text[:idx]
				rest = strings.TrimSpace(text[idx:])
			}
			if !isKnownSlashCommand(text) {
				// Try extensions before giving up. Extensions register
				// commands by bare name (no leading slash); strip it here.
				extName := strings.TrimPrefix(head, "/")
				if i.cfg.Extensions != nil && i.cfg.Extensions.HasCommand(extName) {
					go i.invokeExtensionCommand(ctx, extName, rest)
					return false
				}
				i.mu.Lock()
				i.statusErr = "unknown command " + head + " — type /help to see the list"
				i.statusOK = ""
				i.mu.Unlock()
				return false
			}
			// Slash commands run regardless of busy state. Commands that
			// would mutate the transcript or replace the agent (/clear,
			// /compact, /logout, /login, /model) cancel the active turn
			// first and wait for the goroutine to wind down so they don't
			// race with a streaming response. Safe commands (/help,
			// /jump, /sessions, /jail, /unjail, /exit) run immediately
			// without disturbing the active turn.
			if slashCancelsTurn(head) {
				i.cancelAndWaitForIdle()
			}
			return i.runSlash(ctx, text)
		}

		if !i.turns.HasAgent() {
			i.setStatusErr("not logged in. type /login first.")
			return false
		}
		// Mirror the user's typed prompt into the paired Telegram
		// chat (when the bridge is active) so the Telegram thread
		// stays a complete record of the session, not just the half
		// that originated on the phone. On a goroutine so the
		// network write doesn't delay the local turn.
		if i.chatBridge != nil && i.chatBridge.Active() {
			go i.chatBridge.OnUserTyped(text)
		}
		// startTurn claims-or-queues atomically inside the turn
		// engine: if a turn is already in flight the prompt queues
		// for the agent loop's next safe model-call boundary.
		if len(clipImages) > 0 {
			i.startTurnWithImages(ctx, text, clipImages)
		} else {
			i.startTurn(ctx, text)
		}
	}
	return false
}

func (i *Interactive) handleInputHistoryKey(k tui.Key) bool {
	if k.Kind != tui.KeyLeft && k.Kind != tui.KeyRight {
		return false
	}
	// Do not steal normal cursor movement. History browsing can only
	// start from an empty editor; once active, Left/Right keep walking
	// the ring so repeated presses work even though the editor now
	// contains the selected historical prompt.
	if i.inputHistoryIndex < 0 && !i.ed.IsEmpty() {
		return false
	}
	hist := i.inputHistory()
	if len(hist) == 0 {
		return false
	}

	if i.inputHistoryIndex < 0 {
		// Start just after the newest item so Left lands on the most
		// recent user prompt and Right keeps the editor empty.
		i.inputHistoryIndex = len(hist)
	}

	switch k.Kind {
	case tui.KeyLeft:
		if i.inputHistoryIndex > 0 {
			i.inputHistoryIndex--
		}
	case tui.KeyRight:
		if i.inputHistoryIndex < len(hist) {
			i.inputHistoryIndex++
		}
	}

	if i.inputHistoryIndex >= len(hist) {
		i.ed.Clear()
	} else {
		i.ed.SetValue(hist[i.inputHistoryIndex])
	}
	return true
}

func (i *Interactive) inputHistory() []string {
	ag := i.turns.Agent()
	if ag == nil {
		return nil
	}
	msgs := ag.Messages()
	hist := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.Role != provider.RoleUser || core.IsToolImageMirror(m) {
			continue
		}
		text := userMessageText(m)
		if strings.TrimSpace(text) == "" {
			continue
		}
		hist = append(hist, text)
	}
	return hist
}

func userMessageText(m provider.Message) string {
	var sb strings.Builder
	for _, c := range m.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}

// invokeExtensionCommand fires an extension-registered slash command
// in a background goroutine, awaits the response, and applies the
// requested action (prompt / insert / display / noop). Errors and
// timeouts surface as a status_err line.

// tryPathTabCompleteEditor looks at ed's current value, finds the
// path-like token immediately before the cursor (the cursor is always
// at the end of the buffer after a keystroke, so "before the cursor"
// is the trailing non-whitespace run), and rewrites it to its shell-
// style completion against the filesystem.
//
// Returns true when it consumed the Tab keystroke (token recognised,
// completion attempted — even if no candidates matched, the keystroke
// is still consumed so it doesn't insert a literal tab character).
// Returns false when the token doesn't look like a path; callers then
// let Tab fall through to its normal no-op.
//
// Recognised path shapes:
//   - ~ or ~/foo                  expanded via os.UserHomeDir()
//   - /abs/path or /abs/path/foo  absolute
//   - ./foo, ../foo, foo/bar      relative to cwd
//
// A bare word like "hello" is not treated as a path so plain text
// keeps Tab as a literal no-op.
//
// Free function (not a method) so the same logic runs against the
// editor instances owned by btwDialog and swarmDialog without each
// dialog needing its own copy.
func tryPathTabCompleteEditor(ed *tui.Editor, cwd string) bool {
	if ed == nil {
		return false
	}
	val := ed.Value()
	// Find the trailing run of non-whitespace.
	start := len(val)
	for start > 0 {
		r := val[start-1]
		if r == ' ' || r == '\t' || r == '\n' {
			break
		}
		start--
	}
	token := val[start:]
	if token == "" {
		return false
	}
	if !looksLikePathToken(token) {
		return false
	}

	// Resolve the absolute parent directory + base prefix to match.
	parentAbs, basePrefix, displayParent, ok := resolvePathTabToken(token, cwd)
	if !ok {
		return true
	}
	entries, err := os.ReadDir(parentAbs)
	if err != nil {
		return true
	}
	var names []string
	var isDir []bool
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, basePrefix) {
			continue
		}
		// Hide dotfiles unless the user explicitly typed a leading dot,
		// mirroring bash's default behaviour.
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(basePrefix, ".") {
			continue
		}
		names = append(names, name)
		isDir = append(isDir, e.IsDir())
	}
	if len(names) == 0 {
		return true
	}

	var completed string
	var completedIsDir bool
	if len(names) == 1 {
		completed = names[0]
		completedIsDir = isDir[0]
	} else {
		completed = longestCommonPrefix(names)
		if completed == basePrefix {
			// Already at the deepest unambiguous prefix; nothing to add.
			return true
		}
	}

	// Build the replacement token in the same display form the user
	// typed (preserve ~ vs absolute vs relative).
	newToken := displayParent + completed
	if len(names) == 1 && completedIsDir {
		newToken += "/"
	}

	ed.SetValue(val[:start] + newToken)
	return true
}

// tryPathTabComplete is the Interactive-bound convenience wrapper.
// It calls the free helper against the main editor and invalidates
// the frame on a successful rewrite.
func (i *Interactive) tryPathTabComplete() bool {
	if tryPathTabCompleteEditor(i.ed, i.cfg.CWD) {
		i.invalidate()
		return true
	}
	return false
}

// looksLikePathToken reports whether tok is shaped like a filesystem
// path. Paths must either start with ~, /, ./, ../ or contain a /.
// Plain words are excluded so Tab on "hello" stays a no-op.
func looksLikePathToken(tok string) bool {
	if tok == "" {
		return false
	}
	if tok[0] == '~' || tok[0] == '/' {
		return true
	}
	if strings.HasPrefix(tok, "./") || strings.HasPrefix(tok, "../") {
		return true
	}
	return strings.Contains(tok, "/")
}

// resolvePathTabToken splits tok into (absolute parent dir, basename
// prefix to match, display-form parent the user typed). ok is false
// when the parent dir can't be resolved (e.g. ~ with no $HOME).
func resolvePathTabToken(tok, cwd string) (parentAbs, basePrefix, displayParent string, ok bool) {
	// Detect ~ expansion.
	expanded := tok
	homePrefix := ""
	if tok == "~" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", "", "", false
		}
		// "~" alone: complete in $HOME. parent = home, base = "".
		return home, "", "~/", true
	}
	if strings.HasPrefix(tok, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", "", "", false
		}
		expanded = home + tok[1:]
		homePrefix = "~"
	}

	dir, base := splitDirBase(expanded)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}

	// Reconstruct the display form the user typed for the parent,
	// keeping ~ when they used it. The base is dropped — the caller
	// substitutes the completed name.
	display := tok[:len(tok)-len(base)]
	if homePrefix != "" && !strings.HasPrefix(display, "~") {
		display = homePrefix + display[len(homePrefix):]
	}
	return dir, base, display, true
}

// splitDirBase is like filepath.Split but preserves the trailing
// slash convention: "foo" => (".", "foo"); "foo/" => ("foo", "");
// "a/b" => ("a/", "b"); "/" => ("/", ""). Returned dir always has
// the trailing separator when non-empty so callers can rebuild paths
// by concatenation.
func splitDirBase(p string) (dir, base string) {
	if p == "" {
		return ".", ""
	}
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return ".", p
	}
	return p[:i+1], p[i+1:]
}

func longestCommonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	prefix := ss[0]
	for _, s := range ss[1:] {
		n := 0
		for n < len(prefix) && n < len(s) && prefix[n] == s[n] {
			n++
		}
		prefix = prefix[:n]
		if prefix == "" {
			return ""
		}
	}
	return prefix
}
