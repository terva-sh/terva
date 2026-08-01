package chat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/attach"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// Loop is the standalone chat daemon: it owns a core.Agent, receives
// messages from one Connector, runs them as agent turns (queueing
// behind an in-flight turn), and replies with the final assistant
// text, chunked to the connector's limit. This is the chat-ops loop
// formerly hand-written inside the telegram bot; it knows nothing
// about any particular service.
type Loop struct {
	Connector Connector
	Agent     *core.Agent

	// Status context shown for /status.
	Provider   string
	AuthMethod string
	CWD        string

	// Service names the chat service ("telegram", "discord", an
	// extension connector's name) for the one-time chat-context line
	// each conversation's first prompt carries. Empty reads as "chat".
	Service string

	// RefreshCreds is called before every turn to pick up newly
	// refreshed OAuth tokens. Optional.
	RefreshCreds func() error

	// Pairing is the first-user-claims policy state.
	Pairing Pairing

	// Admissions is the approved-group store (gate v2). nil keeps
	// every non-DM chat silent.
	Admissions *Admissions

	// NewChatAgent, when set, turns on per-chat sessions (v2 stage C):
	// DM conversations keep Agent (and its persisted session); every
	// other chat gets its own lazily-created agent — same model and
	// system prompt, separate transcript — so a busy group cannot
	// pollute your DM's context or vice versa. nil = the v1 behavior,
	// every chat sharing Agent.
	NewChatAgent func() *core.Agent
	// MaxChatAgents bounds the live per-chat agents (LRU; least
	// recently used is dropped, transcript and all). 0 means 8.
	MaxChatAgents int

	// HelpText answers /help; PairedText (with %s = username)
	// acknowledges a claim. Both have service-neutral defaults.
	HelpText   string
	PairedText string

	// IdleAfter, when > 0, turns on the proactive idle nudge: if the paired
	// chat has been silent this long (no inbound message, no running turn), the
	// loop injects IdleNudge as a synthetic prompt so the agent (in its persona)
	// can break the silence — start a conversation, ask a question, comment. It
	// fires once per silence and re-arms only when the paired user speaks again,
	// so it nudges, it does not nag. The reactive loop is unchanged when 0.
	IdleAfter time.Duration
	// IdleNudge is the synthetic prompt injected on an idle nudge. Empty uses a
	// neutral default. The persona shapes the voice; this just supplies the cue.
	IdleNudge string
	// IdlePoll is how often the idle watcher checks; 0 derives it from IdleAfter.
	IdlePoll time.Duration

	// Info and Warn receive operational log lines. Default to stdout
	// and stderr respectively.
	Info func(string)
	Warn func(string)

	mu           sync.Mutex
	busy         bool
	activeCancel context.CancelFunc
	queue        []Message
	lastCtxInput int
	// proactive-nudge bookkeeping (guarded by mu)
	lastActivity time.Time
	pairedChatID string // chat to nudge into; learned from inbound, seeded from pairing
	armed        bool   // a real user message re-arms; a nudge disarms
	// ask bookkeeping (guarded by mu)
	ownerID        string          // current paired user; tracks runtime claims
	activeChatID   string          // chat of the running turn ("" between turns)
	activeChatKind string          // its kind; owner asks avoid non-DM origins
	activeMsgID    string          // message that started the running turn
	textAsk        *pendingTextAsk // outstanding text-fallback ask, one at a time
	admissionAsked map[string]bool // membership asks fired, per chat (never nag)
	chatAgents     map[string]*chatAgentState
	clientSwap     func(*core.Agent)   // applied to every live + future agent on cred refresh
	pendingNotes   map[string][]string // per-chat stage-D notes, ridden by the next prompt
	introduced     map[string]bool     // chats whose first prompt carried the [chat context] line
	// recentTexts remembers the last text seen per consumed message
	// (bounded ring), so edit events that change nothing — Discord
	// fires MESSAGE_UPDATE when an embed unfurls, with the content
	// untouched — don't become "the user edited a message" notes.
	recentTexts    map[string]string
	recentTextsSeq []string
}

// maxPendingNotes caps the per-chat note backlog; the oldest note
// drops first (they are courtesy context, not a ledger).
const maxPendingNotes = 5

// chatAgentState is one non-DM chat's private conversation.
type chatAgentState struct {
	agent    *core.Agent
	lastUsed time.Time
	ctxInput int // last observed context size, for per-chat /status
}

// pendingTextAsk is one text-fallback ask waiting for the next
// matching message from an allowed responder.
type pendingTextAsk struct {
	ask     Ask
	answers chan Answer // buffered(1); first valid answer wins
}

func (l *Loop) info(s string) {
	if l.Info != nil {
		l.Info(s)
		return
	}
	fmt.Println(s)
}

func (l *Loop) warn(s string) {
	if l.Warn != nil {
		l.Warn(s)
		return
	}
	fmt.Fprintln(os.Stderr, s)
}

