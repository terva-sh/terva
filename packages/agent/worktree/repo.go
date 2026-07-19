package worktree

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxNameLen bounds a slugged worktree name so it stays a sane branch/dir name.
const maxNameLen = 64

// Env is the per-call host environment: where managed state lives, the cwd to
// resolve the canonical repo from, and the session that owns new claims.
type Env struct {
	// Root is the first-class state home ($TERVA_HOME/worktrees). New
	// worktrees and every registry write land under <Root>/<repo-key>/.
	Root string
	// LegacyRoot, non-empty, is the retired terva-git-worktree extension's
	// data dir ($TERVA_HOME/ext-data/git-worktree). A repo whose registry
	// exists only there is migrated on first touch: entries keep their legacy
	// absolute paths (git records worktree paths in .git/worktrees — moving
	// the checkouts would orphan them), and the merged registry persists
	// under Root from then on.
	LegacyRoot string
	// CWD resolves the repo (and reports which worktree the caller is in).
	CWD string
	// SessionID is the claim-owner identity; "" means no active session.
	SessionID string
	// RepoRoot is an optional explicit target repo (the tool's repo_root
	// arg), resolved instead of CWD. Absolute, or relative to CWD. CWD stays
	// the authority for "which worktree am I in" (cwd_worktree).
	RepoRoot string
}

// repo identifies the canonical git repository shared across the main checkout
// and all of its worktrees. The key is derived from the git *common* dir, so it
// is stable whether cwd is the main repo or any worktree of it.
type repo struct {
	root       string
	legacyRoot string
	cwd        string // the caller's cwd, used for repo-level `git -C` calls
	key        string // stable per-repo storage key (<Root>/<key>/...)
}

// resolveRepo derives the canonical repo identity from env.CWD — or from
// env.RepoRoot when the caller supplies that override. It keys on the git
// *common* dir (shared by the main checkout and every linked worktree) rather
// than cwd or a project id (both cwd-keyed, which would scatter a worktree's
// view from the main checkout's) — exactly what list/reuse needs.
func resolveRepo(env Env) (*repo, error) {
	if env.CWD == "" {
		return nil, fmt.Errorf("no working directory")
	}
	if env.Root == "" {
		return nil, fmt.Errorf("no worktree state root configured")
	}
	dir := env.CWD
	if env.RepoRoot != "" {
		dir = env.RepoRoot
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(env.CWD, dir)
		}
		dir = canonPath(dir)
	}
	common, err := runGit(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		if env.RepoRoot != "" {
			return nil, fmt.Errorf("not a git repository (repo_root %s): %w", env.RepoRoot, err)
		}
		return nil, noRepoError(env.CWD, err)
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	common = canonPath(common)
	return &repo{root: env.Root, legacyRoot: env.LegacyRoot, cwd: dir, key: repoKey(common)}, nil
}

const (
	// maxRepoProbe bounds how many .git-bearing children we confirm with a git
	// invocation, so a directory full of .git-looking entries can't fan out into
	// many git calls. The pre-filter (a .git entry must exist) keeps this small.
	maxRepoProbe = 8
	// maxRepoHints bounds how many discovered repos we name in an error, so a
	// scratch dir holding many checkouts doesn't dump an unbounded list.
	maxRepoHints = 3
)

// nearbyRepos does a shallow, bounded scan of cwd's immediate children for git
// checkouts, returning their cwd-relative paths (e.g. "./terva") — the input to
// a "you're one directory away" hint. Best-effort: an unreadable cwd or a child
// that doesn't confirm yields no entry, never a failure. It never recurses and
// never follows symlinks. Bare repos (no .git entry) are naturally excluded —
// they're a poor /cd target anyway.
func nearbyRepos(cwd string) []string {
	entries, err := os.ReadDir(cwd) // sorted by name => deterministic output
	if err != nil {
		return nil
	}
	var found []string
	probed := 0
	for _, e := range entries {
		// Immediate child directories only; skip files and symlinks (a symlink
		// reports !IsDir here), so a symlink loop or a symlink to a huge tree
		// cannot blow up the scan.
		if !e.IsDir() {
			continue
		}
		child := filepath.Join(cwd, e.Name())
		// A .git entry — a dir for a normal checkout, a file for a linked
		// worktree — is the cheap pre-filter before we spend a git invocation.
		// Lstat so a symlinked .git doesn't get followed.
		if _, err := os.Lstat(filepath.Join(child, ".git")); err != nil {
			continue
		}
		if probed >= maxRepoProbe {
			break
		}
		probed++
		// Confirm it's a real checkout with the exact probe resolveRepo trusts,
		// not a stray .git that isn't a repo.
		if _, err := runGit(child, "rev-parse", "--git-common-dir"); err != nil {
			continue
		}
		found = append(found, "./"+e.Name())
	}
	return found
}

