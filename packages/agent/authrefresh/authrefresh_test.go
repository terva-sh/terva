package authrefresh

import (
	"errors"
	"testing"
	"time"

	"terva.sh/terva/packages/provider/auth"
)

func stale() *auth.OAuthToken {
	now := time.Now()
	return &auth.OAuthToken{
		AccessToken:  "a",
		RefreshToken: "r",
		IssuedAt:     now.Add(-8 * time.Hour),
		Expiry:       now.Add(5 * time.Minute),
	}
}

// A revoked grant is retried zero times and reported exactly once. Retrying it
// on a timer cannot succeed, and every attempt lands on the same auth server
// the user's next /login has to reach.
func TestTerminalFailureStopsRetryingAndReportsOnce(t *testing.T) {
	var reports int
	r := New(func(string, error) { reports++ })
	tok := stale()
	revoked := &auth.TokenError{Status: 400, Code: "invalid_grant"}

	r.record("kimi", tok, revoked)
	if reports != 1 {
		t.Fatalf("reports = %d after the first refusal, want 1", reports)
	}
	if r.mayAttempt("kimi", tok, time.Now()) {
		t.Error("would retry a revoked grant")
	}

	// A second refusal for the same grant must not re-report: inform, not nag.
	r.record("kimi", tok, revoked)
	if reports != 1 {
		t.Errorf("reports = %d, want 1 — the same dead grant reported twice", reports)
	}
}

// A fresh /login replaces the grant, and the verdict on the OLD one must not
// outlive it. Keying the write-off by token expiry is what makes that work.
func TestNewGrantAfterLoginIsTriedAgain(t *testing.T) {
	r := New(nil)
	dead := stale()
	r.record("kimi", dead, &auth.TokenError{Status: 400, Code: "invalid_grant"})
	if r.mayAttempt("kimi", dead, time.Now()) {
		t.Fatal("would retry the dead grant")
	}

	replacement := stale()
	replacement.Expiry = replacement.Expiry.Add(time.Hour) // a different grant
	if !r.mayAttempt("kimi", replacement, time.Now()) {
		t.Error("a grant from a fresh /login inherited the previous one's verdict")
	}
}

// A transient failure backs off and comes back. Treating it as terminal would
// turn a provider having a bad ten minutes into a re-login.
func TestTransientFailureBacksOffThenRetries(t *testing.T) {
	r := New(func(string, error) { t.Error("a transient failure was reported as a lapsed login") })
	tok := stale()

	r.record("kimi", tok, &auth.TokenError{Status: 503, Description: "unavailable"})
	if r.mayAttempt("kimi", tok, time.Now()) {
		t.Error("retried immediately after a transient failure")
	}
	if r.mayAttempt("kimi", tok, time.Now().Add(retryCeil+time.Minute)) == false {
		t.Error("never came back after the backoff elapsed")
	}
}

// Backoff grows, and stops growing. An unbounded curve would eventually park
// the next attempt past the token's own expiry.
func TestBackoffIsBoundedByTheCeiling(t *testing.T) {
	r := New(nil)
	tok := stale()
	for range 20 {
		r.record("kimi", tok, errors.New("network is unreachable"))
	}
	r.mu.Lock()
	until := r.backoff["kimi"]
	r.mu.Unlock()
	// jitter can add 20%.
	if maxWait := time.Until(until); maxWait > time.Duration(float64(retryCeil)*1.2)+time.Second {
		t.Errorf("backoff grew to %v, past the %v ceiling", maxWait, retryCeil)
	}
}

// Success clears everything, including a previous write-off — the grant is
// demonstrably alive.
func TestSuccessClearsBackoffAndLapse(t *testing.T) {
	r := New(nil)
	tok := stale()
	r.record("kimi", tok, &auth.TokenError{Status: 400, Code: "invalid_grant"})
	r.record("kimi", tok, nil)
	if !r.mayAttempt("kimi", tok, time.Now()) {
		t.Error("a provider that just refreshed successfully is still blocked")
	}
}

