package chat

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/core"
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

	// RefreshCreds is called before every turn to pick up newly
	// refreshed OAuth tokens. Optional.
	RefreshCreds func() error

	// Pairing is the first-user-claims policy state.
	Pairing Pairing

	// HelpText answers /help; PairedText (with %s = username)
	// acknowledges a claim. Both have service-neutral defaults.
	HelpText   string
	PairedText string

	// Info and Warn receive operational log lines. Default to stdout
	// and stderr respectively.
	Info func(string)
	Warn func(string)

	mu           sync.Mutex
	busy         bool
	activeCancel context.CancelFunc
	queue        []Message
	lastCtxInput int
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
	g := &gate{pairing: l.Pairing, helpText: help, pairedText: paired}

	return l.Connector.Receive(ctx, func(m Message) {
		switch g.route(ctx, l.Connector, m) {
		case actStatus:
			l.sendStatus(ctx, m)
		case actStop:
			l.cancelActiveTurn(ctx, m)
		case actPrompt:
			l.enqueue(ctx, m)
		}
	})
}

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
			l.mu.Unlock()
			return
		}
		m := l.queue[0]
		l.queue = l.queue[1:]
		l.busy = true
		turnCtx, cancel := context.WithCancel(parent)
		l.activeCancel = cancel
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

// runTurn sends the queued prompt to the agent and replies with the
// final assistant text.
func (l *Loop) runTurn(ctx context.Context, m Message) {
	stopTyping := l.startTyping(ctx, m.ChatID)
	defer stopTyping()

	// The paired user sent a photo a text-only model will never see
	// (the provider layer drops it rather than 400-bricking the
	// session) — tell them now, not never.
	if len(m.Images) > 0 {
		l.mu.Lock()
		provName := l.Provider
		l.mu.Unlock()
		if mdl, err := provider.FindModel(provName, l.Agent.Model); err == nil && !mdl.Has(provider.CapImageInput) {
			_ = l.Connector.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ReplyTo,
				Text: fmt.Sprintf("note: %s can't see images; only your text reaches it.", l.Agent.Model)})
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
			l.mu.Lock()
			if e.Usage.InputTokens > 0 {
				l.lastCtxInput = e.Usage.InputTokens + e.Usage.CacheReadTokens + e.Usage.CacheWriteTokens
			}
			l.mu.Unlock()
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

	if err := l.Agent.Prompt(ctx, m.Text, m.Images, sink); err != nil {
		turnErr = err
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

func (l *Loop) cancelActiveTurn(ctx context.Context, m Message) {
	l.mu.Lock()
	cancel := l.activeCancel
	l.mu.Unlock()
	if cancel != nil {
		cancel()
		_ = l.Connector.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ReplyTo, Text: "cancelled the current turn."})
	} else {
		_ = l.Connector.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ReplyTo, Text: "nothing running."})
	}
}

// sendStatus describes agent state to the paired user.
func (l *Loop) sendStatus(ctx context.Context, m Message) {
	l.mu.Lock()
	busy := l.busy
	queued := len(l.queue)
	ctxUsed := l.lastCtxInput
	providerName := l.Provider
	authMethod := l.AuthMethod
	cwd := l.CWD
	l.mu.Unlock()

	model := l.Agent.Model
	ctxMax := 0
	if mdl, err := provider.FindModel(providerName, model); err == nil {
		ctxMax = mdl.ContextWindow
	}
	_ = l.Connector.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ReplyTo, Text: FormatStatus(StatusSnapshot{
		Provider:     providerName,
		Model:        model,
		CWD:          cwd,
		Usage:        l.Agent.Cost(),
		Subscription: authMethod == "oauth",
		ContextUsed:  ctxUsed,
		ContextMax:   ctxMax,
		Busy:         busy,
		Queued:       queued,
	})})
}
