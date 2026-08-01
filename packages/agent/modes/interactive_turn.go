package modes

// The turn lifecycle: prompt dispatch, manual compaction, and the
// streaming pacer. The TUI drives the ctrlproto WorkspaceService; the
// turn engine (claim/queue, event consumption, turn policy) is
// daemon-side — see interactive_ctrlproto.go.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// buildStudyPrompt returns the canned prompt the /study command
// submits to the agent.
//
// With no argument, /study targets the current directory — the
// historical behaviour. With an argument, /study targets that path
// instead; either a directory ("read every file in here") or a
// single file ("read this file"). The argument can be:
//
//   - a relative path (resolved against cwd)
//   - an absolute path
//   - an @-picker chip, which has already been expanded to an
//     absolute path by expandFileChips before runSlash sees it
//
// The path is stat'd to pick the right wording ("directory" vs
// "file"). If the path doesn't exist, we still build a sensible
// prompt rather than erroring — the agent will surface the
// missing-file failure itself when it tries to read it, which is
// more useful than a refusal here.
func buildStudyPrompt(arg, cwd string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return i18n.P("study.dir.current", "Read and understand everything in the current directory.")
	}
	abs := arg
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, abs)
	}
	display := arg
	if rel, err := filepath.Rel(cwd, abs); err == nil && !strings.HasPrefix(rel, "..") {
		display = rel
	}
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		return i18n.P("study.file", "Read and understand the file %s.", display)
	}
	return i18n.P("study.dir", "Read and understand everything in the directory %s.", display)
}

