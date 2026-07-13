package dialogs

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/tui"
)

// CompatProvider is the one api-key provider whose login needs more than a
// key — a base URL and a model id — so it gets a small form of its own
// rather than the single paste box.
const CompatProvider = "openai-compatible"

// compatProvider is the unexported alias used inside this file.
const compatProvider = CompatProvider

// Fields of the openai-compatible form, in tab order. Base URL and model are
// required; the key is optional (local servers routinely ignore it) and the
// context window is an optional default for models the endpoint does not
// describe.
const (
	compatFieldBaseURL = iota
	compatFieldModel
	compatFieldKey
	compatFieldContextWindow
	compatFieldCount
)

var compatFieldLabels = [compatFieldCount]string{
	compatFieldBaseURL:       "base url (required), e.g. http://localhost:1234/v1",
	compatFieldModel:         "default model id (required), e.g. qwen2.5-coder",
	compatFieldKey:           "api key (optional - many local servers ignore it)",
	compatFieldContextWindow: "default context window (optional, e.g. 32768)",
}

// loginStep is the current node in the login dialog state machine.
type loginStep int

// loginStepClosed is the zero value on purpose: a freshly-constructed
// dialog must default to closed so nothing shows up until Open() is
// explicitly called.
const (
	loginStepClosed     loginStep = iota
	loginStepMethod               // pick apikey vs subscription
	loginStepProvider             // pick anthropic vs openai vs kimi
	loginStepWaiting              // browser open, waiting for callback
	loginStepPasteCode            // user pastes the auth code here
	loginStepInfo                 // informational setup guidance
	loginStepCompatForm           // openai-compatible: base url, model, key, ctx window
	loginStepDone                 // success or error, waiting for key to dismiss
)

const loginProviderPageSize = 8

// LoginDialog is a tiny inline dialog rendered above the editor while
// the user picks their login method and provider.
type LoginDialog struct {
	step      loginStep
	method    string // "apikey" | "oauth"
	provider  string // "anthropic" | "openai" | "openai-codex" | "kimi" | "google"
	message   string
	success   bool
	url       string
	cursor    int
	codeEd    *tui.Editor
	infoTitle string
	infoLines []string

	// openai-compatible form state: one editor per field, the focused
	// field, and an inline validation message. compatErr is rendered in
	// place rather than closing the dialog, so a missing base url does not
	// throw away everything already typed.
	compatEds [compatFieldCount]*tui.Editor
	compatIdx int
	compatErr string

	// status is a snapshot of the current login state for each
	// provider, captured when Open() runs. Rendered above the
	// method picker so the user can see whether they're already
	// logged in (and how) before starting a new flow. Keys:
	// "anthropic", "openai", "openai-codex", "kimi", "google". Value is
	// "apikey", "oauth", or "" (not logged in).
	status map[string]string
}

func NewLoginDialog() *LoginDialog {
	return &LoginDialog{}
}

// Active reports whether the dialog consumes input.
func (d *LoginDialog) Active() bool { return d != nil && d.step != loginStepClosed }

// Open starts the dialog from scratch and captures the current
// login status for each provider so the picker can show it.
// tervaHome is the terva state directory ($TERVA_HOME); auth.json
// lives inside it. Passing the path in (instead of importing
// the agent package to call AuthPath()) avoids a cyclic import
// between agent and agent/modes.
func (d *LoginDialog) Open(tervaHome string) {
	d.step = loginStepMethod
	d.method = ""
	d.provider = ""
	d.message = ""
	d.success = false
	d.url = ""
	d.cursor = 0
	d.infoTitle = ""
	d.infoLines = nil
	d.status = map[string]string{}
	for _, p := range providersForMethod("apikey") {
		d.status[p] = ""
	}
	for _, p := range providersForMethod("oauth") {
		d.status[p] = ""
	}
	// Best-effort: if the auth file can't be read, treat every
	// provider as not-logged-in. The status line just won't show
	// anything useful in that case, which is fine — the user
	// was about to log in anyway.
	path := filepath.Join(tervaHome, "auth.json")
	if creds, err := auth.NewStore(path).Load(); err == nil {
		d.status["anthropic"] = creds.Method("anthropic")
		d.status["openai"] = ""
		if creds.OpenAI.APIKey != "" {
			d.status["openai"] = "apikey"
		}
		d.status["openai-codex"] = ""
		if creds.OpenAI.OAuth != nil {
			d.status["openai-codex"] = "oauth"
		}
		d.status["kimi"] = creds.Method("kimi")
		d.status["deepseek"] = creds.Method("deepseek")
		d.status["google"] = creds.Method("google")
		d.status["github-copilot"] = creds.Method("github-copilot")
		for p := range creds.AdditionalAPIKeyCreds {
			d.status[p] = creds.Method(p)
		}
	}
}

