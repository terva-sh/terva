package chat

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Pairing is the first-user-claims policy shared by the daemon loop
// and the TUI bridge: an unpaired connector belongs to whoever sends
// /start first; everyone else is refused. The paired user is the
// OWNER — the only identity with authority (admission, /stop, chat
// approvals) everywhere the bot appears.
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

// gate routes inbound messages through pairing, group admission, and
// the built-in commands every chat surface supports. One
// implementation for Loop and Bridge so the policy can't drift
// between them again.
//
// Gate v2 (connproto v2 stage B) is kind-aware:
//   - DMs: exactly the v1 policy — first /start claims, the owner
//     converses, everyone else is refused. Plus the owner can
//     `/approve <chat-id> [all]` / `/revoke <chat-id>` from the DM.
//   - Non-DM chats (group/thread/channel): SILENT by default. The
//     owner admits a chat by saying `/approve [all]` in it (a leading
//     @bot mention is tolerated — on some services the bot only hears
//     messages that mention it); `/revoke` reverses. Approved chats
//     default to mention mode: only messages addressing the bot (a
//     bot_mention entity, or "@username" in the text as the host-side
//     fallback) start turns; `all` mode routes everything. Non-owner
//     members get REACH (their messages start turns in approved
//     chats) but never AUTHORITY — admission, /stop, and /status stay
//     owner-only.
type gate struct {
	mu      sync.Mutex
	pairing Pairing
	// admissions is the approved-chats store; nil means no group may
	// ever be admitted (everything non-DM stays silent).
	admissions *Admissions
	// botUsername enables the @username mention fallback and the
	// leading-mention strip on commands. Optional.
	botUsername string
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
	g.mu.Lock()
	paired := g.pairing.AllowedUserID
	g.mu.Unlock()

	if isDM(m.ChatKind) {
		return g.routeDM(ctx, conn, m, paired)
	}
	return g.routeGroup(ctx, conn, m, paired)
}

func isDM(kind string) bool { return kind == "" || kind == "dm" }

// routeDM is the v1 policy plus the DM-side admission commands.
func (g *gate) routeDM(ctx context.Context, conn Connector, m Message, paired string) action {
	text := strings.TrimSpace(m.Text)

	if paired == "" {
		if strings.HasPrefix(text, "/start") {
			g.mu.Lock()
			g.pairing.AllowedUserID = m.UserID
			if g.pairing.Save != nil {
				_ = g.pairing.Save(m.UserID)
			}
			g.mu.Unlock()
			_ = conn.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID,
				Text: fmt.Sprintf(g.pairedText, m.Username)})
			if g.onPaired != nil {
				g.onPaired(m)
			}
			return actHandled
		}
		_ = conn.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID,
			Text: "this bot isn't paired yet. send /start to claim it."})
		return actHandled
	}

	if m.UserID != paired {
		_ = conn.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID,
			Text: "this bot is paired with a different user."})
		return actHandled
	}

	// Owner DM commands for remote admission: /approve <chat-id>
	// [all|mention], /revoke <chat-id>.
	if cmd, args, ok := splitCommand(text, "/approve"); ok {
		_ = cmd
		if len(args) == 0 {
			_ = conn.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID,
				Text: "usage: /approve <chat-id> [all] — or say /approve inside the chat itself."})
			return actHandled
		}
		// Owner approving a remote chat by id from the DM: no container
		// context, so it records no scope (not guild-tracked).
		g.approve(ctx, conn, m, args[0], modeFromArgs(args[1:]), "")
		return actHandled
	}
	if _, args, ok := splitCommand(text, "/revoke"); ok {
		if len(args) == 0 {
			_ = conn.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID,
				Text: "usage: /revoke <chat-id>"})
			return actHandled
		}
		g.revoke(ctx, conn, m, args[0])
		return actHandled
	}

	switch text {
	case "/start", "/help":
		_ = conn.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID, Text: g.helpText})
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

