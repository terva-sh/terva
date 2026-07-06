// Package connproto defines the JSON-over-stdin/stdout wire format
// spoken between zot and an external (out-of-process) chat connector.
// Both the host proxy (packages/agent/chat/external) and the author
// SDK (packages/agent/connsdk) marshal/unmarshal these types.
//
// This is deliberately a SEPARATE protocol from the extension one
// (packages/agent/extproto): connectors need a continuous child→host
// message stream, which the extension protocol lacks. The framing
// conventions are shared — one JSON object per LF-terminated line, a
// top-level Type discriminator, and an ID only on commands and their
// responses so the sender can correlate.
//
// Direction conventions in this file:
//   - Type names ending in "FromConn" are sent by the connector to zot.
//   - Type names ending in "FromHost" are sent by zot to the connector.
//   - Names without a suffix are shared value types.
//
// Versioning is negotiated, not announce-only: the connector's hello
// carries [ProtocolMin, ProtocolMax] and the host refuses the spawn
// with a clear error when its own version falls outside that range.
// The golden-frame tests in connproto_test.go pin this schema; do not
// change field names or types without bumping ProtocolVersion.
package connproto

import "encoding/json"

// ProtocolVersion is the LOWEST wire-format version this code speaks;
// ProtocolMax is the highest. The host negotiates the highest version
// both sides support (hello carries the connector's [min,max]; the
// hello_ack answers with the pick). Version 2 — stage A of
// docs/proposals/connector-protocol-v2.md — adds message identity:
// message.id/ts/chat_kind/chat_title, result.message_id, true
// in-reply-to semantics for reply_to, and the two-sided feature-string
// capability exchange. Every v2 field is additive (omitempty), so v1
// golden frames are byte-identical; the version exists so both sides
// can rely on SEMANTICS (a protocol-2 peer promises message.id and
// in-reply-to reply_to; a protocol-1 peer's inbound reply_to IS the
// message's own id, which hosts normalize — see connhost).
//
// Constructs past stage A stay inside protocol 2, gated by feature
// strings instead of version bumps: ask/answer/ask_close (stage G,
// feature "asks"), send.speaker (stage H, "speaker:full" /
// "speaker:name_only"), thread_start (stage I, "threads_out"),
// message entities + chat_membership (stage B, "entities" /
// "chat_membership"), edits/deletes/reactions in both directions
// (stage D, "edits_in|out" / "deletes_in|out" / "reactions_in|out"),
// and attachment kinds (stage E, "attachment_kinds") ride the same
// version because a peer that never declares the feature never sees —
// or never emits — the frames.
const (
	ProtocolVersion = 1
	ProtocolMax     = 2
)

// Frame is the minimal envelope every line decodes into first.
type Frame struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

// Capabilities describes the service's formatting limits and upload
// support, declared once in hello. Mirrors chat.Capabilities with
// wire-friendly types (milliseconds instead of time.Duration).
type Capabilities struct {
	// MaxTextLen is the outbound chunking threshold in bytes; the
	// host splits longer replies. 0 = no limit.
	MaxTextLen int `json:"max_text_len,omitempty"`
	// TypingRefreshMS is how often the typing indicator must be
	// re-asserted to stay visible; 0 = service has no indicator.
	TypingRefreshMS int  `json:"typing_refresh_ms,omitempty"`
	SendsImages     bool `json:"sends_images,omitempty"`
	SendsFiles      bool `json:"sends_files,omitempty"`
	// Features is the protocol-2 flat feature-string set (extensible
	// without a boolean per release): "message_ids", "chat_kinds", …
	// On hello it means "I produce these"; on hello_ack's Capabilities
	// it means "I consume these" — a side only emits an optional
	// construct the peer declared.
	Features []string `json:"features,omitempty"`
	// MinEditIntervalMS (stage D, with "edits_out") tells the host how
	// fast it may stream edits to one message; 0 = no limit declared.
	MinEditIntervalMS int `json:"min_edit_interval_ms,omitempty"`
}

// Attachment is one inbound file, passed by path (same-host
// assumption, like extensions): the connector writes the bytes under
// its host-assigned data_dir and the host takes ownership (images are
// read + deleted; other kinds are moved into a per-message dir and
// cleaned after the turn). Keeps frames far below the 4MiB line cap.
//
// Stage E fields (feature "attachment_kinds", all additive): Kind is
// image | audio | voice | video | document | sticker ("" reads as
// image, the v1 assumption); Name is the original filename; Size in
// bytes; DurationMS for audio/voice/video; Caption joins the message
// text host-side when it isn't already there.
type Attachment struct {
	MimeType   string `json:"mime_type"`
	Path       string `json:"path"`
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name,omitempty"`
	Size       int64  `json:"size,omitempty"`
	DurationMS int    `json:"duration_ms,omitempty"`
	Caption    string `json:"caption,omitempty"`
}

