package sessionlock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"terva.sh/terva/packages/filelock"
	"terva.sh/terva/packages/privfs"
)

const (
	// BeatInterval is how often a writing session restamps its heartbeat. The
	// same number config.ListenBeatInterval and runs.HeartbeatInterval use;
	// three subsystems agreeing is worth more here than a tuned constant.
	BeatInterval = 10 * time.Second

	// DefaultAutoTTL is how long an automatic lock stays credible without a
	// restamp. It matches worktree's defaultClaimTTL for the same reason.
	DefaultAutoTTL = 12 * time.Hour

	// DefaultExplicitTTL is the default life of a deliberate claim.
	DefaultExplicitTTL = 12 * time.Hour

	// MaxExplicitTTL caps one. A claim nobody can outlive is a session nobody
	// can recover, and the laptop that made it may never come back.
	MaxExplicitTTL = 7 * 24 * time.Hour
)

// Manager holds the policy and the seams. The clock, the pid probe, this
// process's identity, the timeouts, and the lock primitive are all fields so a
// test can drive staleness and a broken filesystem deterministically. This
// mirrors worktree.Manager, which was built the same way for the same reason.
type Manager struct {
	now      func() time.Time
	pidAlive func(int) bool
	selfPID  int
	host     string
	version  string

	autoTTL     time.Duration
	explicitTTL time.Duration
	beat        time.Duration

	// tryLock is a seam and not an abstraction. It exists so one test can
	// simulate a filesystem that reports every lock as free (NFS without
	// lockd) and prove the record-based fallback still refuses a live holder.
	tryLock func(path string) (*filelock.Lock, bool, error)
}

// lockTranscript takes the flock on the TRANSCRIPT itself.
//
// On the transcript and not on a lockfile beside it, which is where this design
// differs from every other filelock caller in the tree — and the difference is
// earned. worktree and config lock a sibling because they REPLACE their JSON by
// rename, and a rename moves the inode an flock lives on. A transcript is
// append-only: it is never rewritten, never renamed, and its inode is stable
// for its whole life. So it can hold its own lock, and the alternative would
// leave a zero-byte file beside every session that ever ran, forever.
//
// O_RDONLY and no O_CREATE. flock needs no write access, and a probe that
// created its own subject would answer "is anyone writing this session" by
// conjuring the session.
//
// A missing transcript is an ERROR here, not a free lock. There is nothing to
// take a lock on, and answering "free" would hand the caller a Handle holding
// nothing at all. Callers that legitimately ask about a path with no file —
// IsHeld, for the orphan sweep — check for its absence before they get here.
func lockTranscript(path string) (*filelock.Lock, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	return filelock.TryLockFile(f)
}

// New returns a Manager wired to the real clock, process, and filesystem.
func New() *Manager {
	host, _ := os.Hostname()
	return &Manager{
		now:         time.Now,
		pidAlive:    filelock.PIDAlive,
		selfPID:     os.Getpid(),
		host:        host,
		autoTTL:     DefaultAutoTTL,
		explicitTTL: DefaultExplicitTTL,
		beat:        BeatInterval,
		tryLock:     lockTranscript,
	}
}

// SetVersion records the terva version in claims this Manager writes. It is
// diagnostic only: a session held by a build you no longer have is worth being
// able to see.
func (m *Manager) SetVersion(v string) { m.version = v }

// Request is what a caller wants recorded about the lock it is taking.
type Request struct {
	Kind   Kind
	Reason string        // why, in one line, for whoever is refused
	Holder string        // who, e.g. "terva web"
	TTL    time.Duration // 0 takes the default for the kind
}

