// Package chat is the transport-agnostic chat-ops layer: one
// Connector contract that each chat service (telegram today; discord,
// matrix later) implements, plus the single daemon loop (Loop) and
// TUI mirror (Bridge) that consume it.
//
// Division of labor: a Connector is PURE TRANSPORT — wire protocol,
// auth, normalizing service messages, service-side limits. Everything
// about agents, turns, queues, pairing policy, and reply chunking
// lives here, once, instead of being re-written per service (see
// docs/plans/archive/chat-connectors.md).
package chat

import (
	"context"
	"errors"
	"time"

	"terva.sh/terva/packages/provider"
)

// Message is one inbound chat message, already normalized by the
// transport: text assembled (captions merged), image attachments
// downloaded. IDs are strings — every candidate service's IDs fit,
// and the loop treats them as opaque.
type Message struct {
	// ID is the message's own transport id ("" when the transport has
	// none) — the token replies reference. Carriers normalize the old
	// protocol-1 shape (own id riding ReplyTo) onto this field, so
	// consumers only ever see these semantics.
	ID        string
	TS        int64  // when the service says it happened (unix ms); 0 = unknown
	ChatID    string // conversation to reply into
	ChatKind  string // "dm" (also ""), "group", "thread", "channel"
	ChatTitle string // display-only; "" for DMs
	ScopeID   string // container the chat belongs to (e.g. Discord guild); "" = scopeless
	UserID    string // sender, for pairing/allowlist
	Username  string // sender's handle, for pairing acknowledgments
	ReplyTo   string // id of the message THIS one replies to; "" = none
	Text      string
	Entities  []Entity // minimum-viable markup; empty for most messages
	Images    []provider.ImageBlock
	// Files are non-image attachments (stage E), already moved into a
	// host-owned per-message directory the agent can read with its
	// normal tools. The consumer cleans the directory after the turn.
	Files []FileAttachment
}

// FileAttachment is one beyond-image inbound file: provenance-labeled
// gate-approved content on disk, not silently dropped.
type FileAttachment struct {
	Path     string
	Kind     string // audio | voice | video | document | sticker
	MimeType string
	Name     string
	Size     int64
	Duration time.Duration
	Caption  string
}

// MessageEdited reports a user editing an earlier message; ID is the
// ORIGINAL message's id.
type MessageEdited struct {
	ChatID   string
	ID       string
	TS       int64
	Text     string
	Entities []Entity
}

// MessageDeleted reports a user deleting a message.
type MessageDeleted struct {
	ChatID string
	ID     string
}

// Reaction is one reaction toggling on a message. Key is opaque
// (unicode emoji is the interoperable subset); Removed is first-class.
// OwnMessage is true when the reaction landed on a message the bot
// itself sent — the carriers tag it from their send bookkeeping.
// Reactions are LOSSY: never let anything security-relevant hinge on
// them.
type Reaction struct {
	ChatID     string
	MessageID  string
	UserID     string
	Username   string
	Key        string
	Removed    bool
	OwnMessage bool
}

// ChatEventHandlers is the consumer's optional stage-D event surface;
// any handler may be nil. Handlers run on the connector's delivery
// goroutine and must not block.
type ChatEventHandlers struct {
	Edited   func(MessageEdited)
	Deleted  func(MessageDeleted)
	Reaction func(Reaction)
}

// ChatEventsSetter is the optional Connector upgrade for stage-D
// inbound events. Install before Receive.
type ChatEventsSetter interface {
	SetChatEventHandlers(ChatEventHandlers)
}

// Editor / Reactor / Deleter are the optional Connector upgrades for
// the stage-D outbound commands. Gate on the matching Capabilities
// field before type-asserting.
type Editor interface {
	// EditMessage rewrites the bot's own earlier message. Always pass
	// the ORIGINAL message id; respect Capabilities().MinEditInterval
	// when streaming.
	EditMessage(ctx context.Context, chatID, messageID, text string) error
}

type Reactor interface {
	React(ctx context.Context, chatID, messageID, key string, remove bool) error
}

type Deleter interface {
	DeleteMessage(ctx context.Context, chatID, messageID string) error
}

