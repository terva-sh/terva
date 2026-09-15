package tenant

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"terva.sh/terva/packages/filelock"
)

// StoreName is the registry file inside the SUPERVISOR's own home. Tenants
// never see it: it lives beside the supervisor's config, not under the roots it
// hands out.
const StoreName = "tenants.json"

// Store is the durable subject→tenant registry.
//
// Every mutation is a read-modify-write under a file lock rather than a write
// through cached state. Two supervisors on one home is not a supported
// deployment, but "not supported" is not "cannot happen" — an operator
// double-starting the unit would otherwise have one process silently overwrite
// the other's enrolments, and the failure would surface as a tenant landing in
// an empty home. Same reasoning that made auth.json's refresh take the lock and
// re-read under it.
type Store struct{ path string }

// NewStoreAt opens the registry at an explicit path (tests, and any caller that
// is not the supervisor's own home).
func NewStoreAt(path string) *Store { return &Store{path: path} }

// NewStoreIn opens the registry inside a supervisor home directory.
func NewStoreIn(home string) *Store { return &Store{path: filepath.Join(home, StoreName)} }

type file struct {
	Tenants []Record `json:"tenants"`
}

// ErrNotEnrolled is returned when a subject has no environment. It is a distinct
// error because the supervisor's answer differs by cause: an unenrolled subject
// with a matching role is enrolled on the spot, while one without is refused —
// and the refusal has to name the missing role rather than deny generically.
var ErrNotEnrolled = errors.New("tenant: subject is not enrolled")

// Lookup finds the environment for an IdP subject.
func (s *Store) Lookup(subject string) (Record, error) {
	f, err := s.read()
	if err != nil {
		return Record{}, err
	}
	for _, r := range f.Tenants {
		if r.Subject == subject {
			return r, nil
		}
	}
	return Record{}, ErrNotEnrolled
}

// Enrol returns the environment for subject, creating one if it has none. It is
// idempotent by subject: the same person signing in from a second browser gets
// the same environment, which is the entire point of keying on `sub`.
//
// 🚨 The caller decides WHETHER to enrol. This function does not consult roles,
// because "authenticated" and "entitled to an environment" are different
// questions and D7 turns on the second one. A caller that hands every
// successful login to Enrol has quietly made authentication sufficient.
func (s *Store) Enrol(subject, display string) (Record, bool, error) {
	if subject == "" {
		return Record{}, false, errors.New("tenant: refusing to enrol an empty subject")
	}
	var (
		out     Record
		created bool
	)
	err := s.mutate(func(f *file) error {
		for i := range f.Tenants {
			if f.Tenants[i].Subject == subject {
				if display != "" {
					f.Tenants[i].Display = display
				}
				f.Tenants[i].LastSeenAt = time.Now().UTC()
				out = f.Tenants[i]
				return nil
			}
		}
		id, err := mintUnusedID(f)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		out = Record{ID: id, Subject: subject, Display: display, EnrolledAt: now, LastSeenAt: now}
		created = true
		f.Tenants = append(f.Tenants, out)
		return nil
	})
	return out, created, err
}

// TouchInterval is how stale LastSeenAt may get before a read pays for a write.
//
// The hot path is a READ: Lookup takes no lock and rewrites nothing, because a
// tenant's every /media/ request would otherwise take the registry's file lock
// and rewrite the whole file. That was the first shape of this code and it made
// a bookkeeping field cost a serialised write per HTTP request.
//
// Five minutes is far finer than anything that consumes the field — an idle
// reaper measured in tens of minutes, and a panel showing "last seen" to a
// human — so the coarseness costs nothing real.
const TouchInterval = 5 * time.Minute

// Stale reports whether a record's LastSeenAt is old enough to be worth
// rewriting.
func (r Record) Stale(now time.Time) bool {
	return now.Sub(r.LastSeenAt) > TouchInterval
}

// Touch records that a tenant was seen, for D7's idle reaping. A missing record
// is not an error: the caller has already authenticated the principal, and
// failing a live session over a bookkeeping write would trade a real connection
// for a statistic.
func (s *Store) Touch(id string) error {
	return s.mutate(func(f *file) error {
		for i := range f.Tenants {
			if f.Tenants[i].ID == id {
				f.Tenants[i].LastSeenAt = time.Now().UTC()
			}
		}
		return nil
	})
}

