package provider

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// resetFormat is how a provider encodes "time until reset" in its
// x-ratelimit-reset-* headers — it varies across OpenAI-compatible providers.
type resetFormat int

const (
	resetDuration resetFormat = iota // Go duration string: "2m59.56s","7.66s" (OpenAI, Groq)
	resetSeconds                     // plain (fractional) seconds: "12.5" (Cerebras)
	resetUnix                        // epoch seconds at which the window resets
	resetRFC3339                     // absolute RFC3339 timestamp
)

// rateLimitSpec describes how to read one provider's x-ratelimit-* response
// headers. Header names default to the OpenAI standard; providers that deviate
// (Cerebras) override them, and `disabled` turns parsing off for a provider
// that sends junk. See docs/plans/usage-merged-view.md §4.
type rateLimitSpec struct {
	requestsLimit, requestsRemaining, requestsReset string
	tokensLimit, tokensRemaining, tokensReset       string
	resetFormat                                     resetFormat
	requestsLabel, tokensLabel                      string
	disabled                                        bool
}

// standardRateLimitSpec is the OpenAI standard, applied to any openai-compat
// provider without an override (default-on). Groq matches it exactly.
var standardRateLimitSpec = rateLimitSpec{
	requestsLimit: "x-ratelimit-limit-requests", requestsRemaining: "x-ratelimit-remaining-requests", requestsReset: "x-ratelimit-reset-requests",
	tokensLimit: "x-ratelimit-limit-tokens", tokensRemaining: "x-ratelimit-remaining-tokens", tokensReset: "x-ratelimit-reset-tokens",
	resetFormat:   resetDuration,
	requestsLabel: "requests",
	tokensLabel:   "tokens",
}

// rateLimitSpecs holds per-provider overrides; providers absent from the map
// ride the standard spec. Verified June 2026 (usage-merged-view.md §8).
var rateLimitSpecs = map[string]rateLimitSpec{
	// Cerebras bakes the window into the header name and reports reset as plain
	// seconds (token-bucket continuous refill, so reset is approximate).
	"cerebras": {
		requestsLimit: "x-ratelimit-limit-requests-day", requestsRemaining: "x-ratelimit-remaining-requests-day", requestsReset: "x-ratelimit-reset-requests-day",
		tokensLimit: "x-ratelimit-limit-tokens-minute", tokensRemaining: "x-ratelimit-remaining-tokens-minute", tokensReset: "x-ratelimit-reset-tokens-minute",
		resetFormat:   resetSeconds,
		requestsLabel: "requests/day",
		tokensLabel:   "tokens/min",
	},
}

// rateLimitSpecFor returns the rate-limit header spec for a provider — the
// standard spec by default (default-on) — with ok=false when parsing is
// disabled for it.
func rateLimitSpecFor(provider string) (rateLimitSpec, bool) {
	spec, ok := rateLimitSpecs[provider]
	if !ok {
		spec = standardRateLimitSpec
	}
	if spec.disabled {
		return rateLimitSpec{}, false
	}
	if spec.requestsLabel == "" {
		spec.requestsLabel = "requests"
	}
	if spec.tokensLabel == "" {
		spec.tokensLabel = "tokens"
	}
	return spec, true
}

// parseRateLimitHeaders reads the requests/tokens rate-limit windows from h per
// spec. ok=false when neither window's headers are present, so a provider that
// doesn't send them reports no rate-limit usage. Pure + table-testable, like
// parseCodexUsageHeaders.
func parseRateLimitHeaders(h http.Header, spec rateLimitSpec) (UsageSnapshot, bool) {
	now := time.Now()
	var windows []UsageWindow
	addWindow := func(limitH, remH, resetH, label string) {
		limitStr, remStr := h.Get(limitH), h.Get(remH)
		if limitStr == "" || remStr == "" {
			return
		}
		limit, err1 := strconv.ParseFloat(strings.TrimSpace(limitStr), 64)
		rem, err2 := strconv.ParseFloat(strings.TrimSpace(remStr), 64)
		if err1 != nil || err2 != nil || limit <= 0 {
			return
		}
		pct := (limit - rem) / limit * 100
		switch {
		case pct < 0:
			pct = 0
		case pct > 100:
			pct = 100
		}
		w := UsageWindow{Label: label, UsedPercent: pct, Kind: WindowRateLimit}
		if d, ok := parseResetValue(h.Get(resetH), spec.resetFormat); ok {
			w.ResetsAt = now.Add(d)
		}
		windows = append(windows, w)
	}
	addWindow(spec.requestsLimit, spec.requestsRemaining, spec.requestsReset, spec.requestsLabel)
	addWindow(spec.tokensLimit, spec.tokensRemaining, spec.tokensReset, spec.tokensLabel)
	if len(windows) == 0 {
		return UsageSnapshot{}, false
	}
	return UsageSnapshot{Windows: windows, CapturedAt: now}, true
}

// parseResetValue converts a reset header value into a duration-until-reset.
func parseResetValue(v string, f resetFormat) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	switch f {
	case resetDuration:
		if d, err := time.ParseDuration(v); err == nil {
			return d, true
		}
	case resetSeconds:
		if s, err := strconv.ParseFloat(v, 64); err == nil {
			return time.Duration(s * float64(time.Second)), true
		}
	case resetUnix:
		if s, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.Until(time.Unix(s, 0)), true
		}
	case resetRFC3339:
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return time.Until(t), true
		}
	}
	return 0, false
}
