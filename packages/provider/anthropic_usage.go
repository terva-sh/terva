package provider

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Anthropic's subscription usage rides on every successful /v1/messages
// response as anthropic-ratelimit-unified-* headers. /usage said this provider
// reported nothing, which was never a policy decision — the client simply
// implemented no UsageReporter and read exactly one response header
// (Retry-After, on the error path).
//
// The shape is NOT the limit/remaining pair parseRateLimitHeaders is built
// around, which is why this cannot be one more entry in rateLimitSpecs:
// Anthropic sends no absolute numbers at all. It sends a UTILIZATION FRACTION
// per window, plus a unix reset and a status:
//
//	anthropic-ratelimit-unified-5h-utilization        0.06
//	anthropic-ratelimit-unified-5h-reset              1785657600
//	anthropic-ratelimit-unified-5h-status             allowed
//	anthropic-ratelimit-unified-7d-utilization        0.54
//	anthropic-ratelimit-unified-overage-utilization   0.0
//	anthropic-ratelimit-unified-representative-claim  five_hour
//
// Captured from a live OAuth request rather than assumed, because the whole
// point of the header is a number you cannot derive any other way.
const (
	anthropicUsagePrefix = "anthropic-ratelimit-unified-"

	// anthropicFiveHourMinutes / anthropicSevenDayMinutes are the window
	// lengths the header NAMES encode — Anthropic sends no duration field, so
	// unlike Codex these cannot be read off the response. They exist so
	// windowLabel renders "5h"/"weekly" from the same helper every other
	// provider uses, instead of this file inventing its own labels.
	anthropicFiveHourMinutes = 5 * 60
	anthropicSevenDayMinutes = 7 * 24 * 60
)

// parseAnthropicUsageHeaders reads the unified subscription windows. ok=false
// when none are present, which is the honest answer for an API-key account:
// these headers ride the subscription route, and a key without one gets no
// windows rather than windows reading zero.
func parseAnthropicUsageHeaders(h http.Header) (UsageSnapshot, bool) {
	var snap UsageSnapshot
	if w, ok := parseAnthropicWindow(h, "5h", anthropicFiveHourMinutes); ok {
		snap.Windows = append(snap.Windows, w)
	}
	if w, ok := parseAnthropicWindow(h, "7d", anthropicSevenDayMinutes); ok {
		snap.Windows = append(snap.Windows, w)
	}
	// Overage is reported on every response whether or not the account has any,
	// so an untouched one is "you have no overage" rather than a limit being
	// consumed — a third bar pinned at 0% next to two real ones reads as a
	// budget you are near the start of, which is the opposite of true. It
	// appears once it is carrying something, or once it stops saying allowed.
	if w, ok := parseAnthropicWindow(h, "overage", 0); ok && (w.UsedPercent > 0 || !anthropicStatusAllowed(h, "overage")) {
		snap.Windows = append(snap.Windows, w)
	}
	return snap, len(snap.Windows) > 0
}

// parseAnthropicWindow reads one window. minutes is the length the header name
// implies; 0 leaves it unknown and windowLabel falls back to the key.
func parseAnthropicWindow(h http.Header, key string, minutes int) (UsageWindow, bool) {
	util := strings.TrimSpace(h.Get(anthropicUsagePrefix + key + "-utilization"))
	reset := strings.TrimSpace(h.Get(anthropicUsagePrefix + key + "-reset"))
	status := strings.TrimSpace(h.Get(anthropicUsagePrefix + key + "-status"))
	if util == "" && reset == "" && status == "" {
		return UsageWindow{}, false
	}

	// UsedPercent starts negative: "reported but unparseable" must read as
	// unknown, never as a confident 0%.
	w := UsageWindow{UsedPercent: -1, WindowMinutes: minutes, Kind: WindowPlan}
	if v, err := strconv.ParseFloat(util, 64); err == nil {
		// The header is a FRACTION (0.06 = 6%), while UsageWindow.UsedPercent is
		// 0..100 like every other provider's. Getting this backwards would
		// render a nearly-exhausted week as half a percent.
		w.UsedPercent = v * 100
	}
	if secs, err := strconv.ParseInt(reset, 10, 64); err == nil && secs > 0 {
		w.ResetsAt = time.Unix(secs, 0)
	}
	w.Label = windowLabel(minutes, key)
	return w, true
}

// anthropicStatusAllowed reports whether the window's status is the ordinary
// "allowed". Anything else — and an ABSENT status, which cannot be read as
// permission — counts as not allowed, so a window in an unexpected state
// surfaces rather than being filtered out for looking empty.
func anthropicStatusAllowed(h http.Header, key string) bool {
	return strings.EqualFold(strings.TrimSpace(h.Get(anthropicUsagePrefix+key+"-status")), "allowed")
}
