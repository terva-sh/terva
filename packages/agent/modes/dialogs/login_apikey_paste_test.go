package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

// waitingOn drives the dialog into loginStepWaiting for a given method and
// provider, the state /login lands in once a flow has started.
func waitingOn(method, provider string) *LoginDialog {
	d := &LoginDialog{}
	d.step = loginStepWaiting
	d.method = method
	d.provider = provider
	d.url = "http://127.0.0.1:54321/apikey?provider=" + provider
	return d
}

// typeAndEnter feeds a string into the dialog's editor and submits it.
func typeAndEnter(d *LoginDialog, s string) loginDialogAction {
	for _, r := range s {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	return d.HandleKey(tui.Key{Kind: tui.KeyEnter})
}

// The regression this whole change exists for: an api-key login used to
// route the pasted value into the OAuth code exchange, which had no flow in
// progress, returned an error that was then discarded — so the user typed a
// key, pressed enter, and nothing happened at all. A key must come back as
// SubmitKey, never SubmitCode.
func TestWaitingAPIKeySubmitsAsKeyNotCode(t *testing.T) {
	d := waitingOn("apikey", "opencode-go")
	// The editor is lazily built by Render; do that first, as the real
	// TUI does before any key reaches the dialog.
	d.Render(tui.Theme{}, 80)

	act := typeAndEnter(d, "sk-live-key")

	if act.SubmitKey != "sk-live-key" {
		t.Errorf("SubmitKey = %q, want the pasted key", act.SubmitKey)
	}
	if act.SubmitCode != "" {
		t.Errorf("SubmitCode = %q, want empty — a key is not an oauth code", act.SubmitCode)
	}
	if act.Provider != "opencode-go" {
		t.Errorf("Provider = %q, want it carried with the key", act.Provider)
	}
}

// The oauth path must be untouched: a code still goes to the code exchange.
func TestWaitingOAuthStillSubmitsAsCode(t *testing.T) {
	d := waitingOn("oauth", "anthropic")
	d.Render(tui.Theme{}, 80)

	act := typeAndEnter(d, "abc123#state")

	if act.SubmitCode != "abc123#state" {
		t.Errorf("SubmitCode = %q, want the pasted code", act.SubmitCode)
	}
	if act.SubmitKey != "" {
		t.Errorf("SubmitKey = %q, want empty on an oauth flow", act.SubmitKey)
	}
}

// An empty submit must do nothing rather than emit a doomed action.
func TestWaitingEmptySubmitIsInert(t *testing.T) {
	d := waitingOn("apikey", "opencode-go")
	d.Render(tui.Theme{}, 80)

	act := typeAndEnter(d, "   ")

	if act.SubmitKey != "" || act.SubmitCode != "" {
		t.Errorf("act = %+v, want an empty submit to be inert", act)
	}
}

// The api-key prompt must ask for a key. It used to say "paste the
// authorization code", which is what sent users looking for a code that an
// api-key login never produces.
func TestWaitingAPIKeyPromptAsksForAKey(t *testing.T) {
	d := waitingOn("apikey", "opencode-go")
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")

	if !strings.Contains(out, "API key") {
		t.Errorf("render = %q, want it to ask for an API key", out)
	}
	if strings.Contains(out, "authorization code") {
		t.Errorf("render still asks for an authorization code:\n%s", out)
	}
}

// ---- openai-compatible form ----

// compatForm opens the dialog on the openai-compatible field form.
func compatForm() *LoginDialog {
	d := &LoginDialog{}
	d.step = loginStepMethod // any non-closed step; ShowCompatForm refuses closed
	d.method = "apikey"
	d.provider = CompatProvider
	d.ShowCompatForm("http://127.0.0.1:54321/apikey?provider=openai-compatible")
	return d
}

// fill types s into the focused field, then tabs to the next one.
func fill(d *LoginDialog, s string) {
	for _, r := range s {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyTab})
}

