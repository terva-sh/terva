package swarm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/procenv"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/provider"
)

// execRunner spawns `terva --swarm-agent <inbox> --session <path>` in
// the host's working directory (Agent.Dir, which is always the parent
// terva's RepoRoot) and consumes its JSONL event stream on stdout.
//
// Why a long-lived daemon and not `terva --print`: the supervisor and
// the user expect agents to keep accepting follow-up prompts. A
// one-shot subprocess can't do that; this design gives each swarm
// agent a persistent session file plus an inbox socket the parent
// writes to, mirroring Claude Code's "Agents view" model.
//
// Events flow:
//
//	child stdout  -->  decoder  -->  EventLog (events.jsonl)
//	                              -->  Sink (Activity/Transcript)
//
// The on-disk log is the durable record. The Sink updates are an
// in-memory mirror so the dashboard doesn't have to tail the file
// for the parent's own agents. /swarm open in a separate terva would
// read the log directly.
type execRunner struct {
	agent *Agent

	// Command overrides the default `terva --swarm-agent ...`
	// invocation. Tests set this to a fake binary (or `go run`
	// against a tiny stub program) so the supervisor logic can be
	// tested without a real child. Production code leaves it nil.
	Command []string

	// SessionPath is the agent's session file. Empty means "defer
	// to r.agent.SessionPath", which Swarm.Spawn always populates
	// with <swarm-root>/agents/<id>/session.json. Tests that
	// hand-build an Agent without going through Spawn must set
	// one of the two; the runner refuses to invent a fallback
	// because the only plausible one (<Dir>/.terva/session.json)
	// would litter the user's repo — every agent's Dir points
	// at it directly.
	SessionPath string
}

// NewExecRunner builds the default native runner — the `terva --swarm-agent`
// child. Exported so a host that installs its OWN Config.NewRunner (to route
// some agents to a foreign backend by Agent.Backend) can still fall back to the
// native path for the agents that have no backend, which is every agent unless
// someone asked otherwise. Without this the host's NewRunner would have to
// reach for the unexported execRunner it cannot name.
func NewExecRunner(a *Agent) Runner { return &execRunner{agent: a} }

// IngestEvent is the post-parse bookkeeping every runner shares: append the
// event to the durable log, mirror it into the in-memory sink, and fan a
// TASK-level turn_end up to any OnTurnEnd subscriber on the agent.
//
// It exists as a seam because there are now TWO runners producing swarm events
// — the native execRunner, which parses its child's own JSONL, and a bridging
// runner in another package that TRANSLATES a foreign CLI's stream into these
// same events. Both must record identically (same durable log, same sink
// updates, same once-per-task callback), and the only way to guarantee that is
// one function. A foreign-backend runner cannot reach applyEventToSink or the
// agent's mutex from outside the package; this is the door it comes through.
//
// The OnTurnEnd fan-out filters the core agent's intermediate per-turn turn_ends
// (taskLevelTurnEnd), so the callback fires exactly once per ag.Prompt — the
// distinction auto-swarm relies on to summarise as each initial task completes,
// not after the sub-agent's first tool call.
func IngestEvent(ev Event, log *EventLog, sink Sink, a *Agent) {
	if log != nil {
		_ = log.Append(ev)
	}
	applyEventToSink(ev, sink)
	if a != nil {
		// Record the worker's cumulative spend as it flows by, so the snapshot
		// shows current cost. Both event shapes are cumulative — see setCost —
		// so this is last-wins, never a sum.
		if u, ok := costFromEvent(ev); ok {
			a.setUsage(u)
		}
	}
	if step, errMsg, ok := taskLevelTurnEnd(ev); ok && a != nil {
		// Derive the structured deliverable (schema spawns only) BEFORE the
		// OnTurnEnd fan-out, so a recap subscriber reading the snapshot in
		// its callback sees the validated document, not a race.
		a.captureDeliverable()
		a.mu.Lock()
		fn := a.OnTurnEnd
		a.mu.Unlock()
		if fn != nil {
			go fn(step, errMsg)
		}
	}
}

