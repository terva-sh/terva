package modes

// The chat-connector bridge (/connect): connector lifecycle, message
// mirroring, the chat tools toggle, and the host adapter the bridge
// drives.

import (
	"context"
	"fmt"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// openConnectDialog shows the picker for `/connect` with no arg.
// Items depend on current state: disconnect + status when running,
// connect + status when stopped.
func (i *Interactive) openConnectDialog() {
	items := i.connectMenuItems()
	if len(items) == 0 {
		msg := i.connectorName() + " not configured. run `terva bot setup` first."
		if chat.DefaultServiceName() == "" {
			msg = "no chat connectors compiled into this binary"
		}
		i.mu.Lock()
		i.statusErr = msg
		i.mu.Unlock()
		i.invalidate()
		return
	}
	i.connectDialog.Open(items)
	i.invalidate()
}

// connectorName returns the active chat service's name: the running
// bridge's connector when connected, otherwise the registry default.
func (i *Interactive) connectorName() string {
	if i.chatBridge != nil && i.chatBridge.Active() {
		return i.chatBridge.Connector.Name()
	}
	return chat.DefaultServiceName()
}

// chatBridgeName names the connected bridge for the status bar, ""
// when disconnected.
func (i *Interactive) chatBridgeName() string {
	if i.chatBridge != nil && i.chatBridge.Active() {
		return i.chatBridge.Connector.Name()
	}
	return ""
}

// connectMenuItems builds the dialog entries for the current bridge
// state. Returns empty when the service isn't configured so the
// caller can show a helpful status line instead of an empty menu.
func (i *Interactive) connectMenuItems() []connectItem {
	svc, ok := chat.Lookup(chat.DefaultServiceName())
	if !ok || !svc.Configured(i.cfg.TervaHome) {
		return nil
	}
	var items []connectItem
	if i.chatBridge != nil && i.chatBridge.Active() {
		items = append(items, connectItem{label: "disconnect", action: "disconnect", hint: "stop mirroring"})
		st := i.chatBridge.State()
		hint := "active"
		if st.Username != "" {
			hint += " as @" + st.Username
		}
		items = append(items, connectItem{label: "status", action: "status", hint: hint})
	} else {
		hint := "start mirroring dms into this session"
		if _, pairing, err := svc.NewConnector(i.cfg.TervaHome, nil); err == nil && pairing.AllowedUserID == "" {
			hint = "awaiting pairing (send /start to the bot once connected)"
		}
		items = append(items, connectItem{label: "connect", action: "connect", hint: hint})
		items = append(items, connectItem{label: "status", action: "status", hint: "disconnected"})
	}
	return items
}

// doConnector dispatches one of the three explicit actions. Called
// from /connect <action> or after the picker selects a row.
func (i *Interactive) doConnector(action string) {
	switch action {
	case "connect":
		i.connectorConnect()
	case "disconnect":
		i.connectorDisconnect()
	case "status":
		i.connectorStatus()
	default:
		i.mu.Lock()
		i.statusErr = "unknown /connect action: " + action + " (use connect, disconnect, or status)"
		i.mu.Unlock()
		i.invalidate()
	}
}

// connectorConnect starts the bridge for the default chat service.
// Refuses if it's already running or the service isn't configured.
func (i *Interactive) connectorConnect() {
	if i.chatBridge != nil && i.chatBridge.Active() {
		i.mu.Lock()
		i.statusOK = i.connectorName() + " already connected"
		i.statusErr = ""
		i.mu.Unlock()
		i.invalidate()
		return
	}
	svc, ok := chat.Lookup(chat.DefaultServiceName())
	if !ok {
		i.mu.Lock()
		i.statusErr = "no chat connectors compiled in"
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if !svc.Configured(i.cfg.TervaHome) {
		i.mu.Lock()
		i.statusErr = svc.Name + ": not configured. run `terva bot setup` first."
		i.mu.Unlock()
		i.invalidate()
		return
	}
	// Refuse to start when a background daemon is already polling
	// the same service. Two concurrent consumers race each update
	// and one always loses, so messages get half-delivered. The user
	// can `terva bot stop` first, then /connect.
	if pid, alive, _ := chat.IsRunning(i.cfg.TervaHome, svc.Name); alive && pid > 0 {
		i.mu.Lock()
		i.statusErr = fmt.Sprintf("%s: bot daemon already running (pid %d). stop it with `terva bot stop` first.", svc.Name, pid)
		i.mu.Unlock()
		i.invalidate()
		return
	}
	host := &chatHost{iv: i}
	conn, pairing, err := svc.NewConnector(i.cfg.TervaHome, func(msg string) { host.Notify("warn", msg) })
	if err != nil {
		i.mu.Lock()
		i.statusErr = svc.Name + ": " + err.Error()
		i.mu.Unlock()
		i.invalidate()
		return
	}
	i.chatBridge = &chat.Bridge{Connector: conn, Host: host, Pairing: pairing}
	if err := i.chatBridge.Start(i.runCtx); err != nil {
		i.chatBridge = nil
		i.mu.Lock()
		i.statusErr = svc.Name + " connect failed: " + err.Error()
		i.mu.Unlock()
		i.invalidate()
		return
	}
	i.applyChatTools(true)
	state := i.chatBridge.State()
	label := svc.Name + " connected"
	if svc.Dev {
		label = svc.Name + " (dev) connected"
	}
	if state.Username != "" {
		label += " as @" + state.Username
	}
	if state.PairedID == "" {
		label += " — send /start to the bot from your phone to claim it"
	}
	i.mu.Lock()
	i.statusOK = label
	i.statusErr = ""
	i.mu.Unlock()
	i.invalidate()
}

// connectorDisconnect stops the bridge. No-op when already stopped.
func (i *Interactive) connectorDisconnect() {
	name := i.connectorName()
	if i.chatBridge == nil || !i.chatBridge.Active() {
		i.mu.Lock()
		i.statusOK = name + " already disconnected"
		i.statusErr = ""
		i.mu.Unlock()
		i.invalidate()
		return
	}
	i.chatBridge.Stop()
	i.applyChatTools(false)
	i.mu.Lock()
	i.statusOK = name + " disconnected"
	i.statusErr = ""
	i.mu.Unlock()
	i.invalidate()
}

// chatSenderAdapter wraps the bridge so the tools package can drive
// it without importing chat directly. The Active() check is forwarded
// to the bridge so the tool can fail clearly with a model-readable
// error when the user disconnected mid-turn.
type chatSenderAdapter struct {
	bridge *chat.Bridge
}

func (a chatSenderAdapter) SendImage(ctx context.Context, path, caption string) error {
	if a.bridge == nil {
		return fmt.Errorf("chat bridge is not connected")
	}
	return a.bridge.SendImage(ctx, path, caption)
}

func (a chatSenderAdapter) SendDocument(ctx context.Context, path, caption string) error {
	if a.bridge == nil {
		return fmt.Errorf("chat bridge is not connected")
	}
	return a.bridge.SendDocument(ctx, path, caption)
}

func (a chatSenderAdapter) Active() bool {
	return a.bridge != nil && a.bridge.Active()
}

// applyChatTools registers (active=true) or removes (active=false)
// the chat_send_image and chat_send_file tools on the running agent
// so the model only sees them while a bridge is connected. Snapshots
// and mutates the live tool registry so any extension or /reload-ext
// additions made while connected survive a later disconnect (we only
// add or strip the two chat entries, never the rest).
func (i *Interactive) applyChatTools(active bool) {
	ag := i.turns.Agent()
	if ag == nil {
		return
	}
	current := ag.Tools
	next := core.Registry{}
	for name, t := range current {
		if name == "chat_send_image" || name == "chat_send_file" {
			continue
		}
		next[name] = t
	}
	if active && i.chatBridge != nil {
		caps := i.chatBridge.Connector.Capabilities()
		sender := chatSenderAdapter{bridge: i.chatBridge}
		if caps.SendsImages {
			next["chat_send_image"] = &tools.ChatSendImageTool{
				CWD: i.cfg.CWD, Sandbox: i.cfg.Sandbox, Sender: sender,
			}
		}
		if caps.SendsFiles {
			next["chat_send_file"] = &tools.ChatSendFileTool{
				CWD: i.cfg.CWD, Sandbox: i.cfg.Sandbox, Sender: sender,
			}
		}
	}
	ag.SetTools(next)
}

// connectorStatus writes a one-liner describing the bridge state.
// Reports on both the in-tui bridge and the background daemon so
// the user isn't confused when the daemon owns the poll loop.
func (i *Interactive) connectorStatus() {
	name := chat.DefaultServiceName()
	if name == "" && (i.chatBridge == nil || !i.chatBridge.Active()) {
		i.mu.Lock()
		i.statusOK = "no chat connectors compiled into this binary"
		i.statusErr = ""
		i.mu.Unlock()
		i.invalidate()
		return
	}
	// Dev connectors (--connector-manifest) are tagged so they are
	// never mistaken for installed ones.
	label := name
	if svc, ok := chat.Lookup(name); ok && svc.Dev {
		label = name + " (dev)"
	}
	var msg string
	if i.chatBridge != nil && i.chatBridge.Active() {
		s := i.chatBridge.State()
		name = i.chatBridge.Connector.Name()
		label = name
		if svc, ok := chat.Lookup(name); ok && svc.Dev {
			label = name + " (dev)"
		}
		msg = label + ": connected (tui bridge)"
		if s.Username != "" {
			msg += " as @" + s.Username
		}
		if s.PairedID != "" {
			msg += " - paired with user " + s.PairedID
		} else {
			msg += " - awaiting pairing"
		}
	} else if pid, alive, _ := chat.IsRunning(i.cfg.TervaHome, name); alive && pid > 0 {
		msg = fmt.Sprintf("%s: background daemon running (pid %d) - /connect won't work until you stop it", label, pid)
	} else if svc, ok := chat.Lookup(name); !ok || !svc.Configured(i.cfg.TervaHome) {
		msg = label + ": not configured. run `terva bot setup` first."
	} else {
		msg = label + ": disconnected (ready to connect)"
	}
	i.mu.Lock()
	i.statusOK = msg
	i.statusErr = ""
	i.mu.Unlock()
	i.invalidate()
}

// chatHost adapts *Interactive to chat.Host so the bridge can call
// back into the TUI without importing modes directly.
type chatHost struct{ iv *Interactive }

func (h *chatHost) SubmitOrQueue(prompt string, images []provider.ImageBlock) {
	h.iv.SubmitOrQueue(prompt, images)
}

func (h *chatHost) CancelTurn() { h.iv.CancelTurn() }

func (h *chatHost) Status() string {
	h.iv.mu.Lock()
	providerName := h.iv.cfg.Provider
	model := h.iv.cfg.Model
	cwd := h.iv.cfg.CWD
	usage := h.iv.cumUsage
	subscription := h.iv.cfg.AuthMethod == "oauth"
	ctxUsed := h.iv.lastCtxInput
	h.iv.mu.Unlock()
	busy := h.iv.turns.Busy()
	queued := h.iv.turns.QueuedCount()

	ctxMax := 0
	if m, err := provider.FindModel(providerName, model); err == nil {
		ctxMax = m.ContextWindow
	}
	return chat.FormatStatus(chat.StatusSnapshot{
		Provider:     providerName,
		Model:        model,
		CWD:          cwd,
		Usage:        usage,
		Subscription: subscription,
		ContextUsed:  ctxUsed,
		ContextMax:   ctxMax,
		Busy:         busy,
		Queued:       queued,
	})
}

func (h *chatHost) Notify(level, message string) {
	h.iv.mu.Lock()
	switch level {
	case "error", "warn":
		h.iv.statusErr = message
		h.iv.statusOK = ""
	default:
		h.iv.statusOK = message
		h.iv.statusErr = ""
	}
	h.iv.mu.Unlock()
	h.iv.invalidate()
}
