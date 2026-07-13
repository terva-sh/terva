// Package swarm implements terva's multi-agent supervisor.
//
// A Swarm manages a set of headless terva subprocesses ("agents")
// that share the host's working directory. The interactive TUI
// exposes the supervisor through the /swarm slash command and a
// dashboard dialog; non-TUI code can drive it directly through
// this package.
//
// By default every agent runs with cwd == the parent terva's RepoRoot
// — the same files the user sees, the same files the main agent edits.
// There is no git worktree, no per-agent branch, no isolation. If you
// want parallel edits on a separate branch, use normal git tooling (a
// real worktree, a different terminal) yourself.
//
// A host can opt agents into per-agent working directories by setting
// Config.AcquireWorktree (e.g. wiring it to the terva-git-worktree
// extension): each spawn then leases its own directory and releases it
// exactly once on the agent's terminal transition. The swarm itself
// stays generic — it only sees an opaque AcquireWorktree hook and the
// WorktreeReq/WorktreeLease values; it knows nothing about git.
//
// Each Agent has:
//   - a unique id (short slug + nanoseconds)
//   - a Runner (the thing that actually executes the task)
//   - a Status string + Activity string that the dashboard reads
//
// The Runner abstraction means tests can swap a fake in instead of
// really spawning a subprocess; the production Runner shells out to
// `terva --swarm-agent ...` so we reuse terva's own model resolution
// and tooling without re-implementing the agent loop.
package swarm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/privfs"
)

// Status is the high-level lifecycle state of an Agent.
type Status string

const (
	StatusPending  Status = "pending"  // created, not started yet
	StatusRunning  Status = "running"  // Runner.Run is in flight
	StatusDone     Status = "done"     // Runner.Run returned nil
	StatusFailed   Status = "failed"   // Runner.Run returned an error
	StatusKilled   Status = "killed"   // Stop() called before completion
	StatusDetached Status = "detached" // reloaded from disk; no live runner
)

// Config configures a Swarm.
type Config struct {
	// Root is the directory under which per-agent state files live.
	// Typically <TervaHome>/swarm, but tests pass a tempdir.
	Root string

	// RepoRoot is the default working directory a spawned agent runs
	// in — the same cwd the parent terva is using. When
	// AcquireWorktree is nil (the default) there is no per-agent
	// isolation: agents edit the host's files directly. When it is set
	// and yields a non-empty Dir, that leased directory is used
	// instead and RepoRoot is only the fallback.
	RepoRoot string

	// AcquireWorktree, when non-nil, supplies a dedicated working
	// directory for each spawned agent (e.g. an isolated git worktree)
	// instead of RepoRoot. Returning a Lease whose Dir is "" falls back
	// to RepoRoot. Release is called exactly once when the agent reaches
	// a terminal state (done/failed/killed) or the swarm is torn down.
	// When nil, every agent uses RepoRoot (today's behavior). The swarm
	// treats this as an opaque hook — it knows nothing about git or
	// extensions; the host decides what a "worktree" is and how to
	// create/release one. Returning a non-nil error fails the spawn
	// loudly (used when isolation was explicitly requested but the
	// backing mechanism is unavailable).
	AcquireWorktree func(ctx context.Context, req WorktreeReq) (WorktreeLease, error)

	// NewRunner produces the Runner for an Agent. If nil, the default
	// `terva --swarm-agent ...` exec runner is used. Tests inject a fake
	// here.
	NewRunner func(a *Agent) Runner

	// Now is a clock seam for tests; defaults to time.Now.
	Now func() time.Time

	// StopGrace is how long Stop waits for a child to drain and exit
	// cleanly (in response to the inbox shutdown message) before it
	// hard-cancels the child's context as a backstop. Defaults to
	// defaultStopGrace; tests set it short.
	StopGrace time.Duration
}

// WorktreeReq describes the agent that needs a working directory. It
// carries just enough identity for the host's AcquireWorktree hook to
// derive a stable, readable name; the swarm fills it in at spawn time.
type WorktreeReq struct {
	AgentID  string
	Task     string
	Model    string
	Provider string
}