// costFromEvent pulls a worker's cumulative spend out of an event, honouring
// the two shapes the backends produce:
//
//   - a usage event's nested cumulative.cost_usd — the terva/native route. The
//     rpc wire and the native child both emit a core.WireEvent `usage` event
//     every turn, so cost lights up for any backend whose stream IS terva's
//     vocabulary, and updates LIVE as the run spends rather than only at the
//     terminal event.
//   - a task_end's top-level cost_usd — the claude route, where the foreign
//     result envelope's cumulative total_cost_usd is the only cost signal (its
//     stream emits no per-turn usage we can read), synthesised onto task_end by
//     the claude translator.
//
// Both numbers are cumulative session totals, not per-turn deltas.
func costFromEvent(ev Event) (provider.Usage, bool) {
	switch ev.Type {
	case "usage":
		// The `usage` event carries the child's whole cumulative, not just its
		// price. Reading only cost_usd was enough while the figure never left
		// the dashboard; it is not enough to book against a parent session,
		// whose totals are tokens AND dollars.
		if cum, ok := ev.Data["cumulative"].(map[string]any); ok {
			u := usageFromMap(cum)
			if u != (provider.Usage{}) {
				return u, true
			}
		}
	case "task_end":
		// A foreign backend may price its work without reporting tokens at
		// all. Cost alone is honest — zero tokens beside a real dollar figure
		// says "not reported", which is true.
		if cost, ok := ev.Data["cost_usd"].(float64); ok && cost > 0 {
			return provider.Usage{CostUSD: cost}, true
		}
	}
	return provider.Usage{}, false
}

// usageFromMap decodes the wire's cumulative object. JSON numbers arrive as
// float64 through the map, so the token counts are converted rather than
// asserted.
func usageFromMap(m map[string]any) provider.Usage {
	num := func(k string) float64 {
		v, _ := m[k].(float64)
		return v
	}
	return provider.Usage{
		InputTokens:      int(num("input_tokens")),
		OutputTokens:     int(num("output_tokens")),
		CacheReadTokens:  int(num("cache_read_tokens")),
		CacheWriteTokens: int(num("cache_write_tokens")),
		CostUSD:          num("cost_usd"),
	}
}

// swarmAgentArgsOpts captures every dynamic input to swarmAgentArgs
// so future per-agent overrides (e.g. tools, reasoning) can be added
// without churning the signature. The fields map 1:1 onto child CLI
// flags; empty values omit the flag entirely and let the child
// resolve a default the same way a normal `terva` invocation does.
type swarmAgentArgsOpts struct {
	Exe         string
	Dir         string
	SessionPath string
	InboxPath   string
	Task        string
	Model       string
	Provider    string
	Persona     string
	Experience  string
	Substrate   string
	Card        string
}

// defaultChildArgs builds the argv execRunner uses when its Command
// override is empty. Centralised so the spawn-vs-resume decision
// (whether to pass the original Task as a positional) lives in one
// place that tests can hit directly without going through Run's
// side effects.
//
// On Spawn (Resuming==false) we pass the task so the child's first
// turn runs immediately. On Resume (Resuming==true) we omit it: the
// child reopens the existing session file, loads the prior
// conversation, and just waits on the inbox for the next prompt.
// Re-firing the task on every resume produces a duplicate turn that
// collides with whatever the user types next, surfacing the
// "agent busy; send 'cancel' first" error between two assistant
// messages — which is exactly the bug this helper fixes.
func defaultChildArgs(exe string, a *Agent, sessionPath, inboxPath string) []string {
	task := a.Task
	if a.Resuming {
		task = ""
	}
	return swarmAgentArgs(swarmAgentArgsOpts{
		Exe:         exe,
		Dir:         a.Dir,
		SessionPath: sessionPath,
		InboxPath:   inboxPath,
		Task:        task,
		Model:       a.Model,
		Provider:    a.Provider,
		Persona:     a.Persona,
		Experience:  a.Experience,
		Substrate:   a.Substrate,
		Card:        a.Card,
	})
}

