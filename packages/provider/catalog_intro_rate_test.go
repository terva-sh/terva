package provider

import (
	"testing"
	"time"
)

// This guard reads the clock ON PURPOSE. It is not flaky and it must not be
// "fixed" by pinning the time.
//
// The Gemini 3.6/3.7 Flash rows carry an introductory price with a published
// end date, after which input and output both double. Every other stale price
// in the catalog announces itself — the model 404s, or the number looks wrong
// next to a bill. This one does not: the request keeps working and terva
// reports half of what was actually charged, indefinitely, which is precisely
// the failure a cost breakdown exists to prevent.
//
// A date that lives only in a comment is a date nobody meets. So the day after
// the expiry, this fails and says what to change. If you are reading this
// because it went red: check the published rate, update the rows and the
// constants together, and the guard goes green on the other branch.
func TestTheIntroductoryFlashRatesExpireLoudly(t *testing.T) {
	expiry, err := time.Parse("2006-01-02", geminiIntroRateExpiry)
	if err != nil {
		t.Fatalf("geminiIntroRateExpiry %q is not a date: %v", geminiIntroRateExpiry, err)
	}
	// The rate holds THROUGH the expiry day; the change lands the day after.
	expired := time.Now().After(expiry.AddDate(0, 0, 1))

	for _, id := range geminiIntroRateModels {
		m, err := FindModel("google", id)
		if err != nil {
			t.Errorf("%s: not in the catalog, so the rate it is priced at cannot be checked: %v", id, err)
			continue
		}
		wantIn, wantOut := geminiFlashIntroInput, geminiFlashIntroOutput
		when := "during the introductory period (through " + geminiIntroRateExpiry + ")"
		if expired {
			wantIn, wantOut = geminiFlashPostIntroInput, geminiFlashPostIntroOutput
			when = "now that the introductory period has ended (" + geminiIntroRateExpiry + ")"
		}
		if m.PriceInput != wantIn {
			t.Errorf("%s: PriceInput = %v, want %v %s — every session on this model is being misbilled until the row is updated",
				id, m.PriceInput, wantIn, when)
		}
		if m.PriceOutput != wantOut {
			t.Errorf("%s: PriceOutput = %v, want %v %s — every session on this model is being misbilled until the row is updated",
				id, m.PriceOutput, wantOut, when)
		}
	}
}

// The post-expiry rate has to actually be a rise, or the guard above would
// happily accept a "correction" that changed nothing and go quiet again for
// good. It is the only thing standing between a copy-paste and a permanently
// silent misbill.
func TestThePostIntroductoryRatesAreHigherThanTheIntroductoryOnes(t *testing.T) {
	if geminiFlashPostIntroInput <= geminiFlashIntroInput {
		t.Errorf("post-introductory input %v is not above the introductory %v; the expiry guard would accept the stale price",
			geminiFlashPostIntroInput, geminiFlashIntroInput)
	}
	if geminiFlashPostIntroOutput <= geminiFlashIntroOutput {
		t.Errorf("post-introductory output %v is not above the introductory %v; the expiry guard would accept the stale price",
			geminiFlashPostIntroOutput, geminiFlashIntroOutput)
	}
}

// The rolling aliases bill as whatever they resolve to, and gemini-flash-latest
// resolves to a model on the introductory rate — so it inherits the same expiry
// and the same silent-misbill risk. Pinning the equality here means repricing
// the target without repricing the alias fails rather than passing quietly.
func TestTheFlashAliasTracksItsIntroductoryTarget(t *testing.T) {
	alias, err := FindModel("google", "gemini-flash-latest")
	if err != nil {
		t.Fatalf("gemini-flash-latest is not in the catalog: %v", err)
	}
	target, err := FindModel("google", "gemini-3.7-flash")
	if err != nil {
		t.Fatalf("gemini-3.7-flash is not in the catalog: %v", err)
	}
	if alias.PriceInput != target.PriceInput || alias.PriceOutput != target.PriceOutput {
		t.Fatalf("gemini-flash-latest is priced %v/%v but resolves to gemini-3.7-flash at %v/%v — "+
			"an alias bills as its target, so one of these rows is wrong",
			alias.PriceInput, alias.PriceOutput, target.PriceInput, target.PriceOutput)
	}
}
