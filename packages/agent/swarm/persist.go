package swarm

// On-disk persistence for swarm agents.
//
// Every Spawn writes a meta.json next to the agent's events.jsonl and
// session.json. The file captures the immutable identity bits (id,
// task, branch, dir) plus the paths the runner needs to resume the
// agent later. On a fresh terva launch, Swarm.Reload() walks
// <root>/agents/*/meta.json and re-registers every agent it finds in
// StatusDetached so the user can see, view, resume, or remove them
// from the dashboard.
//
// We don't try to keep meta.json in sync with mutable state (status,
// activity, transcript). Those live in the events log (durable) and
// in-memory Agent fields (rebuilt by Reload from the log tail).
// Keeping meta.json immutable means we never have to worry about
// concurrent writers stomping on each other and the file matters
// only on the spawn/reload boundary.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/privfs"
)

// agentMeta is the durable identity record for one agent. Only fields
// the supervisor needs to rebuild an Agent after a restart live here.
// Adding a field is backwards-compatible (older meta.json files just
// leave it zero); removing or renaming one is not.
//
// Historical fields like `branch` and `isolated` are silently dropped
// by encoding/json's permissive decoder when an older meta.json is
// loaded; we don't need to keep them in the struct.
type agentMeta struct {
	ID         string    `json:"id"`
	Task       string    `json:"task"`
	Dir        string    `json:"dir"`
	Started    time.Time `json:"started"`
	Model      string    `json:"model,omitempty"`
	Provider   string    `json:"provider,omitempty"`
	Persona    string    `json:"persona,omitempty"`
	Experience string    `json:"experience,omitempty"`
	Substrate  string    `json:"substrate,omitempty"`
	Card       string    `json:"card,omitempty"`
	// Backend is the worker backend that drove this agent (empty = a native
	// terva swarm child). An agent revived onto the WRONG backend would be
	// handed a cursor and an event stream its runner cannot read, so this is
	// durable state, not a spawn-time convenience.
	Backend string `json:"backend,omitempty"`
	// Approval is an explicit per-worker posture override; Leased records
	// whether this agent got its own worktree. Both feed the worker backend's
	// default-posture resolution, so a revived worker keeps the same posture it
	// ran with (a leased worker stays autonomous; an explicit override persists).
	Approval string `json:"approval,omitempty"`
	Leased   bool   `json:"leased,omitempty"`
	// Origin is the spawning swarm's RepoRoot — the project this agent belongs
	// to. Durable because it is the only record of ownership that survives
	// worktree isolation: Dir is a lease path that hashes to a different
	// project than its parent, so a tool authorizing by project cannot use it.
	Origin string `json:"origin,omitempty"`
	// Schema is the structured-deliverable contract (see Agent.Schema). A
	// revived agent must keep it or its next turn would silently drop the
	// validation the dispatcher asked for.
	Schema       json.RawMessage `json:"schema,omitempty"`
	InboxPath    string          `json:"inbox_path"`
	EventLogPath string          `json:"event_log_path"`
	SessionPath  string          `json:"session_path"`

	// SessionID, when non-empty, scopes the agent to a particular
	// host terva session: the dashboard only shows agents whose
	// SessionID matches the active session. Older meta.json files
	// (and agents spawned outside of any session, e.g. by tests or
	// scripted callers that didn't call SetActiveSession) have an
	// empty SessionID and are visible from every session as a
	// backward-compat fallback.
	SessionID string `json:"session_id,omitempty"`
}

func metaPath(stateDir string) string { return filepath.Join(stateDir, "meta.json") }

// writeAgentMeta serialises a's identity into stateDir/meta.json. The
// write is atomic (tmp + rename) so a crash mid-write can't leave a
// half-parsable file that fails Reload.
func writeAgentMeta(stateDir string, a *Agent) error {
	m := agentMeta{
		ID:           a.ID,
		Task:         a.Task,
		Dir:          a.Dir,
		Started:      a.Started,
		Model:        a.Model,
		Provider:     a.Provider,
		Persona:      a.Persona,
		Experience:   a.Experience,
		Substrate:    a.Substrate,
		Card:         a.Card,
		Backend:      a.Backend,
		Approval:     a.Approval,
		Leased:       a.Leased,
		Origin:       a.Origin,
		Schema:       a.Schema,
		InboxPath:    a.InboxPath,
		EventLogPath: a.EventLogPath,
		SessionPath:  a.SessionPath,
		SessionID:    a.SessionID,
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("swarm meta marshal: %w", err)
	}
	if err := privfs.MkdirAll(stateDir); err != nil {
		return fmt.Errorf("swarm meta dir: %w", err)
	}
	// privfs.WriteFile is the same temp+rename dance this used to hand-roll,
	// plus private modes (0600 under 0700).
	if err := privfs.WriteFile(metaPath(stateDir), append(b, '\n')); err != nil {
		return fmt.Errorf("swarm meta write: %w", err)
	}
	return nil
}

