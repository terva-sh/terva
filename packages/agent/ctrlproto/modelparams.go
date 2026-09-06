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
	//
	// For a model with a catalog row underneath, that restores the shipped
	// values. For one that exists only because of the entry, it removes the model.
	// The view's Custom field is what lets a client say which it is about to do.
	ModelParamsReset(ctx context.Context, p ModelParamsParams) error
	// ModelAdd creates a models.json entry for a model no lower layer knows about.
	//
	// The inverse of ModelParamsSet's guard: that method refuses an id it cannot
	// resolve, and this one refuses an id it can. A single method with a create
	// flag would have to switch off the check that stops an edit landing on the
	// wrong provider's copy of a shared id, so the two stay apart.
	ModelAdd(ctx context.Context, p ModelAddParams) error
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

// ModelAddParams names the model to create and carries its whole form, under
// the same whole-form rule as ModelParamsSetParams.
//
// There is no clone_from field, though the interface that drives this is a
// clone. The client seeds its form from another model's ModelParamsView and
// sends the resulting values, so naming the source here would add a second
// account of the same thing that the daemon could only disagree with.
//
// The client seeds from each spec's Default, not its Value. Value is the
// models.json pin and is empty for any source without an entry, which is most
// of them, so seeding from it would create a model with no context window.
type ModelAddParams struct {
	// Provider must be one a user can actually reach. An entry under a provider
	// with no credentials writes correctly and then never appears, because both
	// pickers filter on reachability, and that reads as a failed save.
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
	HasOverride bool `json:"has_override,omitempty"`
	// Custom is whether the model exists ONLY because of that entry: no catalog
	// row, no discovery, nothing underneath it. Mirrors ModelInfo.custom.
	//
	// It changes what the reset control MEANS, which is why it rides the view
	// rather than being looked up by the client. With a row underneath, dropping
	// the entry restores the shipped values. With nothing underneath, the entry
	// IS the model, so the same button removes it from the picker and there are
	// no defaults to fall back to. A client that renders "reset to defaults" over
	// the second case is offering something that cannot happen.
	Custom bool             `json:"custom,omitempty"`
	Params []ModelParamSpec `json:"params"`
}

// ModelParamSpec is one editable setting.
type ModelParamSpec struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Kind is a rendering and validation HINT: text | int | float | tristate |
	// enum | list.
	// Everything crosses as a string and the DAEMON parses — the same rule the
	// auth fields follow, and for the same reason: bounds are checked in
	// packages/provider already, and a second opinion on the wire is a second
	// thing to disagree.
	//
	// A tristate is a capability: "on", "off", or "" for inherit. The third
	// state is not decoration. "Off" is the operator overruling what terva
	// believes about the model, and it outranks whatever discovery finds later;
	// "" leaves the catalog and the live layer deciding. A client that renders
	// a checkbox instead of three states turns every model an operator has
	// opened into one they have pinned.
	//
	// A list is a set of values, and it crosses as one comma-separated string
	// so that this stays true of every kind: the daemon parses, and "" clears.
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
	// FreeValues says Options are SUGGESTIONS and a value outside them is still
	// valid, so a client offers them and keeps a way to type something else.
	// Only a list uses it today: terva passes a reasoning effort it does not
	// recognize straight through, because an unknown effort is the server's own
	// word, and a closed picker would put that behaviour out of reach.
	FreeValues bool `json:"free_values,omitempty"`
	// Min/Max bound a float param (temperature). Zero means unbounded.
	Min float64 `json:"min,omitempty"`
	Max float64 `json:"max,omitempty"`
	// Help explains a setting whose name does not. desiredContextWindow is the
	// one that actually needs it: it is not the model's ceiling but the working
	// window that drives auto-condensing.
	Help string `json:"help,omitempty"`
}