// swarmAgentArgs builds the argv used when execRunner.Command is
// empty. Pulled out so tests can lock in the flag set without
// actually spawning a subprocess.
func swarmAgentArgs(opts swarmAgentArgsOpts) []string {
	args := []string{
		opts.Exe,
		"--swarm-agent", opts.InboxPath,
		"--session", opts.SessionPath,
		"--cwd", opts.Dir,
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Provider != "" {
		args = append(args, "--provider", opts.Provider)
	}
	if opts.Persona != "" {
		// Flag, so it must precede the positional task below.
		args = append(args, "--persona", opts.Persona)
	}
	// Immersive boot-spec flags (all must precede the positional task). The
	// experience values mirror agent.ExperienceChat/Play; the swarm package
	// stays free of an import cycle by matching the wire strings directly.
	switch opts.Experience {
	case "chat":
		args = append(args, "--chat")
	case "play":
		args = append(args, "--play")
	}
	if opts.Substrate != "" {
		args = append(args, "--substrate", opts.Substrate)
	}
	if opts.Card != "" {
		args = append(args, "--card", opts.Card)
	}
	if opts.Task != "" {
		// First task is positional so the child treats it as the
		// initial user turn; subsequent turns arrive via the inbox.
		args = append(args, opts.Task)
	}
	return args
}

func (r *execRunner) Run(ctx context.Context, sink Sink) error {
	// SessionPath resolution order:
	//   1. explicit r.SessionPath set by the test / caller
	//   2. r.agent.SessionPath baked in by Swarm.Spawn — the
	//      production path. Always lives under
	//      <swarm-root>/agents/<id>/session.json so the per-
	//      agent state is entirely outside the working tree.
	//      Crucial because Agent.Dir points at the user's repo;
	//      any .terva/ scratch directory under Dir would litter
	//      their source tree.
	//
	// There is no third fallback. If neither path is set we
	// refuse to start instead of inventing a directory; that
	// way a misconfigured caller fails loudly the first time
	// instead of silently dumping session data into someone's
	// repo.
	sessionPath := r.SessionPath
	if sessionPath == "" {
		sessionPath = r.agent.SessionPath
	}
	if sessionPath == "" {
		return fmt.Errorf("swarm: agent missing session path (set SpawnRequest via Swarm.SpawnReq, or hand-build Agent with SessionPath populated)")
	}
	if err := privfs.MkdirAll(filepath.Dir(sessionPath)); err != nil {
		return fmt.Errorf("session dir: %w", err)
	}

	inboxPath := r.agent.InboxPath
	logPath := r.agent.EventLogPath
	if logPath == "" {
		return fmt.Errorf("swarm: agent missing event log path")
	}
	log, err := OpenEventLog(logPath)
	if err != nil {
		return err
	}
	defer log.Close()

	args := r.Command
	if len(args) == 0 {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate self: %w", err)
		}
		args = defaultChildArgs(exe, r.agent, sessionPath, inboxPath)
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = r.agent.Dir
	// Sanitized base env: swarm children are usually terva itself,
	// but custom child argv is possible and either way loader
	// injection vars stop here (see procenv).
	cmd.Env = append(procenv.Inherited(),
		"TERVA_SWARM_AGENT_ID="+r.agent.ID,
		"TERVA_SWARM_EVENT_LOG="+logPath,
	)
	// The structured-deliverable contract rides the env, not argv: a schema
	// is a multi-line JSON blob that has no business being a positional or
	// a flag value in `ps` output. The child bootstrap reads it back and
	// wires the deliver_result tool + contract addendum from it.
	if len(r.agent.Schema) > 0 {
		cmd.Env = append(cmd.Env, "TERVA_SWARM_DELIVERABLE_SCHEMA="+string(r.agent.Schema))
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	// "spawning" is briefly shown until the first event arrives;
	// the child's "spawned" lifecycle event then overwrites it.
	sink.Activity("starting")
	if err := cmd.Start(); err != nil {
		return err
	}

	// stdout: parsed as JSONL. Every well-formed event is appended
	// to the durable log AND forwarded to the in-memory sink so the
	// dashboard updates without having to tail the file. Malformed
	// lines are surfaced as plain transcript so they don't vanish.
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		dec := bufio.NewReader(stdout)
		for {
			line, err := dec.ReadBytes('\n')
			if len(line) > 0 {
				trimmed := strings.TrimRight(string(line), "\r\n")
				if trimmed == "" {
					goto next
				}
				if ev, ok := parseEventLine(trimmed); ok {
					// The shared post-parse bookkeeping — durable log, in-memory
					// sink, and the task-level OnTurnEnd fan-out. A bridging
					// runner in another package (one translating a FOREIGN CLI's
					// stream into swarm events) calls the same helper, so the two
					// paths cannot drift.
					IngestEvent(ev, log, sink, r.agent)
				} else {
					// Non-JSON output. Keep it as transcript so an
					// accidental fmt.Println in the child still
					// shows up in the dashboard, and log a
					// lifecycle event so the durable record stays
					// in sync.
					sink.Transcript(trimmed)
					_ = log.Append(NewEvent("stdout", map[string]any{"text": trimmed}))
				}
			}
		next:
			if err != nil {
				return
			}
		}
	}()

	// stderr: lifecycle/error chatter from the child. Every line
	// is mirrored as a stderr event in the durable log AND surfaced
	// in the transcript so users can diagnose a failing agent
	// without leaving the dashboard.
	go func() {
		defer func() { done <- struct{}{} }()
		br := bufio.NewReader(stderr)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				txt := strings.TrimRight(line, "\r\n")
				sink.Transcript("stderr: " + txt)
				_ = log.Append(NewEvent("stderr", map[string]any{"text": txt}))
			}
			if err != nil {
				return
			}
		}
	}()

	<-done
	<-done

	err = cmd.Wait()
	exit := 0
	if ee, ok := err.(*exec.ExitError); ok {
		exit = ee.ExitCode()
	}
	if err != nil && ctx.Err() != nil {
		_ = log.Append(NewEvent("agent_stopped", map[string]any{"reason": "cancelled"}))
		return ctx.Err()
	}
	if err != nil {
		_ = log.Append(NewEvent("agent_stopped", map[string]any{"reason": "exit", "code": exit, "error": err.Error()}))
		return err
	}
	_ = log.Append(NewEvent("agent_stopped", map[string]any{"reason": "exit", "code": 0}))
	sink.Activity("done")
	return nil
}

