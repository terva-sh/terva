package provider

import "testing"

// The failure this guards against: opencode's /v1/models list names its models
// but carries no limits (the OpenAI list schema has none), so a model the baked
// catalog is missing gets discovered, has no context window to report, and falls
// back to a guessed default. That is how GLM-5.2 — a million-token model — was
// budgeted as 32k, and terva compacted a conversation that had barely started.
//
// The catalog is kept in step with models.dev by `just models-sync`. These pin
// the outcome, so a regression is a test failure rather than a silently
// crippled context window.
func TestOpenCodeGoCatalogHasRealContextWindows(t *testing.T) {
	want := map[string]int{
		// The ones that were missing entirely, and so were guessed at.
		"glm-5.2":        1000000,
		"kimi-k2.7-code": 262144,
		// Present all along; here so a resync cannot quietly shrink them.
		"deepseek-v4-pro":   1000000,
		"deepseek-v4-flash": 1000000,
		"mimo-v2.5":         1000000,
		"mimo-v2.5-pro":     1048576,

		// Raised from 512000 when the generator switched to models.dev's
		// dedicated "opencode-go" key. The old shared "opencode" key was the
		// outlier: of the ten providers publishing this model, seven put it at
		// 1M or above, and the gateway's own key is one of them. This is the
		// only pin the switch moved, and it moves UPWARD — the direction that
		// compacts later rather than earlier, so it is the one worth naming.
		"minimax-m3": 1000000,

		// The current Go lineup. Every one of these was served while no source
		// the generator consulted could describe it, so each was budgeted at
		// unknownModelContext (32k) — glm-5.3-flash is the model the Go landing
		// page headlines, and it was being run in a thirtieth of its window.
		"glm-5.3":                    1000000,
		"glm-5.3-flash":              1000000,
		"kimi-k3":                    1048576,
		"hy4-preview":                1024000,
		"longcat-2.0":                1000000,
		"muse-spark-1.2-contributor": 1048576,
		// Also 262144 under the old key; the vendor's own alibaba key agrees
		// with the 1M the gateway key reports.
		"qwen3.6-plus": 1000000,

		// Hand-curated. The gateway serves hy3-preview but models.dev does not
		// carry it under ANY opencode key, so reconcile keeps whatever the
		// catalog already said and a blind regenerate would drop it — which is
		// the whole reason the generator fills gaps instead of replacing
		// wholesale. The limits come from models.dev's tencent-tokenhub key and
		// agree exactly with its sibling hy3 below.
		"hy3":         256000,
		"hy3-preview": 256000,
	}

	got := map[string]Model{}
	for _, m := range Catalog {
		if m.Provider == "opencode-go" {
			got[m.ID] = m
		}
	}

	for id, ctx := range want {
		m, ok := got[id]
		if !ok {
			t.Errorf("opencode-go/%s missing from the catalog — it would be "+
				"discovered live with a guessed context window", id)
			continue
		}
		if m.ContextWindow != ctx {
			t.Errorf("opencode-go/%s context = %d, want %d", id, m.ContextWindow, ctx)
		}
	}

	// Nothing may carry a zero window: a baked entry shadows the live one, so a
	// zero here is worse than no entry at all.
	for id, m := range got {
		if m.ContextWindow == 0 {
			t.Errorf("opencode-go/%s has a zero context window", id)
		}
	}
}

// Every opencode-go model must point at the /v1 base. minimax-m2.5 was pinned
// at "https://opencode.ai/zen/go" — no /v1 — while all eleven of its siblings
// had it. The generator now stamps the base uniformly, which is the point.
func TestOpenCodeGoBaseURLsAreUniform(t *testing.T) {
	const want = "https://opencode.ai/zen/go/v1"
	for _, m := range Catalog {
		if m.Provider != "opencode-go" {
			continue
		}
		if m.BaseURL != want {
			t.Errorf("opencode-go/%s BaseURL = %q, want %q", m.ID, m.BaseURL, want)
		}
	}
}
