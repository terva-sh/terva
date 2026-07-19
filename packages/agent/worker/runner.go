package worker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/deliverable"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/privfs"
)

// Runner drives one foreign-agent worker as a swarm.Runner.
//
// It is the bridge the two packages are deliberately kept from being: swarm is a
// supervisor and knows nothing about backends (Agent.Backend is an opaque label
// it persists and hands back, the same ignorance it keeps about git behind
// AcquireWorktree); worker knows nothing about supervision. This type is the one
// place that imports both, and it does exactly the translation neither will:
//
//	child stdout  --Backend.Translate-->  swarm.Event  -->  events.jsonl + Sink
//	inbox socket  --Backend.Steer-->      child stdin
//	                                      child stdout (verbatim) --> raw.jsonl
//
// The supervisor drives it through the SAME seams it drives a native child
// with: it writes follow-up turns and the shutdown request to the agent's inbox
// socket (Swarm.SendUserTurn / Stop), and this runner is the one listening on
// that socket — impersonating the native child so the supervisor never learns
// there is anything foreign here.
type Runner struct {
	agent   *swarm.Agent
	backend Backend

	// resolved is terva's own assembled state for this dispatch. The runner
	// COMPOSES a portable briefing from it and SCRUBS that briefing — it never
	// forwards it. Holding the Resolved rather than a pre-made Briefing keeps
	// compose→scrub→spawn atomic here, so the leak gate sits exactly where the
	// process is born and a leak fails the spawn instead of reaching the child.
	resolved build.Resolved

	// confirmer is the ORCHESTRATOR's approval seam. When a worker asks for
	// permission (Backend.RecognizeAsk), the runner routes the request here — the
	// dispatching session's human card — and replies to the worker with the
	// verdict. Nil means no approver is watching (a resumed worker whose session
	// is gone, or a host that wired none); the runner then denies asks cleanly so
	// the worker unwinds with a reason rather than hanging.
	confirmer core.Confirmer

	// stdin is the child's input pipe, guarded by stdinMu because two goroutines
	// write it: pumpStdin (the opening turn and inbox steers) and handleAsk (the
	// approve replies). One frame per lock keeps them from interleaving on the
	// wire. Set once in Run.
	stdin     io.WriteCloser
	stdinMu   sync.Mutex
	stdinShut bool
}

// NewRunner builds a Runner for agent a, driven by backend b, composing its
// briefing from resolved. confirmer is the orchestrator's approval seam for
// routing the worker's permission requests (nil to deny them). The host's
// Config.NewRunner returns this for any agent carrying a Backend; agents with no
// backend keep the native swarm.NewExecRunner.
func NewRunner(a *swarm.Agent, b Backend, resolved build.Resolved, confirmer core.Confirmer) *Runner {
	return &Runner{agent: a, backend: b, resolved: resolved, confirmer: confirmer}
}

// writeStdin writes one whole frame to the child's stdin under the lock, so a
// steer and an approve reply can never interleave their bytes. A no-op once
// stdin is shut (a late write after shutdown is expected, not an error).
func (r *Runner) writeStdin(frame []byte) {
	r.stdinMu.Lock()
	defer r.stdinMu.Unlock()
	if r.stdinShut {
		return
	}
	_, _ = r.stdin.Write(frame)
}

// closeStdin shuts the child's input (the graceful-drain signal), idempotently.
func (r *Runner) closeStdin() {
	r.stdinMu.Lock()
	defer r.stdinMu.Unlock()
	if !r.stdinShut {
		_ = r.stdin.Close()
		r.stdinShut = true
	}
}