// WorktreeLease is a working directory plus a release hook, returned by
// Config.AcquireWorktree. Dir == "" means "use RepoRoot". Release, when
// non-nil, is invoked exactly once when the agent reaches a terminal
// state or the swarm is torn down (see Agent.finish).
type WorktreeLease struct {
	Dir     string // "" => use RepoRoot
	Release func()
}

// defaultStopGrace is the window a graceful Stop gives a child to
// drain its in-flight turn and exit on its own before the context is
// cancelled as a backstop.
const defaultStopGrace = 5 * time.Second

// Runner executes one agent task. Run blocks until the task finishes,
// is cancelled via ctx, or hits an unrecoverable error.
//
// Run should report progress by writing short human-readable strings
// to the activity channel and final transcript text to transcript.
// Both channels are non-blocking sinks owned by the Swarm; if the
// dashboard isn't reading, sends are dropped.
type Runner interface {
	Run(ctx context.Context, sink Sink) error
}

// Sink is how a Runner reports activity and transcript back to the
// supervisor. All methods are safe to call from any goroutine and
// never block.
type Sink interface {
	// Activity sets the one-line "what is this agent doing right now"
	// string shown in the dashboard.
	Activity(msg string)
	// Transcript appends a chunk of agent output (typically a final
	// assistant message) to the agent's running transcript.
	Transcript(chunk string)
	// Result records the child's latest complete assistant message —
	// the sub-agent's current answer. Distinct from Transcript, which
	// keeps the line-oriented running log; Result lets the auto-swarm
	// recap surface the actual findings instead of a truncated tail.
	Result(text string)
	// GuardNudge marks that the child was re-prompted by the finalize
	// guard (OpenWorkGateMessage) AFTER it tried to finish. The answer
	// recorded so far is the child's intended deliverable; whatever it
	// says next may be mere housekeeping ("all tasks complete"), and
	// Findings must not let that clobber the real result.
	GuardNudge()
}

// OpenWorkGateMessage is the at-close re-prompt injected once when the
// model tries to finish while tracked work is still open. A soft nudge —
// it grants one more turn, not a hard stop — and core caps it to once
// per prompt. It lives in this package (not the agent host that injects
// it) because the swarm supervisor must ALSO recognize it in a child's
// event stream: the child's literal answer to this nudge is usually task
// housekeeping, which must not displace its findings in the recap.
const OpenWorkGateMessage = "You indicated you're finishing, but tracked items are still open. Complete them, or confirm they're intentionally left incomplete."

// Swarm supervises a set of Agents.
type Swarm struct {
	cfg Config

	// ctx is the swarm's own lifecycle root: every spawned/resumed
	// agent's context derives from it, NOT from the caller's context.
	// The spawning call is usually a tool dispatch inside a live turn,
	// and a sub-agent must outlive that turn — an Esc-cancel, a turn
	// error, or the turn simply ending must not tear down background
	// workers mid-task (they once died to exec.CommandContext through
	// exactly that chain). cancelAll is the last-resort teardown used
	// by StopAllAndWait after the graceful drain window.
	ctx       context.Context
	cancelAll context.CancelFunc

	mu     sync.Mutex
	agents map[string]*Agent
	// claimed holds agent ids minted by an in-flight SpawnReq that has not
	// yet registered in agents — the reservation that makes concurrent
	// spawns collision-proof between mint and insert. Guarded by mu.
	claimed map[string]bool
	order   []string // creation order for stable listing

	// activeSession is the host session id the dashboard is
	// currently scoped to. When non-empty, SnapshotAll filters out
	// agents whose SessionID doesn't match (legacy / unscoped
	// agents with SessionID == "" are always shown). Spawn stamps
	// new agents with this value so they appear only in the session
	// that created them. When empty (the default), the historical
	// "show everything" behaviour is preserved — important for
	// tests and any scripted use of the Swarm that doesn't bother
	// with sessions.
	activeSession string
}

// New constructs a Swarm from cfg. Missing config fields are filled
// with defaults.
func New(cfg Config) *Swarm {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.StopGrace <= 0 {
		cfg.StopGrace = defaultStopGrace
	}
	if cfg.NewRunner == nil {
		cfg.NewRunner = func(a *Agent) Runner { return &execRunner{agent: a} }
	}
	s := &Swarm{
		cfg:     cfg,
		agents:  map[string]*Agent{},
		claimed: map[string]bool{},
	}
	s.ctx, s.cancelAll = context.WithCancel(context.Background())
	return s
}