// Run drives the loop. Blocks until ctx cancels or the connector
// fails permanently.
func (l *Loop) Run(ctx context.Context) error {
	// Admission events (stage B): install before Connect so nothing is
	// missed; each event runs on its own goroutine because the
	// handler contract forbids blocking the delivery path and the
	// admission ask blocks by design.
	if ms, ok := l.Connector.(MembershipHandlerSetter); ok {
		ms.SetMembershipHandler(func(mb Membership) {
			go l.onMembership(ctx, mb)
		})
	}
	// Stage-D inbound events, with the tabled host defaults: edits
	// rewrite still-queued prompts in place, deletions withdraw them,
	// and everything that arrives too late becomes a short note the
	// chat's NEXT prompt carries (a deviation from the sketch's
	// "inject a system note", recorded in the proposal: a note that
	// starts its own turn would let edit spam drive the agent).
	if es, ok := l.Connector.(ChatEventsSetter); ok {
		es.SetChatEventHandlers(ChatEventHandlers{
			Edited:   l.onMessageEdited,
			Deleted:  l.onMessageDeleted,
			Reaction: l.onReaction,
		})
	}

	id, err := l.Connector.Connect(ctx)
	if err != nil {
		return fmt.Errorf("%s: connect: %w", l.Connector.Name(), err)
	}
	l.info(fmt.Sprintf("%s bridge online as @%s (id=%s)", l.Connector.Name(), id.Username, id.ID))
	if l.Pairing.AllowedUserID == "" {
		l.info("no user paired yet — send /start to the bot to claim it")
	} else {
		l.info(fmt.Sprintf("paired with %s user id %s", l.Connector.Name(), l.Pairing.AllowedUserID))
	}

	help := l.HelpText
	if help == "" {
		help = "send me any message and i'll forward it to terva. attach an image and i'll pass it to the model. commands: /status, /stop, or plain stop."
	}
	paired := l.PairedText
	if paired == "" {
		paired = "paired with @%s. send any message and i'll forward it to terva."
	}
	g := &gate{pairing: l.Pairing, admissions: l.Admissions,
		botUsername: id.Username, helpText: help, pairedText: paired}
	// Track runtime claims so asks restrict to the CURRENT owner, not
	// the pre-Run snapshot.
	g.onPaired = func(m Message) {
		l.mu.Lock()
		l.ownerID = m.UserID
		l.mu.Unlock()
	}

	// Arm the proactive idle nudge. Seed the paired chat from pairing (for a
	// DM connector the chat id is the user id) so the bot can open a
	// conversation even before the user has spoken; a real inbound corrects it.
	l.mu.Lock()
	l.lastActivity = time.Now()
	l.armed = true
	l.ownerID = l.Pairing.AllowedUserID
	if l.pairedChatID == "" {
		l.pairedChatID = l.Pairing.AllowedUserID
	}
	l.mu.Unlock()
	if l.IdleAfter > 0 {
		go l.watchIdle(ctx)
	}

	return l.Connector.Receive(ctx, func(m Message) {
		switch g.route(ctx, l.Connector, m) {
		case actStatus:
			l.noteActivity(m)
			l.sendStatus(ctx, m)
		case actStop:
			l.noteActivity(m)
			l.cancelActiveTurn(ctx, m)
		case actPrompt:
			l.noteActivity(m)
			// A pending text-fallback ask gets first claim on the
			// message; anything that isn't an answer flows on as a
			// prompt (asks must not eat conversation).
			if l.takeTextAnswer(m) {
				return
			}
			l.enqueue(ctx, m)
		}
	})
}

// noteActivity records a real interaction from the paired user: it re-arms the
// idle nudge, refreshes the idle clock, and learns the owner's DM.
// Only DM chats are adopted — the idle nudge and owner-directed asks
// must never wander into a group the owner happened to speak in.
func (l *Loop) noteActivity(m Message) {
	l.mu.Lock()
	l.lastActivity = time.Now()
	l.armed = true
	if m.ChatID != "" && isDM(m.ChatKind) {
		l.pairedChatID = m.ChatID
	}
	l.mu.Unlock()
}

// watchIdle is the proactive-nudge ticker. It runs only when IdleAfter > 0.
func (l *Loop) watchIdle(ctx context.Context) {
	poll := l.IdlePoll
	if poll <= 0 {
		poll = l.IdleAfter / 5
	}
	if poll < 10*time.Millisecond {
		poll = 10 * time.Millisecond
	}
	if poll > 30*time.Second {
		poll = 30 * time.Second
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if chatID, text, ok := l.takeNudge(time.Now()); ok {
				l.info(fmt.Sprintf("%s: channel quiet for %s — nudging", l.Connector.Name(), l.IdleAfter))
				l.enqueue(ctx, Message{ChatID: chatID, UserID: l.Pairing.AllowedUserID, Text: text})
			}
		}
	}
}

