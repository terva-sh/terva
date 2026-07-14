package auth

import (
	"context"
	"errors"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"terva.sh/terva/packages/i18n"
)

// Event is delivered on Manager.Events().
type Event struct {
	Kind     string // "started" | "browser_open" | "success" | "error" | "canceled"
	Flow     FlowID // the attempt this is about; empty for browser_open
	Provider string
	Method   string
	URL      string // login URL (for "started"/"browser_open")
	UserCode string // device flows: the code to type at the verification page
	Message  string // on error
}

// FlowID identifies one login attempt.
//
// It exists because a login is not a pure function of its inputs: the manual
// OAuth flow leaves the pkce verifier and the state parameter on the Manager,
// and the code the user pastes back is only meaningful against the flow that
// minted them. With one user at one keyboard that is invisible. With a daemon
// serving a phone and a laptop it is a bug: two logins started concurrently
// overwrite each other's verifier, and the first user's paste is then exchanged
// against the second user's flow. The handle makes that refusable instead of
// silently wrong — see ErrFlowSuperseded.
type FlowID string

// Flow is a started login attempt: what to put in front of the user, and the
// handle needed to finish it.
type Flow struct {
	ID       FlowID
	Provider string
	Method   string // "apikey" | "oauth"
	URL      string // to DISPLAY — never assume the daemon's browser is the user's
	UserCode string // device flows: shown as text, not merely fused into URL
}

var (
	// ErrNoFlow — a completion arrived with no login in progress.
	ErrNoFlow = errors.New("no login flow in progress")
	// ErrFlowSuperseded — a completion arrived for a login that a newer one has
	// replaced. The credentials it carries belong to a flow whose pkce verifier
	// is gone; exchanging them would either fail obscurely or, worse, succeed
	// against the wrong attempt.
	ErrFlowSuperseded = errors.New("this login was superseded by a newer one")
)

// Manager drives a login flow end-to-end. It owns a local web server
// (for api-key form) plus provider-specific OAuth callback servers.
type Manager struct {
	store       *Store
	keyServer   *Server         // random-port web form server (api-key flow)
	oauthServer *CallbackServer // fixed-port callback server (oauth flow, only one at a time)
	mu          sync.Mutex
	events      chan Event
	openBrowser bool

	oauthCtx    context.Context
	oauthCancel context.CancelFunc

	// In-flight loopback flow state, remembered so the manual paste-back
	// flow can adopt it for providers whose "manual" variant shares the
	// same loopback redirect (OpenAI/Codex). Without this, StartManualOAuth
	// would mint a fresh state pointing at the same callback port that the
	// live server only accepts the original state for, producing a
	// guaranteed "state mismatch" when the user opens the displayed URL.
	oauthOp    *OAuthProvider
	oauthPKCE  PKCE
	oauthState string
	oauthURL   string

	manualOp            *OAuthProvider
	manualStoreProvider string
	manualEventProvider string
	manualPKCE          PKCE
	manualState         string
	manualFlow          FlowID

	// flowSeq mints FlowIDs. Monotonic, never reused, so a handle from a
	// superseded attempt can always be told apart from the current one.
	flowSeq atomic.Uint64

	// Probe seams, mirroring Server.probeFn. A key handed straight to the
	// Manager (the headless path) is validated exactly as the browser form
	// validates one, and tests swap these out to stay off the network.
	probeAPIKey func(ctx context.Context, provider, key string) error
	probeCompat func(ctx context.Context, baseURL, key string) error
}

// compatProvider is the openai-compatible endpoint: the one api-key provider
// whose login carries more than a key (a base URL, a model id, and an
// optional default context window).
const compatProvider = "openai-compatible"

// NewManager returns a Manager bound to store.
func NewManager(store *Store) *Manager {
	return &Manager{
		store:       store,
		events:      make(chan Event, 16),
		openBrowser: true,
		probeAPIKey: ProbeAPIKey,
		probeCompat: ProbeOpenAICompatible,
	}
}

// Store returns the underlying credential store.
func (m *Manager) Store() *Store { return m.store }

// Events returns the read-only event channel.
func (m *Manager) Events() <-chan Event { return m.events }

// SetOpenBrowser controls whether a started flow tries to open the login URL in
// a browser on THIS machine.
//
// A terminal on the user's laptop wants that; a daemon does not. `terva web`
// serves a browser that is somewhere else entirely, so an xdg-open here is at
// best a no-op on a headless box and at worst pops a window on a machine nobody
// is looking at. Any carrier whose user is not sitting at the daemon must turn
// this off. The URL is still returned and still emitted — displaying it is the
// contract; opening it is a convenience.
func (m *Manager) SetOpenBrowser(open bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.openBrowser = open
}

