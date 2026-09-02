package provider

import (
	"math"
	"sort"
	"strings"
	"testing"
)

// A provider whose every model prices at zero produces "$0.000" everywhere
// terva shows money — the status bar, /status, the usage pane, and (since the
// cache panel) the savings figure too. That reads as "this is free", and for a
// subscription it is exactly wrong: no money moved per token, but the session
// still consumed a plan whose worth is what those tokens would have cost.
//
// The failure is invisible by construction. Nothing errors, nothing looks
// broken, and the number is only wrong if you know what it should have been.
// openai-codex has carried OpenAI's list prices from the start and reads
// "$0.529 ~$0.71/hr (sub)"; kimi shipped zeros for four releases and nobody
// filed it, because "$0.000 (sub)" looks like a considered answer.
//
// So this guard discovers providers from the catalog and requires each to
// price SOMETHING. The exemption list below is the opposite polarity from the
// filename lists this repo keeps deleting: it cannot hide a provider that is
// ADDED, only one someone has already justified in writing.
var unpricedProviders = map[string]string{
	// Copilot bills "premium requests", not tokens. There is no per-token
	// list price to carry, and inventing one from the underlying vendor's
	// rates would describe a bill nobody receives.
	"github-copilot": "billed per premium request, not per token",

	// Z.AI's coding plan (api.z.ai/api/coding/paas/v4) — the same defect the
	// kimi rows had, and left open on purpose. Every GLM row in this catalog
	// is a RESELLER's rate (fireworks, opencode, vercel, cerebras), and a
	// reseller's margin is not Z.AI's list price. The kimi rows could be
	// fixed because moonshotai is Moonshot's own platform, first-party for
	// the same models. Fixing this one needs a rate from Z.AI, not a
	// neighbour's. Until then zeros are honest and this line says why.
	"zai": "no first-party list price in the catalog; reseller rates are not Z.AI's",
}

func TestEveryProviderPricesSomething(t *testing.T) {
	withCatalogState(t)
	ResetCatalogLayers()

	type tally struct{ total, unpriced int }
	byProvider := map[string]*tally{}
	for _, m := range Active() {
		tl := byProvider[m.Provider]
		if tl == nil {
			tl = &tally{}
			byProvider[m.Provider] = tl
		}
		tl.total++
		if m.PriceInput == 0 && m.PriceOutput == 0 {
			tl.unpriced++
		}
	}

	var offenders []string
	for name, tl := range byProvider {
		if tl.unpriced != tl.total {
			continue // at least one row carries a price
		}
		if _, excused := unpricedProviders[name]; excused {
			continue
		}
		offenders = append(offenders, name)
	}
	sort.Strings(offenders)

	if len(offenders) > 0 {
		t.Errorf("these providers price every model at zero, so every cost readout is $0.000: %v\n\n"+
			"Carry the vendor's published list rates — a subscription still shows what it WOULD\n"+
			"have cost, with \"(sub)\" saying no money moved (see the openai-codex rows). If the\n"+
			"provider genuinely has no per-token price, add it to unpricedProviders with the reason.",
			offenders)
	}

	// An exemption for a provider that no longer exists, or that has since
	// been priced, is a stale claim about the catalog. Nothing else would
	// ever notice.
	for name := range unpricedProviders {
		tl := byProvider[name]
		if tl == nil {
			t.Errorf("unpricedProviders excuses %q, which is not in the catalog — drop the entry", name)
			continue
		}
		if tl.unpriced != tl.total {
			t.Errorf("unpricedProviders excuses %q, but %d of its %d models now carry a price — drop the entry",
				name, tl.total-tl.unpriced, tl.total)
		}
	}
}