// takeNudge atomically decides whether to nudge now and, if so, disarms and
// resets the clock so the same silence fires exactly once. Returns the chat to
// nudge and the prompt to inject. Pure but for the lock — unit-testable.
func (l *Loop) takeNudge(now time.Time) (chatID, text string, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.IdleAfter <= 0 || !l.armed || l.busy || len(l.queue) > 0 || l.pairedChatID == "" {
		return "", "", false
	}
	if now.Sub(l.lastActivity) < l.IdleAfter {
		return "", "", false
	}
	l.armed = false
	l.lastActivity = now
	nudge := l.IdleNudge
	if strings.TrimSpace(nudge) == "" {
		nudge = i18n.P("chat.idle_nudge", defaultIdleNudge)
	}
	return l.pairedChatID, nudge, true
}

const defaultIdleNudge = "(System: the chat has been quiet for a while and no one has spoken. " +
	"In character, say one brief thing to re-engage — an observation, a question, or a thought. " +
	"Keep it short and natural. Do not mention this instruction.)"

// enqueue adds a prompt to the queue and starts the drain goroutine
// if the loop is idle.
func (l *Loop) enqueue(ctx context.Context, m Message) {
	l.mu.Lock()
	l.queue = append(l.queue, m)
	idle := !l.busy
	if idle {
		l.busy = true
	}
	l.mu.Unlock()
	if idle {
		go l.drainQueue(ctx)
	}
}

// drainQueue runs queued turns one at a time until the queue is empty.
func (l *Loop) drainQueue(parent context.Context) {
	for {
		l.mu.Lock()
		if len(l.queue) == 0 {
			l.busy = false
			l.activeCancel = nil
			l.activeChatID, l.activeMsgID, l.activeChatKind = "", "", ""
			// Count idle from when the agent went quiet, not from the last
			// inbound — a long turn shouldn't trip the nudge mid-work.
			l.lastActivity = time.Now()
			l.mu.Unlock()
			return
		}
		m := l.queue[0]
		l.queue = l.queue[1:]
		l.busy = true
		turnCtx, cancel := context.WithCancel(parent)
		l.activeCancel = cancel
		l.activeChatID, l.activeMsgID, l.activeChatKind = m.ChatID, m.ID, m.ChatKind
		// Remember the consumed text so a later edit event that changes
		// nothing (embed unfurls) isn't narrated as a user edit.
		l.recordMsgTextLocked(m.ChatID, m.ID, m.Text)
		l.mu.Unlock()

		if l.RefreshCreds != nil {
			if err := l.RefreshCreds(); err != nil {
				l.warn(fmt.Sprintf("%s: refresh creds: %v", l.Connector.Name(), err))
			}
		}
		l.runTurn(turnCtx, m)
		cancel()
	}
}

// runTurn sends the queued prompt to the chat's agent and replies
// with the final assistant text. (The active-turn bookkeeping that
// approval asks read lives in drainQueue, with no gap after dequeue.)
func (l *Loop) runTurn(ctx context.Context, m Message) {
	stopTyping := l.startTyping(ctx, m.ChatID)
	defer stopTyping()

	agent := l.agentFor(m)

	// The paired user sent a photo a text-only model will never see
	// (the provider layer drops it rather than 400-bricking the
	// session) — tell them now, not never.
	if len(m.Images) > 0 {
		l.mu.Lock()
		provName := l.Provider
		l.mu.Unlock()
		if mdl, err := provider.FindModel(provName, agent.Model); err == nil && !mdl.Has(provider.CapImageInput) {
			_ = l.Connector.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID,
				Text: fmt.Sprintf("note: %s can't see images; only your text reaches it.", agent.Model)})
		}
	}

	var replyBuilder strings.Builder
	var lastAssistantText string
	var turnErr error

	sink := func(ev core.AgentEvent) {
		switch e := ev.(type) {
		case core.EvTextDelta:
			replyBuilder.WriteString(e.Delta)
		case core.EvUsage:
			if e.Usage.InputTokens > 0 {
				l.recordCtx(m, e.Usage.InputTokens+e.Usage.CacheReadTokens+e.Usage.CacheWriteTokens)
			}
		case core.EvAssistantMessage:
			var sb strings.Builder
			for _, c := range e.Message.Content {
				if tb, ok := c.(provider.TextBlock); ok {
					if sb.Len() > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString(tb.Text)
				}
			}
			if sb.Len() > 0 {
				lastAssistantText = sb.String()
			}
			replyBuilder.Reset()
		case core.EvTurnEnd:
			if e.Err != nil {
				turnErr = e.Err
			}
		}
	}

	// PromptWithPolicy adds the core turn policy (pre-turn compaction
	// for an over-threshold transcript, one compact-and-retry on HTTP
	// 413). Surface compactions to the paired chat as a short notice
	// so the longer-than-usual pause reads as work, not a hang.
	defer cleanupFiles(m)
	if err := agent.PromptWithPolicy(ctx, l.promptText(m), m.Images, func(ev core.AgentEvent) {
		if cs, ok := ev.(core.EvCompactStart); ok {
			_ = l.Connector.Send(ctx, Outgoing{ChatID: m.ChatID,
				Text: "note: condensing conversation history (" + cs.Reason + ") ..."})
		}
		sink(ev)
	}); err != nil {
		turnErr = err
	}

	// Post-turn housekeeping for this long-lived session: condense
	// after a clean turn that pushed context past the threshold so the
	// paired user's NEXT message doesn't pay the latency. Failures are
	// non-fatal — the turn itself succeeded.
	if turnErr == nil && ctx.Err() == nil && agent.ShouldAutoCompact(core.AutoCompactThreshold) && agent.CanCompact(core.AutoCompactKeepTail) {
		// Non-fatal, but not silent. The paired user is told when a compaction
		// starts (the EvCompactStart notice above), and a discarded error here
		// meant the one that ran on their behalf after the turn could fail with
		// nothing said at all — leaving the next message to pay a latency, or hit
		// a limit, that had already been diagnosed and thrown away.
		if _, cerr := agent.Compact(ctx, core.AutoCompactKeepTail, nil); cerr != nil &&
			!errors.Is(cerr, context.Canceled) && !errors.Is(cerr, core.ErrNothingToCompact) {
			_ = l.Connector.Send(ctx, Outgoing{ChatID: m.ChatID,
				Text: i18n.T("note: could not condense conversation history (%s); the next message may be slower.", cerr.Error())})
		}
	}

	reply := strings.TrimSpace(lastAssistantText)
	if reply == "" {
		reply = strings.TrimSpace(replyBuilder.String())
	}
	if turnErr != nil && ctx.Err() == nil {
		reply = "error: " + turnErr.Error()
	}
	if reply == "" {
		reply = "(no reply)"
	}
	for _, chunk := range ChunkMessage(reply, l.Connector.Capabilities().MaxTextLen) {
		// context.Background(): the reply must go out even when the
		// turn was cancelled or the parent is shutting down.
		if err := l.Connector.Send(context.Background(), Outgoing{ChatID: m.ChatID, Text: chunk}); err != nil {
			l.warn(fmt.Sprintf("%s: send: %v", l.Connector.Name(), err))
			break
		}
	}
}