// Close hides the dialog.
func (d *LoginDialog) Close() {
	d.step = loginStepClosed
}

// Render returns the dialog lines or nil when inactive.
func (d *LoginDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var lines []string

	switch d.step {
	case loginStepMethod:
		opts := []string{
			"api key",
			"subscription (claude pro/max - chatgpt plus/pro - chatgpt codex - kimi code - github copilot)",
		}
		lines = append(lines, FrameHeader(th, "login", width))
		for _, l := range d.renderStatusLines(th) {
			lines = append(lines, l)
		}
		lines = append(lines, th.FG256(th.Muted, i18n.T("choose login method (↑/↓, enter, esc to cancel):")))
		for i, o := range opts {
			plain := "  " + o
			if i == d.cursor {
				lines = append(lines, th.PadHighlight(plain, width))
			} else {
				lines = append(lines, th.FG256(th.Muted, plain))
			}
		}
		lines = append(lines, FrameRule(th, width))
	case loginStepProvider:
		opts := providersForMethod(d.method)
		lines = append(lines, FrameHeader(th, "login - "+d.method, width))
		for _, l := range d.renderStatusLines(th) {
			lines = append(lines, l)
		}
		lines = append(lines, th.FG256(th.Muted, i18n.T("pick a provider (↑/↓, enter, esc to cancel)")))
		start, end := d.providerPage(len(opts))
		for i := start; i < end; i++ {
			o := opts[i]
			tag := providerPickerTag(d.method, d.status[o])
			label := "  " + ProviderLabel(o)
			plain := label + tag
			if i == d.cursor {
				lines = append(lines, th.PadHighlight(plain, width))
			} else {
				lines = append(lines, th.FG256(th.Muted, label)+th.FG256(th.Accent, tag))
			}
		}
		if len(opts) > loginProviderPageSize {
			lines = append(lines, th.FG256(th.Muted, fmt.Sprintf("  (%d/%d)", d.cursor+1, len(opts))))
		}
		lines = append(lines, FrameRule(th, width))
	case loginStepWaiting:
		lines = append(lines, FrameHeader(th, "login - "+d.method+" - "+ProviderLabel(d.provider), width))
		// The api-key page binds to loopback, so on a headless host the URL
		// below is unreachable and the paste box is the whole flow. Say so,
		// rather than asking for an "authorization code" that an api-key
		// login never produces.
		if d.method == "apikey" {
			lines = append(lines, th.FG256(th.Muted, i18n.T("paste your API key below, or open this URL in a browser on this machine:")))
		} else {
			lines = append(lines, th.FG256(th.Muted, "open this URL in a browser:"))
		}
		wrapW := width - 2
		if wrapW < 20 {
			wrapW = 20
		}
		// Future note (applies to both URL blocks in this dialog): these wrap a
		// plain URL then colour each segment, which is correct. They could adopt
		// tui.WrapANSILineKeepStyle (colour once, keep-style wrap) to match the
		// direct-emit standard, but with no correctness gain — single-colour
		// text already re-coloured per piece. Convert opportunistically.
		for _, seg := range tui.WrapANSILine(d.url, wrapW) {
			lines = append(lines, th.FG256(th.Accent, seg))
		}
		lines = append(lines, "")
		if d.method == "apikey" {
			lines = append(lines, th.FG256(th.Muted, i18n.T("paste your API key:")))
		} else {
			lines = append(lines, th.FG256(th.Muted, i18n.T("paste the authorization code (or full redirect URL / code#state):")))
		}
		if d.codeEd == nil {
			d.codeEd = tui.NewEditor(th.AccentBar(th.Accent))
		}
		edLines, _, _ := d.codeEd.Render(width - 2)
		for _, l := range edLines {
			lines = append(lines, l)
		}
		lines = append(lines, "")
		if d.method == "apikey" {
			lines = append(lines, th.FG256(th.Muted, "enter submits - esc cancels"))
		} else {
			lines = append(lines, th.FG256(th.Muted, "enter submits - esc cancels - waiting for browser callback in background"))
		}
		lines = append(lines, FrameRule(th, width))
	case loginStepCompatForm:
		lines = append(lines, FrameHeader(th, "login - apikey - "+ProviderLabel(d.provider), width))
		lines = append(lines, th.FG256(th.Muted, i18n.T("point terva at any openai-compatible endpoint (lm studio, vllm, llama.cpp, ollama's /v1, a gateway).")))
		lines = append(lines, "")
		wrapW := width - 2
		if wrapW < 20 {
			wrapW = 20
		}
		for i := range compatFieldCount {
			label := i18n.T(compatFieldLabels[i])
			if i == d.compatIdx {
				lines = append(lines, th.FG256(th.Accent, "> "+label))
			} else {
				lines = append(lines, th.FG256(th.Muted, "  "+label))
			}
			edLines, _, _ := d.compatEditor(th, i).Render(width - 2)
			lines = append(lines, edLines...)
		}
		if d.compatErr != "" {
			lines = append(lines, "")
			lines = append(lines, th.FG256(th.Error, d.compatErr))
		}
		lines = append(lines, "")
		// The browser form is still live for anyone at a browser on this
		// machine; on a headless host it is unreachable and this form is
		// the whole flow.
		lines = append(lines, th.FG256(th.Muted, i18n.T("or use the browser form on this machine:")))
		for _, seg := range tui.WrapANSILine(d.url, wrapW) {
			lines = append(lines, th.FG256(th.Accent, seg))
		}
		lines = append(lines, "")
		lines = append(lines, th.FG256(th.Muted, "tab/shift-tab move between fields - enter submits - esc cancels"))
		lines = append(lines, FrameRule(th, width))
	case loginStepPasteCode:
		lines = append(lines, FrameHeader(th, "login - "+d.method+" - "+ProviderLabel(d.provider)+" - paste code", width))
		lines = append(lines, th.FG256(th.Muted, "open this URL in any browser:"))
		wrapW := width - 2
		if wrapW < 20 {
			wrapW = 20
		}
		for _, seg := range tui.WrapANSILine(d.url, wrapW) {
			lines = append(lines, th.FG256(th.Accent, seg))
		}
		lines = append(lines, "")
		lines = append(lines, th.FG256(th.Muted, i18n.T("paste the authorization code (or full redirect URL / code#state):")))
		if d.codeEd == nil {
			d.codeEd = tui.NewEditor(th.AccentBar(th.Accent))
		}
		edLines, _, _ := d.codeEd.Render(width - 2)
		for _, l := range edLines {
			lines = append(lines, l)
		}
		lines = append(lines, "")
		lines = append(lines, th.FG256(th.Muted, "enter submits - esc cancels"))
		lines = append(lines, FrameRule(th, width))
	case loginStepInfo:
		title := d.infoTitle
		if title == "" {
			title = "login - setup"
		}
		lines = append(lines, FrameHeader(th, title, width))
		for _, l := range d.infoLines {
			lines = append(lines, l)
		}
		lines = append(lines, "")
		lines = append(lines, th.FG256(th.Muted, "press any key to close"))
		lines = append(lines, FrameRule(th, width))
	case loginStepDone:
		title := "login - failed"
		body := th.FG256(th.Error, d.message)
		if d.success {
			title = "login - success"
			body = th.FG256(th.Tool, fmt.Sprintf("logged in to %s via %s", ProviderLabel(d.provider), d.method))
		}
		lines = append(lines, FrameHeader(th, title, width))
		lines = append(lines, body)
		lines = append(lines, th.FG256(th.Muted, "press any key to close"))
		lines = append(lines, FrameRule(th, width))
	}
	return lines
}

