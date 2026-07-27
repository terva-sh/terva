// Package workflow is the deterministic orchestration engine (workstream
// C of docs/plans/jsengine-code-execution-and-workflows.md): a JavaScript
// script — same grammar and shape as a Claude Code workflow — fans agents
// out over the swarm, holds intermediate results in script variables
// instead of any model's context, and journals every agent result so a
// run is resumable. The script decides what runs next; the swarm runs it;
// the journal remembers it.
package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/jsengine"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/workflow/runs"
)

// agentLifetimeCap is a runaway-loop backstop, far above any real run.
const agentLifetimeCap = 1000

// Outcome is what one spawned agent produced.
type Outcome struct {
	AgentID string
	// Result is the machine-read report: the schema-validated deliverable
	// for a schema-carrying spawn, else the final findings text
	// JSON-encoded as a string.
	Result  json.RawMessage
	CostUSD float64
	// Err, non-empty, means the agent failed its task (spawn error, death
	// before task end, or an unmet deliverable contract).
	Err string
}

// Handle is one live agent the runner awaits.
type Handle interface {
	ID() string
	// AwaitTask blocks until the agent's task-level turn ends (or the
	// agent dies, or ctx ends) and returns the outcome. The handle owns
	// reaping: the child is stopped before AwaitTask returns.
	AwaitTask(ctx context.Context) Outcome
}

// Engine is the slice of the dispatch substrate a workflow needs — the
// same shape RAATI's Engine takes, defined here so tests fake it and so
// the runner never grows a swarm dependency beyond spawn/await/stop.
type Engine interface {
	Spawn(ctx context.Context, req swarm.SpawnRequest) (Handle, error)
	// AllowBackend reports whether this host can drive the named worker
	// backend ("" — native — is always allowed). The CLI host allows only
	// native children; a workspace host consults the same gate the spawn
	// tool does.
	AllowBackend(name string) error
}

