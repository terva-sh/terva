// Package connsdk is the author SDK for external terva chat
// connectors: implement the Transport interface (pure transport —
// wire protocol, auth, normalization; terva owns pairing, queueing,
// chunking, and commands) and hand it to Main. The SDK speaks the
// connproto JSON-lines protocol over stdio and dispatches the
// lifecycle verbs (`run`, `setup`, `status`, `reset`, `configured`)
// that `terva bot` invokes, so a minimal Go connector is ~50 lines.
//
// The protocol is small enough that any language works without an
// SDK; see docs/connectors.md for the frame reference.
package connsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/connproto"
	"terva.sh/terva/packages/envcompat"
)

// Identity is the connector's own account on the service, resolved
// at Connect time.
type Identity struct {
	ID       string
	Username string
}

// Capabilities mirrors the service's formatting limits; declared
// once, before connecting.
type Capabilities struct {
	// MaxTextLen is the outbound chunking threshold in bytes; terva
	// splits longer replies. 0 = no limit.
	MaxTextLen int
	// TypingRefresh is how often terva re-asserts the typing
	// indicator while a turn runs; 0 = no indicator.
	TypingRefresh time.Duration
	SendsImages   bool
	SendsFiles    bool
	// Features declares protocol-2 feature strings this connector
	// produces ("message_ids", "chat_kinds", …). Declare only what the
	// transport actually implements; inbound identity fields are
	// presence-evident, so an empty list is fine for stage A.
	Features []string
	// MinEditInterval (with "edits_out") tells the host how fast it
	// may stream edits to one message; 0 = no declared limit.
	MinEditInterval time.Duration
}

// Attachment is one inbound file. Write the bytes under the DataDir
// terva assigns (see Session.DataDir) and pass the path; terva takes
// ownership (images are read and deleted; other kinds are moved).
//
// Stage E fields (declare "attachment_kinds"): Kind is image | audio |
// voice | video | document | sticker ("" reads as image); Name the
// original filename; Size in bytes; Duration for audio/voice/video;
// Caption joins the message text host-side when it isn't already
// there.
type Attachment struct {
	MimeType string
	Path     string
	Kind     string
	Name     string
	Size     int64
	Duration time.Duration
	Caption  string
}

// Message is one normalized inbound chat message.
//
// Protocol-2 identity (stage A, docs/proposals/connector-protocol-v2.md):
// ID is the message's own service id (stable within its chat — mint one
// if the service has none), TS is when it happened (unix milliseconds),
// and ReplyTo is the id of the message this one replies TO. ChatKind
// ("dm", "group", "thread", "channel"; "" reads as dm) and ChatTitle
// describe the chat. A transport that fills ID gets true reply
// threading; one that leaves it empty behaves like a v1 connector.
type Message struct {
	ID          string
	TS          int64
	ChatID      string
	ChatKind    string
	ChatTitle   string
	ScopeID     string // container the chat belongs to (e.g. Discord guild); "" = scopeless
	UserID      string
	Username    string
	ReplyTo     string
	Text        string
	Entities    []Entity
	Attachments []Attachment
}

// Entity is one span of markup on Text (stage B; declare "entities" in
// Capabilities.Features when the transport emits them). Offsets in
// Unicode code points. Kind "bot_mention" is the load-bearing one —
// it drives the host's group mention-gating; "mention" (with UserID),
// "code", and "link" are the cheap rest. Offset and Length both zero
// means "present but not locatable in the text".
type Entity struct {
	Kind   string
	Offset int
	Length int
	UserID string
}

// Membership is the bot's OWN admission changing in a chat (stage B;
// declare "chat_membership" when the transport emits it): Change is
// "added" or "removed", By* says who did it where the service reports
// that.
type Membership struct {
	ChatID     string
	ChatKind   string
	ChatTitle  string
	ScopeID    string // container the chat belongs to (e.g. Discord guild); "" = scopeless
	Change     string
	ByUserID   string
	ByUsername string
}

// MembershipSource is the optional Transport upgrade for admission
// events: ReceiveMembership mirrors Receive (block, deliver until ctx
// ends) on its own goroutine. Its death is logged but non-fatal — a
// connector losing membership enrichment must not take the message
// stream down with it.
type MembershipSource interface {
	ReceiveMembership(ctx context.Context, deliver func(Membership)) error
}

