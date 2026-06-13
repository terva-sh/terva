// Package swarm implements terva's multi-agent supervisor.
//
// A Swarm manages a set of headless terva subprocesses ("agents")
// that share the host's working directory. The interactive TUI
// exposes the supervisor through the /swarm slash command and a
// dashboard dialog; non-TUI code can drive it directly through
// this package.
//
// Every agent runs with cwd == the parent terva's RepoRoot — the
// same files the user sees, the same files the main agent edits.
// There is no git worktree, no per-agent branch, no isolation. If
// you want parallel edits on a separate branch, use normal git
// tooling (a real worktree, a different terminal) yourself.
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

	// RepoRoot is the working directory every spawned agent runs
	// in — the same cwd the parent terva is using. There is no
	// per-agent isolation: agents edit the host's files directly.
	RepoRoot string

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
}

// Swarm supervises a set of Agents.
type Swarm struct {
	cfg Config

	mu     sync.Mutex
	agents map[string]*Agent
	order  []string // creation order for stable listing

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
	return &Swarm{
		cfg:    cfg,
		agents: map[string]*Agent{},
	}
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

// SpawnRequest configures a Spawn. Only Task is required; the rest
// are optional. Model + Provider, when set, get baked into the
// child argv as --model / --provider so the agent runs against the
// chosen model regardless of the parent's current selection.
type SpawnRequest struct {
	Task     string
	Model    string // optional override; child resolves default if empty
	Provider string // optional override; usually paired with Model
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
// Every spawned agent runs with cwd == cfg.RepoRoot — the same
// working directory as the host. No per-agent worktree, no branch,
// no isolation. The user explicitly opted out of the worktree flow.
func (f *Swarm) SpawnReq(ctx context.Context, req SpawnRequest) (*Agent, error) {
	task := strings.TrimSpace(req.Task)
	if task == "" {
		return nil, errors.New("swarm: empty task")
	}
	id := newAgentID(task, f.cfg.Now())
	dir := f.cfg.RepoRoot

	stateDir := f.agentStateDir(id)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("swarm state dir: %w", err)
	}
	logPath := filepath.Join(stateDir, "events.jsonl")
	sessionPath := filepath.Join(stateDir, "session.json")
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

	// Snapshot activeSession under the lock; we use it twice (struct
	// init below + persistence). Reading it without the lock would
	// race a concurrent SetActiveSession call.
	f.mu.Lock()
	sessionID := f.activeSession
	f.mu.Unlock()

	a := &Agent{
		ID:           id,
		Task:         task,
		Dir:          dir,
		Started:      f.cfg.Now(),
		Model:        strings.TrimSpace(req.Model),
		Provider:     strings.TrimSpace(req.Provider),
		SessionID:    sessionID,
		InboxPath:    inboxPath,
		EventLogPath: logPath,
		SessionPath:  sessionPath,
		inbox:        NewInbox(inboxPath),
		status:       StatusPending,
		activity:     "queued",
		done:         make(chan struct{}),
	}
	a.ctx, a.cancel = context.WithCancel(ctx)
	a.runner = f.cfg.NewRunner(a)

	f.mu.Lock()
	f.agents[id] = a
	f.order = append(f.order, id)
	f.mu.Unlock()

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
	}()
	return nil
}

// StopAll cancels every running agent. Used on shutdown.
func (f *Swarm) StopAll() {
	for _, a := range f.List() {
		_ = f.Stop(a.ID)
	}
}

// Remove tears down the per-agent state for a terminated agent. It
// is an error to remove an agent that's still running; call Stop
// first and wait for the status to settle. Detached agents
// (reloaded from disk) remove cleanly because they have no live
// runner racing for the same files.
//
// Agents share the host's working tree, so Remove never touches
// any source file — it only deletes the agent's state directory
// under <root>/agents/<id>/.
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

	// Model and Provider expose the per-agent overrides set at
	// Spawn time (empty when the agent inherits the child's default
	// resolution). The dashboard surfaces these so the user can
	// confirm which model an agent is running against.
	Model    string
	Provider string

	// Paths to the agent's durable state. Surface them in the
	// snapshot so the dashboard / /swarm open can read events.jsonl
	// or resume the session without going back through the Agent.
	InboxPath    string
	EventLogPath string
	SessionPath  string
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
		Model:        a.Model,
		Provider:     a.Provider,
		InboxPath:    a.InboxPath,
		EventLogPath: a.EventLogPath,
		SessionPath:  a.SessionPath,
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
