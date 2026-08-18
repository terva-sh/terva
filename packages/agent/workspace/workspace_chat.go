package workspace

// The chat bridge, owned by the workspace.
//
// A bridge mirrors a paired DM conversation into one live session: the owner's
// direct messages become prompts, and the session's assistant turns mirror back
// out. It used to live in the TUI, which meant the connector's receive loop ran
// in the client process, submitted prompts through TUI methods, and mutated the
// agent's tool registry by hand. None of that exists against a remote carrier —
// the agent, the credentials and the transcript are all here.
//
// Bound to a session id, not to "the current session": the mirror stays where
// it was connected. A connector must never take control of whichever pane a
// client happens to be showing, and that assumption is the first to break once
// a workspace hosts several sessions and several connections.

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/chat/extconn"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/extdriver"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// wsChat is the workspace's bridge registry, keyed by chat service name.
//
// v1 admits at most one entry. Not taste, and not the `terva bot` pidfile rule
// (that only excludes a daemon on the SAME service): connector extensions
// resolve their live process through extconn's single process-global host slot,
// so two concurrent bridges wanting two different extensions would clobber each
// other. The map is the shape the feature wants — several sessions, several
// connections — so lifting the cap is a guard removal plus a per-connector host
// registry, not a re-architecture.
type wsChat struct {
	mu      sync.Mutex
	bridges map[string]*boundBridge
	// dials counts chatDial goroutines still in flight.
	//
	// 🪤 Stopping the BRIDGES is not the same as stopping the connect, and the
	// gap is not idle time: a dial that is past its handshake goes on to rebuild
	// the bound session's tools, which installs terva's docs tree into
	// TERVA_HOME. So a dial racing teardown keeps WRITING after the thing that
	// started it is gone — invisible in production, where the home outlives the
	// process, and a "TempDir RemoveAll: directory not empty" flake in any test
	// that points TERVA_HOME at a directory it then removes.
	//
	// Waited on in chatWaitDials rather than in chatStopAll, because that runs
	// BEFORE the workspace context is cancelled and a connector handshake is a
	// network round-trip: joining there would trade a leaked goroutine for a
	// Close that blocks up to the connector's timeout.
	dials sync.WaitGroup
}

// boundBridge is one live (or dialing) bridge and the session it serves.
type boundBridge struct {
	bridge *chat.Bridge
	sess   string // session id the mirror is bound to
	state  string // connecting | connected | error
	err    string // last dial failure, surfaced on the pane
}

const (
	chatStateIdle       = "idle"
	chatStateConnecting = "connecting"
	chatStateConnected  = "connected"
	chatStateError      = "error"
)

// --- registry -----------------------------------------------------------

// chatBound returns the bridge bound to sessID, its connector capabilities, and
// whether it is actually receiving. injectExtraTools uses it to decide whether
// this session's model sees the chat-send tools — so a second session never
// sees another's.
func (w *Workspace) chatBound(sessID string) (*chat.Bridge, chat.Capabilities, bool) {
	if sessID == "" {
		return nil, chat.Capabilities{}, false
	}
	w.chat.mu.Lock()
	defer w.chat.mu.Unlock()
	for _, b := range w.chat.bridges {
		if b.sess == sessID && b.bridge != nil && b.bridge.Active() {
			return b.bridge, b.bridge.Connector.Capabilities(), true
		}
	}
	return nil, chat.Capabilities{}, false
}

// chatMirror returns the bridge bound to sessID for outbound mirroring. Same
// lookup as chatBound without the capability probe.
func (w *Workspace) chatMirror(sessID string) *chat.Bridge {
	b, _, ok := w.chatBound(sessID)
	if !ok {
		return nil
	}
	return b
}

// chatSessionFor reports which session a service's bridge is bound to.
func (w *Workspace) chatSessionFor(service string) string {
	w.chat.mu.Lock()
	defer w.chat.mu.Unlock()
	if b := w.chat.bridges[service]; b != nil {
		return b.sess
	}
	return ""
}

