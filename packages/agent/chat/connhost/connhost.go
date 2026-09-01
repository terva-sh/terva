// Package connhost is the host side of ONE connector-protocol session
// (packages/agent/connproto), factored out of the process-spawning proxy
// so the protocol has exactly one host implementation no matter what
// carries its frames. The two carriers today:
//
//   - chat/external.Proxy — frames are LF-delimited lines on a child
//     process's stdio; the proxy owns spawning, restarts, and reaping.
//   - chat/extconn — frames ride `chat` envelope frames of the extension
//     protocol (a connector-role extension, extproto protocol 5); the
//     extension manager owns the process.
//
// A Session speaks connproto from the first inner frame: it reads the
// connector's hello, negotiates the protocol version, answers hello_ack
// (with the carrier's data dir for attachments), then dispatches the
// stream — connected/connect_error, inbound messages (with attachment
// ingestion and containment), send results, warns — until the carrier
// ends. Everything version- or vocabulary-shaped about the connector
// protocol lives HERE (and in connsdk on the author side); carriers only
// move opaque frames. When connproto grows, both carriers inherit the
// growth with no wire changes of their own.
package connhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/connproto"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/provider"
)

// FrameConn is one carrier of connproto frames: a frame is a single JSON
// object, WITHOUT the LF terminator (line framing is the stdio carrier's
// concern; the extension tunnel embeds frames in envelopes instead).
type FrameConn interface {
	// ReadFrame blocks for the next inbound frame. io.EOF means the
	// carrier closed cleanly; any other error describes why it died
	// (and becomes the session's Err).
	ReadFrame() ([]byte, error)
	// WriteFrame sends one frame. Must be safe for concurrent use —
	// sends, typing, and the handshake race from different goroutines.
	WriteFrame([]byte) error
}

// Config assembles a session.
type Config struct {
	// Name prefixes errors and warnings ("connector %q: ...").
	Name string
	// DataDir is announced in hello_ack as the connector's scratch
	// directory for inbound attachments, and is the containment root
	// the ingest sweep refuses to leave.
	DataDir string
	// HostVersion fills hello_ack's version fields.
	HostVersion string
	// OnSecrets receives the connector's secrets declaration from hello — its
	// own recipient and the paths in its state file that hold sealed values.
	// Optional; a connector that keeps no sealed state sends none.
	//
	// A callback rather than a registry write here: connhost speaks the
	// protocol and knows nothing about data homes, and the handshake is the
	// only source of a recipient the host may trust (the state file is
	// writable by anything that can write $TERVA_HOME).
	OnSecrets func(connproto.SecretsDecl)
	// Conn carries the frames.
	Conn FrameConn
	// Deliver receives each gated-ready inbound message. Called from
	// the session's dispatch goroutine in arrival order; it must not
	// block (buffer + drop is the consumer's policy, as before).
	Deliver func(chat.Message)
	// DeliverMembership receives the bot's own admission events
	// (stage B chat_membership frames). Optional — nil logs and drops.
	// Same contract as Deliver: dispatch goroutine, must not block.
	DeliverMembership func(chat.Membership)
	// Events receives the stage-D inbound streams (edits, deletes,
	// reactions). Any nil handler logs and drops. Same contract as
	// Deliver.
	Events chat.ChatEventHandlers
	// Warn receives operational lines (connector warn frames, dropped
	// attachments). Optional; defaults to stderr.
	Warn func(string)
	// Log receives wire diagnostics (malformed frames, unknown types)
	// that belong in the connector's log file rather than the user's
	// face. Optional; defaults to Warn.
	Log func(string)
	// SendTimeout bounds each send/send_image/send_file round trip.
	// 0 means 30s.
	SendTimeout time.Duration
}

const defaultSendTimeout = 30 * time.Second

// Session is one live connproto conversation over a FrameConn.
type Session struct {
	cfg Config

	frames chan []byte // reader goroutine → handshake, then dispatch
	done   chan struct{}

	mu             sync.Mutex
	caps           chat.Capabilities
	protocol       int // negotiated wire version (see connproto)
	pending        map[string]chan connproto.ResultFromConn
	pendingAsks    map[string]*pendingAsk
	connectPending chan connectOutcome
	readErr        error // why ReadFrame ended; io.EOF for a clean close

	// ownMsgs is a bounded record of message ids this session created
	// (from result.message_id), so inbound reactions can be tagged as
	// landing on the bot's own messages. Ring semantics: oldest ids
	// fall out past the cap.
	ownMsgs    map[string]bool
	ownMsgsSeq []string
}

