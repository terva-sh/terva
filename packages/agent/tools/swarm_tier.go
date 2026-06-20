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

// ResolveSwarmTier maps a weak/medium/strong tier to a concrete model id
// for the host provider, capped so a sub-agent is never *stronger* than
// the host model — a weak host cannot spawn a strong child (the maki
// rule, to keep delegation cheap). Returns "" when the tier is empty or
// unknown, the provider has no family table, or no catalog model matches;
// in every such case the caller falls back to the host model, so an
// unsupported provider simply ignores the tier.
func ResolveSwarmTier(providerID, hostModel, tier string) string {
	want, ok := swarmTierRank[strings.ToLower(strings.TrimSpace(tier))]
	if !ok {
		return ""
	}
	fams := swarmTierFamilies[providerID]
	if fams == nil {
		return ""
	}
	// Cap at the host model's tier when we can identify it.
	if capRank, ok := swarmTierRankOf(providerID, hostModel); ok && want > capRank {
		want = capRank
	}
	keyword := fams[swarmRankName[want]]
	if keyword == "" {
		return ""
	}
	for _, m := range provider.ModelsForProvider(providerID) {
		if strings.Contains(strings.ToLower(m.ID), keyword) {
			return m.ID
		}
	}
	return ""
}

// swarmTierRankOf reports which tier the given model belongs to for its
// provider (and whether it could be identified at all), by matching the
// provider's family keywords against the model id.
func swarmTierRankOf(providerID, modelID string) (int, bool) {
	fams := swarmTierFamilies[providerID]
	if fams == nil {
		return 0, false
	}
	id := strings.ToLower(modelID)
	for rank, name := range swarmRankName {
		if kw := fams[name]; kw != "" && strings.Contains(id, kw) {
			return rank, true
		}
	}
	return 0, false
}
