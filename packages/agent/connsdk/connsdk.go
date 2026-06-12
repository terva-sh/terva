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
	"bufio"
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
}

// Attachment is one inbound file. Write the bytes under the DataDir
// terva assigns (see Session.DataDir) and pass the path; terva reads and
// deletes the file.
type Attachment struct {
	MimeType string
	Path     string
}

// Message is one normalized inbound chat message.
type Message struct {
	ChatID      string
	UserID      string
	Username    string
	ReplyTo     string
	Text        string
	Attachments []Attachment
}

// Outgoing is one outbound text message, pre-chunked by terva.
type Outgoing struct {
	ChatID  string
	ReplyTo string
	Text    string
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
		err := serve(cfg, os.Stdin, os.Stdout, os.Stderr)
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

// serve runs the protocol loop: hello/hello_ack, then frames until
// stdin closes or the host says shutdown. Split from Main for tests.
func serve(cfg Config, in io.Reader, out io.Writer, errlog io.Writer) error {
	if cfg.NewTransport == nil {
		return fmt.Errorf("connsdk: Config.NewTransport is required")
	}
	w := &frameWriter{w: out}

	if err := w.write(connproto.HelloFromConn{
		Type:        "hello",
		Name:        cfg.Name,
		Version:     cfg.Version,
		ProtocolMin: connproto.ProtocolVersion,
		ProtocolMax: connproto.ProtocolVersion,
		Capabilities: connproto.Capabilities{
			MaxTextLen:      cfg.Capabilities.MaxTextLen,
			TypingRefreshMS: int(cfg.Capabilities.TypingRefresh / time.Millisecond),
			SendsImages:     cfg.Capabilities.SendsImages,
			SendsFiles:      cfg.Capabilities.SendsFiles,
		},
	}); err != nil {
		return err
	}

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	if !scanner.Scan() {
		return fmt.Errorf("host closed the stream before hello_ack: %v", scanner.Err())
	}
	var ack connproto.HelloAckFromHost
	if err := json.Unmarshal(scanner.Bytes(), &ack); err != nil || ack.Type != "hello_ack" {
		return fmt.Errorf("expected hello_ack, got %q", scanner.Text())
	}
	if ack.Protocol != connproto.ProtocolVersion {
		return fmt.Errorf("host negotiated protocol %d; this SDK speaks %d", ack.Protocol, connproto.ProtocolVersion)
	}
	// Renamed hosts send terva_version (and keep terva_version for the
	// deprecation window); either spelling fills the same field.
	hostVersion := ack.TervaVersion
	if hostVersion == "" {
		hostVersion = ack.ZotVersion // rename:keep — frozen wire field
	}
	session := Session{DataDir: ack.DataDir, ZotVersion: hostVersion} // rename:keep — public SDK API

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// receiveErr carries a fatal transport failure out of the
	// receive goroutine; the serve loop can't notice it on its own
	// because it is blocked reading stdin.
	receiveErr := make(chan error, 1)

	var transport Transport
	var identity Identity
	connected := false

	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var frame connproto.Frame
		if err := json.Unmarshal(line, &frame); err != nil {
			fmt.Fprintf(errlog, "malformed frame from host: %v\n", err)
			continue
		}

		// A fatal receive error ends the session regardless of what
		// the host just asked for.
		select {
		case err := <-receiveErr:
			_ = w.write(connproto.WarnFromConn{Type: "warn", Message: "receive failed: " + err.Error()})
			return err
		default:
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
						atts = append(atts, connproto.Attachment{MimeType: a.MimeType, Path: a.Path})
					}
					_ = w.write(connproto.MessageFromConn{
						Type: "message", ChatID: m.ChatID, UserID: m.UserID, Username: m.Username,
						ReplyTo: m.ReplyTo, Text: m.Text, Attachments: atts,
					})
				})
				if err != nil && ctx.Err() == nil {
					receiveErr <- err
					// Wake the serve loop? It is blocked on stdin; the
					// warn below at least reaches the host's log right
					// away, and the next host frame surfaces the error.
					_ = w.write(connproto.WarnFromConn{Type: "warn", Message: "receive failed: " + err.Error()})
				}
			}()

		case "send":
			var f connproto.SendFromHost
			if err := json.Unmarshal(line, &f); err != nil {
				continue
			}
			// Sends run off the serve loop so a slow service call
			// can't delay typing/shutdown frames. transport/connected
			// are captured here, before the goroutine starts; only
			// this loop mutates them.
			t, ok := transport, connected
			go func() {
				w.result(f.ID, doSend(ctx, t, ok, func(t Transport) error {
					return t.Send(ctx, Outgoing{ChatID: f.ChatID, ReplyTo: f.ReplyTo, Text: f.Text})
				}))
			}()
		case "send_image":
			var f connproto.SendImageFromHost
			if err := json.Unmarshal(line, &f); err != nil {
				continue
			}
			t, ok := transport, connected
			go func() {
				w.result(f.ID, doSend(ctx, t, ok, func(t Transport) error {
					return t.SendImage(ctx, f.ChatID, f.Path, f.Caption)
				}))
			}()
		case "send_file":
			var f connproto.SendFileFromHost
			if err := json.Unmarshal(line, &f); err != nil {
				continue
			}
			t, ok := transport, connected
			go func() {
				w.result(f.ID, doSend(ctx, t, ok, func(t Transport) error {
					return t.SendFile(ctx, f.ChatID, f.Path, f.Caption)
				}))
			}()
		case "typing":
			var f connproto.TypingFromHost
			if err := json.Unmarshal(line, &f); err != nil || !connected {
				continue
			}
			t := transport
			go func() { _ = t.Typing(ctx, f.ChatID) }()
		case "shutdown":
			return nil
		default:
			fmt.Fprintf(errlog, "unknown frame type %q from host\n", frame.Type)
		}
	}
	// stdin closed: the host is gone; stop receiving and exit.
	return scanner.Err()
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

func (fw *frameWriter) result(id string, err error) {
	res := connproto.ResultFromConn{Type: "result", ID: id}
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
