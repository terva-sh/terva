package worktree

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Registry is the per-repo truth, augmenting git's worktree list with what git
// doesn't track: base commit, claim, creation time. It lives under the managed
// state root, never inside the user's repo.
type Registry struct {
	Worktrees map[string]*Entry `json:"worktrees"`
}

// Entry is the registry record for one managed worktree (keyed by name).
type Entry struct {
	Branch     string `json:"branch"`
	BaseCommit string `json:"base_commit"`
	BaseRef    string `json:"base_ref"`
	CreatedAt  string `json:"created_at"` // RFC3339
	Claim      *Claim `json:"claim"`      // null when available
	// Path pins the checkout's absolute location. Entries migrated from the
	// retired terva-git-worktree extension keep their legacy path here — git
	// records worktree paths in .git/worktrees, so the checkouts must stay
	// where they are. Entries created in-tree pin their location too, which
	// makes any future home move a non-event. Empty (a pre-migration record
	// mid-flight) falls back to the derived <root>/<key>/worktrees/<name>.
	Path string `json:"path,omitempty"`
}

// Claim records who holds a worktree. session_id is the primary liveness
// signal; pid (this terva process) is a coarse backstop.
type Claim struct {
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"`
	ClaimedAt string `json:"claimed_at"` // RFC3339
}

// loadRegistry reads the repo's registry from the first-class home, migrating
// the retired extension's registry on first touch: when no in-tree registry
// exists but a legacy one does, its entries are adopted with their legacy
// checkout paths pinned (see Entry.Path) and the merged registry is saved to
// the new home immediately, so the migration happens exactly once. Callers
// hold the repo lock. An empty or absent registry is an empty map, never an
// error; a parse error is returned so callers don't silently clobber state.
func loadRegistry(r *repo) (*Registry, error) {
	reg, found, err := readRegistryFile(r.registryPath())
	if err != nil {
		return nil, err
	}
	if found {
		return reg, nil
	}
	if lp := r.legacyRegistryPath(); lp != "" {
		legacy, lfound, lerr := readRegistryFile(lp)
		if lerr != nil {
			return nil, fmt.Errorf("legacy worktree registry: %w", lerr)
		}
		if lfound && len(legacy.Worktrees) > 0 {
			for name, e := range legacy.Worktrees {
				if e.Path == "" {
					e.Path = r.legacyWorktreePath(name)
				}
			}
			if err := saveRegistry(r.registryPath(), legacy); err != nil {
				return nil, fmt.Errorf("migrate legacy worktree registry: %w", err)
			}
			return legacy, nil
		}
	}
	return reg, nil
}

// readRegistryFile reads one registry file. found reports whether the file
// existed with content (so a missing new-home registry can fall through to the
// legacy probe).
func readRegistryFile(path string) (*Registry, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Registry{Worktrees: map[string]*Entry{}}, false, nil
		}
		return nil, false, err
	}
	if strings.TrimSpace(string(b)) == "" {
		return &Registry{Worktrees: map[string]*Entry{}}, false, nil
	}
	var r Registry
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, false, fmt.Errorf("parse registry %s: %w", path, err)
	}
	if r.Worktrees == nil {
		r.Worktrees = map[string]*Entry{}
	}
	return &r, true, nil
}

// saveRegistry writes registry.json atomically (temp + rename) so a concurrent
// reader never sees a half-written file.
func saveRegistry(path string, r *Registry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
