package modes

// Transcript rendering: the redraw pass, the chat build/cache,
// scroll/viewport helpers, and the image/ANSI line utilities they
// depend on.

import (
	"fmt"
	"strings"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// Interactive is the TUI chat loop.
type chatCacheKey struct {
	cols            int
	agentRev        uint64
	statusOK        string
	statusErr       string
	help            string
	extNotes        string
	shellBlock      string
	updateAvailable bool
	updateCurrent   string
	updateLatest    string
	updateURL       string
	welcomeShowVer  bool
	expandAll       bool
	tailLimit       int
}

// frameSnapshot is one frame's copy of every input the chat build
// and status bar need from mutex-shared state (TUI plan Phase 2e).
// redraw fills it under a short i.mu hold, then runs the expensive
// markdown/chroma View.Build and the terminal write WITHOUT the lock
// — so the agent-event sink and background status writers never wait
// on a 50-200ms render of a large transcript.
//
// Slice fields are header snapshots: writers replace these slices
// under i.mu (append never mutates the visible prefix), and string
// elements are immutable, so the copies stay coherent. Tool views
// are value copies because the event sink mutates them in place.
type frameSnapshot struct {
	ts        turnRenderState
	toolViews []tui.ToolCallView // gate-open prefix, in arrival order

	statusErr  string
	statusOK   string
	helpBlock  []string
	extNotes   []string
	shellBlock []string

	updateInfo   UpdateInfo
	cumUsage     provider.Usage
	lastCtxInput int

	// busyPrefix is the pre-rendered spinner segment of the status
	// bar; the spinner's state is mutated by turn goroutines under
	// i.mu, so it must be formatted inside the snapshot hold.
	busyPrefix string
}

// snapshotFrameLocked copies the frame inputs. Must be called with
// i.mu held; ts is the engine snapshot taken just before the hold.
func (i *Interactive) snapshotFrameLocked(ts turnRenderState) frameSnapshot {
	snap := frameSnapshot{
		ts:           ts,
		statusErr:    i.statusErr,
		statusOK:     i.statusOK,
		helpBlock:    i.helpBlock,
		extNotes:     i.extNotes,
		shellBlock:   i.shellBlock,
		updateInfo:   i.updateInfo,
		cumUsage:     i.cumUsage,
		lastCtxInput: i.lastCtxInput,
	}
	if ts.busy {
		snap.busyPrefix = fmt.Sprintf("%s %s %s %s",
			i.cfg.Theme.FG256(i.cfg.Theme.Assistant, i.spin.Frame()),
			i.cfg.Theme.FG256(i.cfg.Theme.Assistant, i.spin.Message()),
			i.cfg.Theme.FG256(i.cfg.Theme.Muted, "-"),
			i.cfg.Theme.FG256(i.cfg.Theme.Muted, i.spin.Elapsed().String()),
		)
		// Deterministic ordering: a tool block stays hidden until the
		// paced assistant text that preceded it has finished typing
		// out. toolOrder is append-only in arrival order, so once one
		// tool is still gated, every later tool is too — stop there to
		// avoid showing a tool out of sequence. Value copies: the
		// agent-event sink keeps mutating the live structs.
		for _, id := range i.toolOrder {
			if !i.turns.GateOpen(id) {
				break
			}
			if tc, ok := i.toolCalls[id]; ok {
				snap.toolViews = append(snap.toolViews, *tc)
			}
		}
	}
	return snap
}

func (i *Interactive) cachedChat(cols int, snap frameSnapshot) []string {
	key, cacheable := i.chatCacheKey2(cols, snap)
	if cacheable && i.chatCacheValid && i.chatCacheKey == key {
		return append([]string(nil), i.chatCache...)
	}
	chat := i.buildChat(cols, snap)
	if cacheable {
		i.chatCache = append(i.chatCache[:0], chat...)
		i.chatCacheKey = key
		i.chatCacheValid = true
	} else {
		i.chatCacheValid = false
	}
	return chat
}

func (i *Interactive) chatCacheKey2(cols int, snap frameSnapshot) (chatCacheKey, bool) {
	// Live turns mutate streaming/tool-call state at high frequency;
	// keep those on the old rebuild path. The cache targets the common
	// idle case where only the editor contents changed between redraws.
	if snap.ts.busy || snap.ts.streamActive {
		return chatCacheKey{}, false
	}
	var rev uint64
	if snap.ts.agent != nil {
		rev = snap.ts.agent.Revision()
	}
	showVer := len(i.view.Messages) == 0 && !snap.ts.streamActive && len(snap.toolViews) == 0 && !i.welcomeStart.IsZero() && time.Since(i.welcomeStart) < welcomeVersionDuration
	return chatCacheKey{
		cols:            cols,
		agentRev:        rev,
		statusOK:        snap.statusOK,
		statusErr:       snap.statusErr,
		help:            strings.Join(snap.helpBlock, "\n"),
		extNotes:        strings.Join(snap.extNotes, "\n"),
		shellBlock:      strings.Join(snap.shellBlock, "\n"),
		updateAvailable: snap.updateInfo.Available,
		updateCurrent:   snap.updateInfo.Current,
		updateLatest:    snap.updateInfo.Latest,
		updateURL:       snap.updateInfo.URL,
		welcomeShowVer:  showVer,
		expandAll:       i.view.ExpandAll,
		tailLimit:       i.view.TailLimit,
	}, true
}

func (i *Interactive) buildChat(cols int, snap frameSnapshot) []string {
	ts := snap.ts
	if ts.agent != nil {
		i.view.Messages = filterHiddenTranscriptMessages(ts.agent.Messages())
	} else {
		i.view.Messages = nil
	}
	// Pacer flush: while the streaming pacer is still draining the
	// buffer (i.e. EvAssistantMessage already fired but more runes
	// are queued), the final assistant message is already in
	// the agent's Messages() in full. Painting it in the transcript
	// AND the streaming block at the same time shows the user the
	// complete text immediately — which defeats the whole pacer.
	// Hide the last message until the pacer catches up; once the
	// flushing phase ends, the message is revealed (the streaming
	// block disappears the same frame because the stream goes idle,
	// so the transition is seamless).
	if ts.streamFlushing && len(i.view.Messages) > 0 {
		i.view.Messages = i.view.Messages[:len(i.view.Messages)-1]
	}
	i.view.Streaming = ts.streamVisible
	i.view.StreamingActive = ts.streamActive
	// Guard against the narrow race where EvAssistantMessage has
	// just promoted a streaming reply into the transcript but a
	// render tick hasn't retired the stream state yet. Without the
	// guard, the same text would appear twice (once as the
	// in-flight streaming block, once as the last transcript
	// message). We detect the duplicate strictly: the last
	// assistant message's visible text must equal the streaming
	// buffer. Just matching on role is too broad — it also hides
	// the next round's typewriter streaming after a tool turn,
	// because the last transcript message is always assistant
	// (the tool-use block) until the follow-up summary lands.
	if ts.streamActive && ts.streamLen > 0 {
		if n := len(i.view.Messages); n > 0 && i.view.Messages[n-1].Role == provider.RoleAssistant {
			if assistantText(i.view.Messages[n-1]) == ts.streamVisible {
				i.view.StreamingActive = false
			}
		}
	}
	// Live tool-call view: only shown while a turn is in flight. Once
	// the agent is idle, every tool call has already been folded into
	// the transcript (as assistant.ToolCallBlock + a tool-role message),
	// so showing v.ToolCalls a second time would duplicate them below
	// the final assistant text — which looks like the summary came
	// "before" the tools.
	i.view.ToolCalls = append(i.view.ToolCalls[:0], snap.toolViews...)
	i.view.Err = snap.statusErr
	// Live streaming/tool rows are appended to the chat buffer (not
	// hoisted into a separate live block above the editor). That keeps
	// the renderer's diff view append-only: when a tool finishes the
	// rows update in place at the end of the buffer, instead of the
	// whole bottom band shrinking and shifting chat lines around.
	i.liveBlock = nil
	chat := i.view.Build(cols)

	// Welcome banner: shown at the top of the chat area when there is
	// no transcript yet. Disappears after the first message is sent.
	// The version suffix is shown for welcomeVersionDuration after
	// startup, then drops off automatically.
	if len(i.view.Messages) == 0 && !ts.streamActive && len(snap.toolViews) == 0 {
		showVer := !i.welcomeStart.IsZero() && time.Since(i.welcomeStart) < welcomeVersionDuration
		chat = append(welcomeBanner(i.cfg.Theme, i.cfg.Version, showVer, i.welcomeGreeting), chat...)
	}

	// Update-available banner: prepended above everything else so it's
	// the first thing the user sees when opening a new terva session.
	// Once rendered, it stays until the user updates to a newer
	// version — we don't persist a "dismissed" flag because this is
	// cheap and re-showing it is how most users remember to update.
	if snap.updateInfo.Available {
		banner := renderUpdateBanner(i.cfg.Theme, snap.updateInfo, cols)
		chat = append(banner, chat...)
	}

	// /help block: appended to the transcript so it appears at the
	// bottom of the chat area (right above the status bar / editor).
	// Prepending it would push long conversations off the top of the
	// viewport, which users would miss entirely.
	if len(snap.helpBlock) > 0 {
		chat = append(chat, snap.helpBlock...)
	}

	if snap.statusOK != "" {
		// Hard-truncate the OK line to the visible width so a long
		// session path ("resumed session: /Users/.../sessions/...")
		// doesn't overflow past the right edge and look broken on a
		// narrow terminal.
		line := "✓ " + snap.statusOK
		if cols > 4 && len(line) > cols {
			line = line[:cols-3] + "..."
		}
		chat = append(chat, i.cfg.Theme.FG256(i.cfg.Theme.Tool, line), "")
	}

	// Extension notes (notify / display) live just under the
	// transcript, above the dialog/editor band. Cleared by /clear.
	if len(snap.extNotes) > 0 {
		chat = append(chat, snap.extNotes...)
		chat = append(chat, "")
	}

	// Shell-escape terminal-log block (!command). Rendered below the
	// transcript and extension notes; cleared when the next prompt is
	// sent or on /clear so it never leaks into the model conversation.
	if len(snap.shellBlock) > 0 {
		chat = append(chat, snap.shellBlock...)
		chat = append(chat, "")
	}

	// Strip trailing blank rows so the chat content sits flush
	// against the new "blank above status bar" row added by the
	// bottom-region assembly. Build() ends every message with a
	// blank separator; without this trim, the final message in
	// the transcript would have its own trailing blank plus the
	// status block's leading blank, doubling the gap.
	for len(chat) > 0 && strings.TrimSpace(chat[len(chat)-1]) == "" {
		chat = chat[:len(chat)-1]
	}
	return chat
}

// lastCols returns the current terminal width in columns.
func (i *Interactive) lastCols() int {
	cols, _ := i.cfg.Terminal.Size()
	return cols
}

// chatPage returns the number of chat rows currently visible, used
// as the page size for PageUp/PageDown.
func (i *Interactive) chatPage() int {
	_, rows := i.cfg.Terminal.Size()
	p := rows - 6 // rough reservation for status + editor + a dialog line
	if p < 4 {
		p = 4
	}
	return p
}

// scrollBy adjusts the scroll offset. Positive = up (into history).
// Clearing the parked-turn label when we're back at the bottom means
// the "viewing turn N" footer goes away automatically as soon as you
// scroll back to the live tail.
func (i *Interactive) scrollBy(delta int) {
	i.mu.Lock()
	i.scrollOffset += delta
	if i.scrollOffset < 0 {
		i.scrollOffset = 0
	}
	if i.scrollOffset == 0 {
		i.parkedTurn = 0
		i.parkedTotal = 0
	}
	if i.rend != nil {
		// VS Code's terminal is especially prone to leaving stray
		// wrapped-character fragments behind during scroll-driven
		// viewport changes. Force a full repaint on scroll, but
		// avoid a whole-screen clear because that visibly flickers.
		i.rend.Invalidate()
	}
	i.mu.Unlock()
	i.invalidate()
}

// scrollToBottom pins the view to the latest content.
func (i *Interactive) scrollToBottom() {
	i.mu.Lock()
	i.scrollOffset = 0
	i.parkedTurn = 0
	i.parkedTotal = 0
	// Reset the auto-follow baseline. scrollToBottom is the resume /
	// session-swap snap point, where the chat buffer changes length
	// wholesale; without zeroing these the next render's follow guard
	// compares the fresh transcript against a stale baseline and nudges
	// scrollOffset, which reads as a viewport jump right after resume.
	i.prevChatLen = 0
	i.prevChatCols = 0
	if i.rend != nil {
		i.rend.Invalidate()
	}
	i.mu.Unlock()
	i.invalidate()
}

// anchorScrollOffset keeps the user's reading position pinned when the
// chat buffer grows/shrinks or the viewport height changes between two
// redraws while they're scrolled up.
//
// scrollOffset is measured from the bottom of the chat buffer, so the
// top visible row is start = chatLen - scrollOffset - chatRows. To hold
// `start` constant we adjust the offset by the buffer-length delta minus
// the viewport-height delta, clamped to [0, newLen] so a shrinking
// buffer can't push it negative.
func anchorScrollOffset(offset, prevLen, newLen, prevRows, newRows int) int {
	adj := (newLen - prevLen) - (newRows - prevRows)
	offset += adj
	if offset < 0 {
		offset = 0
	}
	if offset > newLen {
		offset = newLen
	}
	return offset
}

func (i *Interactive) redraw() {
	cols, _ := i.cfg.Terminal.Size()
	// One engine snapshot per frame: chat build, status bar, and the
	// queue chips all read the same turn state, so a turn ending
	// mid-redraw can't produce a frame that is half busy, half idle.
	ts := i.turns.renderState()
	// Copy the mutex-shared frame inputs under a SHORT hold, then
	// build markdown/chroma and write the terminal without the lock:
	// the agent-event sink and background status writers must never
	// wait on a large-transcript render (Phase 2e). Everything else
	// redraw touches (view, caches, editor, renderer, dialogs,
	// scroll state) is main-loop-only and needs no lock.
	i.mu.Lock()
	snap := i.snapshotFrameLocked(ts)
	i.mu.Unlock()
	chat := i.cachedChat(cols, snap)

	// The active overlay (if any) renders between chat and the editor.
	// activeOverlay also decides key routing and cursor ownership, so
	// what is on screen and what eats keys can never disagree.
	var dialog []string
	activeOv := i.activeOverlay()
	if activeOv != nil {
		dialog = activeOv.render(cols)
	}
	if len(dialog) > 0 {
		dialog = padDialogFrame(dialog)
	}

	// Slash-command autocomplete: popup above the status line, only
	// when the editor starts with "/" and no dialog is already open.
	// Feed extension-registered commands into the suggester first so
	// they show up in tab-complete + the popup alongside the built-ins.
	i.suggest.SetJailed(i.cfg.Sandbox.Locked())
	if i.cfg.Extensions != nil {
		catalog := i.cfg.Extensions.Commands()
		extra := make([]slashCommand, 0, len(catalog))
		for _, c := range catalog {
			// The popup renders extension commands under a dedicated
			// "── extensions ───" divider, so the description doesn't
			// need to repeat the source. If the description is empty,
			// fall back to the extension name so the row isn't blank.
			desc := c.Description
			if strings.TrimSpace(desc) == "" {
				desc = "(" + c.Extension + ")"
			}
			extra = append(extra, slashCommand{
				Name: "/" + c.Name,
				Desc: desc,
			})
		}
		i.suggest.SetExtra(extra)
	}
	var suggest []string
	currentInput := i.ed.Value()
	// Slash popup renders even while the agent is busy so the user
	// can queue a destructive command (/clear, /compact, /logout,
	// /model) or a read-only one (/help, /jump, /sessions, etc.)
	// without waiting for the current turn to finish. The dispatcher
	// in runSlash already handles the busy case per-command: safe
	// ones run immediately, destructive ones cancel the turn first.
	i.fileSuggest.SetCWD(i.cfg.CWD)
	if len(dialog) == 0 && i.suggest.Active(currentInput) {
		// Budget the popup's match window from the live terminal
		// height: popup chrome (hint + blanks + indicators ≈ 5) plus
		// the status/editor band (≈ 7). Without the cap the bare-"/"
		// catalog is taller than a 24-row terminal and clips.
		_, rows := i.cfg.Terminal.Size()
		avail := rows - 12
		if avail < 4 {
			avail = 4
		}
		i.suggest.SetMaxRows(avail)
		suggest = i.suggest.Render(currentInput, i.cfg.Theme, cols)
	} else if len(dialog) == 0 && i.fileSuggest.Active(currentInput) {
		suggest = i.fileSuggest.Render(currentInput, i.cfg.Theme, cols)
	}

	// Detect overlay close (any dialog or slash/file suggestion popup
	// just transitioned from open to closed). Force a full redraw so
	// the rows the overlay occupied are guaranteed to be repainted
	// from the chat below, instead of the diff path leaving stale
	// dialog content behind. Equivalent to the user pressing ctrl+l.
	overlayOpen := len(dialog) > 0 || len(suggest) > 0
	if i.rend != nil && i.prevOverlayOpen && !overlayOpen {
		// An overlay (dialog or slash/file popup) just closed, so the
		// bottom band shrinks. On terminals where we can drop
		// scrollback, a full Clear is the simplest way to guarantee
		// the vacated rows are repainted from the chat below.
		//
		// On VS Code's terminal closing a dialog leaves the stale
		// overlay rows in the retained scrollback (we can't drop them
		// with the quiet in-place diff). Run the same full Clear() that
		// Ctrl+L uses so the scrollback is purged and the conversation
		// is repainted clean, matching what the user expects after
		// dismissing a picker. Clear() is keepScrollback-aware and
		// emits \x1b[3J there.
		i.rend.Clear()
	}
	i.prevOverlayOpen = overlayOpen
	if len(suggest) > 0 {
		// One blank row above the popup so it doesn't sit flush
		// against the chat / welcome content above.
		suggest = append([]string{""}, suggest...)
	}

	// The busy prefix (spinner glyph + message, in the terva label
	// colour) was pre-rendered into the snapshot — the spinner's
	// state is shared with turn goroutines.
	ctxMax := 0
	if m, err := provider.FindModel(i.cfg.Provider, i.cfg.Model); err == nil {
		ctxMax = m.ContextWindow
	}
	statusLines := tui.StatusBar(tui.StatusBarParams{
		Theme:          i.cfg.Theme,
		Provider:       i.cfg.Provider,
		Model:          i.cfg.Model,
		Reasoning:      i.cfg.Reasoning,
		Busy:           ts.busy,
		BusyPrefix:     snap.busyPrefix,
		CWD:            i.cfg.CWD,
		Locked:         i.cfg.Sandbox.Locked(),
		NoYolo:         i.cfg.NoYolo,
		ApprovalMode:   i.approvalModeLabel(),
		Usage:          snap.cumUsage,
		Subscription:   i.cfg.AuthMethod == "oauth",
		ContextUsed:    snap.lastCtxInput,
		ContextMax:     ctxMax,
		AutoCompacting: ts.autoCompacting,
		ChatConnected:  i.chatBridgeName(),
		ExtStatus:      i.extStatusSegments(),
		Cols:           cols,
	})
	edLines, curR, curC := i.ed.Render(cols)

	// "Sliding in" chips for messages the user typed while a turn is
	// in flight. Shown directly above the status bar so they're close
	// to the editor but don't push the chat around.
	var queue []string
	var queued []string
	if ts.agent != nil {
		queued = ts.agent.PendingQueuedMessages()
	}
	if len(queued) > 0 {
		queue = append(queue, "")
		for _, q := range queued {
			label := i.cfg.Theme.FG256(i.cfg.Theme.Accent, "  sliding in: ")
			text := truncateLine(q, cols-17)
			queue = append(queue, label+i.cfg.Theme.FG256(i.cfg.Theme.Muted, text))
		}
		// Hint row, rendered in the same muted tone as the model
		// info on the status bar so it reads as ambient metadata
		// rather than a chip. Tells the user how to recover the
		// most recent queued message back into the editor.
		hint := "  Press " + slideBackChordHint() + " to slide back into input"
		queue = append(queue, i.cfg.Theme.FG256(i.cfg.Theme.Muted, hint))
	}

	// Bottom-sticky sections (always visible, never scroll). Each
	// non-empty subsection (dialog, suggest popup, sliding-in queue)
	// is preceded by one blank row so it has air above the chat
	// content. The status block and editor get their own dedicated
	// blanks so spacing stays consistent whether or not a dialog or
	// popup is showing.
	bottom := make([]string, 0, len(dialog)+len(suggest)+len(queue)+len(edLines)+9)
	if len(dialog) > 0 {
		bottom = append(bottom, "")
	}
	bottom = append(bottom, dialog...)
	// The swarm dashboard owns the bottom of the screen while it's
	// active: it has its own inline editors for spawn (`n`) and
	// prompt (`p`), so the main input would be a confusing second
	// caret. The suggest popup, sliding-in queue, status block, and
	// main editor are all hidden underneath it. Keystrokes still
	// reach handleKey — it routes them to swarmDialog.HandleKey
	// before the editor ever sees them — so the only effect of this
	// branch is visual.
	if !i.swarmDialog.Active() {
		bottom = append(bottom, suggest...)
		bottom = append(bottom, queue...)
		bottom = append(bottom, "")
		bottom = append(bottom, statusLines...)
		bottom = append(bottom, "")
		bottom = append(bottom, edLines...)
	}

	_, rows := i.cfg.Terminal.Size()
	chatRows := rows - len(bottom)
	if chatRows < 1 {
		chatRows = 1
	}

	// Auto-follow guard: when the user has scrolled up (scrollOffset
	// > 0) and the agent appends new content below the viewport while
	// they're reading, compensate so the visible content stays
	// anchored. scrollOffset is measured from the bottom of `chat`,
	// so without compensation a growing buffer pushes the window
	// downward through the content and the lines the user was
	// reading scroll off the top.
	//
	// Skip compensation when the terminal width changed (a resize
	// reflows the whole buffer and the line-count delta no longer
	// corresponds to appended content) and when scrollOffset is 0
	// (the user is following the tail and wants new content to push
	// the view down as usual).
	//
	// The window the user sees starts at row
	//   start = len(chat) - scrollOffset - chatRows
	// so to keep `start` fixed we compensate for both the buffer growth
	// (len delta) AND the viewport-height change (chatRows delta — the
	// status band or sliding-in queue appearing during a turn).
	// Compensating only for the len delta let a shrinking chatRows pull
	// the window toward the tail, which read as the viewport jumping to
	// the bottom whenever the agent streamed text or a tool box grew.
	if i.scrollOffset > 0 && i.prevChatCols == cols && i.prevChatLen > 0 {
		i.scrollOffset = anchorScrollOffset(i.scrollOffset,
			i.prevChatLen, len(chat), i.prevChatRows, chatRows)
	}
	i.prevChatLen = len(chat)
	i.prevChatCols = cols
	i.prevChatRows = chatRows

	// Apply scroll offset to the chat slice.
	maxOffset := len(chat) - chatRows
	if maxOffset < 0 {
		maxOffset = 0
	}
	// Tail-render expansion: if the user has scrolled to (or above)
	// the top of the currently rendered tail and there are still
	// truncated messages above, widen view.TailLimit and rebuild.
	// The chat cache is keyed on tailLimit so the next cachedChatLocked
	// will re-issue Build instead of returning the stale slice. We
	// rebuild immediately so the same redraw shows the freshly-revealed
	// rows; otherwise the user would have to scroll again to see them.
	if i.scrollOffset >= maxOffset && i.view.TailLimit > 0 && i.view.TailLimit < len(i.view.Messages) {
		prevLen := len(chat)
		i.view.TailLimit += resumeTailExpandStep
		if i.view.TailLimit >= len(i.view.Messages) {
			i.view.TailLimit = 0 // unbounded
		}
		i.chatCacheValid = false
		chat = i.cachedChat(cols, snap)
		for len(chat) > 0 && strings.TrimSpace(chat[len(chat)-1]) == "" {
			chat = chat[:len(chat)-1]
		}
		// Newly-revealed rows are older messages prepended at the top.
		// scrollOffset counts from the bottom, so shift it up by the rows
		// added to keep the viewport anchored on the same content, and
		// sync the follow-guard baseline so the next render doesn't read
		// this growth as a synthetic delta and jump again.
		if grew := len(chat) - prevLen; grew > 0 {
			i.scrollOffset += grew
		}
		i.prevChatLen = len(chat)
		i.prevChatCols = cols
		maxOffset = len(chat) - chatRows
		if maxOffset < 0 {
			maxOffset = 0
		}
	}
	if i.scrollOffset > maxOffset {
		i.scrollOffset = maxOffset
	}
	if i.scrollOffset < 0 {
		i.scrollOffset = 0
	}

	var visibleChat []string
	if len(chat) <= chatRows {
		visibleChat = chat
	} else {
		end := len(chat) - i.scrollOffset
		rawStart := end - chatRows
		if rawStart < 0 {
			rawStart = 0
		}
		start := snapViewportStartToImageBlock(chat, rawStart)
		// If the snap pulled start upward (an image-block was atomic) while
		// the user is scrolling downward, the viewport would sit on the same
		// image until the user mashes down past every reserved row. Bump
		// scrollOffset past the image so one keypress always clears it.
		if start < rawStart && i.scrollOffset < i.prevScrollOffset {
			jump := rawStart - start
			i.scrollOffset -= jump
			if i.scrollOffset < 0 {
				i.scrollOffset = 0
			}
			end = len(chat) - i.scrollOffset
			rawStart = end - chatRows
			if rawStart < 0 {
				rawStart = 0
			}
			start = snapViewportStartToImageBlock(chat, rawStart)
		}
		end = start + chatRows
		if end > len(chat) {
			end = len(chat)
			start = end - chatRows
			if start < 0 {
				start = 0
			}
		}
		visibleChat = chat[start:end]
	}
	i.prevScrollOffset = i.scrollOffset
	visibleChat = clipBottomClippedImages(visibleChat)

	// A tiny "scrolled up" indicator in the top-right of the chat pane
	// so you know you're not at the bottom. When the viewport was
	// parked by /jump we include the turn number so the user remembers
	// they're reading history rather than the live conversation.
	if i.scrollOffset > 0 && len(visibleChat) > 0 {
		var text string
		if i.parkedTurn > 0 && i.parkedTotal > 0 {
			text = fmt.Sprintf("  ↑ viewing turn %d of %d - %d lines more below (pgdn / end)",
				i.parkedTurn, i.parkedTotal, i.scrollOffset)
		} else {
			text = fmt.Sprintf("  ↑ %d lines more below (end to jump)", i.scrollOffset)
		}
		note := i.cfg.Theme.FG256(i.cfg.Theme.Muted, text)
		visibleChat = append([]string{note}, visibleChat...)
		if len(visibleChat) > chatRows {
			visibleChat = visibleChat[:chatRows]
		}
	}

	// Default: the real terminal cursor sits on the main editor's
	// input position. In main-screen log mode cursor rows are relative
	// to the fixed bottom band, not the chat transcript.
	// dialogLead is 1 when the bottom region prepends a blank above
	// the dialog block (whenever a dialog is showing) so popup-side
	// cursor positions still land in the right cell.
	dialogLead := 0
	if len(dialog) > 0 {
		dialogLead = 1
	}
	// +2 accounts for the blank row above statusLines (so the
	// status block has air above it) and the blank row between
	// statusLines and edLines (input breathing room). Without
	// these the rendered cursor would land on a blank instead of
	// inside the editor row.
	cursorRow := dialogLead + len(dialog) + len(suggest) + len(queue) + 1 + len(statusLines) + 1 + curR
	cursorCol := curC
	if activeOv != nil {
		r, c := -1, 0
		if activeOv.cursor != nil {
			r, c = activeOv.cursor(cols)
		}
		switch {
		case r >= 0:
			cursorRow = dialogLead + r
			cursorCol = c
		case activeOv.hideCaretFallback:
			// The overlay covers the editor but has no caret of its
			// own (dashboard list, extension panel). Without hiding,
			// the default cursorRow points at the obscured editor
			// row and the terminal draws a stray block mid-chat.
			cursorRow = -1
			cursorCol = 0
		}
	}
	_ = visibleChat // maintained for legacy scroll state/indicators; DrawLog owns chat viewport.
	i.rend.DrawLog(chat, bottom, cursorRow, cursorCol)
}

func hasImageEscape(line string) bool {
	return strings.Contains(line, "\x1b]1337;File=") || strings.Contains(line, "\x1b_G")
}

// snapViewportStartToImageBlock treats inline images as atomic blocks for
// scrolling. Terminal image protocols draw from a single escape row into a
// separate graphics layer; the following blank rows are only terva's reserved
// footprint. If the viewport starts on one of those blank rows, there is no
// correct partial-image state to render. Snap back to the escape row instead
// so the image is either shown from its beginning or skipped entirely.
func snapViewportStartToImageBlock(chat []string, start int) int {
	if start <= 0 || start >= len(chat) {
		return start
	}
	if hasImageEscape(chat[start]) || !isBoxBlankLine(chat[start]) {
		return start
	}
	for k := start - 1; k >= 0; k-- {
		line := chat[k]
		if hasImageEscape(line) {
			return k
		}
		if !isBoxBlankLine(line) {
			break
		}
	}
	return start
}

// filterHiddenTranscriptMessages drops messages that exist for the
// model's benefit but shouldn't appear in the human-facing transcript
// — today just the tool-image mirror (a provider-wire artifact; see
// core.IsToolImageMirror, the single source of truth shared with
// compaction so the two can't drift on what counts as a mirror).
func filterHiddenTranscriptMessages(msgs []provider.Message) []provider.Message {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]provider.Message, 0, len(msgs))
	for _, m := range msgs {
		if core.IsToolImageMirror(m) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func clipBottomClippedImages(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	out := append([]string(nil), lines...)
	for i, line := range out {
		if !hasImageEscape(line) {
			continue
		}
		// Image blocks render as: image escape, zero or more blank
		// reservation rows, then the muted "image - ..." info line,
		// then one trailing blank. If the info line isn't visible in
		// the current chat slice, the image would paint down into the
		// fixed status bar area. Suppress that image for this frame.
		//
		// When the image lives inside a tool box, the reservation rows
		// are wrapped in vertical box edges ("│  ...  │"); those rows
		// look non-blank under a naive whitespace check but are still
		// reservation rows for this scan, so treat them as blank.
		foundInfo := false
		for j := i + 1; j < len(out); j++ {
			if strings.Contains(out[j], "image - ") {
				foundInfo = true
				break
			}
			if !isBoxBlankLine(out[j]) {
				break
			}
		}
		if !foundInfo {
			out[i] = ""
		}
	}
	return out
}

// isBoxBlankLine reports whether line is visually empty after
// stripping ANSI escape sequences, surrounding whitespace, and the
// vertical box edges drawn by the tool-box renderer. Used by
// clipBottomClippedImages so an image's reservation rows still count
// as blank when those rows are wrapped in "│  ...  │" inside a tool box.
func isBoxBlankLine(line string) bool {
	stripped := stripANSIBytes(line)
	stripped = strings.TrimSpace(stripped)
	stripped = strings.Trim(stripped, "│")
	stripped = strings.TrimSpace(stripped)
	return stripped == ""
}

// stripANSIBytes removes ANSI CSI escape sequences (ESC '[' ... final
// byte) from s without pulling in the regexp package. Mirrors the
// internal helper in package tui; the duplicated copy avoids exporting
// it just for one caller.
func stripANSIBytes(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			end := i + 2
			for end < len(s) {
				c := s[end]
				end++
				if c >= 0x40 && c <= 0x7e {
					break
				}
			}
			i = end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func truncateLine(s string, n int) string {
	if n <= 0 {
		return ""
	}
	// Collapse newlines — chips are single line.
	s = strings.ReplaceAll(s, "\n", " ↩ ")
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 3 {
		return strings.Repeat(".", n)
	}
	return string(runes[:n-3]) + "..."
}

// formatInt is a tiny strconv.Itoa shim; keeps the handler above
// from needing a strconv import just for one call.
func formatInt(n int) string {
	return fmt.Sprintf("%d", n)
}
