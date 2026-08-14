package provider

import "testing"

// 🪤 Every one of these ids is live on the Google API and NONE of them was in
// the catalog. The resolver does not pass an unknown google id through — it
// warns and silently substitutes the default TEXT model:
//
//	terva: model "gemini-3.1-flash-image" is not in the active catalogue;
//	using "gemini-flash-latest" instead.
//
// So asking for a picture got a text model, which answered with an SVG in a
// code fence. The failure is quiet and the output looks deliberate, which is
// the worst combination. Measured live 2026-08-14.
var geminiImageModels = []string{
	"gemini-2.5-flash-image",
	"gemini-3-pro-image",
	"gemini-3-pro-image-preview",
	"gemini-3.1-flash-image",
	"gemini-3.1-flash-image-preview",
	"gemini-3.1-flash-lite-image",
}

func TestEveryLiveGeminiImageModelIsInTheCatalogue(t *testing.T) {
	for _, id := range geminiImageModels {
		m, err := FindModel("google", id)
		if err != nil {
			t.Errorf("%s is live on the API but absent from the catalogue: "+
				"the resolver will substitute a text model and answer an image request with prose (%v)", id, err)
			continue
		}
		if !m.Has(CapImageOutput) {
			t.Errorf("%s is not tagged image-output", id)
		}
	}
}

// These models exist to emit images, and images are the expensive half of
// their bill. A row without an image rate prices its pictures as prose.
func TestEveryGeminiImageModelPricesItsImages(t *testing.T) {
	for _, id := range geminiImageModels {
		m, err := FindModel("google", id)
		if err != nil {
			continue // reported by the test above
		}
		if m.PriceOutputImage <= 0 {
			t.Errorf("%s has PriceOutputImage %v: its images would bill at the text rate",
				id, m.PriceOutputImage)
		}
		if m.PriceInput <= 0 {
			t.Errorf("%s has PriceInput %v", id, m.PriceInput)
		}
		// The image rate is the expensive one on every model in this family;
		// a row where it is not is almost certainly a transcription slip.
		if m.PriceOutputImage <= m.PriceOutput {
			t.Errorf("%s: image rate %v is not above the text rate %v — check the row",
				id, m.PriceOutputImage, m.PriceOutput)
		}
	}
}

// The published per-image prices, reproduced from the token rates the catalog
// carries. Google quotes these models BOTH ways — "$60.00 (images)" per 1M and
// "$0.067 per 1K image" — and the two must reconcile, or the row is wrong.
// Image token counts are measured from live generations.
func TestGeminiImageRatesReproduceThePublishedPerImagePrice(t *testing.T) {
	cases := []struct {
		id           string
		imageTokens  int
		wantPerImage float64
		tolerance    float64
	}{
		// Published: "$0.039 per image", 1024x1024 = 1290 tokens.
		{"gemini-2.5-flash-image", 1290, 0.039, 0.0005},
		// Published: "$60.00 (images) ... $0.067 per 1K image", 1120 tokens.
		{"gemini-3.1-flash-image", 1120, 0.067, 0.0005},
		// Published: "$30.00 (images) ... $0.0336 per 1K resolution image".
		{"gemini-3.1-flash-lite-image", 1120, 0.0336, 0.0005},
		// Published: "$120.00 (images) ... $0.134 per 1K/2K image".
		{"gemini-3-pro-image", 1120, 0.134, 0.0005},
	}
	for _, tc := range cases {
		m, err := FindModel("google", tc.id)
		if err != nil {
			continue // reported above
		}
		got := float64(tc.imageTokens) * m.PriceOutputImage / 1_000_000.0
		if d := got - tc.wantPerImage; d > tc.tolerance || d < -tc.tolerance {
			t.Errorf("%s: %d tokens at $%v/1M = $%.4f per image, but Google publishes $%.4f",
				tc.id, tc.imageTokens, m.PriceOutputImage, got, tc.wantPerImage)
		}
	}
}

// Live discovery overwrites catalog rows for ids it returns, and the Google
// endpoint reports no prices at all. If the merge took the live row wholesale
// the image rate would vanish at runtime while every catalog test still passed
// — invisible except on the bill.
func TestDiscoveryDoesNotStripTheImageRate(t *testing.T) {
	const id = "gemini-3.1-flash-image"
	before, err := FindModel("google", id)
	if err != nil {
		t.Skipf("%s not in catalogue: %v", id, err)
	}
	// A discovery result of the shape DiscoverGoogle produces: an id and a
	// display name, no pricing.
	merged := MergeCatalog([]Model{{
		Provider: "google", ID: id, DisplayName: "Nano Banana 2", Source: "live",
	}})
	var got Model
	for _, m := range merged {
		if m.Provider == "google" && m.ID == id {
			got = m
			break
		}
	}
	if got.ID == "" {
		t.Fatalf("%s disappeared from the merged catalogue", id)
	}
	if got.PriceOutputImage != before.PriceOutputImage {
		t.Errorf("PriceOutputImage after discovery = %v, want the catalogue's %v: "+
			"a live row with no prices must not erase the image rate",
			got.PriceOutputImage, before.PriceOutputImage)
	}
	if !got.Has(CapImageOutput) {
		t.Error("image-output capability lost through the merge")
	}
}