// startTyping keeps the service's typing indicator alive until the
// returned stop function is called. No-op for connectors without one.
func (l *Loop) startTyping(ctx context.Context, chatID string) func() {
	refresh := l.Connector.Capabilities().TypingRefresh
	if refresh <= 0 {
		return func() {}
	}
	tctx, cancel := context.WithCancel(ctx)
	go func() {
		for {
			_ = l.Connector.Typing(tctx, chatID)
			select {
			case <-tctx.Done():
				return
			case <-time.After(refresh):
			}
		}
	}()
	return cancel
}

// UpdateStatusContext swaps the provider identity shown by /status.
// Used by credential refresh, which can re-resolve the provider while
// turns and status requests run concurrently.
func (l *Loop) UpdateStatusContext(provider, authMethod, cwd string) {
	l.mu.Lock()
	l.Provider = provider
	l.AuthMethod = authMethod
	l.CWD = cwd
	l.mu.Unlock()
}

// cancelActiveTurn is per-chat (stage C): /stop cancels the running
// turn only when it belongs to the chat it was said in, and drops that
// chat's queued prompts either way. Other chats' work is untouchable
// from here — one chat must not be able to kill another's turn.
func (l *Loop) cancelActiveTurn(ctx context.Context, m Message) {
	l.mu.Lock()
	var cancel context.CancelFunc
	if l.activeCancel != nil && l.activeChatID == m.ChatID {
		cancel = l.activeCancel
	}
	kept := l.queue[:0]
	var purgedMsgs []Message
	for _, qm := range l.queue {
		if qm.ChatID == m.ChatID {
			purgedMsgs = append(purgedMsgs, qm)
			continue
		}
		kept = append(kept, qm)
	}
	l.queue = kept
	l.mu.Unlock()
	purged := len(purgedMsgs)
	for _, qm := range purgedMsgs {
		cleanupFiles(qm)
	}

	switch {
	case cancel != nil:
		cancel()
		_ = l.Connector.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID, Text: "cancelled the current turn."})
	case purged > 0:
		_ = l.Connector.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID,
			Text: fmt.Sprintf("dropped %d queued message(s); nothing was running here.", purged)})
	default:
		_ = l.Connector.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID, Text: "nothing running in this chat."})
	}
}