// MessageEdited reports a user editing an earlier message (stage D;
// declare "edits_in"). ID references the ORIGINAL message — collapse
// edit chains latest-wins before delivering.
type MessageEdited struct {
	ChatID   string
	ID       string
	TS       int64
	Text     string
	Entities []Entity
}

// MessageDeleted reports a user deleting a message (stage D; declare
// "deletes_in").
type MessageDeleted struct {
	ChatID string
	ID     string
}

// Reaction is one reaction toggling on a message (stage D; declare
// "reactions_in"). Key is opaque — unicode emoji is the interoperable
// subset; key custom emoji on their platform ID, not their name.
// Removal is first-class; reactions are LOSSY, so never build
// security on them.
type Reaction struct {
	ChatID    string
	MessageID string
	UserID    string
	Username  string
	Key       string
	Removed   bool
}

// ChatEventSink receives the stage-D inbound streams; any handler may
// be nil (the SDK drops what the sink doesn't take).
type ChatEventSink struct {
	Edited   func(MessageEdited)
	Deleted  func(MessageDeleted)
	Reaction func(Reaction)
}

// ChatEventSource is the optional Transport upgrade for the stage-D
// inbound streams: ReceiveChatEvents mirrors Receive (block, deliver
// until ctx ends) on its own goroutine; its death is logged but
// non-fatal, like MembershipSource.
type ChatEventSource interface {
	ReceiveChatEvents(ctx context.Context, sink ChatEventSink) error
}

// MessageEditor / MessageReactor / MessageDeleter are the optional
// Transport upgrades for the stage-D outbound commands (declare
// "edits_out" — with Capabilities.MinEditInterval — "reactions_out",
// "deletes_out" respectively).
type MessageEditor interface {
	EditMessage(ctx context.Context, chatID, messageID, text string) error
}

type MessageReactor interface {
	React(ctx context.Context, chatID, messageID, key string, remove bool) error
}

type MessageDeleter interface {
	DeleteMessage(ctx context.Context, chatID, messageID string) error
}

// Speaker is an alternate outbound identity (stage H, features
// "speaker:full" / "speaker:name_only"): the host's personas and
// --play cast speaking as different characters in one chat. Key is
// stable across the session and keys whatever per-speaker state the
// transport maintains (a managed webhook, a per-message profile);
// AvatarPath is a local image file, only sent to "speaker:full"
// transports.
type Speaker struct {
	Key        string
	Name       string
	AvatarPath string
}

// Outgoing is one outbound text message, pre-chunked by terva.
// Speaker is only ever set when this connector declared a speaker
// feature — the host renders its own prefix fallback otherwise.
type Outgoing struct {
	ChatID  string
	ReplyTo string
	Text    string
	Speaker *Speaker
}

// Transport is the service-specific half a connector author
// implements. Reconnection/backoff inside Receive is the transport's
// job; a returned error means permanently broken (bad token), not a
// blip — the process exits and terva's restart budget takes over.
type Transport interface {
	// Connect validates credentials and resolves the bot's identity.
	Connect(ctx context.Context) (Identity, error)
	// Receive blocks, delivering normalized inbound messages until
	// ctx ends.
	Receive(ctx context.Context, deliver func(Message)) error
	// Send delivers one outbound text message.
	Send(ctx context.Context, out Outgoing) error
	// SendImage / SendFile upload a local file. Only called when the
	// corresponding capability is set.
	SendImage(ctx context.Context, chatID, path, caption string) error
	SendFile(ctx context.Context, chatID, path, caption string) error
	// Typing asserts the typing indicator once. Only called when
	// Capabilities.TypingRefresh > 0.
	Typing(ctx context.Context, chatID string) error
}

// Session is what the host handshake established, passed to
// NewTransport.
type Session struct {
	// DataDir is the host-assigned scratch directory for inbound
	// attachment files.
	DataDir string
	// TervaVersion is the host's version string (informational).
	ZotVersion string // rename:keep — public SDK API
	// Protocol is the negotiated wire version (1 against older hosts;
	// see connproto). At ≥2, fill Message.ID and treat ReplyTo as
	// in-reply-to; at 1 the host expects the v1 shape (own id in
	// ReplyTo), and the SDK downgrades outbound frames accordingly.
	Protocol int
	// HostFeatures is what the host declared it consumes (protocol 2);
	// optional constructs outside this set are wasted bytes at best.
	HostFeatures []string
}

