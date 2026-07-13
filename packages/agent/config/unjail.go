package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The persisted unjail list: directories the user has declared may be worked
// in without the filesystem sandbox.
//
// This is the jail's counterpart to trusted.json, and it deliberately borrows
// that file's identity rules wholesale — CanonicalTrustPath and the
// parent-prefix match — because two hand-rolled copies of a security-relevant
// path comparison WILL drift apart. Only the store shape differs.
//
// It is NOT the same decision as trust, and the two are kept separate on
// purpose. Trust says "load this project's code and instructions"; unjail says
// "let tools write outside this directory". A repo you trust is not thereby a
// repo you want writing to your home directory, and the reverse holds too.
//
// Like trusted.json, it lives under the GLOBAL home even in project-scoped
// mode, so a project cannot unjail itself by shipping an unjailed.json.

// UnjailStoreVersion is the on-disk schema version. Readers tolerate a
// missing/zero version (treated as v1).
const UnjailStoreVersion = 1

// unjailFileName is the basename of the store under $TERVA_HOME. Separate from
// config.json for the same reason trusted.json is: it encodes a security
// decision, and a hand-edit of the user config must not be able to quietly
// widen it.
const unjailFileName = "unjailed.json"

// UnjailEntry is one directory that runs unjailed. Path is the as-entered
// display form; Real is the canonical path used for matching. Parent marks an
// "and its descendants too" entry.
type UnjailEntry struct {
	Path       string `json:"path"`
	Real       string `json:"real"`
	UnjailedAt string `json:"unjailed_at,omitempty"`
	Parent     bool   `json:"parent,omitempty"`
}

// UnjailStore is the persisted unjail list.
type UnjailStore struct {
	Version  int           `json:"version"`
	Unjailed []UnjailEntry `json:"unjailed,omitempty"`
}

// UnjailStorePath returns the path to unjailed.json, under the global home.
func UnjailStorePath() string { return filepath.Join(CredentialHome(), unjailFileName) }

// LoadUnjailStore reads unjailed.json. A missing file is an empty store — the
// jailed-by-default world starts with no exceptions. A corrupt file is a hard
// error: a store that widens a sandbox must never be silently treated as empty,
// because "empty" and "unreadable" would then look the same and fail open in
// the direction the user did not choose.
func LoadUnjailStore() (UnjailStore, error) {
	var s UnjailStore
	b, err := os.ReadFile(UnjailStorePath())
	if errors.Is(err, os.ErrNotExist) {
		return UnjailStore{Version: UnjailStoreVersion}, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("parse unjail store: %w", err)
	}
	if s.Version == 0 {
		s.Version = UnjailStoreVersion
	}
	return s, nil
}

// SaveUnjailStore writes unjailed.json at mode 0600, creating $TERVA_HOME.
func SaveUnjailStore(s UnjailStore) error {
	if err := os.MkdirAll(TervaHome(), 0o755); err != nil {
		return err
	}
	if s.Version == 0 {
		s.Version = UnjailStoreVersion
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(UnjailStorePath(), b, 0o600)
}

// IsUnjailed reports whether path runs unjailed per this store: an exact
// canonical match, or a descendant of a Parent:true entry. A non-parent entry
// covers only its own directory. Returns the matching entry for display.
func (s UnjailStore) IsUnjailed(path string) (bool, UnjailEntry) {
	real := CanonicalTrustPath(path)
	if real == "" {
		return false, UnjailEntry{}
	}
	for _, e := range s.Unjailed {
		entryReal := canonicalEntryReal(e.Real, e.Path)
		if e.Parent {
			if trustPathContains(entryReal, real) {
				return true, e
			}
			continue
		}
		if entryReal == real {
			return true, e
		}
	}
	return false, UnjailEntry{}
}

// Add records path as unjailed. Idempotent: an existing entry with the same
// canonical path is updated in place (e.g. promoted to Parent) rather than
// duplicated. Reports whether the store changed.
func (s *UnjailStore) Add(path string, parent bool) bool {
	real := CanonicalTrustPath(path)
	if real == "" {
		return false
	}
	for i := range s.Unjailed {
		if canonicalEntryReal(s.Unjailed[i].Real, s.Unjailed[i].Path) != real {
			continue
		}
		if s.Unjailed[i].Parent == parent {
			return false
		}
		s.Unjailed[i].Parent = parent
		s.Unjailed[i].UnjailedAt = time.Now().UTC().Format(time.RFC3339)
		return true
	}
	s.Unjailed = append(s.Unjailed, UnjailEntry{
		Path:       filepath.Clean(path),
		Real:       real,
		UnjailedAt: time.Now().UTC().Format(time.RFC3339),
		Parent:     parent,
	})
	return true
}

// Remove drops the entry whose canonical path matches. Idempotent. Reports
// whether the store changed.
func (s *UnjailStore) Remove(path string) bool {
	real := CanonicalTrustPath(path)
	if real == "" {
		return false
	}
	out := s.Unjailed[:0]
	changed := false
	for _, e := range s.Unjailed {
		if canonicalEntryReal(e.Real, e.Path) == real {
			changed = true
			continue
		}
		out = append(out, e)
	}
	s.Unjailed = out
	return changed
}

// UnjailPath records path as unjailed and persists. The surface behind
// `terva unjail`.
func UnjailPath(path string, parent bool) error {
	s, err := LoadUnjailStore()
	if err != nil {
		return err
	}
	if !s.Add(path, parent) {
		return nil
	}
	return SaveUnjailStore(s)
}

// RejailPath drops path from the store and persists, returning it to the
// jailed default. The surface behind `terva jail`.
func RejailPath(path string) error {
	s, err := LoadUnjailStore()
	if err != nil {
		return err
	}
	if !s.Remove(path) {
		return nil
	}
	return SaveUnjailStore(s)
}

// IsPathUnjailed answers the question for one path, loading the store. A store
// that cannot be read reports false: the safe direction is to stay jailed, and
// the caller surfaces the error rather than silently widening the sandbox.
func IsPathUnjailed(path string) (bool, error) {
	s, err := LoadUnjailStore()
	if err != nil {
		return false, err
	}
	ok, _ := s.IsUnjailed(path)
	return ok, nil
}