// sendStatus describes THIS chat's state to the paired user: its own
// agent's context and cost, whether the running turn is its own, and
// how many of the queued prompts are its own (stage C).
func (l *Loop) sendStatus(ctx context.Context, m Message) {
	l.mu.Lock()
	busy := l.busy && l.activeChatID == m.ChatID
	queued := 0
	for _, qm := range l.queue {
		if qm.ChatID == m.ChatID {
			queued++
		}
	}
	ctxUsed := l.lastCtxInput
	agent := l.Agent
	if st, ok := l.chatAgents[m.ChatID]; ok {
		ctxUsed = st.ctxInput
		agent = st.agent
	}
	providerName := l.Provider
	authMethod := l.AuthMethod
	cwd := l.CWD
	l.mu.Unlock()

	model := agent.Model
	ctxMax := 0
	if mdl, err := provider.FindModel(providerName, model); err == nil {
		ctxMax = mdl.ContextWindow
	}
	_ = l.Connector.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID, Text: FormatStatus(StatusSnapshot{
		Provider:     providerName,
		Model:        model,
		CWD:          cwd,
		Usage:        agent.Cost(),
		Subscription: authMethod == "oauth",
		ContextUsed:  ctxUsed,
		ContextMax:   ctxMax,
		Busy:         busy,
		Queued:       queued,
	})})
}

// Ask poses one constrained question in a chat and returns the first
// valid answer, fail-closed on timeout. Connectors that render asks
// natively (Capabilities().Asks) get the real widget — buttons, inline
// keyboards — with the platform's identity attestation; everything
// else falls back to a numbered plain-text question answered by the
// next matching message, best-effort by definition. Approvals over
// chat therefore work with EVERY connector; capability only upgrades
// the UX and the attestation.
func (l *Loop) Ask(ctx context.Context, a Ask) (Answer, error) {
	if asker, ok := l.Connector.(Asker); ok && l.Connector.Capabilities().Asks {
		return asker.Ask(ctx, a)
	}
	return l.textFallbackAsk(ctx, a)
}

// AskTarget returns where an owner-directed ask should go: the chat
// that started the running turn when that chat is the owner's DM
// (threaded to its message), else the owner's DM — a group-originated
// turn must not surface approval questions (and their tool previews)
// to the whole group. Empty when nothing is paired yet.
func (l *Loop) AskTarget() (chatID, replyTo string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.activeChatID != "" && isDM(l.activeChatKind) {
		return l.activeChatID, l.activeMsgID
	}
	return l.pairedChatID, ""
}

// OwnerID returns the currently paired user id, tracking runtime
// claims. Empty while unpaired.
func (l *Loop) OwnerID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ownerID
}

// textFallbackAsk renders the ask as a numbered plain-text question
// and waits for takeTextAnswer to consume a matching reply. One at a
// time: approvals serialize at the caller, and a second concurrent
// ask would make the reply ambiguous.
func (l *Loop) textFallbackAsk(ctx context.Context, a Ask) (Answer, error) {
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = DefaultAskTimeout
	}
	p := &pendingTextAsk{ask: a, answers: make(chan Answer, 1)}
	l.mu.Lock()
	if l.textAsk != nil {
		l.mu.Unlock()
		return Answer{}, fmt.Errorf("another ask is already waiting for an answer")
	}
	l.textAsk = p
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		if l.textAsk == p {
			l.textAsk = nil
		}
		l.mu.Unlock()
	}()

	var b strings.Builder
	b.WriteString(a.Text)
	b.WriteString("\n")
	for i, o := range a.Options {
		fmt.Fprintf(&b, "\n%d — %s", i+1, o.Label)
	}
	b.WriteString("\n\nreply with a number to answer.")
	if err := l.Connector.Send(ctx, Outgoing{ChatID: a.ChatID, ReplyTo: a.ReplyTo, Text: b.String()}); err != nil {
		return Answer{}, err
	}

	select {
	case ans := <-p.answers:
		return ans, nil
	case <-time.After(timeout):
		outcome := a.TimeoutOutcome
		if outcome == "" {
			outcome = "expired (no answer)"
		}
		// The withdrawal must be visible even when the turn is being
		// torn down — same posture as the reply sender.
		_ = l.Connector.Send(context.Background(), Outgoing{ChatID: a.ChatID, Text: outcome})
		return Answer{}, ErrAskTimeout
	case <-ctx.Done():
		return Answer{}, ctx.Err()
	}
}

// takeTextAnswer consumes m as the answer to the pending text ask
// when it is one: same chat, allowed responder, and text matching an
// option by number, key, or label. Anything else returns false and
// the message flows on as a normal prompt.
func (l *Loop) takeTextAnswer(m Message) bool {
	l.mu.Lock()
	p := l.textAsk
	l.mu.Unlock()
	if p == nil || m.ChatID != p.ask.ChatID {
		return false
	}
	if len(p.ask.RestrictTo) > 0 && !containsUserID(p.ask.RestrictTo, m.UserID) {
		return false
	}
	key, ok := matchAskOption(p.ask.Options, m.Text)
	if !ok {
		return false
	}
	select {
	case p.answers <- Answer{Key: key, UserID: m.UserID, Username: m.Username,
		Attestation: AttestationBestEffort}:
	default:
	}
	return true
}

// matchAskOption resolves a reply against the options: the 1-based
// number, the key, or the label, case-insensitively.
func matchAskOption(opts []AskOption, text string) (string, bool) {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return "", false
	}
	if n, err := strconv.Atoi(t); err == nil && n >= 1 && n <= len(opts) {
		return opts[n-1].Key, true
	}
	for _, o := range opts {
		if t == strings.ToLower(o.Key) || t == strings.ToLower(o.Label) {
			return o.Key, true
		}
	}
	return "", false
}

