package ctrlproto

import "strings"

// Addressing: sessions, and the workspace itself.
//
// The envelope's Sess is the SOLE routing truth — an event body carries no
// session id, and the pump stamps every frame with the id from the subscription
// (see serveState.subscribe). That invariant is worth keeping, so a frame that
// is not about a session needs an address rather than a second addressing axis.
//
// The empty string is NOT available for that. It already means "the workspace's
// current/default session" on a command, and resolving it will MINT a session on
// disk when none exists — it is a late-bound session id, not the absence of one.
// Two more reasons it cannot be retconned: serveState.subs is keyed by the raw
// string, so a client subscribed to both "" and the id it resolves to would get
// two pumps and every event twice; and ctrlclient dispatches on c.subs[sess], so
// an event stamped "" is dropped by every client that subscribed with a real id.
//
// So: a small namespace of reserved addresses, marked by a leading '#'. One
// addressing axis, not two — which is why this slots into machinery that already
// keys on Sess (a map lookup in ctrlclient, an equality test in the web client)
// instead of making every client demux on a pair.
//
// See docs/proposals/control-plane-protocol.md §"Addressing".
const (
	// AddrWorkspace addresses the workspace itself: one per connection.
	//
	// Subscribe to it to receive facts that are true of the daemon rather than
	// of any session — a provider login landing, the session set changing shape,
	// the locale changing, a workspace-scoped surface updating. Commands are not
	// addressed here; a method that does not concern a session simply ignores
	// Sess (i18n.catalog and files.list are the existing precedent).
	AddrWorkspace = "#workspace"

	// reservedPrefix marks an address that is not a session id.
	reservedPrefix = "#"
)

// IsReservedAddr reports whether sess names something other than a session.
//
// A session id is a file-name stem, and the workspace's own validSessionID is a
// PATH-SAFETY check rather than a format check — "#workspace" sails through it.
// So a reserved address must be excluded explicitly wherever a session is
// resolved, or the workspace will cheerfully try to materialize a session file
// by that name, and a client could create a session that shadows an address.
func IsReservedAddr(sess string) bool {
	return strings.HasPrefix(sess, reservedPrefix)
}
