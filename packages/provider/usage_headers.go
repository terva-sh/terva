package provider

import (
	"net/http"
	"time"
)

// parseUsageHeaders reads every usage signal terva knows how to read off one
// response, whichever wire delivered it, and merges them into a snapshot
// stamped for provider. ok=false when the response carried none of them.
//
// The three header families are the three vendors' native ones:
//
//	anthropic-ratelimit-unified-*   Claude subscription windows (5h / weekly / overage)
//	x-codex-*                       ChatGPT subscription windows and credits
//	x-ratelimit-*                   OpenAI-style throughput windows (RPM / TPM)
//
// Until this, each family was tied to the client that first met it: the
// Anthropic client read only the first, the OpenAI client only the last, and
// only the codex client — pinned to chatgpt.com — read the middle one. That
// held while every backend was the vendor itself. It stops holding behind a
// gateway that fronts subscription credentials (CLIProxyAPI, LiteLLM, a
// corporate router): the gateway forwards the UPSTREAM vendor's headers
// untouched, and which vendor answered depends on the model, not on the wire
// terva spoke. A Codex-backed model served over Chat Completions arrives with
// x-codex-*; a Claude-backed one over the same wire arrives with the unified
// set. Neither carries x-ratelimit-*, so the OpenAI-compatible client read
// nothing and /usage said the endpoint reported no limits while the numbers
// rode every response.
//
// The plan windows (subscription) come first and rate-limit windows last;
// mergeUsage keeps that order so the status bar shows the budget, not the
// throughput.
func parseUsageHeaders(h http.Header, provider string) (UsageSnapshot, bool) {
	anth, anthOK := parseAnthropicUsageHeaders(h)
	codex, codexOK := parseCodexUsageHeaders(h)
	plan, planOK := mergeUsage(anth, anthOK, codex, codexOK)

	var rl UsageSnapshot
	rlOK := false
	if spec, ok := rateLimitSpecFor(provider); ok {
		rl, rlOK = parseRateLimitHeaders(h, spec)
	}

	snap, ok := mergeUsage(plan, planOK, rl, rlOK)
	if !ok {
		return UsageSnapshot{}, false
	}
	snap.Provider = provider
	snap.CapturedAt = time.Now()
	return snap, true
}
