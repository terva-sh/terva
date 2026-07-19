package ctrlproto

import "context"

// Draft sessions on the wire. A fresh Stage chat DEFERS its greeting (held in
// memory, not written to disk), so a character opened only for preview stays a
// meta-only draft the prune gates discard and the session list hides.
// DiscardDraft lets the front end reclaim such a draft the moment the user
// navigates away without sending — closing its live session (and any extension
// subprocesses it holds) rather than waiting for daemon shutdown or the next
// boot-prune. It is a guarded no-op on any session that is NOT an unpromoted
// draft (one whose greeting has flushed, or a coding session), so it can never
// discard real work.
//
// DraftController is OPTIONAL, like the other Stage controllers, so the verb does
// not ripple to the base WorkspaceService implementers.
type DraftController interface {
	DiscardDraft(ctx context.Context, sess string) error
}