// SetActiveSession scopes the dashboard view (and Spawn stamping)
// to a particular host terva session id. Pass empty to clear the
// scope and revert to "show every agent" (the original behaviour).
//
// Existing in-memory agents keep their SessionID; only the filter
// applied at snapshot time changes. So swapping the active session
// with /sessions instantly re-narrows the dashboard without
// touching any agent state.
func (f *Swarm) SetActiveSession(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activeSession = id
}

// ActiveSession returns the current scope, mostly for tests and
// diagnostics. Empty means "no scope; show everything".
func (f *Swarm) ActiveSession() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeSession
}

// agentStateDir is the per-agent state directory laid out as:
//
//	<root>/agents/<id>/
//	  events.jsonl   durable event log (runner-owned)
//	  in.sock        unix socket inbox  (child-owned)
//	  session.json   persistent agent session (child-owned)
//	  meta.json      static metadata (id, task)
func (f *Swarm) agentStateDir(id string) string {
	return filepath.Join(f.cfg.Root, "agents", id)
}

// claimAgentID mints an agent id that is unique among live agents, reloaded
// agents, concurrent in-flight spawns, and leftover on-disk state dirs.
// newAgentID's nano suffix repeats every millisecond, and a duplicate id
// silently overwrote the first agent's map entry — orphaning it (invisible
// to /swarm, unreachable by StopAll: its runner ran forever) while both
// children shared one state dir (session.json, events.jsonl, meta.json).
// Collisions re-mint with a bumped suffix under the lock; the caller must
// release the claim via f.claimed once registered (or on a failed spawn).
func (f *Swarm) claimAgentID(task string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	base := newAgentID(task, f.cfg.Now())
	id := base
	for i := 1; f.idTakenLocked(id); i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	f.claimed[id] = true
	return id
}

// idTakenLocked reports whether an agent id is already in use: claimed by an
// in-flight spawn, registered live (Spawn or Reload both insert into agents),
// or present on disk from a prior process whose agent was never reloaded —
// reusing that dir would clobber its meta.json and interleave two agents'
// event logs. Caller holds f.mu; the Stat is small and spawns are rare.
func (f *Swarm) idTakenLocked(id string) bool {
	if f.claimed[id] {
		return true
	}
	if _, exists := f.agents[id]; exists {
		return true
	}
	if _, err := os.Stat(f.agentStateDir(id)); err == nil {
		return true
	}
	return false
}

// DefaultRoot is the swarm state root every host wires under a terva home —
// the directory agentStateDir's layout lives in. Exported so out-of-package
// readers (e.g. session_inspect resolving a sub-agent id to its transcript)
// derive the same path instead of duplicating the join.
func DefaultRoot(tervaHome string) string {
	return filepath.Join(tervaHome, "swarm")
}

// AgentSessionPath returns the persistent session transcript for one agent id
// under a swarm root (see agentStateDir's layout). The id is used as a path
// element verbatim, so callers taking ids from an untrusted source must
// path-validate first.
func AgentSessionPath(root, id string) string {
	return filepath.Join(root, "agents", id, "session.json")
}

// SpawnRequest configures a Spawn. Only Task is required; the rest
// are optional. Model + Provider, when set, get baked into the
// child argv as --model / --provider so the agent runs against the
// chosen model regardless of the parent's current selection.
type SpawnRequest struct {
	Task     string
	Model    string // optional override; child resolves default if empty
	Provider string // optional override; usually paired with Model
	// Persona, when set, is baked into the child's --persona flag so the
	// sub-agent boots as that persona (a name resolved against the trusted
	// library, or a path for human-initiated spawns). Empty = host default.
	Persona string
	// Experience ("chat"/"play"), Substrate (an opaque scheme-qualified ref,
	// reserved), and Card (a card path) bake into the child's --chat/--play,
	// --substrate, and --card flags so the sub-agent boots embodied. All empty
	// = a plain coding sub-agent. See docs/proposals/agent-dispatch.md.
	Experience string
	Substrate  string
	Card       string

	// SessionID scopes the agent to the host terva session that spawned it:
	// the /swarm dashboard narrows to matching agents, and the id persists in
	// meta.json (the durable "which conversation do you belong to" stamp).
	// Callers with a session in hand (the spawn tools resolve it from the
	// dispatch-context agent) should set it; empty falls back to the swarm's
	// SetActiveSession value, which no production host currently sets.
	SessionID string
}

