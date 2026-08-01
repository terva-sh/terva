package provider

import "testing"

// k3-256k is the K3 model behind a 256k window instead of 1M. The row
// must stay a true sibling of k3: same endpoint, and — critically — the
// same DefaultReasoning:"high", because the coding endpoint silently
// downgrades a no-thinking request to Kimi 2.6 (see the catalog comment).
// A copy of the row that lost the default would answer as the wrong model.
func TestKimiK3256kMirrorsK3ExceptWindow(t *testing.T) {
	withCatalogState(t)
	ResetCatalogLayers()

	k3, err := FindModel("kimi", "k3")
	if err != nil {
		t.Fatal(err)
	}
	small, err := FindModel("kimi", "k3-256k")
	if err != nil {
		t.Fatal(err)
	}

	if small.ContextWindow != 262144 {
		t.Errorf("k3-256k ContextWindow = %d, want 262144", small.ContextWindow)
	}
	if k3.ContextWindow != 1000000 {
		t.Errorf("k3 ContextWindow = %d, want 1000000", k3.ContextWindow)
	}
	if small.DefaultReasoning != k3.DefaultReasoning || small.DefaultReasoning != "high" {
		t.Errorf("k3-256k DefaultReasoning = %q, want %q (k3's)", small.DefaultReasoning, k3.DefaultReasoning)
	}
	if small.BaseURL != k3.BaseURL {
		t.Errorf("k3-256k BaseURL = %q, want k3's %q", small.BaseURL, k3.BaseURL)
	}
	if small.MaxOutput != k3.MaxOutput {
		t.Errorf("k3-256k MaxOutput = %d, want k3's %d", small.MaxOutput, k3.MaxOutput)
	}
	if !small.Reasoning {
		t.Error("k3-256k must be reasoning-capable")
	}
}

// The Moonshot platform (global and CN) also serves k3-256k, pay-per-token, so
// the rows must carry K3's published pricing.
//
// This comment used to end "rather than the subscription's zeros", which is
// where the Kimi Code readout lost its number: the zeros on the kimi rows were
// not an omission but a stated position, and the position was wrong. A
// subscription's cost readout is an estimate of what the tokens WOULD have
// cost — that is what "(sub)" qualifies, and what openai-codex has always
// shown. The kimi rows carry these same rates now; see
// TestKimiCodeRowsCarryTheMoonshotListPrice, which pins them to this one.
func TestMoonshotK3256kRows(t *testing.T) {
	withCatalogState(t)
	ResetCatalogLayers()

	for _, provider := range []string{"moonshotai", "moonshotai-cn"} {
		m, err := FindModel(provider, "k3-256k")
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if m.ContextWindow != 262144 {
			t.Errorf("%s ContextWindow = %d, want 262144", provider, m.ContextWindow)
		}
		if !m.Reasoning {
			t.Errorf("%s must be reasoning-capable", provider)
		}
		if m.PriceInput == 0 || m.PriceOutput == 0 {
			t.Errorf("%s is pay-per-token; prices must be non-zero, got in=%v out=%v",
				provider, m.PriceInput, m.PriceOutput)
		}
	}
}
