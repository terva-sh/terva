package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// OpenRouter's GET /key maps to Credits: limit_remaining → Balance (only when
// a key cap is set), usage → Used (always).
func TestFetchOpenRouterUsage(t *testing.T) {
	cases := []struct {
		name           string
		body           string
		wantHasCredits bool
		wantBalance    float64
		wantUsed       float64
	}{
		{"capped key", `{"data":{"limit":50,"limit_remaining":37.5,"usage":12.5}}`, true, 37.5, 12.5},
		{"uncapped key", `{"data":{"limit":null,"limit_remaining":null,"usage":8.25}}`, false, 0, 8.25},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/key" {
					http.NotFound(w, r)
					return
				}
				if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
					t.Errorf("auth header = %q, want Bearer sk-test", got)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			snap, err := fetchOpenRouterUsage(&http.Client{}, "sk-test", srv.URL)(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if snap.Provider != "openrouter" || snap.Credits == nil {
				t.Fatalf("snap = %+v", snap)
			}
			c := snap.Credits
			if c.HasCredits != tc.wantHasCredits || c.Balance != tc.wantBalance || c.Used != tc.wantUsed {
				t.Errorf("credits = %+v; want HasCredits=%v Balance=%v Used=%v", c, tc.wantHasCredits, tc.wantBalance, tc.wantUsed)
			}
		})
	}
}

// DeepSeek's balance lives at the host root (/user/balance, not /v1) and may
// report multiple currencies; USD is preferred.
func TestFetchDeepSeekBalance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/balance" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"100.00"},{"currency":"USD","total_balance":"13.66"}]}`))
	}))
	defer srv.Close()

	// baseURL ends in /v1 (the chat path); the balance URL is derived to root.
	snap, err := fetchDeepSeekBalance(&http.Client{}, "sk", srv.URL+"/v1")(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Credits == nil || !snap.Credits.HasCredits || snap.Credits.Balance != 13.66 {
		t.Errorf("want USD balance 13.66, got %+v", snap.Credits)
	}
}

// The TTL cache: a refresh inside the TTL serves the cache instead of
// re-hitting the endpoint, and the passive getter never blocks on one.
//
// This test used to assert that the passive getter never fetched AT ALL, and
// that was the bug rather than the contract: for a poll-family provider the
// passive read is the only thing the status bar and the context breakdown ever
// call, so "never fetches" meant "never updates". It now warms in the
// background — still returning immediately, still serving whatever is cached at
// the moment of the call — so what this pins is that the READ does not block
// and does not re-fetch behind a warm cache. Cold-start warming has its own
// tests in usage_warm_test.go.
func TestPollingUsageClientTTL(t *testing.T) {
	var calls atomic.Int64 // a background warm shares this counter now
	fetch := func(ctx context.Context) (UsageSnapshot, error) {
		return UsageSnapshot{Provider: "x", Credits: &Credits{Balance: float64(calls.Add(1))}}, nil
	}
	c := newPollingUsageClient(nil, time.Minute, fetch) // inner unused by these methods

	s1, ok := c.RefreshUsage(context.Background())
	if !ok || calls.Load() != 1 {
		t.Fatalf("first refresh: ok=%v calls=%d, want ok + 1 fetch", ok, calls.Load())
	}
	s2, _ := c.RefreshUsage(context.Background())
	if calls.Load() != 1 {
		t.Errorf("second refresh within TTL re-fetched; calls=%d", calls.Load())
	}
	if s1.Credits.Balance != s2.Credits.Balance {
		t.Errorf("cached snapshot changed across TTL: %v vs %v", s1.Credits.Balance, s2.Credits.Balance)
	}

	// The passive getter serves that same cache, and does not spend a fetch to
	// do it: the refresh above left the cache inside its TTL.
	if snap, ok := c.UsageSnapshot(); !ok || snap.Credits.Balance != 1 || calls.Load() != 1 {
		t.Errorf("cached getter = %+v ok=%v calls=%d", snap, ok, calls.Load())
	}
}

// usageReporterStub is an inner Client that passively reports a snapshot
// (stands in for an openaiClient with rate-limit windows).
type usageReporterStub struct{ snap UsageSnapshot }

func (usageReporterStub) Name() string { return "stub" }
func (usageReporterStub) Stream(context.Context, Request) (<-chan Event, error) {
	return nil, nil
}
func (s usageReporterStub) UsageSnapshot() (UsageSnapshot, bool) { return s.snap, true }

// The wrapper merges its polled credits with the inner client's rate-limit
// windows, instead of shadowing them (phase 3).
func TestPollingUsageClientMergesInner(t *testing.T) {
	inner := usageReporterStub{snap: UsageSnapshot{
		Provider: "openrouter",
		Windows:  []UsageWindow{{Label: "requests", Kind: WindowRateLimit, UsedPercent: 30}},
	}}
	fetch := func(ctx context.Context) (UsageSnapshot, error) {
		return UsageSnapshot{Provider: "openrouter", Credits: &Credits{Balance: 9}}, nil
	}
	c := newPollingUsageClient(inner, time.Minute, fetch)

	// Before any credit fetch, the inner rate-limit window still surfaces.
	if snap, ok := c.UsageSnapshot(); !ok || len(snap.Windows) != 1 || snap.Credits != nil {
		t.Fatalf("pre-fetch = %+v ok=%v (want inner window, no credits)", snap, ok)
	}

	// After a refresh: credits AND the inner rate-limit window, merged.
	merged, ok := c.RefreshUsage(context.Background())
	if !ok || merged.Credits == nil || merged.Credits.Balance != 9 || len(merged.Windows) != 1 {
		t.Fatalf("merged = %+v ok=%v (want credits + 1 window)", merged, ok)
	}
	if merged.Windows[0].Kind != WindowRateLimit {
		t.Errorf("merged window kind = %d, want WindowRateLimit", merged.Windows[0].Kind)
	}
}
