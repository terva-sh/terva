package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/privfs"
)

// Workspace Trust gates project content that auto-acts — project-local
// extensions (arbitrary subprocess spawn) and project skills / context
// files (instruction injection) — behind an explicit, user-recorded
// "I trust this directory". The default is UNTRUSTED in every mode: a
// cloned repo can't run its own code or steer the agent until the user
// trusts it. The safe core (built-in tools under the sandbox/approval
// model, built-in + user/global skills, user extensions) stays fully
// usable while untrusted.
//
// The store lives OUTSIDE any project ($TERVA_HOME/trusted.json) so a
// repo can never trust itself. See docs/plans/workspace-trust.md for
// the full model, phases, and threat model.
//
// This file implements Phase 0 of that plan: the store, the
// canonical-real-path identity (with parent-prefix "trust parent trusts
// children" matching, cross-platform), and the resolveTrust resolver.
// The gating at the loader seams lives in build.go / permissions.go /
// the extension manager / the skills discoverer.

// TrustState is the resolved trust posture for one launch. The default
// is TrustRestricted in EVERY mode (unlike approval/jail, which split
// interactive vs headless) — only the user-facing NOTICE differs by
// mode. See resolveTrust.
type TrustState int

const (
	// TrustRestricted is the safe default: project-local code and
	// instructions are NOT loaded. The built-in/user/global core is.
	TrustRestricted TrustState = iota
	// TrustGranted means the cwd is trusted (via --trust this run, or a
	// persisted store entry, exact or a trusted-parent prefix): project
	// extensions/skills/context load.
	TrustGranted
)

// IsTrusted reports whether the state grants trust. Threaded explicitly
// to gated loaders so the verdict is never a global.
func (s TrustState) IsTrusted() bool { return s == TrustGranted }

// TrustStoreVersion is the on-disk schema version of trusted.json. Bump
// only on a breaking layout change; readers tolerate a missing/zero
// version (treated as v1) so an old file still loads.
const TrustStoreVersion = 1

// trustFileName is the basename of the trust store under $TERVA_HOME.
// Deliberately separate from config.json: a trust list is a distinct,
// security-sensitive, append-mostly store, and keeping it out of the
// hand-editable user config avoids "I edited config.json and lost my
// trust list" footguns (plan §4.1, OQ-5).
const trustFileName = "trusted.json"

// TrustEntry is one trusted directory. Path is the as-entered display
// form; Real is the canonical real path used for matching. Parent marks
// a "trust the parent folder" entry: any descendant of Real is then
// trusted too.
type TrustEntry struct {
	Path      string `json:"path"`
	Real      string `json:"real"`
	TrustedAt string `json:"trusted_at,omitempty"`
	Parent    bool   `json:"parent,omitempty"`
}

// TrustStore is the persisted trust list. Mirrors the LoadConfig /
// SaveConfig shapes (config.go) and the append-a-decision idiom of
// addUserPermissionRule (workspace/workspace_permissions.go).
type TrustStore struct {
	Version int          `json:"version"`
	Trusted []TrustEntry `json:"trusted,omitempty"`
}

// TrustStorePath returns the path to trusted.json. It lives under the global
// home (CredentialHome) — even in project-scoped mode — so trust verdicts are
// inherited and a project still can't trust itself by shipping a trusted.json.
func TrustStorePath() string { return filepath.Join(CredentialHome(), trustFileName) }

// LoadTrustStore reads trusted.json. A missing file is an empty store
// (no error) — the untrusted-by-default world starts with no entries.
// A corrupt file is a hard error so a security store is never silently
// treated as empty.
func LoadTrustStore() (TrustStore, error) {
	var s TrustStore
	b, err := os.ReadFile(TrustStorePath())
	if errors.Is(err, os.ErrNotExist) {
		return TrustStore{Version: TrustStoreVersion}, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("parse trust store: %w", err)
	}
	if s.Version == 0 {
		s.Version = TrustStoreVersion
	}
	return s, nil
}

