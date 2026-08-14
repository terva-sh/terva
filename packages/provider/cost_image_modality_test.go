package provider

import "testing"

// The image-generating models bill their output at two rates on the SAME
// model, in the SAME response: gemini-3.1-flash-image is $3/1M for text and
// thinking but $60/1M for the image it just drew. A single PriceOutput cannot
// describe that, and both ways of collapsing it are wrong by the same 20x —
// price everything at the text rate and an image turn under-reports, price
// everything at the image rate and the ordinary conversation these models hold
// between pictures over-reports.
//
// Measured live before this split existed (1024x1024, gemini-3.1-flash-image):
// candidatesTokenCount 1450, candidatesTokensDetails [{modality:IMAGE,
// tokenCount:1120}]. So image tokens are a SUBSET of the output total, and the
// remaining 330 are the text and thinking that came with the picture.

// nanoBanana is that measured response, priced at the published rates.
func nanoBanana() (Model, Usage) {
	m := Model{
		Provider:         "google",
		ID:               "gemini-3.1-flash-image",
		PriceInput:       0.50,
		PriceOutput:      3.00,
		PriceOutputImage: 60.00,
	}
	u := Usage{InputTokens: 7, OutputTokens: 1450, ImageOutputTokens: 1120}
	return m, u
}

func TestImageTokensAreBilledAtTheImageRate(t *testing.T) {
	m, u := nanoBanana()
	const per = 1_000_000.0
	// 7 in, 330 text out, 1120 image out — each at its own rate.
	want := 7*0.50/per + 330*3.00/per + 1120*60.00/per
	got := ComputeCost(m, u)
	if !nearlyEqual(got, want) {
		t.Errorf("ComputeCost = %.10f, want %.10f", got, want)
	}
	// The failure this guards against is collapsing to one rate. Both
	// collapses are named here so a regression says which way it went.
	allText := float64(u.OutputTokens) * m.PriceOutput / per
	allImage := float64(u.OutputTokens) * m.PriceOutputImage / per
	outputOnly := got - 7*0.50/per
	if nearlyEqual(outputOnly, allText) {
		t.Error("output was billed entirely at the TEXT rate: an image turn under-reports ~20x")
	}
	if nearlyEqual(outputOnly, allImage) {
		t.Error("output was billed entirely at the IMAGE rate: the text tail over-reports ~20x")
	}
}

// The two rates must PARTITION OutputTokens. Billing the full output at the
// text rate and then adding the image tokens on top would charge the image
// twice — the easy mistake, since ImageOutputTokens reads like a fifth bucket
// and is actually a subset.
func TestImageTokensAreNotBilledTwice(t *testing.T) {
	m, u := nanoBanana()
	const per = 1_000_000.0
	// Compare OUTPUT against OUTPUT. ComputeCost also bills the prompt, and
	// an earlier version of this test forgot that: it measured the full cost
	// against an output-only figure, so the two could never be equal and the
	// test could not fail. A double-billing probe caught it passing.
	input := float64(u.InputTokens) * m.PriceInput / per
	outputOnly := ComputeCost(m, u) - input
	doubled := float64(u.OutputTokens)*m.PriceOutput/per + float64(u.ImageOutputTokens)*m.PriceOutputImage/per
	if nearlyEqual(outputOnly, doubled) {
		t.Errorf("image tokens billed on top of the full output total (output cost %.10f): they are a subset of it, not a fifth bucket", outputOnly)
	}
}

// Every model in the catalog but the image generators has one output rate, and
// this change must not move a single one of their bills. PriceOutputImage == 0
// is that promise.
func TestAModelWithoutAnImageRateIsPricedExactlyAsBefore(t *testing.T) {
	m := Model{PriceInput: 5, PriceOutput: 25, PriceCacheRead: 0.5, PriceCacheWrite: 6.25}
	const per = 1_000_000.0
	for _, u := range []Usage{
		{InputTokens: 1000, OutputTokens: 2000},
		{InputTokens: 1000, OutputTokens: 2000, CacheReadTokens: 500, CacheWriteTokens: 250},
		// A stray ImageOutputTokens on a model with no image rate must be
		// inert, not a discount: the old formula had no such field.
		{InputTokens: 1000, OutputTokens: 2000, ImageOutputTokens: 1500},
	} {
		want := float64(u.InputTokens)*m.PriceInput/per +
			float64(u.OutputTokens)*m.PriceOutput/per +
			float64(u.CacheReadTokens)*m.PriceCacheRead/per +
			float64(u.CacheWriteTokens)*m.PriceCacheWrite/per
		if got := ComputeCost(m, u); !nearlyEqual(got, want) {
			t.Errorf("usage %+v: ComputeCost = %.10f, want the pre-split %.10f", u, got, want)
		}
	}
}

// A text-only turn on an image model is the direction that a naive "just use
// the image rate" fix gets wrong, and these models do hold text conversations.
func TestATextOnlyTurnOnAnImageModelPaysTheTextRate(t *testing.T) {
	m, _ := nanoBanana()
	u := Usage{InputTokens: 10, OutputTokens: 1000} // no image emitted
	const per = 1_000_000.0
	want := 10*0.50/per + 1000*3.00/per
	if got := ComputeCost(m, u); !nearlyEqual(got, want) {
		t.Errorf("text-only turn cost %.10f, want %.10f", got, want)
	}
}

// A provider that reports a subset larger than its own total is describing
// something this code does not model. The clamp must not let the text
// remainder go negative and refund the difference.
func TestAnOversizedImageSubsetNeverRefunds(t *testing.T) {
	m, _ := nanoBanana()
	u := Usage{OutputTokens: 100, ImageOutputTokens: 900}
	got := ComputeCost(m, u)
	if got < 0 {
		t.Errorf("cost went negative (%.10f): an over-large subset refunded money", got)
	}
	if want := 100 * 60.00 / 1_000_000.0; !nearlyEqual(got, want) {
		t.Errorf("cost = %.10f, want the whole output at the image rate %.10f", got, want)
	}
}

// The breakdown has to survive aggregation, or a session total silently loses
// the split the moment it sums two responses.
func TestImageTokensSurviveUsageAdd(t *testing.T) {
	a := Usage{OutputTokens: 1450, ImageOutputTokens: 1120}
	b := Usage{OutputTokens: 1450, ImageOutputTokens: 1120}
	if got := a.Add(b); got.ImageOutputTokens != 2240 {
		t.Errorf("ImageOutputTokens after Add = %d, want 2240", got.ImageOutputTokens)
	}
}

func nearlyEqual(a, b float64) bool {
	const eps = 1e-12
	d := a - b
	return d < eps && d > -eps
}