// readAgentMeta loads one meta.json. Returns os.ErrNotExist (wrapped)
// when the file is missing so callers can distinguish "no such agent"
// from "corrupt metadata".
func readAgentMeta(stateDir string) (agentMeta, error) {
	var m agentMeta
	b, err := os.ReadFile(metaPath(stateDir))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("swarm meta parse %s: %w", stateDir, err)
	}
	if m.ID == "" {
		return m, fmt.Errorf("swarm meta %s: missing id", stateDir)
	}
	return m, nil
}

// Reload scans <root>/agents/*/meta.json and re-registers every
// previously-spawned agent as a StatusDetached entry. Agents already
// present in memory are left alone (Reload is idempotent and safe to
// call after Spawn, though in practice the cli invokes it exactly
// once just after New()).
//
// The reloaded agents have no live Runner; the user can:
//   - view their transcript (the dashboard reads from EventLogPath),
//   - resume them via Swarm.Resume (starts a fresh subprocess on the
//     same worktree / session / inbox path),
//   - remove them (worktree + meta + events log gone).
//
// Reload returns the number of agents loaded plus any per-directory
// error encountered. Malformed entries are skipped rather than
// failing the whole reload — one bad meta.json shouldn't hide the
// rest of the swarm.
func (f *Swarm) Reload() (loaded int, errs []error) {
	agentsDir := filepath.Join(f.cfg.Root, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, []error{fmt.Errorf("swarm reload: %w", err)}
	}

	// Sort by directory name so the load order is stable across runs.
	// agentStateDir uses the id verbatim so name-sort == id-sort,
	// which mirrors the creation order well enough for the dashboard
	// (we also sort by Started in SnapshotAll, but having a stable
	// f.order helps tests).
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		stateDir := filepath.Join(agentsDir, name)
		m, err := readAgentMeta(stateDir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// Bare directory with no meta.json — probably a
				// leftover from a Spawn that failed before
				// writeAgentMeta. Ignore silently.
				continue
			}
			errs = append(errs, err)
			continue
		}

		f.mu.Lock()
		if _, exists := f.agents[m.ID]; exists {
			f.mu.Unlock()
			continue
		}
		a := f.buildDetachedAgent(m)
		f.agents[m.ID] = a
		f.order = append(f.order, m.ID)
		f.mu.Unlock()
		loaded++
	}
	return loaded, errs
}

// buildDetachedAgent constructs an Agent from a meta.json with no
// running Runner. The agent's transcript is populated from the tail
// of its event log so the dashboard immediately shows recent output;
// activity is inferred from the last lifecycle event.
//
// The returned Agent has a closed `done` channel because Wait should
// return instantly: there is nothing to wait for.
func (f *Swarm) buildDetachedAgent(m agentMeta) *Agent {
	// Older meta.json files may still record a per-agent worktree
	// path under Dir. They predate the decision to run every agent
	// in the host's repo and shouldn't continue editing that stale
	// checkout, which most likely no longer matches HEAD. Coerce
	// the dir back to the live RepoRoot so resume picks up where
	// the host is now.
	dir := m.Dir
	if f.cfg.RepoRoot != "" {
		dir = f.cfg.RepoRoot
	}
	a := &Agent{
		ID:           m.ID,
		Task:         m.Task,
		Dir:          dir,
		Started:      m.Started,
		Model:        m.Model,
		Provider:     m.Provider,
		Persona:      m.Persona,
		Experience:   m.Experience,
		Substrate:    m.Substrate,
		Card:         m.Card,
		Backend:      m.Backend,
		Approval:     m.Approval,
		Leased:       m.Leased,
		Origin:       m.Origin,
		Schema:       m.Schema,
		InboxPath:    m.InboxPath,
		EventLogPath: m.EventLogPath,
		SessionPath:  m.SessionPath,
		SessionID:    m.SessionID,
		inbox:        NewInbox(m.InboxPath),
		status:       StatusDetached,
		activity:     "detached",
		done:         make(chan struct{}),
	}
	// Wait() must not block for detached agents; close the channel
	// immediately. Callers Resuming the agent will replace done with
	// a fresh channel before starting the new runner.
	close(a.done)

	// Recover transcript + activity hints from the event log. Best
	// effort: a missing or unreadable log just leaves the agent
	// detached with an empty transcript.
	if a.EventLogPath != "" {
		if evs, err := ReadEventLog(a.EventLogPath); err == nil {
			replayEventsIntoAgent(a, evs)
		}
	}
	return a
}