// Spawn creates a new Agent for the given task, allocates its
// on-disk state directory (events log, inbox socket path, session
// file path), and starts the Runner on a background goroutine. The
// returned Agent is already in StatusRunning (or StatusFailed if
// state setup failed before the goroutine started). This is the
// historical signature; callers that want to override the child's
// model use SpawnReq instead.
func (f *Swarm) Spawn(ctx context.Context, task string) (*Agent, error) {
	return f.SpawnReq(ctx, SpawnRequest{Task: task})
}

// SpawnReq is the full-fat variant of Spawn that accepts a
// SpawnRequest. Existing callers can keep using Spawn; new code that
// wants to pin the child's model uses this.
//
// ctx governs only the call-scoped setup (AcquireWorktree). The spawned
// agent's lifetime is swarm-scoped — it survives the caller's turn and
// ends only via Stop/StopAllAndWait (see the Swarm.ctx field note).
//
// By default every spawned agent runs with cwd == cfg.RepoRoot — the
// same working directory as the host, no per-agent worktree or branch.
// When cfg.AcquireWorktree is set the agent instead leases its own
// directory (e.g. an isolated git worktree); the lease's Release hook
// is stored on the Agent and fired exactly once when the agent reaches
// a terminal state (see Agent.finish). An AcquireWorktree error fails
// the spawn — isolation was explicitly requested, so a missing backing
// mechanism is a misconfiguration, not a silent fallback.
func (f *Swarm) SpawnReq(ctx context.Context, req SpawnRequest) (*Agent, error) {
	task := strings.TrimSpace(req.Task)
	if task == "" {
		return nil, errors.New("swarm: empty task")
	}
	id := f.claimAgentID(task)
	// Every early return below must release the claim, or the id (and its
	// suffix) would stay burned for the life of the process. Registration
	// clears claimedID so the deferred release is a no-op on success.
	claimedID := id
	defer func() {
		if claimedID != "" {
			f.mu.Lock()
			delete(f.claimed, claimedID)
			f.mu.Unlock()
		}
	}()

	dir := f.cfg.RepoRoot
	var releaseWorktree func()
	if f.cfg.AcquireWorktree != nil {
		lease, err := f.cfg.AcquireWorktree(ctx, WorktreeReq{
			AgentID:  id,
			Task:     task,
			Model:    strings.TrimSpace(req.Model),
			Provider: strings.TrimSpace(req.Provider),
		})
		if err != nil {
			return nil, fmt.Errorf("swarm: acquire worktree: %w", err)
		}
		if lease.Dir != "" {
			dir = lease.Dir
		}
		releaseWorktree = lease.Release
	}

	stateDir := f.agentStateDir(id)
	if err := privfs.MkdirAll(stateDir); err != nil {
		return nil, fmt.Errorf("swarm state dir: %w", err)
	}
	logPath := filepath.Join(stateDir, "events.jsonl")
	sessionPath := AgentSessionPath(f.cfg.Root, id)
	// Unix sockets have a hard 104-byte path limit on darwin and 108
	// on linux. Long TERVA_HOME paths plus an agent slug blow that cap
	// quickly. Pick the shortest path that still keeps sockets
	// per-swarm-root so two terva instances on the same machine don't
	// collide. inboxSocketPath falls back from $TMPDIR to /tmp if
	// neither is short enough.
	inboxPath, err := inboxSocketPath(f.cfg.Root, id)
	if err != nil {
		return nil, fmt.Errorf("swarm inbox path: %w", err)
	}

	// The spawning conversation's own session id wins — a daemon hosts many
	// sessions, so a per-spawn stamp is the only value that is always right.
	// Fall back to the swarm-wide SetActiveSession scope (snapshotted under
	// the lock; reading it unlocked would race a concurrent set).
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		f.mu.Lock()
		sessionID = f.activeSession
		f.mu.Unlock()
	}

	a := &Agent{
		ID:           id,
		Task:         task,
		Dir:          dir,
		Started:      f.cfg.Now(),
		Model:        strings.TrimSpace(req.Model),
		Provider:     strings.TrimSpace(req.Provider),
		Persona:      strings.TrimSpace(req.Persona),
		Experience:   strings.TrimSpace(req.Experience),
		Substrate:    strings.TrimSpace(req.Substrate),
		Card:         strings.TrimSpace(req.Card),
		SessionID:    sessionID,
		InboxPath:    inboxPath,
		EventLogPath: logPath,
		SessionPath:  sessionPath,
		inbox:        NewInbox(inboxPath),
		status:       StatusPending,
		activity:     "queued",
		done:         make(chan struct{}),

		releaseWorktree: releaseWorktree,
	}
	// The agent's lifetime is swarm-scoped, deliberately NOT the
	// caller's ctx: a spawn arrives on a turn's tool-dispatch context,
	// and the sub-agent must survive that turn ending, erroring, or
	// being cancelled. The caller ctx still governs the call-scoped
	// setup above (AcquireWorktree).
	a.ctx, a.cancel = context.WithCancel(f.ctx)
	a.runner = f.cfg.NewRunner(a)

	f.mu.Lock()
	f.agents[id] = a
	f.order = append(f.order, id)
	delete(f.claimed, id)
	f.mu.Unlock()
	claimedID = ""

	// Persist the agent's identity so a later `terva` invocation can
	// reload it from disk via Swarm.Reload. Best-effort: if the disk
	// is read-only we still let the runner start, the user just won't
	// see this agent on the next launch.
	_ = writeAgentMeta(stateDir, a)

	go f.run(a)
	return a, nil
}

