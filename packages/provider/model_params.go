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
// Adding a new parameter (top_p, top_k, …) is therefore one ModelParam entry
// plus the typed field — not a hand-edit across editor, save, and merge.
//
// The registry carries three shapes, because a models.json field has three.
// The capability tri-states used to be the exception: declared nowhere,
// hand-written as ten lines in the TUI dialog, and absent from the web
// entirely, which is how an operator ended up editing models.json by hand to
// tell terva that a local model can think. `reasoningEfforts` was worse — its
// merge was a hand-written line whose own comment said a list needed one
// BADLY. A shape the registry cannot express is a shape one frontend gets and
// the other does not.

// ParamKind classifies an editor-managed scalar parameter.
type ParamKind int

const (
	ParamText     ParamKind = iota // free string (base url)
	ParamInt                       // non-negative integer (context window, max tokens)
	ParamFloat                     // bounded float (temperature)
	ParamEnum                      // closed set of values, per model (default thinking)
	ParamTriState                  // inherit / on / off (a capability)
	ParamList                      // a set of values (accepted reasoning efforts)
)

// ModelParam declares one scalar model override end to end.
type ModelParam struct {
	Key   string
	Label string
	Kind  ParamKind
	Min   float64 // ParamFloat: inclusive lower bound
	Max   float64 // ParamFloat: inclusive upper bound

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

	// Options lists the values a ParamEnum or ParamList offers FOR THIS MODEL,
	// in display order; nil for every other kind. Per-model because a closed set
	// can be narrower on one model than another — the thinking ladder collapses
	// rungs that reach a given model as the same wire value, and offering both
	// halves asks the user to choose between two spellings of one thing.
	//
	// An empty result means the parameter does not apply to m at all, and a
	// surface should omit the row rather than show an empty picker.
	Options func(m Model) []string

	// FreeValues says Options are SUGGESTIONS rather than the whole set, so a
	// surface offers them and still accepts a value the operator types.
	//
	// It exists for one measured behaviour: clampEffortToDeclared passes an
	// effort it does not recognize through untouched, on the reasoning that an
	// unknown effort is a server's own extension and terva has no standing to
	// move it. A closed picker over terva's scale would make that unreachable
	// from either frontend, so the escape hatch in the mapper would exist only
	// for people who hand-edit the file.
	FreeValues bool

	// InheritedFrom renders the "inherit (...)" hint for a parameter whose
	// inherited value comes from a precedence CHAIN rather than off the model,
	// so it needs the host's global setting — which Default, taking only a
	// Model, cannot see. When nil, Default is the hint.
	//
	// It exists because a hint that names the wrong value is worse than none:
	// a thinking row reading "inherit (off)" while a global level is quietly
	// deciding the turn is the exact failure ResolveReasoning documents.
	InheritedFrom func(m Model, global string) string
}

