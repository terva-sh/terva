package tools

import (
	"strings"

	"terva.sh/terva/packages/provider"
)

// tierFamily says how to find ONE rung of a provider's ladder in that
// provider's catalog: the first `match` substring that hits a model wins
// (case-insensitive), and a model containing any `unless` substring is
// never a hit.
//
// `unless` is what makes overlapping family names usable. Gemini's
// flash-lite CONTAINS flash, so a bare "flash" keyword would resolve the
// medium rung to a weak model — which is why google sat out of this table
// entirely until now. Excluding the narrower name from the wider one says
// the thing the old comment could only warn about.
//
// A multi-entry `match` is a PREFERENCE, not a requirement: it exists for
// providers whose ids carry no stable family word at all (openai-codex's
// sol/terra/luna), where the only way to name a rung is to name the model.
// Listing generations newest-first buys the good pick today and degrades to
// the older id rather than to nothing when a catalog drops one. It does mean
// those entries want a touch when a new generation lands; the census below
// fails when a listed rung stops resolving entirely, which is the part that
// can be checked mechanically.
type tierFamily struct {
	match  []string
	unless []string
}

// swarmTierFamilies maps a provider to the family rule for each tier
// (weak / medium / strong). Matching is by substring against the provider's
// catalog ids, so a rule survives version bumps (claude-opus-4-5 → 4-8)
// without edits.
//
// Only providers whose rungs resolve UNAMBIGUOUSLY are listed — no catalog
// model may match two rungs, and the rungs must come out in price order.
// TestSwarmTierFamiliesAreUnambiguous checks both against the live catalog
// rather than against a list of expectations, so adding a provider here is
// how you find out whether it belongs. For every provider NOT listed, tier
// resolution is a deliberate no-op and the sub-agent inherits the host model;
// a gateway is expected to answer with a user override (SwarmTierMap).
//
// A partial ladder is allowed and useful: it still gives a swarm spawn a
// cheap model for `tier: weak`. It does NOT satisfy raati rigor level 1,
// which wants a real weak/medium/strong spread — two rungs is not a ladder.
var swarmTierFamilies = map[string]map[string]tierFamily{
	// haiku / sonnet / opus never contain one another — the property that
	// made substring matching safe in the first place.
	"anthropic": {
		"weak":   {match: []string{"haiku"}},
		"medium": {match: []string{"sonnet"}},
		"strong": {match: []string{"opus"}},
	},
	// Copilot serves the same three Anthropic families under its own ids.
	"github-copilot": {
		"weak":   {match: []string{"haiku"}},
		"medium": {match: []string{"sonnet"}},
		"strong": {match: []string{"opus"}},
	},
	"google": {
		"weak":   {match: []string{"flash-lite"}},
		"medium": {match: []string{"flash"}, unless: []string{"flash-lite"}},
		"strong": {match: []string{"pro"}},
	},
	// "gpt-5" is a substring of every mini/nano/chat/codex/pro variant, so
	// the strong rung is defined by what it is NOT as much as what it is.
	"openai": {
		"weak":   {match: []string{"nano"}},
		"medium": {match: []string{"mini"}, unless: []string{"nano"}},
		"strong": {match: []string{"gpt-5.5", "gpt-5.4", "gpt-5"}, unless: []string{"mini", "nano", "chat", "codex", "pro", "turbo", "research"}},
	},
	"openai-responses": {
		"weak":   {match: []string{"nano"}},
		"medium": {match: []string{"mini"}, unless: []string{"nano"}},
		"strong": {match: []string{"gpt-5.5", "gpt-5.4", "gpt-5"}, unless: []string{"mini", "nano", "chat", "codex", "pro", "turbo", "research"}},
	},
	// Codex names its generations sol / terra / luna, which say nothing
	// about capability and do not recur across generations. Named models,
	// newest first — see tierFamily on why that is the honest encoding here.
	"openai-codex": {
		"weak":   {match: []string{"mini"}},
		"medium": {match: []string{"gpt-5.6-terra", "gpt-5.4"}, unless: []string{"mini"}},
		"strong": {match: []string{"gpt-5.6-sol", "gpt-5.5"}, unless: []string{"mini"}},
	},
	// Two rungs each: neither vendor ships a middle model to point at, and
	// inventing one would be a guess wearing a ladder's clothes.
	"kimi": {
		"weak":   {match: []string{"kimi-k2", "kimi-for-coding"}},
		"strong": {match: []string{"k3"}},
	},
	"deepseek": {
		"weak":   {match: []string{"flash"}},
		"strong": {match: []string{"pro"}},
	},
}

