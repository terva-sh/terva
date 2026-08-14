package provider

import (
	"encoding/json"
	"testing"
)

// buildWithTools builds a request for model carrying one tool, and reports
// whether the tool survived onto the wire.
func buildWithTools(t *testing.T, model string) bool {
	t.Helper()
	c := NewGemini("k", "https://example.invalid").(*geminiClient)
	wire, _, err := c.buildRequest(Request{
		Model: model,
		Tools: []Tool{{
			Name:        "read",
			Description: "read a file",
			Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}},
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "draw a red square"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return len(wire.Tools) > 0 && len(wire.Tools[0].FunctionDeclarations) > 0
}

// 🪤 The suppression matched the substring "flash-image". Of the six live
// image ids that matched TWO accept tools perfectly well —
// gemini-3.1-flash-image and its -preview twin — and had every tool stripped
// from every request. An agent on those models could not read a file or run a
// command, and nothing said so: it reads as a model that refuses to work
// rather than a client that disarmed it.
//
// The other three ids below never matched the substring, so they were already
// correct by luck of spelling. They are listed anyway, because the rule they
// pass under is now a measurement rather than a pattern, and a future edit
// that reaches for a pattern again should fail here.
//
// Measured live 2026-08-14, one identical prompt per model with a function
// declaration attached: every id below returned 200 with an image.
func TestImageModelsThatAcceptToolsKeepThem(t *testing.T) {
	for _, id := range []string{
		"gemini-3-pro-image",
		"gemini-3-pro-image-preview",
		"gemini-3.1-flash-image",
		"gemini-3.1-flash-image-preview",
		"gemini-3.1-flash-lite-image",
	} {
		if !buildWithTools(t, id) {
			t.Errorf("%s: tools were stripped, but the live API accepts them (200) — "+
				"an agent on this model would silently have no tools", id)
		}
	}
}

// The other direction. This one really does reject tools, with
// 400 "Function calling is not enabled for this model", so sending them
// would brick every turn.
func TestTheImageModelThatRejectsToolsStillHasThemSuppressed(t *testing.T) {
	if buildWithTools(t, "gemini-2.5-flash-image") {
		t.Error("gemini-2.5-flash-image kept its tools: the live API answers " +
			"400 \"Function calling is not enabled for this model\"")
	}
	// A dated or -preview variant of the same generation is the same model
	// under another name, and cannot be probed once it is retired.
	if buildWithTools(t, "gemini-2.5-flash-image-preview") {
		t.Error("gemini-2.5-flash-image-preview kept its tools")
	}
}

// The retired 2.0-era spelling. No live id matches it today, so this is inert
// for a current key and only matters for a grandfathered one — where it cannot
// be re-probed and a wrong guess costs a 400 on every single turn.
func TestTheLegacyImageGenerationIdsStaySuppressed(t *testing.T) {
	if buildWithTools(t, "gemini-2.0-flash-exp-image-generation") {
		t.Error("gemini-2.0-flash-exp-image-generation kept its tools")
	}
}

// Ordinary text models must be untouched by any of this.
func TestTextModelsKeepTheirTools(t *testing.T) {
	for _, id := range []string{
		"gemini-flash-latest",
		"gemini-3.1-pro-preview",
		"gemini-3.1-flash-lite",
	} {
		if !buildWithTools(t, id) {
			t.Errorf("%s: a text model lost its tools", id)
		}
	}
}