// chatStopAll stops every bridge. Called from Workspace.Close, so a connector's
// receive goroutine never outlives the workspace that owns it.
func (w *Workspace) chatStopAll() {
	w.chat.mu.Lock()
	live := make([]*chat.Bridge, 0, len(w.chat.bridges))
	for _, b := range w.chat.bridges {
		if b.bridge != nil {
			live = append(live, b.bridge)
		}
	}
	if len(w.chat.bridges) > 0 {
		w.chatUnbindExtHostLocked()
	}
	w.chat.bridges = nil
	w.chat.mu.Unlock()
	for _, b := range live {
		b.Stop()
	}
}

// chatStopForSession stops any bridge bound to sessID. Called when that session
// closes (a delete, or workspace teardown): a mirror without a session has
// nowhere to deliver.
func (w *Workspace) chatStopForSession(sessID string) {
	if sessID == "" {
		return
	}
	w.chat.mu.Lock()
	var live []*chat.Bridge
	removed := false
	for name, b := range w.chat.bridges {
		if b.sess == sessID {
			if b.bridge != nil {
				live = append(live, b.bridge)
			}
			delete(w.chat.bridges, name)
			removed = true
		}
	}
	if removed {
		w.chatUnbindExtHostLocked()
	}
	w.chat.mu.Unlock()
	for _, b := range live {
		b.Stop()
	}
}

// --- actions ------------------------------------------------------------

// chatConnect dials the named service ("" = the registry default) and binds its
// bridge to sessID. Returns as soon as the dial is armed: a connector handshake
// is a network round-trip (up to 30s for a connector extension), and the pane
// converges over surface_updated events rather than blocking a client's action.
func (w *Workspace) chatConnect(sessID, name string) error {
	if _, err := w.resolve(sessID); err != nil {
		return err
	}
	if name == "" {
		name = chat.DefaultServiceName()
	}
	svc, ok := chat.Lookup(name)
	if !ok {
		if name == "" {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("no chat connectors compiled into this binary"))
		}
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("unknown chat connector %q (available: %s)", name, chatServiceNames()))
	}
	if !svc.Configured(w.root) {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("%s: not configured — run `terva bot setup` first", svc.Name))
	}
	// A background `terva bot` daemon polling the same service is a second
	// consumer: both race each update and one always loses, so messages arrive
	// half-delivered. Stop it first.
	if pid, alive, _ := chat.IsRunning(w.root, svc.Name); alive && pid > 0 {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest,
			"%s", i18n.T("%s: bot daemon already running (pid %d) — stop it with `terva bot stop` first", svc.Name, pid))
	}

	w.chat.mu.Lock()
	if w.chat.bridges == nil {
		w.chat.bridges = map[string]*boundBridge{}
	}
	for name, b := range w.chat.bridges {
		// A corpse from a failed dial must not wedge /connect until an explicit
		// disconnect: nothing is live behind it, so a retry replaces it. Anything
		// actually live — connected or mid-dial — keeps the one-bridge cap.
		if b.bridge == nil && b.state == chatStateError {
			delete(w.chat.bridges, name)
			continue
		}
		w.chat.mu.Unlock()
		if b.state == chatStateConnecting {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest,
				"%s", i18n.T("%s is still connecting — wait for it, or disconnect first", name))
		}
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest,
			"%s", i18n.T("%s is already connected — disconnect it first (one bridge per workspace: connector extensions share a single host slot)", name))
	}
	w.chat.bridges[svc.Name] = &boundBridge{sess: sessID, state: chatStateConnecting}
	// Bind under the registry lock: every slot mutation happens inside a
	// w.chat.mu critical section, so an unbind from a dying bridge can never
	// clobber the bind of the one replacing it.
	w.chatBindExtHostLocked(svc.Name)
	w.chat.mu.Unlock()
	w.chatChanged()

	w.chat.dials.Add(1)
	go w.chatDial(svc, sessID)
	return nil
}