// providersForMethod returns the providers offered for a given login
// method. API-key is the universal path so it lists every provider;
// subscription/OAuth only lists providers that actually issue tokens
// usable against the same API the model picker drives (Google's
// consumer Gemini Advanced login does not, and DeepSeek has no
// subscription product at all).
func providersForMethod(method string) []string {
	var providers []string
	if method == "oauth" {
		providers = []string{"anthropic", "openai-codex", "kimi", "github-copilot"}
	} else {
		providers = auth.APIKeyProviders()
	}
	sort.Slice(providers, func(a, b int) bool {
		return ProviderLabel(providers[a]) < ProviderLabel(providers[b])
	})
	return providers
}

// ProviderLabel returns the user-facing label for a provider id.
func ProviderLabel(id string) string { return provider.ProviderLabel(id) }

func providerPickerTag(method, status string) string {
	switch method {
	case "apikey":
		// In the API-key picker, only call out an existing subscription so
		// users know choosing this provider will add/replace API-key auth
		// while subscription auth is still configured. Unconfigured rows do
		// not need a redundant "api key" suffix.
		if status == "oauth" {
			return "  (subscription configured)"
		}
	case "oauth":
		// In the subscription picker, only call out an existing API key.
		if status == "apikey" {
			return "  (api key configured)"
		}
	}
	return ""
}