// Run composes the briefing, refuses to spawn if it leaks, starts the child, and
// supervises it until it exits or ctx is cancelled. It blocks for the child's
// whole life, exactly as swarm.Runner requires.
func (r *Runner) Run(ctx context.Context, sink swarm.Sink) error {
	// Compose from terva's resolved state, then SCRUB what we are about to send.
	// The scrub is the gate, not a warning: a briefing that names a harness-local
	// tool or surface would make the worker confidently call something it does
	// not have, so a leak fails the spawn before there is a process to mislead.
	//
	// It applies ONLY to a config-opaque backend. A SelfAssembles backend IS terva
	// — it re-derives the same tools, surfaces, and policy from the same config,
	// so harness-local content is true for it, what crosses is flags + the task
	// (not brief.Text), and scrubbing what it legitimately shares would be wrong.
	brief := Compose(r.resolved, Task{Mission: r.agent.Task}, Workspace{Path: r.agent.Dir})
	// A schema-carrying spawn extends the reporting contract. A foreign
	// worker can't be handed a deliver_result tool, so the schema rides the
	// briefing prose (identical wording to the native addendum's fence
	// fallback) and the supervisor fence-extracts + validates the final
	// message (swarm.captureDeliverable).
	if len(r.agent.Schema) > 0 {
		brief.Reporting += "\n\n" + deliverable.Contract(r.agent.Schema)
	}
	// Resolve the worker's approval posture. Compose seeded it with the
	// dispatcher's (the inherited default); override it per the worker policy —
	// an explicit choice wins, else a leased worker runs autonomously (yolo in
	// its own worktree), else it keeps inheriting. See WorkerPosture.
	brief.Policy.Posture = WorkerPosture(r.agent.Approval, r.agent.Leased, brief.Policy.Posture)
	if !r.backend.SelfAssembles {
		if leaks := Scrub(brief.Text(), r.resolved); len(leaks) > 0 {
			return fmt.Errorf("worker %s: briefing leaked %d harness-local fragment(s) across the boundary — refusing to spawn; first: %s (%q)",
				r.backend.Name, len(leaks), leaks[0].Detail, leaks[0].Excerpt)
		}
	}

	cursor := ""
	if r.backend.Cursor != nil {
		cursor = r.backend.Cursor(r.agent.ID)
	}

	dispatch := Dispatch{
		Briefing:    brief,
		Dir:         r.agent.Dir,
		Cursor:      cursor,
		Resuming:    r.agent.Resuming,
		SessionPath: r.agent.SessionPath,
	}
	// A backend that gates through the MCP approval bridge needs a socket its
	// bridge can dial. Open and serve it here — the runner owns the Confirmer
	// seam — and hand its path to Command so the child's permission tool points
	// at it. Best-effort: if the socket cannot open, the worker still runs and
	// its bridge simply fails to dial, which fails CLOSED (deny) — the safe
	// direction. See serveApprovals.
	if r.backend.ApprovalSocket && r.agent.InboxPath != "" {
		apPath := approvalSocketPath(r.agent.InboxPath)
		if al, aerr := r.serveApprovals(ctx, apPath); aerr == nil {
			dispatch.ApprovalSocket = apPath
			defer al.Close()
		} else {
			sink.Transcript("worker: approval bridge socket unavailable (" + aerr.Error() + "); tool approvals will be denied")
		}
	}

	cmd, err := r.backend.Command(dispatch)
	if err != nil {
		return fmt.Errorf("worker %s: build command: %w", r.backend.Name, err)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	r.stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	logPath := r.agent.EventLogPath
	if logPath == "" {
		return fmt.Errorf("worker %s: agent missing event log path", r.backend.Name)
	}
	log, err := swarm.OpenEventLog(logPath)
	if err != nil {
		return err
	}
	defer log.Close()
	// raw.jsonl retains the vendor stream VERBATIM, next to the translated
	// events.jsonl. It is the safety net that makes translation silence safe:
	// an event this CLI version emits that terva does not yet model translates
	// to nothing, and the raw line is still here to re-translate after an
	// upgrade. Best-effort — if it cannot open we still run; losing forensics is
	// not worth losing the worker.
	raw := openRawLog(logPath)
	if raw != nil {
		defer raw.Close()
	}

	sink.Activity("starting")
	if err := cmd.Start(); err != nil {
		return err
	}

	// Kill the child if ctx is cancelled (Stop's backstop, StopAll). The child's
	// death EOFs the pipes below, so the drains unblock and Wait can be reached.
	// The backend built cmd with exec.Command, not CommandContext, so this
	// watcher is the runner's own binding of the process to the context.
	reaped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
		case <-reaped:
		}
	}()

	// stdout: the vendor's event stream. Every line is retained raw; every line
	// the backend can translate becomes swarm events through the shared ingest
	// path, so a foreign worker's dashboard behaves exactly like a native one's.
	stdoutDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		br := bufio.NewReader(stdout)
		// A failed turn's error must reach the task-level OnTurnEnd, which reads it
		// off the terminal task_end. Some backends (terva's rpc wire) report the
		// failure as a STANDALONE `error` event followed by a bare `done`→task_end,
		// so without carrying it across those two lines the task_end is empty and a
		// failed worker turn reads as success. pendingErr threads it through; it is
		// inert for backends whose task_end already carries the error (claude).
		var pendingErr string
		for {
			line, rerr := br.ReadBytes('\n')
			if len(line) > 0 {
				trimmed := strings.TrimRight(string(line), "\r\n")
				if trimmed != "" {
					appendRaw(raw, trimmed)
					for _, ev := range r.backend.Translate([]byte(trimmed)) {
						ev, pendingErr = carryTurnError(ev, pendingErr)
						// An approval request is intercepted, not mirrored to the
						// sink: it is a question, and the runner answers it (routing
						// to the orchestrator's human) rather than surfacing it as a
						// transcript line. Handling blocks here, which is safe — the
						// worker is itself blocked waiting for the reply, so it emits
						// no further stdout until we answer.
						if r.backend.RecognizeAsk != nil {
							if ask, ok := r.backend.RecognizeAsk(ev); ok {
								_ = log.Append(swarm.NewEvent(ev.Type, ev.Data))
								r.handleAsk(ctx, ask, sink)
								continue
							}
						}
						swarm.IngestEvent(swarm.NewEvent(ev.Type, ev.Data), log, sink, r.agent)
					}
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	// stderr: the child's diagnostic chatter. Mirrored to the transcript and the
	// durable log so a worker failing to start is diagnosable from the dashboard
	// — a bad flag or a missing credential lands here, not on stdout.
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		br := bufio.NewReader(stderr)
		for {
			line, rerr := br.ReadString('\n')
			if line != "" {
				txt := strings.TrimRight(line, "\r\n")
				sink.Transcript("stderr: " + txt)
				_ = log.Append(swarm.NewEvent("stderr", map[string]any{"text": txt}))
			}
			if rerr != nil {
				return
			}
		}
	}()

	// The inbox goroutine is the SOLE writer of the child's stdin: it delivers
	// the opening turn, then every supervisor follow-up, then closes stdin on
	// shutdown. One writer means the opening frame and a steer frame can never
	// interleave on the wire.
	inboxDone := make(chan struct{})
	listener, lerr := swarm.Listen(r.agent.InboxPath)
	go r.pumpStdin(brief, listener, lerr, sink, inboxDone)

	<-stdoutDone
	<-stderrDone
	// The child is gone (both pipes EOF'd). Close the listener so the inbox
	// goroutine, if it is still blocked waiting for a supervisor message, wakes
	// and returns instead of leaking.
	if listener != nil {
		_ = listener.Close()
	}
	<-inboxDone

	err = cmd.Wait()
	close(reaped)

	exit := 0
	if ee, ok := err.(*exec.ExitError); ok {
		exit = ee.ExitCode()
	}
	if err != nil && ctx.Err() != nil {
		_ = log.Append(swarm.NewEvent("agent_stopped", map[string]any{"reason": "cancelled"}))
		return ctx.Err()
	}
	if err != nil {
		_ = log.Append(swarm.NewEvent("agent_stopped", map[string]any{"reason": "exit", "code": exit, "error": err.Error()}))
		return err
	}
	_ = log.Append(swarm.NewEvent("agent_stopped", map[string]any{"reason": "exit", "code": 0}))
	sink.Activity("done")
	return nil
}

// pumpStdin sends the opening turn (unless this is a revival, whose session
// already holds it), then relays supervisor inbox messages onto the child's
// stdin until shutdown or the listener closes. It shares stdin with handleAsk
// (approve replies) through the mutex-guarded writeStdin/closeStdin, so a steer
// and an approve can never interleave their bytes.
func (r *Runner) pumpStdin(brief Briefing, listener *swarm.Listener, lerr error, sink swarm.Sink, done chan struct{}) {
	defer close(done)

	// A backend with no Steer cannot receive turns on stdin — it took its task
	// on argv (the native shape). Close stdin so a child that peeks at it gets a
	// clean EOF instead of blocking, and there is nothing to relay.
	if r.backend.Steer == nil {
		r.closeStdin()
		return
	}
	writeTurn := func(text string) {
		frame, err := r.backend.Steer(text)
		if err != nil {
			sink.Transcript("worker: could not encode a turn for " + r.backend.Name + ": " + err.Error())
			return
		}
		r.writeStdin(frame)
	}

	// The opening turn: the task. Only on a first run — a revival resumes a
	// session that already ran it, and re-sending would double the turn (the
	// same trap the native runner avoids by omitting the positional task on
	// resume). Its text is the backend's choice (Claude opens with the work
	// alone, having carried the identity in the system prompt).
	if !r.agent.Resuming {
		opening := brief.Text()
		if r.backend.Opening != nil {
			opening = r.backend.Opening(brief)
		}
		if strings.TrimSpace(opening) != "" {
			writeTurn(opening)
		}
	}

	if listener == nil {
		// The inbox socket failed to open. The worker still runs its opening
		// task; it just cannot be steered. Say so rather than failing silently.
		if lerr != nil {
			sink.Transcript("worker: steering unavailable (" + lerr.Error() + ")")
		}
		return
	}

	for line := range listener.Lines() {
		kind, text := swarm.ParseInboxLine(line)
		switch kind {
		case "user":
			writeTurn(text)
		case "shutdown":
			// Graceful drain: closing stdin is EOF to a stream-json child, which
			// finishes its in-flight turn, emits its terminal result, and exits.
			// The stdout translator turns that result into task_end.
			r.closeStdin()
			return
		case "cancel":
			// terva's cancel aborts the in-flight turn but keeps the daemon
			// alive. No foreign CLI in the table exposes a mid-turn interrupt
			// frame, so we cannot honor that without killing the worker outright
			// — which cancel is explicitly NOT. Surface it rather than pretend.
			sink.Transcript("worker: " + r.backend.Name + " cannot cancel a turn mid-flight; use stop to end the worker")
		}
	}
}

// decide routes one worker approval request to the orchestrator's Confirmer and
// returns the verdict. It is the shared terminus of every worker approval
// carrier — the rpc-native ask (handleAsk) and the MCP approval socket
// (handleApprovalConn) both call it — so a worker's approval lands on the same
// human card, labelled with the worker id so the human knows whose action it is,
// whichever backend asked.
//
// core.Confirmer.Confirm takes no context, so it runs off-goroutine and races
// the worker's teardown: a worker stopped before the human answers denies
// (leaving at most one bounded parked goroutine per unanswered ask) rather than
// hanging the caller. A nil Confirmer — a resumed worker whose session is gone —
// denies cleanly with a reason instead of waiting on an answer that never comes.
func (r *Runner) decide(ctx context.Context, tool, preview string) core.ConfirmDecision {
	if r.confirmer == nil {
		return core.ConfirmDecision{Allow: false, Reason: "no approver is available for this worker; denied"}
	}
	label := "worker " + r.agent.ID + ": " + preview
	dch := make(chan core.ConfirmDecision, 1)
	go func() { dch <- r.confirmer.Confirm(tool, label) }()
	select {
	case d := <-dch:
		return d
	case <-ctx.Done():
		return core.ConfirmDecision{Allow: false, Reason: "worker stopped before the approval was answered"}
	}
}

// handleAsk routes one worker approval request (rpc-native carrier) to the
// orchestrator's Confirmer and replies to the worker with the verdict. It runs
// on the stdout goroutine, so it MUST be cancellable — decide handles that: if
// the worker is stopped while parked on a human, ctx cancels and decide denies,
// so the stdout goroutine returns and Run does not deadlock on teardown.
func (r *Runner) handleAsk(ctx context.Context, ask Ask, sink swarm.Sink) {
	if r.backend.EncodeApprove == nil {
		return // a backend that recognises asks but can't reply is misconfigured
	}
	d := r.decide(ctx, ask.Tool, ask.Preview)
	frame, err := r.backend.EncodeApprove(ask.ID, d)
	if err != nil {
		sink.Transcript("worker: could not encode the approval reply: " + err.Error())
		return
	}
	r.writeStdin(frame)
	verb := "denied"
	if d.Allow {
		verb = "allowed"
	}
	sink.Transcript("approval: " + verb + " " + ask.Tool)
}

// openRawLog opens the verbatim vendor-stream sink beside the translated event
// log. It is transcript-grade data (full model output), so it is user-private
// like the event log itself. Returns nil on failure; callers treat nil as "no
// raw retention" and carry on.
func openRawLog(eventLogPath string) *os.File {
	rawPath := filepath.Join(filepath.Dir(eventLogPath), "raw.jsonl")
	if err := privfs.MkdirAll(filepath.Dir(rawPath)); err != nil {
		return nil
	}
	f, err := privfs.OpenFile(rawPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY)
	if err != nil {
		return nil
	}
	return f
}

func appendRaw(f *os.File, line string) {
	if f == nil {
		return
	}
	_, _ = f.WriteString(line + "\n")
}

// carryTurnError threads a failed turn's error onto its terminal task_end so the
// task-level OnTurnEnd — which reads the error off task_end — sees it. terva's rpc
// wire reports a turn failure as a STANDALONE `error` event and then a bare
// `done`→task_end (rpc.go emits them as two separate lines), so without carrying
// the message across those lines the task_end is empty and a failed worker turn is
// mistaken for success by the director, workflow.AwaitTask, and the swarm entry.
// It remembers the last error and folds it into the next task_end that carries
// none, then clears it; a backend whose task_end already embeds the error (claude,
// via `result`) never sets pending, so this is inert for it. Pure: (event, pending)
// in, (event, pending) out — the drain goroutine owns the single pending string.
// The `error` event itself is untouched and still reaches the transcript.
func carryTurnError(ev Event, pending string) (Event, string) {
	switch ev.Type {
	case "error":
		if m, _ := ev.Data["error"].(string); m != "" {
			pending = m
		}
	case "task_end":
		if pending != "" {
			if cur, _ := ev.Data["error"].(string); cur == "" {
				if ev.Data == nil {
					ev.Data = map[string]any{}
				}
				ev.Data["error"] = pending
			}
			pending = ""
		}
	}
	return ev, pending
}