// ownMsgsCap bounds the own-message record; reactions to messages
// older than the newest ~256 sends read as not-ours, which only
// downgrades a courtesy note.
const ownMsgsCap = 256

// recordOwnMessage notes a message id this session created.
func (s *Session) recordOwnMessage(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	if s.ownMsgs == nil {
		s.ownMsgs = map[string]bool{}
	}
	if !s.ownMsgs[id] {
		s.ownMsgs[id] = true
		s.ownMsgsSeq = append(s.ownMsgsSeq, id)
		for len(s.ownMsgsSeq) > ownMsgsCap {
			delete(s.ownMsgs, s.ownMsgsSeq[0])
			s.ownMsgsSeq = s.ownMsgsSeq[1:]
		}
	}
	s.mu.Unlock()
}

func (s *Session) isOwnMessage(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ownMsgs[id]
}

// pendingAsk is one in-flight ask awaiting its first valid answer.
// The host-side restrict_to re-filter lives at the routing layer so
// only allowed answers ever reach the waiter — "answers only from
// these ids" is the wire contract, enforced here regardless of what
// the connector filtered service-side.
type pendingAsk struct {
	restrict []string
	answers  chan connproto.AnswerFromConn // buffered(1); first valid answer wins
}

// hostFeatures is what this host consumes at protocol 2 (stages A, G,
// and B); advertised in hello_ack so connectors never emit constructs
// nobody reads.
var hostFeatures = []string{"message_ids", "chat_kinds", "asks", "entities", "chat_membership",
	"edits_in", "deletes_in", "reactions_in", "attachment_kinds"}

type connectOutcome struct {
	identity chat.Identity
	err      error
}

// New builds a session; Start begins it.
func New(cfg Config) *Session {
	if cfg.SendTimeout == 0 {
		cfg.SendTimeout = defaultSendTimeout
	}
	return &Session{
		cfg:         cfg,
		frames:      make(chan []byte),
		done:        make(chan struct{}),
		pending:     map[string]chan connproto.ResultFromConn{},
		pendingAsks: map[string]*pendingAsk{},
	}
}

func (s *Session) warnf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if s.cfg.Warn != nil {
		s.cfg.Warn(msg)
		return
	}
	fmt.Fprintln(os.Stderr, msg)
}

func (s *Session) logf(format string, args ...any) {
	if s.cfg.Log != nil {
		s.cfg.Log(fmt.Sprintf(format, args...))
		return
	}
	s.warnf(format, args...)
}