// A token with nothing to present is terminal without a network round trip.
func TestMissingRefreshTokenIsTerminal(t *testing.T) {
	var reported bool
	r := New(func(string, error) { reported = true })
	tok := stale()
	tok.RefreshToken = ""
	r.record("kimi", tok, errors.New("no refresh_token available"))
	if !reported {
		t.Error("a grant with no refresh token was not reported as lapsed")
	}
	if r.mayAttempt("kimi", tok, time.Now()) {
		t.Error("would retry a grant with nothing to present")
	}
}

// Jitter must stay inside its stated band: too wide and a tick lands outside
// the window it was sized for.
func TestJitterStaysWithinBand(t *testing.T) {
	base := 5 * time.Minute
	lo, hi := time.Duration(float64(base)*0.8), time.Duration(float64(base)*1.2)
	sawLow, sawHigh := false, false
	for range 500 {
		d := jitter(base)
		if d < lo || d > hi {
			t.Fatalf("jitter(%v) = %v, outside [%v, %v]", base, d, lo, hi)
		}
		if d < base {
			sawLow = true
		}
		if d > base {
			sawHigh = true
		}
	}
	if !sawLow || !sawHigh {
		t.Error("jitter never varied in both directions — a fixed offset keeps a herd in lockstep forever")
	}
}

// The tick and the refresh window are two constants in two packages that only
// work if they are sized against each other.
//
// The window says when a token is DUE. The tick says how late we can be in
// noticing. If the worst-case gap between sweeps can exceed the window, a token
// comes due and expires without any sweep ever having looked at it — which is
// exactly what the first cut did to kimi: a 15-minute token due with 5 minutes
// left, against a 5-minute tick whose jitter could land the next sweep at 16.
//
// Neither constant can be raised alone without this failing, which is the point.
func TestTheTickCannotOutrunTheRefreshWindow(t *testing.T) {
	// Worst case: a token comes due one instant after a sweep, so it waits a
	// full jittered interval. jitter tops out at +20%.
	worstLatency := time.Duration(float64(tickInterval) * 1.2)

	mint := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	for name, lifetime := range map[string]time.Duration{
		"kimi":      15 * time.Minute, // the shortest terva knows; measured 2026-07-30
		"anthropic": 8 * time.Hour,
		"openai":    240 * time.Hour,
	} {
		t.Run(name, func(t *testing.T) {
			tok := &auth.OAuthToken{AccessToken: "a", IssuedAt: mint, Expiry: mint.Add(lifetime)}
			var window time.Duration
			for at := mint; !at.After(tok.Expiry); at = at.Add(time.Second) {
				if _, due := tok.StaleFor(at); due {
					window = tok.Expiry.Sub(at)
					break
				}
			}
			if window == 0 {
				t.Fatalf("%s token never came due", name)
			}
			if worstLatency >= window {
				t.Errorf("a sweep can be %v late but %s's token is only due %v before expiry — "+
					"it can expire without a sweep ever having looked at it.\n"+
					"  Shorten tickInterval, or widen the refresh window for short tokens.",
					worstLatency, name, window)
			}
		})
	}
}

// A token with issued_at absent — every token written before that field
// shipped — must also survive one tick. This is the path terva 0.129.0's tokens
// take on first contact with a build that has the refresher.
func TestTheTickCannotOutrunTheFallbackWindow(t *testing.T) {
	worstLatency := time.Duration(float64(tickInterval) * 1.2)
	mint := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	tok := &auth.OAuthToken{AccessToken: "a", Expiry: mint.Add(15 * time.Minute)}

	var window time.Duration
	for at := mint; !at.After(tok.Expiry); at = at.Add(time.Second) {
		if _, due := tok.StaleFor(at); due {
			window = tok.Expiry.Sub(at)
			break
		}
	}
	if window == 0 {
		t.Fatal("a token with no issued_at never came due")
	}
	if worstLatency >= window {
		t.Errorf("a sweep can be %v late but the fallback window is only %v — a pre-IssuedAt token "+
			"can expire unseen on its first contact with this build", worstLatency, window)
	}
}
