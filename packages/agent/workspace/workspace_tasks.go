package workspace

import (
	"strconv"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/swarm"
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
	var lastAny bool
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-t.C:
			snaps := w.swarm.SnapshotAll()
			sig := taskSignature(snaps)
			if sig == lastSig {
				continue
			}
			lastSig = sig
			// Whether the pane exists at all toggles with agent presence; tell
			// clients to re-list so the tab appears/disappears.
			if any := len(snaps) > 0; any != lastAny {
				lastAny = any
				w.BroadcastAll(ctrlproto.SurfacesChangedEvent())
			}
			w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("tasks"))
		}
	}
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

// taskList builds the tasks pane from the swarm snapshot.
func (w *Workspace) taskList() *ctrlproto.TaskList {
	if w.swarm == nil {
		return &ctrlproto.TaskList{}
	}
	snaps := w.swarm.SnapshotAll()
	out := make([]ctrlproto.TaskInfo, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, ctrlproto.TaskInfo{
			ID:       s.ID,
			Task:     s.Task,
			Status:   string(s.Status),
			Activity: s.Activity,
			Model:    s.Model,
			Provider: s.Provider,
			Persona:  s.Persona,
			Dir:      s.Dir,
			Started:  ctrlTimeString(s.Started),
			Finished: ctrlTimeString(s.Finished),
			Err:      s.Err,
			Tail:     s.Tail,
			Lines:    s.Lines,
		})
	}
	return &ctrlproto.TaskList{Tasks: out}
}

// hasTasks reports whether the tasks pane should be offered: auto-swarm is on
// (so the agent can spawn) or agents already exist (e.g. detached from a prior
// run).
func (w *Workspace) hasTasks() bool {
	if w.swarm == nil {
		return false
	}
	return config.AutoSwarmEnabled() || len(w.swarm.SnapshotAll()) > 0
}

// taskAction dispatches a tasks-pane action to the swarm, then nudges clients.
func (w *Workspace) taskAction(action string, args map[string]string) error {
	if w.swarm == nil {
		return ctrlproto.Errorf(ctrlproto.CodeUnsupported, "no swarm")
	}
	id := args["id"]
	var err error
	switch action {
	case "stop":
		err = w.swarm.Stop(id)
	case "remove":
		err = w.swarm.Remove(id)
	case "resume":
		_, err = w.swarm.Resume(w.ctx, id)
	case "send":
		err = w.swarm.SendUserTurn(id, args["text"])
	case "spawn":
		// Actions carry no result payload, so the new agent's id doesn't ride
		// back — the caller sees it appear on the next tasks fetch instead.
		if strings.TrimSpace(args["task"]) == "" {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "spawn: missing task")
		}
		_, err = w.swarm.SpawnReq(w.ctx, swarm.SpawnRequest{
			Task: args["task"], Model: args["model"], Provider: args["provider"], Persona: args["persona"],
		})
	default:
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "unknown tasks action %q", action)
	}
	if err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "%v", err)
	}
	w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("tasks"))
	return nil
}
