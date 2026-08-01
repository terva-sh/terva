package auth

import (
	"testing"
	"time"
)

// The refresh window is a fraction of the token's LIFE, not a fixed margin, so
// an 8-hour subscription token and a 10-day one get lead times proportional to
// how fast they are actually burning down.
func TestStaleForScalesWithLifetime(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name     string
		lifetime time.Duration
		// remaining life at which the token should first come due
		wantLead time.Duration
	}{
		{"8h subscription token", 8 * time.Hour, 96 * time.Minute}, // 20%
		{"10d token", 240 * time.Hour, 48 * time.Hour},             // 20%
		{"30m token", 30 * time.Minute, refreshFloor},              // floor wins over 20%
	} {
		t.Run(tc.name, func(t *testing.T) {
			tok := &OAuthToken{
				AccessToken: "a",
				IssuedAt:    now.Add(-tc.lifetime / 2),
				Expiry:      now.Add(tc.lifetime / 2),
			}
			// Reconstruct so the full lifetime is what we asked for.
			tok.IssuedAt = tok.Expiry.Add(-tc.lifetime)

			justBefore := tok.Expiry.Add(-tc.wantLead - time.Minute)
			if _, due := tok.StaleFor(justBefore); due {
				t.Errorf("due %v before expiry; window should open at %v", tok.Expiry.Sub(justBefore), tc.wantLead)
			}
			justAfter := tok.Expiry.Add(-tc.wantLead + time.Minute)
			if _, due := tok.StaleFor(justAfter); !due {
				t.Errorf("not due %v before expiry; window should have opened at %v", tok.Expiry.Sub(justAfter), tc.wantLead)
			}
		})
	}
}

// A token written before IssuedAt existed has no lifetime to take a fraction
// of. It must still get a usable margin rather than reverting to the old
// last-60-seconds behaviour or, worse, refreshing constantly.
func TestStaleForWithoutIssuedAtUsesFallback(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	tok := &OAuthToken{AccessToken: "a", Expiry: now.Add(refreshFallback + time.Minute)}
	if _, due := tok.StaleFor(now); due {
		t.Error("due while more than the fallback window remains")
	}
	tok.Expiry = now.Add(refreshFallback - time.Minute)
	if _, due := tok.StaleFor(now); !due {
		t.Error("not due inside the fallback window")
	}
}

// A token whose whole life is shorter than the floor would be due the instant
// it is minted — which is a refresh loop, not a policy.
func TestStaleForVeryShortTokenIsNotBornStale(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	tok := &OAuthToken{
		AccessToken: "a",
		IssuedAt:    now,
		Expiry:      now.Add(2 * time.Minute), // far under refreshFloor
	}
	if _, due := tok.StaleFor(now); due {
		t.Error("a freshly minted token is already due for refresh — this loops")
	}
	if _, due := tok.StaleFor(now.Add(2 * time.Minute)); !due {
		t.Error("never came due before expiry")
	}
}

// A token with no expiry at all never comes due: nothing is known about when it
// stops working, and refreshing on a guess would spend a grant for nothing.
func TestStaleForZeroExpiryNeverDue(t *testing.T) {
	if _, due := (&OAuthToken{AccessToken: "a"}).StaleFor(time.Now()); due {
		t.Error("a token with no expiry came due")
	}
}

// The refresh window must open BEFORE the token stops working — that gap is
// what a failed attempt has left to retry in. A window that opened at or after
// expiry would make every retry a race against a token that is already dead.
func TestRefreshWindowOpensBeforeExpiry(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	for _, lifetime := range []time.Duration{5 * time.Minute, time.Hour, 8 * time.Hour, 240 * time.Hour} {
		tok := &OAuthToken{AccessToken: "a", IssuedAt: now, Expiry: now.Add(lifetime)}
		var due time.Time
		for at := now; at.Before(tok.Expiry.Add(time.Minute)); at = at.Add(15 * time.Second) {
			if _, ok := tok.StaleFor(at); ok {
				due = at
				break
			}
		}
		if due.IsZero() {
			t.Errorf("lifetime %v: never came due", lifetime)
			continue
		}
		if !due.Before(tok.Expiry) {
			t.Errorf("lifetime %v: came due at expiry or later (%v vs %v) — no room to retry", lifetime, due, tok.Expiry)
		}
	}
}

