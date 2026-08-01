// Package worktree is terva's managed git-worktree engine: it creates, lists,
// claims, releases, and removes git worktrees for a repository and reasons
// about reuse via an available/claimed model plus the commit each worktree was
// branched from. It depends only on the standard library, x/sys, and a real
// `git` binary, so it is fully unit-testable against a temp repo.
//
// Folded in from the retired terva-git-worktree extension (stage 1 of the
// fold-in): the engine logic is the extension's, adapted to first-class state
// under $TERVA_HOME/worktrees with a one-time registry migration that pins
// legacy checkouts at their extension-era paths (git records worktree paths in
// .git/worktrees — the checkouts never move). Consumers: the worktree_* built-in
// tools (packages/agent/tools) and the swarm's per-agent isolation lease
// (workspace's AcquireWorktree carrier), which now calls this package directly
// instead of dialing an extension over the wire.
package worktree

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	branchPrefix = "wt/"
	// defaultClaimTTL is how long a claim from a non-current session is treated
	// as live before it's considered (reported, not auto-stolen) stale.
	defaultClaimTTL = 12 * time.Hour
)

// Manager runs the worktree operations. The clock, pid-liveness probe, self pid,
// and TTL are fields so tests can drive staleness deterministically.
type Manager struct {
	mu       sync.Mutex
	now      func() time.Time
	pidAlive func(int) bool
	selfPID  int
	ttl      time.Duration
}

// NewManager returns a Manager wired to the real clock and process.
func NewManager() *Manager {
	return &Manager{
		now:      time.Now,
		pidAlive: pidAlive,
		selfPID:  os.Getpid(),
		ttl:      defaultClaimTTL,
	}
}

// status is the derived per-worktree state returned by list & create.
type status struct {
	State       string // "available" | "claimed"
	ClaimedBy   string // "self" | "<session>" | ""(none, when available)
	Stale       bool
	StaleReason string
}

// deriveStatus classifies a claim relative to the current session. A claim owned
// by the current session is "claimed by self". A claim from another session is
// live (claimed by them) only while its pid is alive and it's within TTL;
// otherwise it's available but reported stale, never silently reclaimed.
func (m *Manager) deriveStatus(c *Claim, currentSession string) status {
	if c == nil {
		return status{State: "available"}
	}
	if currentSession != "" && c.SessionID == currentSession {
		return status{State: "claimed", ClaimedBy: "self"}
	}
	pidDead := !m.pidAlive(c.PID)
	expired := false
	if t, err := time.Parse(time.RFC3339, c.ClaimedAt); err == nil {
		expired = m.now().Sub(t) > m.ttl
	} else {
		expired = true // unparseable timestamp ⇒ treat as expired
	}
	if !pidDead && !expired {
		owner := c.SessionID
		if owner == "" {
			owner = "(no session)"
		}
		return status{State: "claimed", ClaimedBy: owner}
	}
	owner := c.SessionID
	if owner == "" {
		owner = "(no session)"
	}
	var reasons []string
	if pidDead {
		reasons = append(reasons, fmt.Sprintf("owning process (pid %d) is gone", c.PID))
	}
	if expired {
		reasons = append(reasons, "claim older than TTL")
	}
	return status{
		State:       "available",
		Stale:       true,
		StaleReason: fmt.Sprintf("%s: %s", owner, strings.Join(reasons, ", ")),
	}
}

func (m *Manager) nowRFC() string { return m.now().UTC().Format(time.RFC3339) }

func (m *Manager) newClaim(session string) *Claim {
	return &Claim{SessionID: session, PID: m.selfPID, ClaimedAt: m.nowRFC()}
}

// reconcile drops registry entries git no longer knows about (worktree removed
// out of band). Reports whether the registry changed.
func reconcile(r *repo, reg *Registry, gwts map[string]gitWorktree) bool {
	changed := false
	for name, e := range reg.Worktrees {
		if _, ok := gwts[canonPath(r.entryPath(name, e))]; !ok {
			delete(reg.Worktrees, name)
			changed = true
		}
	}
	return changed
}