// The Kimi Code rows are the case that prompted the guard: a subscription
// endpoint whose models have a published first-party rate on the Moonshot
// platform. They must carry it, so "(sub)" has a number to qualify.
func TestKimiCodeRowsCarryTheMoonshotListPrice(t *testing.T) {
	withCatalogState(t)
	ResetCatalogLayers()

	// K3 and its 256k sibling are priced from moonshotai/k3-256k.
	k3ref, err := FindModel("moonshotai", "k3-256k")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"k3", "k3-256k"} {
		m, err := FindModel("kimi", id)
		if err != nil {
			t.Fatalf("kimi/%s: %v", id, err)
		}
		if m.PriceInput != k3ref.PriceInput || m.PriceOutput != k3ref.PriceOutput || m.PriceCacheRead != k3ref.PriceCacheRead {
			t.Errorf("kimi/%s prices in=%v out=%v cr=%v; want moonshotai's %v/%v/%v",
				id, m.PriceInput, m.PriceOutput, m.PriceCacheRead,
				k3ref.PriceInput, k3ref.PriceOutput, k3ref.PriceCacheRead)
		}
	}

	// The K2-generation rows are priced from moonshotai/kimi-k2-thinking.
	k2ref, err := FindModel("moonshotai", "kimi-k2-thinking")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"kimi-for-coding", "kimi-k2-thinking"} {
		m, err := FindModel("kimi", id)
		if err != nil {
			t.Fatalf("kimi/%s: %v", id, err)
		}
		if m.PriceInput != k2ref.PriceInput || m.PriceOutput != k2ref.PriceOutput {
			t.Errorf("kimi/%s prices in=%v out=%v; want moonshotai's %v/%v",
				id, m.PriceInput, m.PriceOutput, k2ref.PriceInput, k2ref.PriceOutput)
		}
	}
}

// kimi/kimi-k2-thinking was listed in two files at once, each asserting it was
// not in the other, with init order deciding which one FindModel returned. One
// row, in the file its siblings live in.
func TestKimiK2ThinkingIsDefinedOnce(t *testing.T) {
	withCatalogState(t)
	ResetCatalogLayers()

	n := 0
	for _, m := range Active() {
		if m.Provider == "kimi" && m.ID == "kimi-k2-thinking" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("kimi/kimi-k2-thinking appears %d times in the catalog; want 1", n)
	}
}

// Anthropic does not price prompt caching independently. A cache rate is a
// fixed multiple of the model's base input rate: a 5-minute cache write costs
// 1.25x base input, and a cache read costs 0.1x. Two of every Claude row's
// four prices are therefore determined by a third, and a row that states them
// freely is stating something that cannot be true.
//
// They drifted anyway, in four different shapes, none of them visible:
//
//   - claude-fable-5 carried Opus 5's cache pair (0.5 / 6.25) against a $10
//     base, halving every cached token it reported.
//   - claude-3-sonnet carried its cache READ value in the write field, so a
//     write was priced at the read rate.
//   - Two au.* Bedrock rows took the regional markup on input and output and
//     kept the unscaled US cache rates.
//
// TestEveryProviderPricesSomething cannot see any of it. That guard fires only
// when a provider prices EVERY row at zero, so a wrong but nonzero rate reads
// as a considered answer. This one checks the arithmetic instead.
//
// The 1-hour cache write (2x base input) is deliberately unmodelled. The
// catalog carries a single PriceCacheWrite field holding the 5-minute rate,
// which is the only one terva can incur, because buildRequest never sends
// ttl:"1h". Modelling the 1-hour rate needs a second field, not a second
// assertion here.
//
// Read the failure carefully, because this guard cannot tell you which number
// is wrong. It derives both cache rates from the row's own base input, so it
// treats base input as authoritative. It proves the three prices agree with
// each other, never that any of them matches a vendor's published figure.
//
// The au.* Bedrock rows are the worked example, and they cost a wrong edit.
// They carried 16.5/82.5 beside a cache pair of 0.5/6.25. The disagreement was
// real, but the base was the broken field: 16.5 is the Opus 4.1-era $15 with a
// 10% regional markup AWS does not charge, while 0.5/6.25 were already correct
// for the real $5 base. Scaling the cache up to 20.625 silenced the guard and
// kept the error. AWS prices a geographic cross-Region profile at standard
// rates and says so plainly: "There's no additional routing cost for using
// cross-region inference." A geo row should therefore match its in-region
// sibling. When this guard fires, check the base against a sibling row before
// you touch the cache pair.
const (
	anthCacheReadMultiplier  = 0.1
	anthCacheWriteMultiplier = 1.25
)

// cacheRateRule is the multiplier pair one row must satisfy. An exception
// still asserts exact numbers: it changes what the row is checked AGAINST,
// and never turns the check off. An entry that merely skipped a row would be
// the obvious place for the next typo to hide.
type cacheRateRule struct {
	read  float64
	write float64
	why   string
}