// Acquire takes the lock on a transcript, or refuses with a *BusyError saying
// who holds it and why.
//
// The order matters and is the whole design. The flock is asked first, because
// it is the only witness that cannot be wrong about a live process. The record
// is read second, and only ever to answer a question the flock structurally
// cannot: whether somebody claimed this session deliberately, and whether a
// filesystem that ignores locks is hiding a live holder.
func (m *Manager) Acquire(transcriptPath string, req Request) (*Handle, error) {
	if req.Kind == "" {
		req.Kind = KindAuto
	}
	claimPath := ClaimPath(transcriptPath)

	guard, held, err := m.tryLock(transcriptPath)
	if err != nil {
		// A probe that failed is not a free lock. Report it, so a permissions
		// problem under $TERVA_HOME does not read as "nobody is here".
		return nil, err
	}
	if !held {
		// 🔑 No expiry check on this branch, deliberately. A held flock IS a
		// live process, by the kernel's guarantee. A lapsed record behind one
		// means a holder that stopped writing, not a holder that crashed, and
		// reclaiming here would hand a session to a second writer while the
		// first still has it open.
		rec, ok := readClaim(claimPath)
		return nil, &BusyError{Path: transcriptPath, Cause: CauseLiveHandle, Claim: rec, HasClaim: ok}
	}

	if busy := m.refuseByRecord(transcriptPath, claimPath, req); busy != nil {
		guard.Release()
		return nil, busy
	}

	now := m.now()
	ttl := req.TTL
	if ttl <= 0 {
		ttl = m.autoTTL
		if req.Kind == KindExplicit {
			ttl = m.explicitTTL
		}
	}
	if req.Kind == KindExplicit && ttl > MaxExplicitTTL {
		ttl = MaxExplicitTTL
	}

	c := Claim{
		Format:      ClaimFormat,
		Session:     sessionStem(transcriptPath),
		Kind:        req.Kind,
		Reason:      req.Reason,
		Holder:      req.Holder,
		Host:        m.host,
		Version:     m.version,
		ClaimedAt:   now.UTC().Format(time.RFC3339),
		ExpiresAt:   now.Add(ttl).UTC().Format(time.RFC3339),
		BeatSeconds: int(m.beat / time.Second),
	}
	if req.Kind == KindAuto {
		c.PID = m.selfPID
		c.Heartbeat = c.ClaimedAt
	}
	if err := writeClaim(claimPath, c); err != nil {
		guard.Release()
		return nil, err
	}
	return &Handle{
		m:         m,
		guard:     guard,
		claimPath: claimPath,
		claim:     c,
		ttl:       ttl,
		lastBeat:  now,
	}, nil
}

// refuseByRecord asks the record the two questions the flock cannot answer.
// It runs only once the guard is ours, so the record cannot change underneath.
func (m *Manager) refuseByRecord(transcriptPath, claimPath string, req Request) *BusyError {
	rec, ok := readClaim(claimPath)
	if !ok {
		// No record, or one we cannot read. The guard was free, which already
		// proved no live process holds this, so proceed. Fail open where the
		// stronger witness says the holder is gone.
		return nil
	}
	now := m.now()

	if rec.Kind == KindExplicit {
		// The case the flock structurally cannot express: the process that made
		// this claim has exited on purpose, so its pid is 0 and its flock is
		// long gone. Only the expiry can end it.
		if !rec.expiredAt(now) {
			return &BusyError{Path: transcriptPath, Cause: CauseExplicitClaim, Claim: rec, HasClaim: true}
		}
		return nil
	}

	// An automatic record behind a FREE guard normally means a crashed holder,
	// and taking the lock is the recovery. The exception is a filesystem that
	// does not honour locks, where the guard is free because locking did
	// nothing. Both witnesses are required before refusing on that suspicion:
	// a pid alone goes stale when the number is reused, and a heartbeat alone
	// cannot tell a paused process from a dead one.
	if rec.PID > 0 && rec.PID != m.selfPID && rec.Host == m.host &&
		m.pidAlive(rec.PID) && rec.beatFreshAt(now, m.beat) && !rec.expiredAt(now) {
		return &BusyError{Path: transcriptPath, Cause: CauseLiveHandleUnflocked, Claim: rec, HasClaim: true}
	}
	return nil
}

// ReadClaim returns the record beside a transcript, if there is a readable one.
// It takes nothing, so it is safe to call on a session another process holds —
// which is the point, since the caller is usually about to explain a refusal.
func (m *Manager) ReadClaim(transcriptPath string) (Claim, bool) {
	return readClaim(ClaimPath(transcriptPath))
}

// IsHeld reports whether a live process currently holds this transcript.
//
// It answers with the flock alone, and it does not consider an explicit claim:
// callers are the ones asking "may I delete this file out from under somebody",
// and a claim with no process behind it does not make a file unsafe to touch.
func (m *Manager) IsHeld(transcriptPath string) bool {
	if _, err := os.Stat(transcriptPath); os.IsNotExist(err) {
		// Nobody can be writing a file that is not there. The orphan sweep asks
		// exactly this, about a claim whose transcript is already gone.
		return false
	}
	guard, held, err := m.tryLock(transcriptPath)
	if err != nil {
		// Unknown is treated as held. This gates destructive sweeps, so the
		// safe answer to "I could not tell" is to leave the file alone.
		return true
	}
	if !held {
		return true
	}
	guard.Release()
	return false
}

