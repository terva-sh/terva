package ctrlproto

import (
	"context"
	"time"

	"terva.sh/terva/packages/core"
)

// Session state on the wire: the per-session client state core keeps beside the
// transcript at <session>.state.json (packages/core/session_state.go). Its
// first tenant is the composer draft — the message the user typed and did not
// send. Serving it from the daemon rather than from each front end is what
// makes it ONE draft: the TUI and the web panel see the same unsent text
// instead of each keeping a private copy that the other never learns about.
//
// TWO verbs, asymmetric on purpose, and the asymmetry is the design:
//
//   - READS are whole-document (MethodSessionState). A front end asks once on
//     bind and gets every tenant it understands. Reading a tenant you do not
//     know costs nothing.
//   - WRITES are tenant-scoped (MethodSessionSetComposer). There is no
//     sessions.set_state, because a whole-document setter would re-open, one
//     layer up, exactly the hole core closed underneath: a client that names
//     only the tenants it knows about DELETES the ones it does not, so an older
//     web build would silently drop a newer TUI's state on every keystroke.
//     core carries unknown tenants verbatim precisely so that cannot happen,
//     and the wire must not undo it.
//
// A "patch" setter — one verb, absent field means leave alone — was the
// alternative, and it fails on the client side of this boundary rather than on
// this one: in TypeScript `{composer: undefined}` and `{}` serialize
// identically, so "leave it alone" and "clear it" would rest on a distinction
// the calling language erases. A tenant per verb costs a constant and a table
// row, and it cannot be got wrong.

// The Source domain, ALIASED from core rather than re-spelled: the wire and
// the file must agree on these two strings, and an alias cannot drift from its
// source the way a copied literal can. Aliases so a front end reading only
// ctrlproto still finds the domain it has to send.
const (
	ComposerSourceUser       = core.ComposerSourceUser
	ComposerSourceSuggestion = core.ComposerSourceSuggestion
)

// ComposerDraft is the composer tenant on the wire: one unsent message and
// where it came from. It is both the params of MethodSessionSetComposer and the
// composer field of a MethodSessionState result.
type ComposerDraft struct {
	// Text is the draft as plain text. The TUI sends Editor.SubmitValue(), so
	// paste and file placeholders arrive already expanded — a stored
	// "[pasted text #1]" would point at a map that no longer exists.
	//
	// Empty (or blank) CLEARS the draft: an empty draft is not a draft, and
	// this is how a front end says the composer is now empty.
	Text string `json:"text"`
	// Source is ComposerSourceUser or ComposerSourceSuggestion. It decides how
	// the text comes BACK: the user's own words are restored into the composer,
	// the model's are offered as a ghost to accept.
	// Handing the machine's line back as the user's own is the failure this
	// field exists to prevent, so an unset Source means "user" — the reading
	// that can only under-claim.
	Source string `json:"source,omitempty"`
	// UpdatedAt is when the draft was last written, for a client that wants to
	// say how old the thing it just restored is. Server-set: a setter leaves it
	// nil and core stamps it.
	//
	// A POINTER because `omitempty` does nothing to a time.Time — the same
	// reason SecretInfo.LastSeen is one. Without it every set_composer would
	// carry a year-1 timestamp on the wire.
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// SessionStateResult is the payload of a [MethodSessionState] response: every
// tenant this binary knows about, each absent when unset.
//
// Tenants are added as FIELDS here. A client that predates one ignores it, so
// widening this struct never breaks a caller — which is what makes the
// whole-document read safe where a whole-document write is not.
type SessionStateResult struct {
	// Composer is the unsent message, absent when there is no draft.
	Composer *ComposerDraft `json:"composer,omitempty"`
}

// SessionStateController serves the two state verbs.
//
// OPTIONAL, like DraftController and the Stage controllers: a service that does
// not implement it answers CodeUnsupported rather than failing to compile, so
// the verbs do not ripple out to every WorkspaceService implementer.
type SessionStateController interface {
	// SessionState reads sess's state. A session with no sidecar is not an
	// error — it is a zero result, the same tolerance core's LoadSessionState
	// takes, and for the same reason: the state is a convenience and the
	// transcript is the data.
	SessionState(ctx context.Context, sess string) (SessionStateResult, error)
	// SetComposerDraft writes the composer tenant, leaving every other tenant
	// untouched. Blank text clears it. An over-cap draft is REFUSED with an
	// error (core's ErrSessionStateTooLarge) and nothing is written, so the
	// front end can tell the user their draft was not kept rather than storing
	// a prefix of it that looks whole.
	SetComposerDraft(ctx context.Context, sess string, d ComposerDraft) error
}
