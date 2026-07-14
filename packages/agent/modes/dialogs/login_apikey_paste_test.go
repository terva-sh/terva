package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/tui"
)

// These tests used to pin a class of bug the descriptor makes structurally
// impossible: a pasted API KEY routed into the OAuth code exchange, because a
// single paste box chose its destination from d.method. The dialog could not tell
// a key from a code, so it guessed, and a wrong guess failed silently.
//
// It does not guess now. The daemon NAMES the field — "api_key", "code" — and the
// dialog returns a map keyed by that name. There is no destination to choose. What
// is worth pinning is that the naming survives the round trip, and that this
// package still knows nothing about any provider.

// onStep drives the dialog into a rendered flow step — the state /login lands in
// once the daemon has described what it wants.
func onStep(step ctrlproto.AuthFlowStep) *LoginDialog {
	d := &LoginDialog{}
	d.step = loginStepMethod // any non-closed step; ShowStep refuses a closed dialog
	d.provider = "opencode-go"
	d.ShowStep(step)
	return d
}

func keyStep() ctrlproto.AuthFlowStep {
	return ctrlproto.AuthFlowStep{
		Flow: "f1", Kind: "form", Title: "Sign in with an API key",
		Fields: []ctrlproto.AuthField{
			{Name: "api_key", Label: "API key", Type: "secret", Required: true},
		},
	}
}

func codeStep() ctrlproto.AuthFlowStep {
	return ctrlproto.AuthFlowStep{
		Flow: "f2", Kind: "form", Title: "Authorize terva, then paste the code",
		URL: "https://console.anthropic.com/oauth/authorize?x=1",
		Fields: []ctrlproto.AuthField{
			{Name: "code", Label: "Authorization code", Type: "secret", Required: true},
		},
	}
}

// typeAndEnter feeds a string into the focused editor and submits.
func typeAndEnter(d *LoginDialog, s string) loginDialogAction {
	for _, r := range s {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	return d.HandleKey(tui.Key{Kind: tui.KeyEnter})
}

// A key comes back under the name the daemon gave it — and the dialog does not
// invent a field it was never asked for.
func TestAKeyComesBackAsTheFieldTheDaemonNamed(t *testing.T) {
	d := onStep(keyStep())
	d.Render(tui.Theme{}, 80) // editors are built lazily at render, as in the real TUI

	act := typeAndEnter(d, "sk-live-key")

	if act.Submit["api_key"] != "sk-live-key" {
		t.Errorf("Submit = %+v, want api_key to carry the pasted key", act.Submit)
	}
	if _, ok := act.Submit["code"]; ok {
		t.Error("the dialog invented a 'code' field the daemon never asked for")
	}
}

// ...and a code likewise. The dialog does not know these are different KINDS of
// secret, and does not need to.
func TestACodeComesBackAsTheFieldTheDaemonNamed(t *testing.T) {
	d := onStep(codeStep())
	d.Render(tui.Theme{}, 80)

	act := typeAndEnter(d, "abc123#state")

	if act.Submit["code"] != "abc123#state" {
		t.Errorf("Submit = %+v, want code to carry the pasted value", act.Submit)
	}
	if _, ok := act.Submit["api_key"]; ok {
		t.Error("the dialog invented an 'api_key' field the daemon never asked for")
	}
}

// An empty required field must not submit: it would spend a flow handle on a
// request the daemon can only refuse.
func TestAnEmptyRequiredFieldDoesNotSubmit(t *testing.T) {
	d := onStep(keyStep())
	d.Render(tui.Theme{}, 80)

	if act := typeAndEnter(d, "   "); act.Submit != nil {
		t.Errorf("act = %+v, want an empty required field to be inert", act)
	}
}

// The label the user reads is the daemon's, verbatim. The dialog used to compose
// its own prompt from d.method — and told api-key users to "paste the
// authorization code", sending them to look for a code an api-key login never
// produces.
func TestThePromptIsTheDaemonsLabel(t *testing.T) {
	d := onStep(keyStep())
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")

	if !strings.Contains(out, "API key") {
		t.Errorf("render = %q, want the daemon's label", out)
	}
	if strings.Contains(out, "authorization code") {
		t.Errorf("the dialog invented an 'authorization code' prompt for an api-key login:\n%s", out)
	}
}

// ---- a multi-field form (openai-compatible, and anything shaped like it) ----

func compatStep() ctrlproto.AuthFlowStep {
	return ctrlproto.AuthFlowStep{
		Flow: "f3", Kind: "form", Title: "Connect an OpenAI-compatible endpoint",
		Fields: []ctrlproto.AuthField{
			{Name: "base_url", Label: "Base URL", Type: "text", Required: true},
			{Name: "model", Label: "Default model", Type: "text", Required: true},
			{Name: "api_key", Label: "API key", Type: "secret"},
			{Name: "context_window", Label: "Default context window", Type: "integer"},
		},
	}
}

// fill types s into the focused field, then tabs on.
func fill(d *LoginDialog, s string) {
	for _, r := range s {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyTab})
}

