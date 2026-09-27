package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
)

// The talkoot verbs, over the Go API in workspace_talkoot_api.go. This file
// converts to the wire types and to wire error codes, and does nothing else.

var _ ctrlproto.TalkootController = (*Workspace)(nil)

func (w *Workspace) Talkoots(ctx context.Context) ([]ctrlproto.TalkootSummary, error) {
	list, err := w.talkootList(ctx)
	if err != nil {
		return nil, talkootWireErr(err, ctrlproto.CodeInternal)
	}
	out := make([]ctrlproto.TalkootSummary, 0, len(list))
	for _, s := range list {
		out = append(out, ctrlproto.TalkootSummary{ID: s.ID, Home: s.Home, Running: s.Running, Problem: s.Problem})
	}
	return out, nil
}

func (w *Workspace) Talkoot(ctx context.Context, p ctrlproto.TalkootRef) (ctrlproto.TalkootView, error) {
	v, err := w.talkootGet(ctx, p.ID)
	if err != nil {
		return ctrlproto.TalkootView{}, talkootWireErr(err, ctrlproto.CodeInternal)
	}
	return w.wireTalkootView(v), nil
}

func (w *Workspace) TalkootRoom(ctx context.Context, p ctrlproto.TalkootRoomParams) (ctrlproto.TalkootRoomPage, error) {
	page, err := w.talkootRoom(ctx, p.ID, p.Before, p.Limit)
	if err != nil {
		return ctrlproto.TalkootRoomPage{}, talkootWireErr(err, ctrlproto.CodeInternal)
	}
	out := ctrlproto.TalkootRoomPage{Lines: make([]ctrlproto.TalkootLine, 0, len(page.Lines)), Next: page.Next, Total: page.Total}
	for _, l := range page.Lines {
		out.Lines = append(out.Lines, wireTalkootLine(l))
	}
	return out, nil
}

func (w *Workspace) CreateTalkoot(ctx context.Context, p ctrlproto.TalkootCreateParams) (ctrlproto.TalkootView, error) {
	v, err := w.talkootCreate(ctx, p.ID, []byte(p.Text))
	if err != nil {
		return ctrlproto.TalkootView{}, talkootWireErr(err, ctrlproto.CodeBadRequest)
	}
	return w.wireTalkootView(v), nil
}

func (w *Workspace) UpdateTalkoot(ctx context.Context, p ctrlproto.TalkootUpdateParams) (ctrlproto.TalkootView, error) {
	v, err := w.talkootUpdate(ctx, p.ID, p.By, []byte(p.Text))
	if err != nil {
		return ctrlproto.TalkootView{}, talkootWireErr(err, ctrlproto.CodeBadRequest)
	}
	return w.wireTalkootView(v), nil
}

func (w *Workspace) PostTalkoot(ctx context.Context, p ctrlproto.TalkootPostParams) (ctrlproto.TalkootEnvelope, error) {
	e, err := w.talkootPost(ctx, p.ID, p.By, p.To, p.Body, p.Refs, p.Thread)
	if err != nil {
		return ctrlproto.TalkootEnvelope{}, talkootWireErr(err, ctrlproto.CodeBadRequest)
	}
	return wireTalkootEnvelope(e), nil
}

func (w *Workspace) PauseTalkoot(ctx context.Context, p ctrlproto.TalkootPauseParams) error {
	return talkootWireErr(w.talkootPause(ctx, p.ID, p.By, p.Member, p.Chain, p.Reason), ctrlproto.CodeBadRequest)
}

func (w *Workspace) ResumeTalkoot(ctx context.Context, p ctrlproto.TalkootResumeParams) error {
	return talkootWireErr(w.talkootResume(ctx, p.ID, p.By, p.Member, p.Chain), ctrlproto.CodeBadRequest)
}

func (w *Workspace) TalkootInbox(ctx context.Context, p ctrlproto.TalkootRef) (ctrlproto.TalkootInboxResult, error) {
	cards, err := w.talkootInbox(ctx, p.ID)
	if err != nil {
		return ctrlproto.TalkootInboxResult{}, talkootWireErr(err, ctrlproto.CodeInternal)
	}
	return ctrlproto.TalkootInboxResult{Cards: cards}, nil
}