func (m *Manager) browserEnabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.openBrowser
}

// nextFlow mints a fresh handle.
func (m *Manager) nextFlow() FlowID {
	return FlowID(strconv.FormatUint(m.flowSeq.Add(1), 10))
}

// Close shuts down any running servers and cancels pending flows.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.keyServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = m.keyServer.Shutdown(ctx)
		cancel()
		m.keyServer = nil
	}
	if m.oauthServer != nil {
		m.oauthServer.Shutdown()
		m.oauthServer = nil
	}
	if m.oauthCancel != nil {
		m.oauthCancel()
		m.oauthCancel = nil
	}
}

// ---- API key flow ----

// StartAPIKey launches the API-key login flow.
//
// The URL it returns is a form on the DAEMON's loopback (see NewServer), so it
// is reachable only by a browser on this machine. A remote client must ignore it
// and use CompleteAPIKey, which is the same flow with the key handed over
// directly.
func (m *Manager) StartAPIKey(provider string) (Flow, error) {
	if !isKnownAPIKeyProvider(provider) {
		return Flow{}, errors.New(apiKeyProviderMessage())
	}
	if err := m.ensureKeyServer(); err != nil {
		return Flow{}, err
	}
	u := m.keyServer.URL() + "/apikey?provider=" + provider
	f := Flow{ID: m.nextFlow(), Provider: provider, Method: "apikey", URL: u}
	go m.maybeOpen(u)
	m.emit(Event{Kind: "started", Flow: f.ID, Provider: provider, Method: "apikey", URL: u})
	return f, nil
}

// CompleteAPIKey stores an API key handed to terva directly, rather than
// through the local browser form. The form server binds to loopback with a
// random port (see NewServer), so on a headless host — a remote box over
// SSH, a container — it is unreachable: there is no browser on that side of
// the connection, and the URL cannot be opened from anywhere else. Handing
// the key straight to terva is the only way in, and this is where it lands.
//
// The key is probed before it is stored, exactly as the browser form does,
// so a mistyped key is rejected here instead of surfacing as a confusing
// failure on the first request.
//
// openai-compatible does not come through here — it carries a base URL and
// a model id as well, so it has its own entry point below.
func (m *Manager) CompleteAPIKey(ctx context.Context, provider, key string) error {
	fail := func(err error) error {
		m.emit(Event{Kind: "error", Provider: provider, Method: "apikey", Message: err.Error()})
		return err
	}
	if !isKnownAPIKeyProvider(provider) {
		return fail(errors.New(apiKeyProviderMessage()))
	}
	if provider == compatProvider {
		return fail(errors.New(i18n.T("an openai-compatible endpoint needs a base url and model too")))
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fail(errors.New(i18n.T("api key is empty")))
	}
	if err := m.probeAPIKey(ctx, provider, key); err != nil {
		return fail(err)
	}
	if err := m.store.SetAPIKey(provider, key); err != nil {
		return fail(err)
	}
	m.emit(Event{Kind: "success", Provider: provider, Method: "apikey"})
	return nil
}

// CompleteCompatAPIKey stores an openai-compatible endpoint handed to terva
// directly. It is the headless twin of the browser form's compat fields, and
// it takes everything that form takes.
//
// baseURL and model are required — without them there is nowhere to send a
// request and nothing to send it with. The key is optional on purpose: local
// servers (lm studio, llama.cpp, ollama's /v1, vllm) routinely ignore it.
// contextWindow is a default for models the endpoint does not describe; 0
// means "unknown", which is also what a blank entry becomes.
func (m *Manager) CompleteCompatAPIKey(ctx context.Context, baseURL, model, key string, contextWindow int) error {
	fail := func(err error) error {
		m.emit(Event{Kind: "error", Provider: compatProvider, Method: "apikey", Message: err.Error()})
		return err
	}
	baseURL = strings.TrimSpace(baseURL)
	model = strings.TrimSpace(model)
	key = strings.TrimSpace(key)
	if baseURL == "" || model == "" {
		return fail(errors.New(i18n.T("base url and model are required for an openai-compatible endpoint")))
	}
	if contextWindow < 0 {
		contextWindow = 0
	}
	if err := m.probeCompat(ctx, baseURL, key); err != nil {
		return fail(err)
	}
	if err := m.store.SetCompatAPIKey(compatProvider, key, baseURL, model, contextWindow); err != nil {
		return fail(err)
	}
	m.emit(Event{Kind: "success", Provider: compatProvider, Method: "apikey"})
	return nil
}