// Start runs the hello/hello_ack handshake (with negotiated protocol
// versioning) and starts the dispatch loop. On error the session is
// dead; the caller still owns the carrier and must close it (which
// unblocks the session's reader).
func (s *Session) Start(helloTimeout time.Duration) error {
	go s.readLoop()

	fail := func(err error) error {
		// Nobody will dispatch; keep the reader drainable until the
		// carrier closes, and let waiters (none yet) see the end.
		go func() {
			for range s.frames {
			}
		}()
		close(s.done)
		return err
	}

	var first []byte
	select {
	case f, ok := <-s.frames:
		if !ok {
			return fail(fmt.Errorf("connector %q ended before hello: %v", s.cfg.Name, s.Err()))
		}
		first = f
	case <-time.After(helloTimeout):
		return fail(fmt.Errorf("connector %q sent no hello within %s", s.cfg.Name, helloTimeout))
	}

	var hello connproto.HelloFromConn
	if err := json.Unmarshal(first, &hello); err != nil {
		return fail(fmt.Errorf("connector %q: malformed hello: %v", s.cfg.Name, err))
	}
	if hello.Type != "hello" {
		return fail(fmt.Errorf("connector %q: first frame must be hello, got %q", s.cfg.Name, hello.Type))
	}
	if hello.Name != "" && hello.Name != s.cfg.Name {
		// Trust the configured name (it names the state dirs); just
		// leave a trace.
		s.logf("hello name %q != %q; using the configured name", hello.Name, s.cfg.Name)
	}
	if hello.Secrets != nil && s.cfg.OnSecrets != nil {
		s.cfg.OnSecrets(*hello.Secrets)
	}
	// Negotiated versioning, not announce-only: pick the highest
	// version both sides speak; refuse a disjoint range with a clear
	// error instead of failing on a weird frame later.
	version := connproto.ProtocolMax
	if hello.ProtocolMax < version {
		version = hello.ProtocolMax
	}
	if hello.ProtocolMin > version || version < connproto.ProtocolVersion {
		return fail(fmt.Errorf("connector %q speaks protocol %d..%d; this terva speaks %d..%d — upgrade %s",
			s.cfg.Name, hello.ProtocolMin, hello.ProtocolMax, connproto.ProtocolVersion, connproto.ProtocolMax,
			map[bool]string{true: "terva", false: "the connector"}[hello.ProtocolMin > connproto.ProtocolMax]))
	}

	s.mu.Lock()
	s.protocol = version
	s.caps = chat.Capabilities{
		MaxTextLen:      hello.Capabilities.MaxTextLen,
		TypingRefresh:   time.Duration(hello.Capabilities.TypingRefreshMS) * time.Millisecond,
		SendsImages:     hello.Capabilities.SendsImages,
		SendsFiles:      hello.Capabilities.SendsFiles,
		Asks:            version >= 2 && contains(hello.Capabilities.Features, "asks"),
		Speaker:         speakerGrade(version, hello.Capabilities.Features),
		ThreadsOut:      version >= 2 && contains(hello.Capabilities.Features, "threads_out"),
		TypingStop:      version >= 2 && contains(hello.Capabilities.Features, "typing_stop"),
		EditsOut:        version >= 2 && contains(hello.Capabilities.Features, "edits_out"),
		ReactionsOut:    version >= 2 && contains(hello.Capabilities.Features, "reactions_out"),
		DeletesOut:      version >= 2 && contains(hello.Capabilities.Features, "deletes_out"),
		AttachmentKinds: version >= 2 && contains(hello.Capabilities.Features, "attachment_kinds"),
		MinEditInterval: time.Duration(hello.Capabilities.MinEditIntervalMS) * time.Millisecond,
	}
	s.mu.Unlock()

	ack := connproto.HelloAckFromHost{
		Type:         "hello_ack",
		Protocol:     version,
		ZotVersion:   s.cfg.HostVersion, // rename:keep — frozen wire field
		TervaVersion: s.cfg.HostVersion,
		DataDir:      s.cfg.DataDir,
	}
	if version >= 2 {
		ack.Capabilities = &connproto.Capabilities{Features: hostFeatures}
	}
	if err := s.writeFrame(ack); err != nil {
		return fail(fmt.Errorf("send hello_ack: %w", err))
	}

	go s.dispatch()
	return nil
}

// readLoop pulls carrier frames until the carrier ends, then records
// why and closes the stream to the dispatcher.
func (s *Session) readLoop() {
	for {
		frame, err := s.cfg.Conn.ReadFrame()
		if err != nil {
			s.mu.Lock()
			s.readErr = err
			s.mu.Unlock()
			close(s.frames)
			return
		}
		s.frames <- frame
	}
}

// dispatch handles every post-handshake frame, then ends the session:
// pending waiters fail fast and Done closes.
func (s *Session) dispatch() {
	for frame := range s.frames {
		s.handleFrame(frame)
	}
	err := s.Err()
	if err == nil {
		err = errors.New("stream closed")
	}
	s.deliverConnect(connectOutcome{err: fmt.Errorf("connector %q: session ended during connect: %w", s.cfg.Name, err)})
	s.failPending(err)
	close(s.done)
}

// Done closes when the session has ended (carrier closed either way);
// Err then explains why.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err reports why the carrier ended: nil for a clean close (io.EOF),
// the carrier's error otherwise. Meaningful once Done is closed.
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readErr == nil || errors.Is(s.readErr, io.EOF) {
		return nil
	}
	return s.readErr
}

// Capabilities returns the limits the connector declared in hello.
func (s *Session) Capabilities() chat.Capabilities {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.caps
}

// Connect runs the connect round trip: the connector dials its service
// and answers connected (identity) or connect_error.
func (s *Session) Connect(ctx context.Context, timeout time.Duration) (chat.Identity, error) {
	ch := make(chan connectOutcome, 1)
	s.mu.Lock()
	if s.connectPending != nil {
		s.mu.Unlock()
		return chat.Identity{}, fmt.Errorf("connector %q: a connect is already in flight", s.cfg.Name)
	}
	s.connectPending = ch
	s.mu.Unlock()
	clear := func() {
		s.mu.Lock()
		if s.connectPending == ch {
			s.connectPending = nil
		}
		s.mu.Unlock()
	}

	if err := s.writeFrame(connproto.ConnectFromHost{Type: "connect"}); err != nil {
		clear()
		return chat.Identity{}, err
	}
	select {
	case out := <-ch:
		if out.err != nil {
			return chat.Identity{}, out.err
		}
		return out.identity, nil
	case <-time.After(timeout):
		clear()
		return chat.Identity{}, fmt.Errorf("connector %q: no connected/connect_error within %s", s.cfg.Name, timeout)
	case <-ctx.Done():
		clear()
		return chat.Identity{}, ctx.Err()
	}
}