// SendInput delivers a raw line to the agent's inbox. Prefer the
// typed SendMsg / SendUserTurn; this remains for the transport tests
// and any caller holding a pre-formatted line. Returns an error
// wrapping ErrNotReady if the child hasn't opened its listener yet.
func (f *Swarm) SendInput(id, msg string) error {
	a := f.Get(id)
	if a == nil {
		return fmt.Errorf("no such agent %q", id)
	}
	if a.inbox == nil {
		return fmt.Errorf("agent %s has no inbox", a.ID)
	}
	return a.inbox.SendInput(msg)
}

// SendMsg delivers a typed control message to the agent's inbox as a
// newline-safe JSON envelope (see InboxMsg).
func (f *Swarm) SendMsg(id string, m InboxMsg) error {
	a := f.Get(id)
	if a == nil {
		return fmt.Errorf("no such agent %q", id)
	}
	if a.inbox == nil {
		return fmt.Errorf("agent %s has no inbox", a.ID)
	}
	return a.inbox.Send(m)
}

// SendUserTurn sends text as the agent's next user turn. The body is
// carried in a JSON envelope, so multi-line prompts survive intact.
// Callers are expected to have already trimmed and expanded the text.
func (f *Swarm) SendUserTurn(id, text string) error {
	return f.SendMsg(id, InboxMsg{Kind: "user", Text: text})
}

func (f *Swarm) run(a *Agent) {
	a.setStatus(StatusRunning)
	a.setActivity("starting")
	err := a.runner.Run(a.ctx, agentSink{a: a})
	a.mu.Lock()
	a.finished = f.cfg.Now()
	switch {
	case a.status == StatusKilled:
		// Already finalised by Stop.
	case errors.Is(err, context.Canceled):
		a.status = StatusKilled
		a.activity = "cancelled"
	case err != nil:
		a.status = StatusFailed
		a.activity = "error: " + truncate(err.Error(), 120)
		a.lastErr = err
	default:
		a.status = StatusDone
		a.activity = "done"
	}
	a.mu.Unlock()
	close(a.done)
	// Every live agent's runner goroutine ends here — completion,
	// failure, or a Stop/StopAll cancel that made Run return. This is
	// the guaranteed terminal chokepoint, so release the leased
	// worktree (if any) here. finish() is idempotent, so a belt-and-
	// suspenders call from Stop (below) for a runner that's wedged and
	// never returns can't double-release.
	a.finish()
}

