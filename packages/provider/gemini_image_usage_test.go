package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// geminiUsage drives one SSE response and returns the usage it reported.
func geminiUsage(t *testing.T, model, frame string) Usage {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + frame + "\n\n"))
	}))
	defer srv.Close()

	evs, err := NewGemini("k", srv.URL).Stream(context.Background(), Request{
		Model:    model,
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "draw a red square"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got Usage
	for ev := range evs {
		if e, ok := ev.(EventUsage); ok {
			got = e.Usage
		}
	}
	return got
}

// The exact usageMetadata measured live on 2026-08-14 from a 1024x1024
// generation on gemini-3.1-flash-image. The IMAGE breakdown is the only signal
// that distinguishes a picture from prose, and these models bill the two 20x
// apart.
const geminiImageUsageFrame = `{"candidates":[{"content":{"role":"model","parts":[{"text":"here"}]},"finishReason":"STOP"}],` +
	`"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":1450,"totalTokenCount":1457,` +
	`"promptTokensDetails":[{"modality":"TEXT","tokenCount":7}],` +
	`"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1120}]}}`

// 🪤 Without this the IMAGE breakdown is dropped on the floor and every
// generated image is billed at the model's TEXT rate — 20x under on
// gemini-3.1-flash-image, which is the whole reason the model exists.
func TestGeminiReportsImageOutputTokens(t *testing.T) {
	u := geminiUsage(t, "gemini-3.1-flash-image", geminiImageUsageFrame)

	if u.ImageOutputTokens != 1120 {
		t.Errorf("ImageOutputTokens = %d, want 1120 (the modality IMAGE breakdown)", u.ImageOutputTokens)
	}
	// A SUBSET of the output total, not a bucket beside it. If this ever
	// exceeds OutputTokens the cost split silently clamps.
	if u.OutputTokens != 1450 {
		t.Errorf("OutputTokens = %d, want 1450 (the candidate total)", u.OutputTokens)
	}
	if u.ImageOutputTokens > u.OutputTokens {
		t.Errorf("ImageOutputTokens %d exceeds OutputTokens %d: it must be a subset",
			u.ImageOutputTokens, u.OutputTokens)
	}
}

// The split is only worth parsing if it reaches the bill. This is the
// end-to-end statement: a real catalog model, a real response, real money.
func TestGeminiPricesAGeneratedImageAtTheImageRate(t *testing.T) {
	m, err := FindModel("google", "gemini-3.1-flash-image")
	if err != nil {
		t.Fatalf("gemini-3.1-flash-image missing from the catalog: %v", err)
	}
	if m.PriceOutputImage == 0 {
		t.Fatal("gemini-3.1-flash-image has no PriceOutputImage: its images would bill as text")
	}

	u := geminiUsage(t, "gemini-3.1-flash-image", geminiImageUsageFrame)
	const per = 1_000_000.0
	// 7 prompt, 330 text out, 1120 image out.
	want := 7*m.PriceInput/per + 330*m.PriceOutput/per + 1120*m.PriceOutputImage/per
	if !nearlyEqual(u.CostUSD, want) {
		t.Errorf("CostUSD = %.10f, want %.10f", u.CostUSD, want)
	}
	// Name the specific wrong answer, so a regression is legible.
	asText := 7*m.PriceInput/per + 1450*m.PriceOutput/per
	if nearlyEqual(u.CostUSD, asText) {
		t.Errorf("the image was billed at the text rate (%.10f): ~20x under", u.CostUSD)
	}
}

// A text-only response from the same model must not acquire image tokens from
// nowhere. Absent breakdown means zero, not "assume it was a picture".
func TestGeminiTextResponseHasNoImageTokens(t *testing.T) {
	frame := `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],` +
		`"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1000,"totalTokenCount":1010,` +
		`"candidatesTokensDetails":[{"modality":"TEXT","tokenCount":1000}]}}`
	u := geminiUsage(t, "gemini-3.1-flash-image", frame)

	if u.ImageOutputTokens != 0 {
		t.Errorf("ImageOutputTokens = %d on a TEXT-only response, want 0", u.ImageOutputTokens)
	}
	m, err := FindModel("google", "gemini-3.1-flash-image")
	if err != nil {
		t.Fatalf("model lookup: %v", err)
	}
	const per = 1_000_000.0
	want := 10*m.PriceInput/per + 1000*m.PriceOutput/per
	if !nearlyEqual(u.CostUSD, want) {
		t.Errorf("CostUSD = %.10f, want the text rate %.10f — these models talk as well as draw",
			u.CostUSD, want)
	}
}

// Every other model reports no modality breakdown at all. Their usage must be
// untouched by this parsing.
func TestGeminiTextModelUsageIsUnchanged(t *testing.T) {
	frame := `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}],` +
		`"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":2,"totalTokenCount":14}}`
	u := geminiUsage(t, "gemini-flash-latest", frame)

	if u.ImageOutputTokens != 0 {
		t.Errorf("ImageOutputTokens = %d with no breakdown present, want 0", u.ImageOutputTokens)
	}
	if u.InputTokens != 12 || u.OutputTokens != 2 {
		t.Errorf("usage = %d in / %d out, want 12/2", u.InputTokens, u.OutputTokens)
	}
}
