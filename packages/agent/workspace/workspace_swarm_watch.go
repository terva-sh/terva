package workspace

import (
	"strings"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/i18n"
)

// The daemon-side auto-swarm recap: the carrier twin of the legacy TUI's
// swarmWatch (packages/agent/modes/interactive_swarm.go). swarm_spawn is
// fire-and-forget — the coordinator moves on — so when every sub-agent it
// spawned finishes, the host injects a single [auto-swarm update] recap as a
// queued turn so the coordinator can synthesize their outcomes. Without this a
// coordinator on the carrier never learns its sub-agents finished (it was only
// ever wired on the legacy driver, via SwarmSpawnTool.OnSpawned).

// swarmWaitGateMessage re-prompts a coordinator that tried to finish while its
// spawned sub-agents are still running (see the ContinueOnStop guard). Injected
// once per batch — long enough to stop it racing off; the queued [auto-swarm
// update] recap does the rest.
const swarmWaitGateMessage = "You indicated you're finishing, but sub-agents you spawned are still running. Do NOT write your final answer yet — when they finish you'll receive an [auto-swarm update] with each one's outcome, and you should fold their findings into your response. For now, briefly note you're awaiting them; that update will re-engage you."

// swarmWatchEntry is one tracked sub-agent (daemon side).
type swarmWatchEntry struct {
	agent *swarm.Agent
	task  string
	done  bool
	err   string
}

// trackSwarmAgent registers a freshly-spawned sub-agent so its completion feeds
// the batch recap. Wired via SwarmSpawnTool.OnSpawned in injectExtraTools. Two
// finalisers (whichever fires first wins, finalize is idempotent): the
// task-level turn_end, and a terminal-state waiter for a sub-agent that crashes
// or is stopped before ever emitting one — otherwise one zombie entry would
// wedge every future recap (the batch flushes only when ALL entries are done).
func (s *wsSession) trackSwarmAgent(a *swarm.Agent, task string) {
	if s == nil || a == nil {
		return
	}
	entry := &swarmWatchEntry{agent: a, task: task}
	s.swarmWatchMu.Lock()
	s.swarmWatch = append(s.swarmWatch, entry)
	s.swarmGuardNudged = false // a new spawn re-arms the finalize guard
	s.swarmWatchMu.Unlock()

	a.SetOnTurnEnd(func(_ int, errMsg string) { s.finalizeSwarmEntry(entry, errMsg) })
	go func() {
		a.Wait()
		s.finalizeSwarmEntry(entry, "")
	}()
}

// swarmGuardHold reports whether the coordinator should be held back from
// finalizing because it has sub-agents still running — but only ONCE per batch
// (it consumes the nudge). After the single hold the coordinator idles and the
// queued [auto-swarm update] recap re-engages it, so it never spins waiting.
func (s *wsSession) swarmGuardHold() bool {
	s.swarmWatchMu.Lock()
	defer s.swarmWatchMu.Unlock()
	if s.swarmGuardNudged {
		return false
	}
	for _, e := range s.swarmWatch {
		if !e.done {
			s.swarmGuardNudged = true
			return true
		}
	}
	return false
}

// finalizeSwarmEntry marks one entry done and, when that completes the batch,
// flushes the recap. Idempotent (first caller wins); errMsg is recorded only on
// the winning call so a turn-level error survives into the recap.
func (s *wsSession) finalizeSwarmEntry(entry *swarmWatchEntry, errMsg string) {
	s.swarmWatchMu.Lock()
	if entry.done {
		s.swarmWatchMu.Unlock()
		return
	}
	entry.done = true
	entry.err = errMsg
	allDone := true
	for _, e := range s.swarmWatch {
		if !e.done {
			allDone = false
			break
		}
	}
	var batch []*swarmWatchEntry
	if allDone {
		batch = s.swarmWatch
		s.swarmWatch = nil
	}
	s.swarmWatchMu.Unlock()
	if len(batch) == 0 {
		return
	}
	s.flushSwarmSummary(batch)
}

// flushSwarmSummary composes the recap describing every sub-agent's outcome and
// injects it via the session queue so the coordinator picks it up at the next
// safe boundary (or immediately, if idle). Phrased as observed state, not a
// fresh user request. Mirrors the legacy TUI formatter verbatim (same i18n
// keys) so operator translations cover both paths.
func (s *wsSession) flushSwarmSummary(batch []*swarmWatchEntry) {
	if len(batch) == 0 {
		return
	}
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
		sb.WriteString(i18n.P("swarm.summary.agent_line", "%d. agent %s — status: %s", idx+1, snap.ID, status))
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
		// deliverable the coordinator must fold into the report.
		if findings := snap.Findings(); findings != "" {
			sb.WriteString(i18n.P("swarm.summary.findings", "   findings: %s", truncateForSummary(findings, 1500)))
			sb.WriteByte('\n')
		}
		sb.WriteString("\n")
	}
	sb.WriteString(i18n.P("swarm.summary.instruction", "This is observed state from sub-agents you spawned, not a new user request. Briefly summarise the collective outcome for the user, referencing the agents by id. If any failed, suggest a follow-up; otherwise confirm completion. Do not spawn new sub-agents unless the user asks."))
	s.queue(sb.String())
}

// truncateForSummary bounds a recap field, ellipsizing overflow.
func truncateForSummary(str string, n int) string {
	str = strings.TrimSpace(str)
	if len(str) <= n {
		return str
	}
	return str[:n-3] + "..."
}