var anthropicCacheRateExceptions = map[string]cacheRateRule{
	// Anthropic footnotes this in the pricing table: "Cache hits and refreshes
	// on Claude Fable 5.1 and Claude Mythos 5.1 are priced at 0.025x the base
	// input price. All other models use the standard 0.1x multiplier." Add
	// Mythos 5.1 here when a row for it lands.
	"anthropic/claude-fable-5-1": {
		read: 0.025, write: 1.25,
		why: "published 0.025x cache read, the Fable/Mythos 5.1 exception",
	},

	// Claude 3 Haiku predates the multiplier scheme. Anthropic published $0.03
	// per MTok for a read and $0.30 for a write against a $0.25 base, which is
	// 0.12x and 1.2x. Those are rounded to whole cents, not derived, so they
	// are right and the multipliers are what does not apply.
	"anthropic/claude-3-haiku-20240307": {
		read: 0.12, write: 1.2,
		why: "Claude 3 Haiku's published rates are rounded to cents, not derived",
	},
	"cloudflare-ai-gateway/claude-3-haiku": {
		read: 0.12, write: 1.2,
		why: "passes through Claude 3 Haiku's rounded published rates",
	},
	"vercel-ai-gateway/anthropic/claude-3-haiku": {
		read: 0.12, write: 1.2,
		why: "passes through Claude 3 Haiku's rounded published rates",
	},
}

func TestAnthropicCacheRatesFollowTheirMultipliers(t *testing.T) {
	withCatalogState(t)
	ResetCatalogLayers()

	// Every price here is at most a few hundred dollars per MTok, so an
	// absolute epsilon is enough to absorb binary rounding (0.1*16.5 lands on
	// 1.6500000000000001) without ever accepting a real difference in cents.
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

	used := map[string]bool{}
	checked := 0

	for _, m := range Active() {
		id := strings.ToLower(m.ID)
		if !strings.Contains(id, "claude") && !strings.Contains(id, "anthropic") {
			continue
		}
		// An unpriced route (github-copilot bills per request) has no base to
		// take a multiple of. TestEveryProviderPricesSomething owns that case.
		if m.PriceInput == 0 {
			continue
		}
		checked++

		key := m.Provider + "/" + m.ID
		rule := cacheRateRule{read: anthCacheReadMultiplier, write: anthCacheWriteMultiplier}
		if ex, ok := anthropicCacheRateExceptions[key]; ok {
			rule = ex
			used[key] = true
		}

		if want := m.PriceInput * rule.read; !near(m.PriceCacheRead, want) {
			t.Errorf("%s: cache READ is %v, want %v (%gx of the %v base input)\n"+
				"  A Claude cache read is a fixed multiple of base input, never an\n"+
				"  independent number. Either the rate is wrong, or the base input is.\n"+
				"  If Anthropic publishes something else for this model, add it to\n"+
				"  anthropicCacheRateExceptions with the published figure and a reason.",
				key, m.PriceCacheRead, want, rule.read, m.PriceInput)
		}
		if want := m.PriceInput * rule.write; !near(m.PriceCacheWrite, want) {
			t.Errorf("%s: 5-minute cache WRITE is %v, want %v (%gx of the %v base input)\n"+
				"  Watch for the read value copied into the write field, and for a\n"+
				"  regional row that scaled input and output but not the cache pair.",
				key, m.PriceCacheWrite, want, rule.write, m.PriceInput)
		}
	}

	if checked == 0 {
		t.Fatal("matched no priced Claude rows, so this guard now tests nothing")
	}

	// An exception for a row that is gone, or one that restates the default,
	// is a written claim about the catalog that no longer holds. Nothing else
	// would ever notice, so the accepted set can only shrink on purpose.
	for key, ex := range anthropicCacheRateExceptions {
		if !used[key] {
			t.Errorf("anthropicCacheRateExceptions excuses %q (%s), which is not a "+
				"priced row in the catalog — drop the entry", key, ex.why)
			continue
		}
		if near(ex.read, anthCacheReadMultiplier) && near(ex.write, anthCacheWriteMultiplier) {
			t.Errorf("anthropicCacheRateExceptions excuses %q but states the standard "+
				"%gx/%gx pair — it grants nothing, so drop the entry",
				key, anthCacheReadMultiplier, anthCacheWriteMultiplier)
		}
	}
}
