package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/tui"
)

// RequiredUnless is a rule with TWO implementations — this dialog and the web
// form — which is precisely the drift the one-descriptor design exists to
// prevent. So it is pinned on both sides.
//
// The rule: naming an openai-compatible endpoint makes it its own provider, which
// discovers the models the server serves. Demanding a default model as well would
// be asking the operator to paste back what terva is about to find for itself —
// and it is the field that, left blank on a required form, silently disabled the
// web's Sign in button.
func namedEndpointStep() ctrlproto.AuthFlowStep {
	return ctrlproto.AuthFlowStep{
		Flow: "f3", Kind: "form", Title: "Connect an OpenAI-compatible endpoint",
		Fields: []ctrlproto.AuthField{
			{Name: "name", Label: "Name", Type: "text"},
			{Name: "base_url", Label: "Base URL", Type: "text", Required: true},
			{Name: "model", Label: "Default model", Type: "text", Required: true, RequiredUnless: "name"},
		},
	}
}

// focus moves the editor cursor onto field i by walking tab, the way a user does.
func focusField(d *LoginDialog, i int) {
	for d.edIdx != i {
		d.HandleKey(tui.Key{Kind: tui.KeyTab})
	}
}

func typeField(d *LoginDialog, i int, s string) {
	focusField(d, i)
	for _, r := range s {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
}

// Unnamed, this is the single shared slot: it has no discovery to fall back on,
// so the model is genuinely required and the dialog must say so.
func TestTheSharedSlotStillDemandsAModel(t *testing.T) {
	d := onStep(namedEndpointStep())
	d.Render(tui.Theme{}, 80)

	typeField(d, 1, "http://localhost:1234/v1")
	act := d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	if act.Submit != nil {
		t.Fatalf("submitted without a model: %+v", act.Submit)
	}
	if !strings.Contains(d.flowErr, "Default model") {
		t.Errorf("flowErr = %q, want it to name the missing model", d.flowErr)
	}
}

// Named, the model requirement retires — the endpoint lists its own models.
func TestANamedEndpointDoesNotDemandAModel(t *testing.T) {
	d := onStep(namedEndpointStep())
	d.Render(tui.Theme{}, 80)

	typeField(d, 0, "workshop-3090")
	typeField(d, 1, "http://3090.box:8000/v1")
	act := d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	if act.Submit == nil {
		t.Fatalf("a named endpoint was refused for want of a model it will discover: %q", d.flowErr)
	}
	if act.Submit["name"] != "workshop-3090" || act.Submit["base_url"] != "http://3090.box:8000/v1" {
		t.Errorf("Submit = %+v, want the name and base URL as typed", act.Submit)
	}
	// Empty, not absent: the daemon reads the map by field name, and a missing key
	// and an empty one must not mean different things.
	if _, ok := act.Submit["model"]; !ok {
		t.Error("the model field was dropped from the submission rather than sent empty")
	}
}

// The label and the submit check must agree. A field marked required that the
// dialog will not actually demand — or the reverse — is how a form ends up
// refusing a login it just told you was complete.
func TestTheLabelAgreesWithWhatTheDialogWillDemand(t *testing.T) {
	d := onStep(namedEndpointStep())
	body := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if strings.Contains(body, "Default model (optional)") {
		t.Error("the model is marked optional while the endpoint is unnamed, but submitting without it is refused")
	}

	typeField(d, 0, "little-box")
	body = strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(body, "Default model (optional)") {
		t.Error("the endpoint is named, so the model is not required — the label must stop asking for it")
	}
}