// Entity is one span of markup on Text (offsets in Unicode code
// points). Kind "bot_mention" drives group mention-gating; "mention"
// (with UserID), "code", and "link" are the cheap rest. Offset and
// Length both zero means the presence is known but not located.
type Entity struct {
	Kind   string
	Offset int
	Length int
	UserID string
}

// MentionsBot reports whether the message carries a bot_mention
// entity — the transport said "this one is addressed to you".
func (m Message) MentionsBot() bool {
	for _, e := range m.Entities {
		if e.Kind == "bot_mention" {
			return true
		}
	}
	return false
}

// Membership is the bot's own admission changing in a chat: Change is
// "added" or "removed". The trust hook for group admission — see the
// gate.
type Membership struct {
	ChatID     string
	ChatKind   string
	ChatTitle  string
	ScopeID    string // container the chat belongs to (e.g. Discord guild); "" = scopeless
	Change     string
	ByUserID   string
	ByUsername string
}

// MembershipHandlerSetter is the optional Connector upgrade for
// membership events. The consumer installs its handler BEFORE Receive;
// the handler is called from the connector's delivery goroutine and
// must not block. Connectors whose service can't report admission
// simply never implement this — the gate still works, it just learns
// about new chats from their first message instead.
type MembershipHandlerSetter interface {
	SetMembershipHandler(func(Membership))
}

// Speaker is an alternate outbound identity — personas and the --play
// cast want DIFFERENT characters speaking in one chat. Key is stable
// across the session (it keys per-speaker connector state like a
// managed webhook); Name is the display name; AvatarPath is a local
// image, honored only by SpeakerFull connectors. Asks never carry a
// speaker — they come from the bot principal.
type Speaker struct {
	Key        string
	Name       string
	AvatarPath string
}

// Speaker capability grades (Capabilities.Speaker).
const (
	SpeakerNone     = ""          // host renders the prefix fallback
	SpeakerNameOnly = "name_only" // per-message display name
	SpeakerFull     = "full"      // name + avatar
)

// Outgoing is one outbound text message. Speaker, when set, asks the
// connector to render the message under that identity; senders never
// need to check capability — the plumbing falls back to a
// "**Name:** text" prefix wherever the service can't do better.
type Outgoing struct {
	ChatID  string
	ReplyTo string // "" = not a reply
	Text    string
	Speaker *Speaker
}

// RenderSpeakerFallback rewrites a speaker send as a plain prefixed
// message — the floor that makes the cast work on every connector.
// No-op without a speaker.
func RenderSpeakerFallback(out Outgoing) Outgoing {
	if out.Speaker == nil {
		return out
	}
	name := out.Speaker.Name
	if name == "" {
		name = out.Speaker.Key
	}
	out.Text = "**" + name + ":** " + out.Text
	out.Speaker = nil
	return out
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
	// Asks is true when the connector renders interactive asks
	// natively (wire feature "asks"). When false, Loop.Ask falls back
	// to a plain-text question — approvals over chat work with EVERY
	// connector; this only upgrades the widget and the attestation.
	Asks bool
	// Speaker grades the connector's alternate-identity rendering:
	// SpeakerFull (name + avatar), SpeakerNameOnly, or SpeakerNone —
	// in which case sends with a Speaker get the host's prefix
	// fallback. Wire features "speaker:full" / "speaker:name_only".
	Speaker string
	// ThreadsOut is true when the connector can open work-stream
	// threads (wire feature "threads_out"). Flat services stay false
	// and consumers keep everything in the main chat.
	ThreadsOut bool
	// Stage D outbound abilities (features "edits_out",
	// "reactions_out", "deletes_out") and the edit-streaming pace the
	// connector asked for.
	EditsOut        bool
	ReactionsOut    bool
	DeletesOut      bool
	MinEditInterval time.Duration
	// AttachmentKinds is true when the connector labels beyond-image
	// attachments (stage E, feature "attachment_kinds").
	AttachmentKinds bool
}

