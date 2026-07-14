package workspace

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider/auth"
)

// The auth group: logging a MODEL PROVIDER in and out over the wire.
//
// Only the flows that can actually complete from somewhere else are offered.
// The daemon's loopback is not the browser's loopback: the api-key browser form
// binds 127.0.0.1 on THIS machine, and both OAuth callback servers bind a fixed
// port on THIS machine, so a phone pointed at a VM can never reach any of them.
// That is a property of the providers' registered redirect URIs, not a gap in
// terva, and there is no workaround worth having. What survives the wire is the
// paste-the-key flow, the paste-the-code flow, and the device-code flows — and
// that is what this serves.

// The in-process carrier serves the auth group. The TUI reaches it through this
// assertion's interface, with no serialization; `terva web` reaches the same
// methods over the wire. One backend.
var _ ctrlproto.AuthController = (*Workspace)(nil)

// authFlow is one login attempt in progress, remembered so a submit can be
// matched to the flow that produced its form.
type authFlow struct {
	provider string
	method   string      // apikey | oauth
	mgr      auth.FlowID // the manager's handle, for flows that hold pkce state
}

type wsAuth struct {
	mu    sync.Mutex
	mgr   *auth.Manager
	flows map[string]*authFlow
	seq   uint64

	// applyLogin is the host's "a credential landed" hook: register a custom
	// endpoint's model, promote it to the default, re-discover a provider's live
	// model list. It lives above this package (agent.ApplyLoginSuccess) because it
	// reaches for the model catalog and the user config, which the workspace does
	// not own. Without it a login succeeds and nothing the user configured appears.
	applyLogin func(providerID string)
	// applyStart / applyLogout toggle whether terva may borrow the Kimi CLI's
	// token. Same reason, same direction.
	applyStart  func(providerID string) error
	applyLogout func(providerID string) error
}

// AuthOptions configures the auth group.
type AuthOptions struct {
	// OpenBrowser lets a started flow open its URL in a browser on THIS machine.
	//
	// True for the in-process TUI, whose user is sitting at this host and expects
	// a browser to pop. FALSE for a daemon: `terva web` serves a browser that is
	// somewhere else entirely, so an xdg-open here is at best a no-op on a
	// headless box and at worst a window on a machine nobody is watching.
	OpenBrowser bool

	// ApplyStart / ApplyLogin / ApplyLogout are the host's side of a credential
	// change — what a login MEANS beyond a secret landing on disk. They live above
	// this package (agent.ApplyLoginSuccess and friends) because they reach for the
	// model catalog and the user config, which the workspace does not own.
	ApplyStart  func(providerID string) error
	ApplyLogin  func(providerID string)
	ApplyLogout func(providerID string) error
}

// EnableAuth turns on the auth group: the Workspace becomes a
// ctrlproto.AuthController and can establish, repair, and revoke a model-provider
// credential.
//
// Called by BOTH composition roots — `terva web --web-allow-login` and the
// in-process TUI — because they are the same feature. The TUI reaches it through
// the in-process carrier with no serialization; the panel reaches it over the
// wire. One backend, one form descriptor, one event stream.
//
// A workspace without it answers every auth verb with CodeUnsupported and reports
// CanLogin false, so a client renders no controls rather than controls that fail.
func (w *Workspace) EnableAuth(m *auth.Manager, opts AuthOptions) {
	if m == nil {
		return
	}
	m.SetOpenBrowser(opts.OpenBrowser)
	applyStart, applyLogin, applyLogout := opts.ApplyStart, opts.ApplyLogin, opts.ApplyLogout
	w.auth.mu.Lock()
	w.auth.mgr = m
	w.auth.flows = map[string]*authFlow{}
	w.auth.applyStart, w.auth.applyLogin, w.auth.applyLogout = applyStart, applyLogin, applyLogout
	w.auth.mu.Unlock()
	go w.pumpAuthEvents(m)
}

