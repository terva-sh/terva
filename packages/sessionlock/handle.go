package sessionlock

import (
	"os"
	"sync"
	"time"

	"terva.sh/terva/packages/filelock"
)

// Handle is a held session lock. Release is idempotent, so a caller can defer
// it beside an error return and also call it explicitly on the happy path.
type Handle struct {
	m         *Manager
	guard     *filelock.Lock
	claimPath string

	mu       sync.Mutex
	claim    Claim
	ttl      time.Duration
	lastBeat time.Time
	once     sync.Once
}

// Touch restamps the heartbeat and pushes the expiry out, but only once an
// interval has passed. It is called after every row a session writes.
//
// Lazy rather than a ticker, and the trade is deliberate. A daemon holds many
// sessions at once; a goroutine each would be one atomic write per session
// every ten seconds forever, most of them describing a session nobody is
// touching. The requirement is a lock held "while a terva is actively adding to
// a session", and a restamp driven by the writes themselves is literally that.
//
// The cost: a session held open but idle longer than its TTL has a lapsed
// record while its flock is still held. The flock wins, the refusal says so,
// and that only matters on a filesystem where locking does not work anyway.
func (h *Handle) Touch() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.m.now()
	if now.Sub(h.lastBeat) < h.m.beat {
		return
	}
	h.refreshLocked(now)
}

// Refresh restamps unconditionally. Exported for a caller that wants to drive
// its own cadence over a set of live sessions.
func (h *Handle) Refresh() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refreshLocked(h.m.now())
}

func (h *Handle) refreshLocked(now time.Time) {
	h.claim.Heartbeat = now.UTC().Format(time.RFC3339)
	h.claim.ExpiresAt = now.Add(h.ttl).UTC().Format(time.RFC3339)
	// Best effort. A heartbeat that fails to land must not fail the write that
	// triggered it: the flock is still held, and it is the witness that counts.
	_ = writeClaim(h.claimPath, h.claim)
	h.lastBeat = now
}

// Claim returns a copy of the record this handle wrote.
func (h *Handle) Claim() Claim {
	if h == nil {
		return Claim{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.claim
}

// Release drops the lock.
//
// The order is load-bearing. The flock goes first, because Windows will not let
// anyone delete or overwrite a file with an open handle or a byte-range lock on
// it, so every removal below depends on the handle being gone.
//
// The record is then removed only if it is still OURS. A `terva session lock`
// may have replaced it with a deliberate claim while we were running, and that
// claim is supposed to outlive us. The same test config.PublishListenRecord
// makes before it removes listen.json, and for the same reason.
//
// There is no guard file to remove: the flock lives on the transcript itself,
// so releasing it is closing our handle on a file that stays exactly where it
// was. That is why this package leaves nothing behind on an ordinary session.
func (h *Handle) Release() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		h.mu.Lock()
		mine := h.claim
		h.mu.Unlock()

		h.guard.Release()

		// 🔑 An explicit claim is defined by outliving the process that made
		// it. `terva session lock` acquires, writes, and exits immediately, so
		// removing the record here would delete the claim in the same breath as
		// making it. Only Unlock, or the expiry, ends one.
		if mine.Kind == KindExplicit {
			return
		}

		if cur, ok := readClaim(h.claimPath); ok {
			if cur.Kind != mine.Kind || cur.PID != mine.PID || cur.ClaimedAt != mine.ClaimedAt {
				return // somebody else's record now; leave it alone
			}
		}
		_ = os.Remove(h.claimPath)
	})
}

// RemoveArtifacts drops both files. Only for a caller that is removing the
// transcript itself, which is the one moment the guard file is safe to unlink.
func RemoveArtifacts(transcriptPath string) {
	for _, p := range ArtifactPaths(transcriptPath) {
		_ = os.Remove(p)
	}
}
