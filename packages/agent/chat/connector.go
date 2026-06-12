// Package chat is the transport-agnostic chat-ops layer: one
// Connector contract that each chat service (telegram today; discord,
// matrix later) implements, plus the single daemon loop (Loop) and
// TUI mirror (Bridge) that consume it.
//
// Division of labor: a Connector is PURE TRANSPORT — wire protocol,
// auth, normalizing service messages, service-side limits. Everything
// about agents, turns, queues, pairing policy, and reply chunking
// lives here, once, instead of being re-written per service (see
// docs/plans/chat-connectors.md).
package chat

import (
	"context"
	"time"

	"terva.sh/terva/packages/provider"
)

// Message is one inbound chat message, already normalized by the
// transport: text assembled (captions merged), image attachments
// downloaded. IDs are strings — every candidate service's IDs fit,
// and the loop treats them as opaque.
type Message struct {
	ChatID   string // conversation to reply into
	UserID   string // sender, for pairing/allowlist
	Username string // sender's handle, for pairing acknowledgments
	ReplyTo  string // transport message id to reply to; "" = none
	Text     string
	Images   []provider.ImageBlock
}

// Outgoing is one outbound text message.
type Outgoing struct {
	ChatID  string
	ReplyTo string // "" = not a reply
	Text    string
}

// Identity is the connector's own account, resolved at Connect time.
type Identity struct {
	ID       string
	Username string
}

// Capabilities is an explicit struct, deliberately NOT optional
// interfaces or anonymous type assertions — the MirrorsToolImages
// wrapper bug class is exactly what this package does not repeat.
type Capabilities struct {
	// MaxTextLen is the outbound chunking threshold in bytes; the
	// loop splits longer replies. 0 = no limit.
	MaxTextLen int
	// TypingRefresh is how often the typing indicator must be
	// re-asserted to stay visible; 0 = service has no indicator.
	TypingRefresh time.Duration
	// SendsImages / SendsFiles gate the chat_send_* tools.
	SendsImages bool
	SendsFiles  bool
}

// Connector is one chat service. Implementations own the wire
// protocol and reconnection/backoff; a returned error from Receive
// means permanently broken (bad token), not a blip.
type Connector interface {
	Name() string

	// Connect validates credentials and resolves the bot's own
	// identity. Called once before Receive.
	Connect(ctx context.Context) (Identity, error)

	// Receive blocks, delivering normalized inbound messages to
	// handle until ctx ends. handle is called from a single
	// goroutine, in arrival order.
	Receive(ctx context.Context, handle func(Message)) error

	// Send delivers one outbound text message (pre-chunked by the
	// caller to Capabilities().MaxTextLen).
	Send(ctx context.Context, out Outgoing) error

	// SendImage / SendFile upload a local file to the chat. Only
	// called when the corresponding capability is set.
	SendImage(ctx context.Context, chatID, path, caption string) error
	SendFile(ctx context.Context, chatID, path, caption string) error

	// Typing asserts the typing indicator once; the loop re-calls it
	// every Capabilities().TypingRefresh while a turn runs.
	Typing(ctx context.Context, chatID string) error

	Capabilities() Capabilities
}
