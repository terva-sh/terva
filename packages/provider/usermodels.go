package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// UserModelsFile is the JSON format for user-defined models.
// Place a models.json in $TERVA_HOME to add models that aren't in the
// baked-in catalog or to override catalog entries.
//
// Example:
//
//	{
//	  "providers": {
//	    "openai": {
//	      "models": [
//	        {
//	          "id": "gpt-5.5",
//	          "name": "GPT-5.5",
//	          "reasoning": true,
//	          "contextWindow": 400000,
//	          "maxTokens": 128000,
//	          "priceInput": 2.50,
//	          "priceOutput": 15.00,
//	          "priceCacheRead": 0.25,
//	          "capabilities": { "image-input": true }
//	        }
//	      ]
//	    }
//	  }
//	}
//
// capabilities keys are the provider.Capability names ("image-input",
// "image-output", "reasoning"); absent keys fall back to per-
// capability defaults. This is how a non-vision local model is marked
// text-only so terva drops images instead of bricking the session.
type UserModelsFile struct {
	Providers map[string]UserProvider `json:"providers"`
}

// UserProvider groups models under a provider key.
type UserProvider struct {
	Models []UserModel `json:"models"`
}

// UserModel is a single model entry in the user's models.json.
//
// Reasoning is a *bool so "not mentioned" is distinguishable from
// "explicitly false": a price-only override of a catalog reasoning
// model must not silently disable reasoning (which made OpenAI
// reasoning models 400 under the old bool field).
//
// Capabilities carries per-model capability tags ({"image-input":
// false, ...}); key presence IS the explicit-set marker, so the same
// tri-state lesson needs no extra side flags. Input is the legacy
// spelling for the image-input capability ("image" present ⇒ vision);
// an explicit Capabilities key wins over it.
type UserModel struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Reasoning       *bool           `json:"reasoning"`
	ContextWindow   int             `json:"contextWindow"`
	MaxTokens       int             `json:"maxTokens"`
	PriceInput      float64         `json:"priceInput"`
	PriceOutput     float64         `json:"priceOutput"`
	PriceCacheRead  float64         `json:"priceCacheRead"`
	PriceCacheWrite float64         `json:"priceCacheWrite"`
	BaseURL         string          `json:"baseUrl,omitempty"`
	Capabilities    map[string]bool `json:"capabilities,omitempty"`
	Input           []string        `json:"input"` // legacy capability spelling, see above
	API             string          `json:"api"`   // informational only
}

// UserOverride is one models.json entry held in the user layer: the
// converted Model plus which tri-state fields the user explicitly set
// (a flattened Model can't distinguish false from absent).
type UserOverride struct {
	Model        Model
	ReasoningSet bool
}

// LoadUserModels reads a models.json file and returns the models
// converted to the internal Model type. Returns nil on any error
// (missing file, bad JSON, etc.) so the caller can treat it as
// optional without error handling.
func LoadUserModels(path string) []Model {
	overrides, _ := LoadUserModelsWithWarnings(path)
	out := make([]Model, 0, len(overrides))
	for _, o := range overrides {
		out = append(out, o.Model)
	}
	return out
}

// LoadUserModelsWithWarnings is like LoadUserModels but also returns
// human-readable warnings about every recoverable issue it found in
// the file (unknown provider id, empty model id, malformed JSON for a
// single provider block, etc.). The caller is responsible for
// surfacing the warnings; the file is never rejected wholesale unless
// the top-level JSON itself fails to parse.
func LoadUserModelsWithWarnings(path string) ([]UserOverride, []string) {
	var warnings []string
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var file UserModelsFile
	if err := json.Unmarshal(data, &file); err != nil {
		warnings = append(warnings, fmt.Sprintf("models.json: parse error: %v (file ignored)", err))
		return nil, warnings
	}

	var out []UserOverride
	for providerName, prov := range file.Providers {
		if providerName == "" {
			warnings = append(warnings, "models.json: empty provider key skipped")
			continue
		}
		// Normalize legacy transport aliases to their provider names.
		normalized := providerName
		switch providerName {
		case "openai-responses":
			normalized = "openai"
		case "anthropic-messages":
			normalized = "anthropic"
		case "moonshot", "moonshot-ai", "kimi-code":
			normalized = "kimi"
		case "deepseek-chat", "deepseek-ai":
			normalized = "deepseek"
		}

		for i, um := range prov.Models {
			if um.ID == "" {
				warnings = append(warnings, fmt.Sprintf("models.json: provider %q entry #%d has empty id; skipped", providerName, i))
				continue
			}
			if um.ContextWindow < 0 || um.MaxTokens < 0 {
				warnings = append(warnings, fmt.Sprintf("models.json: %s/%s has negative contextWindow/maxTokens; clamped to 0", normalized, um.ID))
				if um.ContextWindow < 0 {
					um.ContextWindow = 0
				}
				if um.MaxTokens < 0 {
					um.MaxTokens = 0
				}
			}
			caps, reasoningFromCaps, capWarnings := userCaps(um)
			for _, w := range capWarnings {
				warnings = append(warnings, fmt.Sprintf("models.json: %s/%s: %s", normalized, um.ID, w))
			}
			m := Model{
				Provider:        normalized,
				ID:              um.ID,
				DisplayName:     um.Name,
				ContextWindow:   um.ContextWindow,
				MaxOutput:       um.MaxTokens,
				PriceInput:      um.PriceInput,
				PriceOutput:     um.PriceOutput,
				PriceCacheRead:  um.PriceCacheRead,
				PriceCacheWrite: um.PriceCacheWrite,
				BaseURL:         um.BaseURL,
				Source:          "user",
				Caps:            caps,
			}
			// The legacy Reasoning field and capabilities.reasoning are
			// two spellings of the same fact; the top-level field wins
			// when both are present.
			reasoningSet := reasoningFromCaps != nil
			if reasoningFromCaps != nil {
				m.Reasoning = *reasoningFromCaps
			}
			if um.Reasoning != nil {
				m.Reasoning = *um.Reasoning
				reasoningSet = true
			}
			if m.DisplayName == "" {
				m.DisplayName = m.ID
			}
			out = append(out, UserOverride{Model: m, ReasoningSet: reasoningSet})
		}
	}
	return out, warnings
}

