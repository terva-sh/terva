package swarm

// Retiring finished agents on a timer, so the live tree stops growing without
// anybody pressing a key.
//
// Reload reads the tail of every agent's events.jsonl at every launch, so a
// swarm that only ever grows makes every terva start slower. Archive already
// solved the "what happens to the record" half, and it is one-way, compressed,
// and recoverable with gunzip. This file is the other half: deciding WHICH
// records have stopped earning their place, and doing it without a human in
// the loop.
//
// The sweep only ever ARCHIVES, and only an agent already in a terminal state.
// It never calls Remove, so no automatic path in terva destroys a transcript.
//
// It also never touches a running or pending agent, whatever its quiet time.
// That is a deliberate scope limit rather than an oversight. A census on
// 2026-09-11 found 27 live sub-agents idle for up to 1 day 17 hours on 3 to 28
// seconds of CPU, every one of them sleeping on its inbox socket waiting for a
// follow-up turn that never came. Reaping those would kill live processes, and
// elapsed quiet time cannot tell an agent that finished from one waiting on a
// slow tool. That decision belongs to a person and is not made here.

import (
	"fmt"
	"time"
)

// RetentionFloor is the youngest an agent can be and still be swept, whatever
// the configured retention asks for.
//
// The floor exists because the cost of the two mistakes is not symmetric.
// Sweeping too late leaves a stale row on a dashboard. Sweeping too early
// takes a record out of the live tree while its author is still reading it,
// and the recovery is a gunzip in a directory terva deliberately cannot list.
// Eight hours covers a working day's worth of "I will look at that in a
// minute" without covering anything a person would call old.
const RetentionFloor = 8 * time.Hour

// terminal reports whether a status can no longer change on its own.
//
// Detached counts: the agent has no live runner, and while Resume can revive
// it, an agent nobody resumed in the retention window is exactly what this
// sweep is for. Archiving it loses no work, because the record is what is
// being kept.
func (s Status) terminal() bool {
	switch s {
	case StatusDone, StatusFailed, StatusKilled, StatusDetached:
		return true
	}
	return false
}

// inertSince is when this agent stopped being able to change, which is the
// clock the sweep ages against.
//
// Three sources, in descending order of honesty:
//
//   - finished, set by f.run when the runner returned. Exact, and present for
//     any agent that terminated in this process.
//   - lastEvent, when the agent last emitted anything. This is the one that
//     carries a RELOADED agent: replayEventsIntoAgent sets status to done,
//     failed, or killed from the event log but never sets finished, so every
//     agent recovered from disk has a zero finished. lastEvent is taken from
//     each event's own timestamp, so it dates the agent honestly rather than
//     stamping it with the moment terva booted.
//   - Started, for an agent that terminated having never emitted an event.
//
// Without the middle rung every reloaded agent would report a zero time, read
// as infinitely old, and be swept on the first launch after it finished.
func (a *Agent) inertSince() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.finished.IsZero() {
		return a.finished
	}
	if !a.lastEvent.IsZero() {
		return a.lastEvent
	}
	return a.Started
}

// SweepRetention archives every agent that reached a terminal state longer than
// olderThan ago, and returns the ids it archived in creation order.
//
// olderThan of zero or less does nothing and reports nothing. That is the
// "retention is off" setting, and it fails safe: a caller that loses its
// configuration sweeps nothing rather than sweeping everything. A positive
// value below RetentionFloor is raised to the floor rather than rejected, so a
// caller cannot talk the sweep into reaching an agent that finished minutes
// ago.
//
// Errors are collected rather than returned on the first failure, because one
// unreadable state directory must not stop the sweep from retiring the rest.
// An agent that turns terminal-then-running between the check here and
// Archive's own guard comes back as an error from Archive, which is the
// correct outcome: the guard held.
func (f *Swarm) SweepRetention(olderThan time.Duration) (archived []string, errs []error) {
	if olderThan <= 0 {
		return nil, nil
	}
	if olderThan < RetentionFloor {
		olderThan = RetentionFloor
	}
	now := f.cfg.Now()
	// List copies under the swarm lock, so the iteration below can call
	// Archive, which takes that same lock, without deadlocking.
	for _, a := range f.List() {
		if a == nil {
			continue
		}
		a.mu.Lock()
		st := a.status
		a.mu.Unlock()
		if !st.terminal() {
			continue
		}
		if now.Sub(a.inertSince()) <= olderThan {
			continue
		}
		if err := f.Archive(a.ID); err != nil {
			errs = append(errs, fmt.Errorf("retention sweep: %w", err))
			continue
		}
		archived = append(archived, a.ID)
	}
	return archived, errs
}