// taskLevelTurnEnd reports whether ev marks a swarm task completing —
// one whole ag.Prompt round returning — as opposed to the core
// agent's per-turn turn_end events fired after every internal turn of
// its tool-calling loop. The distinction matters: treating a per-turn
// turn_end as task completion made the auto-swarm watcher declare a
// sub-agent "finished" right after its first tool call, flushing a
// premature summary and dropping the watch before the real completion.
//
// The child emits a dedicated "task_end" event (step + error) for
// this. We also still recognise the legacy form — a "turn_end"
// carrying a "step" field — so replaying an old events.jsonl written
// before task_end existed keeps working. The core agent's per-turn
// turn_ends carry "stop" instead of "step", so they're never mistaken
// for task completion.
//
// Returns the step and error string when ok is true.
func taskLevelTurnEnd(ev Event) (step int, errMsg string, ok bool) {
	switch ev.Type {
	case "task_end":
		// Current form: explicit, unambiguous task completion.
	case "turn_end":
		// Legacy form: distinguished from a core per-turn turn_end
		// only by the presence of "step".
		if _, has := ev.Data["step"]; !has {
			return 0, "", false
		}
	default:
		return 0, "", false
	}
	s, _ := ev.Data["step"].(float64)
	e, _ := ev.Data["error"].(string)
	return int(s), e, true
}