func (d *LoginDialog) providerPage(total int) (start, end int) {
	if total <= loginProviderPageSize {
		return 0, total
	}
	if d.cursor < 0 {
		d.cursor = 0
	}
	if d.cursor >= total {
		d.cursor = total - 1
	}
	start = (d.cursor / loginProviderPageSize) * loginProviderPageSize
	end = start + loginProviderPageSize
	if end > total {
		end = total
	}
	return start, end
}

// renderStatusLines returns an overview of the current login
// state for each provider, one row per provider, suitable to
// insert between the frame header and the picker body. Logged-
// in providers get a green checkmark in front; providers with
// no credentials render as a muted dash so the list layout
// stays aligned across first-run and re-login cases.
//
// Returns nil when neither provider is logged in (first-run
// case — a pair of "not logged in" rows is just noise when the
// user is about to pick a method anyway).
func (d *LoginDialog) renderStatusLines(th tui.Theme) []string {
	anth := d.status["anthropic"]
	op := d.status["openai"]
	codex := d.status["openai-codex"]
	kimi := d.status["kimi"]
	ds := d.status["deepseek"]
	goog := d.status["google"]
	gh := d.status["github-copilot"]
	if anth == "" && op == "" && codex == "" && kimi == "" && ds == "" && goog == "" && gh == "" {
		return nil
	}
	row := func(id, method string) string {
		label := ProviderLabel(id)
		var mark, body string
		switch method {
		case "apikey":
			mark = th.FG256(th.Tool, "✓")
			body = th.FG256(th.Muted, label+": api key")
		case "oauth":
			mark = th.FG256(th.Tool, "✓")
			body = th.FG256(th.Muted, label+": subscription")
		default:
			mark = th.FG256(th.Muted, "–")
			body = th.FG256(th.Muted, label+": not logged in")
		}
		return "  " + mark + " " + body
	}
	out := []string{
		row("anthropic", anth),
		row("openai", op),
		row("openai-codex", codex),
		row("kimi", kimi),
		row("deepseek", ds),
		row("google", goog),
		row("github-copilot", gh),
	}
	for _, p := range providersForMethod("apikey") {
		switch p {
		case "anthropic", "openai", "openai-codex", "kimi", "deepseek", "google", "github-copilot":
			continue
		}
		if method := d.status[p]; method != "" {
			out = append(out, row(p, method))
		}
	}
	out = append(out, "")
	return out
}

// Key is the result of handling a key press.
type loginDialogAction struct {
	StartAPIKey bool
	StartOAuth  bool
	StartManual bool
	Provider    string
	Close       bool
	SubmitCode  string
	// SubmitKey carries a pasted API key. It is distinct from SubmitCode
	// because the two go to different places: a code is exchanged with the
	// provider, a key is stored as-is. Routing a key through the code path
	// is what used to make headless api-key logins fail silently.
	SubmitKey string
	// SubmitCompat carries a filled-in openai-compatible form.
	SubmitCompat *CompatSubmit
}

// CompatSubmit is a completed openai-compatible login form.
type CompatSubmit struct {
	BaseURL       string
	Model         string
	Key           string // optional
	ContextWindow int    // 0 = unknown
}

// HandleKey advances the dialog and returns an action to apply, if any.
func (d *LoginDialog) HandleKey(k tui.Key) loginDialogAction {
	switch d.step {
	case loginStepMethod:
		return d.handleMethodKey(k)
	case loginStepProvider:
		return d.handleProviderKey(k)
	case loginStepWaiting:
		return d.handleWaitingKey(k)
	case loginStepPasteCode:
		return d.handlePasteCodeKey(k)
	case loginStepCompatForm:
		return d.handleCompatFormKey(k)
	case loginStepInfo:
		d.Close()
		return loginDialogAction{Close: true}
	case loginStepDone:
		d.Close()
		return loginDialogAction{Close: true}
	}
	return loginDialogAction{}
}