// Options configures one run.
type Options struct {
	// Args is the script's `args` global, verbatim.
	Args any
	// ResumeID re-opens an existing run's journal: completed agent()
	// calls with unchanged (prompt, opts) replay from it. Empty mints a
	// fresh run id.
	ResumeID string
	// Root is the workflows state dir (<tervaHome>/swarm/workflows).
	Root string
	// Concurrency caps simultaneously-running agents; <=0 derives
	// min(16, NumCPU-2, >=1). The cap lives HERE, not in the swarm — the
	// swarm's uncappedness is a feature for its other callers.
	Concurrency int
	// BudgetUSD, >0, is a hard spend ceiling for the run: once the summed
	// worker cost reaches it, further agent() calls throw. (Terva budgets
	// in dollars, not tokens — cost is the number the swarm already
	// carries per agent; a documented divergence from Claude Code.)
	BudgetUSD float64
	// Progress receives human-readable narration (log(), phase(), cache
	// hits, agent lifecycle). Nil discards.
	Progress func(string)
	// Timeout bounds the whole run; <=0 means no bound beyond ctx.
	Timeout time.Duration
	// CWD and ScriptPath are recorded, not used: they are what makes a run
	// identifiable afterwards, and what FindIncomplete matches a rerun against.
	CWD        string
	ScriptPath string
	// Now is a clock seam for tests; defaults to time.Now.
	Now func() time.Time
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Result is a completed run.
type Result struct {
	RunID        string
	Meta         Meta
	Value        any
	Agents       int
	CachedAgents int
	CostUSD      float64
	Elapsed      time.Duration
}

// prelude implements pipeline/parallel in pure JS (their semantics are
// promise plumbing, not host work) plus the budget facade. Semantics
// match the probed contract: parallel is a barrier whose failed thunks
// resolve to null; pipeline runs each item through all stages with NO
// barrier between stages, a throwing stage dropping that item to null.
const prelude = `
function parallel(thunks) {
	return Promise.all(thunks.map(t => Promise.resolve().then(t).catch(e => { __notefail(String(e)); return null })))
}
function pipeline(items, ...stages) {
	return Promise.all(items.map(async (item, i) => {
		let v = item
		for (const s of stages) {
			try { v = await s(v, item, i) } catch (e) { __notefail(String(e)); return null }
		}
		return v
	}))
}
const budget = {
	total: __budget_total,
	spent: () => __budget_spent(),
	remaining: () => (__budget_total === null ? Infinity : Math.max(0, __budget_total - __budget_spent())),
}
`

// Run executes script against eng. The script's return value (exported to
// plain Go values) is the workflow's result.
func Run(ctx context.Context, eng Engine, script []byte, opts Options) (res Result, err error) {
	start := time.Now()
	// Named returns so this defer stamps the Result the caller actually
	// receives — with unnamed returns it mutated a dead local and every
	// run reported 0s (caught by the first live smoke's summary line).
	defer func() { res.Elapsed = time.Since(start) }()

	src := deExport(string(script))
	meta, err := extractMeta("workflow.js", src)
	if err != nil {
		return res, err
	}
	res.Meta = meta
	if err := jsengine.CheckDeterminism(src); err != nil {
		return res, err
	}

	runID := opts.ResumeID
	if runID == "" {
		b := make([]byte, 6)
		if _, err := rand.Read(b); err != nil {
			return res, err
		}
		runID = "wf_" + hex.EncodeToString(b)
	}
	res.RunID = runID
	if opts.Root == "" {
		return res, fmt.Errorf("workflow: Options.Root is required")
	}
	j, err := runs.OpenJournal(fmt.Sprintf("%s/%s", opts.Root, runID))
	if err != nil {
		return res, err
	}
	defer j.Close()

	// The run record, opened now and closed in a defer, so a run that is
	// INTERRUPTED leaves the started half behind rather than nothing. That is
	// the case the record exists for: a failed run prints its resume hint on
	// the way out, an interrupted one dies before it can.
	rec := runs.Record{
		RunID:    runID,
		Name:     meta.Name,
		Started:  opts.now().UTC().Format(time.RFC3339),
		CWD:      opts.CWD,
		ScriptAt: opts.ScriptPath,
		Script:   string(script),
		PID:      os.Getpid(),
	}
	if opts.Args != nil {
		if b, merr := json.Marshal(opts.Args); merr == nil {
			rec.Args = b
		}
	}
	// A record that cannot be written is not a reason to refuse the run — the
	// work is still worth doing and the journal still protects it.
	rec.Heartbeat = rec.Started // alive as of launch, before the first tick
	_ = runs.WriteRecord(opts.Root, rec)

	// Registered FIRST so it runs LAST: defers are LIFO, and the closing record
	// must be the final write. The heartbeat stopper below is registered after
	// it and therefore runs before it.
	defer func() {
		rec.Ended = opts.now().UTC().Format(time.RFC3339)
		rec.Agents, rec.Cached, rec.CostUSD = res.Agents, res.CachedAgents, res.CostUSD
		if err != nil {
			rec.Err = err.Error()
		}
		_ = runs.WriteRecord(opts.Root, rec)
	}()

	// The heartbeat. A reader can now tell a run that is working from one whose
	// process died — the distinction the recorded PID could never give, because
	// a pid lies after reuse. A stopped ticker is what makes a crash legible:
	// the last stamp simply stops advancing, so no shutdown path has to
	// remember to mark anything, including the ones that never get to run
	// (SIGKILL, a panic, a lost machine).
	beatDone := make(chan struct{})
	beatStopped := make(chan struct{})
	go func() {
		defer close(beatStopped)
		t := time.NewTicker(runs.HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-beatDone:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				_ = runs.Beat(opts.Root, runID, opts.now())
			}
		}
	}()
	// Wait for the ticker to actually exit, not just signal it. Beat re-reads
	// the record and skips a finished run, but a tick that read BEFORE the
	// closing write could still land after it and drop the counts and the cost.
	// Waiting removes the window instead of narrowing it.
	defer func() {
		close(beatDone)
		<-beatStopped
	}()

	progress := opts.Progress
	if progress == nil {
		progress = func(string) {}
	}
	conc := opts.Concurrency
	if conc <= 0 {
		conc = min(16, max(1, runtime.NumCPU()-2))
	}
	sem := make(chan struct{}, conc)

	var mu sync.Mutex // guards the counters below across binding goroutines
	spentUSD := 0.0
	agents := 0
	cached := 0

	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	agentBinding := func(bctx context.Context, jsArgs []any) (any, error) {
		prompt, promptOK := first(jsArgs).(string)
		if !promptOK || prompt == "" {
			return nil, fmt.Errorf("agent(prompt, opts?) needs a prompt string")
		}
		var agentOpts map[string]any
		if len(jsArgs) > 1 && jsArgs[1] != nil {
			agentOpts, _ = jsArgs[1].(map[string]any)
			if agentOpts == nil {
				return nil, fmt.Errorf("agent opts must be an object")
			}
		}
		req, label, err := buildSpawnRequest(prompt, agentOpts)
		if err != nil {
			return nil, err
		}
		if err := eng.AllowBackend(req.Backend); err != nil {
			return nil, err
		}
		key, err := runs.AgentKey(prompt, agentOpts)
		if err != nil {
			return nil, err
		}
		if raw, ok := j.Lookup(key); ok {
			mu.Lock()
			cached++
			mu.Unlock()
			progress(fmt.Sprintf("agent %s: replayed from journal", label))
			return decodeResult(raw)
		}
		mu.Lock()
		if agents >= agentLifetimeCap {
			mu.Unlock()
			return nil, fmt.Errorf("agent lifetime cap reached (%d) — a runaway loop backstop", agentLifetimeCap)
		}
		if opts.BudgetUSD > 0 && spentUSD >= opts.BudgetUSD {
			mu.Unlock()
			return nil, fmt.Errorf("budget exhausted ($%.2f of $%.2f)", spentUSD, opts.BudgetUSD)
		}
		agents++
		mu.Unlock()

		select {
		case sem <- struct{}{}:
		case <-bctx.Done():
			return nil, bctx.Err()
		}
		defer func() { <-sem }()

		h, err := eng.Spawn(bctx, req)
		if err != nil {
			// Infra failures resolve to null (the probed contract: an
			// errored agent is null, never an exception from the fan-out).
			progress(fmt.Sprintf("agent %s: spawn failed: %v", label, err))
			return nil, nil
		}
		progress(fmt.Sprintf("agent %s: running (%s)", label, h.ID()))
		j.Started(key, h.ID(), label)
		out := h.AwaitTask(bctx)
		mu.Lock()
		spentUSD += out.CostUSD
		mu.Unlock()
		if out.Err != "" {
			progress(fmt.Sprintf("agent %s: failed: %s", label, out.Err))
			return nil, nil
		}
		if err := j.Result(key, out.AgentID, label, out.Result); err != nil {
			return nil, fmt.Errorf("journal write: %w", err)
		}
		progress(fmt.Sprintf("agent %s: done", label))
		return decodeResult(out.Result)
	}

	engineRes, err := jsengine.RunAsync(ctx, meta.Name+".js", src, jsengine.AsyncOptions{
		AsyncBindings: map[string]jsengine.AsyncBinding{"agent": agentBinding},
		Bindings: map[string]jsengine.RawBinding{
			"log":   func(a []any) (any, error) { progress(fmt.Sprint(first(a))); return nil, nil },
			"phase": func(a []any) (any, error) { progress(fmt.Sprintf("=== phase: %v", first(a))); return nil, nil },
			"__notefail": func(a []any) (any, error) {
				progress(fmt.Sprintf("dropped to null: %v", first(a)))
				return nil, nil
			},
			"__budget_spent": func([]any) (any, error) {
				mu.Lock()
				defer mu.Unlock()
				return spentUSD, nil
			},
		},
		Globals: map[string]any{
			"args":           opts.Args,
			"__budget_total": budgetTotal(opts.BudgetUSD),
		},
		Prelude:                prelude,
		WithholdNondeterminism: true,
	})
	mu.Lock()
	res.Agents, res.CachedAgents, res.CostUSD = agents, cached, spentUSD
	mu.Unlock()
	if err != nil {
		return res, err
	}
	res.Value = engineRes.Value
	return res, nil
}

