package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/look"
	"terva.sh/terva/packages/agent/persona"
	"terva.sh/terva/packages/agent/swarm"
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
		out = append(out, ctrlproto.TalkootSummary{ID: s.ID, Home: s.Home, Running: s.Running, Problem: s.Problem,
			Name: s.Name, Title: s.Title, Color: s.Color, OwnColor: s.OwnColor, State: s.State})
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

// OpenTalkootRef reads the file a path: or note: reference names, for the
// view to show in place. A path: resolves in the roster's home checkout, and
// a note in the talkoot's notes. Neither follows a link out.
func (w *Workspace) OpenTalkootRef(ctx context.Context, p ctrlproto.TalkootOpenRefParams) (ctrlproto.TalkootRefText, error) {
	run, err := w.talkootRunOf(p.ID)
	if err != nil {
		return ctrlproto.TalkootRefText{}, talkootWireErr(err, ctrlproto.CodeInternal)
	}
	t, err := talkoot.ReadRef(run.dir, run.roster.Load().Home, p.Ref)
	if err != nil {
		return ctrlproto.TalkootRefText{}, talkootWireErr(err, ctrlproto.CodeBadRequest)
	}
	return ctrlproto.TalkootRefText{Ref: t.Ref, Text: t.Text, Size: t.Size, Truncated: t.Truncated, Binary: t.Binary}, nil
}

// TalkootWorker reports the swarm agent a worker member is seated on, for
// the view's event view. The tasks surface cannot serve it: that list is
// scoped to the session asking, and a talkoot's workers belong to the
// talkoot's address instead. The tail holds tool output, so the verb needs
// the write capability (capability.go).
func (w *Workspace) TalkootWorker(ctx context.Context, p ctrlproto.TalkootWorkerParams) (ctrlproto.TaskInfo, error) {
	run, err := w.talkootRunOf(p.ID)
	if err != nil {
		return ctrlproto.TaskInfo{}, talkootWireErr(err, ctrlproto.CodeInternal)
	}
	m, ok := memberOf(*run.roster.Load(), p.Member)
	if !ok {
		return ctrlproto.TaskInfo{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "talkoot: %s has no member %q", p.ID, p.Member)
	}
	if memberDriver(m) == talkoot.DriverNative {
		return ctrlproto.TaskInfo{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "talkoot: member %s is native and has no worker; open its session", p.Member)
	}
	w.talkoot.mu.Lock()
	id := run.seats[p.Member]
	w.talkoot.mu.Unlock()
	if id == "" {
		return ctrlproto.TaskInfo{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "talkoot: member %s has no worker yet; its first delivery starts one", p.Member)
	}
	var snap swarm.AgentSnapshot
	found := false
	if h := w.workers(); h != nil {
		snap, found = h.agentSnapshot(id)
	}
	if !found {
		return ctrlproto.TaskInfo{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "talkoot: worker %s of member %s is not in the swarm", id, p.Member)
	}
	return taskInfo(snap), nil
}

func (w *Workspace) CreateTalkoot(ctx context.Context, p ctrlproto.TalkootCreateParams) (ctrlproto.TalkootView, error) {
	var v talkootView
	var err error
	if p.Template != "" {
		v, err = w.createFromTemplate(ctx, p)
	} else {
		v, err = w.talkootCreate(ctx, p.ID, []byte(p.Text))
	}
	if err != nil {
		return ctrlproto.TalkootView{}, talkootWireErr(err, ctrlproto.CodeBadRequest)
	}
	return w.wireTalkootView(v), nil
}

func (w *Workspace) UpdateTalkoot(ctx context.Context, p ctrlproto.TalkootUpdateParams) (ctrlproto.TalkootView, error) {
	var v talkootView
	var err error
	forms := 0
	for _, set := range []bool{p.Text != "", len(p.Ops) > 0, p.Color != nil} {
		if set {
			forms++
		}
	}
	switch {
	case forms > 1:
		return ctrlproto.TalkootView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "talkoot: an update holds one of the whole text, a list of operations, and a colour")
	case forms == 0:
		return ctrlproto.TalkootView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "talkoot: an update needs the whole text, a list of operations, or a colour")
	case p.Color != nil:
		v, err = w.talkootColor(ctx, p.ID, p.By, *p.Color)
	case len(p.Ops) > 0:
		v, err = w.talkootEdit(ctx, p.ID, p.By, wireOps(p.Ops))
	default:
		v, err = w.talkootUpdate(ctx, p.ID, p.By, []byte(p.Text))
	}
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

