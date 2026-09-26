package core

import "sync"

// ReadOnlySet names side-effect-free tools. Registry assembly adds names;
// publication and turn dispatch take independent snapshots. A rebuild must
// start with a fresh set so removed tools cannot retain authority.
//
// The zero value is an empty, usable set, and a nil *ReadOnlySet is an empty
// immutable one. NewReadOnlySet is for seeding names, not for making the value
// legal.
type ReadOnlySet struct {
	mu sync.RWMutex
	m  map[string]bool
}

// NewReadOnlySet builds a set from the initial (built-in) names.
func NewReadOnlySet(names ...string) *ReadOnlySet {
	s := &ReadOnlySet{m: make(map[string]bool, len(names))}
	for _, n := range names {
		s.m[n] = true
	}
	return s
}

// Add marks more tools read-only. Safe on a nil receiver (no-op) and on the
// zero value, whose map is allocated on first use.
//
// The zero value matters because "nil-safe" invites the reader to assume the
// empty forms are interchangeable, and before this they were not: a nil
// *ReadOnlySet absorbed Add silently while a &ReadOnlySet{} panicked on
// assignment to a nil map. That is backwards from every expectation — the more
// constructed-looking value was the one that blew up — and it panics at the
// first Add rather than at construction, so an extension merging its read_only
// declarations mid-session is where it would surface.
func (s *ReadOnlySet) Add(names ...string) {
	if s == nil || len(names) == 0 {
		return
	}
	s.mu.Lock()
	if s.m == nil {
		s.m = make(map[string]bool, len(names))
	}
	for _, n := range names {
		s.m[n] = true
	}
	s.mu.Unlock()
}

// Has reports membership. Safe on a nil receiver and on the zero value; both
// contain nothing (a read from a nil map is legal, unlike a write).
func (s *ReadOnlySet) Has(name string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.m[name]
}

// Snapshot returns an independent set. A nil receiver stays nil.
func (s *ReadOnlySet) Snapshot() *ReadOnlySet {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := NewReadOnlySet()
	for name, readOnly := range s.m {
		out.m[name] = readOnly
	}
	return out
}