// chatWaitDials blocks until every in-flight dial has returned.
//
// Called from Close AFTER the workspace context is cancelled: a dial parked in
// its handshake aborts on that context, so this joins promptly instead of
// waiting out a network timeout. Safe to call with no dial running.
func (w *Workspace) chatWaitDials() { w.chat.dials.Wait() }

// chatDial performs the blocking half of a connect: build the connector, run the
// handshake, start receiving, then re-derive the bound session's tools so the
// model gains chat_send_image / chat_send_file on its next turn.
func (w *Workspace) chatDial(svc chat.Service, sessID string) {
	defer w.chat.dials.Done()
	host := &chatWsHost{w: w, service: svc.Name}
	conn, pairing, err := svc.NewConnector(w.root, func(msg string) { host.Notify("warn", msg) })
	if err == nil {
		// DM-only, deliberately: the bridge has a single session and transcript,
		// so it cannot isolate approved group chats the way the bot daemon's
		// per-chat agents do. Group serving is `terva bot`'s job. chat.Bridge
		// builds its gate with a nil admissions store, keeping every non-DM chat
		// silent.
		b := &chat.Bridge{Connector: conn, Host: host, Pairing: pairing}
		if err = b.Start(w.ctx); err == nil {
			if !w.chatSetLive(svc.Name, b) {
				// A disconnect raced the dial: the registry entry is gone and
				// whoever removed it released the host slot. Stop the bridge that
				// just came up — nothing owns it, and its receive loop would
				// otherwise hold the connector until workspace close.
				b.Stop()
				return
			}
			w.chatRebuildBound(sessID, "chat-connect")
			w.chatChanged()
			host.Notify("success", chatConnectedLabel(svc, b.State()))
			return
		}
	}
	w.chatSetError(svc.Name, err.Error())
	w.chatChanged()
	host.Notify("error", i18n.T("%s connect failed: %v", svc.Name, err))
}

// chatDisconnect stops the live bridge and re-derives the bound session's tools,
// so the model stops seeing chat-send tools it can no longer use.
func (w *Workspace) chatDisconnect() error {
	w.chat.mu.Lock()
	var (
		live    *chat.Bridge
		sess    string
		removed bool
	)
	for name, b := range w.chat.bridges {
		live, sess, removed = b.bridge, b.sess, true
		delete(w.chat.bridges, name)
		break
	}
	if removed {
		w.chatUnbindExtHostLocked()
	}
	w.chat.mu.Unlock()
	if live == nil && sess == "" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("no chat bridge is connected"))
	}
	if live != nil {
		live.Stop()
	}
	w.chatRebuildBound(sess, "chat-disconnect")
	w.chatChanged()
	return nil
}

// chatRebind moves the mirror to another live session. An explicit user act:
// nothing rebinds automatically, least of all a client switching panes.
func (w *Workspace) chatRebind(sessID string) error {
	if _, err := w.resolve(sessID); err != nil {
		return err
	}
	w.chat.mu.Lock()
	var prev string
	found := false
	for _, b := range w.chat.bridges {
		prev, b.sess, found = b.sess, sessID, true
		break
	}
	w.chat.mu.Unlock()
	if !found {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("no chat bridge is connected"))
	}
	// Both sessions change their model-facing tool set: the old one loses the
	// chat-send tools, the new one gains them.
	w.chatRebuildBound(prev, "chat-disconnect")
	w.chatRebuildBound(sessID, "chat-connect")
	w.chatChanged()
	return nil
}

// chatSetLive installs the dialed bridge on its registry entry. False means the
// entry is gone — a disconnect raced the dial — and the caller owns stopping
// the bridge it just started.
func (w *Workspace) chatSetLive(service string, b *chat.Bridge) bool {
	w.chat.mu.Lock()
	defer w.chat.mu.Unlock()
	if bb := w.chat.bridges[service]; bb != nil {
		bb.bridge, bb.state, bb.err = b, chatStateConnected, ""
		return true
	}
	return false
}