// ---- connector → host ----

// HelloFromConn is the first frame on the connector's stdout.
type HelloFromConn struct {
	Type         string       `json:"type"` // "hello"
	Name         string       `json:"name"`
	Version      string       `json:"version,omitempty"`
	ProtocolMin  int          `json:"protocol_min"`
	ProtocolMax  int          `json:"protocol_max"`
	Capabilities Capabilities `json:"capabilities"`
}

// ConnectedFromConn answers ConnectFromHost on success with the
// bot's own identity on the service.
type ConnectedFromConn struct {
	Type     string `json:"type"` // "connected"
	ID       string `json:"id,omitempty"`
	Username string `json:"username,omitempty"`
}

// ConnectErrorFromConn answers ConnectFromHost when credentials are
// permanently broken (bad token), not a transient blip — transient
// failures are the connector's job to retry internally.
type ConnectErrorFromConn struct {
	Type  string `json:"type"` // "connect_error"
	Error string `json:"error"`
}

// Entity is one span of minimum-viable markup on a message's text
// (stage B, protocol 2). Offsets and lengths are in Unicode code
// points over Text. "bot_mention" is the load-bearing kind (group
// mention-gating); "mention" (with UserID), "code", and "link" are the
// cheap rest — formatting fidelity is deliberately NOT modeled. An
// entity with Offset and Length both zero means "presence known,
// location unknown" (e.g. a reply-triggered mention with no textual
// token).
type Entity struct {
	Kind   string `json:"kind"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	UserID string `json:"user_id,omitempty"`
}

// MembershipChat identifies the chat a membership change concerns.
type MembershipChat struct {
	ID    string `json:"id"`
	Kind  string `json:"kind,omitempty"`
	Title string `json:"title,omitempty"`
}

// ChatMembershipFromConn reports the BOT's own admission changing in a
// chat (stage B, protocol 2; declared via feature "chat_membership"):
// added when the bot lands in a group, removed when it is kicked. This
// is the trust hook group admission wants — the owner hears "the bot
// was added to <chat> by <user>" the moment it happens instead of at
// the first awkward message. Full rosters and member-change streams
// stay out of v2.
type ChatMembershipFromConn struct {
	Type       string         `json:"type"` // "chat_membership"
	Chat       MembershipChat `json:"chat"`
	ScopeID    string         `json:"scope_id,omitempty"`
	Change     string         `json:"change"` // "added" | "removed"
	ByUserID   string         `json:"by_user_id,omitempty"`
	ByUsername string         `json:"by_username,omitempty"`
}

// MessageFromConn is one normalized inbound chat message.
//
// Protocol 2 semantics: ID is the message's own service id (stable and
// unique within its chat; REQUIRED at protocol 2), TS is when the
// service says it happened (unix milliseconds), and ReplyTo is the id
// of the message this one replies TO. Protocol 1 predates message
// identity and conflated the two: v1 connectors put their message's
// own id in ReplyTo (so hosts could reply-thread) — hosts normalize
// that shape (connhost moves it to ID). ChatKind/ChatTitle describe
// the chat ("dm", "group", "thread", "channel"; kind "" reads as dm,
// the v1 assumption).
type MessageFromConn struct {
	Type        string       `json:"type"` // "message"
	ID          string       `json:"id,omitempty"`
	TS          int64        `json:"ts,omitempty"`
	ChatID      string       `json:"chat_id"`
	ChatKind    string       `json:"chat_kind,omitempty"`
	ChatTitle   string       `json:"chat_title,omitempty"`
	ScopeID     string       `json:"scope_id,omitempty"`
	UserID      string       `json:"user_id"`
	Username    string       `json:"username,omitempty"`
	ReplyTo     string       `json:"reply_to,omitempty"`
	Text        string       `json:"text,omitempty"`
	Entities    []Entity     `json:"entities,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// ResultFromConn acknowledges one command. MessageID (protocol 2) is
// the id of the message the command created — the prerequisite for
// outbound edits, reactions, and ask resolution. ChatID (stage I)
// answers thread_start with the NEW thread-kind chat's id.
type ResultFromConn struct {
	Type      string `json:"type"` // "result"
	ID        string `json:"id"`
	MessageID string `json:"message_id,omitempty"`
	ChatID    string `json:"chat_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

// WarnFromConn surfaces an operational log line ("gateway
// reconnecting") without ending the session.
type WarnFromConn struct {
	Type    string `json:"type"` // "warn"
	Message string `json:"message"`
}

// MessageEditedFromConn reports a user editing an earlier message
// (stage D, feature "edits_in"). ID references the ORIGINAL message —
// connectors own latest-wins collapsing of edit chains. Deviation
// from the design sketch, recorded there: chat_id rides every stage-D
// frame so hosts can route without a global message index.
type MessageEditedFromConn struct {
	Type     string   `json:"type"` // "message_edited"
	ChatID   string   `json:"chat_id"`
	ID       string   `json:"id"`
	TS       int64    `json:"ts,omitempty"`
	Text     string   `json:"text"`
	Entities []Entity `json:"entities,omitempty"`
}

// MessageDeletedFromConn reports a user deleting a message (stage D,
// feature "deletes_in").
type MessageDeletedFromConn struct {
	Type   string `json:"type"` // "message_deleted"
	ChatID string `json:"chat_id"`
	ID     string `json:"id"`
}

// ReactionFromConn is one reaction toggling on a message (stage D,
// feature "reactions_in"). Key is an OPAQUE STRING, not "an emoji" —
// unicode emoji is the only interoperable subset; custom emoji ride
// platform ids. Removal is first-class (recomputing by absence does
// not work on any surveyed platform). Reactions are a LOSSY channel:
// nothing security-relevant may hinge on them.
type ReactionFromConn struct {
	Type      string `json:"type"` // "reaction"
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id"`
	UserID    string `json:"user_id"`
	Username  string `json:"username,omitempty"`
	Key       string `json:"key"`
	Removed   bool   `json:"removed,omitempty"`
}

// AnswerFromConn is one user's response to an in-flight ask (stage G,
// protocol 2, feature "asks"). Not a command result: answers arrive
// zero or more times, whenever users interact, until the host sends
// ask_close. Attestation grades how sure the PLATFORM is about who
// answered: "attested" (Discord button interactions, telegram callback
// queries — the service proves the identity) vs "best_effort" (parsed
// text, reaction streams; also the default when empty). The host
// re-filters restrict_to regardless — the wire contract is answers-
// only-from, enforced twice.
type AnswerFromConn struct {
	Type        string `json:"type"` // "answer"
	AskID       string `json:"ask_id"`
	Key         string `json:"key"`
	UserID      string `json:"user_id"`
	Username    string `json:"username,omitempty"`
	Attestation string `json:"attestation,omitempty"`
}

// Attestation grades carried on answer frames.
const (
	AttestationAttested   = "attested"
	AttestationBestEffort = "best_effort"
)

// ---- host → connector ----

// HelloAckFromHost answers hello with the negotiated protocol version
// and the connector's scratch directory for inbound attachments.
// ZotVersion and TervaVersion carry the SAME value under both naming
// eras (the rename bridge, docs/plans/rename-terva.md); zot_version
// stays until the connector-SDK deprecation window closes.
type HelloAckFromHost struct {
	Type         string `json:"type"` // "hello_ack"
	Protocol     int    `json:"protocol"`
	ZotVersion   string `json:"zot_version,omitempty"`
	TervaVersion string `json:"terva_version,omitempty"`
	DataDir      string `json:"data_dir,omitempty"`
	// Capabilities (protocol 2) advertises what the HOST consumes —
	// its Features list is the peer-declared gate for optional
	// constructs, the mirror of hello's.
	Capabilities *Capabilities `json:"capabilities,omitempty"`
}

// ConnectFromHost asks the connector to establish its service session
// and answer with connected or connect_error.
type ConnectFromHost struct {
	Type string `json:"type"` // "connect"
}

// Speaker is an alternate outbound identity (stage H, protocol 2,
// features "speaker:full" / "speaker:name_only"): personas and the
// --play cast want DIFFERENT characters speaking in one chat. Key is
// stable across the session and keys whatever per-speaker state the
// connector maintains (a managed webhook, an MSC4144 profile);
// Name is the display name; AvatarPath is a local image file (the
// attachment same-host convention), used only by connectors that
// declared "speaker:full". Asks NEVER carry a speaker — they come
// from the bot principal (structurally: AskFromHost has no field).
type Speaker struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	AvatarPath string `json:"avatar_path,omitempty"`
}

