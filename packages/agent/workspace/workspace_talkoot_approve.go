package workspace

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core/permission"
)

// A worker member's approvals.
//
// 🔑 A worker member has no session of its own, and the talkoot is where a
// person watches it. Its worker names the talkoot's address as its
// dispatching session, and each run keeps a carrier: a session shell that
// only parks the workers' asks. An ask opens as a permission card through
// openPermission, as a member's own session opens one, so talkoot.inbox lists
// it with the others. Workspace.Approve on the talkoot's address answers it.
//
// 🔑 The carrier is not in the workspace's session map. It has no agent and
// no transcript, and nothing that lists or builds sessions may find it. Its
// address is reserved, so no session file can take its id either.

// newTalkootCarrier builds the carrier of talkoot id.
func (w *Workspace) newTalkootCarrier(id string) *wsSession {
	return &wsSession{
		id:      ctrlproto.TalkootAddr(id),
		ws:      w,
		hub:     newWSHub(),
		permReq: map[string]ctrlproto.PermissionRequest{},
		askReq:  map[string]ctrlproto.AskRequest{},
	}
}

// talkootCarrierOf returns the carrier of talkoot id, or nil when no worker
// of it has asked here.
func (w *Workspace) talkootCarrierOf(id string) *wsSession {
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	return w.talkoot.carriers[id]
}

// talkootCarrierLocked returns the carrier of talkoot id, and builds it on
// first use. The caller holds w.talkoot.mu.
func (w *Workspace) talkootCarrierLocked(id string) *wsSession {
	if c := w.talkoot.carriers[id]; c != nil {
		return c
	}
	if w.talkoot.carriers == nil {
		w.talkoot.carriers = map[string]*wsSession{}
	}
	c := w.newTalkootCarrier(id)
	w.talkoot.carriers[id] = c
	return c
}

// talkootAskSeq numbers the asks of every talkoot worker. A revived worker
// gets a new confirmer, and its asks must not take an id that an ask of its
// earlier process still holds on the carrier.
var talkootAskSeq atomic.Uint64

// talkootConfirmer is the approver of a talkoot's worker.
//
// 🔑 It finds the talkoot's run and the worker's member at each ask, not at
// the worker's start. The runner is built inside the spawn, before the
// binding records the seat, and a run can load again while the swarm keeps
// the worker.
type talkootConfirmer struct {
	w       *Workspace
	talkoot string
	agentID string
}

var _ permission.Confirmer = (*talkootConfirmer)(nil)

func (c *talkootConfirmer) Confirm(ctx context.Context, toolName, preview string) permission.ConfirmDecision {
	carrier, member, ok := c.w.talkootWorkerSeat(ctx, c.talkoot, c.agentID)
	if !ok {
		// 🚨 A worker with no seat was retired, or its talkoot is not running
		// here. No person watches its asks, so each one is refused.
		return permission.ConfirmDecision{Allow: false, Reason: fmt.Sprintf("worker %s holds no seat in talkoot %s", c.agentID, c.talkoot)}
	}
	req := ctrlproto.PermissionRequest{
		CallID:  fmt.Sprintf("worker-%s-%d", c.agentID, talkootAskSeq.Add(1)),
		Tool:    toolName,
		Preview: preview,
		Agent:   c.agentID,
	}
	card := openCard{at: time.Now(), talkoot: c.talkoot, member: member}
	return parkWorkerAsk(ctx, c.w.ctx, carrier, req, func() { carrier.openPermissionAs(req, card) })
}

// talkootWorkerSeat returns the carrier of talkoot id and the member whose
// seat holds worker agentID.
//
// A binding in flight holds run.workerMu from the spawn to the seat's record,
// and the worker can ask in between. The lookup waits for that binding, so a
// new worker's first ask finds its seat. The wait ends early when ctx does:
// a binding can hold the lock while a revived worker's inbox starts.
func (w *Workspace) talkootWorkerSeat(ctx context.Context, id, agentID string) (*wsSession, string, bool) {
	w.talkoot.mu.Lock()
	run := w.talkoot.runs[id]
	w.talkoot.mu.Unlock()
	if run == nil {
		return nil, "", false
	}
	settled := make(chan struct{})
	go func() {
		run.workerMu.Lock()
		run.workerMu.Unlock() //nolint:staticcheck // an empty section waits for the holder
		close(settled)
	}()
	select {
	case <-settled:
	case <-ctx.Done():
		return nil, "", false
	}
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	if w.talkoot.runs[id] != run {
		return nil, "", false
	}
	for member, sid := range run.seats {
		if sid == agentID {
			return w.talkootCarrierLocked(id), member, true
		}
	}
	return nil, "", false
}