func (d *LoginDialog) handleMethodKey(k tui.Key) loginDialogAction {
	max := 2
	switch k.Kind {
	case tui.KeyUp:
		if d.cursor > 0 {
			d.cursor--
		}
	case tui.KeyDown:
		if d.cursor < max-1 {
			d.cursor++
		}
	case tui.KeyEsc:
		d.Close()
		return loginDialogAction{Close: true}
	case tui.KeyEnter:
		if d.cursor == 0 {
			d.method = "apikey"
		} else {
			d.method = "oauth"
		}
		d.step = loginStepProvider
		d.cursor = 0
	}
	return loginDialogAction{}
}

func (d *LoginDialog) handleProviderKey(k tui.Key) loginDialogAction {
	switch k.Kind {
	case tui.KeyUp:
		if d.cursor > 0 {
			d.cursor--
		}
	case tui.KeyDown:
		providers := providersForMethod(d.method)
		if d.cursor < len(providers)-1 {
			d.cursor++
		}
	case tui.KeyPageUp:
		d.cursor -= loginProviderPageSize
		if d.cursor < 0 {
			d.cursor = 0
		}
	case tui.KeyPageDown:
		providers := providersForMethod(d.method)
		d.cursor += loginProviderPageSize
		if d.cursor >= len(providers) {
			d.cursor = len(providers) - 1
		}
	case tui.KeyEsc:
		d.Close()
		return loginDialogAction{Close: true}
	case tui.KeyEnter:
		providers := providersForMethod(d.method)
		if d.cursor < 0 || d.cursor >= len(providers) {
			return loginDialogAction{}
		}
		d.provider = providers[d.cursor]
		d.step = loginStepWaiting
		if d.method == "apikey" {
			return loginDialogAction{StartAPIKey: true, Provider: d.provider}
		}
		return loginDialogAction{StartOAuth: true, Provider: d.provider}
	}
	return loginDialogAction{}
}

// ShowWaiting transitions to the waiting state with the given URL.
// No-op if the user has already dismissed the dialog.
func (d *LoginDialog) ShowWaiting(url string) {
	if d.step == loginStepClosed {
		return
	}
	// The compat form is already the right surface for this login, and it
	// wants the browser-form URL, not to be replaced by a paste box. The
	// api-key flow emits its "started" event (carrying that URL) after the
	// form has been opened, so without this the event would demote the form
	// the instant it appeared.
	if d.step == loginStepCompatForm {
		d.url = url
		return
	}
	d.step = loginStepWaiting
	d.url = url
}

// ShowCompatForm opens the openai-compatible field form. url is the browser
// form, still running and still usable from a browser on this machine; the
// TUI form exists because that URL is loopback-only and so unreachable from
// a headless host.
func (d *LoginDialog) ShowCompatForm(url string) {
	if d.step == loginStepClosed {
		return
	}
	d.step = loginStepCompatForm
	d.url = url
	d.compatIdx = 0
	d.compatErr = ""
}

// compatEditor lazily builds the editor for field i.
func (d *LoginDialog) compatEditor(th tui.Theme, i int) *tui.Editor {
	if d.compatEds[i] == nil {
		d.compatEds[i] = tui.NewEditor(th.AccentBar(th.Accent))
	}
	return d.compatEds[i]
}

// compatValue reads a field without disturbing it.
func (d *LoginDialog) compatValue(i int) string {
	if d.compatEds[i] == nil {
		return ""
	}
	return strings.TrimSpace(d.compatEds[i].Value())
}

// SetURL replaces the displayed verification URL without changing the
// step. Restarting a manual OAuth flow re-issues the URL while the
// dialog is already parked on the paste-code screen.
func (d *LoginDialog) SetURL(url string) { d.url = url }

// ShowInfo transitions to an informational setup dialog.
// No-op if the user has already dismissed the dialog.
func (d *LoginDialog) ShowInfo(title string, lines []string) {
	if d.step == loginStepClosed {
		return
	}
	d.step = loginStepInfo
	d.infoTitle = title
	d.infoLines = lines
}