// SaveTrustStore writes trusted.json at mode 0600 (it encodes a
// security decision; not world-readable), creating $TERVA_HOME.
func SaveTrustStore(s TrustStore) error {
	if err := privfs.MkdirAll(TervaHome()); err != nil {
		return err
	}
	if s.Version == 0 {
		s.Version = TrustStoreVersion
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// Atomically: LoadTrustStore treats a corrupt file as a hard error rather
	// than an empty store, so the O_TRUNC window a plain write opens is enough
	// for a concurrent terva process to fail to start on a partial read.
	return privfs.WriteFile(TrustStorePath(), b)
}

// CanonicalTrustPath canonicalizes a directory for identity: absolute
// then symlink-evaluated (best-effort — a path that can't be resolved,
// e.g. doesn't exist yet, falls back to the cleaned abs path). The
// result is the key trust matching compares on.
//
// Cross-platform (Windows CI): on case-insensitive filesystems two
// spellings of the same dir must match, so the canonical form is
// lower-cased on Windows. Separators are normalized by filepath.Clean.
func CanonicalTrustPath(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	abs = filepath.Clean(abs)
	if runtime.GOOS == "windows" {
		// NTFS is case-insensitive (and 8.3 short names fold to the same
		// long path through EvalSymlinks above), so compare case-folded.
		abs = strings.ToLower(abs)
	}
	return abs
}

// canonicalEntryReal returns the canonical match key for a stored entry.
// real is the recorded canonical path; path is the as-entered display form,
// used as a fallback when a hand-edited file recorded only that. On Windows the
// recorded value is re-folded, since it may have been written by hand.
//
// Shared by trusted.json and unjailed.json (unjail.go): both are path-keyed
// security stores, and two copies of this comparison would eventually disagree
// about what counts as the same directory.
func canonicalEntryReal(real, path string) string {
	if real == "" {
		return CanonicalTrustPath(path)
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(filepath.Clean(real))
	}
	return real
}

// trustPathContains reports whether child is parent or a descendant of
// parent, using the same cleaned/case-folded canonical form as
// CanonicalTrustPath. Both arguments must already be canonical. The
// check is lexical on the canonical strings: a trailing separator is
// appended to parent so "/a/b" does not match "/a/bcd".
func trustPathContains(parent, child string) bool {
	if parent == "" || child == "" {
		return false
	}
	if parent == child {
		return true
	}
	sep := string(filepath.Separator)
	prefix := parent
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	return strings.HasPrefix(child, prefix)
}

// IsTrusted reports whether path is trusted by the given store: an exact
// canonical match, or a descendant of a Parent:true entry ("trust parent
// trusts children"). A non-parent entry trusts only its own directory,
// not descendants. Returns the matching entry for display.
func (s TrustStore) IsTrusted(path string) (bool, TrustEntry) {
	real := CanonicalTrustPath(path)
	if real == "" {
		return false, TrustEntry{}
	}
	for _, e := range s.Trusted {
		// Tolerates an entry that recorded only the display path (a
		// hand-edited file): canonicalized on read.
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
	return false, TrustEntry{}
}

// Add records path as trusted (parent marks a "trust descendants too"
// entry). Idempotent: an existing entry with the same canonical Real is
// updated in place (e.g. promoted to Parent) rather than duplicated.
// Returns whether the store changed.
func (s *TrustStore) Add(path string, parent bool) bool {
	real := CanonicalTrustPath(path)
	if real == "" {
		return false
	}
	for i := range s.Trusted {
		if canonicalEntryReal(s.Trusted[i].Real, s.Trusted[i].Path) == real {
			if s.Trusted[i].Parent == parent {
				return false // already present with the same scope
			}
			s.Trusted[i].Parent = parent
			s.Trusted[i].TrustedAt = time.Now().UTC().Format(time.RFC3339)
			return true
		}
	}
	s.Trusted = append(s.Trusted, TrustEntry{
		Path:      filepath.Clean(path),
		Real:      real,
		TrustedAt: time.Now().UTC().Format(time.RFC3339),
		Parent:    parent,
	})
	return true
}

// Remove drops the exact entry whose canonical Real matches path.
// Idempotent: removing an absent path is a no-op. Returns whether the
// store changed.
func (s *TrustStore) Remove(path string) bool {
	real := CanonicalTrustPath(path)
	if real == "" {
		return false
	}
	out := s.Trusted[:0]
	changed := false
	for _, e := range s.Trusted {
		if canonicalEntryReal(e.Real, e.Path) == real {
			changed = true
			continue
		}
		out = append(out, e)
	}
	s.Trusted = out
	return changed
}

// TrustPath canonicalizes path, appends it (idempotent), and persists
// the store. The scriptable surface behind `terva trust`.
func TrustPath(path string, parent bool) error {
	s, err := LoadTrustStore()
	if err != nil {
		return err
	}
	if !s.Add(path, parent) {
		return nil // already trusted with the same scope; nothing to write
	}
	return SaveTrustStore(s)
}

// UntrustPath removes path from the store and persists. The surface
// behind `terva untrust`.
func UntrustPath(path string) error {
	s, err := LoadTrustStore()
	if err != nil {
		return err
	}
	if !s.Remove(path) {
		return nil
	}
	return SaveTrustStore(s)
}

// HasGatedProjectContent reports whether cwd ships project-local content
// that Workspace Trust withholds while restricted: a project (every
// ProjectDirNames spelling) extensions/ or skills/ dir, or a
// .claude/.agents skills dir. Used to
// decide whether to bother the user with an untrusted reminder / warning
// — a plain repo with no such content gates nothing, so there's nothing
// to mention (plan §4.5: "no prompt when nothing is gated"). A
// best-effort presence check; existence is enough, contents are not
// inspected.
func HasGatedProjectContent(cwd string) bool {
	if cwd == "" {
		return false
	}
	var dirs []string
	for _, dirName := range envcompat.ProjectDirNames() {
		dirs = append(dirs,
			filepath.Join(cwd, dirName, "extensions"),
			filepath.Join(cwd, dirName, "skills"),
		)
	}
	dirs = append(dirs,
		filepath.Join(cwd, ".claude", "skills"),
		filepath.Join(cwd, ".agents", "skills"),
	)
	for _, d := range dirs {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

// RestrictedSystemNote is the short system-prompt note appended when the
// workspace is untrusted AND ships gated content, so the model can
// explain the absence of project tools/skills instead of hallucinating
// them. Empty when there's nothing to mention.
func RestrictedSystemNote(cwd string, trusted bool) string {
	if trusted || !HasGatedProjectContent(cwd) {
		return ""
	}
	return "This workspace is untrusted, so its project-local extensions, skills, " +
		"and context files were NOT loaded (a safety default for cloned repos). " +
		"If the user expects project-specific tools or skills, suggest they run " +
		"`/trust` (interactive) or `terva trust` to trust this directory."
}