// knownLifetimes are MEASURED, not assumed. The first cut of the refresh window
// was sized by extrapolating from anthropic and openai and got kimi wrong by a
// factor of thirty — a 15-minute token treated as if it had eight hours, which
// made it due at the instant it was minted.
//
// Anyone re-tuning these constants should add the number they measured here
// rather than reasoning about what a token "probably" lives.
var knownLifetimes = map[string]time.Duration{
	"kimi":      15 * time.Minute, // Kimi Code subscription, measured 2026-07-30
	"anthropic": 8 * time.Hour,
	"openai":    240 * time.Hour,
}

// No token may be born stale. A window that reaches back past the mint instant
// means every sweep refreshes, forever — a loop, not a policy — and on a
// provider that rotates refresh tokens it is a loop that spends a grant each
// time round.
func TestNoKnownTokenIsBornStale(t *testing.T) {
	mint := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	for name, lifetime := range knownLifetimes {
		t.Run(name, func(t *testing.T) {
			withIssued := &OAuthToken{AccessToken: "a", IssuedAt: mint, Expiry: mint.Add(lifetime)}
			if _, due := withIssued.StaleFor(mint); due {
				t.Errorf("due at the instant it was minted (lifetime %v)", lifetime)
			}
			// And the same token as terva 0.129.0 wrote it — no issued_at at all,
			// which is every token on disk before that field shipped. This is the
			// case that actually bit: the fallback window was wider than kimi's
			// whole life.
			noIssued := &OAuthToken{AccessToken: "a", Expiry: mint.Add(lifetime)}
			if _, due := noIssued.StaleFor(mint); due {
				t.Errorf("due at mint with issued_at absent (lifetime %v) — every pre-IssuedAt token takes this path", lifetime)
			}
		})
	}
}

// The window is a retry margin, never a claim on most of the token. A floor
// that outgrows the token it guards leaves almost no usable life and comes due
// on every sweep.
func TestWindowNeverExceedsAThirdOfTheLifetime(t *testing.T) {
	mint := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	for _, lifetime := range []time.Duration{
		2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute,
		time.Hour, 8 * time.Hour, 24 * time.Hour, 240 * time.Hour,
	} {
		tok := &OAuthToken{AccessToken: "a", IssuedAt: mint, Expiry: mint.Add(lifetime)}
		window := windowOf(t, tok, mint)
		if window > lifetime/3 {
			t.Errorf("lifetime %v: window %v exceeds a third of the token", lifetime, window)
		}
		if window <= 0 {
			t.Errorf("lifetime %v: no refresh margin at all", lifetime)
		}
	}
}

// windowOf recovers how long before expiry the token comes due, by scanning.
// Reading the constants back would only re-assert the arithmetic; this measures
// the behaviour a caller actually gets.
func windowOf(t *testing.T, tok *OAuthToken, from time.Time) time.Duration {
	t.Helper()
	step := time.Second
	for at := from; !at.After(tok.Expiry); at = at.Add(step) {
		if _, due := tok.StaleFor(at); due {
			return tok.Expiry.Sub(at)
		}
	}
	t.Fatalf("token never came due before expiry (issued %v, expires %v)", tok.IssuedAt, tok.Expiry)
	return 0
}

// Kimi's is the shortest lifetime terva knows, and the one that broke the first
// cut. Pin the actual numbers so a future re-tune has to face them.
func TestKimiFifteenMinuteTokenGetsUsableLifeAndRetryRoom(t *testing.T) {
	mint := time.Date(2026, 7, 30, 19, 22, 27, 0, time.UTC)
	tok := &OAuthToken{AccessToken: "a", IssuedAt: mint, Expiry: mint.Add(15 * time.Minute)}

	window := windowOf(t, tok, mint)
	if window < 3*time.Minute {
		t.Errorf("window %v leaves too little room to retry a failed refresh", window)
	}
	if usable := 15*time.Minute - window; usable < 8*time.Minute {
		t.Errorf("only %v of a 15-minute token is used before it comes due", usable)
	}
}