func (m *Manager) ensureKeyServer() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.keyServer != nil {
		return nil
	}
	s, err := NewServer()
	if err != nil {
		return err
	}
	m.keyServer = s
	// Hand the server to the consumer rather than letting it reach back for
	// m.keyServer: Close() nils that field under the mutex, and the consumer
	// read it without one — a real data race, and on the losing schedule a nil
	// dereference in a goroutine, which takes the process down. The consumer
	// needs exactly one server for its whole life, so it should hold it.
	go m.consumeKeyServerResults(s)
	return nil
}

func (m *Manager) consumeKeyServerResults(s *Server) {
	for res := range s.Result() {
		if res.Err != nil {
			m.emit(Event{Kind: "error", Provider: res.Provider, Method: res.Method, Message: res.Err.Error()})
			continue
		}
		var setErr error
		if res.Provider == "openai-compatible" {
			setErr = m.store.SetCompatAPIKey(res.Provider, res.APIKey, res.BaseURL, res.Model, res.ContextWindow)
		} else {
			setErr = m.store.SetAPIKey(res.Provider, res.APIKey)
		}
		if setErr != nil {
			m.emit(Event{Kind: "error", Provider: res.Provider, Method: "apikey", Message: setErr.Error()})
			continue
		}
		m.emit(Event{Kind: "success", Provider: res.Provider, Method: "apikey"})
	}
}

// ---- OAuth flow ----

