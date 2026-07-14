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
// This is a seam, not a platform: it holds the handful of loop behaviors whose
// value is genuinely still open, and correctness behaviors (the open-work and
// swarm-hold continuation gates) are NOT features — they have no off switch. A
// feature earns its toggle by having a real question attached to it; when the
// question is answered, the toggle should go away with it.

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
	{
		ID:    "cache_aware_compaction",
		Title: i18n.M("Cache-aware compaction"),
		Desc:  i18n.M("Summarize the conversation using the prompt the provider already has cached, instead of building a separate one. Measured at 99.5% cache-served on Anthropic — the bespoke summarizer re-reads the whole conversation at full price. Turn it off to go back to the dedicated summarizer, which gets a purpose-built prompt rather than writing from inside the agent's own persona."),
		// ON, to dogfood it. The cost side is measured and settled: 99.5%
		// cache-served on Anthropic, 84.9% on codex, zero tool_use fallbacks
		// (docs/plans/cache-aware-compaction-ab.md).
		//
		// What is NOT settled is summary QUALITY on a real transcript, and the
		// pre-registered rule said not to flip the default until it was. This
		// overrides that rule deliberately and with the reason on record: living
		// with the feature is how the quality evidence gets gathered, and the
		// alternative — a synthetic A/B nobody has time to run — leaves a 6x
		// saving unclaimed indefinitely. The rule still governs whether it STAYS
		// on; see the plan's decision criteria, which are unchanged.
		//
		// The risk being accepted, stated plainly: a MID-TURN compaction whose
		// summary omits an already-executed side effect makes the resuming agent
		// re-run it. That is what the cold path's dedicated summarizer prompt was
		// buying, and it is the one failure mode worth watching for.
		Default: true,
		Apply:   func(a *core.Agent, on bool) { a.SetCacheAwareCompaction(on) },
	},
	{
		ID:    "prefix_change_guard",
		Title: i18n.M("Offer to compact before a cache-invalidating change"),
		Desc:  i18n.M("When something changes the prompt the provider has cached — switching model, reloading an extension — the next message silently re-reads the whole conversation at full price. Ask first, and offer to condense it while the old prompt is still cached. Needs cache-aware compaction to be on: without it, compacting costs the same full-price read as sending, so there would be nothing to offer."),
		// Now that cache-aware compaction defaults on, this comes alive with it —
		// it was written to be inert without it, because the offer is only honest
		// when there is a saving behind it. Measured: an unguarded model switch on
		// a 66k transcript cost $0.42; guarded, $0.066.
		Default: true,
		Apply:   func(a *core.Agent, on bool) { a.SetPrefixChangeGuard(on) },
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
