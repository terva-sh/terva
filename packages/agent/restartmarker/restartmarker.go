// Package restartmarker records an intentional, imminent restart on disk so the
// replacement process can tell a planned restart from a stop or a crash.
//
// A supervisor restart (systemctl replacing the process to apply a changed unit)
// arrives as a bare SIGTERM — indistinguishable, inside the dying process, from
// an operator stop. The agent that triggered it knows better, so it arms a
// marker just before issuing the restart: session id, the outgoing build, a
// short expiry, and a nonce. A SIGTERM inside the expiry window is planned; the
// fresh process reads the marker to reconcile the interrupted call as expected
// (not a failure) and to resume the exact session that was live. Outside the
// window, or with no marker, a stop stays a stop.
//
// The marker lives under $TERVA_HOME, is written owner-only via privfs, and
// never carries a secret — only a session id and version strings.
package restartmarker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"terva.sh/terva/packages/privfs"
)

// fileName is the marker's basename under the data home. Singular: one daemon
// per home has at most one restart in flight.
const fileName = "restart-marker.json"

// Marker is the on-disk record of a planned restart. Never store a secret here.
type Marker struct {
	// Session is the wire id of the session that was live and should resume.
	Session string `json:"session"`
	// FromVersion is the outgoing image's build string, for the recovered notice.
	FromVersion string `json:"from_version,omitempty"`
	// Reason is a short operator-facing note (why the restart was planned).
	Reason string `json:"reason,omitempty"`
	// Nonce is a per-arm generation guard: the replacement consumes exactly one
	// marker, so a crash mid-recovery cannot replay a stale generation.
	Nonce string `json:"nonce"`
	// ExpiresUnix bounds the planned window. A SIGTERM after it is a stop, not a
	// planned restart, so a stale marker cannot mislabel a later unrelated stop.
	ExpiresUnix int64 `json:"expires_unix"`
}

// Path returns the marker file path under home.
func Path(home string) string { return filepath.Join(home, fileName) }

// NewNonce returns a random hex generation token. Falls back to a fixed token
// only if the system RNG is unavailable (never, in practice) — dedup still holds
// within a single boot because the file is consumed exactly once.
func NewNonce() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// Expired reports whether the marker's window has closed as of now.
func (m Marker) Expired(now time.Time) bool { return now.Unix() >= m.ExpiresUnix }

// valid reports whether the marker names a session and is still in its window.
func (m Marker) valid(now time.Time) bool { return m.Session != "" && !m.Expired(now) }

// Arm writes m atomically and owner-only. A missing nonce is filled. The caller
// supplies the expiry (ExpiresUnix); arming is the out-of-band declaration of
// intent that a SIGTERM alone cannot carry.
func Arm(home string, m Marker) error {
	if m.Nonce == "" {
		m.Nonce = NewNonce()
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return privfs.WriteFile(Path(home), b)
}

// Read returns the marker if it is present, parseable, and still valid at now
// (names a session and unexpired). It does not remove the file. A missing,
// corrupt, or expired marker yields ok=false.
func Read(home string, now time.Time) (Marker, bool) {
	b, err := os.ReadFile(Path(home))
	if err != nil {
		return Marker{}, false
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return Marker{}, false
	}
	if !m.valid(now) {
		return Marker{}, false
	}
	return m, true
}

// Consume reads a valid marker (like Read) and then removes the file, so a
// planned restart is recovered exactly once. The file is removed even when the
// marker turns out invalid, so a stale/corrupt marker cannot linger to mislabel
// a later stop. ok reports whether a valid marker was recovered.
func Consume(home string, now time.Time) (Marker, bool) {
	m, ok := Read(home, now)
	_ = Clear(home)
	return m, ok
}

// Clear removes the marker file. A missing file is not an error.
func Clear(home string) error {
	if err := os.Remove(Path(home)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