// replayEventsIntoAgent re-derives an agent's transcript and last
// known status hint from its event log. Mirrors applyEventToSink in
// runner.go but writes directly to the Agent fields (no Sink because
// the agent isn't being driven by a runner yet).
//
// Status precedence: explicit lifecycle events (agent_stopped) win
// over inferred ones (assistant_message → idle). If the log contains
// no terminator we keep status=StatusDetached so the user can resume.
func replayEventsIntoAgent(a *Agent, evs []Event) {
	terminal := false
	for _, ev := range evs {
		// Same counters a live runner keeps (IngestEvent), rebuilt from the
		// log so a detached agent reports the work it actually did instead
		// of zero. Each event carries its own timestamp, so the recovered
		// lastEvent is when the agent really went quiet.
		a.noteProgress(ev)
		switch ev.Type {
		case "assistant_message":
			var parts []string
			if c, ok := eventMessageContent(ev.Data); ok {
				for _, blk := range c {
					m, _ := blk.(map[string]any)
					if t, _ := m["type"].(string); t == "text" {
						if txt, _ := m["text"].(string); txt != "" {
							a.appendTranscript(txt)
							parts = append(parts, txt)
						}
					}
				}
			}
			// Recover the last answer too (mirrors applyEventToSink), so a
			// snapshot of a reloaded agent carries its findings, not just a
			// transcript tail.
			if len(parts) > 0 {
				a.setLastAssistant(strings.Join(parts, "\n"))
			}
		case "user_message":
			if c, ok := eventMessageContent(ev.Data); ok {
				for _, blk := range c {
					m, _ := blk.(map[string]any)
					if t, _ := m["type"].(string); t == "text" {
						if txt, _ := m["text"].(string); txt != "" {
							a.appendTranscript("user: " + txt)
							// Mirror applyEventToSink: a reloaded agent's
							// snapshot must arbitrate findings the same
							// way as a live one.
							if IsOpenWorkGateNudge(txt) {
								a.noteGuardNudge()
							}
						}
					}
				}
			}
		case "stdout":
			if txt, _ := ev.Data["text"].(string); txt != "" {
				a.appendTranscript(txt)
			}
		case "stderr":
			if txt, _ := ev.Data["text"].(string); txt != "" {
				a.appendTranscript("stderr: " + txt)
			}
		case "error":
			if msg, _ := ev.Data["message"].(string); msg != "" {
				a.appendTranscript("error: " + msg)
			}
		case "agent_stopped":
			terminal = true
			reason, _ := ev.Data["reason"].(string)
			a.mu.Lock()
			switch reason {
			case "cancelled":
				a.status = StatusKilled
				a.activity = "cancelled (offline)"
			case "shutdown":
				a.status = StatusDone
				a.activity = "shutdown (offline)"
			case "exit":
				if code, ok := ev.Data["code"].(float64); ok && code != 0 {
					a.status = StatusFailed
					a.activity = fmt.Sprintf("exit %d (offline)", int(code))
				} else {
					a.status = StatusDone
					a.activity = "done (offline)"
				}
			default:
				a.status = StatusDone
				a.activity = "stopped (offline)"
			}
			a.mu.Unlock()
		}
	}
	if !terminal {
		// Non-terminal log means the previous parent died mid-run.
		// Leave status=StatusDetached but record a hint so the
		// dashboard shows something useful.
		a.mu.Lock()
		if a.activity == "detached" && len(a.transcript) > 0 {
			a.activity = "detached (resume to continue)"
		}
		a.mu.Unlock()
	}
}

