package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// usagePollTTL bounds how often a poll-based provider re-fetches its usage: the
// balance/limit barely moves between /usage opens, and the GET isn't free.
const usagePollTTL = 60 * time.Second

// usageFetcher pulls a provider's usage/balance from its dedicated endpoint.
type usageFetcher func(ctx context.Context) (UsageSnapshot, error)

// pollingUsageClient wraps a Client whose usage lives behind a separate
// endpoint (OpenRouter /api/v1/key, DeepSeek /user/balance, …) rather than in
// response headers. It fetches lazily and caches for ttl, exposing the result
// via UsageReporter (cached, non-blocking) and UsageRefresher (blocking
// fetch). Name/Stream/Unwrap delegate to inner, so every capability probe
// still sees the real client through clientAs.
type pollingUsageClient struct {
	inner Client
	fetch usageFetcher
	ttl   time.Duration

	mu      sync.Mutex
	snap    UsageSnapshot
	have    bool
	fetched time.Time
}

func newPollingUsageClient(inner Client, ttl time.Duration, fetch usageFetcher) *pollingUsageClient {
	return &pollingUsageClient{inner: inner, fetch: fetch, ttl: ttl}
}

func (c *pollingUsageClient) Name() string   { return c.inner.Name() }
func (c *pollingUsageClient) Unwrap() Client { return c.inner }
func (c *pollingUsageClient) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	return c.inner.Stream(ctx, req)
}

// UsageSnapshot returns the cached credits merged with the inner client's
// (passively-observed) snapshot — e.g. OpenRouter's credits + its rate-limit
// windows. Non-blocking; ok=false until either source has data.
func (c *pollingUsageClient) UsageSnapshot() (UsageSnapshot, bool) {
	credits, have := c.cachedCredits()
	inner, innerOK := ClientUsage(c.inner)
	return mergeUsage(credits, have, inner, innerOK)
}

// RefreshUsage refreshes the credits (TTL-aware, blocking) and merges them with
// the inner client's snapshot, so /usage shows credits AND rate-limit windows
// together.
func (c *pollingUsageClient) RefreshUsage(ctx context.Context) (UsageSnapshot, bool) {
	credits, have := c.fetchCredits(ctx)
	inner, innerOK := ClientUsage(c.inner)
	return mergeUsage(credits, have, inner, innerOK)
}

// cachedCredits returns the cached credit snapshot without fetching.
func (c *pollingUsageClient) cachedCredits() (UsageSnapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snap, c.have
}

// fetchCredits returns the credit snapshot, fetching when the cache is older
// than ttl. On error it keeps and returns the last good snapshot, so a
// transient failure doesn't blank the UI.
func (c *pollingUsageClient) fetchCredits(ctx context.Context) (UsageSnapshot, bool) {
	c.mu.Lock()
	if c.have && time.Since(c.fetched) < c.ttl {
		snap, have := c.snap, c.have
		c.mu.Unlock()
		return snap, have
	}
	c.mu.Unlock()

	snap, err := c.fetch(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		return c.snap, c.have
	}
	c.snap = snap
	c.have = true
	c.fetched = time.Now()
	return c.snap, true
}

// usageGetJSON GETs url with Bearer auth and decodes the JSON body into out.
func usageGetJSON(ctx context.Context, httpc *http.Client, url, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("usage GET %s: status %d: %s", url, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// fetchOpenRouterUsage reads GET {baseURL}/key (the normal inference key
// works — no management key needed). `limit`/`limit_remaining` give the
// per-key credit cap when one is set; `usage` is lifetime spend.
// https://openrouter.ai/docs/api/reference/limits
func fetchOpenRouterUsage(httpc *http.Client, apiKey, baseURL string) usageFetcher {
	keyURL := strings.TrimRight(baseURL, "/") + "/key"
	return func(ctx context.Context) (UsageSnapshot, error) {
		var body struct {
			Data struct {
				Limit          *float64 `json:"limit"`
				LimitRemaining *float64 `json:"limit_remaining"`
				Usage          float64  `json:"usage"`
			} `json:"data"`
		}
		if err := usageGetJSON(ctx, httpc, keyURL, apiKey, &body); err != nil {
			return UsageSnapshot{}, err
		}
		cr := &Credits{Used: body.Data.Usage}
		if body.Data.Limit != nil { // a credit cap is set on the key
			cr.HasCredits = true
			if body.Data.LimitRemaining != nil {
				cr.Balance = *body.Data.LimitRemaining
			}
		}
		return UsageSnapshot{Provider: "openrouter", Credits: cr, CapturedAt: time.Now()}, nil
	}
}

// fetchDeepSeekBalance reads GET {host}/user/balance (NOT under the chat /v1
// path). Prefers the USD balance, else the first reported currency.
// https://api-docs.deepseek.com/api/get-user-balance
func fetchDeepSeekBalance(httpc *http.Client, apiKey, baseURL string) usageFetcher {
	balURL := deepseekBalanceURL(baseURL)
	return func(ctx context.Context) (UsageSnapshot, error) {
		var body struct {
			IsAvailable  bool `json:"is_available"`
			BalanceInfos []struct {
				Currency     string `json:"currency"`
				TotalBalance string `json:"total_balance"`
			} `json:"balance_infos"`
		}
		if err := usageGetJSON(ctx, httpc, balURL, apiKey, &body); err != nil {
			return UsageSnapshot{}, err
		}
		var bal float64
		picked := false
		for _, bi := range body.BalanceInfos {
			v, err := strconv.ParseFloat(strings.TrimSpace(bi.TotalBalance), 64)
			if err != nil {
				continue
			}
			if strings.EqualFold(bi.Currency, "USD") {
				bal, picked = v, true
				break
			}
			if !picked {
				bal, picked = v, true
			}
		}
		return UsageSnapshot{Provider: "deepseek", Credits: &Credits{HasCredits: picked, Balance: bal}, CapturedAt: time.Now()}, nil
	}
}

// deepseekBalanceURL points at the account-balance endpoint at the host root:
// the chat base URL ends in /v1, but /user/balance does not.
func deepseekBalanceURL(baseURL string) string {
	if u, err := url.Parse(baseURL); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Scheme + "://" + u.Host + "/user/balance"
	}
	return "https://api.deepseek.com/user/balance"
}
