package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/i18n"
)

// LoginResult is delivered on the channel returned by Server.Result().
type LoginResult struct {
	Flow     FlowID
	Provider string
	Method   string // "apikey"; OAuth uses CallbackServer
	APIKey   string // populated when Method == "apikey"
	Code     string // Deprecated: OAuth results come from CallbackServer.
	State    string // Deprecated: OAuth results come from CallbackServer.
	// BaseURL, Model and ContextWindow are populated only for the
	// openai-compatible provider, whose login form captures a custom
	// endpoint, default model id, and default context-window size.
	BaseURL       string
	Model         string
	ContextWindow int
	Err           error
}

// Server serves API-key forms created by BeginAPIKey. It binds
// to 127.0.0.1 on a random free port and serves:
//
//	GET  /                      landing page (menu)
//	GET  /apikey?provider=...&flow=...&token=...   API key form
//	POST /apikey                form submit -> probes -> stores via caller
//	GET  /success               generic success page
//	GET  /error                 generic error page
//
// The caller receives login events on Result(). The server stays up
// until Shutdown() is called.
type Server struct {
	l             net.Listener
	srv           *http.Server
	baseURL       string
	results       chan LoginResult
	probeFn       func(ctx context.Context, provider, key string) error
	probeCompatFn func(ctx context.Context, baseURL, key string) error
	mu            sync.Mutex
	shutdown      bool
	flows         map[FlowID]*keyForm
	done          chan struct{}
}

type keyForm struct {
	provider, token string
	ctx             context.Context
	cancel          context.CancelFunc
	submitting      bool
}

// NewServer starts a new login server on a random free port bound to loopback.
func NewServer() (*Server, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{
		l:             l,
		baseURL:       "http://" + l.Addr().String(),
		results:       make(chan LoginResult, 4),
		probeFn:       ProbeAPIKey,
		probeCompatFn: ProbeOpenAICompatible,
		flows:         make(map[FlowID]*keyForm),
		done:          make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/apikey", s.handleAPIKey)
	mux.HandleFunc("/success", s.handleSuccess)
	mux.HandleFunc("/error", s.handleError)
	mux.HandleFunc("/logo.png", serveLogo)
	s.srv = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			// Preserve Origin on form POSTs without sending the flow URL in Referer.
			w.Header().Set("Referrer-Policy", "strict-origin")
			w.Header().Set("X-Frame-Options", "DENY")
			if r.Host != s.l.Addr().String() || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != s.baseURL) {
				http.Error(w, i18n.T("forbidden"), http.StatusForbidden)
				return
			}
			mux.ServeHTTP(w, r)
		}),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	go func() { _ = s.srv.Serve(l) }()
	return s, nil
}

// URL returns the base URL the server is listening on.
func (s *Server) URL() string { return s.baseURL }

// Port returns the TCP port the server is bound to.
func (s *Server) Port() int {
	return s.l.Addr().(*net.TCPAddr).Port
}

// Result returns the channel receiving LoginResult events.
func (s *Server) Result() <-chan LoginResult { return s.results }

// BeginAPIKey creates a provider-bound form for a caller-owned flow handle.
// The returned URL contains a credential-change token; do not log or share it.
func (s *Server) BeginAPIKey(flow FlowID, provider string) (string, error) {
	if flow == "" || !isKnownAPIKeyProvider(provider) {
		return "", ErrNoFlow
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shutdown || s.flows[flow] != nil {
		return "", ErrFlowSuperseded
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &keyForm{provider: provider, token: rand.Text(), ctx: ctx, cancel: cancel}
	s.flows[flow] = f
	return s.URL() + "/apikey?" + url.Values{
		"provider": {provider}, "flow": {string(flow)}, "token": {f.token},
	}.Encode(), nil
}

// CancelAPIKey invalidates a form and cancels its in-flight provider probe.
func (s *Server) CancelAPIKey(flow FlowID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.flows[flow]; f != nil {
		f.cancel()
		delete(s.flows, flow)
	}
}

// Shutdown stops the server. It is safe to call multiple times.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.shutdown {
		s.mu.Unlock()
		return nil
	}
	s.shutdown = true
	for flow, f := range s.flows {
		f.cancel()
		delete(s.flows, flow)
	}
	close(s.done)
	s.mu.Unlock()
	if err := s.srv.Shutdown(ctx); err != nil {
		_ = s.srv.Close()
		return err
	}
	return nil
}

// isKnownAPIKeyProvider reports whether the given provider supports
// API-key login through the loopback flow. OAuth-only paths are handled
// elsewhere (manager.StartOAuth).
func isKnownAPIKeyProvider(p string) bool {
	for _, provider := range APIKeyProviders() {
		if p == provider {
			return true
		}
	}
	return false
}

