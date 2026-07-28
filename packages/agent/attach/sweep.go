package attach

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// staged is one file the sweep is deciding about.
type staged struct {
	path string
	size int64
	mod  time.Time
}

// SweepResult reports what one sweep removed, for the log line and for tests.
type SweepResult struct {
	Expired int   // removed for being older than the TTL
	Evicted int   // removed to bring the total under the cap
	Bytes   int64 // total freed
}

// Sweep enforces the two bounds on the staging area: age, then total size.
//
// Age first, because an expired file is unwanted regardless of how much room
// there is. Size second, oldest-first, and never touching anything younger than
// grace — a cap breach must not consume the uploads a turn is being composed
// around, which is the one deletion the user cannot recover from by waiting.
//
// A missing root is success: nothing has been staged yet. Individual entries
// that cannot be statted or removed are skipped rather than aborting the sweep,
// so one stuck file does not stop the space from being reclaimed.
func (s *Store) Sweep(now time.Time, ttl time.Duration, capBytes int64, grace time.Duration) (SweepResult, error) {
	var res SweepResult
	sessions, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return res, fmt.Errorf("%s sweep: %w", s.label, err)
	}
	var live []staged
	for _, sess := range sessions {
		if !sess.IsDir() {
			continue
		}
		dir := filepath.Join(s.root, sess.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			f := staged{path: filepath.Join(dir, e.Name()), size: info.Size(), mod: info.ModTime()}
			if now.Sub(f.mod) > ttl {
				if os.Remove(f.path) == nil {
					res.Expired++
					res.Bytes += f.size
				}
				continue
			}
			live = append(live, f)
		}
	}
	// Oldest first, so eviction takes the least recently staged.
	sort.Slice(live, func(i, j int) bool { return live[i].mod.Before(live[j].mod) })
	var total int64
	for _, f := range live {
		total += f.size
	}
	for _, f := range live {
		if total <= capBytes {
			break
		}
		if now.Sub(f.mod) < grace {
			continue
		}
		if os.Remove(f.path) == nil {
			res.Evicted++
			res.Bytes += f.size
			total -= f.size
		}
	}
	s.pruneEmptyDirs(sessions)
	return res, nil
}

// pruneEmptyDirs removes session directories left with nothing in them. It uses
// os.Remove, not RemoveAll, so a directory that gained a file between the sweep
// and now simply survives instead of being deleted out from under a live upload.
func (s *Store) pruneEmptyDirs(sessions []os.DirEntry) {
	for _, sess := range sessions {
		if sess.IsDir() {
			_ = os.Remove(filepath.Join(s.root, sess.Name()))
		}
	}
}

// StartSweeper sweeps once immediately, then every SweepInterval until ctx is
// done, applying THIS store's policy — the two areas retain for different
// lengths of time and each sweeper enforces its own.
//
// The immediate pass is the one that matters most: a daemon that crashed
// mid-turn left files with nothing to reap them, and startup is when that debt
// gets paid.
//
// It logs only when it removed something or failed — a quiet sweeper is the
// normal case and should stay quiet.
func (s *Store) StartSweeper(ctx context.Context) {
	go func() {
		t := time.NewTicker(SweepInterval)
		defer t.Stop()
		for {
			res, err := s.Sweep(time.Now(), s.policy.TTL, s.policy.CapBytes, s.policy.Grace)
			switch {
			case err != nil:
				fmt.Fprintf(os.Stderr, "terva: %s sweep failed: %v\n", s.label, err)
			case res.Expired+res.Evicted > 0:
				fmt.Fprintf(os.Stderr, "terva: swept %d expired and %d over-cap %s(s), freed %d bytes\n",
					res.Expired, res.Evicted, s.label, res.Bytes)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}
