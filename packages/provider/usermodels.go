package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
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
//	          "temperature": 0.7,
//	          "priceInput": 2.50,
//	          "priceOutput": 15.00,
//	          "priceCacheRead": 0.25,
//	          "priceOutputImage": 60.00,
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
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	Reasoning            *bool           `json:"reasoning"`
	ContextWindow        int             `json:"contextWindow"`
	DesiredContextWindow int             `json:"desiredContextWindow"`
	MaxTokens            int             `json:"maxTokens"`
	PriceInput           float64         `json:"priceInput"`
	PriceOutput          float64         `json:"priceOutput"`
	PriceCacheRead       float64         `json:"priceCacheRead"`
	PriceCacheWrite      float64         `json:"priceCacheWrite"`
	PriceOutputImage     float64         `json:"priceOutputImage"` // output rate for IMAGE tokens; 0 = one output rate
	BaseURL              string          `json:"baseUrl,omitempty"`
	Temperature          *float32        `json:"temperature,omitempty"`      // default sampling temperature (0–2); nil = inherit
	DefaultReasoning     string          `json:"defaultReasoning,omitempty"` // per-model reasoning level when no global level is set; "" = inherit
	Capabilities         map[string]bool `json:"capabilities,omitempty"`
	Input                []string        `json:"input"` // legacy capability spelling, see above
	API                  string          `json:"api"`   // informational only
}

// UserOverride is one models.json entry held in the user layer: the
// converted Model plus which tri-state fields the user explicitly set
// (a flattened Model can't distinguish false from absent).
type UserOverride struct {
	Model        Model
	ReasoningSet bool
}

// LegacyUserModelProviderAliases maps historical models.json provider keys onto
// the provider they were renamed to. It exists only for files written before
// the rename; a key here must be DEAD — a name the registry no longer knows.
//
// Two entries were not dead, and both silently repointed an operator's override
// onto a different provider than the one they named:
//
//   - "openai-responses" mapped to "openai" while being a first-class registry
//     id with its own client (NewOpenAIResponses), its own catalog rows, its own
//     reasoning wire and its own label. A models.json block keyed
//     "openai-responses" landed on plain "openai" with no warning — and because
//     the WRITE side (UpsertUserModel/FindUserModel) does not normalize at all,
//     /model showed the value as saved while the merged catalog carried it
//     elsewhere. baseUrl and prices ride the same path, so editing a Responses
//     model silently repointed and repriced the operator's chat provider.
//
//   - "moonshot" mapped to "kimi" while the registry makes it an alias of
//     "moonshotai" — and reasoning.go's own comment states that "moonshotai is
//     the OpenAI-wire Kimi; they are different providers". Two hand-maintained
//     tables, opposite answers for one string.
//
// TestLegacyModelAliasesAreDead in packages/agent/build is what keeps this
// honest; the check has to live there because provider cannot import the
// registry without a cycle.
var LegacyUserModelProviderAliases = map[string]string{
	"anthropic-messages": "anthropic",
	"moonshot-ai":        "kimi",
	"kimi-code":          "kimi",
	"deepseek-chat":      "deepseek",
	"deepseek-ai":        "deepseek",
}

// NormalizeUserModelProviderKey resolves a models.json provider key through the
// legacy-alias table, returning the key unchanged when it is not a legacy name.
func NormalizeUserModelProviderKey(key string) string {
	if to, ok := LegacyUserModelProviderAliases[key]; ok {
		return to
	}
	return key
}

