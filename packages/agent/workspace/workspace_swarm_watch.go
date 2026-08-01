package workspace

import (
	"sort"
	"strings"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// The daemon-side auto-swarm recap — since the --tui-legacy driver's removal,
// the ONLY swarm watcher (its modes-side twin was deleted after a recap
// feature once landed there, in dead code, instead of here — recap changes
// belong in THIS file). swarm_spawn is fire-and-forget — the coordinator
// moves on — so when every sub-agent it spawned finishes, the host injects a
// single [auto-swarm update] recap as a queued turn so the coordinator can
// synthesize their outcomes.

// swarmWaitGateMessage re-prompts a coordinator that tried to finish while its
// spawned sub-agents are still running (see the swarm-hold continuation gate). Injected
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
	s.trackSwarmAgentEntry(a, task)
}

// trackSwarmAgentEntry is trackSwarmAgent returning the tracked entry — the
// shape a test needs to hold the entry across the batch's lifetime. Reading
// it back from s.swarmWatch instead is a race: a crash-fast child can
// finalise and FLUSH (which nils the slice) between this returning and the
// caller's next line, which is exactly the index-out-of-range CI run 1735
// died on. trackSwarmAgent itself keeps the OnSpawned func shape.
func (s *wsSession) trackSwarmAgentEntry(a *swarm.Agent, task string) *swarmWatchEntry {
	if s == nil || a == nil {
		return nil
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
	return entry
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

// swarmFindingsBatchBudget is the total bytes of sub-agent findings one recap
// will inline, shared across the batch.
//
// It was 1500 bytes PER CHILD, and that was the expensive setting. Inlining is
// paid once, at the uncached input rate, and is then part of the cached prefix
// for the rest of the session. Fetching the remainder with session_inspect is
// paid per call at whatever the whole context costs — in a 200k-token
// conversation roughly $0.50 a call, no matter how few bytes come back. One
// reviewed session spent $3.50, 8.6% of its total, paging two reports of 12 KB
// and 37 KB back through six such calls; inlining both outright would have cost
// about $0.06. Truncating to protect the context spent an order of magnitude
// more money than it saved.
//
// 48 KB is roughly 12k tokens: large enough that a normal batch of review
// reports lands whole, small enough to stay a rounding error against a large
// context. The session_inspect handle remains for anything past it.
const swarmFindingsBatchBudget = 48 << 10

// findingsBudgets divides swarmFindingsBatchBudget across the batch, max-min
// fair: every child gets an equal share, and a child that needs less than its
// share hands the remainder back to the ones that need more. So a single child
// may use the whole budget, while a batch of ten short reports still prints all
// ten in full rather than truncating each at a tenth.
//
// Takes the sizes rather than the entries: the allocation is arithmetic over
// demand and has no business reaching into an agent snapshot to get it.
func findingsBudgets(need []int) []int {
	order := make([]int, len(need))
	for i := range need {
		order[i] = i
	}
	// Smallest need first, so each pass recomputes the share over exactly the
	// children still unsatisfied.
	sort.Slice(order, func(a, b int) bool { return need[order[a]] < need[order[b]] })
	out := make([]int, len(need))
	remaining, left := swarmFindingsBatchBudget, len(need)
	for _, i := range order {
		share := remaining / left
		if need[i] < share {
			share = need[i]
		}
		out[i] = share
		remaining -= share
		left--
	}
	return out
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
	// Snapshot once per entry: Findings is consulted for the budget split and
	// again when rendering, and a live child could otherwise answer the two
	// calls differently — allocating against one report and printing another.
	snaps := make([]swarm.AgentSnapshot, len(batch))
	need := make([]int, len(batch))
	for i, e := range batch {
		snaps[i] = e.agent.Snapshot()
		need[i] = len(snaps[i].Findings())
	}
	budgets := findingsBudgets(need)
	var batchUsage provider.Usage
	for idx, e := range batch {
		snap := snaps[idx]
		// The snapshot's own Err and the batch entry's turn error are two ways
		// the same task can have failed; either one makes "completed" a lie.
		turnErr := snap.Err
		if turnErr == "" {
			turnErr = e.err
		}
		status := snap.RecapStatus(turnErr)
		// Book what this child spent against the session that ordered it, and
		// sum the batch for the closing line. A failed child still counts: it
		// spent real money before it died, and the whole point of this is that
		// the coordinator's record match what its decisions cost.
		//
		// Safe to add rather than diff here because a batch entry is finalised
		// once (trackSwarmAgent's two finalisers are idempotent), so each
		// child's cumulative is booked exactly once.
		if u := snap.Usage; u != (provider.Usage{}) {
			batchUsage = batchUsage.Add(u)
			if s.agent != nil {
				s.agent.RecordDelegatedUsage(u)
			}
		}
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
		// What this one cost. Delegation is the only action whose price is
		// unbounded by the coordinator's own turn, and the recap is where a
		// coordinator learns the outcome — so it is where the price belongs.
		if snap.Usage.CostUSD > 0 {
			sb.WriteString(i18n.P("swarm.summary.cost", "   cost: $%.4f", snap.Usage.CostUSD))
			sb.WriteByte('\n')
		}
		if snap.Err != "" {
			sb.WriteString(i18n.P("swarm.summary.error", "   error: %s", truncateForSummary(snap.Err, 240)))
			sb.WriteByte('\n')
		} else if e.err != "" {
			sb.WriteString(i18n.P("swarm.summary.turn_error", "   turn error: %s", truncateForSummary(e.err, 240)))
			sb.WriteByte('\n')
		}
		// The sub-agent's own answer — its findings — not a tail of tool
		// output. For a review specialist this IS the deliverable the
		// coordinator must fold into the report, so the budget is generous;
		// see findingsBudgets for why generous is also the CHEAP option.
		if findings := snap.Findings(); findings != "" {
			sb.WriteString(i18n.P("swarm.summary.findings", "   findings: %s", truncateForSummary(findings, budgets[idx])))
			sb.WriteByte('\n')
		} else {
			// Say it plainly. Printing nothing here reads as "findings omitted
			// for brevity" — a coordinator has no way to tell an absent field
			// from an empty one, and the old tail fallback existed precisely
			// because silence looked worse than noise. Silence is not the
			// alternative; a statement is.
			sb.WriteString(i18n.P("swarm.summary.no_findings", "   findings: none — this sub-agent produced no answer"))
			sb.WriteByte('\n')
		}
		// Structured-deliverable verdict (schema spawns only): the free-text
		// findings above can read fine while the machine contract silently
		// failed, so the coordinator is told explicitly which it got. Ported
		// from the deleted modes twin, where c7c22551 had landed it in code
		// nothing called — this recap never said it in production before.
		if len(snap.Deliverable) > 0 {
			sb.WriteString(i18n.P("swarm.summary.deliverable_ok", "   deliverable: validated structured report (%d bytes; full JSON via the tasks surface)", len(snap.Deliverable)))
			sb.WriteByte('\n')
		} else if snap.DeliverableError != "" {
			sb.WriteString(i18n.P("swarm.summary.deliverable_err", "   deliverable: contract NOT met — %s", truncateForSummary(snap.DeliverableError, 240)))
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
	// The batch total, last, where a reader lands. One run measured $24.49 of
	// delegated spend against a launching session that recorded $5.36 — the
	// coordinator could have reported the small number in good faith.
	if batchUsage.CostUSD > 0 {
		sb.WriteString(i18n.P("swarm.summary.batch_cost", "Batch cost: $%.4f across %d sub-agent(s), spent on this session's credentials.", batchUsage.CostUSD, len(batch)))
		sb.WriteString("\n\n")
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