// routeGroup applies the admission policy to non-DM chats.
func (g *gate) routeGroup(ctx context.Context, conn Connector, m Message, paired string) action {
	// No owner yet: nothing in a group may claim or converse. Silence,
	// not a reply — an unpaired bot must not be discoverable spam.
	if paired == "" {
		return actHandled
	}

	// Commands tolerate a leading @bot mention: on mention-gated
	// services the owner can only reach us by addressing us.
	text := g.stripLeadingMention(m)

	isOwner := m.UserID == paired

	// Admission commands work in the chat itself, owner-only, and
	// BEFORE the admission check (that is their whole point).
	if _, args, ok := splitCommand(text, "/approve"); ok {
		if !isOwner {
			return g.ownerOnly(ctx, conn, m)
		}
		// Approved in the chat itself: record the container it belongs to so a
		// later removal from that container revokes this chat with its siblings.
		g.approve(ctx, conn, m, m.ChatID, modeFromArgs(args), m.ScopeID)
		return actHandled
	}
	if _, _, ok := splitCommand(text, "/revoke"); ok {
		if !isOwner {
			return g.ownerOnly(ctx, conn, m)
		}
		g.revoke(ctx, conn, m, m.ChatID)
		return actHandled
	}

	g.mu.Lock()
	adm := g.admissions
	g.mu.Unlock()
	mode, approved := adm.Mode(m.ChatID)
	if !approved {
		// Silent-by-default: the security boundary for group reach.
		return actHandled
	}

	if mode == ModeMention && !g.mentionsBot(m) {
		return actHandled
	}

	switch text {
	case "/help", "/start":
		_ = conn.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID, Text: g.helpText})
		return actHandled
	case "/status":
		if !isOwner {
			return g.ownerOnly(ctx, conn, m)
		}
		return actStatus
	case "/stop":
		if !isOwner {
			return g.ownerOnly(ctx, conn, m)
		}
		return actStop
	}
	if IsStopCommand(text) && isOwner {
		return actStop
	}
	if text == "" && len(m.Images) == 0 {
		return actHandled
	}
	return actPrompt
}

func (g *gate) ownerOnly(ctx context.Context, conn Connector, m Message) action {
	_ = conn.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID,
		Text: "only the paired owner can do that."})
	return actHandled
}

func (g *gate) approve(ctx context.Context, conn Connector, m Message, chatID, mode, scope string) {
	g.mu.Lock()
	adm := g.admissions
	g.mu.Unlock()
	if adm == nil {
		_ = conn.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID,
			Text: "group admission isn't available on this surface."})
		return
	}
	if err := adm.ApproveScoped(chatID, mode, scope); err != nil {
		_ = conn.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID,
			Text: "couldn't save the approval: " + err.Error()})
		return
	}
	how := "when mentioned"
	if mode == ModeAll {
		how = "on every message"
	}
	_ = conn.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID,
		Text: fmt.Sprintf("approved — i'll respond in this chat %s. /revoke reverses.", how)})
}

func (g *gate) revoke(ctx context.Context, conn Connector, m Message, chatID string) {
	g.mu.Lock()
	adm := g.admissions
	g.mu.Unlock()
	if adm == nil {
		return
	}
	if err := adm.Revoke(chatID); err != nil {
		_ = conn.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID,
			Text: "couldn't save the revocation: " + err.Error()})
		return
	}
	_ = conn.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID,
		Text: "revoked — this chat is silent again."})
}

// mentionsBot reports whether m addresses the bot: the transport's
// bot_mention entity, or the host-side @username fallback for
// connectors that don't emit entities.
func (g *gate) mentionsBot(m Message) bool {
	if m.MentionsBot() {
		return true
	}
	g.mu.Lock()
	name := g.botUsername
	g.mu.Unlock()
	if name == "" {
		return false
	}
	return strings.Contains(strings.ToLower(m.Text), "@"+strings.ToLower(name))
}

// stripLeadingMention removes a bot mention at the start of the text —
// by entity span when the transport located it, by "@username" prefix
// otherwise — so "@terva /approve" parses as the command it is.
func (g *gate) stripLeadingMention(m Message) string {
	text := m.Text
	for _, e := range m.Entities {
		if e.Kind != "bot_mention" || e.Length == 0 {
			continue
		}
		r := []rune(text)
		if e.Offset <= 0 && e.Offset+e.Length <= len(r) {
			// Leading (possibly after whitespace already trimmed by
			// the service): drop the span.
			if strings.TrimSpace(string(r[:e.Offset])) == "" {
				text = string(r[e.Offset+e.Length:])
			}
		}
		break
	}
	text = strings.TrimSpace(text)
	g.mu.Lock()
	name := g.botUsername
	g.mu.Unlock()
	if name != "" {
		lower := strings.ToLower(text)
		if strings.HasPrefix(lower, "@"+strings.ToLower(name)) {
			text = strings.TrimSpace(text[len("@"+name):])
		}
	}
	return strings.TrimSpace(text)
}

// splitCommand matches "<cmd>" or "<cmd> args..." (but not
// "<cmd>whatever") and returns the remaining fields.
func splitCommand(text, cmd string) (string, []string, bool) {
	if text == cmd {
		return cmd, nil, true
	}
	if strings.HasPrefix(text, cmd+" ") {
		return cmd, strings.Fields(text[len(cmd)+1:]), true
	}
	return "", nil, false
}

// modeFromArgs reads an optional trailing mode argument; anything but
// "all" is the mention default.
func modeFromArgs(args []string) string {
	for _, a := range args {
		if strings.EqualFold(a, ModeAll) {
			return ModeAll
		}
	}
	return ModeMention
}