// List returns a snapshot of every agent in creation order. The
// returned slice is a copy; callers may iterate without holding the
// swarm lock. Agent fields are read under their own mutex during
// formatting in Snapshot.
func (f *Swarm) List() []*Agent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*Agent, 0, len(f.order))
	for _, id := range f.order {
		out = append(out, f.agents[id])
	}
	return out
}

// Get returns the agent with the given (possibly truncated) id, or
// nil. Matching is prefix-based so the user can type the first few
// characters of a long id.
func (f *Swarm) Get(id string) *Agent {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.agents[id]; ok {
		return a
	}
	// Prefix match.
	var hits []*Agent
	for k, a := range f.agents {
		if strings.HasPrefix(k, id) {
			hits = append(hits, a)
		}
	}
	if len(hits) == 1 {
		return hits[0]
	}
	return nil
}

// Stop gracefully stops the agent: it asks the child to drain via the
// inbox shutdown message (the child aborts any in-flight turn, emits a
// final task_end + agent_stopped, flushes its session, and exits),
// then backstops with a context cancel if the child hasn't exited
// within cfg.StopGrace. This is gentler than the old immediate
// context-kill, which tore the child down mid-write and skipped its
// clean teardown events.
//
// Stop returns immediately; the drain-or-backstop wait runs on a
// background goroutine. Status flips to Killed right away so the
// dashboard reflects the user's intent.
//
// Stop is a no-op for any agent that's not in a live runnable state
// — Done / Failed / Killed (already finalised) and Detached (no
// in-process runner; reloaded from disk). Calling Stop on a detached
// agent must not crash: buildDetachedAgent doesn't allocate a
// context/cancel pair because there's nothing to cancel.
func (f *Swarm) Stop(id string) error {
	a := f.Get(id)
	if a == nil {
		return fmt.Errorf("no such agent %q", id)
	}
	a.mu.Lock()
	switch a.status {
	case StatusDone, StatusFailed, StatusKilled, StatusDetached:
		a.mu.Unlock()
		return nil
	}
	a.status = StatusKilled
	a.activity = "stopping"
	done := a.done
	cancel := a.cancel
	inbox := a.inbox
	a.mu.Unlock()

	// Graceful request first: let the child drain and exit cleanly.
	graceful := false
	if inbox != nil {
		graceful = inbox.Send(InboxMsg{Kind: "shutdown"}) == nil
	}
	grace := f.cfg.StopGrace

	go func() {
		if graceful && done != nil {
			select {
			case <-done:
				// Child exited on its own — no hard cancel needed.
			case <-time.After(grace):
				if cancel != nil {
					cancel()
				}
			}
		} else if cancel != nil {
			// No inbox / send failed: fall straight back to the
			// context cancel (the old behaviour).
			cancel()
		}
		if inbox != nil {
			_ = inbox.Close()
		}
		// Backstop the worktree release. Normally f.run already fired
		// finish() when the runner returned after our cancel; but a
		// runner that ignores cancellation (or an agent with no live
		// runner that somehow slipped past the status guard) would leak
		// its lease. finish() is idempotent, so this never double-
		// releases — it just guarantees the lease is freed even when
		// the runner goroutine doesn't reach its terminal switch.
		a.finish()
	}()
	return nil
}

// StopAll requests a graceful stop of every running agent and returns
// immediately. The drain-or-backstop for each agent runs on background
// goroutines — callers that are about to exit the process should use
// StopAllAndWait instead, or the process dies before the children get
// to flush their terminal events and the logs end mid-sentence.
func (f *Swarm) StopAll() {
	for _, a := range f.List() {
		_ = f.Stop(a.ID)
	}
}

// StopAllAndWait gracefully stops every running agent and waits — up
// to bound (0 means cfg.StopGrace plus a scheduling margin) — for the
// children to drain, write their agent_stopped terminators, and exit.
// It then cancels the swarm's root context as a final backstop so no
// child process can outlive the supervisor's pipes. This is the
// shutdown path for hosts: a child killed silently mid-write shows up
// on the next launch as a mystery truncation; one drained here shows
// up as "shutdown (offline)", resumable.
func (f *Swarm) StopAllAndWait(bound time.Duration) {
	agents := f.List()
	var waits []<-chan struct{}
	for _, a := range agents {
		a.mu.Lock()
		running := a.status == StatusRunning || a.status == StatusPending
		done := a.done
		a.mu.Unlock()
		if !running {
			continue
		}
		_ = f.Stop(a.ID)
		if done != nil {
			waits = append(waits, done)
		}
	}
	if bound <= 0 {
		bound = f.cfg.StopGrace + time.Second
	}
	deadline := time.After(bound)
	for _, done := range waits {
		select {
		case <-done:
		case <-deadline:
			f.cancelAll()
			return
		}
	}
	f.cancelAll()
}

