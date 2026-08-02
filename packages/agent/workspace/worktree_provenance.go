package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	Trusted bool   // lease-time store verdict — how the sub-agent actually boots
	Reason  string // why trusted/restricted, for the operator

	// TrustedNow is the store's CURRENT verdict for Path, re-probed whenever
	// the trust store changes (refreshSwarmWorktreeTrust). It exists because
	// the two facts diverge the moment the operator runs the note's own grant
	// hint: Trusted must keep reporting how the agent booted — a grant cannot
	// reach an agent whose trust was resolved at boot — while the hint must
	// stop asking for a grant that has already been made.
	TrustedNow bool
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
	p.TrustedNow = p.Trusted
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

// swarmWorktreeNoteKey names the single sticky note the swarm's worktree
// leases own on a host that can replace a note (the TUI). Everything about
// that note is a CHANGING fact — how many are leased, how many still run —
// so it rewrites itself rather than stacking one line per change.
const swarmWorktreeNoteKey = "swarm.worktrees"

// renderSwarmWorktreeNote folds the leases into the one line the operator
// reads. live is how many of them are still running; batch is every worktree
// leased since the last one was released, in lease order.
//
// It reports the tense honestly, which is the whole reason it exists. The old
// per-lease notice said "the sub-agent runs without this project's extensions"
// in the present tense and nothing ever retracted it, so a finished swarm left
// a claim about running sub-agents pinned above the input until the next
// prompt. Here the same record says "ran" once the last lease is released, and
// keeps the paths, because a released worktree is kept for review and granting
// it trust is still the thing you might want to do.
//
// Shared git facts are printed once and only when every worktree agrees on
// them; a batch that somehow spans two repos or commits drops the parenthetical
// rather than reporting one worktree's facts as all of theirs.
func renderSwarmWorktreeNote(batch []worktreeProvenance, live int) string {
	if len(batch) == 0 {
		return ""
	}
	n := len(batch)
	noun := "swarm worktrees"
	if n == 1 {
		noun = "swarm worktree"
	}
	// ONE tense variable, read by the header, the restricted clause, and the
	// granted acknowledgment. Split across two sites, they drift: the first
	// cut said "ran — released" in the header while still claiming "running
	// without this project's extensions" four words later, which is the exact
	// falsehood this note was built to stop telling.
	verb, without := "leased", "running without"
	granted := "now trusted — new worktrees get them; the ones already running stay restricted"
	if live == 0 {
		verb, without = "released, kept for review", "ran without"
		granted = "now trusted — the next swarm here runs with them"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d %s %s", n, noun, verb)
	if facts := sharedWorktreeFacts(batch); facts != "" {
		b.WriteString(" · " + facts)
	}

	restricted := 0
	for _, p := range batch {
		if !p.Trusted {
			restricted++
		}
	}
	switch {
	case restricted == 0:
		b.WriteString(" — TRUSTED")
		return b.String()
	case restricted == n && n == 1:
		b.WriteString(" — RESTRICTED")
	case restricted == n:
		b.WriteString(" — all RESTRICTED")
	default:
		fmt.Fprintf(&b, " — %d of %d RESTRICTED", restricted, n)
	}
	b.WriteString(": " + without + " this project's extensions, skills, and context files")
	// The action line tracks the CURRENT store, not the lease-time verdict.
	// Once every restricted path has been granted, repeating the command would
	// be noise — but silently dropping the line would leave RESTRICTED looking
	// unresolved, so the acknowledgment closes the loop instead (with the
	// honest caveat that a grant reaches future leases, never a running agent).
	if hint := swarmWorktreeGrantHint(batch); hint != "" {
		b.WriteString("\n" + hint)
	} else if swarmWorktreeAllGranted(batch) {
		b.WriteString("\n" + granted)
	}
	return b.String()
}

// swarmWorktreeAllGranted reports whether every lease-time-restricted worktree
// has since been granted trust — the state where the grant hint has nothing
// left to ask for and the note owes an acknowledgment instead. False when
// nothing was restricted (nothing to acknowledge) or when any restricted path
// is still ungranted (the hint is still the right line).
func swarmWorktreeAllGranted(batch []worktreeProvenance) bool {
	granted := false
	for _, p := range batch {
		if p.Trusted {
			continue
		}
		if !p.TrustedNow {
			return false
		}
		granted = true
	}
	return granted
}

// sharedWorktreeFacts renders repo/head/base only when every worktree in the
// batch reports the same value — a fact that is not shared is not a fact about
// the batch, and printing the first one's would be a guess.
func sharedWorktreeFacts(batch []worktreeProvenance) string {
	shared := func(get func(worktreeProvenance) string) string {
		v := get(batch[0])
		if v == "" {
			return ""
		}
		for _, p := range batch[1:] {
			if get(p) != v {
				return ""
			}
		}
		return v
	}
	repo := shared(func(p worktreeProvenance) string { return p.Repo })
	commit := shared(func(p worktreeProvenance) string { return p.Commit })
	base := shared(func(p worktreeProvenance) string { return p.Base })

	var parts []string
	switch {
	case repo != "" && commit != "":
		parts = append(parts, repo+" @ "+commit)
	case repo != "":
		parts = append(parts, repo)
	case commit != "":
		parts = append(parts, "@ "+commit)
	}
	if base != "" {
		parts = append(parts, "forked from "+base)
	}
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return parts[0] + " (" + parts[1] + ")"
}

// swarmWorktreeGrantHint is the actionable half. One worktree gets its exact
// path; several sharing a parent get ONE --parent grant instead of one command
// each.
//
// ⚠️ --parent is WIDER than the paths it replaces: it trusts every future
// worktree under that directory too, not only the ones on screen. That is what
// makes it worth offering and exactly why the text says so — a convenience that
// quietly widened trust would be the wrong trade in the one record whose job is
// reporting the trust boundary.
//
// The path is printed in full, never abbreviated. It is meant to be pasted
// back, and a "…/" elision produces a command that does not run.
func swarmWorktreeGrantHint(batch []worktreeProvenance) string {
	var paths []string
	for _, p := range batch {
		// The live verdict, not the lease-time one: a path the operator has
		// granted since the lease has nothing left to hint.
		if !p.Trusted && !p.TrustedNow && p.Path != "" {
			paths = append(paths, p.Path)
		}
	}
	if len(paths) == 0 {
		return ""
	}
	if len(paths) == 1 {
		return fmt.Sprintf("grant with `terva trust %s`", shellQuote(paths[0]))
	}
	if parent := sharedParent(paths); parent != "" {
		return fmt.Sprintf("grant all %d — and every future worktree under it — with `terva trust %s --parent`",
			len(paths), shellQuote(parent))
	}
	return fmt.Sprintf("grant each with `terva trust <path>` (%d paths, no shared parent)", len(paths))
}

// sharedParent returns the directory every path sits directly in, or "" when
// they do not share one. Deliberately only the IMMEDIATE parent: walking up to
// find some common ancestor could land on a directory holding far more than
// these worktrees, and this string goes into a trust grant.
func sharedParent(paths []string) string {
	dir := filepath.Dir(paths[0])
	if dir == "" || dir == "." || dir == string(filepath.Separator) {
		return ""
	}
	for _, p := range paths[1:] {
		if filepath.Dir(p) != dir {
			return ""
		}
	}
	return dir
}

// shellQuote wraps a path in single quotes when it holds anything a shell would
// split or expand. The worktree root lives under "Application Support" on macOS,
// so an unquoted hint is a command that silently trusts the wrong directory.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"\\$`*?[]{}()!&|;<>~#") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
