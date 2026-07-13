package build

import (
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// Engine features: runtime-toggleable agent-loop behaviors
// (docs/proposals/activation-continuation.md stage 3). A feature is a
// declaration — id, display strings, default, and how it lands on a live
// agent — projected into the workspace settings surface (one SettingItem per
// feature, so the web panel, the TUI /settings dialog, and any ctrlproto
// client get the toggle for free) and applied at the NewAgent funnel every
// host passes through. Persisted overrides live in config
// `engine_features` keyed by id; absent ids use the default, which keeps a
// hand-edited config for headless runs one line.
//
// This is a seam, not a platform: it ships with exactly one feature, and
// correctness behaviors (the open-work and swarm-hold continuation gates) are
// NOT features — they have no off switch.

// EngineFeature is one toggleable loop behavior. Title and Desc are declared
// with i18n.M (init time, before i18n.Configure) and translated at render.
type EngineFeature struct {
	ID      string
	Title   string
	Desc    string
	Default bool
	// RequiresLazyTools hides the toggle while lazy tools are off — for a
	// feature that is meaningless without them (the settings pane nests it
	// under the lazy_tools toggle the way auto_swarm_nudge nests under
	// auto_swarm). It gates only display: Apply still runs at build, where
	// the feature's own semantics decide whether it matters.
	RequiresLazyTools bool
	Apply             func(a *core.Agent, on bool)
}

// EngineFeatures lists every engine feature, in display order.
var EngineFeatures = []EngineFeature{
	{
		ID:                "activation_continuation",
		Title:             i18n.M("Activation continuation"),
		Desc:              i18n.M("When the agent activates a tool group and finishes its reply, automatically continue it with those tools live instead of waiting for your next message."),
		Default:           true,
		RequiresLazyTools: true,
		Apply:             func(a *core.Agent, on bool) { a.SetActivationContinuation(on) },
	},
}

// EngineFeatureByID resolves a feature by its id (the settings-action lookup).
func EngineFeatureByID(id string) (EngineFeature, bool) {
	for _, f := range EngineFeatures {
		if f.ID == id {
			return f, true
		}
	}
	return EngineFeature{}, false
}

// EngineFeatureOn resolves a feature's on/off from the persisted overrides
// (config `engine_features`), falling back to its default.
func EngineFeatureOn(overrides map[string]bool, f EngineFeature) bool {
	if v, ok := overrides[f.ID]; ok {
		return v
	}
	return f.Default
}