// handleFrame routes one inner frame. Runs on the dispatch goroutine
// only, in arrival order.
func (s *Session) handleFrame(line []byte) {
	var frame connproto.Frame
	if err := json.Unmarshal(line, &frame); err != nil {
		s.logf("malformed frame from connector: %v", err)
		return
	}
	switch frame.Type {
	case "connected":
		var conn connproto.ConnectedFromConn
		if err := json.Unmarshal(line, &conn); err == nil {
			s.deliverConnect(connectOutcome{identity: chat.Identity{ID: conn.ID, Username: conn.Username}})
		}
	case "connect_error":
		var ce connproto.ConnectErrorFromConn
		if err := json.Unmarshal(line, &ce); err == nil {
			s.deliverConnect(connectOutcome{err: fmt.Errorf("connector %q: connect: %s", s.cfg.Name, ce.Error)})
		}
	case "message":
		var msg connproto.MessageFromConn
		if err := json.Unmarshal(line, &msg); err != nil {
			s.logf("bad message frame: %v", err)
			return
		}
		images, files := s.ingestAttachments(msg.ID, msg.Attachments)
		m := chat.Message{
			ID:        msg.ID,
			TS:        msg.TS,
			ChatID:    msg.ChatID,
			ChatKind:  msg.ChatKind,
			ChatTitle: msg.ChatTitle,
			ScopeID:   msg.ScopeID,
			UserID:    msg.UserID,
			Username:  msg.Username,
			ReplyTo:   msg.ReplyTo,
			Text:      joinCaptions(msg.Text, msg.Attachments),
			Images:    images,
			Files:     files,
		}
		for _, e := range msg.Entities {
			m.Entities = append(m.Entities, chat.Entity{
				Kind: e.Kind, Offset: e.Offset, Length: e.Length, UserID: e.UserID,
			})
		}
		s.mu.Lock()
		v1 := s.protocol < 2
		s.mu.Unlock()
		if v1 {
			// Protocol-1 normalization: the old shape carried the
			// message's OWN id in reply_to (the token hosts echo when
			// replying) and had no true in-reply-to. Consumers only
			// ever see the v2 semantics.
			m.ID = msg.ReplyTo
			m.ReplyTo = ""
		}
		s.cfg.Deliver(m)
	case "result":
		var res connproto.ResultFromConn
		if err := json.Unmarshal(line, &res); err == nil {
			s.mu.Lock()
			ch, ok := s.pending[res.ID]
			if ok {
				delete(s.pending, res.ID)
			}
			s.mu.Unlock()
			if ok {
				ch <- res // buffered(1); never blocks
			}
		}
	case "message_edited":
		var ed connproto.MessageEditedFromConn
		if err := json.Unmarshal(line, &ed); err != nil {
			s.logf("bad message_edited frame: %v", err)
			return
		}
		if s.cfg.Events.Edited == nil {
			s.logf("message_edited for %q dropped (no consumer)", ed.ID)
			return
		}
		// Echo hygiene: only a message's author can edit it, so an edit
		// of an id this session minted (result.message_id from its own
		// sends/asks) is the session's own write-back — an ask_close
		// rendering its outcome, a future streaming edit — reflected off
		// the service. Narrating it as "the user edited a message" is
		// wrong and noisy; drop it here so every carrier is covered.
		if s.isOwnMessage(ed.ID) {
			return
		}
		ev := chat.MessageEdited{ChatID: ed.ChatID, ID: ed.ID, TS: ed.TS, Text: ed.Text}
		for _, e := range ed.Entities {
			ev.Entities = append(ev.Entities, chat.Entity{Kind: e.Kind, Offset: e.Offset, Length: e.Length, UserID: e.UserID})
		}
		s.cfg.Events.Edited(ev)
	case "message_deleted":
		var del connproto.MessageDeletedFromConn
		if err := json.Unmarshal(line, &del); err != nil {
			s.logf("bad message_deleted frame: %v", err)
			return
		}
		if s.cfg.Events.Deleted == nil {
			s.logf("message_deleted for %q dropped (no consumer)", del.ID)
			return
		}
		// Same echo hygiene as edits: deletions of the session's own
		// messages (by itself, or by a chat admin) carry nothing the
		// host consumers act on — the queue only ever holds USER
		// messages — so they are pure noise to the loop.
		if s.isOwnMessage(del.ID) {
			return
		}
		s.cfg.Events.Deleted(chat.MessageDeleted{ChatID: del.ChatID, ID: del.ID})
	case "reaction":
		var re connproto.ReactionFromConn
		if err := json.Unmarshal(line, &re); err != nil {
			s.logf("bad reaction frame: %v", err)
			return
		}
		if s.cfg.Events.Reaction == nil {
			s.logf("reaction on %q dropped (no consumer)", re.MessageID)
			return
		}
		s.cfg.Events.Reaction(chat.Reaction{
			ChatID: re.ChatID, MessageID: re.MessageID,
			UserID: re.UserID, Username: re.Username,
			Key: re.Key, Removed: re.Removed,
			OwnMessage: s.isOwnMessage(re.MessageID),
		})
	case "chat_membership":
		var mb connproto.ChatMembershipFromConn
		if err := json.Unmarshal(line, &mb); err != nil {
			s.logf("bad chat_membership frame: %v", err)
			return
		}
		if s.cfg.DeliverMembership == nil {
			s.logf("chat_membership for %q dropped (no consumer)", mb.Chat.ID)
			return
		}
		s.cfg.DeliverMembership(chat.Membership{
			ChatID: mb.Chat.ID, ChatKind: mb.Chat.Kind, ChatTitle: mb.Chat.Title,
			ScopeID: mb.ScopeID,
			Change:  mb.Change, ByUserID: mb.ByUserID, ByUsername: mb.ByUsername,
		})
	case "answer":
		var ans connproto.AnswerFromConn
		if err := json.Unmarshal(line, &ans); err != nil {
			s.logf("bad answer frame: %v", err)
			return
		}
		s.mu.Lock()
		pa, ok := s.pendingAsks[ans.AskID]
		s.mu.Unlock()
		if !ok {
			// A late click on an already-closed ask; expected churn.
			s.logf("answer for unknown ask %q ignored", ans.AskID)
			return
		}
		if len(pa.restrict) > 0 && !contains(pa.restrict, ans.UserID) {
			// Worth a visible line, not just a log: someone who isn't
			// allowed to answer tried to.
			s.warnf("connector %q: answer to ask %q from unauthorized user %s ignored", s.cfg.Name, ans.AskID, ans.UserID)
			return
		}
		select {
		case pa.answers <- ans:
		default:
			// The waiter already has a winner buffered; first valid
			// answer wins, the rest are noise by contract.
		}
	case "warn":
		var w connproto.WarnFromConn
		if err := json.Unmarshal(line, &w); err == nil {
			s.warnf("connector %q: %s", s.cfg.Name, w.Message)
		}
	case "hello":
		// Duplicate hello after handshake; harmless, note it.
		s.logf("unexpected extra hello frame")
	default:
		s.logf("unknown frame type %q", frame.Type)
	}
}

