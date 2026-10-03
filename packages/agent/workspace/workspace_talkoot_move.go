package workspace

import (
	"context"
	"errors"
	"fmt"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core/permission"
)

// A move runs no git inside the member's worktree.
//
// 🚨 The daemon runs outside the member's sandbox and gate. The mover never
// runs git in the worktree: the member checks out and detaches its branch
// itself, with git in its own bash, which its sandbox and gate cover. enter
// still takes the lease through the worktree engine, whose git worktree add
// runs from the home checkout, as every delivery to a workspace: worktree
// member has done since #1556. write and edit refuse git's own files (#1587),
// so a member cannot plant a hook or filter for that git without an approved
// bash call. (Review of #1572, rounds 1 to 7.)

// talkootMover is the talkoot_workspace tool's binding to one member session.
// It looks the seat up on each call, so a seat that an update took away moves
// nothing.
type talkootMover struct {
	w   *Workspace
	sid string
}

var _ tools.TalkootMover = talkootMover{}

var errNotMovable = errors.New("only a member with workspace: either can move between its worktree and the home checkout")

// movableSeat returns the run and member of a seated native member with
// workspace: either.
func (w *Workspace) movableSeat(sid string) (*talkootRun, talkoot.Member, error) {
	w.talkoot.mu.Lock()
	b := w.talkoot.seats[sid]
	w.talkoot.mu.Unlock()
	if b == nil || b.retired.Load() {
		return nil, talkoot.Member{}, errSeatRevoked
	}
	m, ok := memberOf(*b.run.roster.Load(), b.member)
	if !ok || m.Driver != talkoot.DriverNative || m.Workspace != talkoot.WorkspaceEither {
		return nil, talkoot.Member{}, errNotMovable
	}
	return b.run, m, nil
}

// Move runs one talkoot_workspace action for the bound member.
//
// 🔑 The move rebuilds the session's tools with the new working directory and
// posture, then asks the agent to re-pin (core.Agent.RequestRepin), so the
// next tool call of this same turn runs in the new place.
func (mv talkootMover) Move(_ context.Context, action string) (tools.TalkootWorkspaceStatus, error) {
	w := mv.w
	run, _, err := w.movableSeat(mv.sid)
	if err != nil {
		return tools.TalkootWorkspaceStatus{}, err
	}
	// 🚨 A roster update holds moveMu from the postures it computes to the
	// postures it applies, so a move cannot land between them and leave a
	// write posture in the home checkout. Check the seat again under it.
	run.moveMu.Lock()
	defer run.moveMu.Unlock()
	now, m, err := w.movableSeat(mv.sid)
	if err != nil {
		return tools.TalkootWorkspaceStatus{}, err
	}
	// The lock held is this run's. A seat that moved to another run while we
	// waited is refused, so no move runs under the wrong lock.
	if now != run {
		return tools.TalkootWorkspaceStatus{}, errSeatRevoked
	}
	s := w.existing(mv.sid)
	if s == nil {
		return tools.TalkootWorkspaceStatus{}, errSeatRevoked
	}
	switch action {
	case "enter":
		return mv.enter(run, m, s)
	case "leave":
		return mv.leave(run, m, s)
	}
	return w.moveStatus(run, m.ID), nil
}

func (mv talkootMover) enter(run *talkootRun, m talkoot.Member, s *wsSession) (tools.TalkootWorkspaceStatus, error) {
	w := mv.w
	// Stand down any release still waiting, under the lock a release checks
	// under. A release that already gave the worktree back is followed by a
	// fresh acquire below.
	run.workerMu.Lock()
	w.talkoot.mu.Lock()
	was := run.inside[m.ID]
	nextUnleaseLocked(run, m.ID)
	w.talkoot.mu.Unlock()
	run.workerMu.Unlock()
	if was {
		return w.moveStatus(run, m.ID), nil
	}
	// A failed enter keeps any lease the member holds. It goes back at the
	// next run start or roster change, never on the member's word (see leave).
	if err := w.prepareMemberDir(run, m.ID); err != nil {
		return tools.TalkootWorkspaceStatus{}, err
	}
	w.talkoot.mu.Lock()
	dir := run.memberDirs[m.ID]
	w.talkoot.mu.Unlock()
	// Move first, then mark inside. Until the mark, a roster update computes
	// plan for the member, which is the safe side of the window.
	if err := mv.moveSession(s, dir, m.Posture); err != nil {
		return tools.TalkootWorkspaceStatus{}, err
	}
	w.talkoot.mu.Lock()
	if run.inside == nil {
		run.inside = map[string]bool{}
	}
	run.inside[m.ID] = true
	w.talkoot.mu.Unlock()
	return w.moveStatus(run, m.ID), nil
}

// leave moves the member home. It checks nothing in the worktree, because that
// would run git, and it keeps the lease, so an enter later returns to the same
// worktree.
//
// 🚨 A release runs the worktree engine's Reclaim, which runs git status in
// the worktree from the daemon. So the lease goes back only at a run start or
// a roster change, which the person and the daemon trigger, never on the
// member's own word. (Review of #1572, round 5.)
func (mv talkootMover) leave(run *talkootRun, m talkoot.Member, s *wsSession) (tools.TalkootWorkspaceStatus, error) {
	w := mv.w
	w.talkoot.mu.Lock()
	inside := run.inside[m.ID]
	w.talkoot.mu.Unlock()
	if !inside {
		return w.moveStatus(run, m.ID), nil
	}
	// Out of the worktree in the posture before the mark goes, so a roster
	// update in between still computes plan or finds the member inside.
	if err := mv.moveSession(s, w.cwd, string(permission.ApprovalPlan)); err != nil {
		return tools.TalkootWorkspaceStatus{}, err
	}
	w.talkoot.mu.Lock()
	delete(run.inside, m.ID)
	w.talkoot.mu.Unlock()
	return w.moveStatus(run, m.ID), nil
}

// moveSession points the session at dir in posture, rebuilds its tools, and
// asks the agent to re-pin them for the rest of the turn.
//
// It does not call setApproval, which writes the posture after the gate in
// one fixed order. A move needs the order to depend on the direction, and the
// directory written with the posture.
//
// 🚨 The posture is parsed before anything changes, and the directory and
// the posture go into the args together, so no rebuild sees one without the
// other. A move to plan tightens the gate first. A move out of plan loosens
// it last.
func (mv talkootMover) moveSession(s *wsSession, dir, posture string) error {
	mode, err := permission.ParseApprovalMode(posture)
	if err != nil {
		return fmt.Errorf("talkoot: posture %q after a move: %w", posture, err)
	}
	tighten := mode == permission.ApprovalPlan
	if tighten && s.gate != nil {
		s.gate.SetMode(mode)
	}
	s.mu.Lock()
	s.args.CWD, s.args.Approval, s.cwd = dir, posture, dir
	s.mu.Unlock()
	if !tighten && s.gate != nil {
		s.gate.SetMode(mode)
	}
	s.rebuildTools("talkoot-move")
	if s.agent != nil {
		s.agent.RequestRepin()
	}
	return nil
}

// moveStatus reports where member works now.
func (w *Workspace) moveStatus(run *talkootRun, member string) tools.TalkootWorkspaceStatus {
	w.talkoot.mu.Lock()
	inside, dir := run.inside[member], run.memberDirs[member]
	w.talkoot.mu.Unlock()
	if !inside || dir == "" {
		return tools.TalkootWorkspaceStatus{Dir: w.cwd}
	}
	return tools.TalkootWorkspaceStatus{InWorktree: true, Dir: dir}
}