// ShowResult transitions to the done state with the given outcome.
// No-op if the user has already dismissed the dialog.
func (d *LoginDialog) ShowResult(success bool, message string) {
	if d.step == loginStepClosed {
		return
	}
	d.step = loginStepDone
	d.success = success
	d.message = message
}

func (d *LoginDialog) handleWaitingKey(k tui.Key) loginDialogAction {
	if k.Kind == tui.KeyEsc {
		d.Close()
		return loginDialogAction{Close: true}
	}
	if d.codeEd == nil {
		return loginDialogAction{}
	}
	if submit := d.codeEd.HandleKey(k); submit {
		v := d.codeEd.SubmitValue()
		d.codeEd.Clear()
		if strings.TrimSpace(v) == "" {
			return loginDialogAction{}
		}
		if d.method == "apikey" {
			return loginDialogAction{SubmitKey: v, Provider: d.provider}
		}
		return loginDialogAction{SubmitCode: v}
	}
	return loginDialogAction{}
}

// handleCompatFormKey drives the openai-compatible field form. Tab and
// shift-tab move between fields; enter submits the whole form.
func (d *LoginDialog) handleCompatFormKey(k tui.Key) loginDialogAction {
	switch k.Kind {
	case tui.KeyEsc:
		d.Close()
		return loginDialogAction{Close: true}
	case tui.KeyTab:
		d.compatIdx = (d.compatIdx + 1) % compatFieldCount
		return loginDialogAction{}
	case tui.KeyShiftTab:
		d.compatIdx = (d.compatIdx - 1 + compatFieldCount) % compatFieldCount
		return loginDialogAction{}
	}

	ed := d.compatEds[d.compatIdx]
	if ed == nil {
		// Not rendered yet, so there is nothing to type into.
		return loginDialogAction{}
	}
	submit := ed.HandleKey(k)
	if !submit {
		return loginDialogAction{}
	}

	// Enter submits the form, not just the focused field. Validate here so
	// a mistake is shown in place and nothing already typed is lost.
	baseURL := d.compatValue(compatFieldBaseURL)
	model := d.compatValue(compatFieldModel)
	if baseURL == "" || model == "" {
		d.compatErr = i18n.T("base url and model are required")
		return loginDialogAction{}
	}
	ctxWindow := 0
	if raw := d.compatValue(compatFieldContextWindow); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			d.compatErr = i18n.T("context window must be a positive whole number")
			d.compatIdx = compatFieldContextWindow
			return loginDialogAction{}
		}
		ctxWindow = n
	}
	d.compatErr = ""
	return loginDialogAction{
		Provider: compatProvider,
		SubmitCompat: &CompatSubmit{
			BaseURL:       baseURL,
			Model:         model,
			Key:           d.compatValue(compatFieldKey),
			ContextWindow: ctxWindow,
		},
	}
}

func (d *LoginDialog) handlePasteCodeKey(k tui.Key) loginDialogAction {
	if k.Kind == tui.KeyEsc {
		d.Close()
		return loginDialogAction{Close: true}
	}
	if d.codeEd == nil {
		return loginDialogAction{}
	}
	if submit := d.codeEd.HandleKey(k); submit {
		code := d.codeEd.SubmitValue()
		d.codeEd.Clear()
		return loginDialogAction{SubmitCode: code}
	}
	return loginDialogAction{}
}

// CursorPos returns the absolute row/col inside the dialog where the
// terminal cursor should sit (paste-code step). Returns -1, -1 if the
// dialog is not in an input-expecting state. The host uses this to
// place the real blinking cursor on the code input.
func (d *LoginDialog) CursorPos(width int) (row, col int) {
	if d.codeEd == nil {
		return -1, -1
	}
	if d.step != loginStepPasteCode && d.step != loginStepWaiting {
		return -1, -1
	}
	_, eRow, eCol := d.codeEd.Render(width - 2)
	wrapW := width - 2
	if wrapW < 20 {
		wrapW = 20
	}
	urlLines := len(tui.WrapANSILine(d.url, wrapW))
	// interactive.redraw wraps dialog output with padDialogFrame, which
	// injects a blank row after the frame header. Count that row here so
	// the real terminal cursor lands on the editor input instead of the
	// prompt above it.
	baseOffset := 1 /*frameHeader*/ + 1 /*padDialogFrame blank*/ + 1 /*hint*/ + urlLines + 1 /*blank*/ + 1 /*prompt*/
	return baseOffset + eRow, eCol
}