// userCaps converts one models.json entry's capability spellings into
// the typed Caps map: the `capabilities` object first, then the legacy
// `input` array as a fallback spelling for image-input (an explicit
// capabilities key wins). The reasoning key is extracted separately so
// the caller can normalize it onto the legacy Model.Reasoning field.
// Unknown keys are kept (a models.json written for a newer terva must
// not break this one) but warned about.
func userCaps(um UserModel) (caps map[Capability]bool, reasoning *bool, warnings []string) {
	if len(um.Capabilities) == 0 && um.Input == nil {
		return nil, nil, nil
	}
	known := map[Capability]bool{}
	for _, c := range KnownCapabilities() {
		known[c] = true
	}
	caps = map[Capability]bool{}
	for k, v := range um.Capabilities {
		c := Capability(k)
		if !known[c] {
			warnings = append(warnings, fmt.Sprintf("unknown capability %q (kept; this terva understands: %v)", k, KnownCapabilities()))
		}
		caps[c] = v
	}
	// Legacy spelling: input: ["text","image"]. Only consulted when
	// the capabilities object didn't mention image-input.
	if um.Input != nil {
		if _, ok := caps[CapImageInput]; !ok {
			hasImage := false
			for _, in := range um.Input {
				if strings.EqualFold(strings.TrimSpace(in), "image") {
					hasImage = true
				}
			}
			caps[CapImageInput] = hasImage
		}
	}
	if v, ok := caps[CapReasoning]; ok {
		reasoning = &v
	}
	if len(caps) == 0 {
		caps = nil
	}
	return caps, reasoning, warnings
}

// SetUserOverrides replaces the "user" layer with the given
// models.json overrides. User entries take precedence over every
// other layer; nil clears the layer.
func SetUserOverrides(overrides []UserOverride) {
	activeMu.Lock()
	defer activeMu.Unlock()
	layerUser = append([]UserOverride(nil), overrides...)
	remergeLocked()
}

// SetUserModels is the []Model convenience form of SetUserOverrides
// for callers that build models programmatically (mostly tests): every
// field including Reasoning is treated as explicitly set. nil clears
// the user layer.
func SetUserModels(models []Model) {
	overrides := make([]UserOverride, 0, len(models))
	for _, m := range models {
		overrides = append(overrides, UserOverride{Model: m, ReasoningSet: true})
	}
	SetUserOverrides(overrides)
}

// applyUserOverrides merges the user layer onto base. Fields the user
// left unset keep the underlying entry's values: prices, display name,
// context window, and max output merge field-by-field, and Reasoning
// only applies when the models.json entry mentioned it (ReasoningSet).
// Entries with no underlying match append as new models.
func applyUserOverrides(base []Model, overrides []UserOverride) []Model {
	if len(overrides) == 0 {
		return base
	}
	byKey := func(p, id string) string { return p + "/" + id }
	index := make(map[string]int, len(base))
	for i, m := range base {
		index[byKey(m.Provider, m.ID)] = i
	}

	for _, o := range overrides {
		um := o.Model
		idx, ok := index[byKey(um.Provider, um.ID)]
		if !ok {
			// New model not in any lower layer.
			base = append(base, um)
			index[byKey(um.Provider, um.ID)] = len(base) - 1
			continue
		}
		existing := base[idx]
		if um.PriceInput > 0 {
			existing.PriceInput = um.PriceInput
		}
		if um.PriceOutput > 0 {
			existing.PriceOutput = um.PriceOutput
		}
		if um.PriceCacheRead > 0 {
			existing.PriceCacheRead = um.PriceCacheRead
		}
		if um.PriceCacheWrite > 0 {
			existing.PriceCacheWrite = um.PriceCacheWrite
		}
		if um.DisplayName != "" && um.DisplayName != um.ID {
			existing.DisplayName = um.DisplayName
		}
		if um.ContextWindow > 0 {
			existing.ContextWindow = um.ContextWindow
		}
		if um.MaxOutput > 0 {
			existing.MaxOutput = um.MaxOutput
		}
		if o.ReasoningSet {
			existing.Reasoning = um.Reasoning
		}
		// Capability keys merge key-wise; the user's explicit
		// assertions win over catalog/live/extra. Key presence in the
		// models.json `capabilities` map IS the explicit-set marker,
		// so no ReasoningSet-style side flag is needed.
		existing.Caps = mergeCaps(existing.Caps, um.Caps)
		if um.BaseURL != "" {
			existing.BaseURL = um.BaseURL
		}
		existing.Source = "user"
		existing.Speculative = false
		base[idx] = existing
	}
	return base
}
