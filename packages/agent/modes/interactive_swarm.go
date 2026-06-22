package modes

// Auto-swarm supervision: tracking spawned sub-agents, batching their
// completions into one summary turn, and the system-prompt/tool
// toggles.

import (
	"fmt"
	"strings"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
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
	var sb strings.Builder
	fmt.Fprintf(&sb, "[auto-swarm update] %d sub-agent(s) finished:\n\n", len(batch))
	for idx, e := range batch {
		snap := e.agent.Snapshot()
		status := string(snap.Status)
		task := snap.Task
		if task == "" {
			task = e.task
		}
		fmt.Fprintf(&sb, "%d. agent %s \u2014 status: %s\n", idx+1, snap.ID, status)
		fmt.Fprintf(&sb, "   task: %s\n", truncateForSummary(task, 240))
		if snap.Err != "" {
			fmt.Fprintf(&sb, "   error: %s\n", truncateForSummary(snap.Err, 240))
		} else if e.err != "" {
			fmt.Fprintf(&sb, "   turn error: %s\n", truncateForSummary(e.err, 240))
		}
		if tail := strings.TrimSpace(snap.Tail); tail != "" {
			fmt.Fprintf(&sb, "   tail: %s\n", truncateForSummary(tail, 600))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Briefly summarise the collective outcome for the user. Reference the agents by id. If any failed, suggest a follow-up; otherwise confirm completion. Do not spawn new sub-agents unless the user asks.")
	i.SubmitOrQueue(sb.String(), nil)
}

func truncateForSummary(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// applyAutoSwarmSystemPrompt appends (active=true) or strips
// (active=false) the auto-swarm system-prompt block on the running
// agent so the model proactively considers swarm_spawn when the user
// flips the toggle. The block lives at the tail of agent.System so
// stripping is a plain suffix-trim; idempotent in both directions.
func (i *Interactive) applyAutoSwarmSystemPrompt(active bool) {
	ag := i.turns.Agent()
	if ag == nil {
		return
	}
	addendum := i.cfg.AutoSwarmSystemAddendum
	if addendum == "" {
		return
	}
	sys := ag.System
	has := strings.Contains(sys, addendum)
	switch {
	case active && !has:
		if sys != "" && !strings.HasSuffix(sys, "\n\n") {
			sys += "\n\n"
		}
		ag.System = sys + addendum
	case !active && has:
		ag.System = strings.TrimRight(strings.ReplaceAll(sys, addendum, ""), "\n") + "\n"
	}
}

// applyAutoSwarmTool registers (active=true) or removes (active=false)
// the swarm_spawn tool on the running agent so the model only sees it
// when /settings -> auto-swarm is enabled. Mirrors applyChatTools'
// snapshot+mutate pattern so extension tools and /reload-ext additions
// survive a toggle.
func (i *Interactive) applyAutoSwarmTool(active bool) {
	ag := i.turns.Agent()
	if ag == nil {
		return
	}
	current := ag.Tools
	next := core.Registry{}
	for name, t := range current {
		if name == "swarm_spawn" {
			continue
		}
		next[name] = t
	}
	if active && i.cfg.Swarm != nil {
		next["swarm_spawn"] = &tools.SwarmSpawnTool{
			Swarm:        i.cfg.Swarm,
			Enabled:      func() bool { return true },
			OnSpawned:    i.trackSwarmAgent,
			HostProvider: i.cfg.Provider, // updated on /model swap (interactive_model.go)
			HostModel:    i.cfg.Model,
			Tiers:        tools.SwarmTierMap(i.cfg.SwarmTiers),
		}
	}
	ag.SetTools(next)
}
