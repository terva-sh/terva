package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The Anthropic-Messages client is shared by every compatible third party
// (kimi-coding, minimax, fireworks, vercel-ai-gateway). Its errors must name
// the PROVIDER that answered, not the wire format it speaks.
//
// The regression: a kimi subscription whose access token had expired 401'd, and
// the failure reached the rescue picker as "anthropic/k3-256k" — naming a vendor
// the request never reached, and one whose login the user then went and checked.
// The client already used c.Name() for its model lookup and EventStart; only the
// error sites were hardcoded, which is why nothing else looked wrong.
func TestAnthropicCompatErrorsNameTheProviderThatAnswered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_authentication_error","message":"The API Key appears to be invalid or may have expired."}}`))
	}))
	defer srv.Close()

	for _, tc := range []struct {
		name   string
		client Client
		want   string
	}{
		{"kimi", NewKimiCodingWithHeaders("dead-token", srv.URL, nil), "kimi"},
		{"minimax", NewMinimaxAnthropic("k", srv.URL), "minimax"},
		{"fireworks", NewFireworksAnthropic("k", srv.URL), "fireworks"},
		// Anthropic itself keeps the name it always had — the fix is a
		// generalization, not a rename.
		{"anthropic", NewAnthropic("k", srv.URL), "anthropic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.client.Stream(context.Background(), Request{Model: "k3-256k"})
			if err == nil {
				t.Fatal("Stream succeeded against a 401")
			}
			var pe *ProviderError
			if !errors.As(err, &pe) {
				t.Fatalf("error is not a *ProviderError: %v", err)
			}
			if pe.Provider != tc.want {
				t.Errorf("ProviderError.Provider = %q, want %q (error: %v)", pe.Provider, tc.want, err)
			}
		})
	}
}
