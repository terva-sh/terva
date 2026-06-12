package chat

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"terva.sh/terva/packages/provider"
)

// Host is the small interface the Bridge calls back into the TUI
// through. Decouples bridge plumbing from the Interactive type.
type Host interface {
	// SubmitOrQueue feeds a user prompt into the running agent.
	// Runs now if the agent is idle, queues behind any in-flight
	// turn otherwise.
	SubmitOrQueue(prompt string, images []provider.ImageBlock)

	// CancelTurn aborts the active turn (if any). Called when the
	// paired chat user sends /stop or plain "stop".
	CancelTurn()

	// Status returns the current model, usage, context, and cwd
	// summary shown when the paired chat user sends /status.
	Status() string

	// Notify pushes a one-shot status line into the TUI. Used to
	// surface bridge events ("connected as @bot", "paired with
	// user X", etc.) in the user's local transcript.
	Notify(level, message string)
}

// Bridge mirrors a chat conversation into a running TUI session:
// inbound messages forward to the Host's agent, and the TUI's turns
// mirror back out to the paired chat. One bridge per Interactive
// instance.
type Bridge struct {
	Connector Connector
	Host      Host

	// Pairing, HelpText, PairedText configure the shared gate; see
	// Loop for semantics.
	Pairing    Pairing
	HelpText   string
	PairedText string

	mu       sync.Mutex
	running  bool
	cancel   context.CancelFunc
	identity Identity
	chatID   string // populated after first DM from the paired user
	gate     *gate

	// nextReplyFromChat is set when the next assistant reply was
	// initiated by a chat message rather than typed in the TUI.
	// Historically this chose a "terva: " prefix for TUI-originated
	// replies; both sides currently send bare text, but the flag is
	// kept so the distinction stays available.
	nextReplyFromChat bool
}

// BridgeState is the snapshot the TUI's status dialog reports.
type BridgeState struct {
	Running  bool
	Username string
	PairedID string // "" when no user has claimed the bot yet
}

// Active reports whether the bridge is currently receiving.
func (b *Bridge) Active() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running
}

// State returns a snapshot of the bridge for status displays.
func (b *Bridge) State() BridgeState {
	if b == nil {
		return BridgeState{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	pid := b.Pairing.AllowedUserID
	if b.gate != nil {
		b.gate.mu.Lock()
		pid = b.gate.pairing.AllowedUserID
		b.gate.mu.Unlock()
	}
	return BridgeState{Running: b.running, Username: b.identity.Username, PairedID: pid}
}

// Start connects and begins receiving. Idempotent: calling twice
// returns nil the second time and leaves the existing loop alone.
func (b *Bridge) Start(parent context.Context) error {
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()

	ctx, cancel := context.WithCancel(parent)
	id, err := b.Connector.Connect(ctx)
	if err != nil {
		cancel()
		return err
	}

	help := b.HelpText
	if help == "" {
		help = "mirror is active. send me a message and it'll be forwarded to the terva tui. commands: /status, /stop, or plain stop."
	}
	paired := b.PairedText
	if paired == "" {
		paired = "paired with @%s. messages you send here now mirror into the terva tui."
	}

	b.mu.Lock()
	b.running = true
	b.cancel = cancel
	b.identity = id
	// If a user paired in a previous session, adopt their DM chat
	// when the connector can address them directly. (For telegram,
	// private-chat ids equal user ids; the connector seeds ChatID on
	// the first inbound message either way.)
	if b.Pairing.AllowedUserID != "" && b.chatID == "" {
		b.chatID = b.Pairing.AllowedUserID
	}
	b.gate = &gate{
		pairing:    b.Pairing,
		helpText:   help,
		pairedText: paired,
		onPaired: func(m Message) {
			b.mu.Lock()
			b.chatID = m.ChatID
			b.mu.Unlock()
			b.Host.Notify("success", fmt.Sprintf("%s paired with user %s (@%s)", b.Connector.Name(), m.UserID, m.Username))
		},
	}
	b.mu.Unlock()

	go func() {
		if err := b.Connector.Receive(ctx, b.handle); err != nil && ctx.Err() == nil {
			b.Host.Notify("warn", fmt.Sprintf("%s: receive: %v", b.Connector.Name(), err))
		}
	}()
	return nil
}

// Stop halts the receive loop. Safe to call when not running.
func (b *Bridge) Stop() {
	b.mu.Lock()
	cancel := b.cancel
	b.running = false
	b.cancel = nil
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// handle routes one inbound message.
func (b *Bridge) handle(m Message) {
	ctx := context.Background()

	b.mu.Lock()
	g := b.gate
	b.mu.Unlock()
	if g == nil {
		return
	}

	switch g.route(ctx, b.Connector, m) {
	case actStatus:
		b.rememberChat(m.ChatID)
		_ = b.Connector.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ReplyTo, Text: b.Host.Status()})
	case actStop:
		b.rememberChat(m.ChatID)
		b.Host.CancelTurn()
		_ = b.Connector.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ReplyTo, Text: "cancelled the current turn."})
	case actPrompt:
		b.rememberChat(m.ChatID)
		b.mu.Lock()
		b.nextReplyFromChat = true
		b.mu.Unlock()
		b.Host.SubmitOrQueue(m.Text, m.Images)
	}
}

