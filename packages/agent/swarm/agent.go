package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/provider"
)

// Agent is one supervised task. Public fields are immutable after
// Spawn; mutable state (status, activity, transcript) lives behind
// the embedded mutex.
type Agent struct {
	ID   string
	Task string
	// Dir is the agent's working directory. By default it's the host's
	// RepoRoot (agents share its cwd); when the swarm was configured
	// with an AcquireWorktree hook it's the leased per-agent directory.
	Dir string
	// Leased is true when AcquireWorktree granted this agent its OWN
	// directory (a dedicated worktree), false when it shares the host's
	// RepoRoot. A worker backend reads it to decide its default approval
	// posture: autonomous (yolo) in its own lease, but never silently yolo
	// in the shared host cwd — there it inherits the dispatcher's posture.
	// A lease is a git worktree, NOT a security sandbox, so this gates
	// AUTONOMY, not privilege. Persisted in meta.json so Resume keeps it.
	Leased  bool
	Started time.Time

	// Model and Provider, when non-empty, override the child
	// subprocess's model resolution. Empty means "inherit whatever
	// the child resolves on its own from config / env / flags" —
	// the historical behaviour. Persisted in meta.json so Resume
	// keeps using the same model across terva restarts.
	Model    string
	Provider string

	// Persona, when non-empty, is baked into the child's --persona flag so
	// the sub-agent boots as that persona. Persisted in meta.json so Resume
	// keeps the same identity across terva restarts. Empty = host default.
	Persona string

	// Experience ("chat"/"play"), Substrate (an opaque scheme-qualified ref,
	// reserved), and Card (a card path) are the immersive boot-spec fields:
	// baked into the child's --chat/--play, --substrate, and --card flags so a
	// dispatched actor boots embodied instead of as the default coding agent.
	// Persisted in meta.json so Resume keeps the same boot across restarts. All
	// empty = a plain coding sub-agent (the historical behaviour). See
	// docs/proposals/agent-dispatch.md.
	Experience string
	Substrate  string
	Card       string

	// Backend names the worker backend that drives this agent — "claude" for a
	// Claude Code child, "terva" for a terva one. EMPTY IS THE DEFAULT AND MEANS
	// A NATIVE TERVA SWARM AGENT: the historical `terva --swarm-agent` child,
	// unchanged, and no external process of any other kind.
	//
	// The swarm does not know what a backend IS. It carries the label, persists
	// it, and hands it to Config.NewRunner, which is the host's business — the
	// same deliberate ignorance the swarm keeps about worktrees, where
	// AcquireWorktree is an opaque hook and the swarm knows nothing about git.
	// Keeping it that way is what stops a vendor's CLI quirks from leaking into
	// the supervisor.
	//
	// Persisted in meta.json: an agent revived after a terva restart must come
	// back on the SAME backend, or its resume cursor and its event stream would
	// be handed to a runner that cannot read them.
	Backend string

	// Approval, when non-empty, is an EXPLICIT per-worker approval posture that
	// overrides the default resolution — it always wins, whether the agent is
	// leased or not. Empty means "resolve by policy": yolo in a lease, else the
	// dispatcher's posture (see the worker runner's posture resolution). The
	// value is a posture string ("yolo" | "ask" | "workspace" | "plan"); the
	// swarm carries it opaquely and the backend interprets it. Persisted in
	// meta.json so a revived worker keeps the operator's choice.
	Approval string

	// Schema, when non-empty, is the JSON schema the agent's deliverable
	// must match (the structured-deliverable contract): a native child gets
	// a deliver_result tool bound to it, a foreign worker gets it rendered
	// into its briefing, and the supervisor validates whichever route the
	// report arrives by (see captureDeliverable). Carried opaquely like
	// Backend — the swarm never interprets it — and persisted in meta.json
	// so a revived agent keeps its contract. Empty = no contract (the
	// historical free-text report).
	Schema json.RawMessage

	// SessionID, when non-empty, scopes the agent to a particular
	// host terva session: the dashboard only surfaces agents whose
	// SessionID matches the active session. Empty means "unscoped"
	// (legacy meta files from before the field existed, or agents
	// spawned without a session context such as in tests). Set at
	// Spawn time from Swarm.activeSession and persisted in
	// meta.json so the scope survives restarts.
	SessionID string

	// Resuming is true when this Agent struct was built by Resume
	// rather than Spawn. The runner consults it to decide whether
	// to pass the original Task as a positional argv to the child:
	// on first Spawn we want the child to run the task immediately;
	// on Resume the task was already run last time and replaying it
	// would produce a second turn that collides ("agent busy; send
	// 'cancel' first") with whatever the user types next. Not
	// persisted — every Resume sets it explicitly.
	Resuming bool

	// InboxPath is the unix socket the child agent listens on for
	// follow-up prompts and control messages. The supervisor opens
	// an Inbox at this path; the child opens a Listener.
	InboxPath string

	// EventLogPath is the durable JSONL event log for this agent.
	// The runner appends every well-formed event from the child
	// (plus lifecycle events of its own) here. /swarm open in any
	// terva process reads from this file to replay the full history.
	EventLogPath string

	// SessionPath is the child's persistent session file. Surfaced
	// so the dashboard / /swarm open can resume the agent later.
	SessionPath string

	// inbox is the supervisor-side socket handle. Populated by
	// Swarm.Spawn so SendInput can route messages without each
	// caller redialing.
	inbox *Inbox

	mu         sync.Mutex
	status     Status
	activity   string
	transcript []string
	finished   time.Time
	lastErr    error

	// lastAssistant is the text of the most recent assistant_message
	// the child emitted — i.e. the sub-agent's latest answer, which for
	// a task-scoped dispatch (e.g. a review specialist) is its findings.
	// Kept distinct from the line-oriented transcript so the auto-swarm
	// recap can surface the actual answer instead of a truncated tail of
	// interleaved tool/test output. Set from applyEventToSink via the
	// Sink.Result seam; guarded by mu.
	lastAssistant string

	// preGuardAssistant snapshots lastAssistant at the moment the
	// finalize guard re-prompted the child (see Sink.GuardNudge). The
	// child had just tried to finish, so this is its intended
	// deliverable; its literal answer to the nudge is often task
	// housekeeping that would otherwise clobber the findings in the
	// recap. Guarded by mu.
	preGuardAssistant string

	// deliverable is the schema-validated structured report captured at the
	// last task-level turn_end (nil when validation failed or no turn has
	// ended); deliverableErr carries the extraction/validation error when
	// the contract was not met — ABSENT with a reason. Only meaningful when
	// Schema is set. Guarded by mu; written by captureDeliverable.
	deliverable    json.RawMessage
	deliverableErr string

	// costUSD is the worker's cumulative spend so far — the CUMULATIVE
	// session total, not a per-turn delta, so it is SET to the latest value
	// (never summed). It arrives via costFromEvent from whichever shape the
	// backend produces: a terva/native worker's per-turn `usage` event
	// (cumulative.cost_usd, so this climbs LIVE as the run spends) or a claude
	// worker's synthesised task_end (cost_usd off result.total_cost_usd, which
	// already sums prior turns). Zero until the first event that carries a
	// cost. In-memory like the transcript; the durable record is events.jsonl,
	// which replays those same events through IngestEvent. Guarded by mu.
	costUSD float64

	// usage is the same figure with its token counts, when the backend
	// reported them. Kept beside costUSD rather than replacing it because a
	// foreign backend may price its work without reporting tokens, and a
	// dollar figure with zero tokens beside it is honest — it says "not
	// reported" rather than "none". Guarded by mu.
	usage provider.Usage

	// OnTurnEnd, if set, fires once per TASK-level turn_end the runner
	// observes from the child daemon — i.e. once each time the child's
	// ag.Prompt returns, not after every internal turn of its
	// tool-calling loop (the runner filters those out). Used by
	// auto-swarm watchers to detect that a sub-agent's first (or n-th)
	// task has finished without waiting for the long-lived daemon
	// itself to exit — sub-agents keep running on the inbox even after
	// the initial task completes, so Wait() never unblocks for them.
	OnTurnEnd func(step int, errMsg string)

	ctx    context.Context
	cancel context.CancelFunc
	runner Runner

	// done closes when the run goroutine finalises the agent's
	// status (done / failed / killed). Wait blocks on this so
	// callers don't have to poll.
	done chan struct{}

	// releaseWorktree, when non-nil, releases the per-agent working
	// directory leased at Spawn from Config.AcquireWorktree. It must
	// run exactly once across every terminal path (done/failed/killed,
	// Stop, StopAll, detached cleanup) — a leaked lease is an orphaned
	// worktree. finish() enforces the at-most-once contract by clearing
	// the field under the mutex before invoking it, so concurrent or
	// repeated terminal transitions can never double-release. Guarded by
	// mu (set once at Spawn, read+cleared in finish).
	releaseWorktree func()
}