func containsUserID(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// onMembership reacts to the bot's own admission changing. Being
// added to an unapproved chat fires ONE ask in the owner's DM — the
// trust hook that replaces a command the owner has to know; being
// removed revokes any standing approval (a chat we were kicked from
// must not stay approved for a future re-add).
func (l *Loop) onMembership(ctx context.Context, mb Membership) {
	if l.Admissions == nil {
		return
	}
	switch mb.Change {
	case "removed":
		// Revoke the whole container first — a guild kick must drop every channel
		// approved under it, not just the one this event names — then the named
		// chat if it survived (scopeless approvals aren't caught by scope).
		revoked, _ := l.Admissions.RevokeScope(mb.ScopeID)
		if _, approved := l.Admissions.Mode(mb.ChatID); approved {
			_ = l.Admissions.Revoke(mb.ChatID)
			revoked++
		}
		if revoked > 0 {
			l.info(fmt.Sprintf("%s: removed from %s — revoked %d approval(s)", l.Connector.Name(), describeChat(mb), revoked))
		}
		return
	case "added":
	default:
		return
	}
	if _, approved := l.Admissions.Mode(mb.ChatID); approved {
		return
	}

	l.mu.Lock()
	owner := l.ownerID
	ownerDM := l.pairedChatID
	if l.admissionAsked == nil {
		l.admissionAsked = map[string]bool{}
	}
	asked := l.admissionAsked[mb.ChatID]
	l.admissionAsked[mb.ChatID] = true
	l.mu.Unlock()
	if owner == "" || ownerDM == "" || asked {
		return
	}

	by := mb.ByUsername
	if by == "" {
		by = mb.ByUserID
	}
	if by == "" {
		by = "someone"
	}
	ans, err := l.Ask(ctx, Ask{
		ChatID: ownerDM,
		Text:   fmt.Sprintf("i was added to %s by @%s. approve it?", describeChat(mb), by),
		Options: []AskOption{
			{Key: "approve", Label: "Approve (mention-only)", Style: "affirm"},
			{Key: "approve_all", Label: "Approve (all messages)"},
			{Key: "ignore", Label: "Ignore", Style: "deny"},
		},
		RestrictTo:     []string{owner},
		TimeoutOutcome: "ignored — the chat stays silent",
	})
	if err != nil {
		return // fail closed: the chat stays silent; /approve still works
	}
	switch ans.Key {
	case "approve", "approve_all":
		mode := ModeMention
		how := "when mentioned"
		if ans.Key == "approve_all" {
			mode, how = ModeAll, "on every message"
		}
		if err := l.Admissions.ApproveScoped(mb.ChatID, mode, mb.ScopeID); err != nil {
			_ = l.Connector.Send(ctx, Outgoing{ChatID: ownerDM,
				Text: "couldn't save the approval: " + err.Error()})
			return
		}
		_ = l.Connector.Send(ctx, Outgoing{ChatID: ownerDM,
			Text: fmt.Sprintf("approved — i'll respond in %s %s.", describeChat(mb), how)})
	default:
		// Ignored: stays silent, and the dedupe map keeps us from
		// asking again this run. /approve in the chat still works.
	}
}

// describeChat renders a membership event's chat for humans.
func describeChat(mb Membership) string {
	kind := mb.ChatKind
	if kind == "" {
		kind = "chat"
	}
	if mb.ChatTitle != "" {
		return fmt.Sprintf("the %s %q", kind, mb.ChatTitle)
	}
	return fmt.Sprintf("%s %s", kind, mb.ChatID)
}

// agentFor resolves the agent that owns m's conversation: the primary
// Agent for DMs (and for everything, when per-chat sessions are off),
// a lazily-minted per-chat agent otherwise. Minting past the cap
// drops the least recently used chat's agent, transcript and all — a
// returning chat starts fresh (recorded deviation from the proposal's
// "compacts and drops": nothing persists, so compaction buys nothing).
func (l *Loop) agentFor(m Message) *core.Agent {
	if l.NewChatAgent == nil || isDM(m.ChatKind) {
		return l.Agent
	}
	l.mu.Lock()
	if l.chatAgents == nil {
		l.chatAgents = map[string]*chatAgentState{}
	}
	if st, ok := l.chatAgents[m.ChatID]; ok {
		st.lastUsed = time.Now()
		l.mu.Unlock()
		return st.agent
	}
	max := l.MaxChatAgents
	if max <= 0 {
		max = 8
	}
	var evicted []string
	for len(l.chatAgents) >= max {
		lruID, lruT := "", time.Time{}
		for id, st := range l.chatAgents {
			if lruID == "" || st.lastUsed.Before(lruT) {
				lruID, lruT = id, st.lastUsed
			}
		}
		delete(l.chatAgents, lruID)
		evicted = append(evicted, lruID)
	}
	agent := l.NewChatAgent()
	if l.clientSwap != nil {
		l.clientSwap(agent)
	}
	l.chatAgents[m.ChatID] = &chatAgentState{agent: agent, lastUsed: time.Now()}
	l.mu.Unlock()
	for _, id := range evicted {
		l.info(fmt.Sprintf("%s: chat %s idle longest — dropped its context to make room", l.Connector.Name(), id))
	}
	return agent
}

// recordCtx notes a turn's observed context size where the chat's
// /status will find it.
func (l *Loop) recordCtx(m Message, total int) {
	l.mu.Lock()
	if st, ok := l.chatAgents[m.ChatID]; ok {
		st.ctxInput = total
	} else {
		l.lastCtxInput = total
	}
	l.mu.Unlock()
}

// SetClientAndModel swaps the provider client and model on the
// primary agent and every live per-chat agent, and remembers the swap
// for agents minted later — a credential refresh must reach every
// conversation, including ones that don't exist yet.
func (l *Loop) SetClientAndModel(client provider.Client, model string) {
	l.mu.Lock()
	l.clientSwap = func(a *core.Agent) { a.SetClientAndModel(client, model) }
	agents := make([]*core.Agent, 0, len(l.chatAgents))
	for _, st := range l.chatAgents {
		agents = append(agents, st.agent)
	}
	l.mu.Unlock()
	l.Agent.SetClientAndModel(client, model)
	for _, a := range agents {
		a.SetClientAndModel(client, model)
	}
}

// onMessageEdited applies the stage-D host default: a still-queued
// message is replaced in place (the agent never saw the old text); an
// already-consumed one becomes a note on the chat's next prompt —
// unless the "edit" changed nothing (Discord fires MESSAGE_UPDATE when
// an embed unfurls, content untouched) or repeats a text already
// noted, which would only spam near-identical notes.
func (l *Loop) onMessageEdited(ev MessageEdited) {
	l.mu.Lock()
	for i := range l.queue {
		if l.queue[i].ChatID == ev.ChatID && l.queue[i].ID == ev.ID && ev.ID != "" {
			l.queue[i].Text = ev.Text
			l.queue[i].Entities = ev.Entities
			l.mu.Unlock()
			return
		}
	}
	unchanged := ev.ID != "" && l.lastMsgTextLocked(ev.ChatID, ev.ID) == ev.Text
	if !unchanged {
		l.recordMsgTextLocked(ev.ChatID, ev.ID, ev.Text)
	}
	l.mu.Unlock()
	if unchanged {
		return
	}
	l.addNote(ev.ChatID, "message_edited", fmt.Sprintf("the user edited an earlier message; its text is now: %s", truncateNote(ev.Text)))
}

// onMessageDeleted withdraws a still-queued message (and its staged
// files); anything already consumed is left alone — no retroactive
// transcript surgery.
func (l *Loop) onMessageDeleted(ev MessageDeleted) {
	l.mu.Lock()
	kept := l.queue[:0]
	var dropped []Message
	for _, qm := range l.queue {
		if qm.ChatID == ev.ChatID && qm.ID == ev.ID && ev.ID != "" {
			dropped = append(dropped, qm)
			continue
		}
		kept = append(kept, qm)
	}
	l.queue = kept
	l.mu.Unlock()
	for _, qm := range dropped {
		cleanupFiles(qm)
	}
}

// onReaction notes reactions on the bot's own messages; everything
// else is ignored (v2 default). Reactions are LOSSY — this is
// context, never authority.
func (l *Loop) onReaction(ev Reaction) {
	if !ev.OwnMessage || ev.ChatID == "" {
		return
	}
	who := ev.Username
	if who == "" {
		who = ev.UserID
	}
	verb := "reacted with"
	if ev.Removed {
		verb = "removed their reaction"
	}
	l.addNote(ev.ChatID, "reaction", fmt.Sprintf("@%s %s %s on my earlier message", who, verb, ev.Key))
}

// addNote queues a short observation for the chat's next prompt,
// typed and bracketed ("[chat event: message_edited] ...") so the
// model reads it as connector state, never as something the user just
// said — the confusion the first live agent reported.
func (l *Loop) addNote(chatID, kind, note string) {
	l.mu.Lock()
	if l.pendingNotes == nil {
		l.pendingNotes = map[string][]string{}
	}
	notes := append(l.pendingNotes[chatID], fmt.Sprintf("[chat event: %s] %s", kind, note))
	if len(notes) > maxPendingNotes {
		notes = notes[len(notes)-maxPendingNotes:]
	}
	l.pendingNotes[chatID] = notes
	l.mu.Unlock()
}

// activeTurnChat returns the chat of the currently running turn (""
// between turns). Approval notes ride the chat whose turn asked.
func (l *Loop) activeTurnChat() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.activeChatID
}