func apiKeyProviderMessage() string {
	return i18n.T("provider must be one of: %s", strings.Join(APIKeyProviders(), ", "))
}

// APIKeyProviders is the ordered list shown by /login -> api key.
func APIKeyProviders() []string {
	return []string{
		"anthropic", "openai", "kimi", "deepseek", "google",
		"moonshotai", "moonshotai-cn", "groq", "cerebras", "xai", "together",
		"huggingface", "openrouter", "mistral", "zai",
		"xiaomi", "xiaomi-token-plan-ams", "xiaomi-token-plan-cn", "xiaomi-token-plan-sgp",
		"minimax", "minimax-cn", "fireworks", "vercel-ai-gateway",
		"opencode", "opencode-go", "amazon-bedrock", "google-vertex", "azure-openai-responses",
		"github-copilot", "cloudflare-workers-ai", "cloudflare-ai-gateway",
		"openai-compatible",
	}
}

// ---- handlers ----

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	tpl.ExecuteTemplate(w, "index", map[string]any{
		"Port": s.Port(),
	})
}

func (s *Server) handleAPIKey(w http.ResponseWriter, r *http.Request) {
	var values url.Values
	switch r.Method {
	case http.MethodGet:
		values = r.URL.Query()
	case http.MethodPost:
		if r.Header.Get("Origin") != s.baseURL {
			http.Error(w, i18n.T("forbidden"), http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		values = r.PostForm
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flow := FlowID(values.Get("flow"))
	provider := values.Get("provider")
	s.mu.Lock()
	f := s.flows[flow]
	if s.shutdown || f == nil || f.provider != provider || subtle.ConstantTimeCompare([]byte(f.token), []byte(values.Get("token"))) != 1 {
		s.mu.Unlock()
		http.Error(w, i18n.T("this login is no longer in progress; start it again"), http.StatusForbidden)
		return
	}
	if r.Method == http.MethodGet {
		token := f.token
		s.mu.Unlock()
		tpl.ExecuteTemplate(w, "apikey", map[string]any{
			"Provider": provider, "Compat": provider == compatProvider,
			"Flow": flow, "Token": token,
		})
		return
	}
	if f.submitting {
		s.mu.Unlock()
		http.Error(w, i18n.T("login is already in progress"), http.StatusConflict)
		return
	}
	f.submitting = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		f.submitting = false
		s.mu.Unlock()
	}()
	key := strings.TrimSpace(values.Get("api_key"))
	baseURL := strings.TrimSpace(values.Get("base_url"))
	model := strings.TrimSpace(values.Get("model"))
	// Default context window for discovered models the server doesn't
	// describe. Optional; blank / unparseable leaves it 0 ("unknown").
	contextWindow, _ := strconv.Atoi(strings.TrimSpace(values.Get("context_window")))
	if contextWindow < 0 {
		contextWindow = 0
	}
	compat := provider == "openai-compatible"
	if compat {
		// The key is optional for local endpoints, but we need
		// somewhere to send requests and a model id to send them with.
		if baseURL == "" || model == "" {
			s.errorPage(w, i18n.T("base url and model are required for an openai-compatible endpoint"))
			return
		}
	} else if key == "" {
		s.errorPage(w, i18n.T("missing provider or api key"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	stop := context.AfterFunc(f.ctx, cancel)
	defer stop()
	var probeErr error
	if compat {
		probeErr = s.probeCompatFn(ctx, baseURL, key)
	} else {
		probeErr = s.probeFn(ctx, provider, key)
	}
	res := LoginResult{Flow: flow, Provider: provider, Method: "apikey", APIKey: key, BaseURL: baseURL, Model: model, ContextWindow: contextWindow, Err: probeErr}
	// Recheck after the probe: cancel or shutdown may have invalidated it.
	s.mu.Lock()
	if s.shutdown || s.flows[flow] != f {
		s.mu.Unlock()
		http.Error(w, i18n.T("this login is no longer in progress; start it again"), http.StatusForbidden)
		return
	}
	if err := ctx.Err(); err != nil {
		probeErr = err
		res.Err = err
	}
	select {
	case s.results <- res:
		if probeErr == nil {
			delete(s.flows, flow)
			f.cancel()
		}
	default:
		s.mu.Unlock()
		http.Error(w, i18n.T("login results are busy; try again"), http.StatusServiceUnavailable)
		return
	}
	s.mu.Unlock()
	if probeErr != nil {
		s.errorPage(w, probeErr.Error())
		return
	}
	s.successPage(w, provider, "api key")
}

func (s *Server) handleSuccess(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.successPage(w, q.Get("provider"), q.Get("method"))
}

func (s *Server) handleError(w http.ResponseWriter, r *http.Request) {
	s.errorPage(w, r.URL.Query().Get("message"))
}

func (s *Server) successPage(w http.ResponseWriter, provider, method string) {
	w.Header().Set("content-type", "text/html; charset=utf-8")
	tpl.ExecuteTemplate(w, "success", map[string]any{
		"Provider": provider,
		"Method":   method,
	})
}

func (s *Server) errorPage(w http.ResponseWriter, msg string) {
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	tpl.ExecuteTemplate(w, "error", map[string]any{"Message": msg})
}

// BuildRedirectURI returns the callback URL the OAuth server should
// redirect to.
// Deprecated: Server no longer accepts OAuth callbacks. Use CallbackServer.
func (s *Server) BuildRedirectURI() string {
	return s.baseURL + "/callback"
}

// ---- templates ----
//
// All pages share the monochrome monoStyle defined in callback.go so
// the browser tab looks like the tui: black on white, monospace, thin
// rules, no rounded boxes, no color.

var tpl = template.Must(template.New("index").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"/><title>terva login</title>` + monoStyle + `</head><body>
` + logoTag + `
<h1><span class="terva">terva</span> login</h1>
<hr class="rule">
<p>start /login inside <span class="terva">terva</span> and choose a provider to open its api key form.</p>
<hr class="rule">
<p class="muted">for a subscription login (claude pro/max - chatgpt plus/pro - kimi code - github copilot), close this tab and run /login inside <span class="terva">terva</span>.</p>
</body></html>`))

func init() {
	template.Must(tpl.New("apikey").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"/><title>terva login</title>` + monoStyle + `<style>
  form { display: flex; flex-direction: column; gap: 0.75rem; }
  label { font-size: 0.875rem; }
</style></head><body>
` + logoTag + `
<h1><span class="terva">terva</span> login - {{.Provider}} api key</h1>
<hr class="rule">
{{if .Compat}}
<p>point <span class="terva">terva</span> at any openai-compatible endpoint (lm studio, vllm, llama.cpp, ollama's /v1, a gateway, ...). enter the base url and a default model id; <span class="terva">terva</span> also auto-lists every model the endpoint serves from <span class="mono">/v1/models</span> in the <span class="mono">/model</span> picker. the api key is optional - many local servers ignore it.</p>
<p class="muted">the context window is a default for models the server doesn't describe its size for. leave blank if unsure; override per model in <span class="mono">models.json</span>.</p>
<form method="POST" action="/apikey">
  <input type="hidden" name="flow" value="{{.Flow}}" />
  <input type="hidden" name="token" value="{{.Token}}" />
  <input type="hidden" name="provider" value="{{.Provider}}" />
  <label for="base_url">base url (e.g. http://localhost:1234/v1)</label>
  <input id="base_url" name="base_url" type="text" autocomplete="off" autofocus placeholder="http://localhost:1234/v1" />
  <label for="model">default model id (e.g. qwen2.5-coder)</label>
  <input id="model" name="model" type="text" autocomplete="off" />
  <label for="context_window">default context window in tokens (optional, e.g. 32768)</label>
  <input id="context_window" name="context_window" type="number" min="0" autocomplete="off" placeholder="32768" />
  <label for="api_key">api key (optional)</label>
  <input id="api_key" name="api_key" type="password" autocomplete="off" />
  <button type="submit">log in</button>
</form>
{{else}}
<p>paste your {{.Provider}} api key. <span class="terva">terva</span> will probe the provider with it once, then save it if the key is accepted.</p>
<form method="POST" action="/apikey">
  <input type="hidden" name="flow" value="{{.Flow}}" />
  <input type="hidden" name="token" value="{{.Token}}" />
  <input type="hidden" name="provider" value="{{.Provider}}" />
  <label for="api_key">api key</label>
  <input id="api_key" name="api_key" type="password" autocomplete="off" autofocus />
  <button type="submit">log in</button>
</form>
{{end}}
</body></html>`))

	template.Must(tpl.New("success").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"/><title>terva - logged in</title>` + monoStyle + `</head><body>
` + logoTag + `
<h1><span class="mark">✓</span> logged in to {{.Provider}}</h1>
<hr class="rule">
<p class="msg">method: {{.Method}}</p>
<p class="muted"><span class="terva">terva</span> received the callback. you can close this tab and return to the terminal.</p>
</body></html>`))

	template.Must(tpl.New("error").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"/><title>terva - error</title>` + monoStyle + `</head><body>
` + logoTag + `
<h1><span class="mark">✗</span> login failed</h1>
<hr class="rule">
<p class="msg mono">{{.Message}}</p>
<p class="muted">go back to <span class="terva">terva</span> and try again.</p>
</body></html>`))
}
