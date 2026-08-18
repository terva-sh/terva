package workspace

import (
	"context"
	"errors"
	"os"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// The session state sidecar on the wire. core owns the file
// (packages/core/session_state.go) and ctrlproto owns the shapes
// (session_state.go); this is the daemon in between, and it is deliberately
// thin — it resolves a session id to a path, holds one lock across the
// read-modify-write, and translates core's one refusable error into a wire code.
//
// Serving it from HERE is the point of the feature: the draft belongs to the
// session rather than to whichever front end typed it, so the TUI and the web
// panel see the same unsent text instead of each keeping a private copy the
// other never learns about.
//
// Optional controller, so the verbs do not ripple to the other
// WorkspaceService implementers (a replay carrier has no sessions directory to
// keep a sidecar in).
var _ ctrlproto.SessionStateController = (*Workspace)(nil)

// statePath resolves a wire session id to its state sidecar's path, or "" if
// the id is not one this workspace will touch.
//
// The id must be path-safe (validSessionID: it becomes a filename) and must
// name a session that EXISTS — live, or a transcript on disk. Without the
// existence check a client could strew .state.json files for sessions that
// never were, and nothing would ever collect them: the sidecar table reaps a
// state file when its transcript is deleted, pruned or archived, so a state
// file whose transcript never existed is unreachable litter.
func (w *Workspace) statePath(sess string) string {
	if !validSessionID(sess) {
		return ""
	}
	transcript := w.sessionPath(sess)
	if w.existing(sess) == nil {
		if _, err := os.Stat(transcript); err != nil {
			return ""
		}
	}
	return core.SessionStatePathFor(transcript)
}

// SessionState reads a session's durable client state.
//
// A session with no sidecar yields an empty result rather than an error, which
// is core's LoadSessionState contract carried onto the wire: the state is a
// convenience and the transcript is the data, so "there is no draft" is an
// answer and not a failure. Only an id that names no session at all is refused.
func (w *Workspace) SessionState(_ context.Context, sess string) (ctrlproto.SessionStateResult, error) {
	path := w.statePath(sess)
	if path == "" {
		return ctrlproto.SessionStateResult{}, ctrlproto.ErrNoSession
	}
	w.stateMu.Lock()
	defer w.stateMu.Unlock()

	var out ctrlproto.SessionStateResult
	if d, ok := core.LoadSessionState(path).Composer(); ok {
		draft := ctrlproto.ComposerDraft{Text: d.Text, Source: d.Source}
		// Only a real stamp travels. A hand-edited file with no updated_at
		// would otherwise send year 1 as though it were a time, and a client
		// would render "written 2025 years ago".
		if !d.UpdatedAt.IsZero() {
			at := d.UpdatedAt
			draft.UpdatedAt = &at
		}
		out.Composer = &draft
	}
	return out, nil
}

// SetComposerDraft writes the composer tenant and leaves every other tenant
// alone. Blank text clears it.
//
// The whole read-modify-write happens under stateMu: the file itself is written
// atomically, so the risk is not a torn file but a LOST tenant — two front ends
// saving at once would each write back the document they loaded, and whichever
// landed second would erase the other's work. The lock is workspace-wide rather
// than per-session because the critical section is two small file operations
// and drafts are saved on a debounce, not per keystroke.
func (w *Workspace) SetComposerDraft(_ context.Context, sess string, p ctrlproto.ComposerDraft) error {
	path := w.statePath(sess)
	if path == "" {
		return ctrlproto.ErrNoSession
	}
	w.stateMu.Lock()
	defer w.stateMu.Unlock()

	state := core.LoadSessionState(path)
	if err := state.SetComposer(core.ComposerDraft{Text: p.Text, Source: p.Source}); err != nil {
		return err
	}
	if err := core.SaveSessionState(path, state); err != nil {
		// The cap is the one failure a user can act on — shorten the message,
		// or send it. Localized and given a distinct code so a front end can
		// say so plainly, instead of reporting an internal error for something
		// the user did on purpose. Nothing was written; the previous draft (if
		// any) is still there.
		if errors.Is(err, core.ErrSessionStateTooLarge) {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("the draft is too large to keep"))
		}
		return err
	}
	return nil
}