// Remove tears down the per-agent state for a terminated agent. It
// is an error to remove an agent that's still running; call Stop
// first and wait for the status to settle. Detached agents
// (reloaded from disk) remove cleanly because they have no live
// runner racing for the same files.
//
// Remove never touches any source file — it only deletes the agent's
// state directory under <root>/agents/<id>/. Even when worktree
// isolation gave the agent its own checkout, the worktree's lifecycle
// is the host's (via the AcquireWorktree lease), not Remove's.
func (f *Swarm) Remove(id string) error {
	a := f.Get(id)
	if a == nil {
		return fmt.Errorf("no such agent %q", id)
	}
	a.mu.Lock()
	st := a.status
	a.mu.Unlock()
	if st == StatusRunning || st == StatusPending {
		return fmt.Errorf("agent %s still %s", a.ID, st)
	}
	// Defensively release the worktree lease before dropping the agent.
	// A terminal agent reached via f.run already released (and finish()
	// is idempotent); this only matters for a path that terminated
	// without the runner goroutine running its switch. No-op for the
	// common case and for detached agents (which carry no lease).
	a.finish()
	// Best-effort cleanup of the per-agent state directory
	// (meta.json, events.jsonl, session.json, in.sock if it's
	// local). Failing here would leave the user with no recourse,
	// so swallow the error.
	_ = os.RemoveAll(f.agentStateDir(a.ID))
	f.mu.Lock()
	delete(f.agents, a.ID)
	for i, k := range f.order {
		if k == a.ID {
			f.order = append(f.order[:i], f.order[i+1:]...)
			break
		}
	}
	f.mu.Unlock()
	return nil
}

// Snapshot returns a read-only view of one agent. Safe for the TUI
// goroutine to call repeatedly; never blocks on the Runner.
type AgentSnapshot struct {
	ID       string
	Task     string
	Dir      string
	Status   Status
	Activity string
	Started  time.Time
	Finished time.Time
	Err      string
	Tail     string   // last few transcript lines, joined with "\n"
	Lines    []string // full transcript (already capped by Agent.appendTranscript)

	// LastAssistant is the child's most recent complete assistant
	// message — its current answer / findings. Empty until the child
	// emits its first assistant_message. The auto-swarm recap prefers
	// this over Tail so a coordinator sees the actual result rather
	// than a truncated slice of interleaved tool output.
	LastAssistant string

	// PreGuardAssistant is the answer the child had ALREADY delivered
	// when the finalize guard (OpenWorkGateMessage) re-prompted it.
	// Empty unless the guard fired. See Findings for how the two
	// candidates are arbitrated.
	PreGuardAssistant string

	// Model and Provider expose the per-agent overrides set at
	// Spawn time (empty when the agent inherits the child's default
	// resolution). The dashboard surfaces these so the user can
	// confirm which model an agent is running against.
	Model    string
	Provider string

	// Persona is the persona the sub-agent booted as (empty = host
	// default). Surfaced so the dashboard and the auto-swarm summary can
	// label each sub-agent by the specialist that ran it.
	Persona string

	// Experience, Substrate, and Card are Persona's boot-spec siblings
	// (see SpawnRequest), carried so a snapshot reader can identify a
	// card-based immersive actor — which has an empty Persona — instead
	// of showing it identity-less.
	Experience string
	Substrate  string
	Card       string

	// Paths to the agent's durable state. Surface them in the
	// snapshot so the dashboard / /swarm open can read events.jsonl
	// or resume the session without going back through the Agent.
	InboxPath    string
	EventLogPath string
	SessionPath  string
}