// SendFromHost delivers one outbound text message (pre-chunked by the
// host to the declared max_text_len). Speaker is only ever set for
// connectors that declared a speaker feature; the host renders the
// prefix fallback itself for everyone else. A speaker message's
// reply_to may degrade platform-side (Discord webhooks cannot reply).
type SendFromHost struct {
	Type    string   `json:"type"` // "send"
	ID      string   `json:"id"`
	ChatID  string   `json:"chat_id"`
	ReplyTo string   `json:"reply_to,omitempty"`
	Text    string   `json:"text"`
	Speaker *Speaker `json:"speaker,omitempty"`
}

// SendImageFromHost uploads a local file as an inline image.
type SendImageFromHost struct {
	Type    string `json:"type"` // "send_image"
	ID      string `json:"id"`
	ChatID  string `json:"chat_id"`
	Path    string `json:"path"`
	Caption string `json:"caption,omitempty"`
}

// SendFileFromHost uploads a local file as a raw document.
type SendFileFromHost struct {
	Type    string `json:"type"` // "send_file"
	ID      string `json:"id"`
	ChatID  string `json:"chat_id"`
	Path    string `json:"path"`
	Caption string `json:"caption,omitempty"`
}

// TypingFromHost asserts the typing indicator once. Fire-and-forget:
// no ID, no result.
type TypingFromHost struct {
	Type   string `json:"type"` // "typing"
	ChatID string `json:"chat_id"`
}

