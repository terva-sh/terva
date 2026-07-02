package modes

// The git status prober feeds the status bar's git segment: branch,
// dirty flag, and +added/-removed line counts vs HEAD. It runs in the
// host process on its own goroutine (never through the agent's tool
// sandbox — these are read-only queries about the UI's own cwd), polls
// on a slow ticker plus explicit pokes at turn end and /cd, and
// degrades to "segment absent" on every failure mode: no git binary,
// not a repo, bare repo, command timeout.
//
// GIT_OPTIONAL_LOCKS=0 (belt: --no-optional-locks per command) keeps
// every probe lock-free and write-free, so it can never contend with
// git commands the agent itself runs in a turn.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"terva.sh/terva/packages/tui"
)

// gitRunner abstracts git execution so probeGit is testable with
// canned fixtures. Production is execGit.
type gitRunner func(ctx context.Context, dir string, args ...string) (string, error)

// gitProbeTimeout bounds each git command. A cold cache on a huge
// repo can be slow; on timeout the caller keeps the previous snapshot
// rather than flapping the segment off.
const gitProbeTimeout = 2 * time.Second

// gitProbeInterval is the background poll cadence. Turn-end and /cd
// pokes cover the moments that matter; the ticker just catches
// out-of-band changes (the user committing in another terminal).
const gitProbeInterval = 10 * time.Second

// errGitTimeout marks a probe command that hit gitProbeTimeout. A
// timeout says nothing about the repo (huge tree, cold cache), so the
// prober keeps its previous snapshot instead of flapping to absent.
var errGitTimeout = errors.New("git probe timed out")

func execGit(ctx context.Context, dir string, args ...string) (string, error) {
	tctx, cancel := context.WithTimeout(ctx, gitProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil && tctx.Err() != nil {
		return "", errGitTimeout
	}
	return string(out), err
}

// probeGit gathers one GitInfo snapshot for dir. ok=false with
// transient=false means "genuinely not a usable repository" (render
// the segment absent); transient=true means the probe itself failed
// (timeout) and the caller should keep its previous snapshot. A
// degraded snapshot (e.g. diff stats unavailable on an unborn HEAD)
// still returns ok=true with the fields it could fill.
func probeGit(ctx context.Context, run gitRunner, dir string) (info tui.GitInfo, ok, transient bool) {
	if dir == "" {
		return tui.GitInfo{}, false, false
	}
	// Gate: inside a work tree? Prints "false" for bare repos and
	// errors (exit 128) outside a repository entirely.
	out, err := run(ctx, dir, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(out) != "true" {
		return tui.GitInfo{}, false, errors.Is(err, errGitTimeout)
	}

	info = tui.GitInfo{Present: true}
	out, err = run(ctx, dir, "--no-optional-locks", "status", "--porcelain=v2", "--branch")
	if err != nil {
		return tui.GitInfo{}, false, errors.Is(err, errGitTimeout)
	}
	oid := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.oid "):
			oid = strings.TrimPrefix(line, "# branch.oid ")
		case strings.HasPrefix(line, "# branch.head "):
			info.Branch = strings.TrimPrefix(line, "# branch.head ")
		case line == "" || strings.HasPrefix(line, "#"):
			// other headers / trailing blank
		default:
			// Any non-header entry (staged, unstaged, untracked,
			// unmerged) marks the tree dirty.
			info.Dirty = true
		}
	}
	if info.Branch == "(detached)" || info.Branch == "" {
		// Detached HEAD: show the short OID instead of a branch name.
		if len(oid) >= 7 {
			info.Branch = oid[:7]
		} else {
			info.Branch = oid
		}
	}
	if info.Branch == "" {
		return tui.GitInfo{}, false, false
	}

	// Line stats vs HEAD (staged + unstaged, tracked files). Errors —
	// most commonly an unborn HEAD in a brand-new repo — degrade to
	// 0/0 while keeping the branch info.
	out, err = run(ctx, dir, "--no-optional-locks", "diff", "--numstat", "HEAD")
	if err == nil {
		info.Added, info.Removed = sumNumstat(out)
	}
	return info, true, false
}

// sumNumstat totals the added/removed columns of `git diff --numstat`
// output, skipping binary entries (reported as "-").
func sumNumstat(out string) (added, removed int) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		a, errA := strconv.Atoi(fields[0])
		r, errR := strconv.Atoi(fields[1])
		if errA != nil || errR != nil {
			continue // binary file: "-\t-\tpath"
		}
		added += a
		removed += r
	}
	return added, removed
}

// runGitProber polls the working directory's git state for the status
// bar. Probes immediately, then on a slow ticker plus pokes (turn end,
// /cd). Stores under i.mu and invalidates the frame only when the
// snapshot actually changed, so idle frames stay byte-identical.
func (i *Interactive) runGitProber(ctx context.Context) {
	if _, err := exec.LookPath("git"); err != nil {
		return // no git on PATH: segment stays absent for the session
	}
	tick := time.NewTicker(gitProbeInterval)
	defer tick.Stop()
	for {
		i.probeGitOnce(ctx, execGit)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-i.gitPoke:
		}
	}
}

func (i *Interactive) probeGitOnce(ctx context.Context, run gitRunner) {
	// /cd mutates cfg.CWD under i.mu; re-read it every probe.
	i.mu.Lock()
	dir := i.cfg.CWD
	i.mu.Unlock()

	info, ok, transient := probeGit(ctx, run, dir)
	if !ok && transient {
		return // timeout: keep the previous snapshot, no flapping
	}
	// !ok && !transient leaves info zero-valued: segment absent.

	i.mu.Lock()
	changed := info != i.gitInfo
	i.gitInfo = info
	i.mu.Unlock()
	if changed {
		i.invalidate()
	}
}

// pokeGitProber requests an out-of-band probe (turn end, /cd). Non-
// blocking: a pending poke already covers it.
func (i *Interactive) pokeGitProber() {
	select {
	case i.gitPoke <- struct{}{}:
	default:
	}
}