// matches reports whether a model id belongs to this rung, and is the ONE
// place the match/unless semantics live — resolution and the census must
// not each have their own copy of the rule.
func (f tierFamily) matches(id string) bool {
	lower := strings.ToLower(id)
	for _, no := range f.unless {
		if strings.Contains(lower, strings.ToLower(no)) {
			return false
		}
	}
	for _, yes := range f.match {
		if strings.Contains(lower, strings.ToLower(yes)) {
			return true
		}
	}
	return false
}

// resolve returns the first catalog model of the provider that belongs to
// this rung, honoring the match list's preference order: every model is
// tried against the first keyword before the second is considered, so a
// newest-first pin list picks the newest present rather than whatever the
// catalog happens to list first.
//
// Speculative models are skipped. A resolved rung is DISPATCHED — a swarm
// spawn, a raati seat — and a model the vendor has not switched on yet 404s,
// which is a failed convening rather than a graceful degrade. Adding a
// newest-first pin list is what made this reachable: catalog order happened
// to land on live models, preference order deliberately reaches past them.
// An operator who wants tomorrow's model today can still pin it by id in
// swarm_tiers, which is their call to make and not a default's.
func (f tierFamily) resolve(providerID string) string {
	models := provider.ModelsForProvider(providerID)
	for _, yes := range f.match {
		one := tierFamily{match: []string{yes}, unless: f.unless}
		for _, m := range models {
			if !m.Speculative && one.matches(m.ID) {
				return m.ID
			}
		}
	}
	return ""
}

// swarmTierRank orders the tiers so a request can be capped at the host
// model's own tier.
var swarmTierRank = map[string]int{"weak": 0, "medium": 1, "strong": 2}

var swarmRankName = []string{"weak", "medium", "strong"}

// TierPick is one resolved rung: which model, and how hard it thinks.
//
// A rung used to be a model id, which quietly asserted that strength is only
// ever a matter of WHICH model. For a provider with one good model and no
// cheap sibling that made a ladder impossible — three rungs on one id resolve
// to three identical children. Reasoning is the second axis: same weights,
// different amounts of compute, different money.
//
// Reasoning is empty for every built-in rung. terva names model FAMILIES it
// can recognise; it does not guess how hard someone wants their sub-agents to
// think, and an empty effort leaves that to the child exactly as before.
type TierPick struct {
	Model     string
	Reasoning string
}

func (p TierPick) IsZero() bool { return p.Model == "" && p.Reasoning == "" }

// Label renders a pick for a human: "model" or "model (thinking: high)".
func (p TierPick) Label() string {
	if p.Reasoning == "" {
		return p.Model
	}
	return p.Model + " (thinking: " + p.Reasoning + ")"
}

// SwarmTierMap is a user-configured per-provider tier override: providerID
// -> {"weak"/"medium"/"strong" -> the pick}. It composes over the built-in
// family table — an explicit entry wins, an unset tier falls back to the
// built-in guess. This is what lets a gateway with no built-in family table
// (opencode-go, litellm, openrouter) still answer `tier: weak` with a cheap
// model instead of the full host model. Built from Config.SwarmTiers.
type SwarmTierMap map[string]map[string]TierPick

// ResolveSwarmTier maps a weak/medium/strong tier to a concrete pick for the
// host provider, capped so a sub-agent is never *stronger* than the host
// model — a weak host cannot spawn a strong child (the maki rule, to keep
// delegation cheap). overrides (may be nil) takes precedence over the built-in
// family guesses. Returns the zero pick when the tier is empty/unknown, the
// provider has neither an override nor a built-in table, or nothing resolves;
// in every such case the caller falls back to the host model, so an
// unconfigured provider simply ignores the tier.
func ResolveSwarmTier(providerID, hostModel, tier string, overrides SwarmTierMap) TierPick {
	want, ok := swarmTierRank[strings.ToLower(strings.TrimSpace(tier))]
	if !ok {
		return TierPick{}
	}
	// The provider must have SOME tier source — a user override or a built-in
	// family table — or tier resolution is a deliberate no-op.
	if len(overrides[providerID]) == 0 && swarmTierFamilies[providerID] == nil {
		return TierPick{}
	}
	// Cap at the host model's tier when we can identify it.
	if capRank, ok := swarmTierRankOf(providerID, hostModel, overrides); ok && want > capRank {
		want = capRank
	}
	return swarmPickForRank(providerID, want, overrides)
}