// deliverConnect resolves the in-flight connect round trip, if any.
func (s *Session) deliverConnect(out connectOutcome) {
	s.mu.Lock()
	ch := s.connectPending
	s.connectPending = nil
	s.mu.Unlock()
	if ch != nil {
		ch <- out // buffered(1)
	}
}

// failPending resolves every in-flight send with the session's death —
// a waiter must not sit out its full timeout against a closed stream.
// Ask waiters need no equivalent push: they select on Done directly
// (an ask outlives the send timeout by design, so it watches the
// session itself).
func (s *Session) failPending(err error) {
	s.mu.Lock()
	pending := s.pending
	s.pending = map[string]chan connproto.ResultFromConn{}
	s.mu.Unlock()
	for _, ch := range pending {
		ch <- connproto.ResultFromConn{Type: "result", Error: err.Error()} // buffered(1)
	}
}

// ingestAttachments takes ownership of by-path attachments. Images
// (kind "image" or "" with an image/* mime) are read into memory and
// deleted, exactly the v1 flow. Every other kind (stage E) is MOVED
// into a per-message directory under the same data dir — still
// contained, provenance-labeled, reachable by the agent's normal
// tools, and cleaned by the consumer after the turn. Paths must
// resolve inside the connector's data dir — a connector must not be
// able to point the host at arbitrary files (and certainly not get
// them deleted or moved).
func (s *Session) ingestAttachments(msgID string, atts []connproto.Attachment) ([]provider.ImageBlock, []chat.FileAttachment) {
	if len(atts) == 0 {
		return nil, nil
	}
	dataReal, err := filepath.EvalSymlinks(s.cfg.DataDir)
	if err != nil {
		s.warnf("connector %q: resolve data dir: %v", s.cfg.Name, err)
		return nil, nil
	}
	var images []provider.ImageBlock
	var files []chat.FileAttachment
	for i, a := range atts {
		real, err := filepath.EvalSymlinks(a.Path)
		if err != nil {
			s.warnf("connector %q: attachment %s: %v", s.cfg.Name, a.Path, err)
			continue
		}
		rel, err := filepath.Rel(dataReal, real)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			s.warnf("connector %q: attachment outside data dir ignored: %s", s.cfg.Name, a.Path)
			continue
		}
		kind := a.Kind
		if kind == "" && strings.HasPrefix(a.MimeType, "image/") {
			kind = "image"
		}
		if kind == "image" || kind == "" {
			data, err := os.ReadFile(real)
			if err != nil {
				s.warnf("connector %q: read attachment: %v", s.cfg.Name, err)
				continue
			}
			_ = os.Remove(real)
			images = append(images, provider.ImageBlock{MimeType: a.MimeType, Data: data})
			continue
		}
		name := a.Name
		if name == "" {
			name = filepath.Base(real)
		}
		destDir := filepath.Join(s.cfg.DataDir, "incoming", sanitizeMsgDir(msgID))
		if err := privfs.MkdirAll(destDir); err != nil {
			s.warnf("connector %q: stage attachment dir: %v", s.cfg.Name, err)
			continue
		}
		dest := filepath.Join(destDir, fmt.Sprintf("%d-%s", i, filepath.Base(name)))
		if err := os.Rename(real, dest); err != nil {
			s.warnf("connector %q: move attachment: %v", s.cfg.Name, err)
			continue
		}
		files = append(files, chat.FileAttachment{
			Path: dest, Kind: kind, MimeType: a.MimeType, Name: name,
			Size: a.Size, Duration: time.Duration(a.DurationMS) * time.Millisecond,
			Caption: a.Caption,
		})
	}
	return images, files
}

