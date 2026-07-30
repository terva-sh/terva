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

// usageWarmTimeout bounds a background warm. Nothing waits on it, so it must
// not be able to hold a goroutine open indefinitely against a hung endpoint.
const usageWarmTimeout = 10 * time.Second

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

	// attempted is the backoff clock for BACKGROUND warms only — it moves on
	// every warm, and on every successful fetch from either path. fetched moves
	// only on success, so a failing endpoint would otherwise be re-fetched by
	// every passive read forever. An explicit RefreshUsage deliberately ignores
	// this: a user opening /usage after a failure is asking to try again now.
	attempted time.Time

	// warming is the single-flight guard for the background warm. Several
	// readers can land between one warm starting and its result arriving; only
	// the first should spend a request.
	warming bool
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
//
// It also WARMS the cache in the background when it is stale, and that is the
// difference between a poll-family provider's meters moving on their own and
// standing still. Providers report usage two ways. A header-family client
// (anthropic, codex, openai-compatible) records its windows off every inference
// response, so this passive read is kept warm as a side effect of talking to
// the model. A poll-family client reports nothing until somebody calls its
// endpoint — and the only caller was an explicit refresh, i.e. a user opening
// /usage. Between those, every passive read returned the same frozen numbers:
// the status bar, the panel's meters and the context breakdown all showed
// whatever the last /usage happened to fetch, indefinitely.
//
// Warming here rather than at each caller is deliberate. The staleness belongs
// to this client — it is the thing that knows its numbers come from a GET and
// when that GET last ran — and every host reads usage through this method, so
// fixing it once covers the ones that never knew the distinction existed.
//
// The read stays non-blocking and returns what is cached right now; the fetch
// lands for the NEXT read. Cost is bounded twice over: single-flighted, and no
// more than one request per ttl even when the endpoint is failing.
func (c *pollingUsageClient) UsageSnapshot() (UsageSnapshot, bool) {
	// Read first, THEN warm. Warming first lets the fetch land between the two
	// statements, so the same call could return either the old numbers or the
	// new ones depending on how a goroutine was scheduled — an answer that
	// varies for no reason the caller can see, and a flake in anything
	// asserting on the pre-warm value.
	credits, have := c.cachedCredits()
	inner, innerOK := ClientUsage(c.inner)
	c.warmIfStale()
	return mergeUsage(credits, have, inner, innerOK)
}

// warmIfStale kicks a background fetch when the cache is older than ttl and no
// warm is already running. Returns immediately in every case.
func (c *pollingUsageClient) warmIfStale() {
	c.mu.Lock()
	if c.warming || (!c.attempted.IsZero() && time.Since(c.attempted) < c.ttl) {
		c.mu.Unlock()
		return
	}
	c.warming = true
	c.attempted = time.Now()
	c.mu.Unlock()

	go func() {
		// Its own context: nothing is waiting on this, so it cannot inherit a
		// caller's deadline, and it must not outlive a hung endpoint either.
		ctx, cancel := context.WithTimeout(context.Background(), usageWarmTimeout)
		defer cancel()
		defer func() {
			c.mu.Lock()
			c.warming = false
			c.mu.Unlock()
		}()
		c.fetchCredits(ctx)
	}()
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
		// fetched deliberately does NOT move: an explicit refresh should retry
		// on the next /usage rather than serve a stale number for a full ttl.
		// The background warm has its own clock (attempted) precisely because
		// it cannot afford that — see warmIfStale.
		return c.snap, c.have
	}
	c.snap = snap
	c.have = true
	c.fetched = time.Now()
	c.attempted = c.fetched
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

// kimiUsageDetail is one window's counters. Kimi Code reports every number as a
// STRING ("100") and every reset as RFC3339; `used` is present on the aggregate
// but omitted on sub-windows (derive it from limit-remaining there).
type kimiUsageDetail struct {
	Limit     string `json:"limit"`
	Used      string `json:"used"`
	Remaining string `json:"remaining"`
	ResetTime string `json:"resetTime"`
}

// kimiUsageResponse mirrors GET {base}/v1/usages. `usage` is the long-horizon
// (weekly) aggregate with no window duration; each `limits[]` entry is a shorter
// window (e.g. duration:300 timeUnit:TIME_UNIT_MINUTE = the 5h rolling window)
// carrying its own duration. See docs — usage is body-only, never in headers.
type kimiUsageResponse struct {
	Usage  kimiUsageDetail `json:"usage"`
	Limits []struct {
		Window struct {
			Duration int    `json:"duration"`
			TimeUnit string `json:"timeUnit"`
		} `json:"window"`
		Detail kimiUsageDetail `json:"detail"`
	} `json:"limits"`
}