// rememberChat records where replies should go, so mirroring works
// without waiting for another round-trip.
func (b *Bridge) rememberChat(chatID string) {
	b.mu.Lock()
	b.chatID = chatID
	b.mu.Unlock()
}

// OnAssistantText mirrors the assistant's final visible text for each
// TUI turn out to the paired chat, chunked to the connector's limit.
func (b *Bridge) OnAssistantText(text string) {
	b.mu.Lock()
	// Replies currently send bare regardless of origin; see
	// nextReplyFromChat for the retained distinction.
	prefix := ""
	if b.nextReplyFromChat {
		prefix = ""
		b.nextReplyFromChat = false
	}
	b.mu.Unlock()
	b.sendToPaired(text, prefix)
}

// OnUserTyped mirrors a message the user typed in the terva TUI into
// the paired chat, tagged "you:" so the chat thread stays a complete
// record of the conversation.
func (b *Bridge) OnUserTyped(text string) {
	b.sendToPaired(text, "you: ")
}

// sendToPaired writes text (with an optional prefix, chunked) to the
// paired chat. No-op when the bridge is stopped or before the paired
// chat id is known.
func (b *Bridge) sendToPaired(text, prefix string) {
	b.mu.Lock()
	chatID := b.chatID
	running := b.running
	b.mu.Unlock()
	if !running || chatID == "" {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if prefix != "" {
		text = prefix + text
	}
	for _, chunk := range ChunkMessage(text, b.Connector.Capabilities().MaxTextLen) {
		if err := b.Connector.Send(context.Background(), Outgoing{ChatID: chatID, Text: chunk}); err != nil {
			b.Host.Notify("warn", fmt.Sprintf("%s bridge: send: %v", b.Connector.Name(), err))
			return
		}
	}
}

// SendImage uploads path to the paired chat as an inline image. Used
// by the chat send-image tool so a chat-originated turn can yield a
// real image instead of a textual description.
func (b *Bridge) SendImage(ctx context.Context, path, caption string) error {
	chatID, err := b.pairedChat()
	if err != nil {
		return err
	}
	return b.Connector.SendImage(ctx, chatID, path, caption)
}

// SendDocument uploads path to the paired chat as a raw document
// attachment (no compression). Counterpart of SendImage.
func (b *Bridge) SendDocument(ctx context.Context, path, caption string) error {
	chatID, err := b.pairedChat()
	if err != nil {
		return err
	}
	return b.Connector.SendFile(ctx, chatID, path, caption)
}

func (b *Bridge) pairedChat() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.running {
		return "", fmt.Errorf("%s bridge is not running", b.Connector.Name())
	}
	if b.chatID == "" {
		return "", fmt.Errorf("%s bridge has no paired chat yet", b.Connector.Name())
	}
	return b.chatID, nil
}
