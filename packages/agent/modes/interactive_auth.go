package modes

// Credential flows: /login, /logout, and the auth events that move them.
//
// The TUI drives the SAME backend the web panel does: the workspace's
// ctrlproto.AuthController, reached in-process through the carrier with no
// serialization. It asks the daemon to start a login, the daemon describes the
// form, the dialog renders it, and the values go back. Nothing in this file knows
// that openai-compatible needs four fields, that bedrock is not a form, or that a
// subscription code is different from an api key — because none of that is the
// frontend's business, and knowing it here is what let the TUI and the panel drift
// apart in the first place.
//
// The login's progress arrives as auth_state events on the WORKSPACE address,
// which the TUI already subscribes to (runWorkspaceLoop). That is what makes a
// device-code flow work: you approve it in a browser on another device, and the
// daemon tells everyone.

import (
	"sort"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/i18n"
)

// authController returns the carrier's login surface, or nil when this carrier
// does not serve one: a replay session, or a daemon started without
// --web-allow-login. The dialog is not opened in that case.
func (i *Interactive) authController() ctrlproto.AuthController {
	if i.cfg.Carrier == nil {
		return nil
	}
	ac, ok := i.cfg.Carrier.(ctrlproto.AuthController)
	if !ok {
		return nil
	}
	return ac
}

// canLogin reports whether a login can be performed from here at all. Drives
// whether /login and the credential-less boot dialog are offered.
func (i *Interactive) canLogin() bool { return i.authController() != nil }

func (i *Interactive) openLogoutDialog() {
	if !i.canLogin() {
		i.mu.Lock()
		i.statusErr = i18n.T("this session's credentials are owned by the daemon")
		i.mu.Unlock()
		i.invalidate()
		return
	}

	view, err := i.cfg.Carrier.AuthProviders(i.runCtx)
	if err != nil {
		i.mu.Lock()
		i.statusErr = i18n.T("read providers: %s", err.Error())
		i.mu.Unlock()
		i.invalidate()
		return
	}

	// The daemon already decided who is logged in and how — including the
	// openai/openai-codex split, which is one store slot holding two independent
	// logins. Reconstructing that here is what produced two answers that could
	// disagree.
	var items []dialogs.LogoutItem
	var endpoints []dialogs.LogoutItem
	for _, p := range view.Providers {
		// A named endpoint is the operator's own server, and it is listed even
		// with no credential: most local servers want no key, so gating these
		// rows on p.Method would hide exactly the ones a keyless LM Studio or
		// llama.cpp produces — which is every endpoint terva has no other way to
		// remove. Removing one is a different verb from logging out, so they are
		// a separate block rather than mixed into the list above.
		if p.Endpoint {
			endpoints = append(endpoints, dialogs.LogoutItem{
				Label:    p.Label,
				Target:   p.ID,
				Method:   i18n.T("endpoint - remove"),
				Endpoint: true,
			})
			continue
		}
		if p.Method == "" {
			continue
		}
		method := i18n.T("api key")
		if p.Method == "oauth" {
			method = i18n.T("subscription")
		}
		items = append(items, dialogs.LogoutItem{Label: p.Label, Target: p.ID, Method: method})
	}
	sort.Slice(items, func(a, b int) bool { return items[a].Label < items[b].Label })
	sort.Slice(endpoints, func(a, b int) bool { return endpoints[a].Label < endpoints[b].Label })

	if len(items) == 0 && len(endpoints) == 0 {
		i.mu.Lock()
		i.statusOK = i18n.T("no credentials stored; already logged out")
		i.statusErr = ""
		i.mu.Unlock()
		i.invalidate()
		return
	}
	// "all" logs out of every CREDENTIAL. It deliberately does not sweep up the
	// endpoints below it: losing every local backend definition to a keystroke
	// meant for expired tokens is not a recovery anyone wants.
	if len(items) > 1 {
		items = append(items, dialogs.LogoutItem{Label: "all", Target: "all"})
	}
	items = append(items, endpoints...)

	i.logoutDialog.Open(items)
	i.invalidate()
}