// RecapStatus reports the sub-agent's outcome in task terms for the
// auto-swarm recap. A dispatched sub-agent is a long-lived daemon: it
// keeps status=running after its task's turn_end fires, so the raw
// Status reads "running" even though the task is done — which misleads
// a coordinator into thinking the crew is still working. The recap only
// ever describes agents whose task has finished (or that terminated), so
// collapse the non-terminal states to "completed" and pass real failures
// through.
func (s AgentSnapshot) RecapStatus() string {
	switch s.Status {
	case StatusFailed:
		return "failed"
	case StatusKilled:
		return "killed"
	default:
		// running / done / detached / pending: the task reached the
		// recap, so from the coordinator's view it completed.
		return "completed"
	}
}

// Findings returns the sub-agent's answer for the recap: its last
// complete assistant message when captured, else the transcript tail as
// a fallback (older/detached agents, or a task that ended before
// emitting assistant text). Prefer this over Tail directly so a
// coordinator sees the actual result rather than a slice of interleaved
// tool output.
//
// Guard interaction: a child that delivers its report and THEN gets the
// finalize-guard nudge (open tracked items) often answers the nudge with
// pure housekeeping — "confirmed, all tasks complete" — making that the
// last assistant message. The pre-nudge answer was the deliverable, so
// when it is the more substantive of the two it wins. A child that
// re-states (or extends) its report after the nudge still wins on
// length, so the newer text is preferred whenever it plausibly carries
// the findings.
func (s AgentSnapshot) Findings() string {
	last := strings.TrimSpace(s.LastAssistant)
	if pre := strings.TrimSpace(s.PreGuardAssistant); len(pre) > len(last) {
		return pre
	}
	if last != "" {
		return last
	}
	return strings.TrimSpace(s.Tail)
}

// Snapshot copies the live agent state into a value the caller can
// inspect at leisure.
func (a *Agent) Snapshot() AgentSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	tail := strings.Join(lastN(a.transcript, 6), "\n")
	lines := make([]string, len(a.transcript))
	copy(lines, a.transcript)
	errStr := ""
	if a.lastErr != nil {
		errStr = a.lastErr.Error()
	}
	return AgentSnapshot{
		ID: a.ID, Task: a.Task, Dir: a.Dir,
		Status: a.status, Activity: a.activity,
		Started: a.Started, Finished: a.finished,
		Err: errStr, Tail: tail, Lines: lines,
		LastAssistant:     a.lastAssistant,
		PreGuardAssistant: a.preGuardAssistant,
		Model:             a.Model,
		Provider:          a.Provider,
		Persona:           a.Persona,
		Experience:        a.Experience,
		Substrate:         a.Substrate,
		Card:              a.Card,
		InboxPath:         a.InboxPath,
		EventLogPath:      a.EventLogPath,
		SessionPath:       a.SessionPath,
	}
}

// SnapshotAll returns snapshots of every agent in creation order,
// scoped to the active session when one is set.
//
// Scoping rules:
//   - activeSession == "": no filter, every agent is returned
//     (historical behaviour; used by tests and scripted callers).
//   - activeSession != "": include only agents whose SessionID
//     matches activeSession OR is empty. The empty-id pass-through
//     keeps pre-upgrade agents (their meta.json was written before
//     session_id existed) visible from any session so the user
//     doesn't lose access after the schema bump.
func (f *Swarm) SnapshotAll() []AgentSnapshot {
	agents := f.List()
	f.mu.Lock()
	active := f.activeSession
	f.mu.Unlock()

	out := make([]AgentSnapshot, 0, len(agents))
	for _, a := range agents {
		if active != "" && a.SessionID != "" && a.SessionID != active {
			continue
		}
		out = append(out, a.Snapshot())
	}
	// Sort by start time for a stable, deterministic listing.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// agentSink is the Sink the Swarm hands to each Runner.
type agentSink struct{ a *Agent }

func (s agentSink) Activity(msg string)     { s.a.setActivity(msg) }
func (s agentSink) Transcript(chunk string) { s.a.appendTranscript(chunk) }
func (s agentSink) Result(text string)      { s.a.setLastAssistant(text) }
func (s agentSink) GuardNudge()             { s.a.noteGuardNudge() }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return strings.Repeat(".", n)
	}
	return s[:n-3] + "..."
}

func lastN(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}
