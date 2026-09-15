package tenant

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Record is the durable enrolment: one authenticated identity, one environment.
// It is what makes enrolment idempotent — the same person signing in from a new
// browser lands back in the same home rather than in a fresh one.
type Record struct {
	// ID is an OPAQUE local identifier, and it is what names the home
	// directory, the child's socket, and (later) its systemd unit.
	//
	// 🚨 It is deliberately NOT derived from Subject. Verifying the systemd
	// foundation showed a tenant's neighbours are enumerable — every tenant can
	// read the names of every other tenant's state directory (see
	// docs/reviews/2026-08-12-systemd-socket-activation-on-linux.md). A name
	// that carried the IdP subject would publish the roster; a name that
	// carried a HASH of it would let anyone holding a candidate subject confirm
	// enrolment. A random id leaks the count and nothing else.
	ID string `json:"id"`

	// Subject is the IdP's `sub` claim: the immutable key this record is found
	// by. Never the email — an email is reassignable, and D7's whole argument
	// is that enrolment must survive a rename.
	Subject string `json:"subject"`

	// Display is a human label for the supervisor panel. It is refreshed on
	// every sign-in because it is cosmetic; nothing is ever looked up by it.
	Display string `json:"display,omitempty"`

	EnrolledAt time.Time `json:"enrolled_at"`
	LastSeenAt time.Time `json:"last_seen_at,omitempty"`

	// Suspended stops the supervisor spawning this tenant's child without
	// destroying anything. D7 deliberately ships suspension before deletion:
	// an environment that cannot be destroyed cannot be destroyed by a bug.
	Suspended bool `json:"suspended,omitempty"`

	// UnentitledSince is when this subject last authenticated successfully and
	// carried no role, or nil if the last thing we saw them do was sign in
	// entitled.
	//
	// 🚨 It is EVIDENCE, not a trigger. Nothing in terva reads this to act. An
	// IdP outage, a renamed group and a genuinely revoked role are
	// indistinguishable from here, and the action they would otherwise trigger
	// is irreversible — so what accumulates is a fact with a date on it, for a
	// human to weigh in the supervisor panel.
	//
	// 🪤 A POINTER, because `omitempty` does nothing to a time.Time — a zero
	// value serialises as "0001-01-01T00:00:00Z", so every record in a file an
	// operator reads would carry what looks like a real date for something that
	// never happened. nil is the only spelling of "never" that JSON keeps quiet
	// about.
	UnentitledSince *time.Time `json:"unentitled_since,omitempty"`
}

// Home is the tenant's TERVA_HOME — the value that makes all 171 call sites of
// config.TervaHome() correct in the child by construction.
func (r Record) Home(root string) string { return filepath.Join(root, r.ID) }

// idBytes is 64 bits of randomness, and the length is a MEASURED constraint
// rather than a taste.
//
// The id reaches a Linux user name — `User=terva-%i` in the tenant unit
// template — and a user name is capped at 31 characters (32 with the NUL;
// bisected on systemd 255: 31 accepted, 32 refused). At 128 bits the id alone
// was 34, so every systemd-contained tenant failed to start with "Failed to
// spawn 'start' task: Invalid argument", which names nothing an operator could
// act on. 64 bits gives "t-" + 16 hex = 18, leaving room for a prefix.
//
// 64 bits is ample because the id is an IDENTIFIER, not a capability: it names
// a directory and a unit, and what protects those is the uid and the socket's
// permissions, not the difficulty of guessing a name. Collisions are the only
// risk, and Enrol re-rolls on one under the registry lock, so even that is
// closed rather than merely improbable.
const idBytes = 8

// newID mints an opaque tenant id, prefixed so it is recognisable in a
// directory listing, a unit name and a log line.
func newID() (string, error) {
	var b [idBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("tenant: mint id: %w", err)
	}
	return "t-" + hex.EncodeToString(b[:]), nil
}

// MaxUserNameLen is the Linux login-name ceiling the tenant id has to fit
// inside, once a unit template's prefix is added. Exported so the containment
// that cares can refuse early and say why.
const MaxUserNameLen = 31

// ValidID reports whether s is a well-formed tenant id.
//
// It exists because the id reaches a filesystem path, a socket path and a unit
// name. Anything read back from disk — a hand-edited registry, a file restored
// from a backup — is checked against this before it is joined onto a path, so a
// malformed record fails loudly at load instead of resolving somewhere
// surprising.
func ValidID(s string) bool {
	rest, ok := strings.CutPrefix(s, "t-")
	if !ok || len(rest) != idBytes*2 {
		return false
	}
	_, err := hex.DecodeString(rest)
	return err == nil
}