// parseEventLine attempts to decode one JSONL line as an Event.
// Returns ok=false for non-JSON or JSON without a "type" field.
func parseEventLine(line string) (Event, bool) {
	if len(line) == 0 || line[0] != '{' {
		return Event{}, false
	}
	var ev Event
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return Event{}, false
	}
	if ev.Type == "" {
		return Event{}, false
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	return ev, true
}

// applyEventToSink translates an Event into Sink updates. Only a
// few event types are interpreted; the rest still land in the
// durable log via the caller.
func applyEventToSink(ev Event, sink Sink) {
	// eventMessageContent extracts the content blocks of a
	// user/assistant message event. Canonical shape (core.WireEvent)
	// nests them under "message"; durable logs written before the
	// serializer unification carried them flat under "content", so
	// keep reading both — old events.jsonl files replay forever.
	eventMessageContent := func(data map[string]any) ([]any, bool) {
		if m, ok := data["message"].(map[string]any); ok {
			c, ok := m["content"].([]any)
			return c, ok
		}
		c, ok := data["content"].([]any)
		return c, ok
	}
	switch ev.Type {
	case "assistant_message":
		var parts []string
		if c, ok := eventMessageContent(ev.Data); ok {
			for _, blk := range c {
				m, _ := blk.(map[string]any)
				if t, _ := m["type"].(string); t == "text" {
					if txt, _ := m["text"].(string); txt != "" {
						sink.Transcript(txt)
						parts = append(parts, txt)
					}
				}
			}
		}
		// Record the whole message as this agent's latest answer, so the
		// auto-swarm recap can surface real findings rather than a tail of
		// interleaved tool output. Skip empty (tool-only) turns.
		if len(parts) > 0 {
			sink.Result(strings.Join(parts, "\n"))
		}
		sink.Activity("idle")
	case "user_message":
		if c, ok := eventMessageContent(ev.Data); ok {
			for _, blk := range c {
				m, _ := blk.(map[string]any)
				if t, _ := m["type"].(string); t == "text" {
					if txt, _ := m["text"].(string); txt != "" {
						sink.Transcript("user: " + txt)
						// The finalize guard re-prompting the child: the
						// answer it already gave is its deliverable — pin
						// it so a housekeeping reply can't clobber the
						// recap findings.
						if txt == OpenWorkGateMessage {
							sink.GuardNudge()
						}
					}
				}
			}
		}
	case "turn_start":
		sink.Activity("thinking")
	case "tool_call":
		if name, _ := ev.Data["name"].(string); name != "" {
			sink.Activity("tool: " + truncate(name, 60))
		}
	case "tool_result":
		sink.Activity("idle")
	case "turn_end", "task_end":
		sink.Activity("idle")
	case "agent_ready":
		// This event also carries "backend" + the probed CLI "version" in
		// ev.Data — free from the child's init, no extra probe. We only mark
		// idle today; the version reaches nowhere but the durable event log.
		// Capture it onto the Agent/snapshot if a dashboard tile ever wants
		// "backend vX.Y.Z" — see external-agent-workers.md, "Named, not built —
		// the snapshot's other worker fields".
		sink.Activity("idle")
	case "agent_stopped":
		// terminal status is decided by Swarm.run from the runner's
		// return value, not from this event. Don't overwrite the
		// activity here.
	case "error":
		// Canonical key is "error"; "message" appears in durable logs
		// written before the serializer unification.
		msg, _ := ev.Data["error"].(string)
		if msg == "" {
			msg, _ = ev.Data["message"].(string)
		}
		if msg != "" {
			sink.Transcript("error: " + msg)
		}
	}
}

// RunnerFunc adapts a plain function into a Runner. Useful for tests
// and for callers who don't need their own type.
type RunnerFunc func(ctx context.Context, sink Sink) error

func (f RunnerFunc) Run(ctx context.Context, sink Sink) error { return f(ctx, sink) }