// RecruitTalkoot creates a recruiter session through CreateSession, which
// runs the recruiter checks. It is the only door the wire has to one.
func (w *Workspace) RecruitTalkoot(ctx context.Context, p ctrlproto.TalkootRecruitParams) (ctrlproto.SessionInfo, error) {
	if strings.TrimSpace(p.ID) == "" {
		return ctrlproto.SessionInfo{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "talkoot: name the talkoot to recruit for")
	}
	return w.CreateSession(ctx, ctrlproto.CreateOpts{Recruit: p.ID, Persona: p.Persona})
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
	out, err := w.talkootPropose(p.ID, humanBy(p.By), wireOps(p.Ops), p.Undo, p.Why, "", nil)
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

func (w *Workspace) KickoffTalkoot(ctx context.Context, p ctrlproto.TalkootKickoffParams) (ctrlproto.TalkootKickoff, error) {
	out, err := w.talkootKickoff(ctx, p.ID, p.By, p.Skip)
	if err != nil {
		return ctrlproto.TalkootKickoff{}, talkootWireErr(err, ctrlproto.CodeBadRequest)
	}
	return out, nil
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
	case errors.Is(err, ErrTalkootClosed), errors.Is(err, ErrTalkootExists), errors.Is(err, talkoot.ErrProposalStale),
		errors.Is(err, ErrKickoffRan), errors.Is(err, ErrPreviewStale):
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
		Color: look.TeamColor(r.ID, r.Color), OwnColor: r.Color,
		Text: string(v.Text), Members: make([]ctrlproto.TalkootMember, 0, len(v.Members)), Held: v.Held,
	}
	members := make([]talkoot.Member, 0, len(v.Members))
	statuses := make([]talkoot.Status, 0, len(v.Members))
	for _, m := range v.Members {
		members = append(members, m.Member)
		statuses = append(statuses, m.Status)
	}
	out.State = talkoot.TeamState(statuses)
	marks := talkootMarks(members)
	for _, m := range v.Members {
		mm := m.Member
		out.Members = append(out.Members, ctrlproto.TalkootMember{
			ID: mm.ID, Role: mm.Role, Title: mm.Title, Persona: mm.Persona, Driver: mm.Driver,
			Model: mm.Model, Tier: mm.Tier, Posture: mm.Posture, Workspace: mm.Workspace,
			Reviewer: mm.Reviewer, BudgetUSDPerDay: mm.BudgetUSDPerDay, TurnsPerDay: mm.TurnsPerDay, Tools: mm.Tools, IdleStop: mm.IdleStop,
			Mark: ctrlproto.TalkootMark(marks[mm.ID]), OwnMark: wireMark(mm.Mark),
			Session: m.Session, Status: wireTalkootStatus(m.Status),
		})
	}
	return out
}

// talkootMarks gives each member its whole mark. A member's own mark wins,
// then its persona's mark and accent colour, then a shape and colour from its
// id (look.Resolve). A member with no persona runs as the default persona, so
// its default comes from that one.
func talkootMarks(members []talkoot.Member) map[string]look.Mark {
	type resolved struct {
		key    string
		source look.Source
	}
	cache := map[string]resolved{}
	// 🔑 Members group by the persona they run, not by how the roster names
	// it. A member with no persona runs the default, and so shares its group
	// with a member that names the default.
	resolve := func(ref string) resolved {
		if r, ok := cache[ref]; ok {
			return r
		}
		var p persona.Persona
		var ok bool
		if ref == "" {
			var err error
			p, err = persona.Resolve("")
			ok = err == nil
		} else {
			p, ok = persona.Lookup(ref)
		}
		r := resolved{key: ref}
		if ok {
			r = resolved{key: p.Namespace + "/" + p.Name, source: look.Source{Mark: p.Mark, Accent: p.AccentColor}}
		}
		cache[ref] = r
		return r
	}
	who := make([]look.Who, 0, len(members))
	for _, m := range members {
		r := resolve(m.Persona)
		w := look.Who{ID: m.ID, Persona: r.key, Source: r.source}
		if m.Mark != nil {
			w.Mark = *m.Mark
		}
		who = append(who, w)
	}
	return look.Resolve(who)
}

