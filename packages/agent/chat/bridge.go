package chat

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/provider"
)

// bridgePhase is the bridge's lifecycle state. Start claims idle→starting under
// the lock BEFORE dialing, so two concurrent Starts can't both call Connect; the
// receive goroutine (not Stop) drives the return to idle, so the connector slot
// the loop holds is only surrendered once Receive has actually returned.
type bridgePhase int32

const (
	phaseIdle bridgePhase = iota
	phaseStarting
	phaseRunning
	phaseStopping
)

// defaultStopTimeout bounds Stop's join on the receive loop (and a restart's
// wait for a prior teardown) when the bridge sets no stopTimeout of its own.
const defaultStopTimeout = 10 * time.Second

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

// Bridge mirrors the paired owner's DM conversation into a running TUI
// session: the owner's direct messages forward to the Host's agent, and
// the TUI's turns mirror back out to that DM. One bridge per Interactive
// instance, one conversation.
//
// It is deliberately DM-only. Unlike the daemon Loop, the bridge has a
// single Host/agent/transcript, so it cannot isolate multiple approved
// group chats the way per-chat agents do: a group message would inject
// (unattributed) into the owner's session and, via rememberChat, retarget
// where the owner's own replies are sent. Group admission is the daemon
// Loop's job — run `terva bot` for that. The gate is therefore built with
// a nil admissions store, which keeps every non-DM chat silent.
type Bridge struct {
	Connector Connector
	Host      Host

	// Pairing, HelpText, PairedText configure the shared gate; see
	// Loop for semantics.
	Pairing    Pairing
	HelpText   string
	PairedText string

	mu    sync.Mutex
	phase bridgePhase
	// cancel ends the current lifetime's context; set when a Start claims the
	// slot (phaseStarting), cleared when the loop settles back to idle.
	cancel context.CancelFunc
	// done closes when the receive goroutine exits AND the bridge has returned
	// to phaseIdle; Stop joins on it so "Stop returned" means "the connector is
	// released", and a restart joins on it so a reconnect never overlaps a
	// still-draining receiver. For a connector extension the receive loop's exit
	// is what closes the driver's one-chat-session tunnel slot — an async Stop
	// let an immediate reconnect race that teardown and be refused with "a chat
	// session is already open on this extension" (the CI flake behind #62/#63's
	// deadline raises: no deadline fixes a dial that already failed).
	done     chan struct{}
	identity Identity
	chatID   string // populated after first DM from the paired user
	gate     *gate

	// stopTimeout bounds both Stop's join on the receive loop and a restart's
	// wait for a prior teardown to release the slot; 0 uses defaultStopTimeout.
	// A wedge guard, not a deadline on Receive: a hung handle() must not hang a
	// daemon shutdown; the slot frees whenever the loop finally exits.
	stopTimeout time.Duration

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
	return b.phase == phaseRunning
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
	return BridgeState{Running: b.phase == phaseRunning, Username: b.identity.Username, PairedID: pid}
}

// stopWait is the bounded budget Stop (and a restart's Start) waits on the
// receive loop before giving up as a wedge guard.
func (b *Bridge) stopWait() time.Duration {
	if b.stopTimeout > 0 {
		return b.stopTimeout
	}
	return defaultStopTimeout
}

// settleIdle returns the bridge to idle when a lifetime ends — the receive loop
// exited, or a start failed / was preempted before the loop launched — and wakes
// everyone joined on done (a Stop, or a restart waiting out the teardown).
func (b *Bridge) settleIdle(done chan struct{}) {
	b.mu.Lock()
	b.phase = phaseIdle
	b.cancel = nil
	if b.done == done {
		b.done = nil
	}
	b.mu.Unlock()
	close(done)
}

// joinDone waits (bounded) for the receive loop to release the connector. On
// timeout it warns and returns — the wedge guard — leaving the loop to free the
// slot when it finally exits.
func (b *Bridge) joinDone(done chan struct{}) {
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(b.stopWait()):
		if b.Host != nil {
			b.Host.Notify("warn", fmt.Sprintf("%s: receive loop still draining after Stop; its session slot frees when it exits", b.Connector.Name()))
		}
	}
}