// Four fields, rendered and returned, with nothing in this package naming any of
// them. The case that proves the descriptor earns its keep: the dialog has no idea
// what an openai-compatible endpoint is.
func TestAMultiFieldFormSubmitsEveryFieldTheDaemonAskedFor(t *testing.T) {
	d := onStep(compatStep())
	d.Render(tui.Theme{}, 80)

	fill(d, "http://localhost:1234/v1")
	fill(d, "qwen2.5-coder")
	fill(d, "sk-local")
	for _, r := range "32768" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	act := d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	for k, v := range map[string]string{
		"base_url":       "http://localhost:1234/v1",
		"model":          "qwen2.5-coder",
		"api_key":        "sk-local",
		"context_window": "32768",
	} {
		if act.Submit[k] != v {
			t.Errorf("Submit[%q] = %q, want %q (full: %+v)", k, act.Submit[k], v, act.Submit)
		}
	}
}

// The optional fields stay optional: a local server that ignores keys, and an
// endpoint that describes its own context window, must both be loggable.
func TestOptionalFieldsMayBeLeftBlank(t *testing.T) {
	d := onStep(compatStep())
	d.Render(tui.Theme{}, 80)

	fill(d, "http://localhost:1234/v1")
	for _, r := range "qwen" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	act := d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	if act.Submit == nil {
		t.Fatal("a form with its required fields filled did not submit")
	}
	if act.Submit["api_key"] != "" || act.Submit["context_window"] != "" {
		t.Errorf("blank optionals did not come back blank: %+v", act.Submit)
	}
}

// A missing REQUIRED field is reported in place, and nothing already typed is
// thrown away — which here is a base URL the user just went and looked up.
func TestAMissingRequiredFieldIsReportedWithoutLosingTheForm(t *testing.T) {
	d := onStep(compatStep())
	d.Render(tui.Theme{}, 80)

	fill(d, "http://localhost:1234/v1")
	act := d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // model is still empty

	if act.Submit != nil {
		t.Fatal("submitted with a required field empty")
	}
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "required") {
		t.Errorf("the user was not told a field is missing:\n%s", out)
	}
	if !strings.Contains(out, "http://localhost:1234/v1") {
		t.Error("the base URL the user already typed was discarded")
	}
}

// The daemon's refusal — a key the provider rejected — is shown IN the form, not
// instead of it. Losing a filled-in endpoint because a key was mistyped would be
// its own small cruelty.
func TestADaemonRefusalKeepsTheFormOnScreen(t *testing.T) {
	d := onStep(compatStep())
	d.Render(tui.Theme{}, 80)
	fill(d, "http://localhost:1234/v1")

	d.ShowFlowError("that endpoint refused the key (http 401)")

	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "refused the key") {
		t.Errorf("the daemon's refusal was not shown:\n%s", out)
	}
	if !strings.Contains(out, "http://localhost:1234/v1") {
		t.Error("the form was discarded along with the error")
	}
}

// A display step (a device code) has nothing to submit: the flow completes on its
// own and says so with an event. Enter must not fabricate an empty submission.
func TestADisplayStepHasNothingToSubmit(t *testing.T) {
	d := onStep(ctrlproto.AuthFlowStep{
		Flow: "f4", Kind: "display", Title: "Approve terva in your browser",
		URL: "https://github.com/login/device", UserCode: "ABCD-1234",
	})
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "ABCD-1234") {
		t.Errorf("the device code is not shown as text; a user who cannot open the URL has nothing to type:\n%s", out)
	}

	if act := d.HandleKey(tui.Key{Kind: tui.KeyEnter}); act.Submit != nil {
		t.Errorf("a display step submitted %+v", act.Submit)
	}
}

// An info step (bedrock, vertex, azure, cloudflare) is prose, not a form: there is
// no key for terva to take, and a paste box would be a dead end that looks like a
// way in. Any key dismisses it.
func TestAnInfoStepIsProseAndCloses(t *testing.T) {
	d := onStep(ctrlproto.AuthFlowStep{
		Kind: "info", Title: "Amazon Bedrock setup",
		Lines: []string{"Set:", "  AWS_BEARER_TOKEN_BEDROCK=..."},
	})
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "AWS_BEARER_TOKEN_BEDROCK") {
		t.Errorf("the guidance was not rendered:\n%s", out)
	}

	act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'x'})
	if !act.Close {
		t.Error("an info step did not close on a keypress")
	}
	if act.Submit != nil {
		t.Errorf("an info step submitted %+v — there is nothing to store", act.Submit)
	}
}
