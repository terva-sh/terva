package chat

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Pairing is the first-user-claims policy shared by the daemon loop
// and the TUI bridge: an unpaired connector belongs to whoever sends
// /start first; everyone else is refused.
type Pairing struct {
	// AllowedUserID is the claimed user, "" when unpaired.
	AllowedUserID string
	// Save persists a new claim (e.g. into the connector's config
	// file). Optional.
	Save func(userID string) error
}

// action is what the gate decided about an inbound message.
type action int

const (
	// actHandled — the gate replied inline (pairing, help) or the
	// message should be ignored; nothing further to do.
	actHandled action = iota
	// actStatus — caller should send its status text.
	actStatus
	// actStop — caller should cancel the active turn.
	actStop
	// actPrompt — message is a prompt for the agent.
	actPrompt
)

// gate routes inbound messages through pairing, the allowlist, and
// the built-in commands every chat surface supports (/start, /help,
// /status, /stop, plain "stop"). One implementation for Loop and
// Bridge so the policy can't drift between them again.
type gate struct {
	mu      sync.Mutex
	pairing Pairing
	// helpText answers /help and /start once paired.
	helpText string
	// pairedText acknowledges a successful claim; %s is the username.
	pairedText string
	// onPaired, if set, is told about a successful claim (the bridge
	// notifies its host).
	onPaired func(m Message)
}

// route decides what to do with m, sending pairing/help replies
// itself through conn.
func (g *gate) route(ctx context.Context, conn Connector, m Message) action {
	text := strings.TrimSpace(m.Text)

	g.mu.Lock()
	paired := g.pairing.AllowedUserID
	g.mu.Unlock()

	if paired == "" {
		if strings.HasPrefix(text, "/start") {
			g.mu.Lock()
			g.pairing.AllowedUserID = m.UserID
			if g.pairing.Save != nil {
				_ = g.pairing.Save(m.UserID)
			}
			g.mu.Unlock()
			_ = conn.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ReplyTo,
				Text: fmt.Sprintf(g.pairedText, m.Username)})
			if g.onPaired != nil {
				g.onPaired(m)
			}
			return actHandled
		}
		_ = conn.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ReplyTo,
			Text: "this bot isn't paired yet. send /start to claim it."})
		return actHandled
	}

	if m.UserID != paired {
		_ = conn.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ReplyTo,
			Text: "this bot is paired with a different user."})
		return actHandled
	}

	switch text {
	case "/start", "/help":
		_ = conn.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ReplyTo, Text: g.helpText})
		return actHandled
	case "/status":
		return actStatus
	case "/stop":
		return actStop
	}
	if IsStopCommand(text) {
		return actStop
	}
	if text == "" && len(m.Images) == 0 {
		return actHandled
	}
	return actPrompt
}
