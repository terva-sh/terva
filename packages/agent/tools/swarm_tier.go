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
	// reasoning is the thinking level this rung runs its model at, empty to
	// leave the effort to the child.
	//
	// The table used to refuse to name one, on the grounds that terva
	// recognises model FAMILIES and should not guess how hard someone wants a
	// sub-agent to think. That held while a ladder WAS a family ladder. It
	// stopped holding once price stopped tracking capability: on a recent
	// series the better "medium" is usually the largest model thinking little,
	// not a middling model thinking hard, and a small model is often most
	// useful thinking HARD. A table that can only name families cannot say
	// either of those, so it said the wrong thing confidently instead.
	reasoning string
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
	// An EFFORT ladder, not a family one. The family shape (haiku / sonnet /
	// opus) reads well and was wrong in practice: on a recent series the
	// better "medium" is the largest model thinking a little, not a middling
	// model thinking hard, and a small model earns its place by thinking HARD
	// rather than by being asked for less. So medium and strong share a model
	// and differ by effort, and the weak rung is a haiku that thinks.
	//
	// Sharing a model across two rungs is why swarmTierRankOf refuses to rank
	// a host that matches more than one: ranked, a host on opus-5 would cap
	// `tier: strong` down to medium and hand back the same model thinking
	// less.
	"anthropic": {
		"weak":   {match: []string{"haiku"}, reasoning: "high"},
		"medium": {match: []string{"claude-opus-5", "opus"}, reasoning: "low"},
		"strong": {match: []string{"claude-opus-5", "opus"}, reasoning: "high"},
		"cheap":  {match: []string{"haiku"}, reasoning: "minimum"},
	},
	// Copilot serves the same Anthropic families under its own ids. Left as a
	// FAMILY ladder rather than following anthropic onto an effort one: the
	// subscription bills by request rather than by token, so "the same model
	// thinking harder" does not buy the separation there that it buys on the
	// metered API.
	"github-copilot": {
		"weak":   {match: []string{"haiku"}},
		"medium": {match: []string{"sonnet"}},
		"strong": {match: []string{"opus"}},
		"cheap":  {match: []string{"haiku"}, reasoning: "minimum"},
	},
	// Every rung excludes "image". The nano-banana models are named after the
	// text family they sit beside — gemini-2.5-flash-image, gemini-3-pro-image,
	// gemini-3.1-flash-lite-image — so each one falls inside the very rule that
	// names its text sibling, and they sort EARLIER than the current
	// generations. Without this the medium rung resolved to Nano Banana (32k
	// window against 1M) and the strong rung to Nano Banana Pro, so a
	// `tier: medium` swarm spawn or a raati seat on google was dispatched to an
	// image model. The weak rung looked right only because catalog order
	// happened to reach flash-lite before flash-lite-image, which is luck, not
	// a rule — so it is excluded here too.
	"google": {
		"weak": {match: []string{"flash-lite"}, unless: []string{"image"}},
		// Newest-first, falling through to the bare family word. Catalog order
		// alone put the medium rung on gemini-3-flash-preview, two generations
		// behind the 3.5/3.6/3.7 flashes shipping beside it, because the
		// preview row is listed first and first-match wins. Pinning the
		// generations buys the good pick today; the trailing "flash" means a
		// catalog that has dropped all three degrades to some flash rather than
		// to nothing. TestEveryListedTierRungResolves is what catches a pin
		// list that has gone entirely stale.
		"medium": {match: []string{"gemini-3.7-flash", "gemini-3.6-flash", "gemini-3.5-flash", "flash"}, unless: []string{"flash-lite", "image"}},
		"strong": {match: []string{"pro"}, unless: []string{"image"}},
		"cheap":  {match: []string{"flash-lite"}, unless: []string{"image"}, reasoning: "minimum"},
	},
	// "gpt-5" is a substring of every mini/nano/chat/codex/pro variant, so
	// the strong rung is defined by what it is NOT as much as what it is.
	"openai": {
		"weak":   {match: []string{"nano"}},
		"medium": {match: []string{"mini"}, unless: []string{"nano"}},
		"strong": {match: []string{"gpt-5.5", "gpt-5.4", "gpt-5"}, unless: []string{"mini", "nano", "chat", "codex", "pro", "turbo", "research"}},
		"cheap":  {match: []string{"nano"}, reasoning: "minimum"},
	},
	"openai-responses": {
		"weak":   {match: []string{"nano"}},
		"medium": {match: []string{"mini"}, unless: []string{"nano"}},
		"strong": {match: []string{"gpt-5.5", "gpt-5.4", "gpt-5"}, unless: []string{"mini", "nano", "chat", "codex", "pro", "turbo", "research"}},
		"cheap":  {match: []string{"nano"}, reasoning: "minimum"},
	},
	// Codex names its generations sol / terra / luna, which say nothing
	// about capability and do not recur across generations. Named models,
	// newest first — see tierFamily on why that is the honest encoding here.
	// An effort ladder too, for the reason anthropic's is one. Luna thinking
	// its hardest is the weak rung; sol at two efforts carries medium and
	// strong. The mini stays as the cost tier, which is the only rung whose
	// job is actually "spend less".
	"openai-codex": {
		"weak":   {match: []string{"gpt-5.6-luna"}, unless: []string{"mini"}, reasoning: "maximum"},
		"medium": {match: []string{"gpt-5.6-sol", "gpt-5.5"}, unless: []string{"mini"}, reasoning: "low"},
		"strong": {match: []string{"gpt-5.6-sol", "gpt-5.5"}, unless: []string{"mini"}, reasoning: "high"},
		"cheap":  {match: []string{"mini", "spark"}, reasoning: "minimum"},
	},
	// Two rungs each: neither vendor ships a middle model to point at, and
	// inventing one would be a guess wearing a ladder's clothes.
	"kimi": {
		"weak":   {match: []string{"kimi-k2", "kimi-for-coding"}},
		"strong": {match: []string{"k3"}},
		"cheap":  {match: []string{"kimi-k2", "kimi-for-coding"}, reasoning: "minimum"},
	},
	"deepseek": {
		"weak":   {match: []string{"flash"}},
		"strong": {match: []string{"pro"}},
		"cheap":  {match: []string{"flash"}, reasoning: "minimum"},
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

// resolvePick is resolve plus the rung's own effort — the whole pick a caller
// dispatches, rather than the model half of it.
func (f tierFamily) resolvePick(providerID string) TierPick {
	if m := f.resolve(providerID); m != "" {
		return TierPick{Model: m, Reasoning: f.reasoning}
	}
	return TierPick{}
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

// TierCheap is the cost tier. It is NOT a fourth rung of the ladder above and
// deliberately sits outside that ordering: weak/medium/strong say how capable a
// sub-agent should be, and on a recent model series that is mostly a question
// of how hard the largest model thinks — which means none of them answers "keep
// this cheap". A caller that cares about spend rather than strength had nothing
// to ask for.
//
// Two consequences follow from it being a separate axis, and both are the
// point. A `cheap` spawn is never capped to the host's strength: capping exists
// so a weak host cannot reach for a STRONGER child, and reaching for a cheaper
// one is not that. And rigor level 1 still requires the three capability rungs,
// so adding this cannot quietly change what a raati gate is trusted on.
const TierCheap = "cheap"

// swarmTierNames is every tier a user can configure or see: the capability
// ladder, then the cost axis last so the three still read as a ladder.
var swarmTierNames = []string{"weak", "medium", "strong", TierCheap}

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
	name := strings.ToLower(strings.TrimSpace(tier))
	want, ok := swarmTierRank[name]
	if !ok && name != TierCheap {
		return TierPick{}
	}
	// The provider must have SOME tier source — a user override or a built-in
	// family table — or tier resolution is a deliberate no-op.
	if len(overrides[providerID]) == 0 && swarmTierFamilies[providerID] == nil {
		return TierPick{}
	}
	if name == TierCheap {
		// Uncapped, on purpose. The cap stops a weak host reaching for a
		// stronger child; asking for a cheaper one is not that, and capping
		// "cheap" to a weak host's rung would hand back the weak rung — which
		// on a modern ladder can be the LARGEST model thinking a little.
		if p, ok := overridePick(providerID, TierCheap, overrides); ok {
			return p
		}
		return swarmTierFamilies[providerID][TierCheap].resolvePick(providerID)
	}
	// Cap at the host model's tier when we can identify it.
	if capRank, ok := swarmTierRankOf(providerID, hostModel, overrides); ok && want > capRank {
		want = capRank
	}
	return swarmPickForRank(providerID, want, overrides)
}

// SwarmRankNames returns the CAPABILITY tier names in weak→strong order. It is
// the ordering — host capping and raati rigor level 1 read it — and the cost
// tier is deliberately absent from it. Surfaces that render the whole table
// want SwarmTierNames instead.
func SwarmRankNames() []string { return append([]string(nil), swarmRankName...) }

// SwarmTierNames returns every configurable tier, capability rungs first and
// the cost axis last.
func SwarmTierNames() []string { return append([]string(nil), swarmTierNames...) }

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
	for rank, name := range swarmRankName {
		picks[rank], sources[rank] = swarmTierRow(providerID, name, overrides)
	}
	return
}