// pumpAuthEvents relays the manager's flow events onto the workspace address.
//
// One long-lived pump, and it never blocks on a client: Manager.emit drops on a
// full channel, and the frame it would drop is the "success" that tells a phone
// its login worked.
func (w *Workspace) pumpAuthEvents(m *auth.Manager) {
	for {
		select {
		case <-w.ctx.Done():
			return
		case ev, ok := <-m.Events():
			if !ok {
				return
			}
			if ev.Kind == "browser_open" {
				continue // describes a browser on the daemon's host; we turned that off
			}
			if ev.Kind == "success" {
				w.applyCredential(ev.Provider)
			}
			w.BroadcastAll(ctrlproto.AuthStateEvent(ctrlproto.AuthState{
				Kind:     ev.Kind,
				Flow:     string(ev.Flow),
				Provider: ev.Provider,
				Method:   ev.Method,
				URL:      ev.URL,
				UserCode: ev.UserCode,
				Message:  ev.Message,
			}))
			// The pane's contents changed, whatever the outcome.
			w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("providers"))
		}
	}
}

// applyCredential makes a freshly-stored credential usable without a restart:
// the host-side model wiring, then every live session's provider client.
//
// A login that stores a secret and leaves the running agents pointed at the old
// (or absent) credential has not really logged anyone in — the next turn fails
// exactly as it did before, and the user is told it worked.
func (w *Workspace) applyCredential(providerID string) {
	w.auth.mu.Lock()
	apply := w.auth.applyLogin
	w.auth.mu.Unlock()
	if apply != nil {
		apply(providerID)
	}
	if err := w.RefreshDefaults(); err != nil {
		w.diag("terva: refresh defaults after login: " + err.Error())
	}
	w.mu.Lock()
	ids := make([]string, 0, len(w.sessions))
	for id := range w.sessions {
		ids = append(ids, id)
	}
	w.mu.Unlock()
	for _, id := range ids {
		if err := w.RefreshSessionCredential(w.ctx, id); err != nil {
			w.diag("terva: refresh session credential: " + err.Error())
		}
	}
}

func (w *Workspace) authManager() *auth.Manager {
	w.auth.mu.Lock()
	defer w.auth.mu.Unlock()
	return w.auth.mgr
}

// canLogin reports whether the auth group is served here.
func (w *Workspace) canLogin() bool { return w.authManager() != nil }

func (w *Workspace) newFlow(f *authFlow) string {
	w.auth.mu.Lock()
	defer w.auth.mu.Unlock()
	w.auth.seq++
	id := "f" + strconv.FormatUint(w.auth.seq, 10)
	w.auth.flows[id] = f
	return id
}

func (w *Workspace) lookupFlow(id string) *authFlow {
	w.auth.mu.Lock()
	defer w.auth.mu.Unlock()
	return w.auth.flows[id]
}

func (w *Workspace) dropFlow(id string) {
	w.auth.mu.Lock()
	defer w.auth.mu.Unlock()
	delete(w.auth.flows, id)
}

// --- ctrlproto.AuthController ---

// AuthLoginStart begins a login and describes the form to put in front of the
// user. The daemon picks the concrete flow: the client asks for "oauth", and
// whether that means a device code or a paste-the-code page depends on the
// provider, not on a preference.
func (w *Workspace) AuthLoginStart(_ context.Context, p ctrlproto.AuthLoginStartParams) (ctrlproto.AuthFlowStep, error) {
	m := w.authManager()
	if m == nil {
		return ctrlproto.AuthFlowStep{}, ctrlproto.Errorf(ctrlproto.CodeUnsupported, "this daemon does not serve provider logins")
	}
	provider := strings.TrimSpace(p.Provider)
	if provider == "" {
		return ctrlproto.AuthFlowStep{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "no provider named")
	}

	// The env-var providers are not a form at all: there is no key for terva to
	// take, so offering a paste box would be a dead end that looks like a way in.
	if env, ok := auth.EnvProviderInfo(provider); ok {
		return ctrlproto.AuthFlowStep{
			Kind:  "info",
			Title: env.Title,
			Lines: env.Lines,
		}, nil
	}

	w.auth.mu.Lock()
	applyStart := w.auth.applyStart
	w.auth.mu.Unlock()
	if applyStart != nil {
		_ = applyStart(provider)
	}

	switch p.Method {
	case "apikey":
		return w.startAPIKeyForm(m, provider, p.Local), nil
	case "oauth":
		return w.startOAuth(m, provider, p.Local)
	default:
		return ctrlproto.AuthFlowStep{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "method must be apikey or oauth")
	}
}

