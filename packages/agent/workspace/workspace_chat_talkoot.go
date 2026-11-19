package workspace

// A chat bridge bound to a talkoot.
//
// The bridge in workspace_chat.go mirrors a paired direct room into one
// session. This file gives it a second thing to bind to: a talkoot. The
// person's messages enter the room as their posts, and what the team says to
// that person goes out to the chat. Decision 0029 rule 18 is the reason: the
// coordinator is the entity a person talks to, and a phone is where they are.
//
// 🔑 A chat message never reaches a member's session. It goes through
// talkootPost, so the router sets the sender and the chain and applies the
// guards, exactly as it does for a post from the Talkoot app. The room is the
// record (decision 0022 rule 5), and a prompt written straight into the
// coordinator's session would be a message the room never held.
//
// 🔑 Only an ask card is answered from chat. A permission, a proposal, and a
// kickoff give authority, so the bridge names them and leaves them for the
// app. Decision 0029 rule 21 lets chat grant only from a verified device, and
// nothing here can tell one device from another yet.
//
// 🔑 The bridge serves one person. It carries what that person posted and
// what a member addressed to that person, and nothing of anyone else's. The
// chat sits on a service outside the daemon, and a second person who uses the
// team through the app did not choose to have their words sent there.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// chatTarget says what a bridge serves: one session, or one talkoot. person is
// the name the owner's posts carry in a talkoot's room.
type chatTarget struct {
	sess    string
	talkoot string
	person  string
}

// chatTeamAskWait bounds how long one question stays open in chat. When it
// passes, the chat lets go of the question and the card stays in the inbox.
// The text floor holds the owner's next message for the question, so an
// unbounded wait would hold the thread for as long as nobody answered.
var chatTeamAskWait = 30 * time.Minute

// The waits between dials of a talkoot's bridge that failed to connect.
var (
	chatTeamRedialFirst = 30 * time.Second
	chatTeamRedialMax   = 10 * time.Minute
)

// chatTeamQueueMax bounds the events that wait for the pump. A connector whose
// send hangs stops the pump, and the team goes on talking. Past the bound the
// pump counts what it drops and tells the chat when it runs again.
const chatTeamQueueMax = 1024

// chatTeamEchoMax bounds the ids of chat posts that wait for their own event.
// The pump forgets an id when it handles the envelope, and a dropped event
// never does that.
const chatTeamEchoMax = 256

// errChatTeamUnpaired refuses a talkoot's bridge whose connector has no paired
// account.
//
// 🚨 A session bridge lets the first account that sends /start claim it, and
// a person sits at that session when they connect it. A talkoot's bridge
// starts with the daemon and dials for as long as the daemon runs. So an
// unpaired one would wait, unattended, for whoever found the bot first, and
// that account could then post to the team as the person.
var errChatTeamUnpaired = errors.New("no account is paired with this connector; pair one with `terva bot run` and /start, and stop that bot again")

// AttachTalkootChat binds one configured chat connector to one talkoot and
// returns when the dial is armed. It does not wait for the handshake, and it
// dials again when the handshake fails.
//
// 🔑 That differs from AttachChat on purpose. AttachChat serves a chat agent
// that has no other way in, so a daemon without its connector is a daemon
// that does nothing. A talkoot has the Talkoot app, and the app is where a
// person grants what chat cannot. A homeserver that is down at boot must not
// keep the app down with it.
func (w *Workspace) AttachTalkootChat(id, person, name string) error {
	return w.AttachTalkootChatWith(id, person, name, TalkootChatOptions{})
}

// TalkootChatOptions is what the person set for a talkoot's chat bridge
// beyond which team and which connector.
type TalkootChatOptions struct {
	// Brief holds the times of day the held updates go out, such as 07:30.
	// With none, they go out when the person next writes.
	Brief []string
	// Quiet is the range in which the chat stays quiet, such as 22:00-07:00.
	Quiet string
}