// InRepo reports whether dir is inside a git repo — the strict check: exactly
// when List/Collect against dir will succeed. Distinct from GitAvailable's
// nearby-repo leniency, so surface gates (the worktrees pane) only offer what a
// fetch can actually serve, while tool registration stays lenient enough for
// repo_root guidance.
func InRepo(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := runGit(dir, "rev-parse", "--git-common-dir")
	return err == nil
}

// GitAvailable reports whether the worktree tools can do anything useful from
// dir: either dir is inside a git repo, or a git checkout sits in an immediate
// child (reachable via repo_root). Best-effort and cheap — one git probe plus,
// only when that misses, the same bounded child scan nearbyRepos performs. An
// empty dir or any error reports false. Used once per session build to decide
// whether the worktree tools register at all.
func GitAvailable(dir string) bool {
	if InRepo(dir) {
		return true
	}
	return len(nearbyRepos(dir)) > 0
}

// noRepoError builds the error resolveRepo returns when cwd is not a git repo.
// When the shallow scan finds checkouts one directory down it folds concrete
// next steps into the message (the discoverability win); with nothing nearby it
// returns the bare error unchanged (cause wrapped), so the no-repo-nearby case
// doesn't regress into noisier output.
func noRepoError(cwd string, cause error) error {
	near := nearbyRepos(cwd)
	if len(near) == 0 {
		return fmt.Errorf("not a git repository (cwd %s): %w", cwd, cause)
	}
	if len(near) == 1 {
		r := near[0]
		return fmt.Errorf("not a git repository (cwd %s); found a git repo at %s — cd there (/cd %s) or pass repo_root:%q to operate on it from here",
			cwd, r, r, r)
	}
	shown := near
	extra := 0
	if len(shown) > maxRepoHints {
		extra = len(shown) - maxRepoHints
		shown = shown[:maxRepoHints]
	}
	list := strings.Join(shown, ", ")
	if extra > 0 {
		list = fmt.Sprintf("%s (and %d more)", list, extra)
	}
	return fmt.Errorf("not a git repository (cwd %s); found git repos nearby: %s — cd into one (e.g. /cd %s) or pass repo_root (e.g. repo_root:%q)",
		cwd, list, near[0], near[0])
}

// dataPath resolves <Root>/<key>/<rel>.
func (r *repo) dataPath(rel string) string {
	if rel == "" {
		return filepath.Join(r.root, r.key)
	}
	return filepath.Join(r.root, r.key, filepath.FromSlash(rel))
}

func (r *repo) worktreesDir() string            { return r.dataPath("worktrees") }
func (r *repo) worktreePath(name string) string { return r.dataPath("worktrees/" + name) }
func (r *repo) registryPath() string            { return r.dataPath("registry.json") }
func (r *repo) lockPath() string                { return r.dataPath("registry.lock") }

// Legacy (extension-era) locations, consulted read-only for migration.
func (r *repo) legacyRegistryPath() string {
	if r.legacyRoot == "" {
		return ""
	}
	return filepath.Join(r.legacyRoot, r.key, "registry.json")
}

func (r *repo) legacyWorktreesDir() string {
	if r.legacyRoot == "" {
		return ""
	}
	return filepath.Join(r.legacyRoot, r.key, "worktrees")
}

func (r *repo) legacyWorktreePath(name string) string {
	if r.legacyRoot == "" {
		return ""
	}
	return filepath.Join(r.legacyWorktreesDir(), name)
}

// entryPath is the on-disk checkout for a registry entry: the pinned path when
// the entry carries one (legacy-migrated worktrees keep living where git
// registered them), else the derived first-class location.
func (r *repo) entryPath(name string, e *Entry) string {
	if e != nil && e.Path != "" {
		return e.Path
	}
	return r.worktreePath(name)
}

// repoKey mirrors core.ProjectKey: a readable prefix (the repo's directory name)
// plus a collision-proof suffix (a short hash of the absolute common dir). It
// deliberately matches the retired extension's derivation byte-for-byte — the
// migration looks up the legacy registry by this same key.
func repoKey(commonDir string) string {
	prefix := slugify(filepath.Base(filepath.Dir(commonDir)))
	if prefix == "" {
		prefix = "repo"
	}
	sum := sha256.Sum256([]byte(commonDir))
	return prefix + "-" + hex.EncodeToString(sum[:5])
}

// slugify lowercases and reduces a string to [a-z0-9-], collapsing separators to
// a single dash and trimming dashes from the ends. It is used for both repo-key
// prefixes and worktree names (which become branch wt/<name>).
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == ' ' || r == '/' || r == '.' || r == ':':
			if b.Len() > 0 && !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > maxNameLen {
		out = strings.Trim(out[:maxNameLen], "-")
	}
	return out
}

// dirExists reports whether p exists on disk (file or directory).
func dirExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// canonPath cleans a path and resolves symlinks when it exists, so paths from
// git (which may be symlink-resolved, e.g. macOS /var → /private/var) compare
// equal to paths we construct ourselves. Falls back to a plain Clean when the
// path doesn't exist yet.
func canonPath(p string) string {
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}
