package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/tui"
)

const fakeKey = "sk-ant-SUPERSECRET-0123456789"

// The TUI login dialog echoed API keys in the clear.
//
// The web client renders the SAME descriptor — ctrlproto.AuthField, built by
// the workspace's AuthController — and masks it: AuthStepForm.tsx picks
// <input type="password"> on exactly this Type. The TUI drew the editor
// unmasked, on the surface most likely to be on a shared screen, in a
// screen-share, or in a terminal recording. Two renderers of one descriptor
// disagreeing about whether a field is a secret is the drift the
// one-descriptor design exists to prevent.
func TestASecretLoginFieldIsMaskedOnScreen(t *testing.T) {
	d := onStep(keyStep())
	d.Render(tui.Theme{}, 80) // builds the editors
	typeField(d, 0, fakeKey)

	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")

	if strings.Contains(out, fakeKey) {
		t.Errorf("the API key is rendered in the clear:\n%s", out)
	}
	if strings.Contains(out, "SUPERSECRET") {
		t.Error("part of the key survived into the rendered output")
	}
	if !strings.Contains(out, strings.Repeat("•", 8)) {
		t.Errorf("no mask glyphs in the output — the field renders as nothing at all:\n%s", out)
	}
}

// The value must still SUBMIT in the clear. Masking is a display concern; a
// mask that reached the buffer would send bullets to the provider, which fails
// in a way nobody would attribute to this change.
func TestAMaskedFieldStillSubmitsItsRealValue(t *testing.T) {
	d := onStep(keyStep())
	d.Render(tui.Theme{}, 80)
	typeField(d, 0, fakeKey)

	if got := d.fieldValue(0); got != fakeKey {
		t.Errorf("submitted value = %q, want the real key — masking must not touch the buffer", got)
	}
}

// The complement: a non-secret field must NOT be masked. Without it, masking
// everything would pass the first test while hiding the base URL, the model
// name and the endpoint label — the three fields on the compat form that the
// user most needs to proofread.
func TestNonSecretLoginFieldsAreNotMasked(t *testing.T) {
	d := onStep(ctrlproto.AuthFlowStep{
		Flow: "f9", Kind: "form", Title: "Connect an OpenAI-compatible endpoint",
		Fields: []ctrlproto.AuthField{
			{Name: "base_url", Label: "Base URL", Type: "text", Required: true},
			{Name: "api_key", Label: "API key", Type: ctrlproto.AuthFieldSecret},
		},
	})
	d.Render(tui.Theme{}, 80)
	typeField(d, 0, "http://localhost:1234/v1")
	typeField(d, 1, fakeKey)

	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")

	if !strings.Contains(out, "http://localhost:1234/v1") {
		t.Errorf("the base URL was masked; only a secret field may be:\n%s", out)
	}
	if strings.Contains(out, fakeKey) {
		t.Error("the key is still in the clear alongside the unmasked URL")
	}
}