// MessageIDSender is the optional Transport upgrade for protocol 2:
// Send variants that return the created message's id, which the SDK
// reports in the command's result (the prerequisite for outbound
// edits, reactions, and ask resolution). Implement it alongside Send —
// the SDK prefers it when present.
type MessageIDSender interface {
	SendWithID(ctx context.Context, out Outgoing) (messageID string, err error)
}

// AskOption is one choice on an Ask. Key is the semantic identity the
// answer carries back; Label is the human text. Style ("affirm",
// "deny", "") and Hint (an emoji, for reaction-rendered options) are
// rendering hints. The transport picks the widget — buttons, an inline
// keyboard, pre-seeded reactions, numbered text — never the host.
type AskOption struct {
	Key   string
	Label string
	Style string
	Hint  string
}

// Ask is one constrained question the host wants rendered in a chat.
// ID identifies the ask for answer routing and CloseAsk (embed it in
// button custom_ids and the like — it is opaque and unique). Expires
// is advisory; the host owns the real timeout and always closes
// explicitly.
type Ask struct {
	ID         string
	ChatID     string
	ReplyTo    string
	Text       string
	Options    []AskOption
	RestrictTo []string
	Expires    time.Duration
}

// Answer is one user's response to an Ask. Attestation says how sure
// the PLATFORM is about who answered: AttestationAttested for
// server-proven identity (button interactions, callback queries),
// AttestationBestEffort for parsed text or reaction streams.
type Answer struct {
	AskID       string
	Key         string
	UserID      string
	Username    string
	Attestation string
}

// Attestation grades for Answer.
const (
	AttestationAttested   = "attested"
	AttestationBestEffort = "best_effort"
)

// SpeakerSender is the optional Transport upgrade for alternate
// outbound identities (declare "speaker:full" or "speaker:name_only"
// in Capabilities.Features alongside implementing this). Called
// instead of Send/SendWithID when the outgoing message carries a
// Speaker; returns the created message's id. Platform constraints are
// the transport's to absorb (Discord webhook messages can't be real
// replies — drop or quote the ReplyTo), except one that is
// structural: asks never carry a speaker.
type SpeakerSender interface {
	SendAsSpeaker(ctx context.Context, out Outgoing) (messageID string, err error)
}

// Threader is the optional Transport upgrade for work-stream threads
// (declare "threads_out" in Capabilities.Features alongside
// implementing this). StartThread opens a thread and returns its NEW
// chat id — messages inside it arrive as ordinary Messages with that
// chat id (kind "thread"); sends target it like any chat.
// fromMessageID anchors the thread where the service supports it and
// may be empty; a transport with no anchorless mode returns an error.
type Threader interface {
	StartThread(ctx context.Context, chatID, fromMessageID, name string) (threadChatID string, err error)
}

// TypingStopper is the optional Transport upgrade for withdrawing the
// typing indicator (protocol 2, feature "typing_stop" — declare it in
// Capabilities.Features alongside implementing this). The host sends
// one stop after each reply; a service whose indicator times out on
// its own within a few seconds needs neither.
type TypingStopper interface {
	StopTyping(ctx context.Context, chatID string) error
}

// Asker is the optional Transport upgrade for interactive asks
// (protocol 2, feature "asks" — declare it in Capabilities.Features
// alongside implementing this). Ask renders the question with the best
// widget the service has and returns the rendered message's id; it
// must NOT wait for a human. Answers are pushed to deliver — from any
// goroutine, zero or more times — until CloseAsk for the same ask id,
// after which the transport stops delivering and withdraws the
// controls, rendering outcome into the question message. Where the
// platform allows, the transport also filters RestrictTo service-side
// (an ephemeral "not for you" beats silence); the SDK and the host
// both re-filter regardless.
type Asker interface {
	Ask(ctx context.Context, a Ask, deliver func(Answer)) (messageID string, err error)
	CloseAsk(ctx context.Context, askID, outcome string) error
}

// Config describes the connector to Main.
type Config struct {
	Name         string
	Version      string
	Capabilities Capabilities

	// NewTransport builds the transport when the host asks to
	// connect (`run` verb). Required.
	NewTransport func(s Session) (Transport, error)

	// Optional lifecycle verbs, invoked interactively on the user's
	// tty by `terva bot setup` / `status` / `reset`.
	Setup  func() error
	Status func() (string, error)
	Reset  func() error
	// Configured backs `terva bot`'s configured probe; nil means
	// always configured (e.g. config via env vars).
	Configured func() bool

	// Secrets, when set, declares this connector's sealed state in the
	// handshake, so the host can re-seal it during a key rotation without
	// holding the connector's key. Set it to the same SealedState the
	// connector loads and saves with.
	//
	// The handshake is the ONLY source the host trusts for a recipient: the
	// state file is writable by anything that can write $TERVA_HOME, so
	// reading it from there would turn write access into future read access.
	Secrets *SealedState
}

