package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Has is the only sanctioned read path for capability tags: explicit
// assertion > legacy Reasoning field (for CapReasoning) > per-
// capability default.
func TestCapabilityHas(t *testing.T) {
	cases := []struct {
		name string
		m    Model
		cap  Capability
		want bool
	}{
		{"explicit true", Model{Caps: map[Capability]bool{CapImageInput: true}}, CapImageInput, true},
		{"explicit false", Model{Caps: map[Capability]bool{CapImageInput: false}}, CapImageInput, false},
		{"image-input defaults true", Model{}, CapImageInput, true},
		{"image-output defaults false", Model{}, CapImageOutput, false},
		{"reasoning falls back to the legacy field", Model{Reasoning: true}, CapReasoning, true},
		{"reasoning legacy field false", Model{Reasoning: false}, CapReasoning, false},
		{"explicit reasoning wins over the legacy field", Model{Reasoning: true, Caps: map[Capability]bool{CapReasoning: false}}, CapReasoning, false},
		{"unknown capability defaults false", Model{}, Capability("future-thing"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.Has(tc.cap); got != tc.want {
				t.Errorf("Has(%s) = %v, want %v", tc.cap, got, tc.want)
			}
		})
	}
}

// mergeCaps must never mutate its inputs: base frequently aliases a
// catalog literal, and corrupting that would poison every later
// remerge.
func TestMergeCapsDoesNotMutate(t *testing.T) {
	base := map[Capability]bool{CapImageInput: true}
	over := map[Capability]bool{CapImageInput: false, CapImageOutput: true}

	out := mergeCaps(base, over)
	if !base[CapImageInput] {
		t.Error("base was mutated")
	}
	if out[CapImageInput] || !out[CapImageOutput] {
		t.Errorf("merged = %v, want over's keys to win", out)
	}

	// Empty overlay: base passes through unchanged (same map is fine —
	// nothing will write to it).
	if got := mergeCaps(base, nil); got[CapImageInput] != true || len(got) != 1 {
		t.Errorf("mergeCaps(base, nil) = %v", got)
	}
}

// Capability precedence through the real layer machinery: the curated
// catalog's explicit keys beat live discovery, models.json beats both,
// and absent keys resolve through the default.
func TestCapabilityLayerPrecedence(t *testing.T) {
	t.Cleanup(ResetCatalogLayers)
	ResetCatalogLayers()

	// deepseek-v4-pro asserts image-input:false in the static catalog (the
	// V4 API has no vision endpoint yet). A live entry claiming
	// image-input:true must NOT override that explicit catalog value, but
	// its other keys fill gaps the catalog left absent.
	SetLiveModels([]Model{{
		Provider: "deepseek", ID: "deepseek-v4-pro",
		Caps: map[Capability]bool{CapImageInput: true, CapImageOutput: true},
	}})
	m, err := FindModel("deepseek", "deepseek-v4-pro")
	if err != nil {
		t.Fatal(err)
	}
	if m.Has(CapImageInput) {
		t.Error("live discovery overrode the catalog's explicit image-input")
	}
	if !m.Has(CapImageOutput) {
		t.Error("live discovery should fill keys the catalog left absent")
	}

	// models.json outranks everything.
	SetUserOverrides([]UserOverride{{
		Model: Model{Provider: "deepseek", ID: "deepseek-v4-pro",
			Caps: map[Capability]bool{CapImageInput: true}},
	}})
	m, err = FindModel("deepseek", "deepseek-v4-pro")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Has(CapImageInput) {
		t.Error("user override must win over the catalog")
	}
	if !m.Has(CapImageOutput) {
		t.Error("user override must not clobber keys it did not mention")
	}

	// The catalog literal itself must be untouched by all the merging.
	for _, cm := range Catalog {
		if cm.Provider == "deepseek" && cm.ID == "deepseek-v4-pro" {
			if v, ok := cm.Caps[CapImageInput]; !ok || v {
				t.Error("catalog literal was mutated by the layer merge")
			}
			if _, ok := cm.Caps[CapImageOutput]; ok {
				t.Error("catalog literal gained a key from the live layer")
			}
		}
	}
}

