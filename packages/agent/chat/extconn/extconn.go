// Package extconn adapts a connector-role EXTENSION (extension protocol
// 5, register_connector — docs/proposals/connector-extensions.md) to the
// chat.Connector contract, so one subprocess can serve tools to the agent
// AND stream chat messages that start agent turns.
//
// Split of responsibilities: extdriver owns the TUNNEL (an opaque,
// session-id'd carrier of connector-protocol frames riding the extension
// wire); this package opens that tunnel and runs a connhost.Session over
// it — the same host-side session core the external-connector proxy
// uses, so the connector protocol has exactly one implementation per
// side no matter what carries it. On the extension side the counterpart
// engine is connsdk.Serve. Unlike chat/external's Proxy, the adapter
// never spawns anything — the extension Manager owns the process in
// every mode; the adapter attaches to the live process at Connect time
// through the bound Host.
//
// Consent layering (all load-bearing, see the proposal's trust section):
// the manifest must declare "connector": true (the driver refuses the
// role at the wire without it); only GLOBALLY-installed extensions are
// registered as chat services (RegisterDiscovered scans the global dir
// only — a cloned repo must never introduce a message source); and the
// role only ACTIVATES when a chat consumer selects the extension by name
// (`terva bot run --connector <name>`, the TUI's /connect). Inbound
// messages still flow through the host-owned chat gate (pairing,
// allowlist) before any turn.
package extconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/chat/connhost"
	"terva.sh/terva/packages/agent/extdriver"
)

// Host is the narrow slice of the extension subsystem the adapter needs:
// resolve a live, connector-role extension by name. *extdriver.Driver
// (and therefore extensions.Manager, which embeds it) satisfies it.
type Host interface {
	ConnectorExtension(name string) (*extdriver.Extension, bool)
}

// RestartHost is the optional Host upgrade behind crash recovery:
// respawn a dead extension process so the chat session can be
// re-established. extensions.Manager implements it (tool dispatch
// resolves by name through the driver's live index, so the respawn
// re-binds the agent's tool wrappers automatically); a bare driver
// binding does not, and there a process death stays permanent.
type RestartHost interface {
	Host
	RestartExtension(ctx context.Context, name string) error
}

var (
	hostMu    sync.Mutex
	boundHost Host

	// hostVersion is reported to connector engines in the inner
	// hello_ack. Set once at CLI startup (same site as chat/external's).
	hostVersion string
)

// SetTervaVersion records the running binary's version for the inner
// hello_ack of every tunneled session.
func SetTervaVersion(v string) {
	hostMu.Lock()
	hostVersion = v
	hostMu.Unlock()
}

func tervaVersion() string {
	hostMu.Lock()
	defer hostMu.Unlock()
	return hostVersion
}

// BindHost points the adapters at the live extension subsystem. Each mode
// calls it once after its extension manager finishes discovery (the chat
// service registry is populated earlier, at CLI startup, from manifests
// alone — services must be resolvable by name before any process spawns).
// Bind nil on teardown.
func BindHost(h Host) {
	hostMu.Lock()
	boundHost = h
	hostMu.Unlock()
}

// BoundHost returns the currently bound Host, nil when the slot is free. The
// slot is process-global, so whoever binds it owns releasing it — this getter
// exists so that owner (and its tests) can assert the release actually
// happened, not to let a second consumer probe for a free slot and race the
// first.
func BoundHost() Host {
	return currentHost()
}

func currentHost() Host {
	hostMu.Lock()
	defer hostMu.Unlock()
	return boundHost
}

// Tunables. Fields on the Conn (not consts) so tests can shrink them.
const (
	defaultRoleWait       = 3 * time.Second  // extension loaded + role registered
	defaultHelloWait      = 5 * time.Second  // engine start + inner hello through the tunnel
	defaultConnectTimeout = 30 * time.Second // service dial + auth round-trip
	defaultSendTimeout    = 30 * time.Second // per send/send_image/send_file result
	defaultRestartMax     = 3                // session deaths tolerated per window
	defaultRestartWindow  = 60 * time.Second
	defaultRestartDelay   = time.Second // base backoff, doubles per attempt
)

