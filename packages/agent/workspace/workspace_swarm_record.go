package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/session"
)

// The durable half of the sub-agent recap.
//
// The watch in workspace_swarm_watch.go lives in memory, so a daemon that
// stopped in the middle of a batch lost the report. The session's state file
// now names every sub-agent whose report has not reached the transcript. The
// next daemon reads that record when it builds the session, and it queues one
// recap that says what stopped.
//
// 🔑 An id leaves the record when its recap has left the queue for good. It
// does not leave when the recap is queued, because the queue is in memory
// too. Three places know that a recap left, and each knows the text as the
// session wrote it:
//
//   - the loop's drain, which takes queued texts into the transcript,
//   - the end of a turn that the recap started,
//   - a client's replacement of the queue, which is a person's removal.
//
// ⚠️ None of them reads the text of the message that arrived. A user-message
// guard can change that text, and a client can queue a text of its own under
// the recap's source, so the arrived text names no batch.

// swarmRecapWait is one recap that the queue holds, with the sub-agents it
// reports.
type swarmRecapWait struct {
	text string
	ids  []string
}

// editSwarmBatch changes the sub-agent ids in this session's state file.
//
// A session with no file, such as a test's, records nothing.
func (s *wsSession) editSwarmBatch(edit func(ids []string) []string) {
	if s == nil || s.ws == nil || s.sess == nil {
		return
	}
	if err := s.ws.editSwarmBatch(s.sess.StatePath(), edit); err != nil {
		// The record is a convenience, as the whole file is. A failed write
		// costs one report after a restart.
		s.diag(fmt.Sprintf("swarm: the record of sub-agents that wait for a report was not saved: %v", err))
	}
}

// editSwarmBatch changes the sub-agent ids in the state file at path, and
// signs the record with this daemon instance's name. It holds the state
// lock, so an edit cannot lose a draft that a client saves at the same time.
func (w *Workspace) editSwarmBatch(path string, edit func(ids []string) []string) error {
	if path == "" {
		return nil
	}
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	state := session.LoadSessionState(path)
	batch, _ := state.SwarmBatch()
	batch.Agents = edit(batch.Agents)
	batch.Boot = w.boot
	if err := state.SetSwarmBatch(batch); err != nil {
		return err
	}
	return session.SaveSessionState(path, state)
}

// swarmBatchFromBefore returns the sub-agent ids that another daemon instance
// left in the state file at path. ok is false when the file names none, and
// when this instance wrote the record.
//
// 🔑 The second case is a session that closed and opened again under one
// daemon, as an archive does. No restart came between, so nothing reports
// one.
func (w *Workspace) swarmBatchFromBefore(path string) (ids []string, ok bool) {
	if path == "" {
		return nil, false
	}
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	batch, ok := session.LoadSessionState(path).SwarmBatch()
	if !ok || batch.Boot == w.boot {
		return nil, false
	}
	return batch.Agents, true
}

func appendID(ids []string, id string) []string {
	for _, have := range ids {
		if have == id {
			return ids
		}
	}
	return append(ids, id)
}

func withoutIDs(ids, drop []string) []string {
	out := ids[:0:0]
	for _, id := range ids {
		keep := true
		for _, d := range drop {
			if d == id {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, id)
		}
	}
	return out
}

// awaitSwarmRecap records that a recap is on its way to the transcript.
func (s *wsSession) awaitSwarmRecap(text string, ids []string) {
	s.swarmWatchMu.Lock()
	s.swarmRecaps = append(s.swarmRecaps, swarmRecapWait{text: text, ids: ids})
	s.swarmWatchMu.Unlock()
}

// forgetSwarmRecap drops a wait that was never queued.
func (s *wsSession) forgetSwarmRecap(text string) {
	s.swarmWatchMu.Lock()
	for i, w := range s.swarmRecaps {
		if w.text == text {
			s.swarmRecaps = append(s.swarmRecaps[:i], s.swarmRecaps[i+1:]...)
			break
		}
	}
	s.swarmWatchMu.Unlock()
}

// swarmRecapWaits reports whether m is a recap that this session wrote and
// that has not reached the transcript. A text that a client queued with the
// recap's source is not one.
func (s *wsSession) swarmRecapWaits(m core.QueuedMessage) bool {
	if m.Source != ctrlproto.QueueSourceSwarmRecap {
		return false
	}
	s.swarmWatchMu.Lock()
	defer s.swarmWatchMu.Unlock()
	for _, w := range s.swarmRecaps {
		if w.text == m.Text {
			return true
		}
	}
	return false
}

// swarmRecapLeft records that a recap left the queue for good. Its
// sub-agents leave the state file.
//
// m is the entry as the session queued it. A text that matches no recap of
// this session changes nothing, and a client's own text under the recap's
// source is such a text.
func (s *wsSession) swarmRecapLeft(m core.QueuedMessage) {
	if m.Source != ctrlproto.QueueSourceSwarmRecap {
		return
	}
	var ids []string
	s.swarmWatchMu.Lock()
	for i, w := range s.swarmRecaps {
		if w.text == m.Text {
			ids = w.ids
			s.swarmRecaps = append(s.swarmRecaps[:i], s.swarmRecaps[i+1:]...)
			break
		}
	}
	s.swarmWatchMu.Unlock()
	if len(ids) > 0 {
		s.editSwarmBatch(func(have []string) []string { return withoutIDs(have, ids) })
	}
}

func holdsRecap(queued []core.QueuedMessage, text string) bool {
	for _, q := range queued {
		if q.Source == ctrlproto.QueueSourceSwarmRecap && q.Text == text {
			return true
		}
	}
	return false
}