// SetSuspended stops (or resumes) a tenant being spawned, without destroying
// anything. There is deliberately no Delete: see Record.Suspended.
func (s *Store) SetSuspended(id string, suspended bool) error {
	found := false
	err := s.mutate(func(f *file) error {
		for i := range f.Tenants {
			if f.Tenants[i].ID == id {
				f.Tenants[i].Suspended = suspended
				found = true
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("tenant: no environment %q", id)
	}
	return nil
}

// NoteUnentitled records that a subject authenticated successfully and did NOT
// carry a role — the positive evidence D7 requires before anyone acts.
//
// 🚨 It RECORDS and does not ACT. A role that has genuinely been revoked and a
// group mapping somebody mistyped this morning produce the identical signal
// here, and the difference is only visible to a human. So this accumulates the
// evidence an operator needs and changes nothing else: no suspension, and
// certainly no deletion. Absence of a role is not an instruction.
//
// A subject with no enrolment is ignored rather than recorded — there is
// nothing to attach the observation to, and creating a record for someone who
// was refused would be enrolling them by the back door.
func (s *Store) NoteUnentitled(subject string, when time.Time) error {
	return s.mutate(func(f *file) error {
		for i := range f.Tenants {
			if f.Tenants[i].Subject == subject {
				at := when.UTC()
				f.Tenants[i].UnentitledSince = &at
			}
		}
		return nil
	})
}

// ClearUnentitled forgets an unentitled observation, because the subject signed
// in with a role again. Called on every entitled login, so the field means "as
// of the last time we saw them" rather than "at some point in the past".
func (s *Store) ClearUnentitled(id string) error {
	return s.mutate(func(f *file) error {
		for i := range f.Tenants {
			if f.Tenants[i].ID == id {
				f.Tenants[i].UnentitledSince = nil
			}
		}
		return nil
	})
}

// List returns every enrolment, oldest first.
func (s *Store) List() ([]Record, error) {
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	sort.Slice(f.Tenants, func(i, j int) bool { return f.Tenants[i].EnrolledAt.Before(f.Tenants[j].EnrolledAt) })
	return f.Tenants, nil
}

// mintUnusedID rolls an id no existing enrolment already holds.
//
// The id is only 64 bits, which is ample for the job but not so large that
// "collisions cannot happen" is worth asserting instead of checking. This runs
// under the registry lock with the file already re-read, so the check and the
// write cannot be separated by another writer.
func mintUnusedID(f *file) (string, error) {
	taken := make(map[string]bool, len(f.Tenants))
	for _, r := range f.Tenants {
		taken[r.ID] = true
	}
	for range 8 {
		id, err := newID()
		if err != nil {
			return "", err
		}
		if !taken[id] {
			return id, nil
		}
	}
	return "", errors.New("tenant: could not mint an unused id in 8 attempts — the registry is implausibly full, or the random source is broken")
}

func (s *Store) read() (*file, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return &file{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("tenant: read %s: %w", s.path, err)
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("tenant: parse %s: %w", s.path, err)
	}
	for _, r := range f.Tenants {
		// A malformed id reaches a filesystem path, so it is refused at load
		// rather than at the join. Loud and early beats resolving somewhere
		// surprising.
		if !ValidID(r.ID) {
			return nil, fmt.Errorf("tenant: %s holds a malformed id %q", s.path, r.ID)
		}
	}
	return &f, nil
}

// mutate runs fn against the on-disk registry under the lock, re-reading inside
// it so a concurrent writer's changes are never clobbered, and replaces the file
// atomically.
func (s *Store) mutate(fn func(*file) error) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("tenant: %w", err)
	}
	lock, err := filelock.Acquire(s.path + ".lock")
	if err != nil {
		return fmt.Errorf("tenant: lock %s: %w", s.path, err)
	}
	defer lock.Release()

	f, err := s.read()
	if err != nil {
		return err
	}
	if err := fn(f); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("tenant: encode: %w", err)
	}
	tmp := s.path + ".tmp"
	// 0600: the registry maps real people to their environments.
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("tenant: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("tenant: replace %s: %w", s.path, err)
	}
	return nil
}