// buildSpawnRequest maps agent() opts onto a SpawnRequest, rejecting
// unknown keys so a typo (or an unconverted Claude Code option) fails
// loudly instead of silently spawning a default agent.
func buildSpawnRequest(prompt string, opts map[string]any) (swarm.SpawnRequest, string, error) {
	req := swarm.SpawnRequest{Task: prompt}
	label := trunc(prompt, 48)
	for k, v := range opts {
		switch k {
		case "label":
			if s, ok := v.(string); ok && s != "" {
				label = s
				// Also to the spawn, so the agent's state dir and journal row
				// are named after the slice rather than after the shared
				// prompt preamble. Set only when the script actually gave a
				// label: the narration fallback below is a truncated prompt,
				// which is what the id would have been slugged from anyway.
				req.Label = s
			}
		case "phase":
			// Progress grouping only; participates in the cache key, not
			// the spawn.
		case "model":
			req.Model, _ = v.(string)
		case "provider":
			req.Provider, _ = v.(string)
		case "persona":
			req.Persona, _ = v.(string)
		case "backend":
			req.Backend, _ = v.(string)
		case "schema":
			raw, err := json.Marshal(v)
			if err != nil {
				return req, label, fmt.Errorf("agent schema not serializable: %w", err)
			}
			req.Schema = raw
		case "agentType", "effort", "isolation":
			return req, label, fmt.Errorf("agent option %q is Claude Code's, not terva's — convert it: agentType→persona, effort→model choice, isolation is the host's --swarm-worktrees setting rather than a per-agent opt", k)
		default:
			return req, label, fmt.Errorf("unknown agent option %q (known: label, phase, model, provider, persona, backend, schema)", k)
		}
	}
	return req, label, nil
}

