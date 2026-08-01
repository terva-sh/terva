package provider

import (
	"sort"
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