// parseKimiUsage maps a /v1/usages body into subscription-plan windows. Pure (no
// I/O) so it is table-testable, mirroring parseCodexUsageHeaders. Windows are
// WindowPlan (a subscription budget, like Codex's 5h/weekly) — NOT WindowRateLimit,
// which is reserved for ephemeral RPM/TPM windows off x-ratelimit-* headers.
func parseKimiUsage(body []byte) (UsageSnapshot, bool) {
	var r kimiUsageResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return UsageSnapshot{}, false
	}
	var windows []UsageWindow
	// The top-level aggregate carries no duration, so label it from the
	// fallback; the finer limits[] windows each carry an explicit duration.
	if w, ok := kimiWindow(r.Usage, 0, "weekly"); ok {
		windows = append(windows, w)
	}
	for _, l := range r.Limits {
		mins := kimiWindowMinutes(l.Window.Duration, l.Window.TimeUnit)
		if w, ok := kimiWindow(l.Detail, mins, "limit"); ok {
			windows = append(windows, w)
		}
	}
	if len(windows) == 0 {
		return UsageSnapshot{}, false
	}
	return UsageSnapshot{Provider: "kimi", Windows: windows, CapturedAt: time.Now()}, true
}

// kimiWindow builds one UsageWindow from a detail block; ok=false when it names
// no usable limit. Prefers an explicit `used`, else derives it from remaining.
func kimiWindow(d kimiUsageDetail, minutes int, fallbackLabel string) (UsageWindow, bool) {
	limit := parseKimiNum(d.Limit)
	if limit <= 0 {
		return UsageWindow{}, false
	}
	used := parseKimiNum(d.Used)
	if strings.TrimSpace(d.Used) == "" && strings.TrimSpace(d.Remaining) != "" {
		used = limit - parseKimiNum(d.Remaining)
	}
	pct := used / limit * 100
	switch {
	case pct < 0:
		pct = 0
	case pct > 100:
		pct = 100
	}
	return UsageWindow{
		Label:         windowLabel(minutes, fallbackLabel),
		UsedPercent:   pct,
		WindowMinutes: minutes,
		ResetsAt:      parseCodexResetAt(d.ResetTime),
		Kind:          WindowPlan,
	}, true
}

// kimiWindowMinutes converts a {duration,timeUnit} window into minutes. Accepts
// both the enum ("TIME_UNIT_MINUTE") and the bare unit ("MINUTE"); 0 when unknown.
func kimiWindowMinutes(duration int, unit string) int {
	switch strings.ToUpper(strings.TrimPrefix(strings.ToUpper(unit), "TIME_UNIT_")) {
	case "MINUTE":
		return duration
	case "HOUR":
		return duration * 60
	case "DAY":
		return duration * 1440
	case "WEEK":
		return duration * 10080
	case "MONTH":
		return duration * 43200
	}
	return 0
}

func parseKimiNum(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

// fetchKimiUsage returns a usageFetcher for Kimi Code's GET {base}/v1/usages.
// Unlike the OpenRouter/DeepSeek fetchers (static API keys), Kimi's credential
// is a rotating OAuth token, so it is resolved through cred on EVERY call — a
// captured token would go stale on the next refresh. Auth uses x-api-key (the
// same header the inference path sends) plus Kimi Code's X-Msh-* identity headers.
func fetchKimiUsage(httpc *http.Client, cred CredentialSource, baseURL string, headers map[string]string) usageFetcher {
	usagesURL := strings.TrimRight(baseURL, "/") + "/v1/usages"
	return func(ctx context.Context) (UsageSnapshot, error) {
		token, err := cred(ctx)
		if err != nil {
			return UsageSnapshot{}, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, usagesURL, nil)
		if err != nil {
			return UsageSnapshot{}, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("x-api-key", token)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := httpc.Do(req)
		if err != nil {
			return UsageSnapshot{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			return UsageSnapshot{}, fmt.Errorf("kimi usage GET %s: status %d: %s", usagesURL, resp.StatusCode, strings.TrimSpace(string(b)))
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		if err != nil {
			return UsageSnapshot{}, err
		}
		snap, ok := parseKimiUsage(body)
		if !ok {
			return UsageSnapshot{}, fmt.Errorf("kimi usage %s: no windows in response", usagesURL)
		}
		return snap, nil
	}
}