// SwarmTierTable is the whole configurable set — the capability ladder AND the
// cost tier — for a surface that renders or edits it.
//
// Deliberately separate from SwarmTierLadder rather than a wider version of it.
// That function is the CAPABILITY ladder, and raati rigor level 1 reads it as
// "every rung must resolve" and then seats one panel member per rung. Widening
// it in place would have made level 1 demand a cost tier nobody configured, and
// seated the cheap rung on a gate — changing what a gate is trusted on as a
// side effect of adding a name.
func SwarmTierTable(providerID string, overrides SwarmTierMap) (names []string, picks []TierPick, sources []string) {
	names = SwarmTierNames()
	picks, sources = make([]TierPick, len(names)), make([]string, len(names))
	for i, name := range names {
		picks[i], sources[i] = swarmTierRow(providerID, name, overrides)
	}
	return
}

// swarmTierRow resolves one named tier: an override wins, else the built-in
// family rule. One helper so the ladder and the table cannot drift.
func swarmTierRow(providerID, name string, overrides SwarmTierMap) (TierPick, string) {
	if p, ok := overridePick(providerID, name, overrides); ok {
		return p, "override"
	}
	if p := swarmTierFamilies[providerID][name].resolvePick(providerID); !p.IsZero() {
		return p, "built-in"
	}
	return TierPick{}, ""
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
	if p := swarmTierFamilies[providerID][name].resolvePick(providerID); !p.IsZero() {
		return p
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
	// Same refusal as the override branch above, and now reachable from the
	// built-in table too: an effort ladder puts one model on two rungs, and
	// ranking it would cap `tier: strong` down to medium and hand back that
	// very model thinking less.
	matched, rankOf := 0, -1
	for rank, name := range swarmRankName {
		if swarmTierFamilies[providerID][name].matches(id) {
			matched++
			if rankOf < 0 {
				rankOf = rank
			}
		}
	}
	if matched > 1 {
		return 0, false
	}
	if rankOf >= 0 {
		return rankOf, true
	}

	// Price fallback. An effort ladder names only a model or two, so most of a
	// provider's catalog matches no rung — including sonnet, which is a very
	// common host. The cap exists to keep delegation cheap ("a weak host
	// cannot spawn a strong child"), and that is a COST question, not the
	// capability one the ladder answers, so price is the axis it can honestly
	// use where families no longer do: rank the host at the dearest rung that
	// still costs no more than the host itself.
	//
	// Skipped whenever a price is missing or zero. A subscription provider
	// prices its whole catalog at 0 (github-copilot), and reading that as
	// "everything is equally cheap" would rank every host at the top rung and
	// switch the cap off exactly where nobody would notice.
	host, err := provider.FindModel(providerID, modelID)
	if err != nil || host.PriceOutput <= 0 {
		return 0, false
	}
	best, found := 0, false
	for rank, name := range swarmRankName {
		pick, _ := swarmTierRow(providerID, name, overrides)
		if pick.Model == "" {
			continue
		}
		m, err := provider.FindModel(providerID, pick.Model)
		if err != nil || m.PriceOutput <= 0 {
			continue
		}
		if m.PriceOutput <= host.PriceOutput {
			best, found = rank, true
		}
	}
	return best, found
}