// applyModelSelection switches the active model (and provider, if the
// new model belongs to a different one). It rebuilds the underlying
// client when needed so the provider wire-protocol matches.
// cancelAndWaitForIdle cancels the active turn (if any) and blocks
// briefly until the turn goroutine has released the turn slot. Used
// before destructive slash commands so transcript-mutating work
// (/clear, /compact, /logout, /login completion, cross-provider
// /model swap) doesn't race with the still-running stream.
//
// The wait is bounded; if the turn doesn't release within the timeout
// we proceed anyway. Worst case is a brief overlap that the agent's
// own mutex protects against.
func (i *Interactive) cancelAndWaitForIdle() {
	if !i.turns.Busy() {
		return
	}
	i.turns.cancelActive()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !i.turns.Busy() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// runCompact routes a manual /compact through the ctrlproto carrier. It
// runs in a goroutine (inside runCarrierCompact) so the ui stays
// responsive; esc/ctrl+c cancel via the compact context. Policy
// compactions (pre-turn auto-compact, the 413 retry) are daemon-side and
// surface through the pump's compact_start/compact_end events, never here.
func (i *Interactive) runCompact(parent context.Context) {
	if !i.ready() {
		i.setStatusErr(i18n.T("not logged in. type /login first."))
		return
	}
	i.runCarrierCompact(parent)
}

func (i *Interactive) startTurn(parent context.Context, prompt string) {
	i.startTurnWithImages(parent, prompt, nil)
}

func (i *Interactive) startTurnWithImages(parent context.Context, prompt string, images []provider.ImageBlock) {
	if !i.ready() {
		return
	}
	// Surface a dropped-image note up front: the provider layer
	// silently strips images for models without the image-input
	// capability (better than 400-bricking the session), but silence
	// here would leave the user wondering why the model never saw
	// their screenshot.
	if len(images) > 0 {
		i.mu.Lock()
		provName, modelID := i.cfg.Provider, i.cfg.Model
		i.mu.Unlock()
		if m, err := provider.FindModel(provName, modelID); err == nil && !m.Has(provider.CapImageInput) {
			i.mu.Lock()
			i.statusErr = i18n.T("note: %s can't see images — %d attachment(s) will be dropped", modelID, len(images))
			i.mu.Unlock()
			i.invalidate()
		}
	}

	// Dispatch through the ctrlproto WorkspaceService: the pre-turn
	// auto-compact, the claim-or-queue slot dance, and the post-Prompt
	// policy block (rescue picker, 413 retry, persistence) are all
	// daemon-side.
	i.startTurnCarrier(parent, prompt, images)
}

// resetTurnUI clears the per-turn UI state right after the turn slot is
// claimed — shared by the legacy and carrier dispatch paths.
func (i *Interactive) resetTurnUI() {
	i.mu.Lock()
	i.spin.Start()
	i.statusErr = ""
	i.statusOK = ""
	i.toolCalls = map[string]*tui.ToolCallView{}
	i.toolOrder = nil
	i.shellBlock = nil // sending a prompt clears any parked shell-escape log
	i.extNotes = nil   // ext notes are one-shot; a new prompt clears them
	i.scrollOffset = 0 // jump back to the bottom on new turn
	// Reset the auto-follow baseline so the very next render doesn't
	// see a synthetic shrink between "last frame had the previous
	// turn's tool overlay" and "this frame had it cleared above".
	// Without this, the guard reads delta = -(rows in cleared
	// overlay) and decrements scrollOffset, which on terminals that
	// mirror terva's pane scroll into the host scrollbar visibly
	// yanks the viewport. See autofollow_shrink_test.go.
	i.prevChatLen = 0
	i.prevChatCols = 0
	// Lift the resume tail cap once the user starts interacting. The cap
	// is purely a first-paint optimization; keeping it active during a
	// turn makes the rendered chat a sliding window, so appended messages
	// push older ones off the TOP and the renderer repaints fully,
	// snapping the terminal's native scrollback to the bottom on every
	// streamed chunk. A fresh session has no cap (append-only), which is
	// why the jump only shows in resumed sessions; dropping the cap here
	// makes resumed turns append-only too.
	i.view.TailLimit = 0
	i.parkedTurn = 0 // starting a turn clears the /jump parked state
	i.parkedTotal = 0
	i.helpBlock = nil // hide the help block once the user asks something
	i.mu.Unlock()
	i.invalidate()
}

// noteCondensingBeforeSend / noteCondensingBeforeRetry are the two
// auto-compact heads-up notes, marked once so the render site and
// stripAutoCompactNotes translate the SAME source. Matching on the
// English literal "condensing history" would silently stop stripping
// the note once it's translated, leaving a stale banner in the chat.
var (
	noteCondensingBeforeSend  = i18n.M("context near limit — condensing history before sending...")
	noteCondensingBeforeRetry = i18n.M("request was too large. condensing history before retrying ...")
)

func stripAutoCompactNotes(notes []string) []string {
	if len(notes) == 0 {
		return notes
	}
	sendNote := i18n.T(noteCondensingBeforeSend)
	retryNote := i18n.T(noteCondensingBeforeRetry)
	out := notes[:0]
	for _, n := range notes {
		if strings.Contains(n, sendNote) || strings.Contains(n, retryNote) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// autoCompactNoteLine returns a styled chat-area note for the
// inline auto-compact heads-up. Lives in extNotes so it survives
// the busy-spinner overwrite of the status row.
func autoCompactNoteLine(th tui.Theme, msg string) string {
	return "  " + th.FG256(th.Warning, "⚠ "+msg)
}

// stallNudgeGlyph leads the coalesced "loop detected" nudge note. It doubles as
// the marker the carrier handler matches to find and replace that one line, so
// repeated nudges update a single counted note instead of stacking.
const stallNudgeGlyph = "⟳"

// stallRefuseGlyph and stallStopGlyph lead the detector's two ACTING rungs: a
// call answered without being dispatched, and the turn ended because that was
// ignored too. Each is its own coalescing marker (see coalesceHatchNote), so a
// refusal never overwrites the nudge line that preceded it — the two counts are
// separate facts about the same loop. Single-width, like every other hatch
// glyph, so a note cannot widen a frame.
const (
	stallRefuseGlyph = "⊘"
	stallStopGlyph   = "⊗"
)

// retryGlyph leads the coalesced transient-retry note and is its coalescing
// marker, like the stall glyphs above. Deliberately NOT ⟳ — that one already
// means "the model is looping", and a provider backoff is the opposite
// situation (nothing is repeating; something upstream is unavailable).
// Single-width, checked: a wider glyph would push the note past the frame.
const retryGlyph = "⧖"

// shortDuration renders a backoff wait the way a person says it: "8s", "1m",
// "1m30s" — never "1m0s" or "8.000000001s", which is what Duration.String does
// with a computed value.
func shortDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	mins := int(d / time.Minute)
	secs := int((d % time.Minute) / time.Second)
	if secs == 0 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dm%ds", mins, secs)
}

// clampNote trims a provider's own error prose to something that fits beside a
// note's own words. Providers write whole sentences ("Our servers are currently
// overloaded. Please try again later.") and the note already carries the
// provider, the attempt, and the wait.
func clampNote(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return strings.TrimRight(string(r[:max-1]), " ") + "…"
}

// coalesceHatchNote strips any existing note led by glyph out of notes and
// returns the count that the replacement line should carry: one more than the
// line it replaced, or 1 when there was none. A wedged run fires these many
// times over, and a growing stack of near-identical notes is noise — one line
// that counts up says the same thing in a line the eye can hold.
//
// The count self-resets when the line is gone, since notes clear on the next
// prompt; it deliberately does not live in the event, which reports single
// occurrences.
func coalesceHatchNote(notes *[]string, glyph string, count int) int {
	kept := (*notes)[:0:0]
	found := false
	for _, note := range *notes {
		if strings.Contains(note, glyph) {
			found = true
			continue
		}
		kept = append(kept, note)
	}
	*notes = kept
	if found {
		return count + 1
	}
	return 1
}

// hatchNoteLine styles a stuck-loop-hatch heads-up (a detector nudge or a model
// escalation) as an inline chat-area note, the same shape as autoCompactNoteLine
// but with a caller-chosen glyph and colour so a nudge, a swap, and a failed swap
// read distinctly. Lives in extNotes so it survives the busy-spinner overwrite.
func hatchNoteLine(th tui.Theme, color int, glyph, msg string) string {
	return "  " + th.FG256(color, glyph+" "+msg)
}

// orDash renders a possibly-empty label as an em dash, so a hatch note never
// reads "escalated to  ()" if a field somehow arrives blank.
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// assistantText returns the concatenated text of every TextBlock in
// m. Used by the streaming-view dedupe guard to tell when a live
// streamed reply has already been promoted into the transcript.
func assistantText(m provider.Message) string {
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

// paintPaceRate is how many runes the streaming pacer releases per tick
// once the stream is FLUSHING — the final message has landed, no more
// deltas are coming, and there is nothing left to smooth against. With a
// 16ms tick, 6 runes/tick is ~375 runes/s — fast enough that a 500-rune
// summary finishes in ~1.3s, slow enough to look like a human typing.
const paintPaceRate = 6

// paceDrainTicks is the jitter buffer's horizon, in ticks: while deltas
// are still arriving, the pacer aims to spread whatever is queued across
// this many ticks rather than draining at a fixed rate (see paceRate).
// 25 ticks x 16ms is ~400ms of buffered latency — long enough to bridge
// the silences between a coarse provider's chunks, short enough that the
// text never feels detached from the model that is producing it.
const paceDrainTicks = 25

// paceMaxRate caps the jitter buffer's catch-up, in runes/tick. It binds
// only on a deep buffer — a provider that hands over a whole reply in one
// delta, or a local model streaming faster than the pacer's steady state,
// which the old fixed rate could never keep up with at all. 24 runes/tick
// is ~1500 runes/s: brisk enough to track any real provider, bounded so a
// deep buffer still reveals rather than dumps.
const paceMaxRate = 24

// paintPaceInterval is the tick interval for the streaming pacer.
// 16ms lines up with the redraw throttle so we never paint faster
// than the terminal can keep up.
const paintPaceInterval = 16 * time.Millisecond