// decodeResult turns a journaled result back into script values.
func decodeResult(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("journaled result does not parse: %w", err)
	}
	return v, nil
}

func budgetTotal(usd float64) any {
	if usd <= 0 {
		return nil
	}
	return usd
}

func first(a []any) any {
	if len(a) == 0 {
		return nil
	}
	return a[0]
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// SwarmEngine adapts the real swarm to Engine (the RAATI pattern).
type SwarmEngine struct {
	Swarm *swarm.Swarm
	// Backend gates foreign-backend spawns; nil allows native only.
	Backend func(name string) error
}

func (e SwarmEngine) AllowBackend(name string) error {
	if name == "" {
		return nil
	}
	if e.Backend == nil {
		return fmt.Errorf("backend %q: this workflow host runs native sub-agents only", name)
	}
	return e.Backend(name)
}

func (e SwarmEngine) Spawn(ctx context.Context, req swarm.SpawnRequest) (Handle, error) {
	a, err := e.Swarm.SpawnReq(ctx, req)
	if err != nil {
		return nil, err
	}
	// Install the turn watcher immediately, before the child can finish
	// (the RAATI installWatcher pattern — buffered so an early finish
	// still delivers).
	turnDone := make(chan string, 1)
	a.SetOnTurnEnd(func(step int, errMsg string) {
		select {
		case turnDone <- errMsg:
		default:
		}
	})
	return &swarmHandle{s: e.Swarm, a: a, turnDone: turnDone}, nil
}

type swarmHandle struct {
	s        *swarm.Swarm
	a        *swarm.Agent
	turnDone <-chan string
}

func (h *swarmHandle) ID() string { return h.a.ID }

func (h *swarmHandle) AwaitTask(ctx context.Context) Outcome {
	out := Outcome{AgentID: h.a.ID}
	agentGone := make(chan struct{})
	go func() { h.a.Wait(); close(agentGone) }()
	var turnErr string
	select {
	case turnErr = <-h.turnDone:
	case <-agentGone:
		// Died without a task_end.
		if err := h.a.Err(); err != nil {
			out.Err = err.Error()
		} else {
			out.Err = "agent exited before completing its task"
		}
	case <-ctx.Done():
		out.Err = ctx.Err().Error()
	}
	// Workflow agents are task-scoped, not daemons: reap the child now
	// that its task (or the run) is over, whatever the outcome.
	defer func() { _ = h.s.Stop(h.a.ID) }()
	if out.Err != "" {
		return out
	}
	snap := h.a.Snapshot()
	out.CostUSD = snap.CostUSD
	if turnErr != "" {
		out.Err = turnErr
		return out
	}
	if len(h.a.Schema) > 0 {
		if snap.DeliverableError != "" {
			out.Err = "deliverable contract not met: " + snap.DeliverableError
			return out
		}
		out.Result = snap.Deliverable
		return out
	}
	raw, err := json.Marshal(snap.Findings())
	if err != nil {
		out.Err = err.Error()
		return out
	}
	out.Result = raw
	return out
}