// startAPIKeyForm describes the fields a provider's key needs.
//
// The key is collected as a form and handed straight to CompleteAPIKey. The
// manager's StartAPIKey ALSO spins a browser form on the daemon's loopback, which
// is useless to a remote client and pleasant for a local one — so it runs only
// when the caller said its browser is here, and even then the form below is what
// actually completes the login. The browser page is a convenience, never the
// contract.
func (w *Workspace) startAPIKeyForm(m *auth.Manager, provider string, local bool) ctrlproto.AuthFlowStep {
	flow := w.newFlow(&authFlow{provider: provider, method: "apikey"})

	var browserURL string
	if local {
		if f, err := m.StartAPIKey(provider); err == nil {
			browserURL = f.URL
		}
	}

	if provider == "openai-compatible" {
		return ctrlproto.AuthFlowStep{
			Flow:  flow,
			Kind:  "form",
			Title: i18n.T("Connect an OpenAI-compatible endpoint"),
			Lines: []string{
				i18n.T("Point terva at any OpenAI-compatible server: LM Studio, vLLM, llama.cpp, Ollama's /v1, or a gateway."),
				i18n.T("Name it to keep several servers at once — each named endpoint is its own provider and lists its own models. Leave the name empty and this replaces the single shared endpoint."),
			},
			URL: browserURL,
			Fields: []ctrlproto.AuthField{
				{Name: "name", Label: i18n.T("Name"), Type: "text", Placeholder: "workshop-3090",
					Help: i18n.T("Optional. Naming it keeps your other endpoints; leaving it empty overwrites the shared one.")},
				{Name: "base_url", Label: i18n.T("Base URL"), Type: "text", Required: true, Placeholder: "http://localhost:1234/v1"},
				// Required for the shared slot, pointless for a named endpoint: that one
				// discovers its own models, so there is nothing to type. RequiredUnless
				// lets the form say which of the two you are filling in.
				{Name: "model", Label: i18n.T("Default model"), Type: "text", Required: true, RequiredUnless: "name", Placeholder: "qwen2.5-coder",
					Help: i18n.T("Not needed for a named endpoint — it discovers the models the server serves.")},
				{Name: "api_key", Label: i18n.T("API key"), Type: "secret", Help: i18n.T("Optional — many local servers ignore it.")},
				{Name: "context_window", Label: i18n.T("Default context window"), Type: "integer", Placeholder: "32768"},
			},
		}
	}
	return ctrlproto.AuthFlowStep{
		Flow:  flow,
		Kind:  "form",
		Title: i18n.T("Sign in with an API key"),
		URL:   browserURL,
		Fields: []ctrlproto.AuthField{
			{Name: "api_key", Label: i18n.T("API key"), Type: "secret", Required: true},
		},
	}
}