// recordMsgTextLocked / lastMsgTextLocked track the last text seen per
// message (bounded ring) for the edit-note dedup. Callers hold l.mu.
func (l *Loop) recordMsgTextLocked(chatID, id, text string) {
	if id == "" {
		return
	}
	key := chatID + "\x00" + id
	if l.recentTexts == nil {
		l.recentTexts = map[string]string{}
	}
	if _, seen := l.recentTexts[key]; !seen {
		l.recentTextsSeq = append(l.recentTextsSeq, key)
		for len(l.recentTextsSeq) > recentTextsCap {
			delete(l.recentTexts, l.recentTextsSeq[0])
			l.recentTextsSeq = l.recentTextsSeq[1:]
		}
	}
	l.recentTexts[key] = text
}

func (l *Loop) lastMsgTextLocked(chatID, id string) string {
	return l.recentTexts[chatID+"\x00"+id]
}

// recentTextsCap bounds the edit-dedup record; edits of messages older
// than the newest ~128 read as real edits, which only costs one note.
const recentTextsCap = 128

// takeNotes drains the chat's pending notes.
func (l *Loop) takeNotes(chatID string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	notes := l.pendingNotes[chatID]
	delete(l.pendingNotes, chatID)
	return notes
}

func truncateNote(s string) string {
	if r := []rune(s); len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return s
}