// sanitizeMsgDir keeps the per-message directory name boring no matter
// what the service uses for message ids.
func sanitizeMsgDir(id string) string {
	if id == "" {
		return newFrameID()
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// joinCaptions appends attachment captions the text doesn't already
// carry — telegram users expect a photo's caption to BE the message.
func joinCaptions(text string, atts []connproto.Attachment) string {
	for _, a := range atts {
		c := strings.TrimSpace(a.Caption)
		if c == "" || strings.Contains(text, c) {
			continue
		}
		if text != "" {
			text += "\n"
		}
		text += c
	}
	return text
}

// Send delivers one outbound text message (pre-chunked by the caller).
// A Speaker rides the frame only when the connector graded itself for
// speakers; otherwise the host renders the prefix fallback here, so
// senders never check capability.
func (s *Session) Send(ctx context.Context, out chat.Outgoing) error {
	if out.Speaker != nil && s.Capabilities().Speaker == chat.SpeakerNone {
		out = chat.RenderSpeakerFallback(out)
	}
	id := newFrameID()
	frame := connproto.SendFromHost{
		Type: "send", ID: id, ChatID: out.ChatID, ReplyTo: out.ReplyTo, Text: out.Text,
	}
	if out.Speaker != nil {
		frame.Speaker = &connproto.Speaker{
			Key: out.Speaker.Key, Name: out.Speaker.Name, AvatarPath: out.Speaker.AvatarPath,
		}
	}
	res, err := s.roundTripResult(ctx, id, frame)
	s.recordOwnMessage(res.MessageID)
	return err
}

// speakerGrade maps the connector's declared features onto the
// three-valued speaker capability; full wins when both are declared.
func speakerGrade(version int, features []string) string {
	if version < 2 {
		return chat.SpeakerNone
	}
	if contains(features, "speaker:full") {
		return chat.SpeakerFull
	}
	if contains(features, "speaker:name_only") {
		return chat.SpeakerNameOnly
	}
	return chat.SpeakerNone
}

// SendImage uploads a local file as an inline image.
func (s *Session) SendImage(ctx context.Context, chatID, path, caption string) error {
	id := newFrameID()
	return s.roundTrip(ctx, id, connproto.SendImageFromHost{
		Type: "send_image", ID: id, ChatID: chatID, Path: path, Caption: caption,
	})
}

// SendFile uploads a local file as a raw document.
func (s *Session) SendFile(ctx context.Context, chatID, path, caption string) error {
	id := newFrameID()
	return s.roundTrip(ctx, id, connproto.SendFileFromHost{
		Type: "send_file", ID: id, ChatID: chatID, Path: path, Caption: caption,
	})
}

// Typing asserts the typing indicator once. Fire-and-forget.
func (s *Session) Typing(chatID string) error {
	return s.writeFrame(connproto.TypingFromHost{Type: "typing", ChatID: chatID})
}

// StopTyping withdraws the typing indicator (chat.TypingStopper). Gate
// on Capabilities().TypingStop: a connector that did not declare the
// feature reads the frame as one more typing start.
func (s *Session) StopTyping(ctx context.Context, chatID string) error {
	if !s.Capabilities().TypingStop {
		return fmt.Errorf("connector %q does not support typing_stop", s.cfg.Name)
	}
	off := false
	return s.writeFrame(connproto.TypingFromHost{Type: "typing", ChatID: chatID, Active: &off})
}

// Ask runs one full interaction: render the question, wait for the
// first valid answer (host-side restrict_to re-filter — see the answer
// routing), close the ask with a rendered outcome, return the answer.
// Every non-answer path is fail-closed: timeout, ctx end, and session
// death all withdraw the question (best-effort once the session is
// gone) and return an error the caller treats as a denial.
func (s *Session) Ask(ctx context.Context, a chat.Ask) (chat.Answer, error) {
	if !s.Capabilities().Asks {
		return chat.Answer{}, fmt.Errorf("connector %q does not support asks", s.cfg.Name)
	}
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = chat.DefaultAskTimeout
	}

	id := newFrameID()
	pa := &pendingAsk{restrict: a.RestrictTo, answers: make(chan connproto.AnswerFromConn, 1)}
	s.mu.Lock()
	s.pendingAsks[id] = pa
	s.mu.Unlock()
	unregister := func() {
		s.mu.Lock()
		delete(s.pendingAsks, id)
		s.mu.Unlock()
	}

	opts := make([]connproto.AskOption, 0, len(a.Options))
	for _, o := range a.Options {
		opts = append(opts, connproto.AskOption{Key: o.Key, Label: o.Label, Style: o.Style, Hint: o.Hint})
	}
	// The render round trip: the connector acknowledges the QUESTION
	// went up (its result) before we settle in to wait for a human.
	res, err := s.roundTripResult(ctx, id, connproto.AskFromHost{
		Type: "ask", ID: id, ChatID: a.ChatID, ReplyTo: a.ReplyTo, Text: a.Text,
		Options: opts, RestrictTo: a.RestrictTo, ExpiresMS: int(timeout / time.Millisecond),
	})
	if err != nil {
		unregister()
		return chat.Answer{}, fmt.Errorf("connector %q: render ask: %w", s.cfg.Name, err)
	}
	s.recordOwnMessage(res.MessageID)

	select {
	case ans := <-pa.answers:
		unregister()
		s.closeAsk(id, closeOutcome(a, ans))
		return chat.Answer{
			Key: ans.Key, UserID: ans.UserID, Username: ans.Username,
			Attestation: normalizeAttestation(ans.Attestation),
		}, nil
	case <-time.After(timeout):
		unregister()
		outcome := a.TimeoutOutcome
		if outcome == "" {
			outcome = "expired (no answer)"
		}
		s.closeAsk(id, outcome)
		return chat.Answer{}, chat.ErrAskTimeout
	case <-ctx.Done():
		unregister()
		s.closeAsk(id, "cancelled")
		return chat.Answer{}, ctx.Err()
	case <-s.done:
		unregister()
		err := s.Err()
		if err == nil {
			err = errors.New("session ended")
		}
		return chat.Answer{}, fmt.Errorf("connector %q: %w", s.cfg.Name, err)
	}
}

// closeAsk withdraws an ask's controls and renders the outcome into
// the question message. Best-effort with its own deadline — the caller
// already has its result and must not hang on a wedged connector.
func (s *Session) closeAsk(askID, outcome string) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.SendTimeout)
	defer cancel()
	id := newFrameID()
	if err := s.roundTrip(ctx, id, connproto.AskCloseFromHost{
		Type: "ask_close", ID: id, AskID: askID, Outcome: outcome,
	}); err != nil {
		s.logf("close ask %s: %v", askID, err)
	}
}

