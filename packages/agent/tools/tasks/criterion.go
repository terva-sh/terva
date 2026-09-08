package tasks

// CriterionChecker ticks the acceptance criterion that a task was seeded from.
//
// The interface is declared here, on the tasks side, and implemented on the
// ticket side. That is the opposite of the seeding direction, and it has to be:
// this package imports privfs and core and nothing else, so a direct import of
// the ticket tools would close an import cycle. The tasks package states what it
// needs, and the build layer injects something that can do it.
//
// A nil checker is the normal case, not a failure. A session with no ticket
// store, or one where a --tools allowlist dropped the ticket tools, closes tasks
// with nothing to tick.
type CriterionChecker interface {
	// CheckCriterion ticks criterion index on the ticket named by ref.
	//
	// The index is 1-based and counts over the FULL acceptance criteria list,
	// checked items included, because that is the positional index that
	// ticket.SetChecklistItem addresses. The seeding side records it the same
	// way. Numbering either side off the unchecked subset would tick the wrong
	// box and report success.
	CheckCriterion(ref string, index int) error
}

// SetCriterionChecker binds the checker that a closing task ticks its criterion
// through. The build layer calls this wherever it binds the board in the other
// direction, so the two halves of the bridge cannot drift apart.
func (s *Store) SetCriterionChecker(c CriterionChecker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checker = c
}

// CriterionChecker returns the bound checker, or nil when this session has no
// ticket store behind it. The caller reads it and then calls it outside the
// store lock, because the implementation writes a ticket file and there is no
// reason to hold up the whole task board for that.
func (s *Store) CriterionChecker() CriterionChecker {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checker
}
