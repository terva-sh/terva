package provider

import (
	"reflect"
	"strings"
	"testing"
)

const tieredModelsJSON = `{
  "providers": {
    "openai-compatible": {
      "models": [
        {
          "id": "local-gateway-qwen",
          "contextWindow": 1000000,
          "priceInput": 1, "priceOutput": 5, "priceCacheRead": 0.2,
          "longContextTiers": [
            {"above": 128000, "priceInput": 3, "priceOutput": 15},
            {"above": 32000, "priceInput": 1.8, "priceOutput": 9},
            {"above": 0, "priceInput": 99}
          ]
        }
      ]
    },
    "anthropic": {
      "models": [
        {
          "id": "claude-haiku-5-5",
          "contextSurchargeAt": 150000,
          "longContextTiers": [{"above": 150000, "priceInput": 0.3, "priceOutput": 1.5}]
        }
      ]
    }
  }
}`

// An operator describing a gateway that bills by prompt length gets the tiers
// sorted, the hint derived from the first step, and a warning for a step that
// every prompt would pass.
func TestModelsJSONLongContextTiers(t *testing.T) {
	overrides, warnings := ParseUserModelsWithWarnings([]byte(tieredModelsJSON))
	if len(warnings) != 1 || !strings.Contains(warnings[0], `no positive "above"`) {
		t.Errorf("warnings = %q, want one about the above:0 step", warnings)
	}
	reg := NewRegistry()
	reg.SetUserOverrides(overrides)

	gw, err := reg.FindModel("openai-compatible", "local-gateway-qwen")
	if err != nil {
		t.Fatal(err)
	}
	want := []PriceTier{{Above: 32000, Input: 1.8, Output: 9}, {Above: 128000, Input: 3, Output: 15}}
	if !reflect.DeepEqual(gw.LongContextTiers, want) {
		t.Errorf("tiers = %+v, want %+v (sorted, the above:0 step dropped)", gw.LongContextTiers, want)
	}
	if gw.ContextSurchargeAt != 32000 {
		t.Errorf("ContextSurchargeAt = %d, want 32000, the first step, when the file sets only tiers", gw.ContextSurchargeAt)
	}
	u := Usage{InputTokens: 128001, OutputTokens: 100}
	ApplyCost(gw, &u)
	if w := (128001*3 + 100*15) / 1e6; !near(u.CostUSD, w) {
		t.Errorf("cost past 128K = %v, want %v", u.CostUSD, w)
	}

	// On a catalog row the file's tiers replace the catalog's as a set, and
	// the base prices it did not mention stay the catalog's.
	h, err := reg.FindModel("anthropic", "claude-haiku-5-5")
	if err != nil {
		t.Fatal(err)
	}
	if want := []PriceTier{{Above: 150000, Input: 0.3, Output: 1.5}}; !reflect.DeepEqual(h.LongContextTiers, want) {
		t.Errorf("haiku tiers = %+v, want %+v", h.LongContextTiers, want)
	}
	if h.ContextSurchargeAt != 150000 || h.PriceInput != 0.1 {
		t.Errorf("haiku surcharge / input = %d / %v, want 150000 / 0.1", h.ContextSurchargeAt, h.PriceInput)
	}
}

// The model editor reads an entry into UserModel and writes it back. A field
// the struct does not carry is dropped by that round trip, so an operator's
// tiers would vanish the first time they renamed the model in /model.
func TestModelsJSONTiersSurviveTheEditorRoundTrip(t *testing.T) {
	f, err := ParseUserModelsFile([]byte(tieredModelsJSON))
	if err != nil {
		t.Fatal(err)
	}
	um, ok := f.Find("anthropic", "claude-haiku-5-5")
	if !ok {
		t.Fatal("entry missing")
	}
	um.Name = "Haiku"
	if err := f.Upsert("anthropic", um); err != nil {
		t.Fatal(err)
	}
	b, err := MarshalUserModelsFile(f)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseUserModelsFile(b)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := back.Find("anthropic", "claude-haiku-5-5")
	if got.ContextSurchargeAt != 150000 || len(got.LongContextTiers) != 1 || got.LongContextTiers[0].PriceInput != 0.3 {
		t.Errorf("after the round trip: surcharge %d, tiers %+v", got.ContextSurchargeAt, got.LongContextTiers)
	}

	// An entry that never set them does not grow the keys.
	plain := UserModelsFile{Providers: map[string]UserProvider{"ollama": {Models: []UserModel{{ID: "m"}}}}}
	b, err = MarshalUserModelsFile(plain)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, "longContextTiers") || strings.Contains(s, "contextSurchargeAt") {
		t.Errorf("an entry without tiers wrote them anyway:\n%s", s)
	}
}
