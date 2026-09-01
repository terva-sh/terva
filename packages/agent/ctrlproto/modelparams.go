package ctrlproto

import "context"

// Per-model settings — context window, max tokens, temperature — the overrides
// that live in models.json.
//
// These existed only in the TUI. packages/provider already describes them
// properly (provider.ScalarParams: key, label, kind, bounds, how to read a
// default and how to write an override), but the ONLY caller was a terminal
// dialog, so the web could not touch a single one of them. This is that
// descriptor, on the wire.
//
// Same discipline as the auth flow-step: the daemon describes, the client
// renders. Nothing here names contextWindow or maxTokens, so a provider that
// gains a new knob costs a daemon change and nothing in either frontend.

// ModelParamsController is served by a daemon that can edit a model's overrides.
// Optional, like AuthController: a carrier that cannot write models.json simply
// does not implement it, and the method answers "unsupported" rather than
// failing somewhere deeper.
type ModelParamsController interface {
	// ModelParams describes one model's editable settings, with the default it
	// would take and the override (if any) currently pinned in models.json.
	ModelParams(ctx context.Context, p ModelParamsParams) (ModelParamsView, error)
	// ModelParamsSet writes overrides. An empty value CLEARS that override, which
	// is why values arrive as strings: "" and "0" are different answers, and a
	// typed zero could not tell them apart.
	ModelParamsSet(ctx context.Context, p ModelParamsSetParams) error
	// ModelParamsReset removes the model's models.json entry outright.
	ModelParamsReset(ctx context.Context, p ModelParamsParams) error
}

// ModelParamsParams names a model. Provider is required: the same id can exist
// under an api-key provider and a subscription one, and editing "the other one"
// silently is the bug SwitchModel's provider-qualification already guards.
type ModelParamsParams struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ModelParamsSetParams carries the whole form, not one field.
//
// A blank value clears that override, so a partial map would be ambiguous:
// "absent" and "cleared" would look the same, and a client that omitted an
// untouched field would silently wipe it. Send every field the descriptor listed.
type ModelParamsSetParams struct {
	Provider string            `json:"provider"`
	Model    string            `json:"model"`
	Values   map[string]string `json:"values"`
}

// ModelParamsView is what a client renders.
type ModelParamsView struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// HasOverride is whether models.json already holds an entry for this model —
	// i.e. whether "reset to defaults" would actually do anything.
	HasOverride bool             `json:"has_override,omitempty"`
	Params      []ModelParamSpec `json:"params"`
}

// ModelParamSpec is one editable setting.
type ModelParamSpec struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Kind is a rendering and validation HINT: text | int | float | tristate |
	// enum.
	// Everything crosses as a string and the DAEMON parses — the same rule the
	// auth fields follow, and for the same reason: bounds are checked in
	// packages/provider already, and a second opinion on the wire is a second
	// thing to disagree.
	Kind string `json:"kind"`
	// Default is what this model takes with no override — rendered, not enforced.
	// A client shows it as placeholder text so an empty box reads as "inherit"
	// rather than as "zero".
	Default string `json:"default,omitempty"`
	// Value is the override currently pinned in models.json, or "" for none.
	Value string `json:"value,omitempty"`
	// Options are the values an "enum" param accepts FOR THIS MODEL, in
	// display order; absent for every other kind. A client renders a picker
	// over exactly these and never a free-text box: the set can be narrower on
	// one model than another (the thinking ladder collapses rungs that reach a
	// given model as one wire value), so a fixed client-side list would offer
	// levels this model cannot tell apart.
	Options []string `json:"options,omitempty"`
	// Min/Max bound a float param (temperature). Zero means unbounded.
	Min float64 `json:"min,omitempty"`
	Max float64 `json:"max,omitempty"`
	// Help explains a setting whose name does not. desiredContextWindow is the
	// one that actually needs it: it is not the model's ceiling but the working
	// window that drives auto-condensing.
	Help string `json:"help,omitempty"`
}
