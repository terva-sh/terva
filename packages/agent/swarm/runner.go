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
					_ = log.Append(ev)
					applyEventToSink(ev, sink)
					// Fan the TASK-level turn_end up to any subscriber on
					// the supervised Agent. Daemons stay alive across many
					// turns, so Wait()-style hooks would never fire; a
					// per-task callback lets auto-swarm summarise as each
					// initial task completes. taskLevelTurnEnd filters out
					// the core agent's intermediate per-turn turn_ends so
					// the callback fires exactly once per ag.Prompt.
					if step, errMsg, ok := taskLevelTurnEnd(ev); ok && r.agent != nil {
						r.agent.mu.Lock()
						fn := r.agent.OnTurnEnd
						r.agent.mu.Unlock()
						if fn != nil {
							go fn(step, errMsg)
						}
					}
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