// Threader is the optional Connector upgrade for work-stream threads.
// Gate on Capabilities().ThreadsOut before type-asserting. StartThread
// returns the id of a NEW chat (kind "thread"); everything else —
// sends, asks, speakers — targets it like any chat.
type Threader interface {
	StartThread(ctx context.Context, chatID, fromMessageID, name string) (threadChatID string, err error)
}

// AskOption is one choice on an Ask. Key is the semantic identity
// (what comes back on the Answer); Label is the human text; Style
// ("affirm", "deny", "") and Hint (an emoji, for reaction-rendered
// options) are rendering hints only.
type AskOption struct {
	Key   string
	Label string
	Style string
	Hint  string
}

// Ask is one constrained question posed in a chat: the recipient picks
// an option, and the first valid answer wins (the asker closes the
// question immediately — churn-prone surfaces like reactions never
// deliver a second answer). See docs/proposals/connector-protocol-v2.md.
type Ask struct {
	ChatID  string
	ReplyTo string // message the question replies to; "" = none
	Text    string
	Options []AskOption
	// RestrictTo lists the user ids allowed to answer; empty means
	// anyone. Enforced twice: service-side where the platform can, and
	// by the host regardless.
	RestrictTo []string
	// Timeout bounds the wait; 0 means DefaultAskTimeout. Expiry is
	// fail-closed: the ask is withdrawn and ErrAskTimeout returned.
	Timeout time.Duration
	// TimeoutOutcome is rendered into the withdrawn question on
	// expiry; "" uses a neutral default.
	TimeoutOutcome string
	// MultiSelect invites several options at once; the answer carries
	// them in Keys. AllowCustom invites an answer that is not on the
	// list at all, arriving as Text.
	//
	// 🔑 Only the TEXT FLOOR can express either today — a widget that
	// returns one key structurally cannot, so Loop.Ask routes an ask
	// that sets one of these to the floor even on a connector that
	// renders asks natively. That is backwards from the usual
	// arrangement and deliberate: answering the question that was
	// actually asked beats a prettier answer to a narrower one. Adding
	// the widget path is a wire change behind its own feature string
	// (Discord select menus and modals); until then a rich question
	// costs the button UI and drops attestation to best-effort, which
	// approvals never pay because they never set either flag.
	MultiSelect bool
	AllowCustom bool
}

// Answer is the winning response to an Ask.
type Answer struct {
	// Key is the chosen option, and for a multi-select answer it is
	// the FIRST of Keys — so a caller that only knows about single
	// choice (every approval path) keeps working unchanged rather
	// than silently reading an empty string.
	Key string
	// Keys carries every option chosen when the Ask set MultiSelect.
	// Empty for a single-choice answer; read Key there.
	Keys []string
	// Text carries a written-in answer when the Ask set AllowCustom
	// and the reply matched no option. Key is empty in that case:
	// "they picked nothing from the list" and "they picked the option
	// whose key is the empty string" must not look alike.
	Text     string
	UserID   string
	Username string
	// Attestation grades how sure the platform is about who answered:
	// AttestationAttested (button interactions, callback queries) or
	// AttestationBestEffort (parsed text, reactions). Policy that
	// grants anything durable should require attested.
	Attestation string
}

// Attestation grades for Answer (mirroring the wire constants).
const (
	AttestationAttested   = "attested"
	AttestationBestEffort = "best_effort"
)

// DefaultAskTimeout bounds an Ask that doesn't set its own. Long
// enough to pull a phone out of a pocket; short enough that a
// fail-closed denial doesn't strand the turn.
const DefaultAskTimeout = 2 * time.Minute

// ErrAskTimeout reports that nobody answered before the deadline. The
// ask was withdrawn; callers treat it as a denial (fail closed).
var ErrAskTimeout = errors.New("ask expired with no answer")

// Asker is the optional Connector upgrade for native interactive asks.
// Gate on Capabilities().Asks before type-asserting — a wire-backed
// connector structurally has the method even when its peer never
// declared the feature.
type Asker interface {
	// Ask renders the question, waits for the first valid answer (or
	// the timeout / ctx end), withdraws the question with a rendered
	// outcome, and returns the answer. Fail-closed on every non-answer
	// path.
	Ask(ctx context.Context, a Ask) (Answer, error)
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