// startOAuth begins an OAuth login, in whichever shapes can actually complete for
// this caller.
//
// The paste-back flow (StartManualOAuth) is ALWAYS offered: it mints an authorize
// URL with no local server, and the user hands the result back. It is the only
// shape that works from a phone, and it is what the TUI has always fallen back to
// on a headless host.
//
// The loopback flow (StartOAuth) binds a callback server on the DAEMON and waits
// for a redirect. A remote browser can never deliver that — so it runs only for a
// caller that said its browser is on this host, where it is the nicest flow there
// is: click, approve, done, nothing to paste. Both run together in that case,
// exactly as the in-process TUI has always done: StartManualOAuth adopts the
// loopback flow's pkce/state when the two share a redirect URI, so these are one
// authorization, not two.
func (w *Workspace) startOAuth(m *auth.Manager, provider string, local bool) (ctrlproto.AuthFlowStep, error) {
	if local {
		// Best-effort: a busy callback port (another terva, a claude-code login)
		// must not sink the login, because the paste-back flow below still works.
		if _, err := m.StartOAuth(provider); err != nil {
			w.diag("terva: loopback oauth unavailable, paste-back only: " + err.Error())
		}
	}
	f, err := m.StartManualOAuth(provider)
	if err != nil {
		return ctrlproto.AuthFlowStep{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%v", err)
	}
	flow := w.newFlow(&authFlow{provider: provider, method: "oauth", mgr: f.ID})

	// The device-code providers hand back a URL and a code and then poll. There
	// is nothing for the user to give back, so there is nothing to submit: the
	// flow completes on its own and says so with an auth_state event.
	if f.UserCode != "" {
		return ctrlproto.AuthFlowStep{
			Flow:     flow,
			Kind:     "display",
			Title:    i18n.T("Approve terva in your browser"),
			Lines:    []string{i18n.T("Open this page and enter the code. This panel updates by itself when you are done.")},
			URL:      f.URL,
			UserCode: f.UserCode,
		}, nil
	}

	// Everyone else: authorize in a browser — any browser, anywhere — and paste
	// what comes back. parseManualCodeInput already takes a bare code, a
	// "code#state" pair, or the whole redirect URL out of the address bar, so the
	// user can paste whichever of those they are looking at.
	return ctrlproto.AuthFlowStep{
		Flow:  flow,
		Kind:  "form",
		Title: i18n.T("Authorize terva, then paste the code"),
		Lines: []string{i18n.T("Open this page, approve the request, and paste back the code it gives you (or the whole URL from the address bar).")},
		URL:   f.URL,
		Fields: []ctrlproto.AuthField{
			{Name: "code", Label: i18n.T("Authorization code"), Type: "secret", Required: true},
		},
	}, nil
}

// AuthLoginSubmit completes a form step.
func (w *Workspace) AuthLoginSubmit(ctx context.Context, p ctrlproto.AuthLoginSubmitParams) error {
	m := w.authManager()
	if m == nil {
		return ctrlproto.Errorf(ctrlproto.CodeUnsupported, "this daemon does not serve provider logins")
	}
	fl := w.lookupFlow(p.Flow)
	if fl == nil {
		// Either it never existed, or a newer login replaced it. Both mean the same
		// thing to the user, and neither may be silently exchanged: the pkce
		// verifier this code was minted against is gone.
		//
		// Announced as well as returned. Every other outcome of a login reaches a
		// client as an auth_state event — including the ones that complete on
		// another device — so a client can have ONE result path instead of two that
		// might disagree. A refusal that only came back as a method error would be
		// the exception that forces the second path.
		return w.failFlow(p.Flow, "", ctrlproto.Errorf(ctrlproto.CodeBusy,
			"this login is no longer in progress; start it again"))
	}
	val := func(k string) string { return strings.TrimSpace(p.Values[k]) }

	switch {
	case fl.method == "oauth":
		if err := m.CompleteManualOAuth(ctx, fl.mgr, val("code")); err != nil {
			// The manager emits its own auth_state for a provider refusal; only the
			// flow-bookkeeping refusals (superseded, gone) need announcing here.
			if errors.Is(err, auth.ErrFlowSuperseded) || errors.Is(err, auth.ErrNoFlow) {
				return w.failFlow(p.Flow, fl.provider, authErr(err))
			}
			return authErr(err)
		}
	case fl.provider == "openai-compatible":
		// The one field that is not a string. It is parsed HERE, at the single
		// authority, rather than trusted from the wire.
		win := 0
		if s := val("context_window"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 {
				return w.failFlow(p.Flow, fl.provider, ctrlproto.Errorf(
					ctrlproto.CodeBadRequest, "context window must be a whole number"))
			}
			win = n
		}
		if name := val("name"); name != "" {
			// A named endpoint is a provider DEFINITION, not a credential, so it does
			// not go where a login goes. It is written to config.json (the operator's
			// own layer, never a project's — a checked-in project config must not be
			// able to point the agent at someone else's server), and its key, if the
			// server even wants one, lands in auth.json under this same id, which is
			// exactly where ResolveCredential already looks for it.
			if err := w.saveEndpoint(ctx, name, val("base_url"), val("api_key"), win); err != nil {
				return w.failFlow(p.Flow, fl.provider, err)
			}
			break
		}
		if val("model") == "" {
			// The shared slot has no discovery to fall back on, so it genuinely needs
			// a model. The form says so too (RequiredUnless), but the form is an
			// affordance and this is the authority.
			return w.failFlow(p.Flow, fl.provider, ctrlproto.Errorf(ctrlproto.CodeBadRequest,
				"a default model is required, or name this endpoint and terva will discover its models"))
		}
		if err := m.CompleteCompatAPIKey(ctx, val("base_url"), val("model"), val("api_key"), win); err != nil {
			return authErr(err)
		}
	default:
		if err := m.CompleteAPIKey(ctx, fl.provider, val("api_key")); err != nil {
			return authErr(err)
		}
	}
	w.dropFlow(p.Flow)
	return nil
}

// AuthLoginCancel abandons a flow. Cancelling clears the manager's pkce state, so
// a code pasted afterwards is refused rather than exchanged.
func (w *Workspace) AuthLoginCancel(_ context.Context, p ctrlproto.AuthFlowRef) error {
	m := w.authManager()
	if m == nil {
		return ctrlproto.Errorf(ctrlproto.CodeUnsupported, "this daemon does not serve provider logins")
	}
	m.CancelOAuth()
	w.dropFlow(p.Flow)
	return nil
}

// AuthLogout clears a stored credential and stops the running sessions using it.
func (w *Workspace) AuthLogout(_ context.Context, p ctrlproto.AuthLogoutParams) error {
	m := w.authManager()
	if m == nil {
		return ctrlproto.Errorf(ctrlproto.CodeUnsupported, "this daemon does not serve provider logins")
	}
	store := m.Store()
	if store == nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "no credential store")
	}

	targets := []string{p.Provider}
	if p.Provider == "" || p.Provider == "all" {
		targets = targets[:0]
		creds, _ := store.Load()
		for id, st := range auth.Describe(creds) {
			if st.Method != "" {
				targets = append(targets, id)
			}
		}
	}

	w.auth.mu.Lock()
	applyLogout := w.auth.applyLogout
	w.auth.mu.Unlock()

	var errs []string
	for _, id := range targets {
		var err error
		switch id {
		// openai holds two independent credentials in one slot — a platform API
		// key and a ChatGPT subscription — and revoking one must leave the other
		// standing.
		case "openai":
			err = store.ClearAPIKey("openai")
		case "openai-codex":
			err = store.ClearOAuth("openai")
		default:
			err = store.Clear(id)
		}
		if err != nil {
			errs = append(errs, id+": "+err.Error())
			continue
		}
		if applyLogout != nil {
			if err := applyLogout(id); err != nil {
				errs = append(errs, id+": "+err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", strings.Join(errs, "; "))
	}

	// The panel must be told, and so must every other client: a logout on the
	// phone changes what the laptop can do.
	w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("providers"))
	w.BroadcastAll(ctrlproto.AuthStateEvent(ctrlproto.AuthState{Kind: "success", Provider: p.Provider, Method: "logout"}))
	return nil
}

// saveEndpoint records a named openai-compatible server: its definition in
// config.json, its key (if the server wants one) in auth.json under the same id.
//
// The endpoint is PROBED first. Writing an unreachable backend would leave the
// operator with a provider that appears in /model and fails on the first turn,
// and the failure would surface as a broken conversation rather than as a typo in
// a base URL — which is the whole reason the shared slot probes too.
func (w *Workspace) saveEndpoint(ctx context.Context, name, baseURL, apiKey string, win int) error {
	if err := ValidEndpointName(name); err != nil {
		return err
	}
	if err := auth.ProbeOpenAICompatible(ctx, baseURL, apiKey); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", err.Error())
	}
	// MutateConfig, never LoadConfig+SaveConfig: SaveConfig is a whole-file
	// overwrite, and the web has N sessions that can each be saving a setting.
	if err := config.MutateConfig(func(c *config.Config) {
		if c.Endpoints == nil {
			c.Endpoints = map[string]config.EndpointConfig{}
		}
		c.Endpoints[name] = config.EndpointConfig{BaseURL: baseURL, ContextWindow: win}
	}); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "could not save the endpoint: %v", err)
	}
	// Only if there is one. A key written for a server that ignores keys is a
	// secret stored for no reason.
	if apiKey != "" {
		if m := w.authManager(); m != nil {
			if store := m.Store(); store != nil {
				if err := store.SetAPIKey(name, apiKey); err != nil {
					return ctrlproto.Errorf(ctrlproto.CodeInternal, "endpoint saved, but its key was not: %v", err)
				}
			}
		}
	}
	// Naming a backend is a login: it is the moment this provider became
	// reachable, and nothing else will go and ask it what models it serves. Skip
	// this and the endpoint appears in the picker with an empty model list until
	// the daemon is restarted — which sends the operator to the TUI to type in by
	// hand the models the server would have told us.
	w.applyCredential(name)
	w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("providers"))
	w.BroadcastAll(ctrlproto.AuthStateEvent(ctrlproto.AuthState{Kind: "success", Provider: name, Method: "endpoint"}))
	return nil
}

