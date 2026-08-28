package dialogs

import (
	"fmt"
	"sort"
	"strings"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/tui"
)

// The login dialog is a RENDERER, not a login flow.
//
// It used to be both: it knew that openai-compatible needs four fields in a
// particular order, that an api-key login shows a paste box and an oauth login
// shows a paste box with different wording, and that bedrock is not a form at
// all. Every one of those facts also had to be known by the web panel, which
// could not import this package — so they were known twice, and the two copies
// had already drifted (the browser form and this one ordered the compat fields
// differently).
//
// Now the daemon describes the step — kind, prose, a URL to visit, N typed
// fields — and this draws it. It does not know which provider it is rendering,
// and adding one does not change it. Same descriptor the web client renders, from
// the same code: ctrlproto.AuthFlowStep, built by the workspace's AuthController.
// The TUI reaches that in-process, with no serialization; `terva attach` reaches
// the identical thing over the wire.

// loginStep is the current node in the login dialog state machine.
type loginStep int

// loginStepClosed is the zero value on purpose: a freshly-constructed
// dialog must default to closed so nothing shows up until Open() is
// explicitly called.
const (
	loginStepClosed   loginStep = iota
	loginStepMethod             // pick apikey vs subscription
	loginStepProvider           // pick anthropic vs openai vs kimi
	loginStepFlow               // render whatever the daemon asked for
	loginStepDone               // success or error, waiting for key to dismiss
)

const loginProviderPageSize = 8

// LoginDialog is a tiny inline dialog rendered above the editor while
// the user picks their login method and provider.
type LoginDialog struct {
	step     loginStep
	method   string // "apikey" | "oauth"
	provider string
	message  string
	success  bool
	cursor   int

	// flow is the step the daemon asked us to render, and eds are one editor per
	// field it named. flowErr is shown in place rather than closing the dialog, so
	// a rejected key does not throw away everything already typed.
	flow    ctrlproto.AuthFlowStep
	eds     []*tui.Editor
	edIdx   int
	flowErr string

	// status is a snapshot of the current login state per provider, captured when
	// Open() runs, so the user can see what they are already signed in to before
	// starting a new flow. Value is "apikey", "oauth", or "" (not logged in).
	status map[string]string

	// offerProvider/offerModel is a pair the daemon already proved it can run,
	// offered as a third row on the method picker when the provider the config
	// PINS turned out to be unusable. Empty when there is nothing to offer.
	//
	// Cleared by Open() rather than kept: the offer is a fact about one boot,
	// and a stale one would invite the user to "continue on" a provider after
	// they had already logged the pinned one back in. The caller re-offers each
	// time it opens the dialog, and only while it still has no session.
	offerProvider, offerModel string
}

func NewLoginDialog() *LoginDialog {
	return &LoginDialog{}
}

// Active reports whether the dialog consumes input.
func (d *LoginDialog) Active() bool { return d != nil && d.step != loginStepClosed }

// Open starts the dialog from scratch and captures the current
// login status for each provider so the picker can show it.
// store is the shared auth store (config.AuthStoreFor()); passing it in
// (instead of importing the agent package to call AuthPath()) avoids a
// cyclic import between agent and agent/modes, and — unlike the path this
// used to take — hands the dialog a store that can decrypt an
// encrypted-at-rest auth.json instead of misreading it as logged-out.
// A nil store simply leaves the status column empty.
func (d *LoginDialog) Open(store *auth.Store) {
	d.step = loginStepMethod
	d.method = ""
	d.provider = ""
	d.message = ""
	d.success = false
	d.cursor = 0
	d.flow = ctrlproto.AuthFlowStep{}
	d.eds = nil
	d.edIdx = 0
	d.flowErr = ""
	d.offerProvider, d.offerModel = "", ""
	// Best-effort: if the auth file can't be read, auth.Describe still returns
	// every loggable provider as not-logged-in. The status line just won't show
	// anything useful in that case, which is fine — the user was about to log
	// in anyway.
	//
	// The openai/openai-codex split (one store slot, two independent logins)
	// used to be reconstructed here by hand, and again in the logout dialog.
	// Describe owns it now.
	d.status = map[string]string{}
	if store == nil {
		return
	}
	creds, _ := store.Load()
	for id, st := range auth.Describe(creds) {
		d.status[id] = st.Method
	}
}