// Unlock removes a claim on a session no live process holds.
//
// It refuses while the guard is taken, and that refusal is the point: removing
// the record from under a running writer would tell the next opener the session
// is free while an append is in flight.
func (m *Manager) Unlock(transcriptPath string) error {
	if _, err := os.Stat(transcriptPath); os.IsNotExist(err) {
		// The transcript is gone; only the record is left. Drop it.
		if err := os.Remove(ClaimPath(transcriptPath)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	guard, held, err := m.tryLock(transcriptPath)
	if err != nil {
		return err
	}
	if !held {
		rec, ok := readClaim(ClaimPath(transcriptPath))
		return &BusyError{Path: transcriptPath, Cause: CauseLiveHandle, Claim: rec, HasClaim: ok}
	}
	if err := os.Remove(ClaimPath(transcriptPath)); err != nil && !os.IsNotExist(err) {
		guard.Release()
		return err
	}
	guard.Release()
	return nil
}

// Status is a claim plus this Manager's verdict on whether it still binds, for
// a listing that has to show stale entries rather than hide them.
type Status struct {
	Claim       Claim
	Held        bool   // a live process holds the guard right now
	Stale       bool   // the record describes a holder that is gone
	StaleReason string // which witness said so
}

// Describe classifies the claim on a transcript.
//
// A stale claim is REPORTED and never silently reclaimed here, which is
// worktree's policy and for its reason: a listing that quietly tidied state
// would be a listing that destroys evidence somebody is about to read. Only
// Acquire reclaims, and only as a side effect of actually taking the lock.
func (m *Manager) Describe(transcriptPath string) (Status, bool) {
	rec, ok := readClaim(ClaimPath(transcriptPath))
	if !ok {
		return Status{}, false
	}
	st := Status{Claim: rec, Held: m.IsHeld(transcriptPath)}
	if st.Held {
		return st, true
	}
	now := m.now()
	var reasons []string
	if rec.Kind == KindExplicit {
		if rec.expiredAt(now) {
			reasons = append(reasons, "the claim is past its expiry")
		}
	} else {
		if rec.PID > 0 && rec.Host == m.host && !m.pidAlive(rec.PID) {
			reasons = append(reasons, "the owning process is gone")
		}
		if !rec.beatFreshAt(now, m.beat) {
			reasons = append(reasons, "the heartbeat stopped")
		}
		if rec.expiredAt(now) {
			reasons = append(reasons, "the claim is past its expiry")
		}
	}
	if len(reasons) > 0 {
		st.Stale = true
		st.StaleReason = strings.Join(reasons, ", ")
	}
	return st, true
}

// sessionStem is the transcript's filename without its extension, which is the
// id every session resolver in packages/core already uses.
func sessionStem(transcriptPath string) string {
	return strings.TrimSuffix(filepath.Base(transcriptPath), ".jsonl")
}

func writeClaim(path string, c Claim) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return privfs.WriteFile(path, append(b, '\n'))
}

// The package-level helpers delegate to a default Manager, so an ordinary
// caller writes sessionlock.Acquire and only a test reaches for New.
var std = New()

// Acquire takes the lock on a transcript using the default Manager.
func Acquire(transcriptPath string, req Request) (*Handle, error) {
	return std.Acquire(transcriptPath, req)
}

// ReadClaim returns the record beside a transcript, if any.
func ReadClaim(transcriptPath string) (Claim, bool) { return std.ReadClaim(transcriptPath) }

// IsHeld reports whether a live process holds this transcript.
func IsHeld(transcriptPath string) bool { return std.IsHeld(transcriptPath) }

// Unlock removes a claim on a session no live process holds.
func Unlock(transcriptPath string) error { return std.Unlock(transcriptPath) }

// Describe classifies the claim on a transcript.
func Describe(transcriptPath string) (Status, bool) { return std.Describe(transcriptPath) }

// SetVersion records the terva version in claims the default Manager writes.
func SetVersion(v string) { std.SetVersion(v) }
