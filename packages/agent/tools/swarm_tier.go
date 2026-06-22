package tools

import (
	"strings"

	"terva.sh/terva/packages/provider"
)

// swarmTierFamilies maps a provider to the model-family keyword for each
// tier (weak / medium / strong). Keywords are matched as case-insensitive
// substrings against the provider's catalog model ids, so the table
// survives version bumps (claude-opus-4-5 → 4-8) without edits. Only
// providers whose families are unambiguous substrings are listed; for
// every other provider tier resolution is a deliberate no-op and the
// sub-agent inherits the host model. (Anthropic only for now: haiku /
// sonnet / opus never contain one another, so matching is unambiguous —
// the property that makes substring matching safe. Gemini's flash /
// flash-lite overlap, so it is left out rather than risk a wrong pick.)
var swarmTierFamilies = map[string]map[string]string{
	"anthropic": {"weak": "haiku", "medium": "sonnet", "strong": "opus"},
}

// swarmTierRank orders the tiers so a request can be capped at the host
// model's own tier.
var swarmTierRank = map[string]int{"weak": 0, "medium": 1, "strong": 2}

var swarmRankName = []string{"weak", "medium", "strong"}

// SwarmTierMap is a user-configured per-provider tier override: providerID
// -> {"weak"/"medium"/"strong" -> concrete model id}. It composes over the
// built-in family table — an explicit entry wins, an unset tier falls back to
// the built-in guess. This is what lets a gateway with no built-in family
// table (opencode-go, litellm, openrouter) still answer `tier: weak` with a
// cheap model instead of the full host model. Built from Config.SwarmTiers.
type SwarmTierMap map[string]map[string]string

// ResolveSwarmTier maps a weak/medium/strong tier to a concrete model id for
// the host provider, capped so a sub-agent is never *stronger* than the host
// model — a weak host cannot spawn a strong child (the maki rule, to keep
// delegation cheap). overrides (may be nil) takes precedence over the built-in
// family guesses. Returns "" when the tier is empty/unknown, the provider has
// neither an override nor a built-in table, or nothing resolves; in every such
// case the caller falls back to the host model, so an unconfigured provider
// simply ignores the tier.
func ResolveSwarmTier(providerID, hostModel, tier string, overrides SwarmTierMap) string {
	want, ok := swarmTierRank[strings.ToLower(strings.TrimSpace(tier))]
	if !ok {
		return ""
	}
	// The provider must have SOME tier source — a user override or a built-in
	// family table — or tier resolution is a deliberate no-op.
	if len(overrides[providerID]) == 0 && swarmTierFamilies[providerID] == nil {
		return ""
	}
	// Cap at the host model's tier when we can identify it.
	if capRank, ok := swarmTierRankOf(providerID, hostModel, overrides); ok && want > capRank {
		want = capRank
	}
	return swarmModelForRank(providerID, want, overrides)
}

// SwarmRankNames returns the tier names in weak→strong order, for callers
// that render the tier ladder (e.g. `terva models tiers`).
func SwarmRankNames() []string { return append([]string(nil), swarmRankName...) }

// SwarmTierHasBuiltin reports whether a provider has a built-in family table
// (so an unconfigured tier still resolves without a user override).
func SwarmTierHasBuiltin(providerID string) bool {
	return swarmTierFamilies[providerID] != nil
}

// SwarmTierLadder reports the resolved weak/medium/strong model for a provider
// UNCAPPED (as if the host were the strongest tier), plus where each came from:
// "override", "built-in", or "" when nothing resolves. It's the read-only view
// behind `terva models tiers`; resolution order matches ResolveSwarmTier.
func SwarmTierLadder(providerID string, overrides SwarmTierMap) (models, sources [3]string) {
	for rank := range swarmRankName {
		name := swarmRankName[rank]
		if ov := overrides[providerID]; ov != nil {
			if m := strings.TrimSpace(ov[name]); m != "" {
				models[rank], sources[rank] = m, "override"
				continue
			}
		}
		if fams := swarmTierFamilies[providerID]; fams != nil {
			if kw := fams[name]; kw != "" {
				for _, mm := range provider.ModelsForProvider(providerID) {
					if strings.Contains(strings.ToLower(mm.ID), kw) {
						models[rank], sources[rank] = mm.ID, "built-in"
						break
					}
				}
			}
		}
	}
	return
}

// swarmModelForRank resolves one tier rank to a model id for the provider: a
// user override wins, else the built-in family keyword matched against the
// provider's catalog. "" when neither yields a model.
func swarmModelForRank(providerID string, rank int, overrides SwarmTierMap) string {
	name := swarmRankName[rank]
	if ov := overrides[providerID]; ov != nil {
		if m := strings.TrimSpace(ov[name]); m != "" {
			return m
		}
	}
	if fams := swarmTierFamilies[providerID]; fams != nil {
		if kw := fams[name]; kw != "" {
			for _, m := range provider.ModelsForProvider(providerID) {
				if strings.Contains(strings.ToLower(m.ID), kw) {
					return m.ID
				}
			}
		}
	}
	return ""
}

// swarmTierRankOf reports which tier the given model belongs to for its
// provider (and whether it could be identified at all): first by exact match
// against the user override's ids, then by the built-in family keywords.
func swarmTierRankOf(providerID, modelID string, overrides SwarmTierMap) (int, bool) {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return 0, false
	}
	if ov := overrides[providerID]; ov != nil {
		for rank, name := range swarmRankName {
			if m := strings.TrimSpace(ov[name]); m != "" && strings.ToLower(m) == id {
				return rank, true
			}
		}
	}
	if fams := swarmTierFamilies[providerID]; fams != nil {
		for rank, name := range swarmRankName {
			if kw := fams[name]; kw != "" && strings.Contains(id, kw) {
				return rank, true
			}
		}
	}
	return 0, false
}