// secretsDecl builds the handshake declaration, or nil when this connector
// keeps no sealed state or has not been configured yet. A connector with no key
// of its own has nothing to declare — announcing a recipient it does not have
// would register a component terva could never re-seal to.
func (c Config) secretsDecl() *connproto.SecretsDecl {
	if c.Secrets == nil || len(c.Secrets.Paths) == 0 {
		return nil
	}
	r, err := c.Secrets.Recipient()
	if err != nil || r == "" {
		return nil
	}
	return &connproto.SecretsDecl{Recipient: r, Paths: c.Secrets.Paths}
}

// Main dispatches the lifecycle verb (the LAST argv element — terva
// appends it after any manifest args) and exits. Call it from your
// connector's func main.
func Main(cfg Config) {
	if len(os.Args) < 2 {
		usage(cfg.Name)
		os.Exit(2)
	}
	switch verb := os.Args[len(os.Args)-1]; verb {
	case "run":
		err := Serve(cfg, os.Stdin, os.Stdout, os.Stderr)
		if err != nil {
			fmt.Fprintln(os.Stderr, cfg.Name+":", err)
			os.Exit(1)
		}
	case "setup":
		if cfg.Setup == nil {
			fmt.Println("nothing to set up for", cfg.Name)
			return
		}
		if err := cfg.Setup(); err != nil {
			fmt.Fprintln(os.Stderr, cfg.Name+": setup:", err)
			os.Exit(1)
		}
	case "status":
		if cfg.Status == nil {
			fmt.Println(cfg.Name, "reports no status")
			return
		}
		text, err := cfg.Status()
		if err != nil {
			fmt.Fprintln(os.Stderr, cfg.Name+": status:", err)
			os.Exit(1)
		}
		fmt.Println(text)
	case "reset":
		if cfg.Reset == nil {
			fmt.Println("nothing to reset for", cfg.Name)
			return
		}
		if err := cfg.Reset(); err != nil {
			fmt.Fprintln(os.Stderr, cfg.Name+": reset:", err)
			os.Exit(1)
		}
	case "configured":
		if cfg.Configured != nil && !cfg.Configured() {
			os.Exit(1)
		}
	default:
		usage(cfg.Name)
		os.Exit(2)
	}
}

func usage(name string) {
	fmt.Fprintf(os.Stderr, "%s — terva chat connector\nusage: %s {run|setup|status|reset|configured}\n(`run` speaks the terva connector protocol on stdio; the rest are for `terva bot`)\n", name, name)
}