// finish runs the agent's worktree-release hook exactly once, no matter
// how many terminal paths reach it (the run goroutine's done/failed/
// killed finalisation, Stop, StopAll, or Remove on a detached agent).
// It snapshots-and-clears releaseWorktree under the lock so a second
// caller sees nil and does nothing, then invokes the hook outside the
// lock (the host's release may do real I/O and must not block other
// agent state reads). Safe to call on an agent that never leased a
// worktree — the nil hook is simply a no-op.
func (a *Agent) finish() {
	a.mu.Lock()
	rel := a.releaseWorktree
	a.releaseWorktree = nil
	a.mu.Unlock()
	if rel != nil {
		rel()
	}
}

// Inbox exposes the supervisor-side socket handle. Returns nil for
// agents without inboxes (e.g. tests using a custom Runner that
// doesn't speak the daemon protocol).
func (a *Agent) Inbox() *Inbox { return a.inbox }

// Status returns the current high-level status. Cheap; safe from any goroutine.
func (a *Agent) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

// Activity returns the current one-line activity string.
func (a *Agent) Activity() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.activity
}

// Transcript returns a copy of the running transcript.
func (a *Agent) Transcript() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.transcript))
	copy(out, a.transcript)
	return out
}

// Err returns the runner's terminal error, if any.
func (a *Agent) Err() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastErr
}

