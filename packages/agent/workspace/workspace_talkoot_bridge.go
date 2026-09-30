package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/mcpbridge"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/agent/worker"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/permission"
	"terva.sh/terva/packages/provider"
)

// errWorkerUnseated is what a worker's seat-tool call reads when no member of
// the talkoot holds the worker's seat any more.
var errWorkerUnseated = errors.New("this worker no longer holds a seat in the talkoot")

// workerTeam is the seat a talkoot's worker reaches through the Talkoot MCP
// bridge (decision 0023). Nil for a worker outside a talkoot.
//
// 🔑 The socket the runner serves this on is the worker's alone, so the call
// names no member. Each call finds the member that holds the worker's seat
// now, and a worker whose member left or was reseated speaks for nobody.
func (w *Workspace) workerTeam(a *swarm.Agent) worker.Team {
	id, ok := ctrlproto.TalkootFromAddr(a.SessionID)
	if !ok {
		return nil
	}
	agentID := a.ID
	return func(ctx context.Context, tool string, input json.RawMessage) mcpbridge.TeamReply {
		text, isErr, err := w.talkootWorkerCall(ctx, id, agentID, tool, input)
		if err != nil {
			return mcpbridge.TeamReply{Text: err.Error(), IsError: true}
		}
		return mcpbridge.TeamReply{Text: text, IsError: isErr}
	}
}

// bridgeCallSeq numbers bridge calls, so each has a call id for its card,
// its audit line, and its post hooks.
var bridgeCallSeq atomic.Uint64

