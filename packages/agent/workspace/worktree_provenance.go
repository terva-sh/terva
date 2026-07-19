package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
)

// worktreeProvenance is the compact record surfaced when a managed git worktree
// is leased for a swarm sub-agent (retro H5·ux, workspace-trust Phase 7): the
// git facts for the leased checkout plus the host-owned trust verdict for that
// EXACT path. It never widens trust — Trusted comes from store.IsTrusted(Path)
// verbatim, so a worktree is trusted only by an explicit entry or the store's
// own --parent rule, never by inheriting the host's verdict. A restricted
// worktree carries the actionable `terva trust <path>` hint.
//
// The git facts are best-effort. An engine-reported provenance object (the
// carrier fills it from the worktree engine's CreateResult — the contract this
// file held open since Phase 7 §7a) is preferred; otherwise the host reads
// them lock-free from the on-disk worktree. Any field that can't be determined
// is omitted, never guessed.
type worktreeProvenance struct {
	Repo    string // origin repo name (best-effort)
	Path    string // the leased worktree path (as reported by worktree_create)
	Branch  string // checked-out branch (empty if detached/unknown)
	Base    string // ref it forked from (empty if unknown)
	Commit  string // short HEAD sha (empty if unknown)
	Trusted bool   // store.IsTrusted(Path) — never widened past the store's rules
	Reason  string // why trusted/restricted, for the operator
}

// extProvenance is the optional `provenance` object a worktree extension may
// attach to its worktree_create/claim result. Absent today (the host fills the
// facts itself); parsing it now makes the host prefer authoritative ext-reported
// facts the moment the extension ships them, with no further host change.
type extProvenance struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
	Commit string `json:"commit"`
}

// worktreeTrustVerdict is the pure, git-free core: the trust state for a worktree
// path and the reason, straight from the store. It is the security-critical part
// (the never-auto-inherit invariant) and is unit-tested independently of any git
// probe. A worktree is trusted only by an exact entry or a --parent ancestor;
// the "via --parent" reason makes an inherited grant legible rather than silent.
func worktreeTrustVerdict(store config.TrustStore, worktreePath string) (trusted bool, reason string) {
	if worktreePath == "" {
		return false, "no path"
	}
	ok, entry := store.IsTrusted(worktreePath)
	if !ok {
		return false, "no trust entry"
	}
	entryReal := entry.Real
	if entryReal == "" {
		entryReal = config.CanonicalTrustPath(entry.Path)
	}
	if entry.Parent && entryReal != config.CanonicalTrustPath(worktreePath) {
		return true, "trusted via --parent entry " + entry.Path
	}
	return true, "explicit trust entry"
}

// newWorktreeProvenance assembles the record: the host-owned trust verdict, then
// the git facts (ext-reported preferred, else read from disk best-effort). ctx
// bounds the git probes; hostCwd is the launch checkout the swarm worktree forked
// from (worktree_create bases on the host HEAD), used only to report the base.
func newWorktreeProvenance(ctx context.Context, store config.TrustStore, hostCwd, worktreePath string, ext *extProvenance) worktreeProvenance {
	p := worktreeProvenance{Path: worktreePath}
	p.Trusted, p.Reason = worktreeTrustVerdict(store, worktreePath)
	if ext != nil {
		p.Repo, p.Branch, p.Base, p.Commit = ext.Repo, ext.Branch, ext.Base, ext.Commit
	}
	fillWorktreeGitFacts(ctx, hostCwd, worktreePath, &p)
	return p
}

// fillWorktreeGitFacts fills any git fact still empty by reading it lock-free
// from the checkout (ext-reported facts win). Every probe is read-only and
// write-free (--no-optional-locks + GIT_OPTIONAL_LOCKS=0), best-effort, and
// bounded — a missing git binary, a bare/absent repo, or a slow tree just leaves
// the field empty. It never fails the lease.
func fillWorktreeGitFacts(ctx context.Context, hostCwd, worktreePath string, p *worktreeProvenance) {
	if worktreePath == "" {
		return
	}
	if p.Branch == "" {
		if out, err := gitProbe(ctx, worktreePath, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
			p.Branch = strings.TrimSpace(out)
		}
	}
	if p.Commit == "" {
		if out, err := gitProbe(ctx, worktreePath, "rev-parse", "--short", "-q", "HEAD"); err == nil {
			p.Commit = strings.TrimSpace(out)
		}
	}
	if p.Repo == "" {
		if out, err := gitProbe(ctx, worktreePath, "config", "--get", "remote.origin.url"); err == nil {
			p.Repo = repoNameFromURL(strings.TrimSpace(out))
		}
	}
	// A swarm worktree forks from the host HEAD (worktree_create base=HEAD), so
	// the host checkout's branch is what it forked from.
	if p.Base == "" && hostCwd != "" {
		if out, err := gitProbe(ctx, hostCwd, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
			p.Base = strings.TrimSpace(out)
		}
	}
}

// gitProbeTimeout bounds each provenance git probe. A cold cache on a huge repo
// can be slow; on timeout the fact is simply omitted (the lease never blocks).
const gitProbeTimeout = 2 * time.Second

// gitProbe runs one read-only, lock-free git query in dir. Mirrors the status
// bar's prober discipline (packages/agent/modes/git_prober.go) without importing
// that TUI-layer package into the daemon core.
func gitProbe(ctx context.Context, dir string, args ...string) (string, error) {
	tctx, cancel := context.WithTimeout(ctx, gitProbeTimeout)
	defer cancel()
	full := append([]string{"--no-optional-locks"}, args...)
	cmd := exec.CommandContext(tctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	return string(out), err
}

// repoNameFromURL extracts a bare repo name from a remote URL: the last path
// segment, minus a trailing ".git". Handles scp-style (git@host:owner/repo.git)
// and URL forms. Returns "" on empty input.
func repoNameFromURL(u string) string {
	u = strings.TrimSuffix(strings.TrimSpace(u), "/")
	u = strings.TrimSuffix(u, ".git")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	return u
}

// Render produces the one-line operator record. Present git facts are shown in
// a parenthetical; the trust verdict and its reason always show; a restricted
// worktree appends the actionable, never-auto-applied grant hint.
func (p worktreeProvenance) Render() string {
	repo := p.Repo
	if repo == "" {
		repo = "worktree"
	}

	var head string
	switch {
	case p.Branch != "" && p.Commit != "":
		head = "branch " + p.Branch + " @ " + p.Commit
	case p.Commit != "":
		head = "@ " + p.Commit
	case p.Branch != "":
		head = "branch " + p.Branch
	}
	var facts []string
	if head != "" {
		facts = append(facts, head)
	}
	if p.Base != "" {
		facts = append(facts, "forked from "+p.Base)
	}
	factStr := ""
	if len(facts) > 0 {
		factStr = " (" + strings.Join(facts, ", ") + ")"
	}

	state := "TRUSTED"
	hint := ""
	if !p.Trusted {
		state = "RESTRICTED — the sub-agent runs without this project's extensions, skills, and context files"
		hint = fmt.Sprintf("; grant with `terva trust %s` (a worktree is never auto-trusted from the host)", p.Path)
	}
	reason := ""
	if p.Reason != "" {
		reason = " [" + p.Reason + "]"
	}

	return fmt.Sprintf("swarm worktree provenance: %s %s%s — %s%s%s", repo, p.Path, factStr, state, reason, hint)
}