// CreateArgs are the inputs to Create.
type CreateArgs struct {
	Name             string
	Base             string // optional ref/sha; default: current HEAD
	ReuseIfAvailable bool   // if Name exists & available, claim+return it
}

// CreateResult is the JSON shape Create returns to the agent.
type CreateResult struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Branch     string `json:"branch"`
	BaseCommit string `json:"base_commit"`
	BaseRef    string `json:"base_ref"`
	HeadCommit string `json:"head_commit"`
	Status     string `json:"status"`     // create always claims => "claimed"
	ClaimedBy  string `json:"claimed_by"` // "self"
	Reused     bool   `json:"reused"`     // true if an existing worktree was returned
}

// Create makes (or reuses) a worktree and claims it for the current session.
// Asking for a name that already exists and is available claims and returns it
// instead of erroring; a name claimed by another live session is refused.
func (m *Manager) Create(env Env, args CreateArgs) (*CreateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	name := slugify(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	r, err := resolveRepo(env)
	if err != nil {
		return nil, err
	}
	lk, err := acquireLock(r.lockPath())
	if err != nil {
		return nil, fmt.Errorf("lock repo: %w", err)
	}
	defer lk.Release()

	reg, err := loadRegistry(r)
	if err != nil {
		return nil, err
	}
	gwts, err := listWorktrees(r.cwd)
	if err != nil {
		return nil, err
	}
	reconcile(r, reg, gwts)

	if entry, ok := reg.Worktrees[name]; ok {
		st := m.deriveStatus(entry.Claim, env.SessionID)
		switch {
		case st.ClaimedBy == "self":
			return m.createResult(r, name, entry, true), nil
		case st.State == "available":
			if !args.ReuseIfAvailable {
				return nil, fmt.Errorf("worktree %q already exists; pass a different name or set reuse_if_available", name)
			}
			entry.Claim = m.newClaim(env.SessionID)
			if err := saveRegistry(r.registryPath(), reg); err != nil {
				return nil, err
			}
			return m.createResult(r, name, entry, true), nil
		default: // claimed by another live session
			return nil, fmt.Errorf("worktree %q is claimed by %s; pick a different name", name, st.ClaimedBy)
		}
	}

	path := r.worktreePath(name)
	if _, ok := gwts[canonPath(path)]; ok {
		return nil, fmt.Errorf("an unmanaged worktree already exists at %s; remove it or pick another name", path)
	}

	branch := branchPrefix + name
	resume := branchExists(r.cwd, branch)

	// Determine the base. A leftover branch from a prior remove (which keeps the
	// branch by default) is resumed on its own tip; an explicit base then can't
	// be honored, so flag the conflict instead of silently ignoring it.
	var baseRef, baseArg, baseCommit string
	if resume {
		if args.Base != "" {
			return nil, fmt.Errorf("branch %s already exists from a previous worktree; cannot apply base %q — remove it (delete_branch) or pick another name", branch, args.Base)
		}
		baseRef = branch
		baseCommit, err = resolveCommit(r.cwd, branch)
		if err != nil {
			return nil, err
		}
	} else {
		baseRef, baseArg = args.Base, args.Base
		if baseRef == "" {
			baseRef = currentRef(r.cwd)
			baseArg = "HEAD"
		}
		baseCommit, err = resolveCommit(r.cwd, baseArg)
		if err != nil {
			return nil, fmt.Errorf("base %q is not a valid ref", baseRef)
		}
	}

	if err := os.MkdirAll(r.worktreesDir(), 0o755); err != nil {
		return nil, err
	}
	add := []string{"worktree", "add", "--quiet", path}
	if resume {
		add = append(add, branch) // attach the existing branch
	} else {
		add = append(add, "-b", branch, baseArg) // fresh branch off base
	}
	if _, err := runGit(r.cwd, add...); err != nil {
		return nil, err
	}
	entry := &Entry{
		Branch:     branch,
		BaseCommit: baseCommit,
		BaseRef:    baseRef,
		CreatedAt:  m.nowRFC(),
		Claim:      m.newClaim(env.SessionID),
		Path:       path, // pinned so a future home move can't orphan it
	}
	reg.Worktrees[name] = entry
	if err := saveRegistry(r.registryPath(), reg); err != nil {
		// Don't strand a worktree git knows about but our registry doesn't.
		_, _ = runGit(r.cwd, "worktree", "remove", "--force", path)
		return nil, err
	}
	return m.createResult(r, name, entry, false), nil
}

func (m *Manager) createResult(r *repo, name string, e *Entry, reused bool) *CreateResult {
	path := r.entryPath(name, e)
	return &CreateResult{
		Name:       name,
		Path:       path,
		Branch:     e.Branch,
		BaseCommit: e.BaseCommit,
		BaseRef:    e.BaseRef,
		HeadCommit: headCommit(path),
		Status:     "claimed",
		ClaimedBy:  "self",
		Reused:     reused,
	}
}

// ClaimArgs are the inputs to Claim.
type ClaimArgs struct {
	Name string
}

// ClaimResult is the JSON shape Claim returns.
type ClaimResult struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	Branch         string `json:"branch"`
	BaseCommit     string `json:"base_commit"`
	BaseRef        string `json:"base_ref"`
	HeadCommit     string `json:"head_commit"`
	Status         string `json:"status"`     // "claimed"
	ClaimedBy      string `json:"claimed_by"` // "self"
	ReclaimedStale bool   `json:"reclaimed_stale,omitempty"`
}