// AskOption is one choice on an ask. Keys ride the wire; the connector
// picks the widget (buttons, inline keyboard, pre-seeded reactions,
// numbered text) — never the host. Style is a rendering hint ("affirm",
// "deny", or "" for neutral); Hint is an emoji for reaction-rendered
// options. Neither carries semantics — the key does. Reaction-rendered
// options expose Hint in CLEARTEXT even on E2EE services, so never
// encode sensitive content in Hint or Key; the message Text carries the
// substance.
type AskOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Style string `json:"style,omitempty"`
	Hint  string `json:"hint,omitempty"`
}

// AskFromHost renders one constrained question in a chat (stage G,
// protocol 2; only sent when the connector's features include "asks").
// The command ID doubles as the ask's identity: answer frames carry it
// as ask_id, and ask_close withdraws it. The result acknowledges the
// RENDERING (message_id of the posted question); answers arrive
// separately, whenever users interact. RestrictTo lists the user ids
// allowed to answer — connectors filter service-side where they can
// (no platform hides a button per-user), and the host re-filters
// regardless. ExpiresMS is advisory (the host owns the actual timeout
// and always closes explicitly).
type AskFromHost struct {
	Type       string      `json:"type"` // "ask"
	ID         string      `json:"id"`
	ChatID     string      `json:"chat_id"`
	ReplyTo    string      `json:"reply_to,omitempty"`
	Text       string      `json:"text"`
	Options    []AskOption `json:"options"`
	RestrictTo []string    `json:"restrict_to,omitempty"`
	ExpiresMS  int         `json:"expires_ms,omitempty"`
}

// EditFromHost rewrites the BOT's own earlier message (stage D,
// feature "edits_out") — the fix-up and streaming-reply primitive.
// Always references the original message id; respect the connector's
// min_edit_interval_ms when streaming. Answered by result.
type EditFromHost struct {
	Type      string `json:"type"` // "edit"
	ID        string `json:"id"`
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}

// ReactFromHost toggles the bot's reaction on a message (stage D,
// feature "reactions_out") — a lightweight ack without a turn. terva
// only emits unicode-emoji keys. Answered by result.
type ReactFromHost struct {
	Type      string `json:"type"` // "react"
	ID        string `json:"id"`
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id"`
	Key       string `json:"key"`
	Remove    bool   `json:"remove,omitempty"`
}

// DeleteFromHost retracts the bot's own message (stage D, feature
// "deletes_out"). Answered by result.
type DeleteFromHost struct {
	Type      string `json:"type"` // "delete"
	ID        string `json:"id"`
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id"`
}

// ThreadStartFromHost opens a work-stream thread (stage I, protocol
// 2, feature "threads_out"): dispatch/swarm work wants a thread per
// task so a busy chat stays readable. The result's chat_id is a NEW
// chat (kind "thread"); everything else — sends, asks, speakers —
// targets it like any chat. FromMessageID anchors the thread to a
// message where the service supports it; connectors without an
// anchorless mode may require it.
type ThreadStartFromHost struct {
	Type          string `json:"type"` // "thread_start"
	ID            string `json:"id"`
	ChatID        string `json:"chat_id"`
	FromMessageID string `json:"from_message_id,omitempty"`
	Name          string `json:"name"`
}

// AskCloseFromHost ends an ask: first valid answer won, the timeout
// fired, or the turn was cancelled. The connector withdraws the
// controls and renders Outcome into the question message (the audit
// trail lives in the channel, not just the log). Idempotent — closing
// an unknown or already-closed ask succeeds.
type AskCloseFromHost struct {
	Type    string `json:"type"` // "ask_close"
	ID      string `json:"id"`
	AskID   string `json:"ask_id"`
	Outcome string `json:"outcome,omitempty"`
}

// ShutdownFromHost asks the connector to exit; the host closes stdin
// right after and escalates to SIGTERM/SIGKILL on a deadline.
type ShutdownFromHost struct {
	Type string `json:"type"` // "shutdown"
}

// Encode marshals v and appends the LF frame terminator.
func Encode(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