// StartOAuth launches the subscription OAuth flow for provider.
// Only one oauth flow may be in progress at a time (because the
// callback port is fixed per provider and re-used by the official CLIs).
//
// Note: "google" is intentionally not supported here. Google does not
// offer a subscription OAuth that exchanges a Google One AI / Gemini
// Advanced login for usable Generative Language API credentials, so
// the only supported google login path is the API-key flow.
func (m *Manager) StartOAuth(provider string) (Flow, error) {
	if provider == "kimi" {
		return m.StartKimiDeviceOAuth()
	}
	if provider == "github-copilot" {
		return m.StartGitHubCopilotDeviceOAuth()
	}
	storeProvider := provider
	var op OAuthProvider
	switch provider {
	case "anthropic":
		op = AnthropicOAuth
	case "openai", "openai-codex":
		op = OpenAIOAuth
		storeProvider = "openai"
	case "google":
		return Flow{}, i18n.Errorf("google login is api-key only; use api key login for gemini")
	case "deepseek":
		return Flow{}, i18n.Errorf("deepseek login is api-key only; use api key login")
	default:
		return Flow{}, i18n.Errorf("provider must be anthropic, openai, openai-codex, kimi, github-copilot, deepseek, or google")
	}

	m.mu.Lock()
	if m.oauthServer != nil {
		m.oauthServer.Shutdown()
		m.oauthServer = nil
	}
	if m.oauthCancel != nil {
		m.oauthCancel()
	}
	m.mu.Unlock()

	pkce, err := NewPKCE()
	if err != nil {
		return Flow{}, err
	}
	authURL, state, err := op.AuthorizeURL(pkce)
	if err != nil {
		return Flow{}, err
	}

	cs, err := NewCallbackServer(op, state)
	if err != nil {
		return Flow{}, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	f := Flow{ID: m.nextFlow(), Provider: provider, Method: "oauth", URL: authURL}
	m.mu.Lock()
	m.oauthServer = cs
	m.oauthCtx = ctx
	m.oauthCancel = cancel
	// Remember this flow's generation so a subsequent StartManualOAuth for
	// a provider sharing this loopback redirect adopts it instead of
	// minting a conflicting state on the same callback port.
	opCopy := op
	m.oauthOp = &opCopy
	m.oauthPKCE = pkce
	m.oauthState = state
	m.oauthURL = authURL
	m.mu.Unlock()

	go m.awaitOAuth(ctx, f.ID, op, storeProvider, provider, cs, pkce, state)
	go m.maybeOpen(authURL)
	m.emit(Event{Kind: "started", Flow: f.ID, Provider: provider, Method: "oauth", URL: authURL})
	return f, nil
}

func (m *Manager) awaitOAuth(ctx context.Context, flow FlowID, op OAuthProvider, storeProvider, eventProvider string, cs *CallbackServer, pkce PKCE, state string) {
	defer cs.Shutdown()

	fail := func(msg string) {
		m.emit(Event{Kind: "error", Flow: flow, Provider: eventProvider, Method: "oauth", Message: msg})
	}

	waitCtx, waitCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer waitCancel()
	res, err := cs.Result(waitCtx)
	if err != nil {
		if ctx.Err() == nil {
			fail("timeout waiting for callback")
		}
		return
	}
	if res.Err != nil {
		fail(res.Err.Error())
		return
	}

	exCtx, exCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer exCancel()
	tok, err := op.Exchange(exCtx, res.Code, res.State, pkce)
	if err != nil {
		fail(err.Error())
		return
	}
	if err := m.store.SetOAuth(storeProvider, *tok); err != nil {
		fail(err.Error())
		return
	}
	m.emit(Event{Kind: "success", Flow: flow, Provider: eventProvider, Method: "oauth"})
}

// StartKimiDeviceOAuth starts Kimi Code's device-code subscription login.
//
// The user code rides in the verification URL, but it is also returned on its
// own: a client that cannot open a URL for the user (a phone reading a panel
// served from a VM) has to be able to render "type this code" as text.
func (m *Manager) StartKimiDeviceOAuth() (Flow, error) {
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	if m.oauthCancel != nil {
		m.oauthCancel()
	}
	m.oauthCtx = ctx
	m.oauthCancel = cancel
	m.mu.Unlock()

	dev, err := RequestKimiDeviceAuthorization(ctx)
	if err != nil {
		return Flow{}, err
	}
	f := Flow{
		ID:       m.nextFlow(),
		Provider: "kimi",
		Method:   "oauth",
		URL:      dev.VerificationURIComplete,
		UserCode: dev.UserCode,
	}
	go m.maybeOpen(f.URL)
	m.emit(Event{Kind: "started", Flow: f.ID, Provider: "kimi", Method: "oauth", URL: f.URL, UserCode: f.UserCode})
	go m.awaitDevice(ctx, f, func() (*OAuthToken, error) { return PollKimiDeviceToken(ctx, dev) })
	return f, nil
}

// StartGitHubCopilotDeviceOAuth starts GitHub Copilot's device-code subscription login.
func (m *Manager) StartGitHubCopilotDeviceOAuth() (Flow, error) {
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	if m.oauthCancel != nil {
		m.oauthCancel()
	}
	m.oauthCtx = ctx
	m.oauthCancel = cancel
	m.mu.Unlock()

	dev, err := RequestGitHubCopilotDeviceAuthorization(ctx)
	if err != nil {
		return Flow{}, err
	}
	loginURL := dev.VerificationURI
	if dev.UserCode != "" {
		loginURL += "?user_code=" + url.QueryEscape(dev.UserCode)
	}
	f := Flow{
		ID:       m.nextFlow(),
		Provider: "github-copilot",
		Method:   "oauth",
		URL:      loginURL,
		UserCode: dev.UserCode,
	}
	go m.maybeOpen(f.URL)
	m.emit(Event{Kind: "started", Flow: f.ID, Provider: "github-copilot", Method: "oauth", URL: f.URL, UserCode: f.UserCode})
	go m.awaitDevice(ctx, f, func() (*OAuthToken, error) { return PollGitHubCopilotDeviceToken(ctx, dev) })
	return f, nil
}

// awaitDevice polls a device-code flow to completion. Both device providers
// differ only in how they poll and where the token lands, so the reporting —
// which is the part a wire client depends on — is shared rather than copied.
func (m *Manager) awaitDevice(ctx context.Context, f Flow, poll func() (*OAuthToken, error)) {
	fail := func(msg string) {
		m.emit(Event{Kind: "error", Flow: f.ID, Provider: f.Provider, Method: "oauth", Message: msg})
	}
	tok, err := poll()
	if err != nil {
		if ctx.Err() == nil {
			fail(err.Error())
		}
		return
	}
	if err := m.store.SetOAuth(f.Provider, *tok); err != nil {
		fail(err.Error())
		return
	}
	m.emit(Event{Kind: "success", Flow: f.ID, Provider: f.Provider, Method: "oauth"})
}

// StartManualOAuth begins an OAuth flow but does NOT start a local
// callback server or open a browser. The returned URL is shown to the
// user so they can complete the authorization on another device; the
// resulting code is pasted back via CompleteManualOAuth.
func (m *Manager) StartManualOAuth(provider string) (Flow, error) {
	if provider == "kimi" {
		return m.StartKimiDeviceOAuth()
	}
	if provider == "github-copilot" {
		return m.StartGitHubCopilotDeviceOAuth()
	}
	storeProvider := provider
	var op OAuthProvider
	switch provider {
	case "anthropic":
		op = AnthropicManualOAuth
	case "openai", "openai-codex":
		op = OpenAIOAuth
		storeProvider = "openai"
	case "google":
		return Flow{}, i18n.Errorf("google login is api-key only; use api key login for gemini")
	case "deepseek":
		return Flow{}, i18n.Errorf("deepseek login is api-key only; use api key login")
	default:
		return Flow{}, i18n.Errorf("provider must be anthropic, openai, openai-codex, kimi, github-copilot, deepseek, or google")
	}

	flow := m.nextFlow()

	// If a loopback OAuth flow is already in progress for a provider that
	// shares this redirect URI (OpenAI/Codex has no off-host manual page,
	// so its "manual" variant reuses the same localhost:port callback),
	// adopt that flow's pkce/state/URL instead of minting a fresh one.
	// Otherwise the displayed URL would carry a state the already-running
	// callback server rejects, yielding "state mismatch". Providers with a
	// genuinely separate manual redirect (Anthropic's copy-code page) fall
	// through to a fresh generation as before.
	m.mu.Lock()
	if m.oauthServer != nil && m.oauthOp != nil && m.oauthURL != "" &&
		m.oauthOp.RedirectURI() == op.RedirectURI() {
		m.manualOp = &op
		m.manualStoreProvider = storeProvider
		m.manualEventProvider = provider
		m.manualPKCE = m.oauthPKCE
		m.manualState = m.oauthState
		m.manualFlow = flow
		authURL := m.oauthURL
		m.mu.Unlock()
		f := Flow{ID: flow, Provider: provider, Method: "oauth", URL: authURL}
		m.emit(Event{Kind: "started", Flow: flow, Provider: provider, Method: "oauth", URL: authURL})
		return f, nil
	}
	m.mu.Unlock()

	pkce, err := NewPKCE()
	if err != nil {
		return Flow{}, err
	}
	authURL, state, err := op.AuthorizeURL(pkce)
	if err != nil {
		return Flow{}, err
	}

	m.mu.Lock()
	m.manualOp = &op
	m.manualStoreProvider = storeProvider
	m.manualEventProvider = provider
	m.manualPKCE = pkce
	m.manualState = state
	m.manualFlow = flow
	m.mu.Unlock()

	f := Flow{ID: flow, Provider: provider, Method: "oauth", URL: authURL}
	m.emit(Event{Kind: "started", Flow: flow, Provider: provider, Method: "oauth", URL: authURL})
	return f, nil
}

// CompleteManualOAuth exchanges the user-pasted authorization code for a token
// and stores it. Accepts a raw code, a "code#state" pair (what Anthropic's
// copy-code page shows), or the whole redirect URL out of the browser's address
// bar — see parseManualCodeInput.
//
// flow must be the handle StartManualOAuth returned. The pkce verifier and the
// state parameter live on the Manager, so a code is only meaningful against the
// flow that minted them: if a second login started in the meantime — another
// client, another device — this one's verifier is gone, and exchanging the code
// against the newer flow's verifier would be a silent mix-up of two people's
// logins. Refuse instead (ErrFlowSuperseded), and let the caller start again.
func (m *Manager) CompleteManualOAuth(ctx context.Context, flow FlowID, input string) error {
	m.mu.Lock()
	op := m.manualOp
	storeProvider := m.manualStoreProvider
	eventProvider := m.manualEventProvider
	pkce := m.manualPKCE
	state := m.manualState
	current := m.manualFlow
	m.mu.Unlock()
	if op == nil {
		return ErrNoFlow
	}
	if flow != current {
		return ErrFlowSuperseded
	}
	code, pastedState := parseManualCodeInput(strings.TrimSpace(input))
	if pastedState != "" {
		state = pastedState
	}
	if code == "" {
		return i18n.Errorf("empty code")
	}
	fail := func(err error) error {
		m.emit(Event{Kind: "error", Flow: flow, Provider: eventProvider, Method: "oauth", Message: err.Error()})
		return err
	}
	exCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tok, err := op.Exchange(exCtx, code, state, pkce)
	if err != nil {
		return fail(err)
	}
	if err := m.store.SetOAuth(storeProvider, *tok); err != nil {
		return fail(err)
	}
	m.mu.Lock()
	// Only clear the flow if it is still ours — a login that superseded this
	// one while the exchange was in flight owns the slot now, and wiping it
	// would strand a paste that is about to arrive for a perfectly live flow.
	if m.manualFlow == flow {
		m.clearManualLocked()
	}
	m.mu.Unlock()
	m.emit(Event{Kind: "success", Flow: flow, Provider: eventProvider, Method: "oauth"})
	return nil
}

func (m *Manager) clearManualLocked() {
	m.manualOp = nil
	m.manualStoreProvider = ""
	m.manualEventProvider = ""
	m.manualPKCE = PKCE{}
	m.manualState = ""
	m.manualFlow = ""
}

// parseManualCodeInput accepts any of:
//   - a bare authorization code
//   - a "code#state" pair
//   - a full redirect URL like http(s)://host:port/callback?code=X&state=Y
//
// and returns the extracted code and (if any) state.
func parseManualCodeInput(s string) (code, state string) {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		if u, err := url.Parse(s); err == nil {
			q := u.Query()
			return q.Get("code"), q.Get("state")
		}
	}
	if idx := strings.IndexByte(s, '#'); idx >= 0 {
		return s[:idx], s[idx+1:]
	}
	return s, ""
}

