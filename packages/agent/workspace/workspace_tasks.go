package workspace

import (
	"strconv"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/worker"
	"terva.sh/terva/packages/i18n"
)

// The tasks pane surfaces the workspace-global swarm of background agents. The
// swarm exposes no change-notification, so a poller diffs SnapshotAll() and
// broadcasts a surface_updated signal when anything moves (see docs/proposals/
// web-surfaces.md §"live updates").

const taskPollInterval = 800 * time.Millisecond

// pollTasks watches the swarm for changes and pushes surface updates to every
// live session. Runs for the workspace lifetime (stops when w.ctx is cancelled).
func (w *Workspace) pollTasks() {
	if w.swarm == nil {
		return
	}
	t := time.NewTicker(taskPollInterval)
	defer t.Stop()
	var lastSig string
	var lastIDs string
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-t.C:
			// Unscoped on purpose: this is a CHANGE SIGNAL, not data. Each
			// session re-fetches and gets its own scoped list, so the poller
			// only has to notice that something, somewhere, moved.
			snaps := w.swarm.SnapshotAll()
			sig := taskSignature(snaps)
			if sig == lastSig {
				continue
			}
			lastSig = sig
			// Whether the pane exists toggles per SESSION now, and this loop
			// has no session — so it re-lists whenever the set of agents
			// changes rather than when the global count crosses zero. A spawn
			// in one session must be able to make the tab appear in that
			// session while the workspace-wide count was never zero.
			if ids := taskIDs(snaps); ids != lastIDs {
				lastIDs = ids
				w.BroadcastAll(ctrlproto.SurfacesChangedEvent())
			}
			w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("tasks"))
		}
	}
}

// taskIDs fingerprints only WHICH agents exist — the input to "should this
// session have a tasks tab at all", which changes far less often than the
// activity churn taskSignature tracks.
func taskIDs(snaps []swarm.AgentSnapshot) string {
	var b strings.Builder
	for _, s := range snaps {
		b.WriteString(s.ID)
		b.WriteByte('|')
	}
	return b.String()
}

// taskSignature is a cheap fingerprint of the swarm state — enough to detect a
// spawn, a status/activity change, or new streamed output.
func taskSignature(snaps []swarm.AgentSnapshot) string {
	var b strings.Builder
	for _, s := range snaps {
		b.WriteString(s.ID)
		b.WriteByte(':')
		b.WriteString(string(s.Status))
		b.WriteByte(':')
		b.WriteString(s.Activity)
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(len(s.Tail)))
		b.WriteByte('|')
	}
	return b.String()
}

// BroadcastAll publishes a WORKSPACE-scoped event: one that is true of the
// daemon rather than of any session — a workspace surface changing (tasks, mcp,
// chat, settings, permissions, raati), the locale changing, a restart notice.
//
// It used to fan the event out to every live session's subscribers, stamping a
// copy with each session's id, because a session hub was the only hub there was.
// That had a hole it could not close: it reaches LIVE SESSIONS' subscribers, so
// a client holding no subscriptions — or a workspace whose last live session was
// just deleted — heard nothing at all. For an event whose whole job is to
// describe a workspace that may have no sessions, that is not an edge case.
//
// Now it publishes ONCE, to the workspace itself. The per-session duplication
// still happens for a client that has not negotiated FeatureWorkspaceEvents, but
// it happens at the serialization edge (serveState's compat pump), where it
// belongs: it is a property of that client's contract, not of the workspace.
func (w *Workspace) BroadcastAll(ev ctrlproto.Event) {
	w.events().broadcast(ev)
}

