package workspace

// A swarm child of work that no person began (TKT-01M7R5QJ).
//
// A native child is a process with a gate of its own, so the rungs of its
// parent's session do not reach it. The daemon marks such a child when a
// turn with no person at its root starts it. The child then puts the same
// two rules on its own gate: the merge rule, which it reads itself, and the
// count of review requests, which it asks the daemon for.
//
// 🚨 The rules steer a model, as they do for the parent. A shell can still
// get past them, and the guard that holds is the entity's forge account.

import (
	"context"
	"encoding/json"
	"errors"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/swarm"
)

// spawnMark returns the mark for a child that this session's turn starts
// now. It is nil for a turn that a person began.
//
// 🔑 It reads the turn at the spawn, as each rung reads it at each call. The
// tool's arguments carry no part of it, so a model cannot set the mark and
// cannot clear it.
func (s *wsSession) spawnMark() *swarm.Unattended {
	u, lone := s.unattended()
	if !lone {
		return nil
	}
	b := u.seat.b
	return &swarm.Unattended{Team: b.run.id, Member: b.member, Chain: u.root}
}

// errChildNoChain is the count's answer for an agent whose record names no
// chain.
var errChildNoChain = errors.New("this sub-agent has no record of the chain that started it")

// childReviews is the daemon's count for the review socket of one child. It
// counts against the routine of the chain in the child's record, as a
// request of the member whose turn started the child.
//
// 🚨 Each failure refuses. A team that the daemon does not run now cannot
// count, and neither can a chain whose routine is gone.
func (w *Workspace) childReviews(a *swarm.Agent) func(n int) string {
	return func(n int) string {
		m := a.Unattended
		if m == nil {
			return reviewVerdict(n, errChildNoChain)
		}
		run, err := w.talkootRunOf(m.Team)
		if err != nil {
			return reviewVerdict(n, err)
		}
		return reviewVerdict(n, chainReviews(run, m.Chain, m.Member, n))
	}
}

// UnattendedChildRungs are the rungs of the gate of a native swarm child
// that has the mark of unattended work. dir is the directory in which the
// child's bash tool starts, and inbox is the child's inbox path, which names
// its review socket.
//
// They are the two rungs of wsSession.rungs with no test of the turn. The
// mark lasts for the child's life, so each of its calls is unattended.
func UnattendedChildRungs(dir, inbox string) []build.Rung {
	return []build.Rung{
		build.CallGuard(func(ctx context.Context, tool string, args json.RawMessage) string {
			return mergeDenial(ctx, dir, tool, args)
		}),
		build.AfterAllow(func(ctx context.Context, tool string, args json.RawMessage) string {
			return reviewRequests(tool, args, func(n int) string {
				deny, err := swarm.AskReviews(ctx, inbox, n)
				if err != nil {
					return reviewVerdict(n, err)
				}
				return deny
			})
		}),
	}
}