func (w *Workspace) chatSetError(service, msg string) {
	w.chat.mu.Lock()
	defer w.chat.mu.Unlock()
	if bb := w.chat.bridges[service]; bb != nil {
		bb.state, bb.err = chatStateError, msg
		// The corpse stays on the pane, but nothing live remains behind it:
		// release the host slot now. Guarded by the entry existing — if a
		// disconnect already removed it, the remover unbound, and a fresh
		// connect may have bound since; unbinding here would clobber it.
		w.chatUnbindExtHostLocked()
	}
}

// chatRebuildBound re-derives one session's tools. The chat tools are a
// declarative input to injectExtraTools, exactly as auto-swarm's swarm_spawn is,
// so connect/disconnect never patch a live registry — they change the input and
// re-run the derivation.
func (w *Workspace) chatRebuildBound(sessID, reason string) {
	if sessID == "" {
		return
	}
	if s := w.existing(sessID); s != nil {
		s.rebuildTools(reason)
	}
}

// chatChanged tells every client the chat pane moved.
func (w *Workspace) chatChanged() {
	w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("chat"))
}

// --- the extension host binding -------------------------------------------

// wsExtHost points extconn's process-global slot at the extension subsystem of
// whichever session the bridge is CURRENTLY bound to — resolved per call, like
// chatWsHost, so a rebind moves the crash-recovery respawn path with the
// mirror, and a session that was rematerialized since connect resolves to its
// live manager. Binding a session's *extensions.Manager directly would go
// stale on both.
//
// This is what makes `/connect <ext-connector>` work against a workspace at
// all: extconn.Conn resolves its live extension process through the bound
// Host at dial time, and until the workspace owned the bridge nothing on this
// path ever bound one (only the `terva bot` CLI setup did).
type wsExtHost struct {
	w       *Workspace
	service string
}

// mgr resolves the bound session's extension manager. The empty-id guard is
// load-bearing: resolve("") materializes the DEFAULT session — a client
// affordance an internal host must never inherit, or an unbound host would
// reach into whatever session happens to be latest on disk.
func (h *wsExtHost) mgr() *extensions.Manager {
	sid := h.w.chatSessionFor(h.service)
	if sid == "" {
		return nil
	}
	s, err := h.w.resolve(sid)
	if err != nil || s == nil {
		return nil
	}
	return s.extMgr
}

func (h *wsExtHost) ConnectorExtension(name string) (*extdriver.Extension, bool) {
	m := h.mgr()
	if m == nil {
		return nil, false
	}
	return m.ConnectorExtension(name)
}

// RestartExtension makes the workspace binding a full extconn.RestartHost, so
// a connector extension that crashes mid-bridge gets the same respawn budget
// it has under `terva bot run --connector` instead of dying permanently.
func (h *wsExtHost) RestartExtension(ctx context.Context, name string) error {
	m := h.mgr()
	if m == nil {
		return fmt.Errorf("no session is bound to chat service %q", h.service)
	}
	return m.RestartExtension(ctx, name)
}

// chatBindExtHostLocked / chatUnbindExtHostLocked own extconn's process-global
// host slot. Callers hold w.chat.mu: routing every slot mutation through the
// registry lock linearizes them, so an unbind on one bridge's death can never
// clobber the bind of the bridge replacing it. One bridge per workspace is the
// invariant that makes the global safe here (see wsChat); compiled-in
// connectors never consult the slot, so binding unconditionally at connect is
// harmless and keeps the lifecycle uniform.
func (w *Workspace) chatBindExtHostLocked(service string) {
	extconn.BindHost(&wsExtHost{w: w, service: service})
}

func (w *Workspace) chatUnbindExtHostLocked() {
	extconn.BindHost(nil)
}

// --- pane ---------------------------------------------------------------