// CancelOAuth aborts any in-flight OAuth flow: the loopback callback server,
// the device-code poller, and the manual paste-back flow alike.
//
// It used to leave the manual state standing — the callback server went away
// but manualOp/manualPKCE did not, so a code pasted after a cancel was still
// exchanged, and a flow the user had explicitly abandoned could still store a
// credential. Clearing it here is what makes "cancel" mean cancelled.
//
// The "canceled" event has been part of Event's documented vocabulary since the
// beginning and was never once emitted; a client waiting for a terminal event
// after cancelling waited forever. It is emitted now.
func (m *Manager) CancelOAuth() {
	m.mu.Lock()
	flow := m.manualFlow
	provider := m.manualEventProvider
	if m.oauthCancel != nil {
		m.oauthCancel()
		m.oauthCancel = nil
	}
	if m.oauthServer != nil {
		m.oauthServer.Shutdown()
		m.oauthServer = nil
	}
	m.oauthOp = nil
	m.oauthURL = ""
	m.clearManualLocked()
	m.mu.Unlock()
	m.emit(Event{Kind: "canceled", Flow: flow, Provider: provider, Method: "oauth"})
}

// ---- shared ----

func (m *Manager) emit(e Event) {
	select {
	case m.events <- e:
	default:
	}
}

