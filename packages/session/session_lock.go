package session

import (
	"os"
	"path/filepath"
	"time"

	"terva.sh/terva/packages/sessionlock"
)

// ErrSessionLocked is what a refused open unwraps to, aliased here so a caller
// that already imports core does not also have to import sessionlock to match
// on it with errors.Is.
var ErrSessionLocked = sessionlock.ErrLocked

// sessionLocks is the Manager every session in this process shares. It is a
// var so a test can swap in one with a driven clock.
var sessionLocks = sessionlock.New()

// SetSessionLockVersion records the running terva version in the claims this
// process writes. A session held by a build you no longer have is worth being
// able to identify.
func SetSessionLockVersion(v string) { sessionLocks.SetVersion(v) }

// swapSessionLockManager replaces the process-wide Manager and returns a
// restore func. Test-only.
func swapSessionLockManager(m *sessionlock.Manager) func() {
	prev := sessionLocks
	sessionLocks = m
	return func() { sessionLocks = prev }
}

// lockHolder names this process in a claim: the binary, and nothing more.
// Claim.PID carries the number and Claim.Describe renders it, so adding one
// here would print the pid twice in every refusal.
func lockHolder() string {
	exe, err := os.Executable()
	if err != nil {
		return "terva"
	}
	return filepath.Base(exe)
}

// acquireSessionLock takes the write lock for a session this process is about
// to open or create.
//
// The reason is deliberately about the handle rather than about the turn: a
// session stays locked for as long as terva holds it open, and a message that
// named one prompt would be wrong for most of that time.
func acquireSessionLock(path string) (*sessionlock.Handle, error) {
	return sessionLocks.Acquire(path, sessionlock.Request{
		Kind:   sessionlock.KindAuto,
		Reason: "a terva has this session open for writing",
		Holder: lockHolder(),
	})
}

// SessionLockClaim returns the claim on a session, for a caller that wants to
// show who holds it. It takes nothing, so it is safe on a held session.
func SessionLockClaim(path string) (sessionlock.Claim, bool) {
	return sessionLocks.ReadClaim(path)
}

// DescribeSessionLock classifies the claim on a session, reporting a stale one
// rather than reclaiming it.
func DescribeSessionLock(path string) (sessionlock.Status, bool) {
	return sessionLocks.Describe(path)
}

// SessionIsLocked reports whether a live process holds this session open.
//
// It gates the destructive paths — pruning and archiving — so a probe that
// cannot answer reports true and the caller leaves the file alone.
func SessionIsLocked(path string) bool { return sessionLocks.IsHeld(path) }

// UnlockSession drops a claim on a session no live process holds. It refuses
// while one does, because removing the record from under a running writer
// tells the next opener the session is free mid-append.
func UnlockSession(path string) error { return sessionLocks.Unlock(path) }

// RemoveSessionLockArtifacts drops the lock's files, for a caller that is
// removing the transcript itself. The claim is not a sidecar, so it is not in
// SessionSidecarPaths and has to be taken explicitly.
func RemoveSessionLockArtifacts(path string) { sessionlock.RemoveArtifacts(path) }

// ClaimSession records a deliberate hold that outlives this process.
func ClaimSession(path, reason, holder string, ttlSeconds int) error {
	req := sessionlock.Request{
		Kind:   sessionlock.KindExplicit,
		Reason: reason,
		Holder: holder,
	}
	if ttlSeconds > 0 {
		req.TTL = time.Duration(ttlSeconds) * time.Second
	}
	h, err := sessionLocks.Acquire(path, req)
	if err != nil {
		return err
	}
	// Release drops the flock and deliberately leaves an explicit record in
	// place. That is what makes the claim outlive this process.
	h.Release()
	return nil
}

// SessionLockArtifactPaths is every file the lock may leave beside a
// transcript, for a caller that removes the session and has to take them along.
func SessionLockArtifactPaths(path string) []string {
	return sessionlock.ArtifactPaths(path)
}
