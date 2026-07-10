package modes

// The `/connect` client.
//
// The bridge itself lives in the workspace now (workspace_chat.go). The TUI
// keeps a picker, a status-bar segment, and a cached copy of the daemon's chat
// surface — the same mirror shape the usage gauge uses: seed on bind, refresh on
// surface_updated, render from the cache.
//
// What used to live here: a *chat.Bridge, a chat.Host implementation that fed
// prompts through Interactive.SubmitOrQueue (and therefore through the shell
// escape), a chatSenderAdapter, and applyChatTools — which reached into
// turns.Agent() to patch the live tool registry. The tools are derived daemon-
// side now, so there is nothing left to patch, and no agent to reach for.

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/i18n"
)

// --- the chat-surface mirror --------------------------------------------

// seedCarrierChat fetches the chat pane once per session binding, so a bridge
// the daemon already had running (it survives a TUI detach now) shows up in the
// status bar on the first paint rather than after the next event.
func (i *Interactive) seedCarrierChat() {
	i.mu.Lock()
	armed := i.carrierChatArmed
	i.carrierChatArmed = false
	i.mu.Unlock()
	if armed {
		go i.fetchCarrierChat()
	}
}

// fetchCarrierChat refreshes the cached chat view from the daemon.
func (i *Interactive) fetchCarrierChat() {
	c := i.cfg.Carrier
	if c == nil {
		return
	}
	sf, err := c.Surface(context.Background(), i.carrierSession(), "chat")
	if err != nil || sf.Chat == nil {
		return
	}
	i.mu.Lock()
	i.carrierChat = *sf.Chat
	i.mu.Unlock()
	i.invalidate()
}

// carrierChatView returns the cached chat pane.
func (i *Interactive) carrierChatView() ctrlproto.ChatView {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.carrierChat
}

// chatBridgeName names the connected bridge for the status bar, "" when
// disconnected — or when the bridge is bound to a DIFFERENT session. The mirror
// no longer follows whichever session this TUI is showing, so a status bar that
// claimed otherwise would lie.
func (i *Interactive) chatBridgeName() string {
	v := i.carrierChatView()
	if v.Bridge.State != "connected" {
		return ""
	}
	if v.Bridge.Session != "" && v.Bridge.Session != i.carrierSession() {
		return ""
	}
	return v.Bridge.Connector
}

// --- the picker ---------------------------------------------------------

// openConnectDialog shows the picker for `/connect` with no arg. Items depend on
// the daemon's current bridge state, so the view is fetched fresh: a connect
// from another client — or a bridge that outlived a previous TUI — must show.
func (i *Interactive) openConnectDialog() {
	i.fetchCarrierChat()
	v := i.carrierChatView()
	items := connectMenuItems(v, i.carrierSession())
	if len(items) == 0 {
		msg := i18n.T("no chat connectors compiled into this binary")
		if len(v.Services) > 0 {
			msg = i18n.T("no chat connector is configured. run `terva bot setup` first.")
		}
		i.setStatusErr(msg)
		i.invalidate()
		return
	}
	i.connectDialog.Open(items)
	i.invalidate()
}

// connectMenuItems builds the picker rows from the daemon's chat view: actions
// on the live bridge when connected, otherwise one row per configured service.
// A pure function of the view — the TUI holds no bridge state to consult.
func connectMenuItems(v ctrlproto.ChatView, sess string) []dialogs.ConnectItem {
	var items []dialogs.ConnectItem
	if v.Bridge.State == "connected" {
		hint := i18n.T("active")
		if v.Bridge.Username != "" {
			hint += " as @" + v.Bridge.Username
		}
		items = append(items,
			dialogs.ConnectItem{Label: "disconnect", Action: "disconnect", Hint: i18n.T("stop mirroring")},
			dialogs.ConnectItem{Label: "status", Action: "status", Hint: hint},
		)
		// The mirror is pinned to the session it was connected from. Offer to
		// move it here rather than silently retargeting on a session switch.
		if v.Bridge.Session != "" && v.Bridge.Session != sess {
			items = append(items, dialogs.ConnectItem{
				Label: "rebind", Action: "rebind",
				Hint: i18n.T("mirror this session instead"),
			})
		}
		return items
	}
	for _, svc := range v.Services {
		if !svc.Configured {
			continue
		}
		hint := i18n.T("start mirroring dms into this session")
		if !svc.Paired {
			hint = i18n.T("awaiting pairing (send /start to the bot once connected)")
		}
		if tag := chatServiceTag(svc); tag != "" {
			hint = tag + " — " + hint
		}
		items = append(items, dialogs.ConnectItem{
			Label: "connect " + svc.Name, Action: "connect " + svc.Name, Hint: hint,
		})
	}
	if len(items) > 0 {
		items = append(items, dialogs.ConnectItem{Label: "status", Action: "status", Hint: i18n.T("disconnected")})
	}
	return items
}