func (w *Workspace) ProposeTalkoot(ctx context.Context, p ctrlproto.TalkootProposeParams) (ctrlproto.TalkootProposal, error) {
	if !talkoot.ValidPerson(strings.TrimPrefix(p.By, talkoot.HumanPrefix)) {
		return ctrlproto.TalkootProposal{}, ctrlproto.Wrap(ctrlproto.CodeBadRequest,
			fmt.Errorf("talkoot: %q must name a person in 1 to 64 letters, digits, and . _ @ -", p.By))
	}
	out, err := w.talkootPropose(p.ID, humanBy(p.By), wireOps(p.Ops), p.Undo, p.Why, nil)
	if err != nil {
		return ctrlproto.TalkootProposal{}, talkootWireErr(err, ctrlproto.CodeBadRequest)
	}
	return wireProposal(out), nil
}

func (w *Workspace) TalkootProposals(ctx context.Context, p ctrlproto.TalkootProposalsParams) (ctrlproto.TalkootProposalsResult, error) {
	list, err := w.talkootProposals(p.ID, p.All)
	if err != nil {
		return ctrlproto.TalkootProposalsResult{}, talkootWireErr(err, ctrlproto.CodeInternal)
	}
	out := ctrlproto.TalkootProposalsResult{Proposals: make([]ctrlproto.TalkootProposal, 0, len(list))}
	for _, pr := range list {
		out.Proposals = append(out.Proposals, wireProposal(pr))
	}
	return out, nil
}

func (w *Workspace) DecideTalkoot(ctx context.Context, p ctrlproto.TalkootDecideParams) (ctrlproto.TalkootProposal, error) {
	out, err := w.talkootDecide(ctx, p.ID, p.By, p.Proposal, p.Decision, wireOps(p.Ops), p.Reason)
	if err != nil {
		return ctrlproto.TalkootProposal{}, talkootWireErr(err, ctrlproto.CodeBadRequest)
	}
	return wireProposal(out), nil
}

// talkootWireErr gives a talkoot error its wire code. An error the API does
// not name takes fallback: bad_request on a verb whose errors are almost all a
// caller's input (a roster that fails its rules, a send the router refuses),
// internal on a read.
func talkootWireErr(err error, fallback string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrTalkootNotFound), errors.Is(err, ErrTalkootNotHere):
		return ctrlproto.Wrap(ctrlproto.CodeNotFound, err)
	case errors.Is(err, ErrTalkootClosed), errors.Is(err, ErrTalkootExists), errors.Is(err, talkoot.ErrProposalStale):
		return ctrlproto.Wrap(ctrlproto.CodeConflict, err)
	case errors.Is(err, talkoot.ErrDisabled):
		return ctrlproto.Wrap(ctrlproto.CodeUnsupported, err)
	}
	return ctrlproto.Wrap(fallback, err)
}

func (w *Workspace) wireTalkootView(v talkootView) ctrlproto.TalkootView {
	r := v.Roster
	out := ctrlproto.TalkootView{
		ID: r.ID, Name: r.Name, Title: r.Title, Home: r.Home, BudgetUSDPerDay: r.BudgetUSDPerDay,
		Text: string(v.Text), Members: make([]ctrlproto.TalkootMember, 0, len(v.Members)), Held: v.Held,
	}
	for _, m := range v.Members {
		mm := m.Member
		out.Members = append(out.Members, ctrlproto.TalkootMember{
			ID: mm.ID, Role: mm.Role, Title: mm.Title, Persona: mm.Persona, Driver: mm.Driver,
			Model: mm.Model, Tier: mm.Tier, Posture: mm.Posture, Workspace: mm.Workspace,
			Reviewer: mm.Reviewer, BudgetUSDPerDay: mm.BudgetUSDPerDay, TurnsPerDay: mm.TurnsPerDay,
			Session: m.Session, Status: wireTalkootStatus(m.Status),
		})
	}
	return out
}

