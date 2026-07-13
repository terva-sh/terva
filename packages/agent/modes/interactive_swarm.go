package modes

// Auto-swarm supervision: tracking spawned sub-agents, batching their
// completions into one summary turn, and the system-prompt/tool
// toggles.

import (
	"strings"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/i18n"
)

// swarmWatchEntry is one tracked auto-swarm sub-agent. Filled in at
// spawn time and finalised in trackSwarmAgent's waiter goroutine.
type swarmWatchEntry struct {
	agent *swarm.Agent
	task  string
	done  bool
	err   string
}

// TrackSwarmAgent is the exported entry point used by the cli to
// hand a freshly-spawned auto-swarm agent off to the watcher.
func (i *Interactive) TrackSwarmAgent(a *swarm.Agent, task string) {
	i.trackSwarmAgent(a, task)
}

// trackSwarmAgent records a freshly-spawned auto-swarm agent and
// subscribes to its task-level turn_end events. Sub-agents are
// long-lived daemons that keep running on the inbox after the initial
// task, so we can't wait on agent.Wait() — it never returns until the
// whole daemon dies. Instead we mark each entry done on its first
// task-level turn_end (the initial ag.Prompt returning), and when every
// tracked entry has reported in, flush a single summary into the main
// chat. The runner only fires OnTurnEnd for task-level turn_ends, never
// for the core agent's intermediate per-turn turn_ends — otherwise a
// sub-agent would be declared "finished" after its very first tool call.
//
// Wired in from cli.go via SwarmSpawnTool.OnSpawned only when auto-
// swarm is enabled, so this is a no-op when the feature is off.
func (i *Interactive) trackSwarmAgent(a *swarm.Agent, task string) {
	if i == nil || a == nil {
		return
	}
	entry := &swarmWatchEntry{agent: a, task: task}
	i.swarmWatchMu.Lock()
	i.swarmWatch = append(i.swarmWatch, entry)
	i.swarmWatchMu.Unlock()

	a.SetOnTurnEnd(func(step int, errMsg string) {
		i.finalizeSwarmEntry(entry, errMsg)
	})

	// Fallback finaliser: a sub-agent that crashes, is stopped, or
	// otherwise exits before it ever emits a task-level turn_end would
	// never trip the OnTurnEnd path above, leaving entry.done == false
	// forever. Because the summary only flushes once *every* tracked
	// entry is done, one such zombie would wedge every future auto-
	// swarm summary in the session. swarm.run closes the agent's done
	// channel exactly when it reaches a terminal state (done / failed /
	// killed), so Wait() unblocks then; we finalise the entry from
	// there as a safety net. For a healthy long-lived daemon Wait()
	// simply blocks until the daemon dies, by which point OnTurnEnd has
	// already marked the entry done and finalizeSwarmEntry is a no-op.
	go func() {
		a.Wait()
		i.finalizeSwarmEntry(entry, "")
	}()
}

// finalizeSwarmEntry marks one tracked auto-swarm entry done and, when
// that completes the batch, flushes the summary. Idempotent: the first
// caller (task-level turn_end or the terminal-state waiter, whichever
// fires first) wins and later calls are no-ops. errMsg is recorded
// only on the winning call so a turn-level error survives into the
// summary; a terminal-state finalise passes "" and lets the snapshot's
// own Err carry any crash detail.
func (i *Interactive) finalizeSwarmEntry(entry *swarmWatchEntry, errMsg string) {
	i.swarmWatchMu.Lock()
	if entry.done {
		i.swarmWatchMu.Unlock()
		return
	}
	entry.done = true
	entry.err = errMsg
	allDone := true
	for _, e := range i.swarmWatch {
		if !e.done {
			allDone = false
			break
		}
	}
	var batch []*swarmWatchEntry
	if allDone {
		batch = i.swarmWatch
		i.swarmWatch = nil
	}
	i.swarmWatchMu.Unlock()
	if len(batch) == 0 {
		return
	}
	i.flushSwarmSummary(batch)
}

// flushSwarmSummary composes a synthetic user turn describing every
// sub-agent's outcome and injects it via SubmitOrQueue so the main
// agent picks it up at the next safe boundary. The summary is
// phrased as a system update ("Auto-swarm finished: ...") so the
// model treats it as observed state, not as a fresh user request.
func (i *Interactive) flushSwarmSummary(batch []*swarmWatchEntry) {
	if len(batch) == 0 {
		return
	}
	// Each line is its own i18n.P template so an operator translating
	// terva's prompts gets an all-<lang> summary \u2014 no English glue. P
	// does the Sprintf itself (we WriteString the result rather than
	// Fprintf a P format, which would be a non-constant format vet flags).
	var sb strings.Builder
	sb.WriteString(i18n.P("swarm.summary.header", "[auto-swarm update] %d sub-agent(s) finished:", len(batch)))
	sb.WriteString("\n\n")
	for idx, e := range batch {
		snap := e.agent.Snapshot()
		status := snap.RecapStatus()
		task := snap.Task
		if task == "" {
			task = e.task
		}
		sb.WriteString(i18n.P("swarm.summary.agent_line", "%d. agent %s \u2014 status: %s", idx+1, snap.ID, status))
		sb.WriteByte('\n')
		if snap.Persona != "" {
			sb.WriteString(i18n.P("swarm.summary.persona", "   persona: %s", snap.Persona))
			sb.WriteByte('\n')
		}
		sb.WriteString(i18n.P("swarm.summary.task", "   task: %s", truncateForSummary(task, 240)))
		sb.WriteByte('\n')
		if snap.Err != "" {
			sb.WriteString(i18n.P("swarm.summary.error", "   error: %s", truncateForSummary(snap.Err, 240)))
			sb.WriteByte('\n')
		} else if e.err != "" {
			sb.WriteString(i18n.P("swarm.summary.turn_error", "   turn error: %s", truncateForSummary(e.err, 240)))
			sb.WriteByte('\n')
		}
		// The sub-agent's own answer — its findings — not a tail of tool
		// output. Generous budget: for a review specialist this IS the
		// deliverable the coordinator must fold into its report.
		if findings := snap.Findings(); findings != "" {
			sb.WriteString(i18n.P("swarm.summary.findings", "   findings: %s", truncateForSummary(findings, 1500)))
			sb.WriteByte('\n')
		}
		// The retrieval handle for the findings the 1500-byte budget cut off:
		// the agent id doubles as a session_inspect id (S1 — coordinators
		// otherwise guess it is a project session id and hit "no such
		// session"). Only when the child actually has a transcript.
		if snap.SessionPath != "" {
			sb.WriteString(i18n.P("swarm.summary.inspect", "   full transcript: session_inspect with session_id %q (it is a sub-agent id, not a project session; expand an event's #n for its complete text)", snap.ID))
			sb.WriteByte('\n')
		}
		sb.WriteString("\n")
	}
	sb.WriteString(i18n.P("swarm.summary.instruction", "This is observed state from sub-agents you spawned, not a new user request. Briefly summarise the collective outcome for the user, referencing the agents by id. If any failed, suggest a follow-up; otherwise confirm completion. Do not spawn new sub-agents unless the user asks."))
	i.SubmitOrQueue(sb.String(), nil)
}

func truncateForSummary(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