// taskList builds the tasks pane from the swarm snapshot, scoped to the
// session asking. The swarm is workspace-global and its agents outlive the run
// that spawned them, so without a scope every conversation inherits every other
// conversation's background work — including yesterday's, in another repo.
func (w *Workspace) taskList(sessionID string) *ctrlproto.TaskList {
	if w.swarm == nil {
		return &ctrlproto.TaskList{}
	}
	snaps := w.swarm.SnapshotFor(sessionID)
	out := make([]ctrlproto.TaskInfo, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, ctrlproto.TaskInfo{
			ID:               s.ID,
			Task:             s.Task,
			Status:           string(s.Status),
			Activity:         s.Activity,
			Model:            s.Model,
			Provider:         s.Provider,
			Persona:          s.Persona,
			Backend:          s.Backend,
			Dir:              s.Dir,
			Started:          ctrlTimeString(s.Started),
			Finished:         ctrlTimeString(s.Finished),
			Err:              s.Err,
			CostUSD:          s.CostUSD,
			Deliverable:      s.Deliverable,
			DeliverableError: s.DeliverableError,
			Tail:             s.Tail,
			Lines:            s.Lines,
		})
	}
	// Advertise the spawn capability so the board's swarm lane can offer a
	// backend picker (native is implicit; foreign backends are listed even when
	// disabled so the UI can grey them with a hint) — see TaskList.Backends.
	// No archived tally rides here. An archived record is unreachable from
	// terva by design (swarm.Archive), and a count is a read path — the one
	// that grows into a list, then into "open it just to look".
	return &ctrlproto.TaskList{Tasks: out, Backends: worker.Names(), WorkersEnabled: config.ExternalWorkersEnabled()}
}

// hasTasks reports whether the tasks pane should be offered to this session:
// auto-swarm is on (so the agent can spawn) or agents this session can see
// already exist (e.g. detached from a prior run). Scoped for the same reason
// taskList is — an empty pane is better than a pane full of another
// conversation's agents.
func (w *Workspace) hasTasks(sessionID string) bool {
	if w.swarm == nil {
		return false
	}
	return config.AutoSwarmEnabled() || len(w.swarm.SnapshotFor(sessionID)) > 0
}

// taskAction dispatches a tasks-pane action to the swarm, then nudges clients.
//
// archive is one-way, has no undo verb, and leaves nothing terva can read back
// (see swarm.Archive). It is offered beside remove because the pair IS the
// choice: keep the transcript on disk, or don't keep it at all.
//
// sessionID is the conversation acting. It stamps a spawn — the board used to
// send none at all, so every agent started from the pane landed unscoped and
// showed up in every session forever. Stop/remove/resume/send take an id that
// only reaches the client through a scoped list, so they are not re-checked
// here; the scope is a view, not a permission boundary (same user, same
// machine, and a cross-session stop is a reasonable thing to want).
func (w *Workspace) taskAction(sessionID, action string, args map[string]string) error {
	if w.swarm == nil {
		return ctrlproto.Errorf(ctrlproto.CodeUnsupported, "%s", i18n.T("no swarm"))
	}
	id := args["id"]
	var err error
	switch action {
	case "stop":
		err = w.swarm.Stop(id)
	case "remove":
		err = w.swarm.Remove(id)
	case "archive":
		err = w.swarm.Archive(id)
	case "resume":
		_, err = w.swarm.Resume(w.ctx, id)
	case "send":
		err = w.swarm.SendUserTurn(id, args["text"])
	case "spawn":
		// Actions carry no result payload, so the new agent's id doesn't ride
		// back — the caller sees it appear on the next tasks fetch instead.
		if strings.TrimSpace(args["task"]) == "" {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("spawn: missing task"))
		}
		// A foreign backend goes through the SAME gate the model's swarm_spawn
		// tool applies (external workers on + a registered backend), so the human
		// path is at parity: the board can't dispatch a worker the model couldn't.
		// Empty backend = a native swarm agent, ungated as it always has been.
		backend := args["backend"]
		if backend != "" {
			if gerr := allowWorkerBackend(backend); gerr != nil {
				return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%v", gerr)
			}
		}
		_, err = w.swarm.SpawnReq(w.ctx, swarm.SpawnRequest{
			Task: args["task"], Model: args["model"], Provider: args["provider"], Persona: args["persona"],
			Backend: backend, SessionID: sessionID,
		})
	default:
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("unknown tasks action %q", action))
	}
	if err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "%v", err)
	}
	w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("tasks"))
	return nil
}