// LoadUserModelsWithWarnings reads a models.json file, returning the
// models converted to the internal Model type plus
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
	seen := map[string]string{} // normalized provider/id -> the key that claimed it
	for _, providerName := range userModelsLoadOrder(file) {
		prov := file.Providers[providerName]
		if providerName == "" {
			warnings = append(warnings, "models.json: empty provider key skipped")
			continue
		}
		// Normalize legacy transport aliases to their provider names.
		normalized := NormalizeUserModelProviderKey(providerName)

		for i, um := range prov.Models {
			if um.ID == "" {
				warnings = append(warnings, fmt.Sprintf("models.json: provider %q entry #%d has empty id; skipped", providerName, i))
				continue
			}
			if um.ContextWindow < 0 || um.DesiredContextWindow < 0 || um.MaxTokens < 0 {
				warnings = append(warnings, fmt.Sprintf("models.json: %s/%s has negative contextWindow/desiredContextWindow/maxTokens; clamped to 0", normalized, um.ID))
				if um.ContextWindow < 0 {
					um.ContextWindow = 0
				}
				if um.DesiredContextWindow < 0 {
					um.DesiredContextWindow = 0
				}
				if um.MaxTokens < 0 {
					um.MaxTokens = 0
				}
			}
			if um.Temperature != nil && (*um.Temperature < 0 || *um.Temperature > 2) {
				warnings = append(warnings, fmt.Sprintf("models.json: %s/%s temperature %g is out of range [0,2]; ignored", normalized, um.ID, *um.Temperature))
				um.Temperature = nil
			}
			caps, reasoningFromCaps, capWarnings := userCaps(um)
			for _, w := range capWarnings {
				warnings = append(warnings, fmt.Sprintf("models.json: %s/%s: %s", normalized, um.ID, w))
			}
			// A hand-written name reaches the status bar as raw text, so it
			// goes through the same door the editor writes through. Say so
			// when the file loses characters, or the operator sees a name
			// they didn't type and has nothing to go on.
			name := SanitizeDisplayName(um.Name)
			if name != um.Name {
				warnings = append(warnings, fmt.Sprintf("models.json: %s/%s name was adjusted to %q (control characters and line breaks are not renderable, and names are capped at %d characters)", normalized, um.ID, name, MaxDisplayNameRunes))
			}
			m := Model{
				Provider:             normalized,
				ID:                   um.ID,
				DisplayName:          name,
				DisplayNameSet:       name != "",
				ContextWindow:        um.ContextWindow,
				DesiredContextWindow: um.DesiredContextWindow,
				MaxOutput:            um.MaxTokens,
				PriceInput:           um.PriceInput,
				PriceOutput:          um.PriceOutput,
				PriceCacheRead:       um.PriceCacheRead,
				PriceCacheWrite:      um.PriceCacheWrite,
				PriceOutputImage:     um.PriceOutputImage,
				BaseURL:              um.BaseURL,
				Temperature:          um.Temperature,
				DefaultReasoning:     um.DefaultReasoning,
				Source:               "user",
				Caps:                 caps,
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
			key := normalized + "/" + um.ID
			if prev, dup := seen[key]; dup {
				warnings = append(warnings, fmt.Sprintf(
					"models.json: %s is configured under both %q and %q; the %q block wins. "+
						"Merge them — the editor only ever writes %q.",
					key, prev, providerName, providerName, normalized))
			}
			seen[key] = providerName
			out = append(out, UserOverride{Model: m, ReasoningSet: reasoningSet})
		}
	}
	return out, warnings
}

// userModelsLoadOrder returns f's provider keys in the order their overrides
// must be applied: legacy spellings first, canonical last, each group sorted.
//
// Map iteration used to decide it. A file holding both "kimi-code" and "kimi"
// blocks for one model id therefore resolved to whichever Go happened to visit
// last, so a field set in both flipped between runs of the same binary on the
// same file. Canonical goes last because that is the only spelling the editor
// writes, which makes "what the settings form saved" the one that wins.
func userModelsLoadOrder(f UserModelsFile) []string {
	var legacy, canonical []string
	for key := range f.Providers {
		if NormalizeUserModelProviderKey(key) != key {
			legacy = append(legacy, key)
			continue
		}
		canonical = append(canonical, key)
	}
	sort.Strings(legacy)
	sort.Strings(canonical)
	return append(legacy, canonical...)
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
//
// A non-empty DisplayName counts as explicitly set for the same reason,
// so a caller that spells one out gets it — the merge asks
// DisplayNameSet, which the JSON loader derives from the raw entry and a
// hand-built Model has no other way to assert.
func SetUserModels(models []Model) {
	overrides := make([]UserOverride, 0, len(models))
	for _, m := range models {
		if m.DisplayName != "" {
			m.DisplayNameSet = true
		}
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
		if um.PriceOutputImage > 0 {
			existing.PriceOutputImage = um.PriceOutputImage
		}
		// Scalar overrides (display name, base url, context window, max
		// tokens, temperature) merge through the shared registry, so adding a
		// scalar parameter needs no edit here — just a ScalarParam entry.
		for _, p := range scalarParams {
			p.Merge(&existing, um)
		}
		if o.ReasoningSet {
			existing.Reasoning = um.Reasoning
		}
		// Capability keys merge key-wise; the user's explicit
		// assertions win over catalog/live/extra. Key presence in the
		// models.json `capabilities` map IS the explicit-set marker,
		// so no ReasoningSet-style side flag is needed.
		existing.Caps = mergeCaps(existing.Caps, um.Caps)
		existing.Source = "user"
		existing.Speculative = false
		base[idx] = existing
	}
	return base
}
