package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A representative /wham/rate-limit-reset-credits body (shape captured from a
// live account, ids and values trimmed).
const codexCreditsJSON = `{
  "credits": [
    {"id":"RateLimitResetCredit_a","reset_type":"codex_rate_limits","status":"available",
     "granted_at":"2026-06-18T00:37:30.242601Z","expires_at":"2026-07-18T00:37:30.242601Z",
     "redeem_started_at":null,"redeemed_at":null,
     "title":"Full reset (Weekly + 5 hr)","description":"one free reset"},
    {"id":"RateLimitResetCredit_b","reset_type":"codex_rate_limits","status":"redeemed",
     "granted_at":"2026-06-01T00:00:00Z","expires_at":"2026-07-01T00:00:00Z",
     "redeem_started_at":"2026-06-02T00:00:00Z","redeemed_at":"2026-06-02T00:00:01Z",
     "title":"Full reset (Weekly + 5 hr)","description":"one free reset"}
  ],
  "available_count": 1,
  "total_earned_count": 0
}`

func codexTestClient(t *testing.T, h http.HandlerFunc) Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	// Constructor takes the responses URL; the wham calls derive /backend-api
	// from it, so point it at <server>/backend-api/codex/responses and the
	// account calls land on <server>/backend-api/wham/*.
	return NewOpenAICodex("tok", "acct-123", srv.URL+"/backend-api/codex/responses")
}

func TestCodexListResets(t *testing.T) {
	c := codexTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/rate-limit-reset-credits" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("auth header = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "acct-123" {
			t.Errorf("account header = %q", got)
		}
		io.WriteString(w, codexCreditsJSON)
	})

	resets, err := ClientListResets(context.Background(), c)
	if err != nil {
		t.Fatalf("ListResets: %v", err)
	}
	if len(resets) != 2 {
		t.Fatalf("got %d resets, want 2", len(resets))
	}

	a := resets[0]
	if a.ID != "RateLimitResetCredit_a" || a.Status != ResetAvailable {
		t.Errorf("credit a = %+v, want available", a)
	}
	if a.Title != "Full reset (Weekly + 5 hr)" || a.Kind != "codex_rate_limits" {
		t.Errorf("credit a labels = %q / %q", a.Title, a.Kind)
	}
	want := time.Date(2026, 7, 18, 0, 37, 30, 242601000, time.UTC)
	if !a.ExpiresAt.Equal(want) {
		t.Errorf("credit a expires = %v, want %v", a.ExpiresAt, want)
	}
	if !a.RedeemedAt.IsZero() {
		t.Errorf("available credit should have zero RedeemedAt, got %v", a.RedeemedAt)
	}

	if b := resets[1]; b.Status != ResetRedeemed || b.RedeemedAt.IsZero() {
		t.Errorf("credit b = %+v, want redeemed with RedeemedAt", b)
	}
}

func TestCodexConsumeReset(t *testing.T) {
	var gotBody map[string]string
	c := codexTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			if r.Method != http.MethodPost {
				t.Errorf("consume method = %s", r.Method)
			}
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			io.WriteString(w, `{"code":"reset","windows_reset":2,"credit":{
				"id":"RateLimitResetCredit_a","reset_type":"codex_rate_limits",
				"status":"redeemed","redeemed_at":"2026-07-10T12:00:00Z",
				"title":"Full reset (Weekly + 5 hr)"}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})

	res, err := ClientConsumeReset(context.Background(), c, "RateLimitResetCredit_a")
	if err != nil {
		t.Fatalf("ConsumeReset: %v", err)
	}
	if res.WindowsReset != 2 {
		t.Errorf("windows reset = %d, want 2", res.WindowsReset)
	}
	if res.Reset.Status != ResetRedeemed || res.Reset.RedeemedAt.IsZero() {
		t.Errorf("consumed credit = %+v, want redeemed", res.Reset)
	}
	if gotBody["credit_id"] != "RateLimitResetCredit_a" {
		t.Errorf("body credit_id = %q", gotBody["credit_id"])
	}
	// The redeem id is deterministic from the credit id (idempotency by
	// construction), so the server sees exactly what codexRedeemRequestID makes.
	if want := codexRedeemRequestID("RateLimitResetCredit_a"); gotBody["redeem_request_id"] != want {
		t.Errorf("redeem_request_id = %q, want deterministic %q", gotBody["redeem_request_id"], want)
	}
}

func TestCodexConsumeResetPropagatesHTTPError(t *testing.T) {
	c := codexTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"detail":"credit already redeemed"}`)
	})
	_, err := ClientConsumeReset(context.Background(), c, "RateLimitResetCredit_a")
	if err == nil {
		t.Fatal("expected error on 409")
	}
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Status != http.StatusConflict {
		t.Errorf("err = %v, want ProviderError 409", err)
	}
}

func TestCodexRedeemRequestIDDeterministic(t *testing.T) {
	if a, b := codexRedeemRequestID("cr_x"), codexRedeemRequestID("cr_x"); a != b {
		t.Errorf("same credit gave different ids: %q vs %q", a, b)
	}
	if a, b := codexRedeemRequestID("cr_x"), codexRedeemRequestID("cr_y"); a == b {
		t.Error("different credits gave the same id")
	}
	// Valid UUID shape (36 chars, 4 hyphens) so the server accepts it.
	if id := codexRedeemRequestID("cr_x"); len(id) != 36 {
		t.Errorf("redeem id %q is not a uuid", id)
	}
}

func TestCodexWhamBase(t *testing.T) {
	cases := map[string]string{
		"https://chatgpt.com/backend-api/codex/responses":  "https://chatgpt.com/backend-api",
		"https://chatgpt.com/backend-api/codex/responses/": "https://chatgpt.com/backend-api",
		"https://proxy.local/v1/responses":                 "https://proxy.local/v1",
		"https://exotic.local/gateway":                     "https://exotic.local/gateway",
	}
	for in, want := range cases {
		if got := codexWhamBase(in); got != want {
			t.Errorf("codexWhamBase(%q) = %q, want %q", in, got, want)
		}
	}
}