// OfferSession adds a "continue on <provider>/<model>" row to the method
// picker. Call it AFTER Open, which clears any previous offer.
//
// The pair must be one the daemon has already resolved a working credential
// for — the whole point is that choosing it cannot fail the way the pinned
// provider just did.
func (d *LoginDialog) OfferSession(provider, model string) {
	d.offerProvider, d.offerModel = provider, model
}

// methodOptions is the method picker's rows, and the ONE place their count is
// decided. It used to be a literal `max := 2` in the key handler beside a
// separate literal list in the renderer: adding a row meant remembering both,
// and forgetting the handler renders a row the cursor cannot reach.
func (d *LoginDialog) methodOptions() []string {
	opts := []string{
		i18n.T("api key"),
		i18n.T("subscription (claude pro/max - chatgpt plus/pro - chatgpt codex - kimi code - github copilot)"),
	}
	if d.offerProvider != "" {
		opts = append(opts, i18n.T("continue on %s/%s for this session",
			ProviderLabel(d.offerProvider), d.offerModel))
	}
	return opts
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
		opts := d.methodOptions()
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
	case loginStepFlow:
		lines = append(lines, d.renderFlow(th, width)...)
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

// providersForMethod returns the providers offered for a given login method,
// ordered for display. Which providers those ARE is the auth package's business
// (see auth.OAuthProviders); sorting them by their user-facing label is this
// layer's.
func providersForMethod(method string) []string {
	var providers []string
	if method == "oauth" {
		providers = auth.OAuthProviders()
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

// loginDialogAction is the result of handling a key press.
//
// Two verbs, where there used to be five (StartAPIKey, StartOAuth, SubmitCode,
// SubmitKey, SubmitCompat). Start a login, or submit the values the daemon asked
// for. The dialog no longer knows what a code is, or that a key is different from
// one — the daemon named the fields, and this hands them back.
type loginDialogAction struct {
	StartLogin bool
	Provider   string
	Method     string // apikey | oauth
	// Submit carries the filled-in fields, keyed by AuthField.Name. Nil unless the
	// user pressed enter on a completed form.
	Submit map[string]string
	Close  bool
	// UseOffer is the third verb, and it is not a login: bind a session to
	// Provider/Model, which the daemon already holds a working credential for.
	// Set only when the user picks the offer row (see OfferSession).
	UseOffer bool
	Model    string
}

// HandleKey advances the dialog and returns an action to apply, if any.
func (d *LoginDialog) HandleKey(k tui.Key) loginDialogAction {
	switch d.step {
	case loginStepMethod:
		return d.handleMethodKey(k)
	case loginStepProvider:
		return d.handleProviderKey(k)
	case loginStepFlow:
		return d.handleFlowKey(k)
	case loginStepDone:
		d.Close()
		return loginDialogAction{Close: true}
	}
	return loginDialogAction{}
}

func (d *LoginDialog) handleMethodKey(k tui.Key) loginDialogAction {
	max := len(d.methodOptions())
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
		switch d.cursor {
		case 0:
			d.method = "apikey"
		case 1:
			d.method = "oauth"
		default:
			// The offer row. Not a login at all — it binds a session to a
			// provider that already works, so it closes the dialog rather than
			// advancing to the provider picker.
			p, m := d.offerProvider, d.offerModel
			d.Close()
			return loginDialogAction{UseOffer: true, Provider: p, Model: m}
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
		// Stay put until the daemon answers with a step to render; ShowStep moves
		// us on. A dialog that jumped to a blank form and then had to be corrected
		// by an event is how the old one ended up with a "do not demote the compat
		// form" guard.
		return loginDialogAction{StartLogin: true, Provider: d.provider, Method: d.method}
	}
	return loginDialogAction{}
}

// ShowStep renders the step the daemon described. No-op if the user already
// dismissed the dialog — a login the user walked away from must not reappear.
func (d *LoginDialog) ShowStep(step ctrlproto.AuthFlowStep) {
	if d.step == loginStepClosed {
		return
	}
	d.step = loginStepFlow
	d.flow = step
	d.eds = make([]*tui.Editor, len(step.Fields))
	d.edIdx = 0
	d.flowErr = ""
}

// ShowFlowError puts the daemon's refusal in front of the user WITHOUT closing
// the form. A mistyped key should cost you the key, not everything else you
// typed — which for the compat endpoint is a base URL, a model, and a context
// window.
func (d *LoginDialog) ShowFlowError(msg string) {
	if d.step != loginStepFlow {
		return
	}
	d.flowErr = msg
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

// editor lazily builds the editor for field i. The theme is only available at
// render time, which is why these are not built in ShowStep.
func (d *LoginDialog) editor(th tui.Theme, i int) *tui.Editor {
	if i < 0 || i >= len(d.eds) {
		return nil
	}
	if d.eds[i] == nil {
		ed := tui.NewEditor(th.AccentBar(th.Accent))
		// A secret field is masked here and nowhere else, because this is the
		// only place a login editor is built. The web client renders the same
		// AuthField descriptor as <input type="password">; the TUI echoed the
		// key in the clear, on the surface most likely to be on a shared screen
		// or in a recording.
		if i < len(d.flow.Fields) && d.flow.Fields[i].Type == ctrlproto.AuthFieldSecret {
			ed.Mask = '•'
		}
		d.eds[i] = ed
	}
	return d.eds[i]
}

func (d *LoginDialog) fieldValue(i int) string {
	if i < 0 || i >= len(d.eds) || d.eds[i] == nil {
		return ""
	}
	return strings.TrimSpace(d.eds[i].Value())
}

// fieldNeeded reports whether f must be filled in RIGHT NOW.
//
// RequiredUnless names another field that relaxes this one. Naming an
// openai-compatible endpoint makes its default model pointless — a named endpoint
// is its own provider and discovers the models the server serves — so demanding
// one would be asking the operator to paste back what terva is about to find for
// itself. The daemon re-checks at submit; this only decides what the dialog asks.
func (d *LoginDialog) fieldNeeded(f ctrlproto.AuthField) bool {
	if !f.Required || f.RequiredUnless == "" {
		return f.Required
	}
	for i, other := range d.flow.Fields {
		if other.Name == f.RequiredUnless {
			return d.fieldValue(i) == ""
		}
	}
	// The field it names does not exist. Fall back to required: refusing a login is
	// recoverable, silently accepting an incomplete one is not.
	return true
}

// renderFlow draws the descriptor: a title, some prose, a URL to open, a device
// code to type, and one labelled editor per field. It branches on Kind, never on
// which provider this is.
func (d *LoginDialog) renderFlow(th tui.Theme, width int) []string {
	var lines []string
	title := d.flow.Title
	if title == "" {
		title = "login - " + ProviderLabel(d.provider)
	}
	lines = append(lines, FrameHeader(th, title, width))

	for _, l := range d.flow.Lines {
		lines = append(lines, th.FG256(th.Muted, l))
	}

	wrapW := width - 2
	if wrapW < 20 {
		wrapW = 20
	}
	if d.flow.URL != "" {
		lines = append(lines, "")
		lines = append(lines, th.FG256(th.Muted, i18n.T("open this URL in a browser:")))
		for _, seg := range tui.WrapANSILine(d.flow.URL, wrapW) {
			lines = append(lines, th.FG256(th.Accent, seg))
		}
	}
	if d.flow.UserCode != "" {
		lines = append(lines, "")
		lines = append(lines, th.FG256(th.Muted, i18n.T("and enter this code:")))
		lines = append(lines, th.FG256(th.Accent, "  "+d.flow.UserCode))
	}

	for i, f := range d.flow.Fields {
		lines = append(lines, "")
		label := f.Label
		if !d.fieldNeeded(f) {
			label += i18n.T(" (optional)")
		}
		if i == d.edIdx {
			lines = append(lines, th.FG256(th.Accent, "> "+label))
		} else {
			lines = append(lines, th.FG256(th.Muted, "  "+label))
		}
		edLines, _, _ := d.editor(th, i).Render(width - 2)
		lines = append(lines, edLines...)
		if f.Help != "" {
			lines = append(lines, th.FG256(th.Muted, "  "+f.Help))
		}
	}

	if d.flowErr != "" {
		lines = append(lines, "")
		lines = append(lines, th.FG256(th.Error, d.flowErr))
	}

	lines = append(lines, "")
	switch {
	case d.flow.Kind == "info":
		lines = append(lines, th.FG256(th.Muted, i18n.T("press any key to close")))
	case d.flow.Kind == "display":
		lines = append(lines, th.FG256(th.Muted, i18n.T("waiting for you to approve it - esc cancels")))
	case len(d.flow.Fields) > 1:
		lines = append(lines, th.FG256(th.Muted, i18n.T("tab/shift-tab move between fields - enter submits - esc cancels")))
	default:
		lines = append(lines, th.FG256(th.Muted, i18n.T("enter submits - esc cancels")))
	}
	lines = append(lines, FrameRule(th, width))
	return lines
}

// handleFlowKey drives whatever the daemon asked for.
//
// It validates only that the REQUIRED fields are filled — nothing else. Whether a
// context window is a number, whether a base URL is reachable, whether a key is
// accepted: all of that is the daemon's to decide, and it is decided in one place
// instead of here and there and in the manager.
func (d *LoginDialog) handleFlowKey(k tui.Key) loginDialogAction {
	if d.flow.Kind == "info" {
		d.Close()
		return loginDialogAction{Close: true}
	}
	switch k.Kind {
	case tui.KeyEsc:
		d.Close()
		return loginDialogAction{Close: true}
	case tui.KeyTab:
		if n := len(d.flow.Fields); n > 0 {
			d.edIdx = (d.edIdx + 1) % n
		}
		return loginDialogAction{}
	case tui.KeyShiftTab:
		if n := len(d.flow.Fields); n > 0 {
			d.edIdx = (d.edIdx - 1 + n) % n
		}
		return loginDialogAction{}
	}
	if len(d.flow.Fields) == 0 {
		return loginDialogAction{} // a display step: nothing to type into
	}
	ed := d.eds[d.edIdx]
	if ed == nil {
		return loginDialogAction{} // not rendered yet
	}
	if submit := ed.HandleKey(k); !submit {
		return loginDialogAction{}
	}

	// fieldNeeded, not f.Required: see the method. The label and this check must
	// agree, or the dialog marks a field optional and then refuses to submit
	// without it.
	values := map[string]string{}
	for i, f := range d.flow.Fields {
		v := d.fieldValue(i)
		if d.fieldNeeded(f) && v == "" {
			d.flowErr = i18n.T("%s is required", f.Label)
			d.edIdx = i
			return loginDialogAction{}
		}
		values[f.Name] = v
	}
	d.flowErr = ""
	return loginDialogAction{Submit: values, Provider: d.provider}
}

// CursorPos returns the absolute row/col inside the dialog where the terminal
// cursor should sit, so the real blinking cursor lands on the focused input.
// Returns -1,-1 when the dialog expects no typing.
func (d *LoginDialog) CursorPos(width int) (row, col int) {
	if d.step != loginStepFlow || len(d.flow.Fields) == 0 {
		return -1, -1
	}
	ed := d.eds[d.edIdx]
	if ed == nil {
		return -1, -1
	}
	wrapW := width - 2
	if wrapW < 20 {
		wrapW = 20
	}
	// Count the rows renderFlow emits ahead of the focused editor. interactive's
	// redraw wraps dialog output in padDialogFrame, which injects a blank row
	// after the frame header — hence the +1 that is not in renderFlow itself.
	rows := 1 /*frame header*/ + 1 /*padDialogFrame blank*/ + len(d.flow.Lines)
	if d.flow.URL != "" {
		rows += 1 /*blank*/ + 1 /*hint*/ + len(tui.WrapANSILine(d.flow.URL, wrapW))
	}
	if d.flow.UserCode != "" {
		rows += 3 // blank, hint, code
	}
	for i := 0; i < d.edIdx; i++ {
		rows += 2 // blank, label
		edLines, _, _ := d.editor(tui.Dark, i).Render(width - 2)
		rows += len(edLines)
		if d.flow.Fields[i].Help != "" {
			rows++
		}
	}
	rows += 2 // the focused field's own blank + label
	_, eRow, eCol := ed.Render(width - 2)
	return rows + eRow, eCol
}
