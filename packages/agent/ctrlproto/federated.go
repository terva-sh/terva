package ctrlproto

import "strings"

// The federated id: how a hub names an object that lives on one of its members.
//
// A hub fronts several members and the browser sees one flat space, so a bare
// downstream id collides the moment two members each own a session called
// abc123. The hub mints a composite instead: the member's origin and the
// downstream id, joined by a slash, "neot/abc123".
//
// The composite travels in the ordinary ID field, so a client that knows
// nothing about fleets keeps working. SessionInfo.Origin and TaskInfo.Origin
// carry the origin alongside it, so a client can label a tile with its daemon
// without taking the id apart. Splitting is the hub's job, not the client's.
//
// A lone daemon has no origin and mints nothing. Its ids stay exactly the bytes
// they are today, which is what keeps this seam invisible until a hub is in
// front of it.

// FederatedIDSep separates the origin from the downstream id.
const FederatedIDSep = "/"

// JoinFederatedID composes the id a hub shows for one member's object.
//
// An empty origin returns id unchanged, so a daemon with no fleet emits the
// bare id and nothing downstream can tell this function ran.
//
// The round trip through SplitFederatedID holds for any id, including one that
// contains a slash of its own, as long as ValidOrigin(origin) is true.
func JoinFederatedID(origin, id string) string {
	if origin == "" {
		return id
	}
	return origin + FederatedIDSep + id
}

// SplitFederatedID undoes JoinFederatedID. It cuts at the FIRST separator, so a
// downstream id carrying its own slash survives the round trip whole.
//
// An id with no separator gives an empty origin and the id unchanged, which is
// the lone-daemon case.
//
// Call this only on an id a hub minted. A bare id that happens to contain a
// slash is indistinguishable from a federated one, and this would then report
// an origin no member ever claimed. Both id families are slash-free today (a
// session id is uuid.NewString, and a swarm agent id is used as a single path
// segment under <root>/agents/<id>/), so the ambiguity does not arise in
// practice. It is a property of the id formats, though, not of this function,
// which is why it is written down rather than assumed.
func SplitFederatedID(federated string) (origin, id string) {
	before, after, found := strings.Cut(federated, FederatedIDSep)
	if !found {
		return "", federated
	}
	return before, after
}

// ValidOrigin reports whether origin can name a fleet member.
//
// An origin must be non-empty and must not contain the separator, because
// JoinFederatedID's round trip rests on the first slash belonging to the join.
// An origin with a slash in it would silently re-parse as a different member.
// Enrollment is where a hub applies this, before an origin ever reaches an id.
func ValidOrigin(origin string) bool {
	return origin != "" && !strings.Contains(origin, FederatedIDSep)
}