// wireMark is a roster mark on the wire, and nil for none.
func wireMark(m *look.Mark) *ctrlproto.TalkootMark {
	if m == nil {
		return nil
	}
	w := ctrlproto.TalkootMark(*m)
	return &w
}

func wireTalkootStatus(s talkoot.Status) ctrlproto.TalkootMemberStatus {
	return ctrlproto.TalkootMemberStatus{Member: s.Member, Presence: s.Presence(), Working: s.Working, Tool: s.Tool, Paused: s.Paused,
		Pauses: s.Pauses, SpendUSD: s.SpendUSD, Turns: s.Turns, Idle: s.Idle,
		Expression: s.Expression, Intensity: s.Intensity, ExpressionCause: s.ExpressionCause}
}

func wireTalkootEnvelope(e talkoot.Envelope) ctrlproto.TalkootEnvelope {
	out := ctrlproto.TalkootEnvelope{
		ID: e.ID, Talkoot: e.Talkoot, From: e.From, To: e.To, Kind: string(e.Kind), Body: e.Body,
		Refs: e.Refs, Thread: e.Thread, ReplyTo: e.ReplyTo,
		Chain: ctrlproto.TalkootChain{Root: e.Chain.Root, Hops: e.Chain.Hops}, At: e.At,
	}
	for _, c := range e.Cites {
		out.Cites = append(out.Cites, ctrlproto.TalkootCitation{Answer: c.Answer, Asker: c.Asker, At: c.At, Answers: wireAnswered(c.Answers)})
	}
	return out
}

func wireAnswered(qs []talkoot.Answered) []ctrlproto.TalkootAnswered {
	out := make([]ctrlproto.TalkootAnswered, 0, len(qs))
	for _, q := range qs {
		out = append(out, ctrlproto.TalkootAnswered{Question: q.Question, Chosen: q.Chosen, Note: q.Note, Declined: q.Declined, Omitted: q.Omitted})
	}
	return out
}

// wireTalkootLine copies a room line without its seal. The kid and the MAC
// prove the line to the room, and a client has no key to check them with.
func wireTalkootLine(l talkoot.Line) ctrlproto.TalkootLine {
	out := ctrlproto.TalkootLine{
		Type: l.Type, At: l.At, Member: l.Member, Chain: l.Chain, CostUSD: l.CostUSD,
		Guard: l.Guard, Action: l.Action, Reason: l.Reason, By: l.By, SpendUSD: l.SpendUSD,
		Ref: l.Ref, Notes: l.Notes, Proposal: l.Proposal, Proposer: l.Proposer, Edited: l.Edited,
		Text: l.Text, Tool: l.Tool, Attempt: l.Attempt, Card: l.Card, Outcome: l.Outcome,
		ColorBefore: l.ColorBefore, ColorAfter: l.ColorAfter,
	}
	if len(l.Changes) > 0 {
		out.Changes = wireChanges(l.Changes)
	}
	if len(l.Answers) > 0 {
		out.Answers = wireAnswered(l.Answers)
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
	case "intro":
		if ev.Line != nil {
			return ctrlproto.TalkootIntroEvent(ev.Talkoot, wireTalkootLine(*ev.Line)), true
		}
	case "answer":
		if ev.Line != nil {
			return ctrlproto.TalkootAnswerEvent(ev.Talkoot, wireTalkootLine(*ev.Line)), true
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
		e := ctrlproto.TalkootStatusEvent(ev.Talkoot, members)
		e.Talkoot.State = talkoot.TeamState(ev.Status)
		return e, true
	case "inbox":
		if ev.Wire != nil {
			return *ev.Wire, true
		}
	case "beat":
		if ev.Beat != nil {
			return ctrlproto.TalkootBeatEvent(ev.Talkoot, wireTalkootBeat(*ev.Beat)), true
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