func (w *Workspace) chatView() ctrlproto.ChatView {
	var cv ctrlproto.ChatView
	for _, svc := range chat.Services() {
		info := ctrlproto.ChatServiceInfo{
			Name:       svc.Name,
			Kind:       svc.Kind,
			Dev:        svc.Dev,
			Configured: svc.Configured(w.root),
		}
		if info.Configured {
			if _, pairing, err := svc.NewConnector(w.root, nil); err == nil {
				info.Paired = pairing.AllowedUserID != ""
			}
		}
		cv.Services = append(cv.Services, info)
	}

	w.chat.mu.Lock()
	var bb *boundBridge
	var service string
	for name, b := range w.chat.bridges {
		bb, service = b, name
		break
	}
	if bb == nil {
		w.chat.mu.Unlock()
		cv.Bridge = ctrlproto.ChatBridgeState{State: chatStateIdle}
		cv.DaemonPID = w.chatDaemonPID()
		return cv
	}
	st := ctrlproto.ChatBridgeState{
		State: bb.state, Connector: service, Session: bb.sess, Error: bb.err,
	}
	live := bb.bridge
	w.chat.mu.Unlock()

	if live != nil {
		s := live.State()
		st.Username, st.PairedID = s.Username, s.PairedID
	}
	cv.Bridge = st
	cv.DaemonPID = w.chatDaemonPID()
	return cv
}

// chatDaemonPID reports a `terva bot` daemon holding the default service's poll
// loop, so the pane can explain why connect is refused.
func (w *Workspace) chatDaemonPID() int {
	name := chat.DefaultServiceName()
	if name == "" {
		return 0
	}
	if pid, alive, _ := chat.IsRunning(w.root, name); alive && pid > 0 {
		return pid
	}
	return 0
}

// chatAction applies a chat-pane action. sessID is the session whose surface
// issued it — the bind target for connect.
func (w *Workspace) chatAction(sessID, action string, args map[string]string) error {
	switch action {
	case "connect":
		return w.chatConnect(sessID, args["name"])
	case "disconnect":
		return w.chatDisconnect()
	case "rebind":
		target := args["session"]
		if target == "" {
			target = sessID
		}
		return w.chatRebind(target)
	}
	return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("unknown chat action %q", action))
}