// AttachTalkootChatWith is AttachTalkootChat with the outbound rules. It
// refuses rules that do not read before it dials anything.
func (w *Workspace) AttachTalkootChatWith(id, person, name string, opt TalkootChatOptions) error {
	t := chatTarget{talkoot: strings.TrimSpace(id), person: strings.TrimPrefix(strings.TrimSpace(person), talkoot.HumanPrefix)}
	rules, err := parseChatOutboundRules(opt.Brief, opt.Quiet)
	if err != nil {
		return err
	}
	w.chatOutboundSet(t.talkoot, rules)
	service, ready, err := w.chatConnectTarget(t, name)
	if err != nil {
		return err
	}
	parent := w.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	w.chat.mu.Lock()
	if w.chat.stopKeep != nil {
		w.chat.stopKeep()
	}
	w.chat.stopKeep = cancel
	w.chat.mu.Unlock()
	w.chat.dials.Add(1)
	go w.chatTeamKeep(ctx, t, service, ready)
	return nil
}

// chatTeamKeep dials the talkoot's bridge again, with a growing wait, until a
// dial succeeds, the workspace closes, or a person disconnects the bridge.
// After a first handshake the connector owns its own reconnects.
//
// ctx ends when the bridge is stopped, so a disconnect does not leave this
// waiting out a ten-minute timer.
func (w *Workspace) chatTeamKeep(ctx context.Context, t chatTarget, service string, ready <-chan error) {
	defer w.chat.dials.Done()
	wait := chatTeamRedialFirst
	for {
		// A nil ready means the last dial was refused before it started, so
		// there is no result to wait for.
		if ready != nil {
			select {
			case err := <-ready:
				if err == nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
		wait = min(wait*2, chatTeamRedialMax)

		// The failed dial left its entry behind in the error state. A bridge
		// that someone connected in its place is the signal to stop.
		w.chat.mu.Lock()
		b := w.chat.bridges[service]
		ours := b != nil && b.bridge == nil && b.state == chatStateError && b.talkoot == t.talkoot
		w.chat.mu.Unlock()
		if !ours {
			return
		}
		next, r, err := w.chatConnectTarget(t, service)
		if err != nil {
			// A refusal can pass: a `terva bot` daemon that pairs the account
			// holds the connector for a while. So this waits and asks again.
			w.diagf("talkoot %s: the chat bridge could not dial again: %v", t.talkoot, err)
			ready = nil
			continue
		}
		service, ready = next, r
	}
}

// chatTeamView returns the bridge bound to the talkoot id, for the team's own
// view. It reports false when the workspace's bridge serves something else.
func (w *Workspace) chatTeamView(id string) (ctrlproto.ChatBridgeState, bool) {
	w.chat.mu.Lock()
	var st ctrlproto.ChatBridgeState
	var live *chat.Bridge
	found := false
	for name, b := range w.chat.bridges {
		if b.talkoot == id {
			st = ctrlproto.ChatBridgeState{State: b.state, Connector: name, Talkoot: id, Error: b.err}
			live, found = b.bridge, true
			break
		}
	}
	w.chat.mu.Unlock()
	if live != nil {
		s := live.State()
		st.Username, st.PairedID = s.Username, s.PairedID
	}
	if found {
		st.Outbound = w.chatOutboundView(id)
	}
	return st, found
}

// --- the host ------------------------------------------------------------

// chatTeamHost is the chat.Host of a bridge bound to a talkoot.
type chatTeamHost struct {
	w       *Workspace
	service string
	id      string // the talkoot
	person  string

	// postMu orders a post from chat against the pump's check for its echo.
	// talkootPost sends the envelope's event before it returns the envelope,
	// so without this the pump could mirror a message back to the chat it
	// came from, in the gap before fromChat held its id.
	postMu   sync.Mutex
	fromChat map[string]bool

	bridge *chat.Bridge
}

// policy decides when a message from the team reaches the chat. It is read
// at each use and not kept, so rules that the workspace set again apply to a
// bridge that is already up.
func (h *chatTeamHost) policy() *chatOutbound { return h.w.chatOutboundFor(h.id) }

func (h *chatTeamHost) say(text string) {
	if h.bridge != nil {
		h.bridge.Say("", text)
	}
}

// SubmitOrQueue posts the owner's message to the talkoot. An empty address
// reaches the coordinator.
func (h *chatTeamHost) SubmitOrQueue(text string, _ []provider.ImageBlock) {
	ctx := h.w.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	h.postMu.Lock()
	e, err := h.w.talkootPost(ctx, h.id, h.person, nil, text, nil, "")
	if err == nil {
		// Past the bound the ids are of events the queue dropped, or the pump
		// is far behind. Forgetting them costs an echo at most: the person
		// reads their own message again as "you".
		if h.fromChat == nil || len(h.fromChat) >= chatTeamEchoMax {
			h.fromChat = map[string]bool{}
		}
		h.fromChat[e.ID] = true
	}
	h.postMu.Unlock()
	if err != nil {
		// The person must learn that the team never got the message. A paused
		// team and a tripped guard both refuse a post.
		h.say(i18n.T("the team did not get that: %v", err))
	}
}

// echoed reports whether envelope id is a post this bridge made, and forgets
// it. The pump calls it once for each envelope the person sent.
func (h *chatTeamHost) echoed(id string) bool {
	h.postMu.Lock()
	defer h.postMu.Unlock()
	if h.fromChat[id] {
		delete(h.fromChat, id)
		return true
	}
	return false
}

// CancelTurn answers "stop" from chat by pausing the whole team. A person
// lifts the pause in the Talkoot app.
func (h *chatTeamHost) CancelTurn() {
	ctx := h.w.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := h.w.talkootPause(ctx, h.id, h.person, "", "", i18n.T("stopped from chat")); err != nil {
		h.say(i18n.T("the team did not pause: %v", err))
	}
}

// Status is the reply to /status: the team's state, then a line for each
// member that is working, waiting, or paused.
func (h *chatTeamHost) Status() string {
	ctx := h.w.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	v, err := h.w.Talkoot(ctx, ctrlproto.TalkootRef{ID: h.id})
	if err != nil {
		return i18n.T("the team is unavailable: %v", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s", v.Name, v.State)
	for _, m := range v.Members {
		switch {
		case m.Status.Paused != "":
			fmt.Fprintf(&b, "\n%s: %s", m.ID, i18n.T("paused (%s)", m.Status.Paused))
		case m.Status.Working && m.Status.Tool != "":
			fmt.Fprintf(&b, "\n%s: %s", m.ID, i18n.T("working (%s)", m.Status.Tool))
		case m.Status.Working:
			fmt.Fprintf(&b, "\n%s: %s", m.ID, i18n.T("working"))
		}
	}
	if cards, err := h.w.talkootInbox(ctx, h.id); err == nil {
		open := 0
		for _, c := range cards {
			if c.Kind != ctrlproto.TalkootCardKickoff {
				open++
			}
		}
		if open > 0 {
			fmt.Fprintf(&b, "\n%s", i18n.T("%d waiting in the inbox", open))
		}
	}
	return b.String()
}

// Notify has no session to show a notice in, so a bridge event goes to the
// daemon's log.
func (h *chatTeamHost) Notify(level, message string) {
	h.w.diagf("talkoot %s chat (%s): %s", h.id, level, message)
}

// --- the pump ------------------------------------------------------------

// chatTeamPump carries a talkoot's events out to the chat.
//
// 🔑 The watcher only queues. talkootWatch runs its callback on the goroutine
// that caused the event, which is often a member's tool call, and a callback
// that called back into the talkoot would deadlock against the flush that
// sent it. So the callback appends and returns, and run does the work.
type chatTeamPump struct {
	w    *Workspace
	host *chatTeamHost
	b    *chat.Bridge
	id   string

	mu    sync.Mutex
	queue []talkootEvent
	// dropped counts the events that found the queue full.
	dropped int
	wake    chan struct{}
	// asks holds the cancel of each question open in chat, by card id.
	asks map[string]context.CancelFunc
	wg   sync.WaitGroup

	// team is the team's last state, and settleAt is when to ask whether the
	// work finished. brief and quietEnd say that a timer fired.
	team     string
	settleAt time.Time
	brief    bool
	quietEnd bool
}

// chatTeamSettleWait is how long the team must stay off work before the host
// calls the work finished. One member's turn can end a moment before the
// next member's begins, and that gap is not the end of the work.
var chatTeamSettleWait = 5 * time.Second

// enqueue takes the events the pump acts on, up to chatTeamQueueMax.
//
// ⚠️ The ctrlproto subscription drops an event when a client is slow, and the
// client reads the room again. Nothing reads again for a chat. So a drop here
// is counted, and the chat is told to read the app.
func (p *chatTeamPump) enqueue(ev talkootEvent) {
	if ev.Kind == "status" {
		// A status comes at every tool call, so it is read here and never
		// queued.
		p.teamIs(talkoot.TeamState(ev.Status))
		return
	}
	// Of a routine's lines the chat acts on one: a pause that no person
	// made. The rest never enter the queue, so a routine that skips at each
	// of its times cannot fill it.
	selfPause := ev.Kind == "routine" && ev.Line != nil && ev.Line.Action == talkoot.ActionPaused && ev.Line.By == ""
	if ev.Kind != "envelope" && ev.Kind != "inbox" && !selfPause {
		return
	}
	p.mu.Lock()
	if len(p.queue) >= chatTeamQueueMax {
		p.dropped++
	} else {
		p.queue = append(p.queue, ev)
	}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// chatTeamStart starts the pump for a bridge that just went live, and returns
// it with what stops it.
func (w *Workspace) chatTeamStart(host *chatTeamHost, b *chat.Bridge) (pump *chatTeamPump, stop func()) {
	parent := w.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	p := &chatTeamPump{w: w, host: host, b: b, id: host.id, wake: make(chan struct{}, 1), asks: map[string]context.CancelFunc{}}
	unwatch := w.talkootWatch(host.id, p.enqueue)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.run(ctx)
	}()
	return p, func() {
		unwatch()
		cancel()
		<-done
		p.wg.Wait()
	}
}

func (p *chatTeamPump) run(ctx context.Context) {
	// A question asked before the bridge connected is still a question.
	p.reconcile(ctx)
	for {
		// What quiet hours held goes out once they are over, whether or not a
		// timer said so: a daemon with no timer service still gets here at
		// the next event.
		p.send(p.host.policy().quietEnded(chatNow()))

		p.mu.Lock()
		var settle <-chan time.Time
		if !p.settleAt.IsZero() {
			settle = time.After(time.Until(p.settleAt))
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
		case <-settle:
		}
		p.mu.Lock()
		batch, dropped := p.queue, p.dropped
		brief := p.brief
		p.queue, p.dropped, p.brief, p.quietEnd = nil, 0, false, false
		p.mu.Unlock()
		for _, ev := range batch {
			p.handle(ctx, ev)
		}
		// 🔑 After the batch, never before it. A status sets the settle time
		// at once, and an envelope waits in the queue. The team's last
		// message can still be in the queue when the time comes, and a settle
		// that ran first would send an older update as the last word and
		// leave the real one with nothing to release it.
		p.settled(ctx)
		if dropped > 0 {
			p.reconcile(ctx)
			p.b.Say("", i18n.T("%d updates from the team did not reach this chat. read the room in the Talkoot app.", dropped))
		}
		if brief {
			p.send(p.host.policy().briefCame(chatNow()))
		}
	}
}

// send puts the lines the policy released into the chat.
func (p *chatTeamPump) send(lines []chatLine) {
	for _, l := range lines {
		p.b.Say(l.speaker, l.text)
	}
}

// teamIs records the team's state. When the team stops working, it sets the
// time to ask whether the work finished.
func (p *chatTeamPump) teamIs(state string) {
	p.mu.Lock()
	was := p.team
	p.team = state
	switch {
	case state == talkoot.TeamBusy:
		p.settleAt = time.Time{}
	case was == talkoot.TeamBusy:
		p.settleAt = time.Now().Add(chatTeamSettleWait)
	}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// settled runs when the wait after the team stopped working is over. The
// work finished when the team is still off work and no member waits on a
// person: a member that waits has a question out, and the work goes on when
// it is answered.
func (p *chatTeamPump) settled(ctx context.Context) {
	p.mu.Lock()
	due := !p.settleAt.IsZero() && !time.Now().Before(p.settleAt)
	if due {
		p.settleAt = time.Time{}
	}
	p.mu.Unlock()
	if !due {
		return
	}
	v, err := p.w.Talkoot(ctx, ctrlproto.TalkootRef{ID: p.id})
	if err != nil || v.State == talkoot.TeamBusy || v.State == talkoot.TeamNeedsYou {
		return
	}
	p.send(p.host.policy().settled(chatNow()))
}

// timerFired records that the brief time or the end of quiet hours came, and
// wakes the pump. A timer's handler must return at once, and a send to a chat
// server can take as long as the network does.
func (p *chatTeamPump) timerFired(brief bool) {
	p.mu.Lock()
	if brief {
		p.brief = true
	} else {
		p.quietEnd = true
	}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// reconcile makes the questions in chat match the inbox. It poses each
// question that is open and not yet in chat, and it withdraws each chat
// question whose card is gone.
//
// 🚨 The pump runs it after the queue dropped events. One of them can be the
// event that says the app settled a card. A question left open in chat after
// that would take the person's next message as its answer, and that message
// would reach nobody.
//
// Other cards are left for the app, unnamed: naming every old card again
// would be noise.
func (p *chatTeamPump) reconcile(ctx context.Context) {
	cards, err := p.w.talkootInbox(ctx, p.id)
	if err != nil {
		return
	}
	open := map[string]bool{}
	for _, c := range cards {
		if c.Kind == ctrlproto.TalkootCardAsk {
			open[c.ID] = true
		}
	}
	p.mu.Lock()
	var gone []string
	for id := range p.asks {
		if !open[id] {
			gone = append(gone, id)
		}
	}
	p.mu.Unlock()
	for _, id := range gone {
		p.resolved(id)
	}
	for _, c := range cards {
		if c.Kind == ctrlproto.TalkootCardAsk {
			p.pose(ctx, c)
		}
	}
}

func (p *chatTeamPump) handle(ctx context.Context, ev talkootEvent) {
	switch ev.Kind {
	case "envelope":
		if ev.Line != nil && ev.Line.Envelope != nil {
			p.envelope(ctx, *ev.Line.Envelope)
		}
	case "routine":
		if ev.Line != nil {
			p.routine(ctx, *ev.Line)
		}
	case "inbox":
		if ev.Wire == nil || ev.Wire.Talkoot == nil || ev.Wire.Talkoot.Card == nil {
			return
		}
		card := *ev.Wire.Talkoot.Card
		switch ev.Wire.Type {
		case ctrlproto.EventTalkootInbox:
			p.card(ctx, card)
		case ctrlproto.EventTalkootInboxResolved:
			p.resolved(card.ID)
		}
	}
}

// envelope sends what the bridge's person should read. A member's envelope
// goes out when it is addressed to that person. The person's own post from
// the app goes out as "you", so the chat holds both sides. A post that came
// from this chat is not sent back, and nothing of another person's goes out.
func (p *chatTeamPump) envelope(ctx context.Context, e talkoot.Envelope) {
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return
	}
	me := talkoot.HumanPrefix + p.host.person
	if strings.HasPrefix(e.From, talkoot.HumanPrefix) {
		if e.From == me {
			if !p.host.echoed(e.ID) {
				p.b.Say(i18n.T("you"), body)
			}
			// The person is in the conversation now, from chat or from the app.
			p.send(p.host.policy().personPosted(chatNow()))
		}
		return
	}
	if talkoot.IsUnansweredNote(e) {
		p.unanswered(ctx, e)
		return
	}
	if !slices.Contains(e.To, me) {
		return
	}
	// The coordinator is the voice of the thread, so it speaks bare. Any other
	// member is named, because the person did not address it.
	speaker := e.From
	if v, err := p.w.Talkoot(ctx, ctrlproto.TalkootRef{ID: p.id}); err == nil {
		for _, m := range v.Members {
			if m.ID == e.From && m.Role == talkoot.RoleCoordinator {
				speaker = ""
			}
		}
	}
	p.send(p.host.policy().memberSaid(chatNow(), e.ID, e.Chain.Root, speaker, body))
}

// approvedByMe reports whether the bridge's person approved the routine, and
// returns the routine's member.
//
// 🚨 A routine's notes go to the person who approved it, and to nobody else
// (TKT-01M7QFVHC). Another person's routine is that person's to resume. A
// routine that does not read, or that a person deleted, names nobody.
func (p *chatTeamPump) approvedByMe(ctx context.Context, routine string) (member string, mine bool) {
	v, err := p.w.Talkoot(ctx, ctrlproto.TalkootRef{ID: p.id})
	if err != nil || v.Routines == nil {
		return "", false
	}
	for _, r := range v.Routines.Routines {
		if r.ID == routine {
			return r.Member, r.ApprovedBy == talkoot.HumanPrefix+p.host.person
		}
	}
	return "", false
}

// routine tells the person that a routine paused itself. A run that failed
// pauses its routine, and so does a run that passed the routine's daily
// spend limit. The routine then starts no run until a person resumes it.
//
// 🔑 It goes out at once, in quiet hours too. A question that nobody hears
// parks one turn. A routine that nobody knows is paused does nothing at each
// of its times, for as long as nobody looks (decision 0029 rule 22).
//
// A pause that a person made has that person's name on its line, and the
// chat says nothing of it: the person knows.
func (p *chatTeamPump) routine(ctx context.Context, l talkoot.Line) {
	if l.Action != talkoot.ActionPaused || l.By != "" {
		return
	}
	member, mine := p.approvedByMe(ctx, l.Ref)
	if !mine {
		return
	}
	p.b.Say("", i18n.T("a routine of %s paused itself: %s. it starts no run until you resume it in the Talkoot app.", member, l.Reason))
}

// unanswered puts a run's note into the next brief: the run ended at a
// question that nobody answered. The chat already said that the question
// closed. The note holds the question and what the run left undone.
func (p *chatTeamPump) unanswered(ctx context.Context, e talkoot.Envelope) {
	member, mine := p.approvedByMe(ctx, strings.TrimPrefix(e.From, talkoot.RoutinePrefix))
	if !mine {
		return
	}
	// The question goes first. A held text is cut at its limit, and the
	// question is what the person must read.
	asked, left := talkoot.UnansweredNoteParts(e)
	text := i18n.T("a run of a routine of %s ended at a question that nobody answered: %s", member, asked)
	if left != "" {
		text += i18n.T(" what the run left: %s", left)
	}
	p.host.policy().forBrief(chatNow(), e.ID, text)
}

// card poses an ask in chat, and names any other card as waiting in the app.
//
// 🔑 An ask and an approval go out at once, in quiet hours too. A member's
// turn waits on each, and a question nobody hears parks the work until it
// expires (decision 0029 rule 12). No turn waits on a proposal or a kickoff,
// so quiet hours hold those.
//
// The chat gets the member's name and where to decide. It never gets the
// tool call: a raw tool card in a chat thread is the noise rule 22 exists to
// stop, and it could carry a command's arguments to a service outside the
// daemon.
func (p *chatTeamPump) card(ctx context.Context, c ctrlproto.TalkootCard) {
	now := chatNow()
	switch c.Kind {
	case ctrlproto.TalkootCardAsk:
		p.pose(ctx, c)
	case ctrlproto.TalkootCardPermission:
		p.b.Say("", i18n.T("%s waits for an approval. open the Talkoot app to decide.", c.Member))
	case ctrlproto.TalkootCardProposal:
		text := i18n.T("%s proposed a change to the team. open the Talkoot app to decide.", c.Member)
		if c.Proposal != nil && c.Proposal.Routine != nil {
			// 🔑 A routine's approval is a grant, and chat cannot tell a
			// verified device from another yet (decision 0029 rule 21). So
			// the chat names the card, and the app decides it.
			text = i18n.T("%s asks for a routine. open the Talkoot app to decide.", c.Member)
		}
		p.send(p.host.policy().notice(now, c.ID, outDecision, text))
	case ctrlproto.TalkootCardKickoff:
		p.send(p.host.policy().notice(now, c.ID, outDecision, i18n.T("the team waits for its kickoff. open the Talkoot app to start it.")))
	}
}

// pose asks one card's questions in chat, on its own goroutine, and answers
// the card with what the person said.
//
// 🚨 Nothing here answers for the person. A question that expires, or that
// the person skips, leaves the card open in the inbox. Decision 0029 rule 12
// says the host never answers for the person, and a declined answer written
// here would settle the card for the app as well.
func (p *chatTeamPump) pose(ctx context.Context, c ctrlproto.TalkootCard) {
	if c.Ask == nil {
		return
	}
	askCtx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	if _, open := p.asks[c.ID]; open {
		p.mu.Unlock()
		cancel()
		return
	}
	p.asks[c.ID] = cancel
	p.mu.Unlock()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer cancel()
		// AskQuestions poses one set at a time. A set that the app settles
		// while it waits behind another never reached the chat, so the chat
		// has nothing to be told about it.
		posed := false
		answers, err := p.b.AskQuestions(askCtx, c.Ask.Set(), chat.QuestionOpts{
			From:           c.Member,
			Timeout:        chatTeamAskWait,
			TimeoutOutcome: i18n.T("no answer here. the question waits in the Talkoot app."),
			OnPosed:        func() { posed = true },
		})
		// Whoever removes the entry owns the outcome. resolved removes it when
		// the card closed somewhere else, and then this goroutine says so and
		// answers nothing. A card closes when a person answers it in the app,
		// when the member's turn ends, and when its deadline passes in a
		// chain that a routine began. So the notice names all three.
		p.mu.Lock()
		_, mine := p.asks[c.ID]
		delete(p.asks, c.ID)
		p.mu.Unlock()
		switch {
		case err == nil && mine:
			// The app can settle the card after the person replied here and
			// before this line. Then the reply was not used, and the person
			// who wrote it must know.
			if !p.w.answerDelivered(c.Session, c.ID, answers) {
				p.b.Say("", i18n.T("that question was already settled, so your answer was not used."))
			}
		case !mine && posed && ctx.Err() == nil:
			p.b.Say("", i18n.T("that question is closed. it was answered in the Talkoot app, the member's turn ended, or its deadline passed."))
		case errors.Is(err, chat.ErrAskSkipped):
			p.b.Say("", i18n.T("left for the Talkoot app."))
		}
	}()
}

// resolved withdraws a chat question whose card was settled somewhere else.
func (p *chatTeamPump) resolved(cardID string) {
	p.mu.Lock()
	cancel, open := p.asks[cardID]
	delete(p.asks, cardID)
	p.mu.Unlock()
	if open {
		cancel()
	}
}

// answerDelivered is Answer, and it reports whether a waiting question took
// the answer. False means the question was already settled, or is gone.
func (w *Workspace) answerDelivered(sess, askID string, answers []core.UserAnswer) bool {
	if id, ok := ctrlproto.TalkootFromAddr(sess); ok {
		if c := w.talkootCarrierOf(id); c != nil {
			return c.answer(askID, answers)
		}
		return false
	}
	if s := w.live(sess); s != nil {
		return s.answer(askID, answers)
	}
	return false
}

var _ chat.Host = (*chatTeamHost)(nil)