// AuthEndpointRemove forgets a named endpoint: its config entry and any key
// stored under its id.
//
// Not a logout, and not reachable through one. A logout forgets a secret and
// leaves the provider there to sign back into; this forgets which machine, which
// port, which context window — and nothing in terva remembers those but this
// entry. So it is a verb an operator has to ask for by name.
func (w *Workspace) AuthEndpointRemove(_ context.Context, p ctrlproto.AuthEndpointRemoveParams) error {
	m := w.authManager()
	if m == nil {
		return ctrlproto.Errorf(ctrlproto.CodeUnsupported, "this daemon does not serve provider logins")
	}
	id := strings.TrimSpace(p.ID)
	if _, ok := configEndpoints()[id]; !ok {
		// Refuse rather than no-op: "removed" for something that was never there
		// would let a typo look like a success, and the operator would go on
		// believing a backend is gone when it is still serving the agent.
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "no endpoint named %q", id)
	}
	if err := config.MutateConfig(func(c *config.Config) {
		delete(c.Endpoints, id)
	}); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "could not remove the endpoint: %v", err)
	}
	// The key outlives the definition otherwise: a secret for a provider that no
	// longer exists, which nothing would ever show the operator again.
	if store := m.Store(); store != nil {
		if err := store.Clear(id); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "endpoint removed, but its key was not: %v", err)
		}
	}
	w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("providers"))
	w.BroadcastAll(ctrlproto.AuthStateEvent(ctrlproto.AuthState{Kind: "success", Provider: id, Method: "endpoint-removed"}))
	return nil
}

// failFlow announces a refusal on the event stream and returns it. Callers that
// reject a submit before it ever reaches the manager use this, so that every
// outcome of a login — success, provider refusal, and our own bookkeeping
// refusals alike — arrives at a client the same way.
func (w *Workspace) failFlow(flow, provider string, err error) error {
	w.BroadcastAll(ctrlproto.AuthStateEvent(ctrlproto.AuthState{
		Kind:     "error",
		Flow:     flow,
		Provider: provider,
		Message:  err.Error(),
	}))
	return err
}

// authErr maps the manager's refusals onto wire codes. A superseded flow is BUSY,
// not a bad request: the client did nothing wrong — someone else started a login,
// and the pkce verifier this code was minted against is gone.
func authErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, auth.ErrFlowSuperseded), errors.Is(err, auth.ErrNoFlow):
		return ctrlproto.Errorf(ctrlproto.CodeBusy, "%v", err)
	default:
		// Everything else is the provider refusing the credential — a bad key, a
		// dead code, an endpoint that will not answer. That is the user's to fix,
		// so the message goes back verbatim.
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%v", err)
	}
}