// swarmRecapsRemoved names the recaps that a client's replacement of the
// queue removed. before and after are the queue on each side of that
// replacement, and the caller holds s.mu across all three steps.
//
// 🔑 A removal is a recap that the queue held before the replacement and does
// not hold after it. A recap that the queue does not hold now proves nothing:
// its writer may not have queued it yet, or it may be the prompt of the turn
// that runs. Both keep their record. s.mu makes the comparison exact, because
// each place that takes a text out of the queue for a turn holds it too.
//
// ⚠️ The loop's own drain does not hold s.mu. A recap that it takes between
// the two readings is read as removed, and it has left the queue for good
// either way.
func (s *wsSession) swarmRecapsRemoved(before, after []core.QueuedMessage) []core.QueuedMessage {
	s.swarmWatchMu.Lock()
	defer s.swarmWatchMu.Unlock()
	var gone []core.QueuedMessage
	for _, w := range s.swarmRecaps {
		if holdsRecap(before, w.text) && !holdsRecap(after, w.text) {
			gone = append(gone, core.QueuedMessage{Text: w.text, Source: ctrlproto.QueueSourceSwarmRecap})
		}
	}
	return gone
}

// restoreSwarmBatch runs once, when the daemon builds the session. It reads
// the record that another daemon left, and queues a recap for it.
//
// 🚨 The recap starts no turn. Nobody is present after a restart, and a turn
// with nobody present is what decision 0002 holds back. The text waits in the
// queue, where a person can read it and remove it. The next turn takes it.
func (s *wsSession) restoreSwarmBatch() {
	if s == nil || s.ws == nil || s.sess == nil || s.agent == nil || s.ws.swarm == nil {
		return
	}
	ids, ok := s.ws.swarmBatchFromBefore(s.sess.StatePath())
	if !ok {
		return
	}
	rows := make([]swarmRecapRow, len(ids))
	unfinished, unknown := 0, 0
	for i, id := range ids {
		rows[i] = s.restartRecapRow(id)
		switch {
		case rows[i].missing:
			unknown++
		case rows[i].stopped:
			unfinished++
		}
	}
	// 🔑 The header claims what the rows show, and no more. A sub-agent with
	// no record may have finished before the retention sweep took it, so it
	// is not counted as work that did not finish.
	var header string
	switch {
	case unfinished > 0:
		header = i18n.P("swarm.restart.header", "[auto-swarm update] terva restarted before the report on %d sub-agent(s) reached this session. %d did not finish:", len(rows), unfinished)
	case unknown > 0:
		header = i18n.P("swarm.restart.header_unknown", "[auto-swarm update] terva restarted before the report on %d sub-agent(s) reached this session:", len(rows))
	default:
		header = i18n.P("swarm.restart.header_all_done", "[auto-swarm update] terva restarted before the report on %d sub-agent(s) reached this session. Each of them finished its task:", len(rows))
	}
	// 🔑 The figures are not booked here. This recap is written again for each
	// start that finds it still waiting, and a batch that finished under the
	// earlier daemon was booked there, when it ended.
	text, _ := swarmRecapText(header, rows, i18n.P("swarm.restart.instruction", "This note reports the state of sub-agents you started. It is not a new user request. A sub-agent that stopped does not start again without a new request. Tell the user which work did not finish, and reference the agents by id. Do not start new sub-agents unless the user asks."))
	// The wait comes first, because the queue is what a turn reads.
	s.awaitSwarmRecap(text, ids)
	if !s.agent.QueueMessageFrom(ctrlproto.QueueSourceSwarmRecap, text) {
		// 🚨 Nothing was queued, so this daemon must not sign the record. A
		// signed record reads as this daemon's own batch, and the next build
		// under this daemon would then pass it by with no report.
		s.forgetSwarmRecap(text)
		s.diag("swarm: the recap for the sub-agents that a restart stopped was not queued")
		return
	}
	// This daemon owns the record now. A second restart before the recap
	// reaches the transcript finds another name, and reports again.
	s.editSwarmBatch(func(ids []string) []string { return ids })
	s.broadcastQueue()
}

// restartRecapRow reads what the swarm kept of one sub-agent. The event log
// says whether the task ran to its end, because the status cannot: a daemon
// that shuts down in good order leaves every child as done.
func (s *wsSession) restartRecapRow(id string) swarmRecapRow {
	a := s.ws.swarm.Get(id)
	if a == nil {
		return swarmRecapRow{id: id, missing: true}
	}
	row := swarmRecapRow{id: id, snap: a.Snapshot(), stopped: true}
	// 🔑 The tasks pane belongs to the workspace, so a person can start a
	// stopped sub-agent again before this session is built. Its log then ends
	// in new work, and "stopped" would be false. The recap says what is true
	// now. It does not join the sub-agent to a batch: a sub-agent that a
	// person started from the pane never had one, and that person watches it
	// there.
	if row.snap.Status == swarm.StatusRunning || row.snap.Status == swarm.StatusPending {
		row.resumed = true
		return row
	}
	if a.EventLogPath == "" {
		return row
	}
	evs, err := swarm.ReadEventLog(a.EventLogPath)
	if err != nil {
		return row
	}
	if errMsg, interrupted, ok := swarm.LastTaskEnd(evs); ok && !interrupted {
		row.stopped = false
		row.turnErr = errMsg
	}
	return row
}

// newBootID names one daemon instance. Eight random bytes are enough to tell
// two instances apart, and the name is no secret.
func newBootID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A clock reading still differs between two starts.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