// Wait blocks until the agent reaches a terminal state. Used by tests
// and by /swarm wait <id>.
func (a *Agent) Wait() { <-a.done }

// SetOnTurnEnd installs (or clears, with nil) the per-turn callback
// fired from the runner when the child daemon emits a turn_end
// event. Safe to call from any goroutine: the runner reads the
// callback under the same mutex.
func (a *Agent) SetOnTurnEnd(fn func(step int, errMsg string)) {
	a.mu.Lock()
	a.OnTurnEnd = fn
	a.mu.Unlock()
}

func (a *Agent) setStatus(s Status) {
	a.mu.Lock()
	a.status = s
	a.mu.Unlock()
}

func (a *Agent) setActivity(msg string) {
	a.mu.Lock()
	a.activity = strings.TrimSpace(msg)
	a.mu.Unlock()
}

// setLastAssistant records the child's latest assistant answer. A
// blank message is ignored so a stray empty assistant turn never
// clobbers real findings.
func (a *Agent) setLastAssistant(txt string) {
	txt = strings.TrimSpace(txt)
	if txt == "" {
		return
	}
	a.mu.Lock()
	a.lastAssistant = txt
	a.mu.Unlock()
}

// setUsage records the worker's cumulative usage. The value is the vendor's
// running session total (not a per-turn delta), so it REPLACES the stored
// figure rather than adding to it. A zero value is ignored, so an event with no
// usage never wipes a real one.
//
// Cumulative-not-delta is why a parent booking this must diff against what it
// last saw: a child emits one of these per turn, and adding them would count
// the same spend once per event.
func (a *Agent) setUsage(u provider.Usage) {
	if u == (provider.Usage{}) {
		return
	}
	a.mu.Lock()
	a.usage = u
	a.costUSD = u.CostUSD
	a.mu.Unlock()
}

// noteGuardNudge snapshots the child's current answer when the finalize
// guard re-prompts it. Keeps the newest pre-guard answer if the guard
// somehow fires more than once; ignores the no-answer-yet case.
func (a *Agent) noteGuardNudge() {
	a.mu.Lock()
	if a.lastAssistant != "" {
		a.preGuardAssistant = a.lastAssistant
	}
	a.mu.Unlock()
}

func (a *Agent) appendTranscript(chunk string) {
	chunk = strings.TrimRight(chunk, "\n")
	if chunk == "" {
		return
	}
	a.mu.Lock()
	for _, line := range strings.Split(chunk, "\n") {
		a.transcript = append(a.transcript, line)
	}
	// Bound the transcript so dashboards don't hold gigabytes for
	// long-running agents. 2000 lines is plenty for inspection;
	// the durable record lives in the agent's session file.
	const cap = 2000
	if len(a.transcript) > cap {
		a.transcript = a.transcript[len(a.transcript)-cap:]
	}
	a.mu.Unlock()
}

// newAgentID returns a short, readable identifier of the form
// "<slug>-<nano>". The slug is derived from the task text so dashboards
// stay readable; the nano suffix is only entropy, NOT a uniqueness
// guarantee — UnixNano()%1e6 is the sub-millisecond offset, which
// repeats every millisecond, so two spawns CAN mint the same id.
// Uniqueness is claimAgentID's job (it re-mints on collision under the
// swarm lock); nothing else may use this raw value as an identity.
func newAgentID(task string, now time.Time) string {
	slug := taskSlug(task)
	return fmt.Sprintf("%s-%d", slug, now.UnixNano()%1_000_000)
}

func taskSlug(task string) string {
	task = strings.ToLower(task)
	var b strings.Builder
	dash := false
	for _, r := range task {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= 24 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "agent"
	}
	return out
}