// doLogout clears credentials for a provider (or all of them). The daemon owns
// the clearing, the openai split, and the side effects; this reports the result.
func (i *Interactive) doLogout(target string) {
	ac := i.authController()
	if ac == nil {
		i.mu.Lock()
		i.statusErr = i18n.T("this session's credentials are owned by the daemon")
		i.mu.Unlock()
		return
	}
	if err := ac.AuthLogout(i.runCtx, ctrlproto.AuthLogoutParams{Provider: target}); err != nil {
		i.mu.Lock()
		i.statusErr = i18n.T("logout: %s", err.Error())
		i.mu.Unlock()
		return
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	i.statusErr = ""
	if target == "all" || target == i.cfg.Provider {
		// The credential this session's agent was using is gone. Close the prompt
		// gate so nothing goes out until the next login.
		i.setReadyLocked(false)
		i.statusOK = i18n.T("logged out of %s. type /login to sign back in.", target)
		return
	}
	i.statusOK = i18n.T("logged out of %s", target)
}

// doRemoveEndpoint forgets a named openai-compatible endpoint: the definition,
// not just its key. The daemon owns the removal (config entry, stored key, and
// the provider registration); this reports the result.
//
// Reachable only by picking an endpoint row in /logout. There is no
// `/logout <endpoint>` spelling for it on purpose — a logout that silently
// deleted the address of a machine, because the id happened to name an endpoint
// rather than a provider, is not a surprise worth shipping.
func (i *Interactive) doRemoveEndpoint(id string) {
	ac := i.authController()
	if ac == nil {
		i.mu.Lock()
		i.statusErr = i18n.T("this session's credentials are owned by the daemon")
		i.mu.Unlock()
		return
	}
	if err := ac.AuthEndpointRemove(i.runCtx, ctrlproto.AuthEndpointRemoveParams{ID: id}); err != nil {
		i.mu.Lock()
		i.statusErr = i18n.T("remove endpoint: %s", err.Error())
		i.mu.Unlock()
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.statusErr = ""
	if i.cfg.Provider == id {
		// The backend this session has been talking to is the one that just went
		// away. Say so here rather than letting the next turn discover it.
		i.statusOK = i18n.T("removed endpoint %s — this session was using it; pick another with /model", id)
		return
	}
	i.statusOK = i18n.T("removed endpoint %s", id)
}

// startLogin asks the daemon to begin a flow and renders whatever it describes.
//
// Local: true — this TUI's browser IS on the daemon's host (the in-process
// carrier), so the daemon may also run the loopback callback and open a browser,
// which is the nicest OAuth flow there is. An ATTACHED TUI is a different
// machine, and says so, and gets the paste-back form instead. The carrier knows
// which it is; this does not have to.
func (i *Interactive) startLogin(provider, method string) {
	ac := i.authController()
	if ac == nil {
		return
	}
	step, err := ac.AuthLoginStart(i.runCtx, ctrlproto.AuthLoginStartParams{
		Provider: provider,
		Method:   method,
		Local:    i.cfg.CarrierLocal,
	})
	if err != nil {
		i.dialog.ShowResult(false, err.Error())
		return
	}
	i.loginFlow = step.Flow
	i.dialog.ShowStep(step)
	i.invalidate()
}

// submitLogin hands back the values the daemon's form asked for.
//
// Off the main goroutine, because the submit talks to a provider over the network
// — a key is probed, a code is exchanged — and that can take seconds. The returned
// error is deliberately discarded: EVERY outcome, refusal included, also arrives
// as an auth_state event, so there is one result path rather than two that can
// disagree. It is the same path a login completed on another device takes.
func (i *Interactive) submitLogin(values map[string]string) {
	ac := i.authController()
	if ac == nil {
		return
	}
	flow := i.loginFlow
	go func() {
		_ = ac.AuthLoginSubmit(i.runCtx, ctrlproto.AuthLoginSubmitParams{Flow: flow, Values: values})
	}()
}

// cancelLogin abandons the flow. The daemon clears its pkce state, so a code
// pasted afterwards is refused rather than exchanged against a login the user
// walked away from.
func (i *Interactive) cancelLogin() {
	ac := i.authController()
	if ac == nil || i.loginFlow == "" {
		return
	}
	flow := i.loginFlow
	i.loginFlow = ""
	go func() { _ = ac.AuthLoginCancel(i.runCtx, ctrlproto.AuthFlowRef{Flow: flow}) }()
}

// handleAuthState renders a login's progress. It arrives on the workspace
// address, from the daemon, whoever started it — so a device-code login approved
// on a phone lands here too.
func (i *Interactive) handleAuthState(st ctrlproto.AuthState) {
	switch st.Kind {
	case "started":
		// The dialog already has the step (startLogin rendered it synchronously).
		// A "started" for a flow we did not begin is another client's business.
	case "error":
		i.dialog.ShowFlowError(st.Message)
	case "canceled":
		// The dialog is already closed by whoever cancelled.
	case "success":
		if st.Method == "logout" {
			return // doLogout already reported it
		}
		i.loginFlow = ""
		i.finishCarrierLogin(st)
	}
	i.invalidate()
}

// finishCarrierLogin binds the TUI onto the session the fresh credential unlocks.
// The daemon has already stored it, refreshed the workspace defaults, and rebuilt
// every live session's provider client (Workspace.applyCredential) — all this does
// is re-point the frontend.
func (i *Interactive) finishCarrierLogin(st ctrlproto.AuthState) {
	if i.cfg.CarrierLogin == nil {
		// A remote/replay carrier with no session-binding hook. The credential
		// landed on the daemon regardless; there is simply nothing local to rebind.
		i.dialog.ShowResult(true, "")
		return
	}
	info, err := i.cfg.CarrierLogin(i.carrierSession())
	if err != nil {
		i.dialog.ShowResult(false, err.Error())
		return
	}
	if i.carrierSession() != info.ID {
		if err := i.SwitchCarrierSession(info.ID); err != nil {
			i.dialog.ShowResult(false, err.Error())
			return
		}
	} else if !i.ready() {
		// Same session, re-authenticated: reopen the prompt gate.
		i.setReady(true)
	}
	i.mu.Lock()
	if info.Provider != "" {
		i.cfg.Provider = info.Provider
	}
	if info.Model != "" {
		i.cfg.Model = info.Model
	}
	i.statusErr = ""
	i.statusOK = i18n.T("logged in to %s via %s", st.Provider, st.Method)
	i.mu.Unlock()
	i.dialog.ShowResult(true, "")
	// --resume on a credential-less boot: the picker deferred to the login dialog
	// at startup; now that a session exists, arm it. One-shot — a later re-login
	// must not reopen it.
	if i.bootSessionsPending {
		i.bootSessionsPending = false
		i.openSessionsDialog()
	}
}