// SwarmRankNames returns the tier names in weak→strong order, for callers
// that render the tier ladder (e.g. `terva models tiers`).
func SwarmRankNames() []string { return append([]string(nil), swarmRankName...) }

// SwarmTierHasBuiltin reports whether a provider has a built-in family table
// (so an unconfigured tier still resolves without a user override).
func SwarmTierHasBuiltin(providerID string) bool {
	return swarmTierFamilies[providerID] != nil
}

// SwarmTierLadder reports the resolved weak/medium/strong pick for a provider
// UNCAPPED (as if the host were the strongest tier), plus where each came from:
// "override", "built-in", or "" when nothing resolves. It's the read-only view
// behind `terva models tiers`; resolution order matches ResolveSwarmTier.
func SwarmTierLadder(providerID string, overrides SwarmTierMap) (picks [3]TierPick, sources [3]string) {
	for rank := range swarmRankName {
		name := swarmRankName[rank]
		if p, ok := overridePick(providerID, name, overrides); ok {
			picks[rank], sources[rank] = p, "override"
			continue
		}
		if m := swarmTierFamilies[providerID][name].resolve(providerID); m != "" {
			picks[rank], sources[rank] = TierPick{Model: m}, "built-in"
		}
	}
	return
}

// overridePick reads one rung from the user's map. A rung that names ONLY an
// effort is deliberately honored: "run the built-in model for this rung, but
// think this hard" is the least-effort way to build a ladder on a provider
// whose model family terva already knows, and requiring the id to be repeated
// would just invite it to drift from the built-in one.
func overridePick(providerID, rung string, overrides SwarmTierMap) (TierPick, bool) {
	ov := overrides[providerID]
	if ov == nil {
		return TierPick{}, false
	}
	p := ov[rung]
	p.Model, p.Reasoning = strings.TrimSpace(p.Model), strings.TrimSpace(p.Reasoning)
	if p.IsZero() {
		return TierPick{}, false
	}
	if p.Model == "" {
		p.Model = swarmTierFamilies[providerID][rung].resolve(providerID)
		if p.Model == "" {
			return TierPick{}, false
		}
	}
	return p, true
}

// swarmPickForRank resolves one tier rank for the provider: a user override
// wins, else the built-in family keyword matched against the provider's
// catalog. The zero pick when neither yields a model.
func swarmPickForRank(providerID string, rank int, overrides SwarmTierMap) TierPick {
	name := swarmRankName[rank]
	if p, ok := overridePick(providerID, name, overrides); ok {
		return p
	}
	if m := swarmTierFamilies[providerID][name].resolve(providerID); m != "" {
		return TierPick{Model: m}
	}
	return TierPick{}
}

// swarmTierRankOf reports which tier the given model belongs to for its
// provider (and whether it could be identified at all): first by exact match
// against the user override's ids, then by the built-in family keywords.
//
// A same-model ladder makes this ambiguous ON PURPOSE — three rungs, one id —
// so the FIRST (weakest) match wins and the host cap becomes a no-op rather
// than pinning every spawn to the weak rung. Which is right: on a one-model
// ladder the host is not stronger than anything, and the cap exists to stop a
// weak host reaching for a stronger MODEL.
func swarmTierRankOf(providerID, modelID string, overrides SwarmTierMap) (int, bool) {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return 0, false
	}
	if ov := overrides[providerID]; ov != nil {
		matched := 0
		rankOf := -1
		for rank, name := range swarmRankName {
			if m := strings.TrimSpace(ov[name].Model); m != "" && strings.ToLower(m) == id {
				matched++
				if rankOf < 0 {
					rankOf = rank
				}
			}
		}
		// One id on several rungs is a thinking ladder, not a strength
		// ordering: refuse to rank it and let every tier resolve uncapped.
		if matched > 1 {
			return 0, false
		}
		if rankOf >= 0 {
			return rankOf, true
		}
	}
	for rank, name := range swarmRankName {
		if swarmTierFamilies[providerID][name].matches(id) {
			return rank, true
		}
	}
	return 0, false
}
