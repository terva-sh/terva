package provider

import (
	"fmt"
	"strconv"
)

// Scalar model parameters are the numeric/text per-model overrides that follow
// one shared convention: a zero/empty value means "inherit the catalog
// default, persist nothing". Declaring a parameter ONCE in the registry below
// drives three call sites from a single source of truth:
//
//   - the /model editor form (row, input filtering, range validation, save),
//   - the user-layer merge (applyUserOverrides),
//   - and, with one typed field on Model + UserModel, the loader.
//
// Adding a new scalar (top_p, top_k, …) is therefore one ScalarParam entry
// plus the typed field — not a hand-edit across editor, save, and merge. The
// tri-state capability/bool fields (reasoning, image-input) are a different
// shape and are handled separately.

// ScalarKind classifies an editor-managed scalar parameter.
type ScalarKind int

const (
	ScalarText  ScalarKind = iota // free string (base url)
	ScalarInt                     // non-negative integer (context window, max tokens)
	ScalarFloat                   // bounded float (temperature)
)

// ScalarParam declares one scalar model override end to end.
type ScalarParam struct {
	Key   string
	Label string
	Kind  ScalarKind
	Min   float64 // ScalarFloat: inclusive lower bound
	Max   float64 // ScalarFloat: inclusive upper bound

	// Default renders the catalog/live default shown as "inherit (...)" in
	// the editor. It may report a sentinel (e.g. "n/a") when the parameter
	// doesn't apply to m.
	Default func(m Model) string
	// Override renders the user's current override as a string ("" = inherit).
	Override func(um UserModel) string
	// SetOverride parses an already-trimmed editor value ("" clears the
	// override) and writes it onto um, returning a validation error for an
	// out-of-range or malformed value. It is the single validation authority.
	SetOverride func(um *UserModel, s string) error
	// Merge copies a SET override (Model->Model) onto dst during the user
	// layer merge; a no-op when the user left the parameter unset. src is the
	// override entry already converted to a Model by the loader.
	Merge func(dst *Model, src Model)
}

var scalarParams = []ScalarParam{
	{
		Key: "baseUrl", Label: "base url", Kind: ScalarText,
		Default:     func(m Model) string { return strOrDefault(m.BaseURL, "provider default") },
		Override:    func(um UserModel) string { return um.BaseURL },
		SetOverride: func(um *UserModel, s string) error { um.BaseURL = s; return nil },
		Merge: func(dst *Model, src Model) {
			if src.BaseURL != "" {
				dst.BaseURL = src.BaseURL
			}
		},
	},
	{
		Key: "contextWindow", Label: "context window", Kind: ScalarInt,
		Default:     func(m Model) string { return posIntStr(m.ContextWindow) },
		Override:    func(um UserModel) string { return posIntStr(um.ContextWindow) },
		SetOverride: func(um *UserModel, s string) error { return setNonNegInt(&um.ContextWindow, s) },
		Merge: func(dst *Model, src Model) {
			if src.ContextWindow > 0 {
				dst.ContextWindow = src.ContextWindow
			}
		},
	},
	{
		Key: "desiredContextWindow", Label: "desired context window", Kind: ScalarInt,
		Default:     func(m Model) string { return posIntStr(m.DesiredContextWindow) },
		Override:    func(um UserModel) string { return posIntStr(um.DesiredContextWindow) },
		SetOverride: func(um *UserModel, s string) error { return setNonNegInt(&um.DesiredContextWindow, s) },
		Merge: func(dst *Model, src Model) {
			if src.DesiredContextWindow > 0 {
				dst.DesiredContextWindow = src.DesiredContextWindow
			}
		},
	},
	{
		Key: "maxTokens", Label: "max tokens", Kind: ScalarInt,
		Default:     func(m Model) string { return posIntStr(m.MaxOutput) },
		Override:    func(um UserModel) string { return posIntStr(um.MaxTokens) },
		SetOverride: func(um *UserModel, s string) error { return setNonNegInt(&um.MaxTokens, s) },
		Merge: func(dst *Model, src Model) {
			if src.MaxOutput > 0 {
				dst.MaxOutput = src.MaxOutput
			}
		},
	},
	{
		Key: "temperature", Label: "temperature", Kind: ScalarFloat, Min: 0, Max: 2,
		Default: func(m Model) string {
			if m.AdaptiveThinking {
				return "n/a (adaptive thinking)"
			}
			return floatStr(m.Temperature)
		},
		Override: func(um UserModel) string { return floatStr(um.Temperature) },
		SetOverride: func(um *UserModel, s string) error {
			if s == "" {
				um.Temperature = nil
				return nil
			}
			f, err := strconv.ParseFloat(s, 32)
			if err != nil {
				return fmt.Errorf("enter a number between 0 and 2 (blank = inherit)")
			}
			if f < 0 || f > 2 {
				return fmt.Errorf("temperature must be between 0 and 2")
			}
			v := float32(f)
			um.Temperature = &v
			return nil
		},
		Merge: func(dst *Model, src Model) {
			if src.Temperature != nil {
				dst.Temperature = src.Temperature
			}
		},
	},
}

// ScalarParams returns the editor-managed scalar model parameters, in editor
// row order. The slice is shared; callers must not mutate it.
func ScalarParams() []ScalarParam { return scalarParams }

func strOrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func posIntStr(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func floatStr(p *float32) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(float64(*p), 'g', -1, 32)
}

// setNonNegInt parses a non-negative integer ("" clears to 0) into dst.
func setNonNegInt(dst *int, s string) error {
	if s == "" {
		*dst = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return fmt.Errorf("enter a non-negative whole number (blank = inherit)")
	}
	*dst = n
	return nil
}
