package build

// Provider-scoped activation continuation
// (docs/proposals/activation-continuation.md, the per-provider stage).
//
// activation_continuation is an engine feature, so it is global by default.
// This scopes it: providers.<id>.activation_continuation overrides the global
// for sessions on that provider, and an unset provider inherits the global
// exactly as before.
//
// The override lands through the SAME core setter as the global
// (Agent.SetActivationContinuation). That is the design constraint and not an
// implementation detail. Core stays provider-ignorant, and there is still one
// flag for runLoop to snapshot once per Prompt, so the immediate-refresh and
// natural-stop-gate semantics still cannot mix inside one Prompt. A second
// flag threaded through the gate would break that.
//
// What the override buys, stated exactly, because the settings row and the
// documentation have to say the same thing. Activating a tool group
// invalidates the prompt prefix from the tools rung down, so the next dispatch
// re-prefills at full price. Continuation makes that dispatch happen
// immediately, because the gate fires at the model's natural stop. Off, the
// re-prefill waits for the user's next message, and a user who redirects
// instead of continuing never pays it on that prefix. The saving is at most
// one dispatch per activation.
//
// It is NOT a remedy for the sustained cache collapse. The corpus sweep on
// TKT-01M29HFZEX (n=56, section 15 of
// docs/reviews/2026-08-04-gpt56-post-compaction-cache-collapse.md) found that
// activate_tools is a marker and not the cause: 31 of 49 cliff opens have no
// activation to account for them, and 30 sessions on cache-write providers
// took 17 activations with zero cliffs. Anyone who turns this off expecting
// the floor to lift will be disappointed.

// ActivationContinuationFeatureID is the engine feature this scopes. Named
// rather than spelled out at each use, because a caller that has to special-case
// this one feature should be findable by a reference search.
const ActivationContinuationFeatureID = "activation_continuation"

// The keywords providers.<id>.activation_continuation accepts. Empty inherits
// the global, which is what an operator who never touched this has.
const (
	ActivationContinuationInherit = ""
	ActivationContinuationOn      = "on"
	ActivationContinuationOff     = "off"
)

// ActivationContinuationKeywords lists the accepted values in display order,
// for a settings row and for an error message that has to name them.
var ActivationContinuationKeywords = []string{
	ActivationContinuationInherit,
	ActivationContinuationOn,
	ActivationContinuationOff,
}

// ValidActivationContinuation reports whether s is a keyword this understands.
// A settings write validates against this rather than accepting free text, so
// a typo is refused at the boundary instead of silently inheriting.
func ValidActivationContinuation(s string) bool {
	for _, k := range ActivationContinuationKeywords {
		if s == k {
			return true
		}
	}
	return false
}

// ActivationContinuationOverride resolves one provider's keyword. ok is false
// when the provider does not override, and the caller must then leave whatever
// the global resolved to alone. An unrecognized word inherits, so a
// hand-edited config with a typo degrades to the default rather than to off.
func ActivationContinuationOverride(keyword string) (on bool, ok bool) {
	switch keyword {
	case ActivationContinuationOn:
		return true, true
	case ActivationContinuationOff:
		return false, true
	default:
		return false, false
	}
}

// ActivationContinuationFor resolves the effective value for a session on a
// provider: the provider's override if it has one, otherwise the global engine
// feature. Callers that already applied the global through the feature loop
// want ActivationContinuationOverride instead, so an unset provider is left
// untouched rather than re-derived.
func ActivationContinuationFor(overrides map[string]bool, keyword string) bool {
	if on, ok := ActivationContinuationOverride(keyword); ok {
		return on
	}
	f, ok := EngineFeatureByID(ActivationContinuationFeatureID)
	if !ok {
		return true
	}
	return EngineFeatureOn(overrides, f)
}

// ActivationContinuationEffective resolves this route's setting for
// ModelSwap.ActivationContinuation, which takes nil for "no opinion". It is
// never nil here: a resolved route always knows both the provider override and
// the global, so a swap onto a provider with no override restores the global
// rather than leaving the previous provider's answer standing.
func (r Resolved) ActivationContinuationEffective() *bool {
	on := ActivationContinuationFor(r.EngineFeatures, r.ActivationContinuation)
	return &on
}