// inboundBuffer bounds how many normalized messages can queue between
// the protocol session and Receive; overflow drops with a warn (the
// session's dispatcher must never block).
const inboundBuffer = 256

// RegisterDiscovered scans the GLOBAL extensions directory for manifests
// declaring the connector role and registers a chat.Service per hit, so
// `terva bot --connector <name>` (which resolves services by name before
// any extension spawns) can select them. Never called with a project
// directory — project-local extensions do not get the connector role.
// Returns per-extension errors for unreadable manifests; an empty or
// missing directory is not an error.
func RegisterDiscovered(tervaHome, globalExtensionsDir string) []error {
	entries, err := os.ReadDir(globalExtensionsDir)
	if err != nil {
		return nil // no extensions dir yet — nothing to register
	}
	var errs []error
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(globalExtensionsDir, ent.Name(), "extension.json"))
		if err != nil {
			continue // not an extension dir
		}
		var mf extdriver.Manifest
		if err := json.Unmarshal(b, &mf); err != nil {
			errs = append(errs, fmt.Errorf("%s: %v", ent.Name(), err))
			continue
		}
		if !mf.Connector || !mf.IsEnabled() || mf.Name == "" {
			continue
		}
		RegisterService(mf.Name)
	}
	return errs
}

// RegisterService registers one connector extension as a chat service.
// Split from RegisterDiscovered for tests and future explicit paths.
func RegisterService(name string) {
	chat.Register(chat.Service{
		Name: name,
		Kind: "extension",
		// The manifest declared the role; whether the extension can
		// actually dial its service is its own config's business and
		// surfaces as a connect error at /connect time.
		Configured: func(tervaHome string) bool { return true },
		NewConnector: func(tervaHome string, warn func(string)) (chat.Connector, chat.Pairing, error) {
			c := NewConn(name, warn)
			pairing := chat.Pairing{
				AllowedUserID: loadPairing(tervaHome, name),
				Save: func(userID string) error {
					return savePairing(tervaHome, name, userID)
				},
			}
			return c, pairing, nil
		},
		StatusText: func(tervaHome string) (string, error) {
			paired := loadPairing(tervaHome, name)
			if paired == "" {
				paired = "(unpaired)"
			}
			return fmt.Sprintf("connector extension %q\n  configure: terva ext / the /extensions dialog\n  paired user: %s", name, paired), nil
		},
		Reset: func(tervaHome string) (string, error) {
			path := pairingPath(tervaHome, name)
			if err := removePairing(tervaHome, name); err != nil {
				return "", err
			}
			return path, nil
		},
	})
}

// Conn adapts one connector-role extension to chat.Connector. It attaches
// to the live extension process at Connect time; construction is cheap.
type Conn struct {
	name string
	warn func(string)

	roleWait       time.Duration
	helloWait      time.Duration
	connectTimeout time.Duration
	sendTimeout    time.Duration
	restartMax     int
	restartWindow  time.Duration
	restartDelay   time.Duration

	mu         sync.Mutex
	tun        *extdriver.ChatTunnel
	sess       *connhost.Session
	inbound    chan chat.Message
	restarts   []time.Time // Receive-goroutine only
	membership func(chat.Membership)
	events     chat.ChatEventHandlers
}

// SetMembershipHandler installs the admission-event consumer
// (chat.MembershipHandlerSetter). Call before Connect; survives
// redials (each new session re-binds it).
func (c *Conn) SetMembershipHandler(fn func(chat.Membership)) {
	c.mu.Lock()
	c.membership = fn
	c.mu.Unlock()
}

func (c *Conn) deliverMembership(mb chat.Membership) {
	c.mu.Lock()
	fn := c.membership
	c.mu.Unlock()
	if fn != nil {
		fn(mb)
	}
}