// talkootWorkerCall runs one seat tool for the member that holds worker
// agentID's seat in talkoot id. isErr is the tool result's own error mark.
//
// 🔑 The call takes a native member's ladder: the daemon's pre-tool-use hooks,
// then the policy a native member in the same posture holds, with a card in
// the talkoot's inbox when it asks, then the native tool over a workerSeat,
// then the post-tool-use hooks. The worker's harness pre-approves the seat
// tools, so this is their one gate, and a rule or a hook the person wrote for
// talkoot_send holds for a Claude Code member as it holds for a native one.
// ⚠️ Extension intercepts do not run. A worker has no session, and so no
// extension manager.
func (w *Workspace) talkootWorkerCall(ctx context.Context, id, agentID, tool string, input json.RawMessage) (text string, isErr bool, err error) {
	// The bridge lists these tools alone. The check here holds whatever a
	// bridge process sends.
	if !slices.Contains(talkoot.BridgeTools, tool) {
		return "", false, fmt.Errorf("the talkoot bridge does not serve %q", tool)
	}
	run, member, ok := w.talkootWorker(ctx, id, agentID, nil)
	if !ok {
		return "", false, errWorkerUnseated
	}
	seat := workerSeat{ctx: ctx, w: w, run: run, member: member, agentID: agentID}
	var t core.Tool
	if tool == "ask_user_question" {
		t = &tools.AskUserTool{Asker: &workerAsker{seat: seat}}
	}
	for _, c := range tools.TalkootTools(seat) {
		if c.Name() == tool {
			t = c
		}
	}
	if t == nil {
		return "", false, fmt.Errorf("no talkoot tool is named %q", tool)
	}
	gate, err := w.workerGate(id, run, member, agentID)
	if err != nil {
		return "", false, err
	}
	call := provider.ToolCallBlock{ID: fmt.Sprintf("bridge-%s-%d", agentID, bridgeCallSeq.Add(1)), Name: tool, Arguments: input}
	allowed, reason, mod := build.BuildToolGate(w.hookEng, gate, nil).CheckTool(ctx, call, t)
	if !allowed {
		if reason == "" {
			reason = "tool call refused"
		}
		return "", false, errors.New(reason)
	}
	if mod != nil {
		input = mod
	}
	w.hookEng.Observe("tool_call", call.ID, tool, input, false)
	res, err := t.Execute(ctx, input, nil)
	w.hookEng.Observe("tool_result", call.ID, "", nil, err != nil || res.IsError)
	if err != nil {
		return "", false, err
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String(), res.IsError, nil
}

// workerGate is the confirm gate of a worker member's seat-tool call: the
// policy a native member in the same posture holds, with the user's and the
// project's rules and the member rule, and the talkoot's inbox as its
// approver.
//
// 🚨 It fails closed. A config that cannot be read would take the person's
// rules with it, so the call is refused rather than run on the posture alone.
func (w *Workspace) workerGate(id string, run *talkootRun, member, agentID string) (*permission.ConfirmGate, error) {
	roster := run.roster.Load()
	m, ok := memberOf(*roster, member)
	if !ok {
		return nil, errWorkerUnseated
	}
	cwd := roster.Home
	if cwd == "" {
		cwd = w.cwd
	}
	pol, _, err := permissions.LoadPolicy(permissions.Inputs{CWD: cwd, Approval: m.Posture, TalkootMember: true})
	if err != nil {
		return nil, fmt.Errorf("the talkoot cannot check this call: %w", err)
	}
	if pol == nil {
		// The member rule makes a policy for every member, so nil is a
		// failure here and not the yolo fast path.
		return nil, errors.New("the talkoot cannot check this call: no permission policy")
	}
	return permission.NewPolicyGate(pol, &talkootConfirmer{w: w, talkoot: id, agentID: agentID}), nil
}

// workerSeat is a worker member's seat for one bridge call. It implements the
// team verbs of tools.TalkootSeat. The rest refuse, because the bridge does not
// serve them (talkoot.BridgeTools).
type workerSeat struct {
	ctx     context.Context
	w       *Workspace
	run     *talkootRun
	member  string
	agentID string
}

var errNotBridged = errors.New("the talkoot bridge does not serve this tool")

// Send routes an envelope from the worker's member. A handoff that names a
// ticket meets the check a native member's does, which refuses a claim for a
// member on another driver.
func (s workerSeat) Send(o talkoot.Outgoing) (talkoot.Envelope, error) {
	ho, err := s.w.checkTicketHandoff(s.ctx, &seatBinding{run: s.run, member: s.member}, o)
	if err != nil {
		return talkoot.Envelope{}, err
	}
	var e talkoot.Envelope
	err = s.run.do(func(rt *talkoot.Router) (err error) {
		// The seat can move between the lookup and the router. The check
		// under the router call keeps a reseated worker from speaking.
		if !s.holds() {
			return errWorkerUnseated
		}
		e, err = rt.Send(s.member, o)
		return err
	})
	if err != nil {
		return e, err
	}
	return e, ho.move(s.ctx, s.w.cwd, e)
}

// holds reports whether the worker still holds its member's seat.
func (s workerSeat) holds() bool {
	s.w.talkoot.mu.Lock()
	defer s.w.talkoot.mu.Unlock()
	return s.w.talkoot.runs[s.run.id] == s.run && s.run.seats[s.member] == s.agentID
}

func (s workerSeat) Roster() ([]tools.TalkootRosterEntry, error) {
	var out []tools.TalkootRosterEntry
	err := s.run.do(func(rt *talkoot.Router) error {
		if !s.holds() {
			return errWorkerUnseated
		}
		status := map[string]talkoot.Status{}
		for _, st := range rt.Statuses() {
			status[st.Member] = st
		}
		for _, m := range rt.Members() {
			out = append(out, tools.TalkootRosterEntry{Member: m, Status: status[m.ID], Self: m.ID == s.member})
		}
		return nil
	})
	return out, err
}

// recordAnswer records a person's answer to the member's question as a room
// line, and returns the answer: reference that cites it, as
// recordTalkootAnswer does for a member's own session.
func (s workerSeat) recordAnswer(qs []core.UserQuestion, ans []core.UserAnswer) tools.AnswerRecord {
	out := answeredOf(qs, ans)
	var id string
	err := s.run.do(func(rt *talkoot.Router) (err error) {
		if !s.holds() {
			return errWorkerUnseated
		}
		id, err = rt.Answer(s.member, out)
		return err
	})
	if err != nil {
		return tools.AnswerRecord{Err: err}
	}
	return tools.AnswerRecord{Ref: "answer:" + id}
}

func (workerSeat) WriteNote(string, string) (string, error) { return "", errNotBridged }
func (workerSeat) ReadNote(string) (string, error)          { return "", errNotBridged }
func (workerSeat) ListNotes() ([]talkoot.Note, error)       { return nil, errNotBridged }
func (workerSeat) Propose([]talkoot.Op, string, string) (talkoot.Proposal, error) {
	return talkoot.Proposal{}, errNotBridged
}

// workerQuestionWait is mcpbridge.TeamQuestionWait. A test shortens it.
var workerQuestionWait = mcpbridge.TeamQuestionWait

// workerAsker is ask_user_question's asker for a worker member's bridge call.
// The question opens as an ask card on the talkoot's carrier, as the worker's
// approvals do (talkootConfirmer), so talkoot.inbox lists it and
// Workspace.Answer on the talkoot's address answers it. The answer becomes a
// room line from the member, and the member reads its answer: reference, as a
// native member does.
type workerAsker struct{ seat workerSeat }

var _ tools.CitedAsker = (*workerAsker)(nil)

func (a *workerAsker) Ask(ctx context.Context, qs []core.UserQuestion) ([]core.UserAnswer, error) {
	ans, _, err := a.AskCited(ctx, qs)
	return ans, err
}

func (a *workerAsker) AskCited(ctx context.Context, qs []core.UserQuestion) ([]core.UserAnswer, tools.AnswerRecord, error) {
	if len(qs) == 0 {
		return nil, tools.AnswerRecord{}, nil
	}
	s := a.seat
	carrier, member, ok := s.w.talkootWorkerSeat(ctx, s.run.id, s.agentID)
	if !ok || member != s.member {
		return nil, tools.AnswerRecord{}, errWorkerUnseated
	}
	req := ctrlproto.NewAskRequest(fmt.Sprintf("worker-%s-ask-%d", s.agentID, talkootAskSeq.Add(1)), qs)
	ch, release, ok := carrier.askPark.Park(req.AskID)
	if !ok {
		return nil, tools.AnswerRecord{}, fmt.Errorf("the ask id %s is already waiting", req.AskID)
	}
	carrier.openAskAs(req, openCard{at: time.Now(), talkoot: s.run.id, member: member})
	defer func() {
		release()
		carrier.closeAsk(req.AskID)
		carrier.broadcast(ctrlproto.AskResolvedEvent(req.AskID))
	}()
	carrier.broadcast(ctrlproto.AskEvent(req))
	wait := time.NewTimer(workerQuestionWait)
	defer wait.Stop()
	select {
	case ans := <-ch:
		ans = core.PadAnswers(ans, len(qs))
		return ans, s.recordAnswer(qs, ans), nil
	case <-ctx.Done():
		// The bridge hung up, or the worker stopped. No one waits for the
		// answer, so the card closes.
		return core.PadAnswers(nil, len(qs)), tools.AnswerRecord{}, ctx.Err()
	case <-wait.C:
		return core.PadAnswers(nil, len(qs)), tools.AnswerRecord{}, fmt.Errorf("the person did not answer within %s", workerQuestionWait)
	case <-s.w.ctx.Done():
		return core.PadAnswers(nil, len(qs)), tools.AnswerRecord{}, errors.New("cancelled (session ending)")
	}
}
