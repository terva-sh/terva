package workspace

import (
	"errors"
	"io/fs"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/session"
)

var _ ctrlproto.TalkootSessions = (*Workspace)(nil)

// SteersTalkoot reports whether a talkoot drives the session id names: a
// member's session, or a recruiter's. The dispatch steer rule asks it before a
// caller without steer may write to or spend on the session (TKT-01M3FAJC).
//
// A member's session stays the team's after an unseat, and one that another
// process runs is the team's too, so the answer reads every member session
// the room lines name rather than only the seats this process holds.
//
// An empty id names the session an empty id resolves to. That is the marked
// session after a planned restart, else the latest person session, and the
// answer asks about both, so it never depends on which one a verb picks.
func (w *Workspace) SteersTalkoot(id string) bool {
	if id != "" {
		return w.steersTalkoot(id)
	}
	w.mu.Lock()
	marked := w.markedSessionID()
	w.mu.Unlock()
	if marked != "" && w.steersTalkoot(marked) {
		return true
	}
	if p := w.latestPersonSession(); p != "" {
		return w.steersTalkoot(build.SessionIDFromPath(p))
	}
	return false
}

func (w *Workspace) steersTalkoot(id string) bool {
	w.talkoot.foldOnce.Do(w.foldTalkootMembers)
	if w.isTalkootMember(id) {
		return true
	}
	// 🔑 Another process can seat a member after the first fold, so a miss
	// folds again. A room that did not grow costs a stat and is not read.
	w.foldTalkootMembers()
	if w.isTalkootMember(id) {
		return true
	}
	if s := w.existing(id); s != nil {
		return s.recruitOf() != ""
	}
	// 🔑 A cold recruiter is still bound: its meta names the talkoot, and a
	// resume rebuilds it with the recruiter's tools. An id that names no file
	// has nothing to steer, and the verb answers for itself.
	if !validSessionID(id) {
		return false
	}
	_, meta, err := session.ReadSessionMeta(w.sessionPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	// 🚨 A file that exists and does not read could hold a recruit binding,
	// so it counts as bound. Answering false would fail open.
	return err != nil || meta.Recruit != ""
}

func (w *Workspace) isTalkootMember(id string) bool {
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	return w.talkoot.members[id]
}