// promptText assembles the turn's prompt: the one-time chat-context
// line, pending stage-D notes, the stage-E file manifest, then the
// user's text — attributed to its sender in multi-user chats.
func (l *Loop) promptText(m Message) string {
	var b strings.Builder
	if intro := l.takeChatIntro(m); intro != "" {
		b.WriteString(intro + "\n")
	}
	for _, n := range l.takeNotes(m.ChatID) {
		b.WriteString(n + "\n")
	}
	// One renderer for every inbound attachment path: a connector's staged files
	// here, the web client's uploads in workspace.Prompt. missing is 0 because
	// these files were moved into place by the host, not resolved by id, so
	// there is nothing that could have expired between then and now.
	b.WriteString(attach.Manifest(attachRefs(m.Files), 0))
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	// In multi-user chats every prompt says who is speaking; the DM has
	// exactly one human, so its text stays untouched.
	if !isDM(m.ChatKind) && (m.Username != "" || m.UserID != "") {
		who := m.Username
		if who == "" {
			who = m.UserID
		}
		fmt.Fprintf(&b, "@%s: ", who)
	}
	b.WriteString(m.Text)
	return b.String()
}

// attachRefs adapts a connector's staged files to the shared manifest type.
// The connector wire carries Kind and Duration itself (a voice note's length is
// something only the service knows), so they pass through rather than being
// re-derived from the mime type.
func attachRefs(files []FileAttachment) []attach.Ref {
	if len(files) == 0 {
		return nil
	}
	refs := make([]attach.Ref, 0, len(files))
	for _, f := range files {
		refs = append(refs, attach.Ref{
			Name:     f.Name,
			Mime:     f.MimeType,
			Kind:     f.Kind,
			Size:     f.Size,
			Duration: f.Duration,
			Path:     f.Path,
		})
	}
	return refs
}

// takeChatIntro returns the [chat context] line exactly once per chat:
// where the conversation lives, who can be speaking, and how to format
// for it — chat services render only simple markdown (no tables), which
// the model can't otherwise know.
func (l *Loop) takeChatIntro(m Message) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.introduced[m.ChatID] {
		return ""
	}
	if l.introduced == nil {
		l.introduced = map[string]bool{}
	}
	l.introduced[m.ChatID] = true

	service := l.Service
	if service == "" {
		service = "chat"
	}
	// Each variant is one whole template so a prompt translation reads as
	// coherent <lang> — the shared "how to format" tail is repeated in
	// both rather than glued on in English.
	if isDM(m.ChatKind) {
		return i18n.P("chat.intro.dm", "[chat context] You are replying over %s in a direct chat with your paired user. Replies render in the chat app: plain text and simple markdown only — tables and other heavy markdown don't render there.", service)
	}
	title := m.ChatTitle
	if title == "" {
		title = m.ChatID
	}
	return i18n.P("chat.intro.group", "[chat context] You are replying over %s in the %s %q; multiple people can speak here, so each message is attributed (@name: …). Replies render in the chat app: plain text and simple markdown only — tables and other heavy markdown don't render there.", service, chatKindWord(m.ChatKind), title)
}

// chatKindWord renders a chat kind for the intro line.
func chatKindWord(kind string) string {
	switch kind {
	case "thread":
		return "thread"
	case "channel":
		return "channel"
	default:
		return "group chat"
	}
}

// cleanupFiles removes a message's staged attachment files and their
// per-message directories — after its turn, or when it is withdrawn
// from the queue.
func cleanupFiles(m Message) {
	dirs := map[string]bool{}
	for _, f := range m.Files {
		if f.Path == "" {
			continue
		}
		_ = os.Remove(f.Path)
		dirs[filepath.Dir(f.Path)] = true
	}
	for d := range dirs {
		// Only removes when empty — shared dirs survive siblings.
		_ = os.Remove(d)
	}
}
