package resultcap

import (
	"context"
	"os"
	"sync"
)

// Spills is the set of spill files that one session kept for its model to read.
// The session removes them when it ends. A spill outlives the call that wrote
// it, because the pointer in the result sends the model back to it later, so
// nothing else knows when the file is done with.
//
// The zero value is ready to use.
type Spills struct {
	mu     sync.Mutex
	paths  []string
	closed bool
}

// Remove deletes every file in the set and closes it. A file tracked after
// Remove is deleted at once: a call can finish after its session closed, and
// no session is left to read what it kept.
func (s *Spills) Remove() { s.remove(true) }

// remove deletes every file in the set. close says whether the set then deletes
// a late file at once.
func (s *Spills) remove(close bool) {
	s.mu.Lock()
	paths := s.paths
	s.paths = nil
	s.closed = s.closed || close
	s.mu.Unlock()
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

func (s *Spills) add(path string) {
	s.mu.Lock()
	if !s.closed {
		s.paths = append(s.paths, path)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	_ = os.Remove(path)
}

// unowned holds the spills of calls whose context names no session: print,
// json and rpc mode, a bot, and the TUI's shell escape. In those hosts the
// process is the session, so RemoveUnowned runs when the process ends.
var unowned Spills

type spillsKey struct{}

// WithSpills returns ctx carrying s, so a tool that runs under ctx records the
// spill files it keeps in s.
func WithSpills(ctx context.Context, s *Spills) context.Context {
	return context.WithValue(ctx, spillsKey{}, s)
}

// Track records path as a spill file kept for the session that ctx carries, or
// for the process when ctx carries none. An empty path records nothing, so a
// caller can pass the path a cut returned without checking it.
func Track(ctx context.Context, path string) {
	if path == "" {
		return
	}
	s, _ := ctx.Value(spillsKey{}).(*Spills)
	if s == nil {
		s = &unowned
	}
	s.add(path)
}

// RemoveUnowned deletes the spill files that calls with no session kept. The
// CLI runs it on exit.
//
// The set stays open, unlike a session's. The process is about to end, so a
// late file costs one leak, while a closed set would delete the spill of any
// later call in the same process, such as the next test in a test binary.
func RemoveUnowned() { unowned.remove(false) }
