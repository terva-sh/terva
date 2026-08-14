package modes

// Returning a withdrawn prompt to the composer — stage 3 of
// docs/proposals/withdraw-cancelled-prompt.md.
//
// The daemon withdraws a prompt whose turn was interrupted before the model
// said anything, so it never becomes part of the permanent transcript. From
// here the message simply disappears; without this the user then has to retype
// what they only just sent, which is most of the annoyance the feature set out
// to remove.

import (
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// withdrawableDraft is the editor state a dispatched prompt came from, held
// until that prompt is either answered or withdrawn.
//
// It is the EDITOR state and not the dispatched string, and that distinction is
// the whole reason this is worth doing properly. By the time a prompt is on the
// wire it has been through SubmitValue (every `[pasted text #N +L lines]`
// placeholder expanded to its body), ExpandFileChips (`[file:x]` expanded to an
// absolute path) and image reconciliation. Putting THAT back in the composer
// would face the user with the wall of expanded paste they were never shown,
// paths they did not type, and no attachments. Restoring the editor state gives
// back exactly what they were looking at — placeholders, chips, cursor — and
// carries the pending images alongside, the same pairing draftStash makes for
// the same reason.
type withdrawableDraft struct {
	ed     tui.EditorState
	images []clipboardImageAttachment
	// text is the prompt as dispatched, matched against the withdrawal event so
	// this client only reclaims its OWN message. A workspace session can be
	// driven from several clients at once, and a withdrawal broadcast is seen by
	// all of them; without this, another device's interrupted prompt would
	// appear in this composer.
	text string
}

// armWithdrawableDraft records the editor state a prompt was dispatched from.
// Called at dispatch, after the submit path has decided this really is a prompt
// (not a slash command, not a shell escape) and cleared the editor.
//
// Each dispatch replaces the last: a withdrawal only ever concerns the trailing
// message, so the most recent dispatch is the only one that can be reclaimed.
func (i *Interactive) armWithdrawableDraft(ed tui.EditorState, images []clipboardImageAttachment, text string) {
	i.withdrawDraft = &withdrawableDraft{ed: ed, images: images, text: text}
}

// restoreWithdrawnPrompt puts a withdrawn prompt back in the composer. Runs on
// the main goroutine (the editor has no lock of its own — see runOnMain).
//
// Nothing is restored when the text does not match what this client sent, which
// covers both a foreign client's withdrawal and a stale arm.
func (i *Interactive) restoreWithdrawnPrompt(text string) {
	d := i.withdrawDraft
	if d == nil || text == "" || d.text != text {
		// Deliberately does NOT disarm. A withdrawal that is not ours belongs to
		// another client on the same session, and letting it consume this
		// client's draft would mean somebody else's Esc silently costing the
		// user their own reclaimable prompt.
		return
	}
	i.withdrawDraft = nil

	// A draft typed since the prompt went out is the user's newest intent and
	// outranks the one coming back. Park the returning draft instead of
	// overwriting it: ctrl+s swaps them, and stashRows says so on screen. The
	// alternative — clobbering — would fix a message they lost by losing a
	// different one.
	if !i.ed.IsEmpty() {
		// Three drafts, one park: a composer in use, something already set
		// aside, and this one arriving. Overwriting the stash would destroy a
		// draft to rescue a draft, so nothing moves — the prompt was recorded in
		// the input history at dispatch, and that is where it stays recoverable.
		if i.stash != nil {
			i.setStatusOK(i18n.T("withdrawn — press ← to bring it back"))
			i.invalidate()
			return
		}
		i.stash = &draftStash{ed: d.ed, images: d.images}
		i.stashHintArmed = false
		i.setStatusOK(i18n.T("withdrawn — set aside, press ctrl+s to take it back"))
		i.invalidate()
		return
	}

	i.ed.Restore(d.ed)
	// Append rather than assign, matching popStashedDraft: an image pasted
	// between the dispatch and this restore is kept.
	i.clipboardImages = append(d.images, i.clipboardImages...)
	i.stashHintArmed = false
	i.inputHistoryIndex = -1
	i.setStatusOK(i18n.T("withdrawn — your message is back in the composer"))
	i.invalidate()
}