// Serve runs the protocol loop: hello/hello_ack, then frames until
// `in` closes or the host says shutdown. Main runs it over stdio; the
// extension SDK (packages/agent/ext) runs the SAME engine over an
// in-process pipe pair when an extension also plays the connector role
// (the connproto frames then ride the extension wire inside `chat`
// envelope frames — docs/proposals/connector-extensions.md). Only the
// carrier differs; there is exactly one implementation of the
// connector protocol's author side.
//
// Config's lifecycle verbs (Setup/Status/Reset/Configured) are Main's
// business; Serve only uses Name, Version, Capabilities, and
// NewTransport.
func Serve(cfg Config, in io.Reader, out io.Writer, errlog io.Writer) error {
	if cfg.NewTransport == nil {
		return fmt.Errorf("connsdk: Config.NewTransport is required")
	}
	w := &frameWriter{w: out}

	if err := w.write(connproto.HelloFromConn{
		Type:        "hello",
		Name:        cfg.Name,
		Version:     cfg.Version,
		ProtocolMin: connproto.ProtocolVersion,
		ProtocolMax: connproto.ProtocolMax,
		Capabilities: connproto.Capabilities{
			MaxTextLen:        cfg.Capabilities.MaxTextLen,
			TypingRefreshMS:   int(cfg.Capabilities.TypingRefresh / time.Millisecond),
			SendsImages:       cfg.Capabilities.SendsImages,
			SendsFiles:        cfg.Capabilities.SendsFiles,
			Features:          cfg.Capabilities.Features,
			MinEditIntervalMS: int(cfg.Capabilities.MinEditInterval / time.Millisecond),
		},
		Secrets: cfg.secretsDecl(),
	}); err != nil {
		return err
	}

	fr := connproto.NewFrameReader(in, func(msg string) {
		if errlog != nil {
			fmt.Fprintf(errlog, "[connsdk] %s\n", msg)
		}
	})

	ackLine, err := fr.Read()
	if err != nil {
		return fmt.Errorf("host closed the stream before hello_ack: %v", err)
	}
	var ack connproto.HelloAckFromHost
	if err := json.Unmarshal(ackLine, &ack); err != nil || ack.Type != "hello_ack" {
		return fmt.Errorf("expected hello_ack, got %q", ackLine)
	}
	if ack.Protocol < connproto.ProtocolVersion || ack.Protocol > connproto.ProtocolMax {
		return fmt.Errorf("host negotiated protocol %d; this SDK speaks %d..%d", ack.Protocol, connproto.ProtocolVersion, connproto.ProtocolMax)
	}
	// Renamed hosts send terva_version (and keep terva_version for the
	// deprecation window); either spelling fills the same field.
	hostVersion := ack.TervaVersion
	if hostVersion == "" {
		hostVersion = ack.ZotVersion // rename:keep — frozen wire field
	}
	session := Session{DataDir: ack.DataDir, ZotVersion: hostVersion, Protocol: ack.Protocol} // rename:keep — public SDK API
	if ack.Capabilities != nil {
		session.HostFeatures = ack.Capabilities.Features
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// receiveErr carries a fatal transport failure out of the
	// receive goroutine; the serve loop can't notice it on its own
	// because it is blocked reading stdin.
	receiveErr := make(chan error, 1)

	var transport Transport
	var identity Identity
	connected := false

	// The frame reader runs on its own goroutine so a fatal transport
	// failure ends the session IMMEDIATELY — the serve loop must not sit
	// blocked on a read while its transport is dead (the host may have
	// nothing to say for hours; a prompt exit is what triggers its
	// restart budget, or the extension tunnel's chat_down). done stops
	// the reader's hand-off on early return so it never leaks mid-send.
	lines := make(chan []byte)
	scanErr := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer close(lines)
		for {
			line, err := fr.Read()
			if err != nil {
				if err != io.EOF {
					select {
					case scanErr <- err:
					default:
					}
				}
				return
			}
			select {
			case lines <- line:
			case <-done:
				return
			}
		}
	}()

	for {
		var line []byte
		select {
		case err := <-receiveErr:
			// Fatal transport failure: report it and end the session.
			_ = w.write(connproto.WarnFromConn{Type: "warn", Message: "receive failed: " + err.Error()})
			return err
		case l, ok := <-lines:
			if !ok {
				// stdin closed: the host is gone; stop receiving and exit.
				select {
				case err := <-scanErr:
					return err
				default:
					return nil
				}
			}
			line = l
		}

		var frame connproto.Frame
		if err := json.Unmarshal(line, &frame); err != nil {
			fmt.Fprintf(errlog, "malformed frame from host: %v\n", err)
			continue
		}

		switch frame.Type {
		case "connect":
			if connected {
				_ = w.write(connproto.ConnectedFromConn{Type: "connected", ID: identity.ID, Username: identity.Username})
				continue
			}
			t, err := cfg.NewTransport(session)
			if err != nil {
				_ = w.write(connproto.ConnectErrorFromConn{Type: "connect_error", Error: err.Error()})
				continue
			}
			id, err := t.Connect(ctx)
			if err != nil {
				_ = w.write(connproto.ConnectErrorFromConn{Type: "connect_error", Error: err.Error()})
				continue
			}
			transport, identity, connected = t, id, true
			if err := w.write(connproto.ConnectedFromConn{Type: "connected", ID: id.ID, Username: id.Username}); err != nil {
				return err
			}
			go func() {
				err := t.Receive(ctx, func(m Message) {
					atts := make([]connproto.Attachment, 0, len(m.Attachments))
					for _, a := range m.Attachments {
						atts = append(atts, connproto.Attachment{
							MimeType: a.MimeType, Path: a.Path,
							Kind: a.Kind, Name: a.Name, Size: a.Size,
							DurationMS: int(a.Duration / time.Millisecond),
							Caption:    a.Caption,
						})
					}
					frame := connproto.MessageFromConn{
						Type: "message", ChatID: m.ChatID, UserID: m.UserID, Username: m.Username,
						ReplyTo: m.ReplyTo, Text: m.Text, Attachments: atts,
					}
					if session.Protocol >= 2 {
						frame.ID = m.ID
						frame.TS = m.TS
						frame.ChatKind = m.ChatKind
						frame.ChatTitle = m.ChatTitle
						frame.ScopeID = m.ScopeID
						for _, e := range m.Entities {
							frame.Entities = append(frame.Entities, connproto.Entity{
								Kind: e.Kind, Offset: e.Offset, Length: e.Length, UserID: e.UserID,
							})
						}
					} else if m.ID != "" {
						// Protocol-1 downgrade: the old shape carries the
						// message's OWN id in reply_to (the reply token
						// v1 hosts echo back); the true in-reply-to has
						// no v1 home and is dropped.
						frame.ReplyTo = m.ID
					}
					_ = w.write(frame)
				})
				if err != nil && ctx.Err() == nil {
					receiveErr <- err // buffered(1); the serve loop selects on it
				}
			}()
			if es, has := t.(ChatEventSource); has && session.Protocol >= 2 {
				go func() {
					err := es.ReceiveChatEvents(ctx, ChatEventSink{
						Edited: func(ev MessageEdited) {
							frame := connproto.MessageEditedFromConn{
								Type: "message_edited", ChatID: ev.ChatID, ID: ev.ID, TS: ev.TS, Text: ev.Text,
							}
							for _, e := range ev.Entities {
								frame.Entities = append(frame.Entities, connproto.Entity{
									Kind: e.Kind, Offset: e.Offset, Length: e.Length, UserID: e.UserID,
								})
							}
							_ = w.write(frame)
						},
						Deleted: func(ev MessageDeleted) {
							_ = w.write(connproto.MessageDeletedFromConn{
								Type: "message_deleted", ChatID: ev.ChatID, ID: ev.ID,
							})
						},
						Reaction: func(ev Reaction) {
							_ = w.write(connproto.ReactionFromConn{
								Type: "reaction", ChatID: ev.ChatID, MessageID: ev.MessageID,
								UserID: ev.UserID, Username: ev.Username, Key: ev.Key, Removed: ev.Removed,
							})
						},
					})
					if err != nil && ctx.Err() == nil {
						_ = w.write(connproto.WarnFromConn{Type: "warn",
							Message: "chat-event stream ended: " + err.Error()})
					}
				}()
			}
			if ms, has := t.(MembershipSource); has && session.Protocol >= 2 {
				go func() {
					err := ms.ReceiveMembership(ctx, func(mb Membership) {
						_ = w.write(connproto.ChatMembershipFromConn{
							Type: "chat_membership",
							Chat: connproto.MembershipChat{
								ID: mb.ChatID, Kind: mb.ChatKind, Title: mb.ChatTitle,
							},
							ScopeID: mb.ScopeID,
							Change:  mb.Change, ByUserID: mb.ByUserID, ByUsername: mb.ByUsername,
						})
					})
					if err != nil && ctx.Err() == nil {
						// Non-fatal: the message stream is the session's
						// heartbeat; losing membership enrichment only
						// costs the owner the early admission ping.
						_ = w.write(connproto.WarnFromConn{Type: "warn",
							Message: "membership stream ended: " + err.Error()})
					}
				}()
			}

		case "send":
			var f connproto.SendFromHost
			if err := json.Unmarshal(line, &f); err != nil {
				w.result(frame.ID, "", fmt.Errorf("malformed %s: %v", frame.Type, err))
				continue
			}
			// Sends run off the serve loop so a slow service call
			// can't delay typing/shutdown frames. transport/connected
			// are captured here, before the goroutine starts; only
			// this loop mutates them.
			t, ok := transport, connected
			go func() {
				out := Outgoing{ChatID: f.ChatID, ReplyTo: f.ReplyTo, Text: f.Text}
				if f.Speaker != nil {
					out.Speaker = &Speaker{Key: f.Speaker.Key, Name: f.Speaker.Name, AvatarPath: f.Speaker.AvatarPath}
				}
				var msgID string
				err := doSend(ctx, t, ok, func(t Transport) error {
					// A speaker send goes to the speaker surface; a
					// transport that declared the feature but lost the
					// interface (author bug) degrades to the same
					// prefix fallback the host would have rendered.
					if out.Speaker != nil {
						if sp, has := t.(SpeakerSender); has {
							var err error
							msgID, err = sp.SendAsSpeaker(ctx, out)
							return err
						}
						name := out.Speaker.Name
						if name == "" {
							name = out.Speaker.Key
						}
						out.Text = "**" + name + ":** " + out.Text
						out.Speaker = nil
					}
					// Prefer the id-returning variant (protocol 2's
					// result.message_id) when the transport has one.
					if ids, has := t.(MessageIDSender); has {
						var err error
						msgID, err = ids.SendWithID(ctx, out)
						return err
					}
					return t.Send(ctx, out)
				})
				w.result(f.ID, msgID, err)
			}()
		case "send_image":
			var f connproto.SendImageFromHost
			if err := json.Unmarshal(line, &f); err != nil {
				w.result(frame.ID, "", fmt.Errorf("malformed %s: %v", frame.Type, err))
				continue
			}
			t, ok := transport, connected
			go func() {
				w.result(f.ID, "", doSend(ctx, t, ok, func(t Transport) error {
					return t.SendImage(ctx, f.ChatID, f.Path, f.Caption)
				}))
			}()
		case "send_file":
			var f connproto.SendFileFromHost
			if err := json.Unmarshal(line, &f); err != nil {
				w.result(frame.ID, "", fmt.Errorf("malformed %s: %v", frame.Type, err))
				continue
			}
			t, ok := transport, connected
			go func() {
				w.result(f.ID, "", doSend(ctx, t, ok, func(t Transport) error {
					return t.SendFile(ctx, f.ChatID, f.Path, f.Caption)
				}))
			}()
		case "typing":
			var f connproto.TypingFromHost
			if err := json.Unmarshal(line, &f); err != nil || !connected {
				continue
			}
			t := transport
			if f.Active != nil && !*f.Active {
				// Only ever sent to a connector that declared
				// typing_stop; a transport without the upgrade is
				// a declaration bug, and treating the stop as a
				// start would be the worst reading of it.
				if st, ok := t.(TypingStopper); ok {
					go func() { _ = st.StopTyping(ctx, f.ChatID) }()
				}
				continue
			}
			go func() { _ = t.Typing(ctx, f.ChatID) }()
		case "ask":
			var f connproto.AskFromHost
			if err := json.Unmarshal(line, &f); err != nil {
				w.result(frame.ID, "", fmt.Errorf("malformed %s: %v", frame.Type, err))
				continue
			}
			t, ok := transport, connected
			go func() {
				var msgID string
				err := doSend(ctx, t, ok, func(t Transport) error {
					asker, has := t.(Asker)
					if !has {
						return fmt.Errorf("connector does not support asks")
					}
					opts := make([]AskOption, 0, len(f.Options))
					for _, o := range f.Options {
						opts = append(opts, AskOption{Key: o.Key, Label: o.Label, Style: o.Style, Hint: o.Hint})
					}
					a := Ask{
						ID: f.ID, ChatID: f.ChatID, ReplyTo: f.ReplyTo, Text: f.Text,
						Options: opts, RestrictTo: f.RestrictTo,
						Expires: time.Duration(f.ExpiresMS) * time.Millisecond,
					}
					var err error
					msgID, err = asker.Ask(ctx, a, func(ans Answer) {
						// Defense in depth before the frame leaves the
						// process: pin the ask id (a transport cannot
						// misroute) and re-check restrict_to (the host
						// filters again regardless).
						if len(f.RestrictTo) > 0 && !containsStr(f.RestrictTo, ans.UserID) {
							return
						}
						_ = w.write(connproto.AnswerFromConn{
							Type: "answer", AskID: f.ID, Key: ans.Key,
							UserID: ans.UserID, Username: ans.Username,
							Attestation: ans.Attestation,
						})
					})
					return err
				})
				w.result(f.ID, msgID, err)
			}()
		case "edit":
			var f connproto.EditFromHost
			if err := json.Unmarshal(line, &f); err != nil {
				w.result(frame.ID, "", fmt.Errorf("malformed %s: %v", frame.Type, err))
				continue
			}
			t, ok := transport, connected
			go func() {
				w.result(f.ID, "", doSend(ctx, t, ok, func(t Transport) error {
					ed, has := t.(MessageEditor)
					if !has {
						return fmt.Errorf("connector does not support edits")
					}
					return ed.EditMessage(ctx, f.ChatID, f.MessageID, f.Text)
				}))
			}()
		case "react":
			var f connproto.ReactFromHost
			if err := json.Unmarshal(line, &f); err != nil {
				w.result(frame.ID, "", fmt.Errorf("malformed %s: %v", frame.Type, err))
				continue
			}
			t, ok := transport, connected
			go func() {
				w.result(f.ID, "", doSend(ctx, t, ok, func(t Transport) error {
					re, has := t.(MessageReactor)
					if !has {
						return fmt.Errorf("connector does not support reactions")
					}
					return re.React(ctx, f.ChatID, f.MessageID, f.Key, f.Remove)
				}))
			}()
		case "delete":
			var f connproto.DeleteFromHost
			if err := json.Unmarshal(line, &f); err != nil {
				w.result(frame.ID, "", fmt.Errorf("malformed %s: %v", frame.Type, err))
				continue
			}
			t, ok := transport, connected
			go func() {
				w.result(f.ID, "", doSend(ctx, t, ok, func(t Transport) error {
					del, has := t.(MessageDeleter)
					if !has {
						return fmt.Errorf("connector does not support deletes")
					}
					return del.DeleteMessage(ctx, f.ChatID, f.MessageID)
				}))
			}()
		case "thread_start":
			var f connproto.ThreadStartFromHost
			if err := json.Unmarshal(line, &f); err != nil {
				w.result(frame.ID, "", fmt.Errorf("malformed %s: %v", frame.Type, err))
				continue
			}
			t, ok := transport, connected
			go func() {
				var threadChatID string
				err := doSend(ctx, t, ok, func(t Transport) error {
					th, has := t.(Threader)
					if !has {
						return fmt.Errorf("connector does not support threads")
					}
					var err error
					threadChatID, err = th.StartThread(ctx, f.ChatID, f.FromMessageID, f.Name)
					return err
				})
				res := connproto.ResultFromConn{Type: "result", ID: f.ID, ChatID: threadChatID}
				if err != nil {
					res.Error = err.Error()
				}
				_ = w.write(res)
			}()
		case "ask_close":
			var f connproto.AskCloseFromHost
			if err := json.Unmarshal(line, &f); err != nil {
				w.result(frame.ID, "", fmt.Errorf("malformed %s: %v", frame.Type, err))
				continue
			}
			t, ok := transport, connected
			go func() {
				w.result(f.ID, "", doSend(ctx, t, ok, func(t Transport) error {
					asker, has := t.(Asker)
					if !has {
						return fmt.Errorf("connector does not support asks")
					}
					return asker.CloseAsk(ctx, f.AskID, f.Outcome)
				}))
			}()
		case "shutdown":
			return nil
		default:
			// An id on the envelope means the host expects exactly one
			// result — answer in milliseconds instead of letting its
			// 30 s send timeout expire, and a future command type
			// degrades gracefully against an older connector. Id-less
			// unknowns stay log-only: no result is owed, and inventing
			// one would corrupt the correlation space.
			if frame.ID != "" {
				w.result(frame.ID, "", fmt.Errorf("unknown command type %q", frame.Type))
			}
			fmt.Fprintf(errlog, "unknown frame type %q from host\n", frame.Type)
		}
	}
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// doSend runs one outbound command against the transport, mapping
// "not connected yet" to a result error instead of a crash.
func doSend(ctx context.Context, t Transport, connected bool, fn func(Transport) error) error {
	if !connected || t == nil {
		return fmt.Errorf("not connected")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(t)
}

// frameWriter serializes frame writes — deliver, results, and warns
// race from different goroutines, and interleaved bytes would corrupt
// the host's stream.
type frameWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (fw *frameWriter) write(v any) error {
	b, err := connproto.Encode(v)
	if err != nil {
		return err
	}
	fw.mu.Lock()
	defer fw.mu.Unlock()
	_, err = fw.w.Write(b)
	return err
}

func (fw *frameWriter) result(id, messageID string, err error) {
	res := connproto.ResultFromConn{Type: "result", ID: id, MessageID: messageID}
	if err != nil {
		res.Error = err.Error()
	}
	_ = fw.write(res)
}

// StateDir returns the conventional place for a connector's own
// config and credentials: <data dir>/connectors/<name>. The lifecycle
// verbs (setup/configured/...) run without a host handshake, so this
// is deterministic rather than host-assigned. envcompat.Home is the
// same resolver the host uses, so both sides agree (it was copy-
// pasted here once; the rename plan deduped it).
func StateDir(name string) string {
	return filepath.Join(envcompat.Home(), "connectors", name)
}