// chatServiceTag renders a service's provenance: "extension", "dev", or both.
func chatServiceTag(svc ctrlproto.ChatServiceInfo) string {
	var tags []string
	if svc.Kind != "" {
		tags = append(tags, svc.Kind)
	}
	if svc.Dev {
		tags = append(tags, "dev")
	}
	return strings.Join(tags, ", ")
}

func shortSession(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// --- actions ------------------------------------------------------------

// doConnector dispatches one `/connect` action onto the daemon's chat surface:
// "connect [name]", a bare service name, "disconnect", "rebind", or "status".
// Every one of these is a surface action — the TUI adds no ctrlproto verb and
// owns no bridge.
func (i *Interactive) doConnector(action string) {
	fields := strings.Fields(action)
	if len(fields) == 0 {
		i.openConnectDialog()
		return
	}
	switch fields[0] {
	case "connect":
		name := ""
		if len(fields) > 1 {
			name = fields[1]
		}
		i.chatSurfaceAction("connect", map[string]string{"name": name})
	case "disconnect":
		i.chatSurfaceAction("disconnect", nil)
	case "rebind":
		i.chatSurfaceAction("rebind", map[string]string{"session": i.carrierSession()})
	case "status":
		i.connectorStatus()
	default:
		// A bare service name is an implicit connect. Resolve it against the
		// daemon's list, fetched now: the TUI has no chat registry of its own,
		// and a stale mirror would reject a service that exists.
		i.fetchCarrierChat()
		if i.knownChatService(fields[0]) {
			i.chatSurfaceAction("connect", map[string]string{"name": fields[0]})
			return
		}
		i.setStatusErr(i18n.T("unknown /connect action: %s (use connect [name], disconnect, rebind, status, or a connector name)", action))
		i.invalidate()
	}
}

// chatSurfaceAction sends one action and refreshes the mirror. Connect is async
// daemon-side: it returns once the dial is armed, and the outcome arrives as a
// notice plus a surface_updated. A nil error here means "accepted", not
// "connected" — the status line is fed by those events, not by this call.
func (i *Interactive) chatSurfaceAction(action string, args map[string]string) {
	c := i.cfg.Carrier
	if c == nil {
		return
	}
	if err := c.SurfaceAction(context.Background(), i.carrierSession(), "chat", action, args); err != nil {
		i.setStatusErr(err.Error())
		i.invalidate()
		return
	}
	go i.fetchCarrierChat()
}

func (i *Interactive) knownChatService(name string) bool {
	for _, svc := range i.carrierChatView().Services {
		if svc.Name == name {
			return true
		}
	}
	return false
}

// connectorStatus writes a one-liner describing the bridge, rendered from the
// mirror. It reports the daemon's view — including a `terva bot` daemon holding
// the poll loop, which is why /connect would be refused.
func (i *Interactive) connectorStatus() {
	i.fetchCarrierChat()
	v := i.carrierChatView()

	if len(v.Services) == 0 {
		i.setStatusOK(i18n.T("no chat connectors compiled into this binary"))
		i.invalidate()
		return
	}

	var msg string
	switch v.Bridge.State {
	case "connected":
		msg = v.Bridge.Connector + ": " + i18n.T("connected")
		if v.Bridge.Username != "" {
			msg += " as @" + v.Bridge.Username
		}
		if v.Bridge.PairedID != "" {
			msg += " - " + i18n.T("paired with user %s", v.Bridge.PairedID)
		} else {
			msg += " - " + i18n.T("awaiting pairing")
		}
		if v.Bridge.Session != "" && v.Bridge.Session != i.carrierSession() {
			msg += " - " + i18n.T("mirroring session %s", shortSession(v.Bridge.Session))
		}
	case "connecting":
		msg = v.Bridge.Connector + ": " + i18n.T("connecting…")
	case "error":
		i.setStatusErr(v.Bridge.Connector + ": " + v.Bridge.Error)
		i.invalidate()
		return
	default:
		if v.DaemonPID > 0 {
			msg = i18n.T("background bot daemon running (pid %d) - /connect won't work until you stop it", v.DaemonPID)
			break
		}
		if !anyConfigured(v) {
			i.setStatusErr(i18n.T("not configured. run `terva bot setup` first."))
			i.invalidate()
			return
		}
		msg = i18n.T("disconnected (ready to connect)")
		if len(v.Services) > 1 {
			msg += " - " + i18n.T("connectors: %s", chatServiceNames(v))
		}
	}
	i.setStatusOK(msg)
	i.invalidate()
}

func anyConfigured(v ctrlproto.ChatView) bool {
	for _, svc := range v.Services {
		if svc.Configured {
			return true
		}
	}
	return false
}

// chatServiceNames lists every registered service with its provenance tag.
func chatServiceNames(v ctrlproto.ChatView) string {
	var names []string
	for _, svc := range v.Services {
		name := svc.Name
		if tag := chatServiceTag(svc); tag != "" {
			name += " (" + tag + ")"
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}
