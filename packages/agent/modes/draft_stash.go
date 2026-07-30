package modes

// The draft stash (ctrl+s): park a half-written message, answer the
// question the agent just asked, and get the draft back automatically
// after the answer goes out.
//
// The scenario this exists for: the user starts composing a reply while
// the agent is still responding, and the response turns out to end in a
// question that must be answered before the draft makes sense to send.
// Queued messages (enter while busy) already have Alt+Up; this covers
// the draft that was never submitted.

import (
	"context"
	"strings"
	"unicode/utf8"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// draftStash is one parked draft: the complete editor state (cursor and
// hidden paste/file placeholder bodies included — a plain string round
// trip through SetValue would orphan them) plus the clipboard images
// that were pending when it was set aside. The images must travel with
// the draft: left on the pending list, the interposed answer's submit
// would reconcile them against text that lacks their markers and
// silently drop them.
type draftStash struct {
	ed     tui.EditorState
	images []clipboardImageAttachment
}

// stashHintMinRunes is how much typed draft arms the pre-stash hint.
// Long enough that a quick "ok"-sized ack doesn't trigger it, short
// enough to surface within the first word or two of a real reply.
const stashHintMinRunes = 4

// keyStashDraft (ctrl+s) parks the current draft, brings a parked one
// back, or — with a draft on both sides — swaps them. The parked draft
// also returns on its own after the next message is sent (see
// popStashedDraft), so the manual restore is for changing your mind
// early.
func (i *Interactive) keyStashDraft(context.Context, tui.Key) keyOutcome {
	hasDraft := !i.ed.IsEmpty()
	switch {
	case i.stash == nil && !hasDraft:
		// Nothing to park, nothing to bring back.
		return keyHandled
	case i.stash == nil:
		i.stash = &draftStash{ed: i.ed.State(), images: i.clipboardImages}
		i.ed.Clear()
		i.clipboardImages = nil
	case !hasDraft:
		st := i.stash
		i.stash = nil
		i.ed.Restore(st.ed)
		i.clipboardImages = append(st.images, i.clipboardImages...)
	default:
		cur := &draftStash{ed: i.ed.State(), images: i.clipboardImages}
		i.ed.Restore(i.stash.ed)
		i.clipboardImages = i.stash.images
		i.stash = cur
	}
	i.stashHintArmed = false
	i.inputHistoryIndex = -1
	i.invalidate()
	return keyHandled
}

// popStashedDraft returns the parked draft to the editor once the
// message that displaced it has gone out. Called from the submit path
// right after a prompt is dispatched (sent or queued) — not for slash
// commands or ! escapes, which don't consume the user's conversational
// turn; the parked row stays visible until a real message goes.
func (i *Interactive) popStashedDraft() {
	if i.stash == nil {
		return
	}
	st := i.stash
	i.stash = nil
	i.ed.Restore(st.ed)
	// Append rather than assign: the submit just cleared the pending
	// list, but a paste raced between dispatch and here is kept.
	i.clipboardImages = append(st.images, i.clipboardImages...)
	i.stashHintArmed = false
	i.inputHistoryIndex = -1
}

// refreshStashHint re-evaluates the pre-stash nudge after every key.
// It arms only while a turn is in flight — a reply drafted against a
// response that hasn't finished arriving is the case the stash serves —
// and once armed it stays through the turn's end (the moment the
// agent's question actually lands) until the draft empties or the
// stash takes over. Composing while idle never shows it.
func (i *Interactive) refreshStashHint() {
	if i.stash != nil || i.ed.IsEmpty() {
		i.stashHintArmed = false
		return
	}
	if i.stashHintArmed || !i.turns.Busy() {
		return
	}
	v := strings.TrimSpace(i.ed.Value())
	// Slash commands and ! escapes aren't drafts of a reply; they run
	// fine while busy and never need parking.
	if looksLikeSlashCommand(v) || strings.HasPrefix(v, "!") {
		return
	}
	if utf8.RuneCountInString(v) >= stashHintMinRunes {
		i.stashHintArmed = true
	}
}

// stashRows renders the parked-draft row (and its recovery hint) or,
// with nothing parked yet, the one-line nudge that the stash exists.
// Same visual family and screen slot as the "sliding in" queue chips:
// muted, above the status bar, never in scrollback. Empty when there
// is nothing to say.
func (i *Interactive) stashRows(cols int) []string {
	th := i.cfg.Theme
	if i.stash != nil {
		preview := draftPreviewLine(i.stash.ed.Value())
		return []string{
			"",
			th.FG256(th.Accent, "  "+i18n.T("set aside: ")) + th.FG256(th.Muted, truncateLine(preview, cols-17)),
			th.FG256(th.Muted, "  "+i18n.T("it returns after your next message — or press ctrl+s to take it back now")),
		}
	}
	if i.stashHintArmed {
		return []string{
			"",
			th.FG256(th.Muted, "  "+i18n.T("press ctrl+s to set this draft aside while you answer — it comes back after you send")),
		}
	}
	return nil
}

// draftPreviewLine picks the first non-blank line of a parked draft for
// the "set aside:" chip, marking anything beyond it with an ellipsis so
// a multi-line draft doesn't read as a one-liner.
func draftPreviewLine(v string) string {
	lines := strings.Split(v, "\n")
	first, rest := "", false
	for _, ln := range lines {
		if first == "" && strings.TrimSpace(ln) != "" {
			first = ln
			continue
		}
		if first != "" && strings.TrimSpace(ln) != "" {
			rest = true
			break
		}
	}
	if first == "" {
		first = lines[0]
	}
	if rest {
		first += " …"
	}
	return first
}
