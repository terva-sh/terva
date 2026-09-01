package tools

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// The built-in tier table is a set of guesses about strings, checked against
// the catalog those strings are matched into. Every check here reads the TABLE
// rather than a list of expectations, so adding a provider to swarmTierFamilies
// is how you find out whether it belongs there — the failure is the audit.
//
// The bug being guarded is specific and was the stated reason google sat out of
// the table for so long: "flash-lite" contains "flash", so a bare keyword can
// resolve the MEDIUM rung to a WEAK model, and nothing at runtime would say so.
// A sub-agent asked for the strong tier would just quietly be cheap.

func tableProviders() []string {
	out := make([]string, 0, len(swarmTierFamilies))
	for p := range swarmTierFamilies {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Every rung named in the table must actually resolve. A rung that matches
// nothing is a typo, or a pin list whose models have all left the catalog —
// the one kind of staleness in a named-model rung that IS mechanically
// detectable (see tierFamily).
func TestEveryListedTierRungResolves(t *testing.T) {
	for _, p := range tableProviders() {
		for _, rung := range swarmRankName {
			fam, listed := swarmTierFamilies[p][rung]
			if !listed || len(fam.match) == 0 {
				continue // a deliberately partial ladder; see the table's doc
			}
			if got := fam.resolve(p); got == "" {
				t.Errorf("%s/%s: matches %v but no catalog model qualifies — dead rung", p, rung, fam.match)
			}
		}
	}
}

// No catalog model may belong to two rungs. This is the property that makes
// substring matching safe at all: swarmTierRankOf asks "which tier is the host
// model" and can only have one answer, and a model in two rungs means one of
// the two rungs resolves to something the other also claims.
func TestSwarmTierFamiliesAreUnambiguous(t *testing.T) {
	for _, p := range tableProviders() {
		for _, m := range provider.ModelsForProvider(p) {
			var in []string
			for _, rung := range swarmRankName {
				if swarmTierFamilies[p][rung].matches(m.ID) {
					in = append(in, rung)
				}
			}
			if len(in) > 1 {
				// Two rungs on one model is legitimate now — an effort ladder
				// is exactly that — but only when the efforts differ. Same
				// model at the same effort is two names for one child, which
				// is the ambiguity this always guarded against; it just used
				// to be expressible only as a keyword overlap.
				efforts := map[string]bool{}
				for _, rung := range in {
					efforts[swarmTierFamilies[p][rung].reasoning] = true
				}
				if len(efforts) < len(in) {
					t.Errorf("%s: model %q matches %s at the same effort — add an `unless` to the wider rung, or give the rungs different efforts",
						p, m.ID, strings.Join(in, " and "))
				}
			}
		}
	}
}

// The rungs must come out in the order they claim. Price is the catalog's own
// opinion about capability, and it is the only cross-check available that the
// table itself did not author: a rule set whose "strong" costs less than its
// "weak" has the ladder upside down, whatever the keywords read like.
//
// Skipped where the catalog cannot answer — a subscription provider prices
// everything at 0 (github-copilot), and a rung that did not resolve has no
// price to compare.
func TestSwarmTierLadderRunsCheapToExpensive(t *testing.T) {
	for _, p := range tableProviders() {
		picks, _ := SwarmTierLadder(p, nil)
		var (
			names  []string
			prices []float64
		)
		for rank, pick := range picks {
			if pick.Model == "" {
				continue
			}
			m, err := provider.FindModel(p, pick.Model)
			if err != nil || m.PriceOutput == 0 {
				continue
			}
			names = append(names, fmt.Sprintf("%s=%s($%.2f)", swarmRankName[rank], pick.Model, m.PriceOutput))
			prices = append(prices, m.PriceOutput)
		}
		for i := 1; i < len(prices); i++ {
			if prices[i] < prices[i-1] {
				t.Errorf("%s: ladder is not monotonic — %s", p, strings.Join(names, " "))
				break
			}
		}
	}
}

// A resolved rung is dispatched, so it must be a model that answers today.
// Speculative catalog entries 404 until the vendor switches them on; a
// newest-first pin list reaches for exactly those, which is how a "strong"
// rung can become a guaranteed failed spawn while reading perfectly.
func TestNoTierRungResolvesToASpeculativeModel(t *testing.T) {
	for _, p := range tableProviders() {
		picks, _ := SwarmTierLadder(p, nil)
		for rank, pick := range picks {
			if pick.Model == "" {
				continue
			}
			m, err := provider.FindModel(p, pick.Model)
			if err != nil {
				t.Errorf("%s/%s resolved to %q, which is not in the catalog", p, swarmRankName[rank], pick.Model)
				continue
			}
			if m.Speculative {
				t.Errorf("%s/%s resolved to %q, which is speculative — it 404s until the vendor ships it", p, swarmRankName[rank], pick.Model)
			}
		}
	}
}

// A ladder whose rungs resolve to the SAME model is theater: it reads as three
// tiers and spends like one. Two rungs pointing at one id means the keywords
// do not actually separate the families.
func TestSwarmTierLadderRungsAreDistinctModels(t *testing.T) {
	for _, p := range tableProviders() {
		picks, _ := SwarmTierLadder(p, nil)
		seen := map[string]string{}
		for rank, pick := range picks {
			if pick.Model == "" {
				continue
			}
			// The whole PICK, not the model alone. A built-in rung may name an
			// effort now, so one model across two rungs is a deliberate effort
			// ladder rather than theater. What is still theater is two rungs
			// agreeing on both halves.
			key := pick.Model + "@" + pick.Reasoning
			if prev, dup := seen[key]; dup {
				t.Errorf("%s: %s and %s both resolve to %q at the same effort %q", p, prev, swarmRankName[rank], pick.Model, pick.Reasoning)
			}
			seen[key] = swarmRankName[rank]
		}
	}
}

// What the table is FOR, stated as the symptom that sent me here: a model
// picking the built-in `code-review` profile on a fresh install got
// "a correlated panel cannot hold a gate" and read it as being denied
// permission. The profile rides auto level, auto level is capped by what the
// config can seat, and with an empty tier table that was 0 for everyone —
// so the gate refused itself for every host on earth except Anthropic.
//
// Enrolled from the table, not listed: a provider whose ladder is complete
// must be able to seat level 1 and therefore to hold a gate.
func TestAFullLadderCanHoldAGate(t *testing.T) {
	for _, p := range tableProviders() {
		picks, _ := SwarmTierLadder(p, nil)
		full := true
		for _, pick := range picks {
			if pick.Model == "" {
				full = false
			}
		}
		if !full {
			continue // a deliberately partial ladder; level 2 is its route
		}
		seats := 3
		lvl := HighestRaatiLevel(p, nil, nil, seats)
		if lvl < 1 {
			t.Errorf("%s: full ladder %v but HighestRaatiLevel = %d", p, picks, lvl)
			continue
		}
		pool, err := ResolveRaatiBindings(1, p, picks[2].Model, p, nil, nil, seats)
		if err != nil {
			t.Errorf("%s: level 1 does not seat: %v", p, err)
			continue
		}
		if err := RefuseCorrelatedGate("code-review", "gate", pool, true); err != nil {
			t.Errorf("%s: a full ladder still refuses a gate: %v", p, err)
		}
	}
}

// Teeth. The checks above pass vacuously if `matches` stops matching, so aim
// each shape at the rule it exists to enforce.
func TestTierFamilyMatchSemantics(t *testing.T) {
	flash := tierFamily{match: []string{"flash"}, unless: []string{"flash-lite"}}
	if !flash.matches("gemini-2.5-flash") {
		t.Error("medium rung must match the plain flash model")
	}
	if flash.matches("gemini-2.5-flash-lite") {
		t.Error("`unless` must exclude the narrower family — this is the whole bug")
	}
	if flash.matches("GEMINI-2.5-FLASH-LITE") {
		t.Error("`unless` must be case-insensitive")
	}
	if !flash.matches("GEMINI-2.5-FLASH") {
		t.Error("`match` must be case-insensitive")
	}
	if (tierFamily{}).matches("anything") {
		t.Error("an unlisted rung must match nothing")
	}
	// Preference order: the whole point of a pin list is that the earlier
	// entry wins even when a later one also matches and sorts first.
	pinned := tierFamily{match: []string{"opus", "sonnet"}}
	if got := pinned.resolve("anthropic"); !strings.Contains(got, "opus") {
		t.Errorf("resolve honored catalog order over match order: got %q, want an opus", got)
	}
}

// A resolved rung is DISPATCHED — a swarm spawn, a raati seat — so it must be
// a model you can hold a working session with. The image-generation families
// are named after the text family they sit beside (gemini-2.5-flash-image,
// gemini-3-pro-image), which puts each one INSIDE the rule that names its text
// sibling, and they sort earlier than the current generations. That is how
// google's medium rung came to resolve to Nano Banana — a 32k window against
// its siblings' 1M, billing pictures at 20x the text rate — with every existing
// check passing: the ambiguity census asks whether a model matches TWO rungs
// (it matched one), and the price census asks whether the rungs ascend (they
// did). Neither can notice that a rung resolved to the wrong KIND of thing.
//
// PriceOutputImage is the discriminator, and the choice matters. The obvious
// one — CapImageOutput — is WRONG here and would have emptied openai-codex's
// whole ladder: gpt-5.6-sol, gpt-5.6-terra and gpt-5.4-mini all carry that
// capability, because they emit images through the Responses image tool while
// being ordinary text models. PriceOutputImage is set only on the dual-rate
// image families, which is exactly the set that should never fill a rung.
func TestNoTierRungResolvesToAnImageModel(t *testing.T) {
	for _, p := range tableProviders() {
		picks, _ := SwarmTierLadder(p, nil)
		for rank, pick := range picks {
			if pick.Model == "" {
				continue
			}
			m, err := provider.FindModel(p, pick.Model)
			if err != nil {
				continue // TestNoTierRungResolvesToASpeculativeModel reports this
			}
			if m.PriceOutputImage > 0 {
				t.Errorf("%s/%s resolved to %q, an image-generation model (PriceOutputImage=%g) — add an `unless` so the rung reaches its text sibling",
					p, swarmRankName[rank], pick.Model, m.PriceOutputImage)
			}
		}
	}
}

// Built-in rungs MAY name an effort now, and the rule that replaced "never do
// it" is this: an effort has to buy something.
//
// The table used to refuse efforts outright, on the grounds that terva
// recognises model FAMILIES and should not guess how hard anyone wants to
// think. That reasoning held while a ladder was a family ladder. It stopped
// holding once price stopped tracking capability — on a recent series the
// better "medium" is the largest model thinking a little, and a small model
// earns its keep by thinking hard — so a family-only table could not describe
// the ladder people actually want, and stated a different one confidently.
//
// What must be true instead: where two rungs share a model, their efforts have
// to reach that model as DIFFERENT wire values. Two rungs that collapse onto
// one effort are a single child wearing two labels, and the label is the whole
// basis on which a caller picks a tier.
func TestBuiltinEffortLadderRungsDiffer(t *testing.T) {
	for _, p := range tableProviders() {
		picks, _ := SwarmTierLadder(p, nil)
		byModel := map[string][]int{}
		for rank, pick := range picks {
			if pick.Model != "" {
				byModel[pick.Model] = append(byModel[pick.Model], rank)
			}
		}
		for id, ranks := range byModel {
			if len(ranks) < 2 {
				continue
			}
			m, err := provider.FindModel(p, id)
			if err != nil {
				continue
			}
			seen := map[provider.ReasoningEffect]string{}
			for _, rank := range ranks {
				eff := provider.ReasoningEffectFor(m, provider.LadderWireValue(picks[rank].Reasoning))
				if prev, dup := seen[eff]; dup {
					t.Errorf("%s: %s and %s both run %q at one effort on the wire (%+v) — one child, two labels",
						p, prev, swarmRankName[rank], id, eff)
				}
				seen[eff] = swarmRankName[rank]
			}
		}
	}
}

// An effort the table names has to be a real rung of the ladder. A typo fails
// SILENTLY — the request carries whatever NormalizeReasoning made of it — so
// nothing downstream would report it.
func TestBuiltinEffortsAreRealLadderRungs(t *testing.T) {
	for _, p := range tableProviders() {
		for _, name := range SwarmTierNames() {
			lv := swarmTierFamilies[p][name].reasoning
			if lv == "" {
				continue
			}
			if !slices.Contains(provider.ReasoningLevels, lv) {
				t.Errorf("%s/%s names effort %q, which is not a ladder rung (%v)", p, name, lv, provider.ReasoningLevels)
			}
		}
	}
}

// The cost tier has to actually be the cheap one. It is what a caller reaches
// for when spend is the concern — a test run, a bulk pass — and a `cheap` rung
// dearer than a capability rung is the failure nobody would think to look for.
func TestCheapTierIsNotDearerThanTheLadder(t *testing.T) {
	for _, p := range tableProviders() {
		names, picks, _ := SwarmTierTable(p, nil)
		cheap, haveCheap := 0.0, false
		for i, n := range names {
			if n != TierCheap || picks[i].Model == "" {
				continue
			}
			if m, err := provider.FindModel(p, picks[i].Model); err == nil && m.PriceOutput > 0 {
				cheap, haveCheap = m.PriceOutput, true
			}
		}
		if !haveCheap {
			continue
		}
		for i, n := range names {
			if n == TierCheap || picks[i].Model == "" {
				continue
			}
			m, err := provider.FindModel(p, picks[i].Model)
			if err != nil || m.PriceOutput <= 0 {
				continue
			}
			if cheap > m.PriceOutput {
				t.Errorf("%s: cheap costs $%.2f, dearer than %s's %q at $%.2f", p, cheap, n, m.ID, m.PriceOutput)
			}
		}
	}
}