// The compat endpoint carries a base URL and a model as well as a key, so it
// gets a form. Filling it must produce all four fields — this is what makes
// openai-compatible usable on a headless host at all.
func TestCompatFormSubmitsEveryField(t *testing.T) {
	d := compatForm()
	d.Render(tui.Theme{}, 80) // builds the field editors

	fill(d, "http://localhost:1234/v1") // base url -> tab
	fill(d, "qwen2.5-coder")            // model    -> tab
	fill(d, "sk-local")                 // key      -> tab
	for _, r := range "32768" {         // context window
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	act := d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	if act.SubmitCompat == nil {
		t.Fatalf("act = %+v, want a compat submission", act)
	}
	got := *act.SubmitCompat
	want := CompatSubmit{
		BaseURL:       "http://localhost:1234/v1",
		Model:         "qwen2.5-coder",
		Key:           "sk-local",
		ContextWindow: 32768,
	}
	if got != want {
		t.Errorf("submitted %+v, want %+v", got, want)
	}
}

// The key is optional — local servers ignore it — but the base URL and model
// are not. A missing one must be reported in place, not silently swallowed
// and not by throwing away everything already typed.
func TestCompatFormRequiresBaseURLAndModel(t *testing.T) {
	d := compatForm()
	d.Render(tui.Theme{}, 80)

	fill(d, "http://localhost:1234/v1") // base url, then tab to model
	act := d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	if act.SubmitCompat != nil {
		t.Fatalf("act = %+v, want no submission without a model", act)
	}
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "required") {
		t.Errorf("render = %q, want an inline validation message", out)
	}
	// The already-typed base url must survive the failed submit.
	if !strings.Contains(out, "http://localhost:1234/v1") {
		t.Error("a failed submit discarded what was already typed")
	}
}

func TestCompatFormAllowsAnEmptyKey(t *testing.T) {
	d := compatForm()
	d.Render(tui.Theme{}, 80)

	fill(d, "http://localhost:11434/v1")
	fill(d, "llama3")
	act := d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // key + window left blank

	if act.SubmitCompat == nil {
		t.Fatalf("act = %+v, want a submission with no key", act)
	}
	if act.SubmitCompat.Key != "" {
		t.Errorf("key = %q, want empty", act.SubmitCompat.Key)
	}
	if act.SubmitCompat.ContextWindow != 0 {
		t.Errorf("context window = %d, want 0 (unknown)", act.SubmitCompat.ContextWindow)
	}
}

func TestCompatFormRejectsNonNumericContextWindow(t *testing.T) {
	d := compatForm()
	d.Render(tui.Theme{}, 80)

	fill(d, "http://localhost:1234/v1")
	fill(d, "qwen")
	fill(d, "") // skip the key
	for _, r := range "lots" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	act := d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	if act.SubmitCompat != nil {
		t.Fatalf("act = %+v, want no submission for a non-numeric window", act)
	}
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "whole number") {
		t.Errorf("render = %q, want it to explain the context window format", out)
	}
}

// The form must still show the browser URL: it works for anyone at a browser
// on the terva host, and only fails to be reachable from elsewhere.
func TestCompatFormStillOffersTheBrowserForm(t *testing.T) {
	d := compatForm()
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "127.0.0.1:54321") {
		t.Errorf("render = %q, want the browser form URL offered too", out)
	}
}

// The api-key flow emits its "started" event — carrying the browser-form URL —
// after the compat form is opened, and the event handler turns that into a
// ShowWaiting call. ShowWaiting must hand the form its URL rather than replace
// it with the paste box: the form would otherwise vanish the instant it
// appeared, and the fields typed into the paste box behind it would be routed
// to the wrong place entirely. Only an end-to-end run catches this, so pin it.
func TestStartedEventDoesNotClobberTheCompatForm(t *testing.T) {
	d := compatForm()

	d.ShowWaiting("http://127.0.0.1:99999/apikey?provider=openai-compatible")

	if d.step != loginStepCompatForm {
		t.Fatalf("step = %v, want the compat form to survive a started event", d.step)
	}
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "base url") {
		t.Error("the compat form was replaced by the paste box")
	}
	if !strings.Contains(out, "99999") {
		t.Error("the compat form did not pick up the browser-form URL")
	}
}