// Claim takes an existing managed worktree for the current session without
// creating or deleting one — the clean hand-off primitive for reusing an idle
// (available) worktree. Idempotent when this session already holds it; refuses a
// worktree held by another live session. Reclaiming a stale claim is allowed and
// reported via reclaimed_stale.
func (m *Manager) Claim(env Env, args ClaimArgs) (*ClaimResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	name := slugify(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	r, err := resolveRepo(env)
	if err != nil {
		return nil, err
	}
	lk, err := acquireLock(r.lockPath())
	if err != nil {
		return nil, fmt.Errorf("lock repo: %w", err)
	}
	defer lk.Release()

	reg, err := loadRegistry(r)
	if err != nil {
		return nil, err
	}
	gwts, err := listWorktrees(r.cwd)
	if err != nil {
		return nil, err
	}
	reconcile(r, reg, gwts)

	entry, ok := reg.Worktrees[name]
	if !ok {
		return nil, fmt.Errorf("no managed worktree %q; use worktree_create to make one", name)
	}
	st := m.deriveStatus(entry.Claim, env.SessionID)
	switch {
	case st.ClaimedBy == "self":
		// Already ours — idempotent, no write needed.
	case st.State == "available":
		entry.Claim = m.newClaim(env.SessionID)
		if err := saveRegistry(r.registryPath(), reg); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("worktree %q is claimed by %s; cannot claim", name, st.ClaimedBy)
	}
	path := r.entryPath(name, entry)
	return &ClaimResult{
		Name:           name,
		Path:           path,
		Branch:         entry.Branch,
		BaseCommit:     entry.BaseCommit,
		BaseRef:        entry.BaseRef,
		HeadCommit:     headCommit(path),
		Status:         "claimed",
		ClaimedBy:      "self",
		ReclaimedStale: st.Stale,
	}, nil
}

// ReleaseArgs are the inputs to Release.
type ReleaseArgs struct {
	Name string
}

// ReleaseResult is the JSON shape Release returns.
type ReleaseResult struct {
	Name     string `json:"name"`
	Released bool   `json:"released"` // true if a claim was cleared
	Status   string `json:"status"`   // "available" afterward
}

// Release frees this session's claim on a managed worktree so another agent can
// take it, without removing the worktree. Clearing a stale claim (no live owner)
// is allowed as cleanup; releasing a worktree held by another live session is
// refused.
func (m *Manager) Release(env Env, args ReleaseArgs) (*ReleaseResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	name := slugify(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	r, err := resolveRepo(env)
	if err != nil {
		return nil, err
	}
	lk, err := acquireLock(r.lockPath())
	if err != nil {
		return nil, fmt.Errorf("lock repo: %w", err)
	}
	defer lk.Release()

	reg, err := loadRegistry(r)
	if err != nil {
		return nil, err
	}
	gwts, err := listWorktrees(r.cwd)
	if err != nil {
		return nil, err
	}
	reconcile(r, reg, gwts)

	entry, ok := reg.Worktrees[name]
	if !ok {
		return nil, fmt.Errorf("no managed worktree %q", name)
	}
	st := m.deriveStatus(entry.Claim, env.SessionID)
	if st.State == "claimed" && st.ClaimedBy != "self" {
		return nil, fmt.Errorf("worktree %q is claimed by %s; only its owner can release it", name, st.ClaimedBy)
	}
	released := entry.Claim != nil
	if released {
		entry.Claim = nil
		if err := saveRegistry(r.registryPath(), reg); err != nil {
			return nil, err
		}
	}
	return &ReleaseResult{Name: name, Released: released, Status: "available"}, nil
}

// ListResult is the JSON shape List returns.
type ListResult struct {
	RepoKey     string      `json:"repo_key"`
	CWDWorktree *string     `json:"cwd_worktree"` // listed worktree cwd is in, or null
	Worktrees   []*ListItem `json:"worktrees"`
}

// ListItem is one worktree in a ListResult.
type ListItem struct {
	Name        string  `json:"name"`
	Path        string  `json:"path"`
	Branch      string  `json:"branch"`
	BaseCommit  string  `json:"base_commit"`
	BaseRef     string  `json:"base_ref"`
	HeadCommit  string  `json:"head_commit"`
	Status      string  `json:"status"`
	ClaimedBy   *string `json:"claimed_by"` // "self" | "<session>" | null
	StaleReason string  `json:"stale_reason,omitempty"`
	Dirty       bool    `json:"dirty"`
	Unmanaged   bool    `json:"unmanaged,omitempty"`
}

// ListFilter narrows what List returns. The zero value matches everything.
type ListFilter struct {
	Status  string // "" any | "available" | "claimed"
	BaseRef string // "" any | only worktrees whose base_ref equals this
	Mine    bool   // only worktrees claimed by the current session
}

func (f ListFilter) empty() bool { return f.Status == "" && f.BaseRef == "" && !f.Mine }

func (f ListFilter) matches(it *ListItem) bool {
	if f.Status != "" && it.Status != f.Status {
		return false
	}
	if f.BaseRef != "" && it.BaseRef != f.BaseRef {
		return false
	}
	if f.Mine && (it.ClaimedBy == nil || *it.ClaimedBy != "self") {
		return false
	}
	return true
}

// List joins git's worktree list with the registry, derives status, and reports
// drift (base vs current HEAD), dirtiness, and which worktree cwd is in. It is
// read-only except for reconciling away worktrees git has dropped. An optional
// filter narrows the returned worktrees (e.g. available ones branched from main)
// without affecting cwd_worktree.
func (m *Manager) List(env Env, filter ListFilter) (*ListResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	r, err := resolveRepo(env)
	if err != nil {
		return nil, err
	}
	lk, err := acquireLock(r.lockPath())
	if err != nil {
		return nil, fmt.Errorf("lock repo: %w", err)
	}
	defer lk.Release()

	reg, err := loadRegistry(r)
	if err != nil {
		return nil, err
	}
	gwts, err := listWorktrees(r.cwd)
	if err != nil {
		return nil, err
	}
	if reconcile(r, reg, gwts) {
		_ = saveRegistry(r.registryPath(), reg)
	}

	items := make([]*ListItem, 0, len(reg.Worktrees))
	for _, name := range sortedNames(reg.Worktrees) {
		e := reg.Worktrees[name]
		path := r.entryPath(name, e)
		gw := gwts[canonPath(path)]
		st := m.deriveStatus(e.Claim, env.SessionID)
		dirty, _ := worktreeDirty(path)
		head := gw.Head
		if head == "" {
			head = headCommit(path)
		}
		items = append(items, &ListItem{
			Name:        name,
			Path:        path,
			Branch:      e.Branch,
			BaseCommit:  e.BaseCommit,
			BaseRef:     e.BaseRef,
			HeadCommit:  head,
			Status:      st.State,
			ClaimedBy:   claimedByPtr(st.ClaimedBy),
			StaleReason: st.StaleReason,
			Dirty:       dirty,
		})
	}

	// Surface git worktrees living under our managed dirs that we don't track
	// (e.g. created out-of-band) as unmanaged, so the agent isn't surprised.
	// Both homes count: the first-class one and the retired extension's.
	prefixes := []string{canonPath(r.worktreesDir()) + string(os.PathSeparator)}
	if ld := r.legacyWorktreesDir(); ld != "" {
		prefixes = append(prefixes, canonPath(ld)+string(os.PathSeparator))
	}
	for cp, gw := range gwts {
		under := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(cp, prefix) {
				under = true
				break
			}
		}
		if !under {
			continue
		}
		name := lastPathElem(gw.Path)
		if _, ok := reg.Worktrees[name]; ok {
			continue
		}
		dirty, _ := worktreeDirty(gw.Path)
		items = append(items, &ListItem{
			Name:       name,
			Path:       gw.Path,
			Branch:     gw.Branch,
			HeadCommit: gw.Head,
			Status:     "available",
			Dirty:      dirty,
			Unmanaged:  true,
		})
	}

	// cwd_worktree reflects where the agent actually is, independent of the
	// filter — compute it from the full set before narrowing.
	var cwdName *string
	if top := topLevel(env.CWD); top != "" {
		for _, it := range items {
			if canonPath(it.Path) == top {
				n := it.Name
				cwdName = &n
				break
			}
		}
	}

	if !filter.empty() {
		kept := make([]*ListItem, 0, len(items))
		for _, it := range items {
			if filter.matches(it) {
				kept = append(kept, it)
			}
		}
		items = kept
	}

	return &ListResult{RepoKey: r.key, CWDWorktree: cwdName, Worktrees: items}, nil
}

// RemoveArgs are the inputs to Remove.
type RemoveArgs struct {
	Name         string
	Force        bool
	DeleteBranch bool
}

// RemoveResult is the JSON shape Remove returns.
type RemoveResult struct {
	Name          string `json:"name"`
	Removed       bool   `json:"removed"`
	BranchDeleted bool   `json:"branch_deleted"`
}

// Remove deletes a managed worktree. It refuses when the worktree has
// uncommitted changes or unmerged/unpushed commits unless Force is set — a clear
// error the agent can act on, not a silent destroy. The branch is left by
// default (the work may be unmerged); DeleteBranch is an explicit opt-in.
func (m *Manager) Remove(env Env, args RemoveArgs) (*RemoveResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	name := slugify(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	r, err := resolveRepo(env)
	if err != nil {
		return nil, err
	}
	lk, err := acquireLock(r.lockPath())
	if err != nil {
		return nil, fmt.Errorf("lock repo: %w", err)
	}
	defer lk.Release()

	reg, err := loadRegistry(r)
	if err != nil {
		return nil, err
	}
	gwts, err := listWorktrees(r.cwd)
	if err != nil {
		return nil, err
	}

	entry, ok := reg.Worktrees[name]
	if !ok {
		return nil, fmt.Errorf("no managed worktree %q", name)
	}
	path := r.entryPath(name, entry)
	_, known := gwts[canonPath(path)]

	// Enforce safety only when the checkout actually exists on disk. If the dir
	// was deleted out from under git, there's nothing to protect — fall through
	// to prune-based cleanup so a vanished worktree doesn't strand its registry
	// entry (the design's "recover gracefully" path).
	if known && !args.Force && dirExists(path) {
		dirty, derr := worktreeDirty(path)
		if derr != nil || dirty {
			return nil, fmt.Errorf("worktree %q has uncommitted changes; commit or stash them, or pass force:true", name)
		}
		if unmerged, why := hasUnmergedWork(path, entry.BaseCommit); unmerged {
			return nil, fmt.Errorf("worktree %q has unmerged work (%s); pass force:true to remove anyway", name, why)
		}
	}

	if known {
		rmArgs := []string{"worktree", "remove", path}
		if args.Force {
			rmArgs = []string{"worktree", "remove", "--force", path}
		}
		if _, err := runGit(r.cwd, rmArgs...); err != nil {
			// The dir may have been deleted out from under git; prune and treat
			// as removed if that clears it, else surface the original error.
			if _, perr := runGit(r.cwd, "worktree", "prune"); perr != nil {
				return nil, err
			}
		}
	}
	_, _ = runGit(r.cwd, "worktree", "prune")

	branchDeleted := false
	if args.DeleteBranch && entry.Branch != "" {
		if _, err := runGit(r.cwd, "branch", "-D", entry.Branch); err == nil {
			branchDeleted = true
		}
	}

	delete(reg.Worktrees, name)
	if err := saveRegistry(r.registryPath(), reg); err != nil {
		return nil, err
	}
	return &RemoveResult{Name: name, Removed: true, BranchDeleted: branchDeleted}, nil
}

// collectMaxCommits bounds the per-worktree commit list in a collect view.
const collectMaxCommits = 10

// CollectResult summarizes the work pending across all managed worktrees so the
// user can decide what to merge back. The engine never merges automatically.
type CollectResult struct {
	RepoKey   string         `json:"repo_key"`
	Worktrees []*CollectItem `json:"worktrees"`
}

// CollectItem is one worktree's pending work relative to its base.
type CollectItem struct {
	Name       string   `json:"name"`
	Branch     string   `json:"branch"`
	BaseRef    string   `json:"base_ref"`
	BaseCommit string   `json:"base_commit"`
	HeadCommit string   `json:"head_commit"`
	Ahead      int      `json:"ahead"`             // commits beyond base
	Commits    []string `json:"commits,omitempty"` // oneline subjects (capped)
	Dirty      bool     `json:"dirty"`
	Unpushed   bool     `json:"unpushed"` // has commits not on an upstream (best-effort)
}

// Collect reports, per managed worktree, how far its branch is ahead of its base
// (with the commit subjects), whether it is dirty, and whether it has unpushed
// work — the read-only merge-back overview. It never merges; the user reviews and
// merges manually.
func (m *Manager) Collect(env Env) (*CollectResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	r, err := resolveRepo(env)
	if err != nil {
		return nil, err
	}
	lk, err := acquireLock(r.lockPath())
	if err != nil {
		return nil, fmt.Errorf("lock repo: %w", err)
	}
	defer lk.Release()

	reg, err := loadRegistry(r)
	if err != nil {
		return nil, err
	}
	gwts, err := listWorktrees(r.cwd)
	if err != nil {
		return nil, err
	}
	if reconcile(r, reg, gwts) {
		_ = saveRegistry(r.registryPath(), reg)
	}

	items := make([]*CollectItem, 0, len(reg.Worktrees))
	for _, name := range sortedNames(reg.Worktrees) {
		e := reg.Worktrees[name]
		path := r.entryPath(name, e)
		ahead, commits := aheadCommits(path, e.BaseCommit, collectMaxCommits)
		dirty, _ := worktreeDirty(path)
		unpushed, _ := hasUnmergedWork(path, e.BaseCommit)
		items = append(items, &CollectItem{
			Name:       name,
			Branch:     e.Branch,
			BaseRef:    e.BaseRef,
			BaseCommit: e.BaseCommit,
			HeadCommit: headCommit(path),
			Ahead:      ahead,
			Commits:    commits,
			Dirty:      dirty,
			Unpushed:   unpushed,
		})
	}
	return &CollectResult{RepoKey: r.key, Worktrees: items}, nil
}

func claimedByPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func sortedNames(m map[string]*Entry) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// lastPathElem returns the final element of a slash- or backslash-separated
// path without depending on the host separator (git reports forward slashes).
func lastPathElem(p string) string {
	p = strings.TrimRight(p, "/\\")
	if i := strings.LastIndexAny(p, "/\\"); i >= 0 {
		return p[i+1:]
	}
	return p
}