// Resume re-attaches a Runner to a previously-spawned agent. The
// existing worktree, session file, branch, and inbox path are kept;
// only the in-memory Agent and its runner are replaced. Use this to
// continue a swarm session across terva restarts:
//
//	swarmMgr.Reload()
//	a, err := swarmMgr.Resume(ctx, "alpha-12345")
//	swarmMgr.SendUserTurn(a.ID, "where were we?")
//
// The agent must be in a non-running state (Detached, Done, Failed,
// Killed). Resuming a still-running agent returns an error so two
// runners don't race for the same session.
//
// ctx is reserved for call-scoped setup; the resumed agent's lifetime
// is swarm-scoped (see SpawnReq) and ends only via Stop/StopAllAndWait.
func (f *Swarm) Resume(ctx context.Context, id string) (*Agent, error) {
	existing := f.Get(id)
	if existing == nil {
		return nil, fmt.Errorf("swarm: no such agent %q", id)
	}
	existing.mu.Lock()
	st := existing.status
	existing.mu.Unlock()
	if st == StatusRunning || st == StatusPending {
		return nil, fmt.Errorf("swarm: agent %s is still %s; stop it first", existing.ID, st)
	}

	// Rebuild from the meta record so we don't carry stale runner
	// state from a previous incarnation. We re-read meta.json rather
	// than reusing the live struct's fields so callers that mutated
	// (e.g. tests that hand-built an Agent) don't accidentally route
	// the new runner at the wrong paths.
	m := agentMeta{
		ID: existing.ID, Task: existing.Task,
		Dir: existing.Dir, Started: existing.Started,
		Model: existing.Model, Provider: existing.Provider, Persona: existing.Persona,
		Experience: existing.Experience, Substrate: existing.Substrate, Card: existing.Card,
		Backend:  existing.Backend,
		Approval: existing.Approval, Leased: existing.Leased,
		// Origin is spawn-time ownership; dropping it here would persist an
		// empty origin and permanently orphan the agent from its project.
		Origin:    existing.Origin,
		Schema:    existing.Schema,
		InboxPath: existing.InboxPath, EventLogPath: existing.EventLogPath,
		SessionPath: existing.SessionPath,
		// SessionID is spawn-time scoping; it must survive Resume or
		// writeAgentMeta below would persist an empty session_id and
		// permanently un-scope the agent from its host session.
		SessionID: existing.SessionID,
	}

	a := &Agent{
		ID:           m.ID,
		Task:         m.Task,
		Dir:          m.Dir,
		Started:      m.Started,
		Model:        m.Model,
		Provider:     m.Provider,
		Persona:      m.Persona,
		Experience:   m.Experience,
		Substrate:    m.Substrate,
		Card:         m.Card,
		Backend:      m.Backend,
		Approval:     m.Approval,
		Leased:       m.Leased,
		Origin:       m.Origin,
		Schema:       m.Schema,
		SessionID:    m.SessionID,
		InboxPath:    m.InboxPath,
		EventLogPath: m.EventLogPath,
		SessionPath:  m.SessionPath,
		Resuming:     true,
		inbox:        NewInbox(m.InboxPath),
		status:       StatusPending,
		activity:     "resuming",
		done:         make(chan struct{}),
	}
	// Carry the previous transcript forward so the dashboard doesn't
	// flash empty between resume and the first new event.
	prev := existing.Transcript()
	if len(prev) > 0 {
		a.appendTranscript(strings.Join(prev, "\n"))
	}
	// Swarm-scoped lifetime, same as SpawnReq: the resumed agent must
	// outlive the caller's (turn-scoped) context.
	a.ctx, a.cancel = context.WithCancel(f.ctx)
	a.runner = f.cfg.NewRunner(a)

	f.mu.Lock()
	f.agents[a.ID] = a
	// Keep the agent's slot in f.order; replacing in-place avoids
	// reshuffling the dashboard's row ordering on resume.
	found := false
	for _, k := range f.order {
		if k == a.ID {
			found = true
			break
		}
	}
	if !found {
		f.order = append(f.order, a.ID)
	}
	f.mu.Unlock()

	// Refresh the meta.json so any new path defaults (e.g. socket
	// path moved into /tmp because the root got renamed) get
	// persisted. Best-effort; resume still works if the disk is
	// read-only because everything the runner needs is in-memory.
	_ = writeAgentMeta(f.agentStateDir(a.ID), a)

	go f.run(a)
	return a, nil
}