// Start connects and begins receiving. Idempotent: a second call while starting
// or running returns nil and never dials a second connector session. A call
// while a prior Stop is still draining waits (bounded) for the slot to free
// before reconnecting, so a restart never overlaps a still-live receiver.
func (b *Bridge) Start(parent context.Context) error {
	b.mu.Lock()
	// A prior lifetime still tearing down: wait for its receive loop to release
	// the connector before claiming the slot, so a reconnect never overlaps a
	// still-draining receiver (old inbound entering a new lifetime; for a
	// connector extension, the one-chat-session slot refused).
	for b.phase == phaseStopping {
		done := b.done
		b.mu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-time.After(b.stopWait()):
				return fmt.Errorf("%s bridge: previous session still draining; retry", b.Connector.Name())
			}
		}
		b.mu.Lock()
	}
	if b.phase != phaseIdle {
		// Already starting or running: idempotent, and — crucially — never a
		// second Connect. Concurrent Starts serialize here; the first claims
		// phaseStarting under the lock, the rest return without dialing.
		b.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	b.phase = phaseStarting
	b.cancel = cancel
	b.done = done
	b.mu.Unlock()

	id, err := b.Connector.Connect(ctx)
	if err != nil {
		cancel()
		b.settleIdle(done)
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
	if b.phase != phaseStarting {
		// A Stop arrived while Connect was in flight and moved us to
		// phaseStopping. No receive loop launched, so release here; the Stop
		// joined on done returns with the slot free.
		b.mu.Unlock()
		cancel()
		b.settleIdle(done)
		return nil
	}
	b.phase = phaseRunning
	b.identity = id
	// If a user paired in a previous session, adopt their DM chat
	// when the connector can address them directly. (For telegram,
	// private-chat ids equal user ids; the connector seeds ChatID on
	// the first inbound message either way.)
	if b.Pairing.AllowedUserID != "" && b.chatID == "" {
		b.chatID = b.Pairing.AllowedUserID
	}
	b.gate = &gate{
		pairing: b.Pairing,
		// DM-only: a nil admissions store keeps every non-DM chat
		// silent. See the Bridge doc for why groups can't be mirrored.
		admissions:  nil,
		botUsername: id.Username,
		helpText:    help,
		pairedText:  paired,
		onPaired: func(m Message) {
			b.mu.Lock()
			b.chatID = m.ChatID
			b.mu.Unlock()
			b.Host.Notify("success", fmt.Sprintf("%s paired with user %s (@%s)", b.Connector.Name(), m.UserID, m.Username))
		},
	}
	b.mu.Unlock()

	go func() {
		defer b.settleIdle(done)
		if err := b.Connector.Receive(ctx, b.handle); err != nil && ctx.Err() == nil {
			b.Host.Notify("warn", fmt.Sprintf("%s: receive: %v", b.Connector.Name(), err))
		}
	}()
	return nil
}

// Stop halts the receive loop and waits for it to exit, so the connector
// (and, for a connector extension, the driver's single chat-tunnel slot) is
// actually released when Stop returns — a disconnect immediately followed by
// a reconnect must find the slot free, not race the teardown. Safe to call
// when not running. The wait is bounded (stopTimeout, default 10s) as a wedge
// guard: a connector whose in-flight handle() never returns should not hang a
// daemon shutdown; the slot then frees whenever the loop finally exits.
func (b *Bridge) Stop() {
	b.mu.Lock()
	switch b.phase {
	case phaseIdle:
		b.mu.Unlock()
		return
	case phaseStopping:
		// Another Stop already cancelled and owns the transition to idle; join
		// the same teardown so this call, too, returns only once the loop exits.
		done := b.done
		b.mu.Unlock()
		b.joinDone(done)
		return
	}
	// phaseStarting or phaseRunning: this call owns the teardown.
	cancel := b.cancel
	done := b.done
	b.phase = phaseStopping
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	b.joinDone(done)
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
		_ = b.Connector.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID, Text: b.Host.Status()})
	case actStop:
		b.rememberChat(m.ChatID)
		b.Host.CancelTurn()
		_ = b.Connector.Send(ctx, Outgoing{ChatID: m.ChatID, ReplyTo: m.ID, Text: "cancelled the current turn."})
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
	running := b.phase == phaseRunning
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
	if b.phase != phaseRunning {
		return "", fmt.Errorf("%s bridge is not running", b.Connector.Name())
	}
	if b.chatID == "" {
		return "", fmt.Errorf("%s bridge has no paired chat yet", b.Connector.Name())
	}
	return b.chatID, nil
}