// Caps survive the on-disk model cache round trip (the cache marshals
// Model directly).
func TestCapabilityCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	in := ModelCache{Models: []Model{{
		Provider: "openai-compatible", ID: "local-vlm",
		Caps: map[Capability]bool{CapImageInput: false},
	}}}
	if err := SaveCache(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Models) != 1 {
		t.Fatalf("cache round trip lost models: %+v", out.Models)
	}
	m := out.Models[0]
	if v, ok := m.Caps[CapImageInput]; !ok || v {
		t.Errorf("Caps after round trip = %v, want explicit image-input:false", m.Caps)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// A model tagged image-input:false gets image blocks dropped at
// serialization (string content, no multimodal parts), so a non-vision
// model can't be bricked by a screenshot in the transcript. A model
// without the tag keeps the multimodal parts — including deepseek,
// whose V4 catalog rows assert image-input:true (this is the deliberate
// behavior change that replaced the provider-wide name check).
func TestOpenAIImageDropPerModelCapability(t *testing.T) {
	t.Cleanup(ResetCatalogLayers)
	ResetCatalogLayers()
	RegisterExtraModel(Model{
		Provider: "openai-compatible", ID: "blind-model",
		Caps: map[Capability]bool{CapImageInput: false},
	})

	msgs := []Message{{Role: RoleUser, Content: []Content{
		TextBlock{Text: "what is in this image?"},
		ImageBlock{MimeType: "image/png", Data: []byte("bytes")},
	}}}

	c := NewOpenAI("token", "https://example.test").(*openaiClient)

	req, err := c.buildRequest(Request{Model: "blind-model", Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := req.Messages[0].Content.(string); !ok {
		t.Errorf("blind model user content = %T, want plain string (image dropped)", req.Messages[0].Content)
	} else if s == "" {
		t.Error("text part was lost along with the image")
	}

	// gpt-5 is vision-capable (catalog), unknown-model-xyz defaults to vision:
	// both keep the image as multimodal parts. (deepseek-v4-pro is NOT here —
	// its API has no vision endpoint, so it's marked text-only.)
	for _, id := range []string{"gpt-5", "unknown-model-xyz"} {
		req, err = c.buildRequest(Request{Model: id, Messages: msgs})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := req.Messages[0].Content.(string); ok {
			t.Errorf("%s: user content is a plain string, want multimodal parts (image kept)", id)
		}
	}
}

// models.json capability spellings: the `capabilities` object, the
// legacy `input` array, precedence between them, the reasoning alias,
// and unknown-key warnings.
func TestUserModelCapabilities(t *testing.T) {
	load := func(t *testing.T, entry string) ([]UserOverride, []string) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "models.json")
		blob := `{"providers":{"openai-compatible":{"models":[` + entry + `]}}}`
		if err := os.WriteFile(path, []byte(blob), 0o644); err != nil {
			t.Fatal(err)
		}
		return LoadUserModelsWithWarnings(path)
	}

	t.Run("capabilities object", func(t *testing.T) {
		got, warns := load(t, `{"id":"m","capabilities":{"image-input":false}}`)
		if len(warns) != 0 {
			t.Errorf("warnings: %v", warns)
		}
		if len(got) != 1 || got[0].Model.Has(CapImageInput) {
			t.Errorf("override = %+v, want explicit image-input:false", got)
		}
	})

	t.Run("legacy input array", func(t *testing.T) {
		got, _ := load(t, `{"id":"m","input":["text"]}`)
		if got[0].Model.Has(CapImageInput) {
			t.Error(`input:["text"] should mark the model text-only`)
		}
		got, _ = load(t, `{"id":"m","input":["text","image"]}`)
		if !got[0].Model.Has(CapImageInput) {
			t.Error(`input:["text","image"] should mark the model vision`)
		}
	})

	t.Run("capabilities wins over input", func(t *testing.T) {
		got, _ := load(t, `{"id":"m","input":["text"],"capabilities":{"image-input":true}}`)
		if !got[0].Model.Has(CapImageInput) {
			t.Error("explicit capabilities key must beat the legacy input spelling")
		}
	})

	t.Run("no spelling means no assertion", func(t *testing.T) {
		got, _ := load(t, `{"id":"m"}`)
		if got[0].Model.Caps != nil {
			t.Errorf("Caps = %v, want nil (nothing asserted)", got[0].Model.Caps)
		}
	})

	t.Run("reasoning alias normalizes onto the field", func(t *testing.T) {
		got, _ := load(t, `{"id":"m","capabilities":{"reasoning":true}}`)
		if !got[0].Model.Reasoning || !got[0].ReasoningSet {
			t.Errorf("override = %+v, want Reasoning true + set", got[0])
		}
		// Top-level field wins when both are present.
		got, _ = load(t, `{"id":"m","reasoning":false,"capabilities":{"reasoning":true}}`)
		if got[0].Model.Reasoning || !got[0].ReasoningSet {
			t.Errorf("override = %+v, want the top-level reasoning:false to win", got[0])
		}
	})

	t.Run("unknown keys warn but are kept", func(t *testing.T) {
		got, warns := load(t, `{"id":"m","capabilities":{"telepathy":true}}`)
		if len(warns) != 1 || !strings.Contains(warns[0], `unknown capability "telepathy"`) {
			t.Errorf("warnings = %v", warns)
		}
		if !got[0].Model.Has(Capability("telepathy")) {
			t.Error("unknown capability assertions must be kept (forward compat)")
		}
	})
}