// maybeOpen tries to open u in the system browser — on THIS machine, which is
// why a daemon carrier turns it off (SetOpenBrowser).
func (m *Manager) maybeOpen(u string) {
	if !m.browserEnabled() {
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Start()
	m.emit(Event{Kind: "browser_open", URL: u})
}

// RefreshIfNeeded returns the currently-usable credential for provider,
// refreshing an expired OAuth access token if necessary.
func (m *Manager) RefreshIfNeeded(ctx context.Context, provider string) (string, string, error) {
	creds, err := m.store.Load()
	if err != nil {
		return "", "", err
	}
	p := creds.get(provider)
	if p == nil {
		return "", "", i18n.Errorf("unknown provider %q", provider)
	}
	if p.APIKey != "" {
		return p.APIKey, "apikey", nil
	}
	if p.OAuth == nil {
		return "", "", i18n.Errorf("no credentials for %s", provider)
	}
	if !p.OAuth.Expired() {
		return p.OAuth.AccessToken, "oauth", nil
	}
	if p.OAuth.RefreshToken == "" {
		return "", "", i18n.Errorf("%s access token expired and no refresh token is available; please /login again", provider)
	}
	var op OAuthProvider
	switch provider {
	case "anthropic":
		op = AnthropicOAuth
	case "openai":
		op = OpenAIOAuth
	}
	tok, err := op.Refresh(ctx, p.OAuth.RefreshToken)
	if err != nil {
		return "", "", err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = p.OAuth.RefreshToken
	}
	if err := m.store.SetOAuth(provider, *tok); err != nil {
		return "", "", err
	}
	return tok.AccessToken, "oauth", nil
}
