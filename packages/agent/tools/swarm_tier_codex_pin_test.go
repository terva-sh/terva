package tools

import "testing"

// The openai-codex rungs name a model, and this says which one. It is the
// codex counterpart of TestAnthropicTierRungsResolveToTheirPin; read that
// test for why a resolved-id assertion exists at all.
//
// Codex names are already pinned, since sol / luna say nothing across
// generations, so the risk here is not a bare family word catching the
// wrong row. It is the list order. GPT-6 was added to the catalog one
// commit before it was put on the ladder, and in between the rungs stayed
// on GPT-5.6 with the suite green. Which generation a `tier: strong` spawn
// gets is a choice (TKT-01M35F3Y0VWD1W8D6S90HB2P9B), so it is stated here.
//
// EDIT this test, do not delete it, when the pin advances: change the list
// in swarmTierFamilies and the constants here in the same diff. The same
// goes for each rung's effort, which the operator set alongside the model.
func TestCodexTierRungsResolveToTheirPin(t *testing.T) {
	const (
		wantWeak   = "gpt-6-luna"
		wantMedium = "gpt-6-sol"
		wantStrong = "gpt-6-sol"
		wantCheap  = "gpt-6-luna"
	)

	for _, tc := range []struct {
		rung, want, effort, fallback string
	}{
		{"weak", wantWeak, "medium", "gpt-5.6-luna"},
		{"medium", wantMedium, "low", "gpt-5.6-sol"},
		{"strong", wantStrong, "high", "gpt-5.6-sol"},
		// Luna undercuts the mini, and TestCheapTierIsNotDearerThanTheLadder
		// holds cheap to the bottom of the price range.
		{"cheap", wantCheap, "low", "mini"},
	} {
		fam, listed := swarmTierFamilies["openai-codex"][tc.rung]
		if !listed {
			t.Errorf("openai-codex has no %s rung", tc.rung)
			continue
		}
		if got := fam.resolve("openai-codex"); got != tc.want {
			t.Errorf("openai-codex/%s resolves to %q, want %q.\n"+
				"If this is a deliberate move, update the list in swarmTierFamilies "+
				"and the constant in this test together.", tc.rung, got, tc.want)
		}
		// The effort is half the pick, and chosen with the model.
		if fam.reasoning != tc.effort {
			t.Errorf("openai-codex/%s runs at %q, want %q", tc.rung, fam.reasoning, tc.effort)
		}

		// The previous generation must still be on the list, below the pin,
		// so a catalog without the GPT-6 rows degrades a generation rather
		// than falling to whatever else the list happens to hold. This is a
		// catalog fallback, not an entitlement one: the builtin GPT-6 rows are
		// always present, so an account without GPT-6 still resolves to it.
		pin, prev := -1, -1
		for i, m := range fam.match {
			switch m {
			case tc.want:
				pin = i
			case tc.fallback:
				prev = i
			}
		}
		if pin < 0 || prev < 0 || prev < pin {
			t.Errorf("openai-codex/%s match list %v: want %q listed above the %q fall-through",
				tc.rung, fam.match, tc.want, tc.fallback)
		}
		// An earlier entry that itself matches the pin shadows it, since the
		// list is walked in order.
		if pin > 0 {
			for _, earlier := range fam.match[:pin] {
				if (tierFamily{match: []string{earlier}}).matches(tc.want) {
					t.Errorf("openai-codex/%s lists %q above the pinned %q, and it already matches it",
						tc.rung, earlier, tc.want)
				}
			}
		}
	}
}
