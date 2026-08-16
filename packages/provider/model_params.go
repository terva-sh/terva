package provider

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
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
		Key: "name", Label: "display name", Kind: ScalarText,
		// The merged model can't report what a cleared override would fall
		// back to (the underlying layer's name is gone by the time we see
		// it), so a renamed model shows the id — the floor, and the exact
		// answer for the local models this override exists for.
		Default: func(m Model) string {
			if m.DisplayNameSet {
				return m.ID
			}
			return strOrDefault(m.DisplayName, m.ID)
		},
		Override:    func(um UserModel) string { return um.Name },
		SetOverride: func(um *UserModel, s string) error { um.Name = SanitizeDisplayName(s); return nil },
		// DisplayNameSet, not a src.DisplayName != src.ID heuristic. The
		// loader backfills DisplayName = ID when `name` is absent, so the
		// string comparison was the only way to ask "did the operator
		// actually write a name" — and it silently ignored a name that
		// equalled the id. The flag answers it directly.
		Merge: func(dst *Model, src Model) {
			if src.DisplayNameSet && src.DisplayName != "" {
				dst.DisplayName = src.DisplayName
				dst.DisplayNameSet = true
			}
		},
	},
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
	{
		Key: "defaultReasoning", Label: "default reasoning", Kind: ScalarText,
		Default:  func(m Model) string { return strOrDefault(m.DefaultReasoning, "off") },
		Override: func(um UserModel) string { return um.DefaultReasoning },
		SetOverride: func(um *UserModel, s string) error {
			v := strings.ToLower(strings.TrimSpace(s))
			switch v {
			case "", "off", "none", "minimum", "minimal", "low", "medium", "high", "maximum", "max":
				um.DefaultReasoning = v
				return nil
			default:
				return fmt.Errorf("invalid reasoning level %q (want off|minimum|low|medium|high|maximum|max)", s)
			}
		},
		Merge: func(dst *Model, src Model) {
			if src.DefaultReasoning != "" {
				dst.DefaultReasoning = src.DefaultReasoning
				// Marks this as the OPERATOR's choice, which outranks the
				// global level; a catalog value does not. See
				// Model.DefaultReasoningSet.
				dst.DefaultReasoningSet = true
			}
		},
	},
}

// ScalarParams returns the editor-managed scalar model parameters, in editor
// row order. The slice is shared; callers must not mutate it.
func ScalarParams() []ScalarParam { return scalarParams }

// MaxDisplayNameRunes bounds a models.json `name`. Not a layout decision —
// the render sites do their own width clamping — just a sanity ceiling so a
// pasted essay can't become a model's name.
const MaxDisplayNameRunes = 64

// SanitizeDisplayName makes an operator-supplied model name safe to print.
//
// Unlike every other scalar override, this one is rendered raw into a
// terminal status bar and picker rows, so an ESC in it is not a cosmetic
// problem: it repaints the frame. Escape sequences, C0/C1 controls, and DEL
// are dropped outright; the remaining whitespace is collapsed to single
// spaces so a multi-line paste becomes one line rather than a torn status
// bar. Applied at BOTH doors — the editor's SetOverride and the loader —
// because models.json is hand-edited at least as often as it is written.
func SanitizeDisplayName(s string) string {
	// Escape forms have to be told apart rather than closed on "the next
	// plausible terminator": '[' is itself in the CSI final-byte range, so a
	// single-state skip ends ESC[31m immediately and emits "31m".
	const (
		stNormal = iota
		stEscape // consumed ESC, the next byte says which form
		stCSI    // ESC [ — parameter/intermediate bytes, then one final byte
		stString // ESC ] P X ^ _ — runs until BEL or ST
		stStrEsc // inside a string escape, saw ESC (expecting the ST backslash)
	)
	var b strings.Builder
	b.Grow(len(s))
	state := stNormal
	for _, r := range s {
		switch state {
		case stEscape:
			switch r {
			case '[':
				state = stCSI
			case ']', 'P', 'X', '^', '_':
				state = stString
			default:
				state = stNormal // two-character escape; this WAS its final byte
			}
			continue
		case stCSI:
			if r < 0x20 || r > 0x3f { // anything else ends it, malformed or not
				state = stNormal
			}
			continue
		case stString:
			switch r {
			case 0x07:
				state = stNormal
			case 0x1b:
				state = stStrEsc
			}
			continue
		case stStrEsc:
			state = stNormal
			continue
		}
		switch {
		case r == 0x1b:
			state = stEscape
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == utf8.RuneError:
			// Controls become a space so "a\nb" reads as "a b", not "ab".
			// RuneError catches raw C1 bytes too: 0x9b on its own is invalid
			// UTF-8, so ranging yields the replacement rune, not 0x9b.
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if runes := []rune(out); len(runes) > MaxDisplayNameRunes {
		out = strings.TrimSpace(string(runes[:MaxDisplayNameRunes]))
	}
	return out
}

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