// NewConn builds the adapter. warn receives operational lines (dropped
// attachments, stream-down notices); nil defaults to stderr.
func NewConn(name string, warn func(string)) *Conn {
	return &Conn{
		name:           name,
		warn:           warn,
		roleWait:       defaultRoleWait,
		helloWait:      defaultHelloWait,
		connectTimeout: defaultConnectTimeout,
		sendTimeout:    defaultSendTimeout,
		restartMax:     defaultRestartMax,
		restartWindow:  defaultRestartWindow,
		restartDelay:   defaultRestartDelay,
	}
}

func (c *Conn) Name() string { return c.name }

func (c *Conn) warnf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if c.warn != nil {
		c.warn(msg)
		return
	}
	fmt.Fprintln(os.Stderr, msg)
}

// Connect resolves the live extension (waiting briefly for it to finish
// registering the role), opens the tunnel, and runs the connector
// protocol's handshake and connect round trips through it.
func (c *Conn) Connect(ctx context.Context) (chat.Identity, error) {
	return c.dial(ctx)
}

// dial establishes one chat session end to end — role resolution,
// tunnel open, inner handshake, inner connect — and installs it on the
// Conn. Shared by Connect and the crash-recovery redial.
func (c *Conn) dial(ctx context.Context) (chat.Identity, error) {
	h := currentHost()
	if h == nil {
		return chat.Identity{}, fmt.Errorf("connector extension %q: extension subsystem not available in this mode", c.name)
	}
	var ext *extdriver.Extension
	deadline := time.Now().Add(c.roleWait)
	for {
		if e, ok := h.ConnectorExtension(c.name); ok {
			ext = e
			break
		}
		if time.Now().After(deadline) {
			return chat.Identity{}, fmt.Errorf(
				"extension %q is not loaded or %w (is it enabled, with \"connector\": true in its manifest?)", c.name, errRoleUnregistered)
		}
		select {
		case <-ctx.Done():
			return chat.Identity{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}

	tun, err := ext.OpenChat()
	if err != nil {
		return chat.Identity{}, fmt.Errorf("connector extension %q: %w", c.name, err)
	}
	// The inbound queue is long-lived — shared across redials, like the
	// external proxy's — so messages buffered when a session dies still
	// reach Receive after the next session is up.
	c.mu.Lock()
	if c.inbound == nil {
		c.inbound = make(chan chat.Message, inboundBuffer)
	}
	inbound := c.inbound
	c.mu.Unlock()
	sess := connhost.New(connhost.Config{
		Name:              c.name,
		DataDir:           ext.DataDir(),
		HostVersion:       tervaVersion(),
		Conn:              tun,
		DeliverMembership: c.deliverMembership,
		Events: chat.ChatEventHandlers{
			Edited: func(ev chat.MessageEdited) {
				if h := c.chatEvents().Edited; h != nil {
					h(ev)
				}
			},
			Deleted: func(ev chat.MessageDeleted) {
				if h := c.chatEvents().Deleted; h != nil {
					h(ev)
				}
			},
			Reaction: func(ev chat.Reaction) {
				if h := c.chatEvents().Reaction; h != nil {
					h(ev)
				}
			},
		},
		Deliver: func(m chat.Message) {
			select {
			case inbound <- m:
			default:
				c.warnf("connector extension %q: inbound queue full; dropping a message", c.name)
			}
		},
		Warn:        func(msg string) { c.warnf("%s", msg) },
		SendTimeout: c.sendTimeout,
	})
	if err := sess.Start(c.helloWait); err != nil {
		tun.Close()
		return chat.Identity{}, err
	}
	id, err := sess.Connect(ctx, c.connectTimeout)
	if err != nil {
		tun.Close()
		return chat.Identity{}, err
	}

	c.mu.Lock()
	c.tun = tun
	c.sess = sess
	c.mu.Unlock()
	return id, nil
}

// Receive delivers inbound messages until ctx ends. Session death gets
// the external proxy's crash posture, adapted to a dual-role process:
// when the extension ENDED the session but lives on (chat_down with a
// reason), the session is reopened — the engine re-dials through a
// fresh transport; when the PROCESS died, the extension subsystem is
// asked to respawn it (RestartHost — extension tools dispatch by name
// through the driver's live index, so the respawn re-binds the agent's
// tool wrappers automatically) before reopening. Both share one budget:
// past restartMax deaths in restartWindow, Receive returns a permanent
// error. Where the bound Host cannot respawn (no RestartHost), a
// process death is permanent immediately. Transient service blips
// remain the transport's job to retry internally, exactly as for a
// standalone connector.
func (c *Conn) Receive(ctx context.Context, handle func(chat.Message)) error {
	for {
		c.mu.Lock()
		tun, sess, inbound := c.tun, c.sess, c.inbound
		c.mu.Unlock()
		if sess == nil {
			return fmt.Errorf("connector extension %q: not connected", c.name)
		}
	session:
		for {
			select {
			case <-ctx.Done():
				// Consumer is leaving (daemon shutdown, bridge
				// disconnect): close the session but leave the PROCESS
				// — and its tools — alone.
				tun.Close()
				return ctx.Err()
			case m := <-inbound:
				handle(m)
			case <-sess.Done():
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err := c.redial(ctx, sess.Err()); err != nil {
					return err
				}
				break session // dial installed a fresh session
			}
		}
	}
}

// errRoleUnregistered is dial's verdict that the extension is not there to be
// dialled — not loaded, or loaded without the connector role. redial treats it
// as "the process is gone" and asks the host to respawn; every other dial
// failure is a live process refusing us, where a respawn would not help.
//
// A sentinel rather than a phrase, because the decision and the sentence that
// used to carry it sit 185 lines apart. See the errors.Is below.
var errRoleUnregistered = errors.New("did not register the connector role")

// redial re-establishes the chat session after a death, under the
// restart budget. cause is the dead session's Err(): non-nil means the
// extension reported its engine dead while the process lives on (reopen
// only); nil means the process itself died (respawn, then reopen —
// orderly closes don't reach here because the consumer's ctx ends
// first). Returns nil once a fresh session is installed, or the
// permanent error Receive should surface.
func (c *Conn) redial(ctx context.Context, cause error) error {
	needRespawn := cause == nil
	if cause == nil {
		cause = fmt.Errorf("process exited")
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A host that cannot respawn makes a process death permanent —
		// fail before burning budget or sleeping on a lost cause.
		var rh RestartHost
		if needRespawn {
			var ok bool
			if rh, ok = currentHost().(RestartHost); !ok {
				return fmt.Errorf("connector extension %q exited and this mode cannot respawn it; its chat stream ended (see its log)", c.name)
			}
		}
		// The proxy's exact budget policy.
		now := time.Now()
		keep := c.restarts[:0]
		for _, t := range c.restarts {
			if now.Sub(t) < c.restartWindow {
				keep = append(keep, t)
			}
		}
		c.restarts = keep
		if len(c.restarts) >= c.restartMax {
			return fmt.Errorf("connector extension %q: chat session keeps dying (%d attempts in %s; last error: %v; see the extension's log)",
				c.name, len(c.restarts), c.restartWindow, cause)
		}
		c.restarts = append(c.restarts, now)
		attempt := len(c.restarts)

		delay := c.restartDelay << (attempt - 1)
		verb := "reopening the session"
		if needRespawn {
			verb = "respawning the process"
		}
		c.warnf("connector extension %q: chat session died (%v); %s in %s (attempt %d/%d)",
			c.name, cause, verb, delay, attempt, c.restartMax)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}

		if needRespawn {
			if err := rh.RestartExtension(ctx, c.name); err != nil {
				c.warnf("connector extension %q: respawn failed: %v", c.name, err)
				cause = err
				continue // still needRespawn
			}
		}
		if _, err := c.dial(ctx); err != nil {
			c.warnf("connector extension %q: reconnect failed: %v", c.name, err)
			cause = err
			// A vanished role means the process is gone (again);
			// anything else — inner connect_error, handshake failure —
			// is a live process refusing us, where a respawn wouldn't
			// help.
			//
			// errors.Is, not a substring of dial's message. The two sit 185
			// lines apart and this used to match the prose between them, so
			// rewording that sentence would have silently stopped every
			// subsequent crash from being recovered — and the only test that
			// would have failed asserts the MESSAGE, which reads like a
			// wording change to whoever updates it.
			needRespawn = errors.Is(err, errRoleUnregistered)
			continue
		}
		return nil
	}
}

func (c *Conn) Send(ctx context.Context, out chat.Outgoing) error {
	sess, err := c.connected()
	if err != nil {
		return err
	}
	return sess.Send(ctx, out)
}

func (c *Conn) SendImage(ctx context.Context, chatID, path, caption string) error {
	sess, err := c.connected()
	if err != nil {
		return err
	}
	return sess.SendImage(ctx, chatID, path, caption)
}

func (c *Conn) SendFile(ctx context.Context, chatID, path, caption string) error {
	sess, err := c.connected()
	if err != nil {
		return err
	}
	return sess.SendFile(ctx, chatID, path, caption)
}

func (c *Conn) Typing(ctx context.Context, chatID string) error {
	sess, err := c.connected()
	if err != nil {
		return err
	}
	return sess.Typing(chatID)
}

// Ask runs one interactive question through the tunneled wire
// (chat.Asker). Callers gate on Capabilities().Asks, as everywhere.
// An ask does not survive a redial — the session's death fails the
// in-flight Ask and the caller's fail-closed posture takes it from
// there.
func (c *Conn) Ask(ctx context.Context, a chat.Ask) (chat.Answer, error) {
	sess, err := c.connected()
	if err != nil {
		return chat.Answer{}, err
	}
	return sess.Ask(ctx, a)
}

// StartThread opens a work-stream thread through the tunneled wire
// (chat.Threader). Callers gate on Capabilities().ThreadsOut.
func (c *Conn) StartThread(ctx context.Context, chatID, fromMessageID, name string) (string, error) {
	sess, err := c.connected()
	if err != nil {
		return "", err
	}
	return sess.StartThread(ctx, chatID, fromMessageID, name)
}

func (c *Conn) Capabilities() chat.Capabilities {
	c.mu.Lock()
	sess := c.sess
	c.mu.Unlock()
	if sess == nil {
		return chat.Capabilities{}
	}
	return sess.Capabilities()
}

func (c *Conn) connected() (*connhost.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess == nil {
		return nil, fmt.Errorf("connector extension %q: not connected", c.name)
	}
	return c.sess, nil
}

// SetChatEventHandlers installs the stage-D inbound event consumers
// (chat.ChatEventsSetter). Call before Connect.
func (c *Conn) SetChatEventHandlers(h chat.ChatEventHandlers) {
	c.mu.Lock()
	c.events = h
	c.mu.Unlock()
}

func (c *Conn) chatEvents() chat.ChatEventHandlers {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.events
}

// EditMessage / React / DeleteMessage delegate the stage-D outbound
// commands (chat.Editor / chat.Reactor / chat.Deleter). Gate on the
// matching Capabilities field, as everywhere.
func (c *Conn) EditMessage(ctx context.Context, chatID, messageID, text string) error {
	sess, err := c.connected()
	if err != nil {
		return err
	}
	return sess.EditMessage(ctx, chatID, messageID, text)
}

func (c *Conn) React(ctx context.Context, chatID, messageID, key string, remove bool) error {
	sess, err := c.connected()
	if err != nil {
		return err
	}
	return sess.React(ctx, chatID, messageID, key, remove)
}

func (c *Conn) DeleteMessage(ctx context.Context, chatID, messageID string) error {
	sess, err := c.connected()
	if err != nil {
		return err
	}
	return sess.DeleteMessage(ctx, chatID, messageID)
}