// chatServiceNames lists every registered service with its provenance tag.
func chatServiceNames() string {
	var names []string
	for _, svc := range chat.Services() {
		name := svc.Name
		if tag := chatServiceTag(svc); tag != "" {
			name += " (" + tag + ")"
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

func chatServiceTag(svc chat.Service) string {
	var tags []string
	if svc.Kind != "" {
		tags = append(tags, svc.Kind)
	}
	if svc.Dev {
		tags = append(tags, "dev")
	}
	return strings.Join(tags, ", ")
}

func chatConnectedLabel(svc chat.Service, st chat.BridgeState) string {
	name := svc.Name
	if tag := chatServiceTag(svc); tag != "" {
		name += " (" + tag + ")"
	}
	label := i18n.T("%s connected", name)
	if st.Username != "" {
		label += " " + i18n.T("as @%s", st.Username)
	}
	if st.PairedID == "" {
		label += " — " + i18n.T("send /start to the bot to claim it")
	}
	return label
}

// --- the daemon-side chat.Host ------------------------------------------

// chatWsHost adapts the bound workspace session to chat.Host. It holds the
// service name, never a *wsSession: every call re-resolves the bound session id,
// so a rebind takes effect immediately and an evicted session rematerializes.
//
// Note what is absent. The TUI's Host implementation routed prompts through
// Interactive.SubmitOrQueue, whose first act was shellEscapeCommand — so a
// paired DM reading "!rm -rf ~" ran a shell command on the host with no approval
// gate. Here a prompt is a prompt: it goes to the model, behind the session's
// confirm gate, and a leading "!" is prose.
type chatWsHost struct {
	w       *Workspace
	service string
}

func (h *chatWsHost) session() string { return h.w.chatSessionFor(h.service) }

// bound resolves the session the bridge is bound to, refusing the empty id.
// The refusal is load-bearing: resolve("") materializes the DEFAULT session —
// a client affordance — and this host can hold the empty id in the window
// between the bridge leaving the registry and its receive loop stopping. A DM
// delivered in that window must be dropped, not routed into whatever session
// happens to be latest on disk.
func (h *chatWsHost) bound() *wsSession {
	sid := h.session()
	if sid == "" {
		return nil
	}
	s, err := h.w.resolve(sid)
	if err != nil {
		return nil
	}
	return s
}

func (h *chatWsHost) SubmitOrQueue(text string, images []provider.ImageBlock) {
	s := h.bound()
	if s == nil {
		return
	}
	// Submit through the INTERNAL seam, not Workspace.Prompt: the client-facing
	// entries mirror OnUserTyped back out to the chat, and a message that
	// arrived from the chat must not be echoed to it.
	if err := s.promptBlocks(text, images, core.UserMessageExtras{}); err != nil {
		// Busy: queue behind the running turn. Text-only, matching the queue's
		// contract — images only ever reach an immediately-started turn.
		s.queue(text)
	}
}

func (h *chatWsHost) CancelTurn() {
	if s := h.bound(); s != nil {
		s.interruptTurn()
	}
}

func (h *chatWsHost) Status() string {
	s := h.bound()
	if s == nil {
		return i18n.T("session unavailable")
	}
	prov, model := s.currentModel()
	s.mu.Lock()
	busy := s.turnCancel != nil
	cwd := s.cwd
	s.mu.Unlock()

	var (
		usage   provider.Usage
		ctxUsed int
		queued  int
	)
	if ag := s.agent; ag != nil {
		usage = ag.Cost()
		last := ag.LastTurnUsage()
		ctxUsed = last.PromptTokens()
		queued = ag.QueuedMessageCount()
	}
	ctxMax := provider.ContextGauge(prov, model)
	return chat.FormatStatus(chat.StatusSnapshot{
		Provider: prov, Model: model, CWD: cwd,
		Usage: usage, Subscription: s.subscription,
		ContextUsed: ctxUsed, ContextMax: ctxMax,
		Busy: busy, Queued: queued,
	})
}

// Notify surfaces a bridge event in the bound session's conversation area. The
// bridge's four levels collapse onto the notice wire's two; the text stands
// alone either way.
func (h *chatWsHost) Notify(level, message string) {
	s := h.w.existing(h.session())
	if s == nil {
		return
	}
	wire := "info"
	if level == "warn" || level == "error" {
		wire = "error"
	}
	s.broadcast(ctrlproto.NoticeEvent(wire, "", message))
}

// --- the model-facing sender --------------------------------------------

// chatSender lets the chat_send_* tools drive the bridge without the tools
// package importing chat. Active() is forwarded so a tool call after a
// mid-turn disconnect fails with a model-readable error rather than silently.
type chatSender struct{ bridge *chat.Bridge }

func (c chatSender) SendImage(ctx context.Context, path, caption string) error {
	if c.bridge == nil {
		return fmt.Errorf("chat bridge is not connected")
	}
	return c.bridge.SendImage(ctx, path, caption)
}

func (c chatSender) SendDocument(ctx context.Context, path, caption string) error {
	if c.bridge == nil {
		return fmt.Errorf("chat bridge is not connected")
	}
	return c.bridge.SendDocument(ctx, path, caption)
}

func (c chatSender) Active() bool { return c.bridge != nil && c.bridge.Active() }

// assistantVisibleText concatenates an assistant message's text blocks,
// newline-joined. Tool-use blocks carry no prose and are skipped; a message with
// only tool calls mirrors nothing.
func assistantVisibleText(m provider.Message) string {
	var sb strings.Builder
	for _, blk := range m.Content {
		tb, ok := blk.(provider.TextBlock)
		if !ok {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(tb.Text)
	}
	return sb.String()
}