// closeOutcome renders the audit line for an answered ask: the chosen
// option's label and who chose it.
func closeOutcome(a chat.Ask, ans connproto.AnswerFromConn) string {
	label := ans.Key
	for _, o := range a.Options {
		if o.Key == ans.Key {
			label = o.Label
			break
		}
	}
	who := ans.Username
	if who == "" {
		who = ans.UserID
	}
	return label + " — @" + who
}

// normalizeAttestation maps the wire's open set onto the two grades
// policy consumes; anything unknown or absent reads as best-effort.
func normalizeAttestation(a string) string {
	if a == connproto.AttestationAttested {
		return chat.AttestationAttested
	}
	return chat.AttestationBestEffort
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// Shutdown asks the connector to end (best-effort). What "ending" means
// belongs to the carrier: a child process exits; an extension's
// connector engine stops while the process lives on.
func (s *Session) Shutdown() {
	_ = s.writeFrame(connproto.ShutdownFromHost{Type: "shutdown"})
}

// StartThread opens a work-stream thread and returns the new thread
// chat's id (chat.Threader). Gate on Capabilities().ThreadsOut.
func (s *Session) StartThread(ctx context.Context, chatID, fromMessageID, name string) (string, error) {
	if !s.Capabilities().ThreadsOut {
		return "", fmt.Errorf("connector %q does not support threads", s.cfg.Name)
	}
	id := newFrameID()
	res, err := s.roundTripResult(ctx, id, connproto.ThreadStartFromHost{
		Type: "thread_start", ID: id, ChatID: chatID, FromMessageID: fromMessageID, Name: name,
	})
	if err != nil {
		return "", err
	}
	if res.ChatID == "" {
		return "", fmt.Errorf("connector %q: thread_start result carried no chat_id", s.cfg.Name)
	}
	return res.ChatID, nil
}

// EditMessage rewrites the bot's own earlier message (chat.Editor;
// stage D). Gate on Capabilities().EditsOut and pace streaming by
// Capabilities().MinEditInterval.
func (s *Session) EditMessage(ctx context.Context, chatID, messageID, text string) error {
	if !s.Capabilities().EditsOut {
		return fmt.Errorf("connector %q does not support edits", s.cfg.Name)
	}
	id := newFrameID()
	return s.roundTrip(ctx, id, connproto.EditFromHost{
		Type: "edit", ID: id, ChatID: chatID, MessageID: messageID, Text: text,
	})
}

// React toggles the bot's reaction on a message (chat.Reactor; stage
// D). Gate on Capabilities().ReactionsOut. terva emits unicode-emoji
// keys only.
func (s *Session) React(ctx context.Context, chatID, messageID, key string, remove bool) error {
	if !s.Capabilities().ReactionsOut {
		return fmt.Errorf("connector %q does not support reactions", s.cfg.Name)
	}
	id := newFrameID()
	return s.roundTrip(ctx, id, connproto.ReactFromHost{
		Type: "react", ID: id, ChatID: chatID, MessageID: messageID, Key: key, Remove: remove,
	})
}

// DeleteMessage retracts the bot's own message (chat.Deleter; stage D).
// Gate on Capabilities().DeletesOut.
func (s *Session) DeleteMessage(ctx context.Context, chatID, messageID string) error {
	if !s.Capabilities().DeletesOut {
		return fmt.Errorf("connector %q does not support deletes", s.cfg.Name)
	}
	id := newFrameID()
	return s.roundTrip(ctx, id, connproto.DeleteFromHost{
		Type: "delete", ID: id, ChatID: chatID, MessageID: messageID,
	})
}

// roundTrip writes one command frame and waits for its correlated
// result. Session death fails it fast; the timeout protects against a
// live-but-wedged connector.
func (s *Session) roundTrip(ctx context.Context, id string, frame any) error {
	_, err := s.roundTripResult(ctx, id, frame)
	return err
}

// roundTripResult is roundTrip for callers that need the result's
// payload fields (thread_start's chat_id).
func (s *Session) roundTripResult(ctx context.Context, id string, frame any) (connproto.ResultFromConn, error) {
	ch := make(chan connproto.ResultFromConn, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()
	cleanup := func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}
	if err := s.writeFrame(frame); err != nil {
		cleanup()
		return connproto.ResultFromConn{}, err
	}
	select {
	case res := <-ch:
		if res.Error != "" {
			return res, errors.New(res.Error)
		}
		return res, nil
	case <-time.After(s.cfg.SendTimeout):
		cleanup()
		return connproto.ResultFromConn{}, fmt.Errorf("connector %q: no result within %s", s.cfg.Name, s.cfg.SendTimeout)
	case <-ctx.Done():
		cleanup()
		return connproto.ResultFromConn{}, ctx.Err()
	}
}

// writeFrame marshals one inner frame onto the carrier.
func (s *Session) writeFrame(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.cfg.Conn.WriteFrame(b)
}

// frameSeq + newFrameID mint process-unique correlation ids, the
// extension manager's scheme: timestamp for log readability, counter
// for uniqueness within a burst.
var frameSeq atomic.Uint64

func newFrameID() string {
	ts := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	return ts + "-" + strconv.FormatUint(frameSeq.Add(1), 10)
}