var modelParams = []ModelParam{
	{
		Key: "name", Label: "display name", Kind: ParamText,
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
		Key: "baseUrl", Label: "base url", Kind: ParamText,
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
		Key: "contextWindow", Label: "context window", Kind: ParamInt,
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
		Key: "desiredContextWindow", Label: "desired context window", Kind: ParamInt,
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
		Key: "maxTokens", Label: "max tokens", Kind: ParamInt,
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
		Key: "temperature", Label: "temperature", Kind: ParamFloat, Min: 0, Max: 2,
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
		// "thinking" facing the user, `defaultReasoning` in models.json and on
		// the wire: the house rule is jargon inside, plain language out.
		Key: "defaultReasoning", Label: "default thinking", Kind: ParamEnum,
		Default: func(m Model) string { return strOrDefault(m.DefaultReasoning, "off") },
		Options: ThinkingOptions,
		InheritedFrom: func(m Model, global string) string {
			// Cleared, so the hint cannot quote the very value the row is
			// offering to replace.
			m.DefaultReasoningSet = false
			if lv, _ := ResolveReasoning("", m, global); lv != "" {
				return lv
			}
			return "off"
		},
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
	{
		// Whether the model thinks AT ALL, which is a different question from
		// how hard: this one decides whether a request carries a reasoning
		// field, and `defaultReasoning` decides what it says.
		//
		// It is the field a local endpoint most often gets wrong. Discovery
		// reads /v1/models, which says nothing about thinking, so a server that
		// reasons arrives with this off. Every reasoning surface then goes
		// quiet at once: ReasoningLadderFor returns nil, so the picker offers
		// seven rungs that all read "this model takes no thinking setting",
		// and openaiClient.buildRequest gates the whole reasoning_effort block
		// on it, so the request carries nothing and the server runs at its own
		// default on every turn. The picker and the wire agree, and both are
		// useless to the operator until this flips.
		Key: "reasoning", Label: "thinking", Kind: ParamTriState,
		Default:     func(m Model) string { return onOffStr(m.Has(CapReasoning)) },
		Override:    func(um UserModel) string { return triStateStr(um.Reasoning) },
		SetOverride: func(um *UserModel, s string) error { return setTriState(&um.Reasoning, s) },
		Merge:       mergeCapParam(CapReasoning),
	},
	{
		Key: "imageInput", Label: "image input", Kind: ParamTriState,
		Default:  func(m Model) string { return onOffStr(m.Has(CapImageInput)) },
		Override: func(um UserModel) string { return capTriStateStr(um.Capabilities, "image-input") },
		SetOverride: func(um *UserModel, s string) error {
			return setCapTriState(um, "image-input", s)
		},
		Merge: mergeCapParam(CapImageInput),
	},
	{
		// Which reasoning_effort values this backend accepts. Declaring them
		// removes a guess rather than adding a preference: with no declaration
		// the blind mapper clamps both top rungs to "high", because an unknown
		// server might reject "xhigh" and a wrong guess is an HTTP 400 on every
		// turn. On a server that declares {none, low, medium, xhigh} that
		// pre-clamp lands "maximum" on medium, the cheaper neighbour of a rung
		// the model never had, while the xhigh it does accept sits unused.
		Key: "reasoningEfforts", Label: "accepted efforts", Kind: ParamList,
		Options: func(m Model) []string {
			// openAICompatEffort is the only reader, so the row would be a
			// control that cannot act on any other wire. An empty result omits
			// it, which is the same contract the enum rows use.
			if reasoningWireFamily(m.Provider) != reasoningWireOpenAICompat {
				return nil
			}
			return append([]string(nil), reasoningEffortScale...)
		},
		FreeValues: true,
		Default: func(m Model) string {
			if len(m.ReasoningEfforts) == 0 {
				return "undeclared"
			}
			return strings.Join(m.ReasoningEfforts, ", ")
		},
		Override:    func(um UserModel) string { return strings.Join(um.ReasoningEfforts, ", ") },
		SetOverride: func(um *UserModel, s string) error { um.ReasoningEfforts = parseList(s); return nil },
		Merge: func(dst *Model, src Model) {
			if len(src.ReasoningEfforts) > 0 {
				dst.ReasoningEfforts = src.ReasoningEfforts
			}
		},
	},
}

// ModelParams returns the editor-managed scalar model parameters, in editor
// row order. The slice is shared; callers must not mutate it.
func ModelParams() []ModelParam { return modelParams }

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

// onOffStr renders a capability's inherited value for the "inherit (...)"
// hint.
func onOffStr(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// triStateStr renders a capability override: "" when the entry does not
// mention the capability, else on or off.
//
// The three states are not two. "Absent" means the catalog or the live layer
// decides and keeps deciding as terva learns more; "off" is the operator
// saying this model does not do that, and it outranks anything discovery
// finds later. Collapsing them would turn every model an operator has ever
// opened in the editor into one they have pinned.
func triStateStr(p *bool) string {
	if p == nil {
		return ""
	}
	return onOffStr(*p)
}

// setTriState parses an editor value onto a tri-state override.
func setTriState(dst **bool, s string) error {
	v, err := parseTriState(s)
	if err != nil {
		return err
	}
	*dst = v
	return nil
}

func parseTriState(s string) (*bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "inherit":
		return nil, nil
	case "on", "true", "yes":
		v := true
		return &v, nil
	case "off", "false", "no":
		v := false
		return &v, nil
	default:
		return nil, fmt.Errorf("enter on or off (blank = inherit)")
	}
}

// capTriStateStr reads a capability override out of a models.json
// `capabilities` map, where key PRESENCE is the override marker.
func capTriStateStr(caps map[string]bool, key string) string {
	v, ok := caps[key]
	if !ok {
		return ""
	}
	return onOffStr(v)
}

// setCapTriState writes one capability override, deleting the key when the
// value clears. An emptied map goes back to nil so a cleared entry does not
// persist an empty `capabilities` object.
func setCapTriState(um *UserModel, key, s string) error {
	v, err := parseTriState(s)
	if err != nil {
		return err
	}
	if v == nil {
		delete(um.Capabilities, key)
		if len(um.Capabilities) == 0 {
			um.Capabilities = nil
		}
		return nil
	}
	if um.Capabilities == nil {
		um.Capabilities = map[string]bool{}
	}
	um.Capabilities[key] = *v
	return nil
}

// mergeCapParam is the Merge for a capability tri-state: the key is carried
// only when the operator's entry mentions it.
//
// It reads Caps rather than a side flag because the map already draws the
// distinction a tri-state needs — key presence IS "the operator said so" — and
// a second signal is a second thing to disagree. applyUserOverrides folds the
// legacy top-level `reasoning` spelling into the same map before the merge
// runs, so both spellings arrive here as one fact.
//
// CapReasoning also writes the legacy Model.Reasoning bool, because that field
// is what the request builders gate on and Has() only prefers the map.
func mergeCapParam(c Capability) func(dst *Model, src Model) {
	return func(dst *Model, src Model) {
		v, ok := src.Caps[c]
		if !ok {
			return
		}
		dst.Caps = mergeCaps(dst.Caps, map[Capability]bool{c: v})
		if c == CapReasoning {
			dst.Reasoning = v
		}
	}
}

// parseList splits an editor value into a set, lowercased and de-duplicated,
// keeping the operator's order. "" clears the override.
//
// Commas and spaces both separate, because a list typed into a text box
// arrives both ways and neither is wrong.
func parseList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	var out []string
	seen := map[string]bool{}
	for _, f := range fields {
		v := strings.ToLower(strings.TrimSpace(f))
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
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

// ThinkingOptions is the per-model thinking ladder a user may pick from,
// lowest rung first, and empty for a model that takes no thinking control at
// all. Only canonical rungs: ReasoningLadderFor marks a rung that reaches THIS
// model as the same wire value as another, and offering both would be two
// names for one choice.
func ThinkingOptions(m Model) []string {
	var out []string
	for _, r := range ReasoningLadderFor(m) {
		if r.SameAs == "" {
			out = append(out, r.Level)
		}
	}
	return out
}