func wireTalkootStatus(s talkoot.Status) ctrlproto.TalkootMemberStatus {
	return ctrlproto.TalkootMemberStatus{Member: s.Member, Working: s.Working, Paused: s.Paused, SpendUSD: s.SpendUSD, Turns: s.Turns}
}

func wireTalkootEnvelope(e talkoot.Envelope) ctrlproto.TalkootEnvelope {
	return ctrlproto.TalkootEnvelope{
		ID: e.ID, Talkoot: e.Talkoot, From: e.From, To: e.To, Kind: string(e.Kind), Body: e.Body,
		Refs: e.Refs, Thread: e.Thread, ReplyTo: e.ReplyTo,
		Chain: ctrlproto.TalkootChain{Root: e.Chain.Root, Hops: e.Chain.Hops}, At: e.At,
	}
}

// wireTalkootLine copies a room line without its seal. The kid and the MAC
// prove the line to the room, and a client has no key to check them with.
func wireTalkootLine(l talkoot.Line) ctrlproto.TalkootLine {
	out := ctrlproto.TalkootLine{
		Type: l.Type, At: l.At, Member: l.Member, Chain: l.Chain, CostUSD: l.CostUSD,
		Guard: l.Guard, Action: l.Action, Reason: l.Reason, By: l.By, SpendUSD: l.SpendUSD,
		Ref: l.Ref, Notes: l.Notes, Proposal: l.Proposal, Proposer: l.Proposer, Edited: l.Edited,
	}
	if len(l.Changes) > 0 {
		out.Changes = wireChanges(l.Changes)
	}
	if l.Envelope != nil {
		e := wireTalkootEnvelope(*l.Envelope)
		out.Envelope = &e
	}
	return out
}

// wireTalkootEvent converts a run's event for the room's address. A loaded
// event has no form there: it rides #workspace as talkoots_changed.
func wireTalkootEvent(ev talkootEvent) (ctrlproto.Event, bool) {
	switch ev.Kind {
	case "envelope":
		if ev.Line != nil {
			return ctrlproto.TalkootEnvelopeEvent(ev.Talkoot, wireTalkootLine(*ev.Line)), true
		}
	case "roster":
		if ev.Line != nil {
			return ctrlproto.TalkootRosterEvent(ev.Talkoot, wireTalkootLine(*ev.Line)), true
		}
	case "status":
		members := make([]ctrlproto.TalkootMemberStatus, 0, len(ev.Status))
		for _, s := range ev.Status {
			members = append(members, wireTalkootStatus(s))
		}
		return ctrlproto.TalkootStatusEvent(ev.Talkoot, members), true
	case "inbox":
		if ev.Wire != nil {
			return *ev.Wire, true
		}
	}
	return ctrlproto.Event{}, false
}

// subscribeTalkoot streams one talkoot's room events. There is no snapshot:
// a client reads the talkoot with talkoot.get and pages the room with
// talkoot.room.
//
// 🔑 Delivery is lossy even for SubscribeReliable. The router's caller sends
// each event, and that caller is often a member's tool call. A consumer that
// stalls must not stall the team, and a consumer that answers an event by
// calling the talkoot would deadlock against the flush that sent it. A missed
// event costs nothing that a re-read does not restore, because the room keeps
// every line.
func (w *Workspace) subscribeTalkoot(ctx context.Context, id string) (<-chan ctrlproto.Event, error) {
	if _, err := w.talkootRunOf(id); err != nil {
		return nil, talkootWireErr(err, ctrlproto.CodeInternal)
	}
	hub := newWSHub()
	ch := hub.add(nil, false)
	stop := w.talkootWatch(id, func(ev talkootEvent) {
		if e, ok := wireTalkootEvent(ev); ok {
			hub.broadcast(e)
		}
	})
	go func() {
		<-ctx.Done()
		stop()
		hub.remove(ch)
	}()
	return ch, nil
}
